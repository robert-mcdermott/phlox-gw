package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type Provider struct {
	ID                  string     `json:"id"`
	Name                string     `json:"name"`
	Type                string     `json:"type"`
	BaseURL             string     `json:"base_url"`
	APIKey              string     `json:"-"`
	APIKeyEnv           string     `json:"api_key_env"`
	AzureAPIVersion     string     `json:"azure_api_version"`
	BetaHeaderPrefixes  string     `json:"beta_header_prefixes"`
	AWSRegion           string     `json:"aws_region"`
	AWSAuthMethod       string     `json:"aws_auth_method"`
	AWSAccessKeyID      string     `json:"aws_access_key_id"`
	AWSSecretAccessKey  string     `json:"-"`
	AWSSessionToken     string     `json:"-"`
	BedrockAPIKey       string     `json:"-"`
	HasAWSSecret        bool       `json:"has_aws_secret"`
	HasBedrockAPIKey    bool       `json:"has_bedrock_api_key"`
	Enabled             bool       `json:"enabled"`
	HealthStatus        string     `json:"health_status"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	LastHealthCheckAt   *time.Time `json:"last_health_check_at,omitempty"`
	LastError           string     `json:"last_error"`
	CircuitOpenUntil    *time.Time `json:"circuit_open_until,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

func (s *Store) ListProviders(ctx context.Context) ([]Provider, error) {
	rows, err := s.query(ctx, `SELECT `+providerColumns+` FROM providers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var providers []Provider
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		providers = append(providers, p)
	}
	return providers, rows.Err()
}

func (s *Store) GetProvider(ctx context.Context, id string) (Provider, error) {
	return scanProvider(s.queryRow(ctx, `SELECT `+providerColumns+` FROM providers WHERE id = ?`, id))
}

func (s *Store) CreateProvider(ctx context.Context, p Provider) error {
	now := time.Now().UTC()
	_, err := s.exec(ctx, `
		INSERT INTO providers (id, name, type, base_url, api_key, api_key_env, azure_api_version, beta_header_prefixes, aws_region, aws_auth_method, aws_access_key_id, aws_secret_access_key, aws_session_token, bedrock_api_key, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Type, p.BaseURL, p.APIKey, p.APIKeyEnv, p.AzureAPIVersion, p.BetaHeaderPrefixes, p.AWSRegion, p.AWSAuthMethod, p.AWSAccessKeyID, p.AWSSecretAccessKey, p.AWSSessionToken, p.BedrockAPIKey, boolInt(p.Enabled), formatTime(now), formatTime(now))
	if isUniqueErr(err) {
		return ErrConflict
	}
	return err
}

// ProviderSecretUpdate marks which write-only provider secrets an update
// should overwrite; unset fields keep their stored value.
type ProviderSecretUpdate struct {
	APIKey         bool
	AWSCredentials bool
	BedrockAPIKey  bool
}

func (s *Store) UpdateProvider(ctx context.Context, p Provider, secrets ProviderSecretUpdate) error {
	now := time.Now().UTC()
	set := []string{"name = ?", "type = ?", "base_url = ?", "api_key_env = ?", "azure_api_version = ?", "beta_header_prefixes = ?", "aws_region = ?", "aws_auth_method = ?", "aws_access_key_id = ?", "enabled = ?", "updated_at = ?"}
	args := []any{p.Name, p.Type, p.BaseURL, p.APIKeyEnv, p.AzureAPIVersion, p.BetaHeaderPrefixes, p.AWSRegion, p.AWSAuthMethod, p.AWSAccessKeyID, boolInt(p.Enabled), formatTime(now)}
	if secrets.APIKey {
		set = append(set, "api_key = ?")
		args = append(args, p.APIKey)
	}
	if secrets.AWSCredentials {
		set = append(set, "aws_secret_access_key = ?", "aws_session_token = ?")
		args = append(args, p.AWSSecretAccessKey, p.AWSSessionToken)
	}
	if secrets.BedrockAPIKey {
		set = append(set, "bedrock_api_key = ?")
		args = append(args, p.BedrockAPIKey)
	}
	args = append(args, p.ID)
	res, err := s.exec(ctx, `UPDATE providers SET `+strings.Join(set, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) RecordProviderSuccess(ctx context.Context, providerID string, now time.Time) error {
	res, err := s.exec(ctx, `
		UPDATE providers
		SET health_status = 'healthy', consecutive_failures = 0, last_error = '', circuit_open_until = NULL,
		    last_health_check_at = ?, updated_at = ?
		WHERE id = ?`,
		formatTime(now), formatTime(now), providerID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) RecordProviderFailure(ctx context.Context, providerID string, threshold int, cooldown time.Duration, now time.Time, errText string) (Provider, error) {
	if threshold <= 0 {
		threshold = 3
	}
	if cooldown <= 0 {
		cooldown = 5 * time.Minute
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Provider{}, err
	}
	defer tx.Rollback()

	var failures int
	err = s.txQueryRow(ctx, tx, `SELECT consecutive_failures FROM providers WHERE id = ?`, providerID).Scan(&failures)
	if errors.Is(err, sql.ErrNoRows) {
		return Provider{}, ErrNotFound
	}
	if err != nil {
		return Provider{}, err
	}
	failures++
	status := "degraded"
	var circuit any
	if failures >= threshold {
		status = "down"
		circuit = formatTime(now.Add(cooldown))
	}
	if _, err := s.txExec(ctx, tx, `
		UPDATE providers
		SET health_status = ?, consecutive_failures = ?, last_error = ?, circuit_open_until = ?,
		    last_health_check_at = ?, updated_at = ?
		WHERE id = ?`,
		status, failures, limitProviderError(errText), circuit, formatTime(now), formatTime(now), providerID); err != nil {
		return Provider{}, err
	}
	if err := tx.Commit(); err != nil {
		return Provider{}, err
	}
	return s.GetProvider(ctx, providerID)
}

func (s *Store) DeleteProvider(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := s.txExec(ctx, tx, `DELETE FROM rate_limits WHERE scope_type = 'provider' AND scope_value = ?`, id); err != nil {
		return err
	}
	if _, err := s.txExec(ctx, tx, `DELETE FROM rate_limits WHERE scope_type = 'model' AND scope_value IN (
		SELECT route FROM models WHERE provider_id = ?
		UNION SELECT model_id FROM models WHERE provider_id = ?
		UNION SELECT id FROM models WHERE provider_id = ?
	)`, id, id, id); err != nil {
		return err
	}
	res, err := s.txExec(ctx, tx, `DELETE FROM providers WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

const providerColumns = `id, name, type, base_url, api_key, api_key_env, azure_api_version, beta_header_prefixes, aws_region, aws_auth_method, aws_access_key_id, aws_secret_access_key, aws_session_token, bedrock_api_key, enabled, health_status, consecutive_failures, last_health_check_at, last_error, circuit_open_until, created_at, updated_at`

const providerColumnsAliased = `p.id, p.name, p.type, p.base_url, p.api_key, p.api_key_env, p.azure_api_version, p.beta_header_prefixes, p.aws_region, p.aws_auth_method, p.aws_access_key_id, p.aws_secret_access_key, p.aws_session_token, p.bedrock_api_key, p.enabled, p.health_status, p.consecutive_failures, p.last_health_check_at, p.last_error, p.circuit_open_until, p.created_at, p.updated_at`

func scanProvider(row scanner) (Provider, error) {
	var p Provider
	var enabled int
	var lastCheck, circuitOpen sql.NullString
	var created, updated string
	err := row.Scan(&p.ID, &p.Name, &p.Type, &p.BaseURL, &p.APIKey, &p.APIKeyEnv, &p.AzureAPIVersion, &p.BetaHeaderPrefixes, &p.AWSRegion, &p.AWSAuthMethod, &p.AWSAccessKeyID, &p.AWSSecretAccessKey, &p.AWSSessionToken, &p.BedrockAPIKey, &enabled, &p.HealthStatus, &p.ConsecutiveFailures, &lastCheck, &p.LastError, &circuitOpen, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Provider{}, ErrNotFound
	}
	if err != nil {
		return Provider{}, err
	}
	p.Enabled = enabled == 1
	if p.HealthStatus == "" {
		p.HealthStatus = "unknown"
	}
	if lastCheck.Valid {
		t := parseTime(lastCheck.String)
		p.LastHealthCheckAt = &t
	}
	if circuitOpen.Valid {
		t := parseTime(circuitOpen.String)
		p.CircuitOpenUntil = &t
	}
	p.CreatedAt = parseTime(created)
	p.UpdatedAt = parseTime(updated)
	return p, nil
}
