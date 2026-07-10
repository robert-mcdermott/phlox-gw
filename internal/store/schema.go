package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const postgresMigrationLockKey int64 = 0x50484c4f584757

func (s *Store) init(ctx context.Context, lockTimeout time.Duration) error {
	if s.dialect == dialectSQLite {
		pragmas := []string{
			"PRAGMA journal_mode = WAL",
			"PRAGMA foreign_keys = ON",
			"PRAGMA busy_timeout = 5000",
		}
		for _, stmt := range pragmas {
			if _, err := s.exec(ctx, stmt); err != nil {
				return err
			}
		}
	}
	if s.dialect == dialectPostgres {
		return s.initPostgresSchema(ctx, lockTimeout)
	}
	return s.initSchema(ctx)
}

func (s *Store) initSchema(ctx context.Context) error {
	for _, stmt := range schema {
		if _, err := s.exec(ctx, stmt); err != nil {
			return err
		}
	}
	return s.migrate(ctx)
}

func (s *Store) initPostgresSchema(ctx context.Context, lockTimeout time.Duration) error {
	if lockTimeout <= 0 {
		lockTimeout = 30 * time.Second
	}
	lockCtx, cancel := context.WithTimeout(ctx, lockTimeout)
	defer cancel()
	conn, err := s.db.Conn(lockCtx)
	if err != nil {
		return err
	}
	defer conn.Close()

	tx, err := conn.BeginTx(lockCtx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(lockCtx, "SELECT pg_advisory_xact_lock($1)", postgresMigrationLockKey); err != nil {
		return err
	}
	for _, stmt := range schema {
		if _, err := tx.ExecContext(lockCtx, s.rebind(stmt)); err != nil {
			return err
		}
	}
	if err := s.migrateTx(lockCtx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

type columnMigration struct {
	table  string
	column string
	spec   string
}

var columnMigrations = []columnMigration{
	{table: "users", column: "must_change_password", spec: "INTEGER NOT NULL DEFAULT 0"},
	{table: "users", column: "session_version", spec: "INTEGER NOT NULL DEFAULT 0"},
	{table: "api_keys", column: "budget_usd", spec: "DOUBLE PRECISION NOT NULL DEFAULT 0"},
	{table: "api_keys", column: "rpm_limit", spec: "INTEGER NOT NULL DEFAULT 0"},
	{table: "api_keys", column: "tpm_limit", spec: "INTEGER NOT NULL DEFAULT 0"},
	{table: "api_keys", column: "model_allowlist", spec: "TEXT NOT NULL DEFAULT ''"},
	{table: "providers", column: "health_status", spec: "TEXT NOT NULL DEFAULT 'unknown'"},
	{table: "providers", column: "consecutive_failures", spec: "INTEGER NOT NULL DEFAULT 0"},
	{table: "providers", column: "last_health_check_at", spec: "TEXT"},
	{table: "providers", column: "last_error", spec: "TEXT NOT NULL DEFAULT ''"},
	{table: "providers", column: "circuit_open_until", spec: "TEXT"},
	{table: "providers", column: "aws_auth_method", spec: "TEXT NOT NULL DEFAULT ''"},
	{table: "providers", column: "aws_access_key_id", spec: "TEXT NOT NULL DEFAULT ''"},
	{table: "providers", column: "aws_secret_access_key", spec: "TEXT NOT NULL DEFAULT ''"},
	{table: "providers", column: "aws_session_token", spec: "TEXT NOT NULL DEFAULT ''"},
	{table: "providers", column: "bedrock_api_key", spec: "TEXT NOT NULL DEFAULT ''"},
	{table: "providers", column: "azure_api_version", spec: "TEXT NOT NULL DEFAULT ''"},
	{table: "providers", column: "beta_header_prefixes", spec: "TEXT NOT NULL DEFAULT ''"},
	{table: "models", column: "fallback_routes", spec: "TEXT NOT NULL DEFAULT ''"},
	{table: "models", column: "weighted_routes", spec: "TEXT NOT NULL DEFAULT ''"},
	{table: "models", column: "retry_attempts", spec: "INTEGER NOT NULL DEFAULT 0"},
	{table: "models", column: "request_timeout_ms", spec: "INTEGER NOT NULL DEFAULT 0"},
	{table: "models", column: "health_routing_enabled", spec: "INTEGER NOT NULL DEFAULT 1"},
	{table: "guardrail_policies", column: "custom_patterns", spec: "TEXT NOT NULL DEFAULT '[]'"},
}

func (s *Store) migrate(ctx context.Context) error {
	for _, migration := range columnMigrations {
		if err := s.ensureColumn(ctx, migration.table, migration.column, migration.spec); err != nil {
			return err
		}
	}
	return s.migrateProviderTypeCheckSQLite(ctx)
}

func (s *Store) migrateTx(ctx context.Context, tx *sql.Tx) error {
	for _, migration := range columnMigrations {
		if err := s.ensureColumnTx(ctx, tx, migration.table, migration.column, migration.spec); err != nil {
			return err
		}
	}
	return migrateProviderTypeCheckPostgres(ctx, tx)
}

// migrateProviderTypeCheckSQLite rebuilds the providers table when its DDL
// still carries a CHECK constraint that predates the newest provider types.
// SQLite cannot alter CHECK constraints in place, so the table is copied.
// 'google' is the newest type, so its absence marks a stale constraint.
func (s *Store) migrateProviderTypeCheckSQLite(ctx context.Context) error {
	var ddl sql.NullString
	err := s.queryRow(ctx, `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'providers'`).Scan(&ddl)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !strings.Contains(ddl.String, "CHECK") || strings.Contains(ddl.String, "'google'") {
		return nil
	}
	if _, err := s.exec(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		return err
	}
	defer s.exec(ctx, "PRAGMA foreign_keys = ON")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmts := []string{
		`DROP TABLE IF EXISTS providers_new`,
		providersTableDDL("providers_new"),
		`INSERT INTO providers_new (` + providerColumns + `) SELECT ` + providerColumns + ` FROM providers`,
		`DROP TABLE providers`,
		`ALTER TABLE providers_new RENAME TO providers`,
	}
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// migrateProviderTypeCheckPostgres swaps the providers type CHECK constraint
// for the current type list. Postgres names inline column checks
// {table}_{column}_check, so the drop targets that name.
func migrateProviderTypeCheckPostgres(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `ALTER TABLE providers DROP CONSTRAINT IF EXISTS providers_type_check`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `ALTER TABLE providers ADD CONSTRAINT providers_type_check `+providerTypeCheck)
	return err
}

func (s *Store) ensureColumnTx(ctx context.Context, tx *sql.Tx, table, column, spec string) error {
	if s.dialect != dialectPostgres {
		return fmt.Errorf("transactional migration is unsupported for database dialect %q", s.dialect)
	}
	var count int
	if err := tx.QueryRowContext(ctx, s.rebind(`
		SELECT COUNT(*)
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = ? AND column_name = ?`),
		table, column).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, s.rebind("ALTER TABLE "+table+" ADD COLUMN "+column+" "+spec))
	return err
}

func (s *Store) ensureColumn(ctx context.Context, table, column, spec string) error {
	switch s.dialect {
	case dialectSQLite:
		rows, err := s.query(ctx, "PRAGMA table_info("+table+")")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var cid int
			var name, typ string
			var notNull int
			var defaultValue any
			var pk int
			if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
				return err
			}
			if name == column {
				return rows.Err()
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
	case dialectPostgres:
		var count int
		if err := s.queryRow(ctx, `
			SELECT COUNT(*)
			FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = ? AND column_name = ?`,
			table, column).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
	default:
		return fmt.Errorf("unsupported database dialect %q", s.dialect)
	}
	_, err := s.exec(ctx, "ALTER TABLE "+table+" ADD COLUMN "+column+" "+spec)
	return err
}

type SeedResult struct {
	AdminCreated bool
}

func (s *Store) EnsureSeedData(adminPasswordHash string) error {
	return s.ensureSeedData(adminPasswordHash, false, nil)
}

func (s *Store) EnsureBootstrapData(adminPasswordHash string) (SeedResult, error) {
	var result SeedResult
	err := s.ensureSeedData(adminPasswordHash, true, &result)
	return result, err
}

func (s *Store) ensureSeedData(adminPasswordHash string, requirePasswordChange bool, result *SeedResult) error {
	ctx := context.Background()
	now := time.Now().UTC()

	seeds := []Provider{
		{ID: "local-ollama", Name: "Ollama (local)", Type: "openai", BaseURL: "http://localhost:11434/v1", Enabled: true},
		{ID: "openai", Name: "OpenAI", Type: "openai", BaseURL: "https://api.openai.com/v1", APIKeyEnv: "OPENAI_API_KEY", Enabled: false},
		{ID: "anthropic", Name: "Anthropic", Type: "anthropic", BaseURL: "https://api.anthropic.com", APIKeyEnv: "ANTHROPIC_API_KEY", Enabled: false},
		{ID: "bedrock", Name: "AWS Bedrock", Type: "bedrock", BaseURL: "", AWSRegion: "us-east-1", AWSAuthMethod: "chain", Enabled: false},
	}
	for _, p := range seeds {
		if _, err := s.exec(ctx, `
			INSERT INTO providers (id, name, type, base_url, api_key, api_key_env, aws_region, aws_auth_method, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?)
			ON CONFLICT DO NOTHING`,
			p.ID, p.Name, p.Type, p.BaseURL, p.APIKeyEnv, p.AWSRegion, p.AWSAuthMethod, boolInt(p.Enabled), formatTime(now), formatTime(now)); err != nil {
			return err
		}
	}

	models := []Model{
		{ID: "model_local_ollama_llama", ProviderID: "local-ollama", ModelID: "llama3.1:8b", Route: "local-ollama/llama3.1:8b", DisplayName: "Llama 3.1 8B (Ollama)", Enabled: true, SupportsStreaming: true},
		{ID: "model_openai_gpt4o_mini", ProviderID: "openai", ModelID: "gpt-4o-mini", Route: "openai/gpt-4o-mini", DisplayName: "GPT-4o mini", Enabled: false, SupportsStreaming: true},
		{ID: "model_anthropic_sonnet", ProviderID: "anthropic", ModelID: "claude-3-5-sonnet-latest", Route: "anthropic/claude-3-5-sonnet-latest", DisplayName: "Claude Sonnet", Enabled: false, SupportsStreaming: true},
	}
	for _, m := range models {
		if _, err := s.exec(ctx, `
			INSERT INTO models (id, provider_id, model_id, route, display_name, input_cost_per_million, output_cost_per_million, context_window, supports_streaming, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 0, 0, 0, ?, ?, ?, ?)
			ON CONFLICT DO NOTHING`,
			m.ID, m.ProviderID, m.ModelID, m.Route, m.DisplayName, boolInt(m.SupportsStreaming), boolInt(m.Enabled), formatTime(now), formatTime(now)); err != nil {
			return err
		}
	}

	adminInsert, err := s.exec(ctx, `
		INSERT INTO users (id, username, email, display_name, department, role, password_hash, auth_provider, is_active, must_change_password, session_version, created_at, updated_at)
		SELECT ?, 'admin', 'admin@localhost', 'Administrator', 'IT', 'admin', ?, 'local', 1, ?, 0, ?, ?
		WHERE NOT EXISTS (SELECT 1 FROM users)
		ON CONFLICT DO NOTHING`,
		"user_admin", adminPasswordHash, boolInt(requirePasswordChange), formatTime(now), formatTime(now))
	if err != nil {
		return err
	}
	if result != nil {
		created, err := adminInsert.RowsAffected()
		if err != nil {
			return err
		}
		result.AdminCreated = created == 1
	}
	return nil
}

const providerTypeCheck = `CHECK (type IN ('openai', 'anthropic', 'azure-openai', 'azure-anthropic', 'google', 'bedrock'))`

func providersTableDDL(name string) string {
	return `CREATE TABLE IF NOT EXISTS ` + name + ` (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		type TEXT NOT NULL ` + providerTypeCheck + `,
		base_url TEXT NOT NULL DEFAULT '',
		api_key TEXT NOT NULL DEFAULT '',
		api_key_env TEXT NOT NULL DEFAULT '',
		azure_api_version TEXT NOT NULL DEFAULT '',
		beta_header_prefixes TEXT NOT NULL DEFAULT '',
		aws_region TEXT NOT NULL DEFAULT '',
		aws_auth_method TEXT NOT NULL DEFAULT '',
		aws_access_key_id TEXT NOT NULL DEFAULT '',
		aws_secret_access_key TEXT NOT NULL DEFAULT '',
		aws_session_token TEXT NOT NULL DEFAULT '',
		bedrock_api_key TEXT NOT NULL DEFAULT '',
		enabled INTEGER NOT NULL DEFAULT 1,
		health_status TEXT NOT NULL DEFAULT 'unknown',
		consecutive_failures INTEGER NOT NULL DEFAULT 0,
		last_health_check_at TEXT,
		last_error TEXT NOT NULL DEFAULT '',
		circuit_open_until TEXT,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`
}

var schema = []string{
	`CREATE TABLE IF NOT EXISTS users (
		id TEXT PRIMARY KEY,
		username TEXT NOT NULL UNIQUE,
		email TEXT NOT NULL DEFAULT '',
		display_name TEXT NOT NULL DEFAULT '',
		department TEXT NOT NULL DEFAULT '',
		role TEXT NOT NULL CHECK (role IN ('admin', 'user')),
		password_hash TEXT NOT NULL,
		auth_provider TEXT NOT NULL DEFAULT 'local',
		is_active INTEGER NOT NULL DEFAULT 1,
		must_change_password INTEGER NOT NULL DEFAULT 0,
		session_version INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		last_login_at TEXT
	)`,
	`CREATE TABLE IF NOT EXISTS api_keys (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		prefix TEXT NOT NULL,
		key_hash TEXT NOT NULL UNIQUE,
		is_active INTEGER NOT NULL DEFAULT 1,
		expires_at TEXT,
		last_used_at TEXT,
		created_at TEXT NOT NULL,
		budget_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
		rpm_limit INTEGER NOT NULL DEFAULT 0,
		tpm_limit INTEGER NOT NULL DEFAULT 0,
		model_allowlist TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS idx_api_keys_user ON api_keys(user_id)`,
	providersTableDDL("providers"),
	`CREATE TABLE IF NOT EXISTS models (
		id TEXT PRIMARY KEY,
		provider_id TEXT NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
		model_id TEXT NOT NULL,
		route TEXT NOT NULL UNIQUE,
		display_name TEXT NOT NULL DEFAULT '',
		input_cost_per_million DOUBLE PRECISION NOT NULL DEFAULT 0,
		output_cost_per_million DOUBLE PRECISION NOT NULL DEFAULT 0,
		context_window INTEGER NOT NULL DEFAULT 0,
		supports_streaming INTEGER NOT NULL DEFAULT 1,
		enabled INTEGER NOT NULL DEFAULT 1,
		fallback_routes TEXT NOT NULL DEFAULT '',
		weighted_routes TEXT NOT NULL DEFAULT '',
		retry_attempts INTEGER NOT NULL DEFAULT 0,
		request_timeout_ms INTEGER NOT NULL DEFAULT 0,
		health_routing_enabled INTEGER NOT NULL DEFAULT 1,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_models_provider ON models(provider_id)`,
	`CREATE TABLE IF NOT EXISTS budgets (
		id TEXT PRIMARY KEY,
		scope_type TEXT NOT NULL CHECK (scope_type IN ('user', 'department')),
		scope_value TEXT NOT NULL,
		limit_usd DOUBLE PRECISION NOT NULL,
		warn_pct DOUBLE PRECISION NOT NULL DEFAULT 90,
		is_active INTEGER NOT NULL DEFAULT 1,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		UNIQUE(scope_type, scope_value)
	)`,
	`CREATE TABLE IF NOT EXISTS rate_limits (
		id TEXT PRIMARY KEY,
		scope_type TEXT NOT NULL CHECK (scope_type IN ('user', 'department', 'provider', 'model')),
		scope_value TEXT NOT NULL,
		rpm_limit INTEGER NOT NULL DEFAULT 0,
		tpm_limit INTEGER NOT NULL DEFAULT 0,
		is_active INTEGER NOT NULL DEFAULT 1,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		UNIQUE(scope_type, scope_value)
	)`,
	`CREATE TABLE IF NOT EXISTS guardrail_policies (
		id TEXT PRIMARY KEY CHECK (id = 'default'),
		enabled INTEGER NOT NULL DEFAULT 0,
		input_action TEXT NOT NULL DEFAULT 'redact' CHECK (input_action IN ('off', 'redact', 'block')),
		output_action TEXT NOT NULL DEFAULT 'redact' CHECK (output_action IN ('off', 'redact', 'block')),
		detect_email INTEGER NOT NULL DEFAULT 1,
		detect_phone INTEGER NOT NULL DEFAULT 1,
		detect_ssn INTEGER NOT NULL DEFAULT 1,
		detect_credit_card INTEGER NOT NULL DEFAULT 1,
		detect_api_key INTEGER NOT NULL DEFAULT 1,
		custom_patterns TEXT NOT NULL DEFAULT '[]',
		redaction_text TEXT NOT NULL DEFAULT '[REDACTED]',
		streaming_block_mode TEXT NOT NULL DEFAULT 'reject' CHECK (streaming_block_mode IN ('reject')),
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS usage_ledger (
		id TEXT PRIMARY KEY,
		request_id TEXT NOT NULL UNIQUE,
		user_id TEXT NOT NULL DEFAULT '',
		username TEXT NOT NULL DEFAULT '',
		department TEXT NOT NULL DEFAULT '',
		api_key_id TEXT NOT NULL DEFAULT '',
		provider_id TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		protocol TEXT NOT NULL DEFAULT '',
		input_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0,
		total_tokens INTEGER NOT NULL DEFAULT 0,
		cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
		latency_ms INTEGER NOT NULL DEFAULT 0,
		status_code INTEGER NOT NULL DEFAULT 0,
		error_text TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_usage_user_created ON usage_ledger(user_id, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_usage_api_key_created ON usage_ledger(api_key_id, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_usage_department_created ON usage_ledger(department, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_usage_provider_created ON usage_ledger(provider_id, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_usage_model_created ON usage_ledger(model, created_at)`,
	`CREATE TABLE IF NOT EXISTS request_log (
		id TEXT PRIMARY KEY,
		request_id TEXT NOT NULL UNIQUE,
		user_id TEXT NOT NULL DEFAULT '',
		username TEXT NOT NULL DEFAULT '',
		department TEXT NOT NULL DEFAULT '',
		api_key_id TEXT NOT NULL DEFAULT '',
		api_key_prefix TEXT NOT NULL DEFAULT '',
		api_key_name TEXT NOT NULL DEFAULT '',
		provider_id TEXT NOT NULL DEFAULT '',
		provider_type TEXT NOT NULL DEFAULT '',
		model_route TEXT NOT NULL DEFAULT '',
		upstream_model_id TEXT NOT NULL DEFAULT '',
		protocol TEXT NOT NULL DEFAULT '',
		method TEXT NOT NULL DEFAULT '',
		endpoint TEXT NOT NULL DEFAULT '',
		streaming INTEGER NOT NULL DEFAULT 0,
		input_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0,
		total_tokens INTEGER NOT NULL DEFAULT 0,
		cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
		latency_ms INTEGER NOT NULL DEFAULT 0,
		status_code INTEGER NOT NULL DEFAULT 0,
		error_text TEXT NOT NULL DEFAULT '',
		client_ip TEXT NOT NULL DEFAULT '',
		user_agent TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_request_log_created ON request_log(created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_request_log_user_created ON request_log(user_id, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_request_log_department_created ON request_log(department, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_request_log_provider_created ON request_log(provider_id, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_request_log_model_created ON request_log(model_route, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_request_log_status_created ON request_log(status_code, created_at)`,
	`CREATE TABLE IF NOT EXISTS audit_log (
		id TEXT PRIMARY KEY,
		actor_user_id TEXT NOT NULL DEFAULT '',
		actor_username TEXT NOT NULL DEFAULT '',
		action TEXT NOT NULL,
		target_type TEXT NOT NULL DEFAULT '',
		target_id TEXT NOT NULL DEFAULT '',
		target_display TEXT NOT NULL DEFAULT '',
		details TEXT NOT NULL DEFAULT '',
		ip_address TEXT NOT NULL DEFAULT '',
		user_agent TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_audit_log_created ON audit_log(created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_audit_log_target ON audit_log(target_type, target_id, created_at)`,
	`CREATE TABLE IF NOT EXISTS cluster_nodes (
		instance_id TEXT PRIMARY KEY,
		hostname TEXT NOT NULL DEFAULT '',
		version TEXT NOT NULL DEFAULT '',
		addr TEXT NOT NULL DEFAULT '',
		deployment_mode TEXT NOT NULL DEFAULT '',
		db_driver TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'starting',
		started_at TEXT NOT NULL,
		last_seen_at TEXT NOT NULL,
		metadata TEXT NOT NULL DEFAULT '{}'
	)`,
	`CREATE INDEX IF NOT EXISTS idx_cluster_nodes_last_seen ON cluster_nodes(last_seen_at)`,
}
