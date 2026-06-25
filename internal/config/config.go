package config

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Addr               string
	DataDir            string
	DBPath             string
	Database           DatabaseConfig
	SessionSecret      string
	UsingDefaultSecret bool
	OIDC               OIDCConfig
	Telemetry          TelemetryConfig
}

type DatabaseConfig struct {
	Driver string
	Path   string
	URL    string
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

	return Config{
		Addr:               addr,
		DataDir:            dataDir,
		DBPath:             database.Path,
		Database:           database,
		SessionSecret:      secret,
		UsingDefaultSecret: usingDefault,
		OIDC:               oidc,
		Telemetry:          loadTelemetryConfig(),
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
		Driver: driver,
		Path:   dbPath,
		URL:    dbURL,
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
