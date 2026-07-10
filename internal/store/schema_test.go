package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestMigrateLegacyProviderTypeCheck(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	legacyDDL := `CREATE TABLE providers (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		type TEXT NOT NULL CHECK (type IN ('openai', 'anthropic', 'bedrock')),
		base_url TEXT NOT NULL DEFAULT '',
		api_key TEXT NOT NULL DEFAULT '',
		api_key_env TEXT NOT NULL DEFAULT '',
		aws_region TEXT NOT NULL DEFAULT '',
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`
	if _, err := db.ExecContext(ctx, legacyDDL); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO providers (id, name, type, base_url, api_key, created_at, updated_at)
		VALUES ('legacy-openai', 'Legacy OpenAI', 'openai', 'http://legacy.test/v1', 'k', '2025-01-01T00:00:00Z', '2025-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert legacy provider: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open after legacy schema: %v", err)
	}
	defer s.Close()
	legacy, err := s.GetProvider(ctx, "legacy-openai")
	if err != nil {
		t.Fatalf("GetProvider legacy: %v", err)
	}
	if legacy.Type != "openai" || legacy.BaseURL != "http://legacy.test/v1" || legacy.APIKey != "k" || !legacy.Enabled {
		t.Fatalf("legacy provider mangled by migration: %#v", legacy)
	}
	if err := s.CreateProvider(ctx, Provider{
		ID:      "azure-new",
		Name:    "Azure New",
		Type:    "azure-openai",
		BaseURL: "https://myres.openai.azure.com",
		Enabled: true,
	}); err != nil {
		t.Fatalf("CreateProvider azure on migrated db: %v", err)
	}
	// Re-opening must not rebuild again or lose data.
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatalf("re-open migrated db: %v", err)
	}
	defer s.Close()
	providers, err := s.ListProviders(ctx)
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(providers) != 2 {
		t.Fatalf("provider count after re-open = %d", len(providers))
	}
}

func TestMigrateAzureEraProviderTypeCheckAcceptsGoogle(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "azure-era.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	// Providers DDL as created between the Azure and Google provider releases.
	azureEraDDL := `CREATE TABLE providers (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		type TEXT NOT NULL CHECK (type IN ('openai', 'anthropic', 'azure-openai', 'azure-anthropic', 'bedrock')),
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
	if _, err := db.ExecContext(ctx, azureEraDDL); err != nil {
		t.Fatalf("create azure-era table: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO providers (id, name, type, base_url, azure_api_version, created_at, updated_at)
		VALUES ('azure-east', 'Azure East', 'azure-openai', 'https://myres.openai.azure.com', '2024-12-01-preview', '2026-07-01T00:00:00Z', '2026-07-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert azure provider: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open after azure-era schema: %v", err)
	}
	defer s.Close()
	existing, err := s.GetProvider(ctx, "azure-east")
	if err != nil {
		t.Fatalf("GetProvider existing: %v", err)
	}
	if existing.Type != "azure-openai" || existing.AzureAPIVersion != "2024-12-01-preview" {
		t.Fatalf("existing provider mangled by migration: %#v", existing)
	}
	if err := s.CreateProvider(ctx, Provider{
		ID:      "google",
		Name:    "Google Gemini",
		Type:    "google",
		BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai",
		Enabled: true,
	}); err != nil {
		t.Fatalf("CreateProvider google on migrated db: %v", err)
	}
}

func TestMigrateExistingUsersPreservesAccess(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-users.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	_, err = db.ExecContext(ctx, `CREATE TABLE users (
		id TEXT PRIMARY KEY,
		username TEXT NOT NULL UNIQUE,
		email TEXT NOT NULL DEFAULT '',
		display_name TEXT NOT NULL DEFAULT '',
		department TEXT NOT NULL DEFAULT '',
		role TEXT NOT NULL CHECK (role IN ('user', 'admin')),
		password_hash TEXT NOT NULL,
		auth_provider TEXT NOT NULL DEFAULT 'local',
		is_active INTEGER NOT NULL DEFAULT 1,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		last_login_at TEXT
	)`)
	if err != nil {
		t.Fatalf("create legacy users: %v", err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO users (id, username, role, password_hash, created_at, updated_at)
		VALUES ('existing-admin', 'admin', 'admin', 'existing-hash', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert legacy user: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open migrated database: %v", err)
	}
	defer s.Close()
	admin, err := s.GetUserByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if admin.PasswordHash != "existing-hash" || admin.MustChangePassword || admin.SessionVersion != 0 {
		t.Fatalf("legacy user access state changed: %#v", admin)
	}
}
