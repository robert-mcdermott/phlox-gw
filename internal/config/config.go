package config

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr                 string
	DataDir              string
	DBPath               string
	Database             DatabaseConfig
	Deployment           DeploymentConfig
	SessionSecret        string
	UsingDefaultSecret   bool
	ConfigSigningKeyFile string
	OIDC                 OIDCConfig
	Telemetry            TelemetryConfig
}

type DatabaseConfig struct {
	Driver               string
	Path                 string
	URL                  string
	MaxOpenConns         int
	MaxIdleConns         int
	ConnMaxLifetime      time.Duration
	MigrationLockTimeout time.Duration
}

type DeploymentConfig struct {
	Mode              string
	InstanceID        string
	HeartbeatInterval time.Duration
	NodeStaleAfter    time.Duration
}

type OIDCConfig struct {
	Enabled         bool
	DisplayName     string
	IssuerURL       string
	ClientID        string
	ClientSecret    string
	RedirectURL     string
	Scopes          []string
	UsernameClaim   string
	DepartmentClaim string
	GroupsClaim     string
	AdminGroups     []string
	AutoProvision   bool
}

type TelemetryConfig struct {
	MetricsEnabled  bool
	MetricsPath     string
	TracesEnabled   bool
	ServiceName     string
	ServiceVersion  string
	OTLPEndpointURL string
	OTLPInsecure    bool
	SampleRatio     float64
}

func Load() (Config, error) {
	addr := getenv("PHLOX_GW_ADDR", "127.0.0.1:8080")

	dataDir := os.Getenv("PHLOX_GW_DATA_DIR")
	if dataDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return Config{}, err
		}
		dataDir = wd
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return Config{}, err
	}
	database := loadDatabaseConfig(dataDir)
	if err := validateDatabaseConfig(database); err != nil {
		return Config{}, err
	}

	secret := os.Getenv("PHLOX_GW_SESSION_SECRET")
	usingDefault := false
	if secret == "" {
		secret = devSecret()
		usingDefault = true
	}
	oidc := loadOIDCConfig()
	if oidc.Enabled {
		if oidc.IssuerURL == "" || oidc.ClientID == "" || oidc.ClientSecret == "" {
			return Config{}, errMissingOIDCConfig()
		}
	}
	deployment := loadDeploymentConfig(database)
	if err := validateDeploymentConfig(deployment, database, usingDefault); err != nil {
		return Config{}, err
	}

	return Config{
		Addr:                 addr,
		DataDir:              dataDir,
		DBPath:               database.Path,
		Database:             database,
		Deployment:           deployment,
		SessionSecret:        secret,
		UsingDefaultSecret:   usingDefault,
		ConfigSigningKeyFile: strings.TrimSpace(os.Getenv("PHLOX_GW_CONFIG_SIGNING_KEY_FILE")),
		OIDC:                 oidc,
		Telemetry:            loadTelemetryConfig(),
	}, nil
}

func loadDatabaseConfig(dataDir string) DatabaseConfig {
	dbURL := strings.TrimSpace(os.Getenv("PHLOX_GW_DATABASE_URL"))
	if dbURL == "" {
		dbURL = strings.TrimSpace(os.Getenv("PHLOX_GW_POSTGRES_DSN"))
	}

	driver := strings.TrimSpace(os.Getenv("PHLOX_GW_DATABASE_DRIVER"))
	if driver == "" {
		driver = strings.TrimSpace(os.Getenv("PHLOX_GW_DB_DRIVER"))
	}
	if driver == "" {
		if dbURL != "" {
			driver = "postgres"
		} else {
			driver = "sqlite"
		}
	}
	driver = normalizeDatabaseDriver(driver)

	dbPath := strings.TrimSpace(os.Getenv("PHLOX_GW_DB_PATH"))
	if dbPath == "" {
		dbPath = filepath.Join(dataDir, "phlox-gw.db")
	}

	return DatabaseConfig{
		Driver:               driver,
		Path:                 dbPath,
		URL:                  dbURL,
		MaxOpenConns:         intEnv("PHLOX_GW_DB_MAX_OPEN_CONNS", 25),
		MaxIdleConns:         intEnv("PHLOX_GW_DB_MAX_IDLE_CONNS", 25),
		ConnMaxLifetime:      durationEnv("PHLOX_GW_DB_CONN_MAX_LIFETIME", 30*time.Minute),
		MigrationLockTimeout: durationEnv("PHLOX_GW_DB_MIGRATION_LOCK_TIMEOUT", 30*time.Second),
	}
}

