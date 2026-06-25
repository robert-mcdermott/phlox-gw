package config

import (
	"path/filepath"
	"testing"
)

func TestLoadDatabaseConfigDefaultsToSQLite(t *testing.T) {
	clearDatabaseEnv(t)
	dataDir := t.TempDir()

	cfg := loadDatabaseConfig(dataDir)

	if cfg.Driver != "sqlite" {
		t.Fatalf("driver = %q, want sqlite", cfg.Driver)
	}
	if cfg.Path != filepath.Join(dataDir, "phlox-gw.db") {
		t.Fatalf("path = %q, want default sqlite path", cfg.Path)
	}
	if cfg.URL != "" {
		t.Fatalf("url = %q, want empty", cfg.URL)
	}
}

func TestLoadDatabaseConfigInfersPostgresFromURL(t *testing.T) {
	clearDatabaseEnv(t)
	t.Setenv("PHLOX_GW_DATABASE_URL", "postgres://phlox:secret@localhost:5432/phlox_gw?sslmode=disable")

	cfg := loadDatabaseConfig(t.TempDir())

	if cfg.Driver != "postgres" {
		t.Fatalf("driver = %q, want postgres", cfg.Driver)
	}
	if cfg.URL == "" {
		t.Fatal("expected database URL")
	}
	if err := validateDatabaseConfig(cfg); err != nil {
		t.Fatalf("validateDatabaseConfig returned error: %v", err)
	}
}

func clearDatabaseEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"PHLOX_GW_DATABASE_URL",
		"PHLOX_GW_POSTGRES_DSN",
		"PHLOX_GW_DATABASE_DRIVER",
		"PHLOX_GW_DB_DRIVER",
		"PHLOX_GW_DB_PATH",
	} {
		t.Setenv(key, "")
	}
}
