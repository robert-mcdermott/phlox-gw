package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("conflict")

type Store struct {
	db      *sql.DB
	dialect sqlDialect
}

type OpenOptions struct {
	Driver               string
	Path                 string
	URL                  string
	MaxOpenConns         int
	MaxIdleConns         int
	ConnMaxLifetime      time.Duration
	MigrationLockTimeout time.Duration
}

type sqlDialect string

const (
	dialectSQLite   sqlDialect = "sqlite"
	dialectPostgres sqlDialect = "postgres"
)

type Budget struct {
	ID         string    `json:"id"`
	ScopeType  string    `json:"scope_type"`
	ScopeValue string    `json:"scope_value"`
	LimitUSD   float64   `json:"limit_usd"`
	WarnPct    float64   `json:"warn_pct"`
	IsActive   bool      `json:"is_active"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

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

type GuardrailPolicy struct {
	ID                 string                   `json:"id"`
	Enabled            bool                     `json:"enabled"`
	InputAction        string                   `json:"input_action"`
	OutputAction       string                   `json:"output_action"`
	DetectEmail        bool                     `json:"detect_email"`
	DetectPhone        bool                     `json:"detect_phone"`
	DetectSSN          bool                     `json:"detect_ssn"`
	DetectCreditCard   bool                     `json:"detect_credit_card"`
	DetectAPIKey       bool                     `json:"detect_api_key"`
	CustomPatterns     []GuardrailCustomPattern `json:"custom_patterns"`
	RedactionText      string                   `json:"redaction_text"`
	StreamingBlockMode string                   `json:"streaming_block_mode"`
	CreatedAt          time.Time                `json:"created_at"`
	UpdatedAt          time.Time                `json:"updated_at"`
}

type GuardrailCustomPattern struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Pattern       string `json:"pattern"`
	Action        string `json:"action"`
	RedactionText string `json:"redaction_text"`
	Enabled       bool   `json:"enabled"`
}

type RateLimitWindowUsage struct {
	Requests    int64
	TotalTokens int64
}

type BudgetStatus struct {
	Blocked bool             `json:"blocked"`
	Warning bool             `json:"warning"`
	Reason  string           `json:"reason"`
	Items   []BudgetLineItem `json:"items"`
}

type BudgetLineItem struct {
	Budget   Budget  `json:"budget"`
	SpendUSD float64 `json:"spend_usd"`
	Ratio    float64 `json:"ratio"`
	Blocked  bool    `json:"blocked"`
	Warning  bool    `json:"warning"`
}

type BudgetBurnDownItem struct {
	Budget               Budget  `json:"budget"`
	SpendUSD             float64 `json:"spend_usd"`
	RemainingUSD         float64 `json:"remaining_usd"`
	Ratio                float64 `json:"ratio"`
	DailyAverageUSD      float64 `json:"daily_average_usd"`
	ProjectedMonthEndUSD float64 `json:"projected_month_end_usd"`
	ProjectedRatio       float64 `json:"projected_ratio"`
	DaysElapsed          int     `json:"days_elapsed"`
	DaysRemaining        int     `json:"days_remaining"`
	Blocked              bool    `json:"blocked"`
	Warning              bool    `json:"warning"`
}

type UsageRecord struct {
	ID           string
	RequestID    string
	UserID       string
	Username     string
	Department   string
	APIKeyID     string
	ProviderID   string
	Model        string
	Protocol     string
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	CostUSD      float64
	LatencyMS    int64
	StatusCode   int
	ErrorText    string
	CreatedAt    time.Time
}

type UsageSummary struct {
	InputTokens  int64                 `json:"input_tokens"`
	OutputTokens int64                 `json:"output_tokens"`
	TotalTokens  int64                 `json:"total_tokens"`
	CostUSD      float64               `json:"cost_usd"`
	Requests     int64                 `json:"requests"`
	ByModel      []UsageSummaryByModel `json:"by_model"`
}

type UsageSummaryByModel struct {
	Model        string  `json:"model"`
	ProviderID   string  `json:"provider_id"`
	Department   string  `json:"department,omitempty"`
	Username     string  `json:"username,omitempty"`
	Requests     int64   `json:"requests"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	TotalTokens  int64   `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

type UsageTimeSeriesPoint struct {
	Date         string  `json:"date"`
	Requests     int64   `json:"requests"`
	Errors       int64   `json:"errors"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	TotalTokens  int64   `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	AvgLatencyMS float64 `json:"avg_latency_ms"`
}

type UsageDrilldowns struct {
	Providers []UsageDrilldownRow `json:"providers"`
	Models    []UsageDrilldownRow `json:"models"`
}

type UsageDrilldownRow struct {
	ProviderID   string     `json:"provider_id"`
	Model        string     `json:"model,omitempty"`
	Requests     int64      `json:"requests"`
	Errors       int64      `json:"errors"`
	ErrorRate    float64    `json:"error_rate"`
	InputTokens  int64      `json:"input_tokens"`
	OutputTokens int64      `json:"output_tokens"`
	TotalTokens  int64      `json:"total_tokens"`
	CostUSD      float64    `json:"cost_usd"`
	AvgLatencyMS float64    `json:"avg_latency_ms"`
	LastUsedAt   *time.Time `json:"last_used_at,omitempty"`
}

type UsageExportRow struct {
	CreatedAt    time.Time `json:"created_at"`
	RequestID    string    `json:"request_id"`
	Username     string    `json:"username"`
	Department   string    `json:"department"`
	APIKeyID     string    `json:"api_key_id"`
	ProviderID   string    `json:"provider_id"`
	Model        string    `json:"model"`
	Protocol     string    `json:"protocol"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	TotalTokens  int       `json:"total_tokens"`
	CostUSD      float64   `json:"cost_usd"`
	LatencyMS    int64     `json:"latency_ms"`
	StatusCode   int       `json:"status_code"`
	ErrorText    string    `json:"error_text"`
}

