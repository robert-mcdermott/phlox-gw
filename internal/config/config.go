package config

import (
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
)

type Config struct {
	Addr               string
	DataDir            string
	DBPath             string
	SessionSecret      string
	UsingDefaultSecret bool
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

	secret := os.Getenv("PHLOX_GW_SESSION_SECRET")
	usingDefault := false
	if secret == "" {
		secret = devSecret()
		usingDefault = true
	}

	return Config{
		Addr:               addr,
		DataDir:            dataDir,
		DBPath:             filepath.Join(dataDir, "phlox-gw.db"),
		SessionSecret:      secret,
		UsingDefaultSecret: usingDefault,
	}, nil
}

func getenv(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func devSecret() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "phlox-gw-development-secret-change-me"
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}
