package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestLoadDeploymentConfigDefaultsFromDatabaseDriver(t *testing.T) {
	clearDeploymentEnv(t)

	sqlite := loadDeploymentConfig(DatabaseConfig{Driver: "sqlite"})
	if sqlite.Mode != "single-sqlite" {
		t.Fatalf("sqlite mode = %q, want single-sqlite", sqlite.Mode)
	}
	if sqlite.InstanceID == "" {
		t.Fatal("expected derived instance id")
	}

	postgres := loadDeploymentConfig(DatabaseConfig{Driver: "postgres"})
	if postgres.Mode != "single-postgres" {
		t.Fatalf("postgres mode = %q, want single-postgres", postgres.Mode)
	}
}

func TestValidateClusterDeploymentRequiresPostgresAndSessionSecret(t *testing.T) {
	deployment := DeploymentConfig{
		Mode:              "cluster-postgres",
		InstanceID:        "node-1",
		HeartbeatInterval: time.Second,
		NodeStaleAfter:    3 * time.Second,
	}
	if err := validateDeploymentConfig(deployment, DatabaseConfig{Driver: "sqlite"}, false); err == nil || !strings.Contains(err.Error(), "cluster-postgres requires") {
		t.Fatalf("expected postgres requirement error, got %v", err)
	}
	if err := validateDeploymentConfig(deployment, DatabaseConfig{Driver: "postgres"}, true); err == nil || !strings.Contains(err.Error(), "PHLOX_GW_SESSION_SECRET") {
		t.Fatalf("expected session secret requirement error, got %v", err)
	}
	if err := validateDeploymentConfig(deployment, DatabaseConfig{Driver: "postgres"}, false); err != nil {
		t.Fatalf("validateDeploymentConfig returned error: %v", err)
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
		"PHLOX_GW_DB_MAX_OPEN_CONNS",
		"PHLOX_GW_DB_MAX_IDLE_CONNS",
		"PHLOX_GW_DB_CONN_MAX_LIFETIME",
		"PHLOX_GW_DB_MIGRATION_LOCK_TIMEOUT",
	} {
		t.Setenv(key, "")
	}
}

func clearDeploymentEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"PHLOX_GW_DEPLOYMENT_MODE",
		"PHLOX_GW_INSTANCE_ID",
		"PHLOX_GW_CLUSTER_HEARTBEAT_INTERVAL",
		"PHLOX_GW_CLUSTER_NODE_STALE_AFTER",
	} {
		t.Setenv(key, "")
	}
}