func normalizeDatabaseDriver(driver string) string {
	switch strings.ToLower(strings.TrimSpace(driver)) {
	case "", "sqlite", "sqlite3":
		return "sqlite"
	case "postgres", "postgresql", "pgx":
		return "postgres"
	default:
		return strings.ToLower(strings.TrimSpace(driver))
	}
}

func validateDatabaseConfig(database DatabaseConfig) error {
	switch database.Driver {
	case "sqlite":
		if strings.TrimSpace(database.Path) == "" {
			return fmt.Errorf("PHLOX_GW_DB_PATH is required when PHLOX_GW_DATABASE_DRIVER=sqlite")
		}
	case "postgres":
		if strings.TrimSpace(database.URL) == "" {
			return fmt.Errorf("PHLOX_GW_DATABASE_URL is required when PHLOX_GW_DATABASE_DRIVER=postgres")
		}
	default:
		return fmt.Errorf("unsupported PHLOX_GW_DATABASE_DRIVER %q", database.Driver)
	}
	return nil
}

func loadDeploymentConfig(database DatabaseConfig) DeploymentConfig {
	mode := normalizeDeploymentMode(os.Getenv("PHLOX_GW_DEPLOYMENT_MODE"))
	if mode == "" {
		if database.Driver == "postgres" {
			mode = "single-postgres"
		} else {
			mode = "single-sqlite"
		}
	}
	instanceID := strings.TrimSpace(os.Getenv("PHLOX_GW_INSTANCE_ID"))
	if instanceID == "" {
		instanceID = defaultInstanceID()
	}
	return DeploymentConfig{
		Mode:              mode,
		InstanceID:        instanceID,
		HeartbeatInterval: durationEnv("PHLOX_GW_CLUSTER_HEARTBEAT_INTERVAL", 10*time.Second),
		NodeStaleAfter:    durationEnv("PHLOX_GW_CLUSTER_NODE_STALE_AFTER", 45*time.Second),
	}
}

func normalizeDeploymentMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "", "single-sqlite", "sqlite":
		if mode == "sqlite" {
			return "single-sqlite"
		}
		return mode
	case "single-postgres", "single-postgresql", "postgres", "postgresql":
		return "single-postgres"
	case "cluster-postgres", "cluster-postgresql", "cluster":
		return "cluster-postgres"
	default:
		return mode
	}
}

func validateDeploymentConfig(deployment DeploymentConfig, database DatabaseConfig, usingDefaultSecret bool) error {
	switch deployment.Mode {
	case "single-sqlite":
		if database.Driver != "sqlite" {
			return fmt.Errorf("PHLOX_GW_DEPLOYMENT_MODE=single-sqlite requires PHLOX_GW_DATABASE_DRIVER=sqlite")
		}
	case "single-postgres":
		if database.Driver != "postgres" {
			return fmt.Errorf("PHLOX_GW_DEPLOYMENT_MODE=single-postgres requires PHLOX_GW_DATABASE_DRIVER=postgres")
		}
	case "cluster-postgres":
		if database.Driver != "postgres" {
			return fmt.Errorf("PHLOX_GW_DEPLOYMENT_MODE=cluster-postgres requires PHLOX_GW_DATABASE_URL")
		}
		if usingDefaultSecret {
			return fmt.Errorf("PHLOX_GW_SESSION_SECRET is required when PHLOX_GW_DEPLOYMENT_MODE=cluster-postgres")
		}
	default:
		return fmt.Errorf("unsupported PHLOX_GW_DEPLOYMENT_MODE %q", deployment.Mode)
	}
	if strings.TrimSpace(deployment.InstanceID) == "" {
		return fmt.Errorf("PHLOX_GW_INSTANCE_ID could not be derived")
	}
	if deployment.HeartbeatInterval <= 0 {
		return fmt.Errorf("PHLOX_GW_CLUSTER_HEARTBEAT_INTERVAL must be positive")
	}
	if deployment.NodeStaleAfter <= deployment.HeartbeatInterval {
		return fmt.Errorf("PHLOX_GW_CLUSTER_NODE_STALE_AFTER must be greater than PHLOX_GW_CLUSTER_HEARTBEAT_INTERVAL")
	}
	return nil
}

