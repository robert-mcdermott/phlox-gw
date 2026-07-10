package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	phloxgw "github.com/robert-mcdermott/phlox-gw"
	"github.com/robert-mcdermott/phlox-gw/internal/auth"
	"github.com/robert-mcdermott/phlox-gw/internal/config"
	"github.com/robert-mcdermott/phlox-gw/internal/httpapi"
	"github.com/robert-mcdermott/phlox-gw/internal/store"
	"github.com/robert-mcdermott/phlox-gw/internal/telemetry"
)

func main() {
	if printVersion(os.Args[1:], os.Stdout) {
		return
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("load config", "error", err)
		os.Exit(1)
	}
	applyBuildDefaults(&cfg)

	db, err := store.OpenWithOptions(store.OpenOptions{
		Driver:               cfg.Database.Driver,
		Path:                 cfg.Database.Path,
		URL:                  cfg.Database.URL,
		MaxOpenConns:         cfg.Database.MaxOpenConns,
		MaxIdleConns:         cfg.Database.MaxIdleConns,
		ConnMaxLifetime:      cfg.Database.ConnMaxLifetime,
		MigrationLockTimeout: cfg.Database.MigrationLockTimeout,
	})
	if err != nil {
		logger.Error("open database", "driver", cfg.Database.Driver, "target", databaseLogTarget(cfg), "error", err)
		os.Exit(1)
	}
	defer db.Close()

	temporaryPassword, err := auth.NewTemporaryPassword()
	if err != nil {
		logger.Error("generate temporary administrator password", "error", err)
		os.Exit(1)
	}
	adminHash, err := auth.HashPassword(temporaryPassword)
	if err != nil {
		logger.Error("hash seed password", "error", err)
		os.Exit(1)
	}
	seedResult, err := db.EnsureBootstrapData(adminHash)
	if err != nil {
		logger.Error("seed database", "error", err)
		os.Exit(1)
	}
	if seedResult.AdminCreated {
		printBootstrapPassword(os.Stdout, temporaryPassword)
	}

	tel, err := telemetry.New(context.Background(), cfg.Telemetry, logger)
	if err != nil {
		logger.Error("initialize telemetry", "error", err)
		os.Exit(1)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tel.Shutdown(ctx); err != nil {
			logger.Warn("telemetry shutdown failed", "error", err)
		}
	}()

	handler, err := httpapi.New(httpapi.Options{
		Config:     cfg,
		Store:      db,
		Frontend:   phloxgw.Frontend,
		Logger:     logger,
		HTTPClient: &http.Client{Timeout: 10 * time.Minute},
		Telemetry:  tel,
	})
	if err != nil {
		logger.Error("initialize api", "error", err)
		os.Exit(1)
	}

	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("phlox-gw listening", "version", phloxgw.Version(), "commit", valueOrUnknown(phloxgw.BuildCommit), "addr", cfg.Addr, "deployment_mode", cfg.Deployment.Mode, "instance_id", cfg.Deployment.InstanceID, "db_driver", cfg.Database.Driver, "db", databaseLogTarget(cfg))
		if cfg.UsingDefaultSecret {
			logger.Warn("using development session secret; set PHLOX_GW_SESSION_SECRET before shared use")
		}
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("shutdown failed", "error", err)
		os.Exit(1)
	}
	if err := db.DeleteClusterNode(context.Background(), cfg.Deployment.InstanceID); err != nil && !errors.Is(err, store.ErrNotFound) {
		logger.Warn("release cluster node registration failed", "instance_id", cfg.Deployment.InstanceID, "error", err)
	}
}

func printVersion(args []string, w io.Writer) bool {
	if len(args) != 1 || args[0] != "--version" {
		return false
	}
	_, _ = fmt.Fprintln(w, phloxgw.VersionString())
	return true
}

func printBootstrapPassword(w io.Writer, password string) {
	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "+------------------------------------------------------------+")
	_, _ = fmt.Fprintln(w, "|  PHLOX-GW FIRST-RUN ADMINISTRATOR                          |")
	_, _ = fmt.Fprintln(w, "+------------------------------------------------------------+")
	_, _ = fmt.Fprintln(w, "|  Username:           admin                                 |")
	_, _ = fmt.Fprintf(w, "|  Temporary password: %-36s |\n", password)
	_, _ = fmt.Fprintln(w, "|                                                            |")
	_, _ = fmt.Fprintln(w, "|  Sign in and choose a new password before continuing.      |")
	_, _ = fmt.Fprintln(w, "|  This password is shown once. Store first-run logs safely. |")
	_, _ = fmt.Fprintln(w, "+------------------------------------------------------------+")
	_, _ = fmt.Fprintln(w, "")
}

func applyBuildDefaults(cfg *config.Config) {
	if cfg.Telemetry.ServiceVersion == "" {
		cfg.Telemetry.ServiceVersion = phloxgw.Version()
	}
}

func valueOrUnknown(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	return value
}

func databaseLogTarget(cfg config.Config) string {
	if cfg.Database.Driver == "postgres" {
		return "postgres"
	}
	return cfg.DBPath
}
