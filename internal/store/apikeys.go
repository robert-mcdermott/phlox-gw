package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type APIKey struct {
	ID             string     `json:"id"`
	UserID         string     `json:"user_id"`
	Name           string     `json:"name"`
	Prefix         string     `json:"prefix"`
	KeyHash        string     `json:"-"`
	IsActive       bool       `json:"is_active"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	BudgetUSD      float64    `json:"budget_usd"`
	RPMLimit       int        `json:"rpm_limit"`
	TPMLimit       int        `json:"tpm_limit"`
	ModelAllowlist string     `json:"model_allowlist"`
}

type AdminAPIKey struct {
	APIKey
	Username        string  `json:"username"`
	Department      string  `json:"department"`
	MonthlySpendUSD float64 `json:"monthly_spend_usd"`
}

type APIKeyWindowUsage struct {
	Requests    int64
	TotalTokens int64
}

func (s *Store) ListAPIKeys(ctx context.Context, userID string) ([]APIKey, error) {
	rows, err := s.query(ctx, `SELECT `+apiKeyColumns+` FROM api_keys WHERE user_id = ? ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []APIKey
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func (s *Store) ListAllAPIKeys(ctx context.Context) ([]AdminAPIKey, error) {
	start, end := monthBounds(time.Now().UTC())
	rows, err := s.query(ctx, `
		SELECT `+apiKeyColumnsAliased+`, u.username, u.department, COALESCE(spend.monthly_spend_usd, 0)
		FROM api_keys k
		JOIN users u ON u.id = k.user_id
		LEFT JOIN (
			SELECT api_key_id, SUM(cost_usd) AS monthly_spend_usd
			FROM usage_ledger
			WHERE created_at >= ? AND created_at < ?
			GROUP BY api_key_id
		) spend ON spend.api_key_id = k.id
		ORDER BY k.created_at DESC`, formatTime(start), formatTime(end))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []AdminAPIKey
	for rows.Next() {
		var item AdminAPIKey
		if err := scanAPIKeyWithOwner(rows, &item); err != nil {
			return nil, err
		}
		item.MonthlySpendUSD = roundCost(item.MonthlySpendUSD)
		keys = append(keys, item)
	}
	return keys, rows.Err()
}

func (s *Store) CreateAPIKey(ctx context.Context, k APIKey) error {
	now := time.Now().UTC()
	var expires any
	if k.ExpiresAt != nil {
		expires = formatTime(*k.ExpiresAt)
	}
	_, err := s.exec(ctx, `
		INSERT INTO api_keys (id, user_id, name, prefix, key_hash, is_active, expires_at, created_at, budget_usd, rpm_limit, tpm_limit, model_allowlist)
		VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?)`,
		k.ID, k.UserID, k.Name, k.Prefix, k.KeyHash, expires, formatTime(now), k.BudgetUSD, k.RPMLimit, k.TPMLimit, k.ModelAllowlist)
	if isUniqueErr(err) {
		return ErrConflict
	}
	return err
}

func (s *Store) UpdateAPIKeySelf(ctx context.Context, k APIKey) error {
	var expires any
	if k.ExpiresAt != nil {
		expires = formatTime(*k.ExpiresAt)
	}
	res, err := s.exec(ctx, `
		UPDATE api_keys
		SET name = ?, expires_at = ?
		WHERE id = ? AND user_id = ? AND is_active = 1`,
		k.Name, expires, k.ID, k.UserID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) RotateAPIKey(ctx context.Context, userID, keyID, prefix, hash string) error {
	res, err := s.exec(ctx, `
		UPDATE api_keys
		SET prefix = ?, key_hash = ?, last_used_at = NULL
		WHERE id = ? AND user_id = ? AND is_active = 1`,
		prefix, hash, keyID, userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) RotateAPIKeyAdmin(ctx context.Context, keyID, prefix, hash string) error {
	res, err := s.exec(ctx, `
		UPDATE api_keys
		SET prefix = ?, key_hash = ?, last_used_at = NULL
		WHERE id = ? AND is_active = 1`,
		prefix, hash, keyID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) RevokeAPIKey(ctx context.Context, userID, keyID string) error {
	res, err := s.exec(ctx, `UPDATE api_keys SET is_active = 0 WHERE id = ? AND user_id = ?`, keyID, userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) RevokeAPIKeyAdmin(ctx context.Context, keyID string) error {
	res, err := s.exec(ctx, `UPDATE api_keys SET is_active = 0 WHERE id = ?`, keyID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateAPIKeyControls(ctx context.Context, k APIKey) error {
	var expires any
	if k.ExpiresAt != nil {
		expires = formatTime(*k.ExpiresAt)
	}
	res, err := s.exec(ctx, `
		UPDATE api_keys
		SET name = ?, is_active = ?, expires_at = ?, budget_usd = ?, rpm_limit = ?, tpm_limit = ?, model_allowlist = ?
		WHERE id = ?`,
		k.Name, boolInt(k.IsActive), expires, k.BudgetUSD, k.RPMLimit, k.TPMLimit, normalizeList(k.ModelAllowlist), k.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ResolveAPIKey(ctx context.Context, hash string, now time.Time) (User, APIKey, error) {
	row := s.queryRow(ctx, `
		SELECT `+apiKeyColumnsAliased+`,
		       u.id, u.username, u.email, u.display_name, u.department, u.role, u.password_hash, u.auth_provider, u.is_active, u.created_at, u.updated_at, u.last_login_at
		FROM api_keys k
		JOIN users u ON u.id = k.user_id
		WHERE k.key_hash = ?`, hash)
	var k APIKey
	var u User
	if err := scanAPIKeyAndUser(row, &k, &u); err != nil {
		return User{}, APIKey{}, err
	}
	if !k.IsActive || !u.IsActive {
		return User{}, APIKey{}, ErrNotFound
	}
	if k.ExpiresAt != nil && !k.ExpiresAt.After(now) {
		return User{}, APIKey{}, ErrNotFound
	}
	_, _ = s.exec(ctx, `UPDATE api_keys SET last_used_at = ? WHERE id = ?`, formatTime(now), k.ID)
	return u, k, nil
}

func (s *Store) APIKeyMonthlySpend(ctx context.Context, keyID string, start, end time.Time) (float64, error) {
	var spend float64
	err := s.queryRow(ctx, `SELECT COALESCE(SUM(cost_usd), 0) FROM usage_ledger WHERE api_key_id = ? AND created_at >= ? AND created_at < ?`,
		keyID, formatTime(start), formatTime(end)).Scan(&spend)
	return roundCost(spend), err
}

func (s *Store) APIKeyWindowUsage(ctx context.Context, keyID string, since time.Time) (APIKeyWindowUsage, error) {
	var usage APIKeyWindowUsage
	err := s.queryRow(ctx, `SELECT COUNT(*), COALESCE(SUM(total_tokens), 0) FROM usage_ledger WHERE api_key_id = ? AND created_at >= ?`,
		keyID, formatTime(since)).Scan(&usage.Requests, &usage.TotalTokens)
	return usage, err
}

func scanAPIKey(row scanner) (APIKey, error) {
	var k APIKey
	var active int
	var expires, last sql.NullString
	var created string
	err := row.Scan(&k.ID, &k.UserID, &k.Name, &k.Prefix, &k.KeyHash, &active, &expires, &last, &created, &k.BudgetUSD, &k.RPMLimit, &k.TPMLimit, &k.ModelAllowlist)
	if errors.Is(err, sql.ErrNoRows) {
		return APIKey{}, ErrNotFound
	}
	if err != nil {
		return APIKey{}, err
	}
	k.IsActive = active == 1
	if expires.Valid {
		t := parseTime(expires.String)
		k.ExpiresAt = &t
	}
	if last.Valid {
		t := parseTime(last.String)
		k.LastUsedAt = &t
	}
	k.CreatedAt = parseTime(created)
	return k, nil
}

func scanAPIKeyWithOwner(row scanner, item *AdminAPIKey) error {
	var monthlySpend float64
	if err := scanAPIKeyAndExtras(row, &item.APIKey, &item.Username, &item.Department, &monthlySpend); err != nil {
		return err
	}
	item.MonthlySpendUSD = monthlySpend
	return nil
}

func scanAPIKeyAndExtras(row scanner, k *APIKey, extras ...any) error {
	var active int
	var expires, last sql.NullString
	var created string
	dest := []any{&k.ID, &k.UserID, &k.Name, &k.Prefix, &k.KeyHash, &active, &expires, &last, &created, &k.BudgetUSD, &k.RPMLimit, &k.TPMLimit, &k.ModelAllowlist}
	dest = append(dest, extras...)
	err := row.Scan(dest...)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	k.IsActive = active == 1
	if expires.Valid {
		t := parseTime(expires.String)
		k.ExpiresAt = &t
	}
	if last.Valid {
		t := parseTime(last.String)
		k.LastUsedAt = &t
	}
	k.CreatedAt = parseTime(created)
	return nil
}

func scanAPIKeyAndUser(row scanner, k *APIKey, u *User) error {
	var uActive int
	var uLast sql.NullString
	var uCreated, uUpdated string
	err := scanAPIKeyAndExtras(row, k,
		&u.ID, &u.Username, &u.Email, &u.DisplayName, &u.Department, &u.Role, &u.PasswordHash, &u.AuthProvider, &uActive, &uCreated, &uUpdated, &uLast)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	u.IsActive = uActive == 1
	u.CreatedAt = parseTime(uCreated)
	u.UpdatedAt = parseTime(uUpdated)
	if uLast.Valid {
		t := parseTime(uLast.String)
		u.LastLoginAt = &t
	}
	return nil
}

const apiKeyColumns = `id, user_id, name, prefix, key_hash, is_active, expires_at, last_used_at, created_at, budget_usd, rpm_limit, tpm_limit, model_allowlist`

const apiKeyColumnsAliased = `k.id, k.user_id, k.name, k.prefix, k.key_hash, k.is_active, k.expires_at, k.last_used_at, k.created_at, k.budget_usd, k.rpm_limit, k.tpm_limit, k.model_allowlist`