func getenv(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func loadOIDCConfig() OIDCConfig {
	return OIDCConfig{
		Enabled:         boolEnv("PHLOX_GW_OIDC_ENABLED", false),
		DisplayName:     getenv("PHLOX_GW_OIDC_DISPLAY_NAME", "Entra ID"),
		IssuerURL:       strings.TrimRight(strings.TrimSpace(os.Getenv("PHLOX_GW_OIDC_ISSUER_URL")), "/"),
		ClientID:        strings.TrimSpace(os.Getenv("PHLOX_GW_OIDC_CLIENT_ID")),
		ClientSecret:    os.Getenv("PHLOX_GW_OIDC_CLIENT_SECRET"),
		RedirectURL:     strings.TrimSpace(os.Getenv("PHLOX_GW_OIDC_REDIRECT_URL")),
		Scopes:          scopesEnv("PHLOX_GW_OIDC_SCOPES", []string{"openid", "profile", "email"}),
		UsernameClaim:   getenv("PHLOX_GW_OIDC_USERNAME_CLAIM", "preferred_username"),
		DepartmentClaim: getenv("PHLOX_GW_OIDC_DEPARTMENT_CLAIM", "department"),
		GroupsClaim:     getenv("PHLOX_GW_OIDC_GROUPS_CLAIM", "groups"),
		AdminGroups:     listEnv("PHLOX_GW_OIDC_ADMIN_GROUPS"),
		AutoProvision:   boolEnv("PHLOX_GW_OIDC_AUTO_PROVISION", true),
	}
}

func loadTelemetryConfig() TelemetryConfig {
	metricsPath := strings.TrimSpace(getenv("PHLOX_GW_METRICS_PATH", "/metrics"))
	if metricsPath == "" || !strings.HasPrefix(metricsPath, "/") {
		metricsPath = "/metrics"
	}
	endpoint := strings.TrimSpace(os.Getenv("PHLOX_GW_OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"))
	if endpoint == "" {
		endpoint = strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"))
	}
	if endpoint == "" {
		endpoint = strings.TrimSpace(os.Getenv("PHLOX_GW_OTEL_EXPORTER_OTLP_ENDPOINT"))
	}
	if endpoint == "" {
		endpoint = strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	}
	return TelemetryConfig{
		MetricsEnabled:  boolEnv("PHLOX_GW_METRICS_ENABLED", false),
		MetricsPath:     metricsPath,
		TracesEnabled:   boolEnv("PHLOX_GW_OTEL_TRACES_ENABLED", false),
		ServiceName:     getenv("PHLOX_GW_OTEL_SERVICE_NAME", "phlox-gw"),
		ServiceVersion:  strings.TrimSpace(os.Getenv("PHLOX_GW_OTEL_SERVICE_VERSION")),
		OTLPEndpointURL: endpoint,
		OTLPInsecure:    boolEnv("PHLOX_GW_OTEL_EXPORTER_OTLP_INSECURE", boolEnv("OTEL_EXPORTER_OTLP_INSECURE", false)),
		SampleRatio:     floatEnv("PHLOX_GW_OTEL_SAMPLE_RATIO", 1.0),
	}
}

func boolEnv(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if value == "" {
		return fallback
	}
	switch value {
	case "1", "true", "t", "yes", "y", "on":
		return true
	case "0", "false", "f", "no", "n", "off":
		return false
	default:
		return fallback
	}
}

func floatEnv(key string, fallback float64) float64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func intEnv(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

func durationEnv(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func scopesEnv(key string, fallback []string) []string {
	values := listEnv(key)
	if len(values) == 0 {
		values = append([]string(nil), fallback...)
	}
	hasOpenID := false
	for _, value := range values {
		if value == "openid" {
			hasOpenID = true
			break
		}
	}
	if !hasOpenID {
		values = append([]string{"openid"}, values...)
	}
	return values
}

func listEnv(key string) []string {
	raw := os.Getenv(key)
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\r' || r == '\t'
	})
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			values = append(values, part)
		}
	}
	return values
}

func errMissingOIDCConfig() error {
	return &missingOIDCConfigError{}
}

type missingOIDCConfigError struct{}

func (*missingOIDCConfigError) Error() string {
	return "PHLOX_GW_OIDC_ISSUER_URL, PHLOX_GW_OIDC_CLIENT_ID, and PHLOX_GW_OIDC_CLIENT_SECRET are required when PHLOX_GW_OIDC_ENABLED is true"
}

func devSecret() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "phlox-gw-development-secret-change-me"
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

func defaultInstanceID() string {
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		hostname = "localhost"
	}
	hostname = strings.ToLower(strings.TrimSpace(hostname))
	hostname = strings.NewReplacer(" ", "-", ".", "-").Replace(hostname)
	return fmt.Sprintf("%s-%d", hostname, os.Getpid())
}