func Open(path string) (*Store, error) {
	return OpenWithOptions(OpenOptions{Driver: string(dialectSQLite), Path: path})
}

func OpenWithOptions(opts OpenOptions) (*Store, error) {
	driver := normalizeDriver(opts.Driver)
	var sqlDriver, dsn string
	var dialect sqlDialect
	switch driver {
	case "", "sqlite", "sqlite3":
		if strings.TrimSpace(opts.Path) == "" {
			return nil, errors.New("sqlite database path is required")
		}
		sqlDriver = "sqlite"
		dsn = opts.Path
		dialect = dialectSQLite
	case "postgres", "postgresql", "pgx":
		if strings.TrimSpace(opts.URL) == "" {
			return nil, errors.New("postgres database URL is required")
		}
		sqlDriver = "pgx"
		dsn = opts.URL
		dialect = dialectPostgres
	default:
		return nil, fmt.Errorf("unsupported database driver %q", opts.Driver)
	}

	db, err := sql.Open(sqlDriver, dsn)
	if err != nil {
		return nil, err
	}
	switch dialect {
	case dialectSQLite:
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		db.SetConnMaxLifetime(0)
	case dialectPostgres:
		maxOpen := opts.MaxOpenConns
		if maxOpen <= 0 {
			maxOpen = 25
		}
		maxIdle := opts.MaxIdleConns
		if maxIdle <= 0 {
			maxIdle = maxOpen
		}
		lifetime := opts.ConnMaxLifetime
		if lifetime <= 0 {
			lifetime = 30 * time.Minute
		}
		db.SetMaxOpenConns(maxOpen)
		db.SetMaxIdleConns(maxIdle)
		db.SetConnMaxLifetime(lifetime)
	}

	s := &Store{db: db, dialect: dialect}
	if err := s.init(context.Background(), opts.MigrationLockTimeout); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *Store) Driver() string {
	return string(s.dialect)
}

func normalizeDriver(driver string) string {
	return strings.ToLower(strings.TrimSpace(driver))
}

func (s *Store) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return s.db.ExecContext(ctx, s.rebind(query), args...)
}

func (s *Store) query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, s.rebind(query), args...)
}

func (s *Store) queryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return s.db.QueryRowContext(ctx, s.rebind(query), args...)
}

func (s *Store) txExec(ctx context.Context, tx *sql.Tx, query string, args ...any) (sql.Result, error) {
	return tx.ExecContext(ctx, s.rebind(query), args...)
}

func (s *Store) txQueryRow(ctx context.Context, tx *sql.Tx, query string, args ...any) *sql.Row {
	return tx.QueryRowContext(ctx, s.rebind(query), args...)
}

func (s *Store) rebind(query string) string {
	if s.dialect != dialectPostgres {
		return query
	}
	return rebindPostgres(query)
}

func rebindPostgres(query string) string {
	var b strings.Builder
	b.Grow(len(query) + 8)
	arg := 1
	inSingle := false
	inDouble := false
	inLineComment := false
	inBlockComment := false

	for i := 0; i < len(query); i++ {
		ch := query[i]
		next := byte(0)
		if i+1 < len(query) {
			next = query[i+1]
		}

		if inLineComment {
			b.WriteByte(ch)
			if ch == '\n' {
				inLineComment = false
			}
			continue
		}
		if inBlockComment {
			b.WriteByte(ch)
			if ch == '*' && next == '/' {
				b.WriteByte(next)
				i++
				inBlockComment = false
			}
			continue
		}
		if inSingle {
			b.WriteByte(ch)
			if ch == '\'' {
				if next == '\'' {
					b.WriteByte(next)
					i++
					continue
				}
				inSingle = false
			}
			continue
		}
		if inDouble {
			b.WriteByte(ch)
			if ch == '"' {
				if next == '"' {
					b.WriteByte(next)
					i++
					continue
				}
				inDouble = false
			}
			continue
		}

		switch {
		case ch == '-' && next == '-':
			b.WriteByte(ch)
			b.WriteByte(next)
			i++
			inLineComment = true
		case ch == '/' && next == '*':
			b.WriteByte(ch)
			b.WriteByte(next)
			i++
			inBlockComment = true
		case ch == '\'':
			b.WriteByte(ch)
			inSingle = true
		case ch == '"':
			b.WriteByte(ch)
			inDouble = true
		case ch == '?':
			b.WriteByte('$')
			b.WriteString(fmt.Sprint(arg))
			arg++
		default:
			b.WriteByte(ch)
		}
	}
	return b.String()
}

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

