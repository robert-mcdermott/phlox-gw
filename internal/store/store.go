package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
	Driver string
	Path   string
	URL    string
}

type sqlDialect string

const (
	dialectSQLite   sqlDialect = "sqlite"
	dialectPostgres sqlDialect = "postgres"
)

type User struct {
	ID           string     `json:"id"`
	Username     string     `json:"username"`
	Email        string     `json:"email"`
	DisplayName  string     `json:"display_name"`
	Department   string     `json:"department"`
	Role         string     `json:"role"`
	PasswordHash string     `json:"-"`
	AuthProvider string     `json:"auth_provider"`
	IsActive     bool       `json:"is_active"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`
}

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

type Provider struct {
	ID                  string     `json:"id"`
	Name                string     `json:"name"`
	Type                string     `json:"type"`
	BaseURL             string     `json:"base_url"`
	APIKey              string     `json:"-"`
	APIKeyEnv           string     `json:"api_key_env"`
	AWSRegion           string     `json:"aws_region"`
	Enabled             bool       `json:"enabled"`
	HealthStatus        string     `json:"health_status"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	LastHealthCheckAt   *time.Time `json:"last_health_check_at,omitempty"`
	LastError           string     `json:"last_error"`
	CircuitOpenUntil    *time.Time `json:"circuit_open_until,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

type Model struct {
	ID                   string    `json:"id"`
	ProviderID           string    `json:"provider_id"`
	ModelID              string    `json:"model_id"`
	Route                string    `json:"route"`
	DisplayName          string    `json:"display_name"`
	InputCostPerMillion  float64   `json:"input_cost_per_million"`
	OutputCostPerMillion float64   `json:"output_cost_per_million"`
	ContextWindow        int       `json:"context_window"`
	SupportsStreaming    bool      `json:"supports_streaming"`
	Enabled              bool      `json:"enabled"`
	FallbackRoutes       string    `json:"fallback_routes"`
	WeightedRoutes       string    `json:"weighted_routes"`
	RetryAttempts        int       `json:"retry_attempts"`
	RequestTimeoutMS     int       `json:"request_timeout_ms"`
	HealthRoutingEnabled bool      `json:"health_routing_enabled"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type RoutedModel struct {
	Model    Model    `json:"model"`
	Provider Provider `json:"provider"`
}

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

type RequestLogRecord struct {
	ID              string    `json:"id"`
	RequestID       string    `json:"request_id"`
	UserID          string    `json:"user_id"`
	Username        string    `json:"username"`
	Department      string    `json:"department"`
	APIKeyID        string    `json:"api_key_id"`
	APIKeyPrefix    string    `json:"api_key_prefix"`
	APIKeyName      string    `json:"api_key_name"`
	ProviderID      string    `json:"provider_id"`
	ProviderType    string    `json:"provider_type"`
	ModelRoute      string    `json:"model_route"`
	UpstreamModelID string    `json:"upstream_model_id"`
	Protocol        string    `json:"protocol"`
	Method          string    `json:"method"`
	Endpoint        string    `json:"endpoint"`
	Streaming       bool      `json:"streaming"`
	InputTokens     int       `json:"input_tokens"`
	OutputTokens    int       `json:"output_tokens"`
	TotalTokens     int       `json:"total_tokens"`
	CostUSD         float64   `json:"cost_usd"`
	LatencyMS       int64     `json:"latency_ms"`
	StatusCode      int       `json:"status_code"`
	ErrorText       string    `json:"error_text"`
	ClientIP        string    `json:"client_ip"`
	UserAgent       string    `json:"user_agent"`
	CreatedAt       time.Time `json:"created_at"`
}

type RequestLogQuery struct {
	Search       string
	Username     string
	Department   string
	APIKeyID     string
	ProviderID   string
	ProviderType string
	ModelRoute   string
	Protocol     string
	Endpoint     string
	Status       string
	Streaming    *bool
	From         *time.Time
	To           *time.Time
	Limit        int
	Offset       int
}

type RequestLogSearchResult struct {
	Items  []RequestLogRecord `json:"items"`
	Total  int64              `json:"total"`
	Limit  int                `json:"limit"`
	Offset int                `json:"offset"`
}

type AuditLog struct {
	ID            string    `json:"id"`
	ActorUserID   string    `json:"actor_user_id"`
	ActorUsername string    `json:"actor_username"`
	Action        string    `json:"action"`
	TargetType    string    `json:"target_type"`
	TargetID      string    `json:"target_id"`
	TargetDisplay string    `json:"target_display"`
	Details       string    `json:"details"`
	IPAddress     string    `json:"ip_address"`
	UserAgent     string    `json:"user_agent"`
	CreatedAt     time.Time `json:"created_at"`
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
		db.SetMaxOpenConns(25)
		db.SetMaxIdleConns(25)
		db.SetConnMaxLifetime(30 * time.Minute)
	}

	s := &Store{db: db, dialect: dialect}
	if err := s.init(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
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

func (s *Store) init(ctx context.Context) error {
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
	for _, stmt := range schema {
		if _, err := s.exec(ctx, stmt); err != nil {
			return err
		}
	}
	return s.migrate(ctx)
}

func (s *Store) migrate(ctx context.Context) error {
	migrations := []struct {
		table  string
		column string
		spec   string
	}{
		{table: "api_keys", column: "budget_usd", spec: "DOUBLE PRECISION NOT NULL DEFAULT 0"},
		{table: "api_keys", column: "rpm_limit", spec: "INTEGER NOT NULL DEFAULT 0"},
		{table: "api_keys", column: "tpm_limit", spec: "INTEGER NOT NULL DEFAULT 0"},
		{table: "api_keys", column: "model_allowlist", spec: "TEXT NOT NULL DEFAULT ''"},
		{table: "providers", column: "health_status", spec: "TEXT NOT NULL DEFAULT 'unknown'"},
		{table: "providers", column: "consecutive_failures", spec: "INTEGER NOT NULL DEFAULT 0"},
		{table: "providers", column: "last_health_check_at", spec: "TEXT"},
		{table: "providers", column: "last_error", spec: "TEXT NOT NULL DEFAULT ''"},
		{table: "providers", column: "circuit_open_until", spec: "TEXT"},
		{table: "models", column: "fallback_routes", spec: "TEXT NOT NULL DEFAULT ''"},
		{table: "models", column: "weighted_routes", spec: "TEXT NOT NULL DEFAULT ''"},
		{table: "models", column: "retry_attempts", spec: "INTEGER NOT NULL DEFAULT 0"},
		{table: "models", column: "request_timeout_ms", spec: "INTEGER NOT NULL DEFAULT 0"},
		{table: "models", column: "health_routing_enabled", spec: "INTEGER NOT NULL DEFAULT 1"},
		{table: "guardrail_policies", column: "custom_patterns", spec: "TEXT NOT NULL DEFAULT '[]'"},
	}
	for _, migration := range migrations {
		if err := s.ensureColumn(ctx, migration.table, migration.column, migration.spec); err != nil {
			return err
		}
	}
	return nil
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
		{ID: "bedrock", Name: "AWS Bedrock", Type: "bedrock", BaseURL: "", AWSRegion: "us-east-1", Enabled: false},
	}
	for _, p := range seeds {
		if _, err := s.exec(ctx, `
			INSERT INTO providers (id, name, type, base_url, api_key, api_key_env, aws_region, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, '', ?, ?, ?, ?, ?)
			ON CONFLICT DO NOTHING`,
			p.ID, p.Name, p.Type, p.BaseURL, p.APIKeyEnv, p.AWSRegion, boolInt(p.Enabled), formatTime(now), formatTime(now)); err != nil {
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

func (s *Store) GetUserByUsername(ctx context.Context, username string) (User, error) {
	row := s.queryRow(ctx, `SELECT id, username, email, display_name, department, role, password_hash, auth_provider, is_active, created_at, updated_at, last_login_at FROM users WHERE username = ?`, username)
	return scanUser(row)
}

func (s *Store) GetUserByID(ctx context.Context, id string) (User, error) {
	row := s.queryRow(ctx, `SELECT id, username, email, display_name, department, role, password_hash, auth_provider, is_active, created_at, updated_at, last_login_at FROM users WHERE id = ?`, id)
	return scanUser(row)
}

func (s *Store) TouchLogin(ctx context.Context, userID string, t time.Time) error {
	_, err := s.exec(ctx, `UPDATE users SET last_login_at = ?, updated_at = ? WHERE id = ?`, formatTime(t), formatTime(t), userID)
	return err
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.query(ctx, `SELECT id, username, email, display_name, department, role, password_hash, auth_provider, is_active, created_at, updated_at, last_login_at FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func (s *Store) CreateUser(ctx context.Context, u User) error {
	now := time.Now().UTC()
	_, err := s.exec(ctx, `
		INSERT INTO users (id, username, email, display_name, department, role, password_hash, auth_provider, is_active, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.Username, u.Email, u.DisplayName, u.Department, u.Role, u.PasswordHash, valueOr(u.AuthProvider, "local"), boolInt(u.IsActive), formatTime(now), formatTime(now))
	if isUniqueErr(err) {
		return ErrConflict
	}
	return err
}

func (s *Store) UpdateUser(ctx context.Context, u User) error {
	now := time.Now().UTC()
	res, err := s.exec(ctx, `
		UPDATE users
		SET email = ?, display_name = ?, department = ?, role = ?, is_active = ?, updated_at = ?
		WHERE id = ?`,
		u.Email, u.DisplayName, u.Department, u.Role, boolInt(u.IsActive), formatTime(now), u.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateFederatedUser(ctx context.Context, u User) error {
	now := time.Now().UTC()
	res, err := s.exec(ctx, `
		UPDATE users
		SET email = ?, display_name = ?, department = ?, role = ?, auth_provider = ?, updated_at = ?
		WHERE id = ?`,
		u.Email, u.DisplayName, u.Department, u.Role, valueOr(u.AuthProvider, "oidc"), formatTime(now), u.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetUserPassword(ctx context.Context, userID, passwordHash string) error {
	now := time.Now().UTC()
	res, err := s.exec(ctx, `UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`, passwordHash, formatTime(now), userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteUser(ctx context.Context, userID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := s.txExec(ctx, tx, `DELETE FROM budgets WHERE scope_type = 'user' AND scope_value = ?`, userID); err != nil {
		return err
	}
	if _, err := s.txExec(ctx, tx, `DELETE FROM rate_limits WHERE scope_type = 'user' AND scope_value = ?`, userID); err != nil {
		return err
	}
	res, err := s.txExec(ctx, tx, `DELETE FROM users WHERE id = ?`, userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
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
		INSERT INTO providers (id, name, type, base_url, api_key, api_key_env, aws_region, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Type, p.BaseURL, p.APIKey, p.APIKeyEnv, p.AWSRegion, boolInt(p.Enabled), formatTime(now), formatTime(now))
	if isUniqueErr(err) {
		return ErrConflict
	}
	return err
}

func (s *Store) UpdateProvider(ctx context.Context, p Provider, updateAPIKey bool) error {
	now := time.Now().UTC()
	var res sql.Result
	var err error
	if updateAPIKey {
		res, err = s.exec(ctx, `
			UPDATE providers
			SET name = ?, type = ?, base_url = ?, api_key = ?, api_key_env = ?, aws_region = ?, enabled = ?, updated_at = ?
			WHERE id = ?`,
			p.Name, p.Type, p.BaseURL, p.APIKey, p.APIKeyEnv, p.AWSRegion, boolInt(p.Enabled), formatTime(now), p.ID)
	} else {
		res, err = s.exec(ctx, `
			UPDATE providers
			SET name = ?, type = ?, base_url = ?, api_key_env = ?, aws_region = ?, enabled = ?, updated_at = ?
			WHERE id = ?`,
			p.Name, p.Type, p.BaseURL, p.APIKeyEnv, p.AWSRegion, boolInt(p.Enabled), formatTime(now), p.ID)
	}
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

func (s *Store) ListModels(ctx context.Context, includeDisabled bool) ([]Model, error) {
	query := `SELECT ` + modelColumns + ` FROM models`
	if !includeDisabled {
		query += ` WHERE enabled = 1`
	}
	query += ` ORDER BY provider_id, model_id`
	rows, err := s.query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var models []Model
	for rows.Next() {
		m, err := scanModel(rows)
		if err != nil {
			return nil, err
		}
		models = append(models, m)
	}
	return models, rows.Err()
}

func (s *Store) CreateModel(ctx context.Context, m Model) error {
	now := time.Now().UTC()
	_, err := s.exec(ctx, `
		INSERT INTO models
		(id, provider_id, model_id, route, display_name, input_cost_per_million, output_cost_per_million, context_window,
		 supports_streaming, enabled, fallback_routes, weighted_routes, retry_attempts, request_timeout_ms, health_routing_enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ProviderID, m.ModelID, m.Route, m.DisplayName, m.InputCostPerMillion, m.OutputCostPerMillion, m.ContextWindow,
		boolInt(m.SupportsStreaming), boolInt(m.Enabled), normalizeList(m.FallbackRoutes), normalizeList(m.WeightedRoutes), clampNonNegative(m.RetryAttempts), clampNonNegative(m.RequestTimeoutMS), boolInt(m.HealthRoutingEnabled), formatTime(now), formatTime(now))
	if isUniqueErr(err) {
		return ErrConflict
	}
	return err
}

func (s *Store) UpdateModel(ctx context.Context, m Model) error {
	now := time.Now().UTC()
	res, err := s.exec(ctx, `
		UPDATE models
		SET provider_id = ?, model_id = ?, route = ?, display_name = ?, input_cost_per_million = ?, output_cost_per_million = ?,
		    context_window = ?, supports_streaming = ?, enabled = ?, fallback_routes = ?, weighted_routes = ?, retry_attempts = ?, request_timeout_ms = ?,
		    health_routing_enabled = ?, updated_at = ?
		WHERE id = ?`,
		m.ProviderID, m.ModelID, m.Route, m.DisplayName, m.InputCostPerMillion, m.OutputCostPerMillion, m.ContextWindow,
		boolInt(m.SupportsStreaming), boolInt(m.Enabled), normalizeList(m.FallbackRoutes), normalizeList(m.WeightedRoutes), clampNonNegative(m.RetryAttempts), clampNonNegative(m.RequestTimeoutMS),
		boolInt(m.HealthRoutingEnabled), formatTime(now), m.ID)
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

func (s *Store) DeleteModel(ctx context.Context, id string) error {
	route, err := s.ResolveModelByID(ctx, id, false)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := s.txExec(ctx, tx, `DELETE FROM rate_limits WHERE scope_type = 'model' AND scope_value IN (?, ?, ?)`, route.Model.Route, route.Model.ModelID, route.Model.ID); err != nil {
		return err
	}
	res, err := s.txExec(ctx, tx, `DELETE FROM models WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (s *Store) ResolveModelByID(ctx context.Context, id string, requireEnabled bool) (RoutedModel, error) {
	query := routedModelQuery + ` WHERE m.id = ?`
	if requireEnabled {
		query += ` AND m.enabled = 1 AND p.enabled = 1`
	}
	return scanRoutedModel(s.queryRow(ctx, query, id))
}

func (s *Store) ResolveModel(ctx context.Context, requested string) (RoutedModel, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return RoutedModel{}, ErrNotFound
	}
	var row *sql.Row
	if strings.Contains(requested, "/") {
		row = s.queryRow(ctx, routedModelQuery+` WHERE m.route = ? AND m.enabled = 1 AND p.enabled = 1`, requested)
	} else {
		rows, err := s.query(ctx, routedModelQuery+` WHERE m.model_id = ? AND m.enabled = 1 AND p.enabled = 1`, requested)
		if err != nil {
			return RoutedModel{}, err
		}
		defer rows.Close()
		var matches []RoutedModel
		for rows.Next() {
			rm, err := scanRoutedModel(rows)
			if err != nil {
				return RoutedModel{}, err
			}
			matches = append(matches, rm)
		}
		if err := rows.Err(); err != nil {
			return RoutedModel{}, err
		}
		if len(matches) != 1 {
			return RoutedModel{}, ErrNotFound
		}
		return matches[0], nil
	}
	return scanRoutedModel(row)
}

func (s *Store) ResolveModelCandidates(ctx context.Context, requested string) ([]RoutedModel, error) {
	primary, err := s.ResolveModel(ctx, requested)
	if err != nil {
		return nil, err
	}
	candidates := []RoutedModel{primary}
	seen := map[string]bool{primary.Model.Route: true, primary.Model.ID: true}
	for _, fallback := range splitNormalizedList(primary.Model.FallbackRoutes) {
		if seen[fallback] {
			continue
		}
		route, err := s.ResolveModel(ctx, fallback)
		if err != nil {
			continue
		}
		if seen[route.Model.Route] || seen[route.Model.ID] {
			continue
		}
		seen[route.Model.Route] = true
		seen[route.Model.ID] = true
		candidates = append(candidates, route)
	}
	return candidates, nil
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

func (s *Store) InsertRequestLog(ctx context.Context, r RequestLogRecord) error {
	now := r.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	_, err := s.exec(ctx, `
		INSERT INTO request_log
		(id, request_id, user_id, username, department, api_key_id, api_key_prefix, api_key_name, provider_id, provider_type,
		 model_route, upstream_model_id, protocol, method, endpoint, streaming, input_tokens, output_tokens, total_tokens, cost_usd,
		 latency_ms, status_code, error_text, client_ip, user_agent, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING`,
		r.ID, r.RequestID, r.UserID, r.Username, r.Department, r.APIKeyID, r.APIKeyPrefix, r.APIKeyName, r.ProviderID, r.ProviderType,
		r.ModelRoute, r.UpstreamModelID, r.Protocol, r.Method, r.Endpoint, boolInt(r.Streaming), r.InputTokens, r.OutputTokens, r.TotalTokens, r.CostUSD,
		r.LatencyMS, r.StatusCode, limitProviderError(r.ErrorText), r.ClientIP, r.UserAgent, formatTime(now))
	return err
}

func (s *Store) SearchRequestLogs(ctx context.Context, q RequestLogQuery) (RequestLogSearchResult, error) {
	q.Limit = clampQueryLimit(q.Limit, 100, 500)
	if q.Offset < 0 {
		q.Offset = 0
	}
	where, args := requestLogWhere(q)
	var total int64
	if err := s.queryRow(ctx, `SELECT COUNT(*) FROM request_log `+where, args...).Scan(&total); err != nil {
		return RequestLogSearchResult{}, err
	}
	queryArgs := append([]any{}, args...)
	queryArgs = append(queryArgs, q.Limit, q.Offset)
	rows, err := s.query(ctx, `
		SELECT id, request_id, user_id, username, department, api_key_id, api_key_prefix, api_key_name, provider_id, provider_type,
		       model_route, upstream_model_id, protocol, method, endpoint, streaming, input_tokens, output_tokens, total_tokens, cost_usd,
		       latency_ms, status_code, error_text, client_ip, user_agent, created_at
		FROM request_log `+where+`
		ORDER BY created_at DESC
		LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return RequestLogSearchResult{}, err
	}
	defer rows.Close()
	items := []RequestLogRecord{}
	for rows.Next() {
		item, err := scanRequestLog(rows)
		if err != nil {
			return RequestLogSearchResult{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return RequestLogSearchResult{}, err
	}
	return RequestLogSearchResult{Items: items, Total: total, Limit: q.Limit, Offset: q.Offset}, nil
}

func (s *Store) RequestLogExport(ctx context.Context, q RequestLogQuery) ([]RequestLogRecord, error) {
	q.Limit = clampQueryLimit(q.Limit, 100000, 100000)
	q.Offset = 0
	where, args := requestLogWhere(q)
	args = append(args, q.Limit)
	rows, err := s.query(ctx, `
		SELECT id, request_id, user_id, username, department, api_key_id, api_key_prefix, api_key_name, provider_id, provider_type,
		       model_route, upstream_model_id, protocol, method, endpoint, streaming, input_tokens, output_tokens, total_tokens, cost_usd,
		       latency_ms, status_code, error_text, client_ip, user_agent, created_at
		FROM request_log `+where+`
		ORDER BY created_at DESC
		LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RequestLogRecord
	for rows.Next() {
		item, err := scanRequestLog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func requestLogWhere(q RequestLogQuery) (string, []any) {
	var clauses []string
	var args []any
	addExact := func(column, value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		clauses = append(clauses, column+" = ?")
		args = append(args, strings.TrimSpace(value))
	}
	if search := strings.TrimSpace(q.Search); search != "" {
		like := "%" + strings.ToLower(search) + "%"
		clauses = append(clauses, `(lower(request_id) LIKE ? OR lower(username) LIKE ? OR lower(department) LIKE ? OR lower(api_key_id) LIKE ? OR lower(api_key_prefix) LIKE ? OR lower(api_key_name) LIKE ? OR lower(provider_id) LIKE ? OR lower(provider_type) LIKE ? OR lower(model_route) LIKE ? OR lower(upstream_model_id) LIKE ? OR lower(protocol) LIKE ? OR lower(endpoint) LIKE ? OR lower(error_text) LIKE ?)`)
		for i := 0; i < 13; i++ {
			args = append(args, like)
		}
	}
	addExact("username", q.Username)
	addExact("department", q.Department)
	addExact("api_key_id", q.APIKeyID)
	addExact("provider_id", q.ProviderID)
	addExact("provider_type", q.ProviderType)
	addExact("model_route", q.ModelRoute)
	addExact("protocol", q.Protocol)
	addExact("endpoint", q.Endpoint)
	if q.Streaming != nil {
		clauses = append(clauses, "streaming = ?")
		args = append(args, boolInt(*q.Streaming))
	}
	if q.From != nil {
		clauses = append(clauses, "created_at >= ?")
		args = append(args, formatTime(q.From.UTC()))
	}
	if q.To != nil {
		clauses = append(clauses, "created_at <= ?")
		args = append(args, formatTime(q.To.UTC()))
	}
	switch strings.ToLower(strings.TrimSpace(q.Status)) {
	case "", "any":
	case "success":
		clauses = append(clauses, "(status_code >= 200 AND status_code < 400 AND error_text = '')")
	case "error":
		clauses = append(clauses, "(status_code >= 400 OR error_text <> '')")
	case "4xx":
		clauses = append(clauses, "(status_code >= 400 AND status_code < 500)")
	case "5xx":
		clauses = append(clauses, "status_code >= 500")
	}
	if len(clauses) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

func clampQueryLimit(value, fallback, max int) int {
	if value <= 0 {
		value = fallback
	}
	if value > max {
		value = max
	}
	return value
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

func (s *Store) InsertAuditLog(ctx context.Context, item AuditLog) error {
	now := item.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	_, err := s.exec(ctx, `
		INSERT INTO audit_log
		(id, actor_user_id, actor_username, action, target_type, target_id, target_display, details, ip_address, user_agent, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.ActorUserID, item.ActorUsername, item.Action, item.TargetType, item.TargetID, item.TargetDisplay, item.Details, item.IPAddress, item.UserAgent, formatTime(now))
	return err
}

func (s *Store) ListAuditLogs(ctx context.Context, limit int) ([]AuditLog, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.query(ctx, `
		SELECT id, actor_user_id, actor_username, action, target_type, target_id, target_display, details, ip_address, user_agent, created_at
		FROM audit_log
		ORDER BY created_at DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditLog
	for rows.Next() {
		item, err := scanAuditLog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
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

func scanUser(row scanner) (User, error) {
	var u User
	var active int
	var created, updated string
	var last sql.NullString
	err := row.Scan(&u.ID, &u.Username, &u.Email, &u.DisplayName, &u.Department, &u.Role, &u.PasswordHash, &u.AuthProvider, &active, &created, &updated, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	u.IsActive = active == 1
	u.CreatedAt = parseTime(created)
	u.UpdatedAt = parseTime(updated)
	if last.Valid {
		t := parseTime(last.String)
		u.LastLoginAt = &t
	}
	return u, nil
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

const providerColumns = `id, name, type, base_url, api_key, api_key_env, aws_region, enabled, health_status, consecutive_failures, last_health_check_at, last_error, circuit_open_until, created_at, updated_at`

const providerColumnsAliased = `p.id, p.name, p.type, p.base_url, p.api_key, p.api_key_env, p.aws_region, p.enabled, p.health_status, p.consecutive_failures, p.last_health_check_at, p.last_error, p.circuit_open_until, p.created_at, p.updated_at`

const modelColumns = `id, provider_id, model_id, route, display_name, input_cost_per_million, output_cost_per_million, context_window, supports_streaming, enabled, fallback_routes, weighted_routes, retry_attempts, request_timeout_ms, health_routing_enabled, created_at, updated_at`

const modelColumnsAliased = `m.id, m.provider_id, m.model_id, m.route, m.display_name, m.input_cost_per_million, m.output_cost_per_million, m.context_window, m.supports_streaming, m.enabled, m.fallback_routes, m.weighted_routes, m.retry_attempts, m.request_timeout_ms, m.health_routing_enabled, m.created_at, m.updated_at`

func scanProvider(row scanner) (Provider, error) {
	var p Provider
	var enabled int
	var lastCheck, circuitOpen sql.NullString
	var created, updated string
	err := row.Scan(&p.ID, &p.Name, &p.Type, &p.BaseURL, &p.APIKey, &p.APIKeyEnv, &p.AWSRegion, &enabled, &p.HealthStatus, &p.ConsecutiveFailures, &lastCheck, &p.LastError, &circuitOpen, &created, &updated)
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

func scanModel(row scanner) (Model, error) {
	var m Model
	var streaming, enabled, healthRouting int
	var created, updated string
	err := row.Scan(&m.ID, &m.ProviderID, &m.ModelID, &m.Route, &m.DisplayName, &m.InputCostPerMillion, &m.OutputCostPerMillion, &m.ContextWindow,
		&streaming, &enabled, &m.FallbackRoutes, &m.WeightedRoutes, &m.RetryAttempts, &m.RequestTimeoutMS, &healthRouting, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Model{}, ErrNotFound
	}
	if err != nil {
		return Model{}, err
	}
	m.SupportsStreaming = streaming == 1
	m.Enabled = enabled == 1
	m.HealthRoutingEnabled = healthRouting == 1
	m.CreatedAt = parseTime(created)
	m.UpdatedAt = parseTime(updated)
	return m, nil
}

const routedModelQuery = `
	SELECT ` + modelColumnsAliased + `,
	       ` + providerColumnsAliased + `
	FROM models m
	JOIN providers p ON p.id = m.provider_id`

func scanRoutedModel(row scanner) (RoutedModel, error) {
	var m Model
	var p Provider
	var mStreaming, mEnabled, mHealthRouting, pEnabled int
	var pLastCheck, pCircuitOpen sql.NullString
	var mCreated, mUpdated, pCreated, pUpdated string
	err := row.Scan(&m.ID, &m.ProviderID, &m.ModelID, &m.Route, &m.DisplayName, &m.InputCostPerMillion, &m.OutputCostPerMillion, &m.ContextWindow,
		&mStreaming, &mEnabled, &m.FallbackRoutes, &m.WeightedRoutes, &m.RetryAttempts, &m.RequestTimeoutMS, &mHealthRouting, &mCreated, &mUpdated,
		&p.ID, &p.Name, &p.Type, &p.BaseURL, &p.APIKey, &p.APIKeyEnv, &p.AWSRegion, &pEnabled, &p.HealthStatus, &p.ConsecutiveFailures, &pLastCheck, &p.LastError, &pCircuitOpen, &pCreated, &pUpdated)
	if errors.Is(err, sql.ErrNoRows) {
		return RoutedModel{}, ErrNotFound
	}
	if err != nil {
		return RoutedModel{}, err
	}
	m.SupportsStreaming = mStreaming == 1
	m.Enabled = mEnabled == 1
	m.HealthRoutingEnabled = mHealthRouting == 1
	m.CreatedAt = parseTime(mCreated)
	m.UpdatedAt = parseTime(mUpdated)
	p.Enabled = pEnabled == 1
	if p.HealthStatus == "" {
		p.HealthStatus = "unknown"
	}
	if pLastCheck.Valid {
		t := parseTime(pLastCheck.String)
		p.LastHealthCheckAt = &t
	}
	if pCircuitOpen.Valid {
		t := parseTime(pCircuitOpen.String)
		p.CircuitOpenUntil = &t
	}
	p.CreatedAt = parseTime(pCreated)
	p.UpdatedAt = parseTime(pUpdated)
	return RoutedModel{Model: m, Provider: p}, nil
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

func scanAuditLog(row scanner) (AuditLog, error) {
	var item AuditLog
	var created string
	err := row.Scan(&item.ID, &item.ActorUserID, &item.ActorUsername, &item.Action, &item.TargetType, &item.TargetID, &item.TargetDisplay, &item.Details, &item.IPAddress, &item.UserAgent, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return AuditLog{}, ErrNotFound
	}
	if err != nil {
		return AuditLog{}, err
	}
	item.CreatedAt = parseTime(created)
	return item, nil
}

func scanRequestLog(row scanner) (RequestLogRecord, error) {
	var item RequestLogRecord
	var streaming int
	var created string
	err := row.Scan(&item.ID, &item.RequestID, &item.UserID, &item.Username, &item.Department, &item.APIKeyID, &item.APIKeyPrefix, &item.APIKeyName,
		&item.ProviderID, &item.ProviderType, &item.ModelRoute, &item.UpstreamModelID, &item.Protocol, &item.Method, &item.Endpoint, &streaming,
		&item.InputTokens, &item.OutputTokens, &item.TotalTokens, &item.CostUSD, &item.LatencyMS, &item.StatusCode, &item.ErrorText, &item.ClientIP, &item.UserAgent, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return RequestLogRecord{}, ErrNotFound
	}
	if err != nil {
		return RequestLogRecord{}, err
	}
	item.Streaming = streaming == 1
	item.CostUSD = roundCost(item.CostUSD)
	item.CreatedAt = parseTime(created)
	return item, nil
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func valueOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
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

func normalizeList(v string) string {
	return strings.Join(splitNormalizedList(v), "\n")
}

func splitNormalizedList(v string) []string {
	parts := strings.FieldsFunc(v, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == '\t'
	})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func clampNonNegative(v int) int {
	if v < 0 {
		return 0
	}
	return v
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

func limitProviderError(v string) string {
	v = strings.TrimSpace(v)
	if len(v) <= 1000 {
		return v
	}
	return v[:1000]
}

func isUniqueErr(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
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
	`CREATE TABLE IF NOT EXISTS providers (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		type TEXT NOT NULL CHECK (type IN ('openai', 'anthropic', 'bedrock')),
		base_url TEXT NOT NULL DEFAULT '',
		api_key TEXT NOT NULL DEFAULT '',
		api_key_env TEXT NOT NULL DEFAULT '',
		aws_region TEXT NOT NULL DEFAULT '',
		enabled INTEGER NOT NULL DEFAULT 1,
		health_status TEXT NOT NULL DEFAULT 'unknown',
		consecutive_failures INTEGER NOT NULL DEFAULT 0,
		last_health_check_at TEXT,
		last_error TEXT NOT NULL DEFAULT '',
		circuit_open_until TEXT,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
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
}
