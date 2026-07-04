package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type RateLimit struct {
	ID         string    `json:"id"`
	ScopeType  string    `json:"scope_type"`
	ScopeValue string    `json:"scope_value"`
	RPMLimit   int       `json:"rpm_limit"`
	TPMLimit   int       `json:"tpm_limit"`
	IsActive   bool      `json:"is_active"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type RateLimitWindowUsage struct {
	Requests    int64
	TotalTokens int64
}

func (s *Store) ListRateLimits(ctx context.Context) ([]RateLimit, error) {
	rows, err := s.query(ctx, `SELECT id, scope_type, scope_value, rpm_limit, tpm_limit, is_active, created_at, updated_at FROM rate_limits ORDER BY scope_type, scope_value`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var limits []RateLimit
	for rows.Next() {
		rl, err := scanRateLimit(rows)
		if err != nil {
			return nil, err
		}
		limits = append(limits, rl)
	}
	return limits, rows.Err()
}

func (s *Store) ApplicableRateLimits(ctx context.Context, u User, route RoutedModel) ([]RateLimit, error) {
	rows, err := s.query(ctx, `
		SELECT id, scope_type, scope_value, rpm_limit, tpm_limit, is_active, created_at, updated_at
		FROM rate_limits
		WHERE is_active = 1 AND (
			(scope_type = 'user' AND scope_value = ?) OR
			(scope_type = 'department' AND scope_value = ?) OR
			(scope_type = 'provider' AND scope_value = ?) OR
			(scope_type = 'model' AND scope_value = ?)
		)
		ORDER BY scope_type, scope_value`,
		u.ID, u.Department, route.Provider.ID, route.Model.Route)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var limits []RateLimit
	for rows.Next() {
		rl, err := scanRateLimit(rows)
		if err != nil {
			return nil, err
		}
		limits = append(limits, rl)
	}
	return limits, rows.Err()
}

func (s *Store) CreateRateLimit(ctx context.Context, rl RateLimit) error {
	now := time.Now().UTC()
	_, err := s.exec(ctx, `
		INSERT INTO rate_limits (id, scope_type, scope_value, rpm_limit, tpm_limit, is_active, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		rl.ID, rl.ScopeType, rl.ScopeValue, rl.RPMLimit, rl.TPMLimit, boolInt(rl.IsActive), formatTime(now), formatTime(now))
	if isUniqueErr(err) {
		return ErrConflict
	}
	return err
}

func (s *Store) UpdateRateLimit(ctx context.Context, rl RateLimit) error {
	now := time.Now().UTC()
	res, err := s.exec(ctx, `
		UPDATE rate_limits
		SET scope_type = ?, scope_value = ?, rpm_limit = ?, tpm_limit = ?, is_active = ?, updated_at = ?
		WHERE id = ?`,
		rl.ScopeType, rl.ScopeValue, rl.RPMLimit, rl.TPMLimit, boolInt(rl.IsActive), formatTime(now), rl.ID)
	if err != nil {
		if isUniqueErr(err) {
			return ErrConflict
		}
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteRateLimit(ctx context.Context, id string) error {
	res, err := s.exec(ctx, `DELETE FROM rate_limits WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) RateLimitWindowUsage(ctx context.Context, rl RateLimit, since time.Time) (RateLimitWindowUsage, error) {
	field, ok := rateLimitUsageField(rl.ScopeType)
	if !ok {
		return RateLimitWindowUsage{}, nil
	}
	var usage RateLimitWindowUsage
	err := s.queryRow(ctx, `SELECT COUNT(*), COALESCE(SUM(total_tokens), 0) FROM usage_ledger WHERE `+field+` = ? AND created_at >= ?`,
		rl.ScopeValue, formatTime(since)).Scan(&usage.Requests, &usage.TotalTokens)
	return usage, err
}

func scanRateLimit(row scanner) (RateLimit, error) {
	var rl RateLimit
	var active int
	var created, updated string
	err := row.Scan(&rl.ID, &rl.ScopeType, &rl.ScopeValue, &rl.RPMLimit, &rl.TPMLimit, &active, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return RateLimit{}, ErrNotFound
	}
	if err != nil {
		return RateLimit{}, err
	}
	rl.IsActive = active == 1
	rl.CreatedAt = parseTime(created)
	rl.UpdatedAt = parseTime(updated)
	return rl, nil
}

func rateLimitUsageField(scopeType string) (string, bool) {
	switch scopeType {
	case "user":
		return "user_id", true
	case "department":
		return "department", true
	case "provider":
		return "provider_id", true
	case "model":
		return "model", true
	default:
		return "", false
	}
}