func (s *Store) EnsureSeedData(adminPasswordHash string) error {
	ctx := context.Background()
	now := time.Now().UTC()
	var count int
	if err := s.queryRow(ctx, "SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		if _, err := s.exec(ctx, `
			INSERT INTO users (id, username, email, display_name, department, role, password_hash, auth_provider, is_active, created_at, updated_at)
			VALUES (?, 'admin', 'admin@localhost', 'Administrator', 'IT', 'admin', ?, 'local', 1, ?, ?)`,
			"user_admin", adminPasswordHash, formatTime(now), formatTime(now)); err != nil {
			return err
		}
	}

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
	return nil
}

func (s *Store) ListBudgets(ctx context.Context) ([]Budget, error) {
	rows, err := s.query(ctx, `SELECT id, scope_type, scope_value, limit_usd, warn_pct, is_active, created_at, updated_at FROM budgets ORDER BY scope_type, scope_value`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var budgets []Budget
	for rows.Next() {
		b, err := scanBudget(rows)
		if err != nil {
			return nil, err
		}
		budgets = append(budgets, b)
	}
	return budgets, rows.Err()
}

func (s *Store) CreateBudget(ctx context.Context, b Budget) error {
	now := time.Now().UTC()
	_, err := s.exec(ctx, `
		INSERT INTO budgets (id, scope_type, scope_value, limit_usd, warn_pct, is_active, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		b.ID, b.ScopeType, b.ScopeValue, b.LimitUSD, b.WarnPct, boolInt(b.IsActive), formatTime(now), formatTime(now))
	if isUniqueErr(err) {
		return ErrConflict
	}
	return err
}

func (s *Store) DeleteBudget(ctx context.Context, id string) error {
	res, err := s.exec(ctx, `DELETE FROM budgets WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateBudget(ctx context.Context, b Budget) error {
	now := time.Now().UTC()
	res, err := s.exec(ctx, `
		UPDATE budgets
		SET scope_type = ?, scope_value = ?, limit_usd = ?, warn_pct = ?, is_active = ?, updated_at = ?
		WHERE id = ?`,
		b.ScopeType, b.ScopeValue, b.LimitUSD, b.WarnPct, boolInt(b.IsActive), formatTime(now), b.ID)
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

func DefaultGuardrailPolicy() GuardrailPolicy {
	now := time.Now().UTC()
	return GuardrailPolicy{
		ID:                 "default",
		Enabled:            false,
		InputAction:        "redact",
		OutputAction:       "redact",
		DetectEmail:        true,
		DetectPhone:        true,
		DetectSSN:          true,
		DetectCreditCard:   true,
		DetectAPIKey:       true,
		RedactionText:      "[REDACTED]",
		StreamingBlockMode: "reject",
		CreatedAt:          now,
		UpdatedAt:          now,
	}
}

func (s *Store) GetGuardrailPolicy(ctx context.Context) (GuardrailPolicy, error) {
	row := s.queryRow(ctx, `
		SELECT id, enabled, input_action, output_action, detect_email, detect_phone, detect_ssn, detect_credit_card, detect_api_key,
		       custom_patterns, redaction_text, streaming_block_mode, created_at, updated_at
		FROM guardrail_policies
		WHERE id = 'default'`)
	p, err := scanGuardrailPolicy(row)
	if errors.Is(err, ErrNotFound) {
		return DefaultGuardrailPolicy(), nil
	}
	if err != nil {
		return GuardrailPolicy{}, err
	}
	return p, nil
}

func (s *Store) UpdateGuardrailPolicy(ctx context.Context, p GuardrailPolicy) (GuardrailPolicy, error) {
	p = normalizeGuardrailPolicy(p)
	now := time.Now().UTC()
	existing, err := s.GetGuardrailPolicy(ctx)
	if err != nil {
		return GuardrailPolicy{}, err
	}
	created := existing.CreatedAt
	if created.IsZero() {
		created = now
	}
	_, err = s.exec(ctx, `
		INSERT INTO guardrail_policies
		(id, enabled, input_action, output_action, detect_email, detect_phone, detect_ssn, detect_credit_card, detect_api_key,
		 custom_patterns, redaction_text, streaming_block_mode, created_at, updated_at)
		VALUES ('default', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			enabled = excluded.enabled,
			input_action = excluded.input_action,
			output_action = excluded.output_action,
			detect_email = excluded.detect_email,
			detect_phone = excluded.detect_phone,
			detect_ssn = excluded.detect_ssn,
			detect_credit_card = excluded.detect_credit_card,
			detect_api_key = excluded.detect_api_key,
			custom_patterns = excluded.custom_patterns,
			redaction_text = excluded.redaction_text,
			streaming_block_mode = excluded.streaming_block_mode,
			updated_at = excluded.updated_at`,
		boolInt(p.Enabled), p.InputAction, p.OutputAction, boolInt(p.DetectEmail), boolInt(p.DetectPhone), boolInt(p.DetectSSN),
		boolInt(p.DetectCreditCard), boolInt(p.DetectAPIKey), encodeGuardrailCustomPatterns(p.CustomPatterns), p.RedactionText, p.StreamingBlockMode, formatTime(created), formatTime(now))
	if err != nil {
		return GuardrailPolicy{}, err
	}
	return s.GetGuardrailPolicy(ctx)
}

func (s *Store) BudgetStatus(ctx context.Context, u User, pricedOnly bool) (BudgetStatus, error) {
	if !pricedOnly {
		return BudgetStatus{}, nil
	}
	rows, err := s.query(ctx, `
		SELECT id, scope_type, scope_value, limit_usd, warn_pct, is_active, created_at, updated_at
		FROM budgets
		WHERE is_active = 1 AND (
			(scope_type = 'user' AND scope_value = ?) OR
			(scope_type = 'department' AND scope_value = ?)
		)`, u.ID, u.Department)
	if err != nil {
		return BudgetStatus{}, err
	}
	var budgets []Budget
	for rows.Next() {
		b, err := scanBudget(rows)
		if err != nil {
			rows.Close()
			return BudgetStatus{}, err
		}
		budgets = append(budgets, b)
	}
	if err := rows.Close(); err != nil {
		return BudgetStatus{}, err
	}
	if err := rows.Err(); err != nil {
		return BudgetStatus{}, err
	}

	var status BudgetStatus
	start, end := monthBounds(time.Now().UTC())
	for _, b := range budgets {
		spend, err := s.spendForBudget(ctx, b, start, end)
		if err != nil {
			return BudgetStatus{}, err
		}
		ratio := 0.0
		if b.LimitUSD > 0 {
			ratio = spend / b.LimitUSD
		}
		item := BudgetLineItem{
			Budget:   b,
			SpendUSD: roundCost(spend),
			Ratio:    ratio,
			Blocked:  b.LimitUSD > 0 && spend >= b.LimitUSD,
			Warning:  b.LimitUSD > 0 && spend >= b.LimitUSD*(b.WarnPct/100),
		}
		if item.Blocked {
			status.Blocked = true
			status.Reason = fmt.Sprintf("%s budget %s is at or over limit", b.ScopeType, b.ScopeValue)
		}
		if item.Warning {
			status.Warning = true
		}
		status.Items = append(status.Items, item)
	}
	return status, nil
}

func (s *Store) InsertUsage(ctx context.Context, r UsageRecord) error {
	now := r.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	_, err := s.exec(ctx, `
		INSERT INTO usage_ledger
		(id, request_id, user_id, username, department, api_key_id, provider_id, model, protocol, input_tokens, output_tokens, total_tokens, cost_usd, latency_ms, status_code, error_text, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING`,
		r.ID, r.RequestID, r.UserID, r.Username, r.Department, r.APIKeyID, r.ProviderID, r.Model, r.Protocol,
		r.InputTokens, r.OutputTokens, r.TotalTokens, r.CostUSD, r.LatencyMS, r.StatusCode, r.ErrorText, formatTime(now))
	return err
}

func (s *Store) UsageForUser(ctx context.Context, userID string) (UsageSummary, error) {
	return s.usageSummary(ctx, `WHERE user_id = ?`, userID)
}

func (s *Store) UsageAll(ctx context.Context) (UsageSummary, error) {
	return s.usageSummary(ctx, ``)
}

func (s *Store) BudgetBurnDown(ctx context.Context, now time.Time) ([]BudgetBurnDownItem, error) {
	start, end := monthBounds(now.UTC())
	budgets, err := s.ListBudgets(ctx)
	if err != nil {
		return nil, err
	}
	elapsed := now.UTC().Sub(start)
	if elapsed < 0 {
		elapsed = 0
	}
	total := end.Sub(start)
	elapsedRatio := 0.0
	if total > 0 {
		elapsedRatio = elapsed.Seconds() / total.Seconds()
	}
	if elapsedRatio <= 0 {
		elapsedRatio = 1 / total.Seconds()
	}
	daysElapsed := int(math.Ceil(elapsed.Hours() / 24))
	if daysElapsed < 1 {
		daysElapsed = 1
	}
	daysRemaining := int(math.Ceil(end.Sub(now.UTC()).Hours() / 24))
	if daysRemaining < 0 {
		daysRemaining = 0
	}
	items := make([]BudgetBurnDownItem, 0, len(budgets))
	for _, budget := range budgets {
		spend, err := s.spendForBudget(ctx, budget, start, end)
		if err != nil {
			return nil, err
		}
		spend = roundCost(spend)
		remaining := roundCost(math.Max(budget.LimitUSD-spend, 0))
		ratio := 0.0
		if budget.LimitUSD > 0 {
			ratio = spend / budget.LimitUSD
		}
		projected := roundCost(spend / elapsedRatio)
		projectedRatio := 0.0
		if budget.LimitUSD > 0 {
			projectedRatio = projected / budget.LimitUSD
		}
		warnRatio := budget.WarnPct / 100
		items = append(items, BudgetBurnDownItem{
			Budget:               budget,
			SpendUSD:             spend,
			RemainingUSD:         remaining,
			Ratio:                ratio,
			DailyAverageUSD:      roundCost(spend / float64(daysElapsed)),
			ProjectedMonthEndUSD: projected,
			ProjectedRatio:       projectedRatio,
			DaysElapsed:          daysElapsed,
			DaysRemaining:        daysRemaining,
			Blocked:              budget.IsActive && budget.LimitUSD > 0 && spend >= budget.LimitUSD,
			Warning:              budget.IsActive && budget.LimitUSD > 0 && spend >= budget.LimitUSD*warnRatio,
		})
	}
	return items, nil
}

type ChargebackUserRow struct {
	UserID       string  `json:"user_id"`
	Username     string  `json:"username"`
	Requests     int64   `json:"requests"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	TotalTokens  int64   `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

type ChargebackDepartment struct {
	Department   string              `json:"department"`
	Requests     int64               `json:"requests"`
	InputTokens  int64               `json:"input_tokens"`
	OutputTokens int64               `json:"output_tokens"`
	TotalTokens  int64               `json:"total_tokens"`
	CostUSD      float64             `json:"cost_usd"`
	BudgetUSD    float64             `json:"budget_usd"`
	Users        []ChargebackUserRow `json:"users"`
}

type MonthlyChargebackReport struct {
	Month           string                 `json:"month"`
	PeriodStart     time.Time              `json:"period_start"`
	PeriodEnd       time.Time              `json:"period_end"`
	GeneratedAt     time.Time              `json:"generated_at"`
	Requests        int64                  `json:"requests"`
	InputTokens     int64                  `json:"input_tokens"`
	OutputTokens    int64                  `json:"output_tokens"`
	TotalTokens     int64                  `json:"total_tokens"`
	CostUSD         float64                `json:"cost_usd"`
	Departments     []ChargebackDepartment `json:"departments"`
	AvailableMonths []string               `json:"available_months"`
}

// ChargebackReport aggregates the usage ledger for one calendar month by
// department and user. Department and username come from the ledger rows, so
// the report reflects attribution at the time of use even if users have since
// moved departments or been deleted.
func (s *Store) ChargebackReport(ctx context.Context, month time.Time, now time.Time) (MonthlyChargebackReport, error) {
	start, end := monthBounds(month.UTC())
	report := MonthlyChargebackReport{
		Month:       start.Format("2006-01"),
		PeriodStart: start,
		PeriodEnd:   end,
		GeneratedAt: now.UTC(),
	}
	rows, err := s.query(ctx, `
		SELECT department, user_id, MAX(username) AS username,
		       COUNT(*) AS requests,
		       COALESCE(SUM(input_tokens), 0) AS input_tokens,
		       COALESCE(SUM(output_tokens), 0) AS output_tokens,
		       COALESCE(SUM(total_tokens), 0) AS total_tokens,
		       COALESCE(SUM(cost_usd), 0) AS cost_usd
		FROM usage_ledger
		WHERE created_at >= ? AND created_at < ?
		GROUP BY department, user_id
		ORDER BY department`, formatTime(start), formatTime(end))
	if err != nil {
		return MonthlyChargebackReport{}, err
	}
	defer rows.Close()
	byDept := make(map[string]*ChargebackDepartment)
	var deptOrder []string
	for rows.Next() {
		var department string
		var user ChargebackUserRow
		if err := rows.Scan(&department, &user.UserID, &user.Username, &user.Requests, &user.InputTokens, &user.OutputTokens, &user.TotalTokens, &user.CostUSD); err != nil {
			return MonthlyChargebackReport{}, err
		}
		user.CostUSD = roundCost(user.CostUSD)
		dept, ok := byDept[department]
		if !ok {
			dept = &ChargebackDepartment{Department: department}
			byDept[department] = dept
			deptOrder = append(deptOrder, department)
		}
		dept.Requests += user.Requests
		dept.InputTokens += user.InputTokens
		dept.OutputTokens += user.OutputTokens
		dept.TotalTokens += user.TotalTokens
		dept.CostUSD = roundCost(dept.CostUSD + user.CostUSD)
		dept.Users = append(dept.Users, user)
		report.Requests += user.Requests
		report.InputTokens += user.InputTokens
		report.OutputTokens += user.OutputTokens
		report.TotalTokens += user.TotalTokens
		report.CostUSD = roundCost(report.CostUSD + user.CostUSD)
	}
	if err := rows.Err(); err != nil {
		return MonthlyChargebackReport{}, err
	}

	budgets, err := s.ListBudgets(ctx)
	if err != nil {
		return MonthlyChargebackReport{}, err
	}
	for _, budget := range budgets {
		if budget.ScopeType == "department" && budget.IsActive {
			if dept, ok := byDept[budget.ScopeValue]; ok {
				dept.BudgetUSD = budget.LimitUSD
			}
		}
	}

	report.Departments = make([]ChargebackDepartment, 0, len(deptOrder))
	for _, name := range deptOrder {
		dept := byDept[name]
		sort.SliceStable(dept.Users, func(i, j int) bool { return dept.Users[i].CostUSD > dept.Users[j].CostUSD })
		report.Departments = append(report.Departments, *dept)
	}
	sort.SliceStable(report.Departments, func(i, j int) bool { return report.Departments[i].CostUSD > report.Departments[j].CostUSD })

	monthRows, err := s.query(ctx, `SELECT DISTINCT substr(created_at, 1, 7) AS month FROM usage_ledger ORDER BY month DESC`)
	if err != nil {
		return MonthlyChargebackReport{}, err
	}
	defer monthRows.Close()
	for monthRows.Next() {
		var m string
		if err := monthRows.Scan(&m); err != nil {
			return MonthlyChargebackReport{}, err
		}
		report.AvailableMonths = append(report.AvailableMonths, m)
	}
	return report, monthRows.Err()
}

func (s *Store) UsageTimeSeries(ctx context.Context, days int, now time.Time) ([]UsageTimeSeriesPoint, error) {
	if days <= 0 {
		days = 30
	}
	if days > 365 {
		days = 365
	}
	endDay := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	startDay := endDay.AddDate(0, 0, -(days - 1))
	points := make([]UsageTimeSeriesPoint, days)
	byDate := make(map[string]*UsageTimeSeriesPoint, days)
	for i := range points {
		day := startDay.AddDate(0, 0, i).Format("2006-01-02")
		points[i].Date = day
		byDate[day] = &points[i]
	}

	rows, err := s.query(ctx, `
		SELECT substr(created_at, 1, 10) AS day,
		       COUNT(*) AS requests,
		       COALESCE(SUM(CASE WHEN status_code >= 400 OR error_text <> '' THEN 1 ELSE 0 END), 0) AS errors,
		       COALESCE(SUM(input_tokens), 0),
		       COALESCE(SUM(output_tokens), 0),
		       COALESCE(SUM(total_tokens), 0),
		       COALESCE(SUM(cost_usd), 0),
		       COALESCE(AVG(latency_ms), 0)
		FROM usage_ledger
		WHERE created_at >= ?
		GROUP BY day
		ORDER BY day`, formatTime(startDay))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item UsageTimeSeriesPoint
		if err := rows.Scan(&item.Date, &item.Requests, &item.Errors, &item.InputTokens, &item.OutputTokens, &item.TotalTokens, &item.CostUSD, &item.AvgLatencyMS); err != nil {
			return nil, err
		}
		if point, ok := byDate[item.Date]; ok {
			item.CostUSD = roundCost(item.CostUSD)
			item.AvgLatencyMS = math.Round(item.AvgLatencyMS)
			*point = item
		}
	}
	return points, rows.Err()
}

func (s *Store) UsageDrilldowns(ctx context.Context, days int, now time.Time) (UsageDrilldowns, error) {
	if days <= 0 {
		days = 30
	}
	if days > 365 {
		days = 365
	}
	start := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -(days - 1))
	providers, err := s.usageDrilldownRows(ctx, "provider", start)
	if err != nil {
		return UsageDrilldowns{}, err
	}
	models, err := s.usageDrilldownRows(ctx, "model", start)
	if err != nil {
		return UsageDrilldowns{}, err
	}
	return UsageDrilldowns{Providers: providers, Models: models}, nil
}

func (s *Store) usageDrilldownRows(ctx context.Context, dimension string, start time.Time) ([]UsageDrilldownRow, error) {
	var query string
	switch dimension {
	case "provider":
		query = `
			SELECT provider_id, '' AS model,
			       COUNT(*) AS requests,
			       COALESCE(SUM(CASE WHEN status_code >= 400 OR error_text <> '' THEN 1 ELSE 0 END), 0) AS errors,
			       COALESCE(SUM(input_tokens), 0),
			       COALESCE(SUM(output_tokens), 0),
			       COALESCE(SUM(total_tokens), 0),
			       COALESCE(SUM(cost_usd), 0) AS cost,
			       COALESCE(AVG(latency_ms), 0),
			       MAX(created_at)
			FROM usage_ledger
			WHERE created_at >= ?
			GROUP BY provider_id
			ORDER BY cost DESC, requests DESC
			LIMIT 100`
	case "model":
		query = `
			SELECT provider_id, model,
			       COUNT(*) AS requests,
			       COALESCE(SUM(CASE WHEN status_code >= 400 OR error_text <> '' THEN 1 ELSE 0 END), 0) AS errors,
			       COALESCE(SUM(input_tokens), 0),
			       COALESCE(SUM(output_tokens), 0),
			       COALESCE(SUM(total_tokens), 0),
			       COALESCE(SUM(cost_usd), 0) AS cost,
			       COALESCE(AVG(latency_ms), 0),
			       MAX(created_at)
			FROM usage_ledger
			WHERE created_at >= ?
			GROUP BY provider_id, model
			ORDER BY cost DESC, requests DESC
			LIMIT 100`
	default:
		return nil, fmt.Errorf("unsupported drilldown dimension %q", dimension)
	}
	rows, err := s.query(ctx, query, formatTime(start))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageDrilldownRow
	for rows.Next() {
		var item UsageDrilldownRow
		var lastUsed string
		if err := rows.Scan(&item.ProviderID, &item.Model, &item.Requests, &item.Errors, &item.InputTokens, &item.OutputTokens, &item.TotalTokens, &item.CostUSD, &item.AvgLatencyMS, &lastUsed); err != nil {
			return nil, err
		}
		if item.Requests > 0 {
			item.ErrorRate = float64(item.Errors) / float64(item.Requests)
		}
		item.CostUSD = roundCost(item.CostUSD)
		item.AvgLatencyMS = math.Round(item.AvgLatencyMS)
		if lastUsed != "" {
			t := parseTime(lastUsed)
			item.LastUsedAt = &t
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) UsageExport(ctx context.Context) ([]UsageExportRow, error) {
	rows, err := s.query(ctx, `
		SELECT created_at, request_id, username, department, api_key_id, provider_id, model, protocol,
		       input_tokens, output_tokens, total_tokens, cost_usd, latency_ms, status_code, error_text
		FROM usage_ledger
		ORDER BY created_at DESC
		LIMIT 100000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageExportRow
	for rows.Next() {
		var row UsageExportRow
		var created string
		if err := rows.Scan(&created, &row.RequestID, &row.Username, &row.Department, &row.APIKeyID, &row.ProviderID, &row.Model, &row.Protocol,
			&row.InputTokens, &row.OutputTokens, &row.TotalTokens, &row.CostUSD, &row.LatencyMS, &row.StatusCode, &row.ErrorText); err != nil {
			return nil, err
		}
		row.CreatedAt = parseTime(created)
		row.CostUSD = roundCost(row.CostUSD)
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Store) usageSummary(ctx context.Context, where string, args ...any) (UsageSummary, error) {
	var summary UsageSummary
	row := s.queryRow(ctx, `SELECT COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(total_tokens),0), COALESCE(SUM(cost_usd),0), COUNT(*) FROM usage_ledger `+where, args...)
	if err := row.Scan(&summary.InputTokens, &summary.OutputTokens, &summary.TotalTokens, &summary.CostUSD, &summary.Requests); err != nil {
		return UsageSummary{}, err
	}
	summary.CostUSD = roundCost(summary.CostUSD)

	query := `SELECT model, provider_id, department, username, COUNT(*) AS requests, COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(total_tokens),0), COALESCE(SUM(cost_usd),0) AS cost
		FROM usage_ledger ` + where + ` GROUP BY model, provider_id, department, username ORDER BY cost DESC, requests DESC LIMIT 100`
	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return UsageSummary{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var item UsageSummaryByModel
		if err := rows.Scan(&item.Model, &item.ProviderID, &item.Department, &item.Username, &item.Requests, &item.InputTokens, &item.OutputTokens, &item.TotalTokens, &item.CostUSD); err != nil {
			return UsageSummary{}, err
		}
		item.CostUSD = roundCost(item.CostUSD)
		summary.ByModel = append(summary.ByModel, item)
	}
	return summary, rows.Err()
}

func (s *Store) spendForBudget(ctx context.Context, b Budget, start, end time.Time) (float64, error) {
	var field string
	switch b.ScopeType {
	case "user":
		field = "user_id"
	case "department":
		field = "department"
	default:
		return 0, nil
	}
	var spend float64
	err := s.queryRow(ctx, `SELECT COALESCE(SUM(cost_usd), 0) FROM usage_ledger WHERE `+field+` = ? AND created_at >= ? AND created_at < ?`,
		b.ScopeValue, formatTime(start), formatTime(end)).Scan(&spend)
	return spend, err
}

func Cost(inputTokens, outputTokens int, m Model) float64 {
	cost := (float64(inputTokens) * m.InputCostPerMillion / 1_000_000) + (float64(outputTokens) * m.OutputCostPerMillion / 1_000_000)
	return roundCost(cost)
}

func IsPriced(m Model) bool {
	return m.InputCostPerMillion > 0 || m.OutputCostPerMillion > 0
}

func monthBounds(t time.Time) (time.Time, time.Time) {
	start := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(0, 1, 0)
}

func roundCost(v float64) float64 {
	return math.Round(v*1_000_000) / 1_000_000
}

type scanner interface {
	Scan(dest ...any) error
}

func scanBudget(row scanner) (Budget, error) {
	var b Budget
	var active int
	var created, updated string
	err := row.Scan(&b.ID, &b.ScopeType, &b.ScopeValue, &b.LimitUSD, &b.WarnPct, &active, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Budget{}, ErrNotFound
	}
	if err != nil {
		return Budget{}, err
	}
	b.IsActive = active == 1
	b.CreatedAt = parseTime(created)
	b.UpdatedAt = parseTime(updated)
	return b, nil
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

func scanGuardrailPolicy(row scanner) (GuardrailPolicy, error) {
	var p GuardrailPolicy
	var enabled, email, phone, ssn, creditCard, apiKey int
	var customPatterns, created, updated string
	err := row.Scan(&p.ID, &enabled, &p.InputAction, &p.OutputAction, &email, &phone, &ssn, &creditCard, &apiKey, &customPatterns, &p.RedactionText, &p.StreamingBlockMode, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return GuardrailPolicy{}, ErrNotFound
	}
	if err != nil {
		return GuardrailPolicy{}, err
	}
	p.Enabled = enabled == 1
	p.DetectEmail = email == 1
	p.DetectPhone = phone == 1
	p.DetectSSN = ssn == 1
	p.DetectCreditCard = creditCard == 1
	p.DetectAPIKey = apiKey == 1
	p.CustomPatterns = decodeGuardrailCustomPatterns(customPatterns)
	p.CreatedAt = parseTime(created)
	p.UpdatedAt = parseTime(updated)
	return normalizeGuardrailPolicy(p), nil
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

func normalizeGuardrailPolicy(p GuardrailPolicy) GuardrailPolicy {
	p.ID = "default"
	switch p.InputAction {
	case "off", "redact", "block":
	default:
		p.InputAction = "redact"
	}
	switch p.OutputAction {
	case "off", "redact", "block":
	default:
		p.OutputAction = "redact"
	}
	if strings.TrimSpace(p.RedactionText) == "" {
		p.RedactionText = "[REDACTED]"
	} else {
		p.RedactionText = strings.TrimSpace(p.RedactionText)
	}
	if p.StreamingBlockMode != "reject" {
		p.StreamingBlockMode = "reject"
	}
	p.CustomPatterns = normalizeGuardrailCustomPatterns(p.CustomPatterns, p.RedactionText)
	return p
}

func normalizeGuardrailCustomPatterns(patterns []GuardrailCustomPattern, defaultRedaction string) []GuardrailCustomPattern {
	if len(patterns) == 0 {
		return nil
	}
	out := make([]GuardrailCustomPattern, 0, len(patterns))
	for i, pattern := range patterns {
		pattern.ID = strings.TrimSpace(pattern.ID)
		pattern.Name = strings.TrimSpace(pattern.Name)
		pattern.Pattern = strings.TrimSpace(pattern.Pattern)
		pattern.Action = strings.TrimSpace(strings.ToLower(pattern.Action))
		pattern.RedactionText = strings.TrimSpace(pattern.RedactionText)
		if pattern.Pattern == "" {
			continue
		}
		if pattern.ID == "" {
			pattern.ID = fmt.Sprintf("custom-%d", i+1)
		}
		if pattern.Name == "" {
			pattern.Name = pattern.ID
		}
		if pattern.Action != "block" {
			pattern.Action = "redact"
		}
		if pattern.RedactionText == "" {
			pattern.RedactionText = defaultRedaction
		}
		out = append(out, pattern)
		if len(out) >= 100 {
			break
		}
	}
	return out
}

func encodeGuardrailCustomPatterns(patterns []GuardrailCustomPattern) string {
	if len(patterns) == 0 {
		return "[]"
	}
	body, err := json.Marshal(patterns)
	if err != nil {
		return "[]"
	}
	return string(body)
}

func decodeGuardrailCustomPatterns(value string) []GuardrailCustomPattern {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	var patterns []GuardrailCustomPattern
	if err := json.Unmarshal([]byte(value), &patterns); err != nil {
		return nil
	}
	return patterns
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
