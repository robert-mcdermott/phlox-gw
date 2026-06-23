package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	phloxgw "github.com/robert-mcdermott/phlox-gw"
	"github.com/robert-mcdermott/phlox-gw/internal/auth"
	"github.com/robert-mcdermott/phlox-gw/internal/config"
	"github.com/robert-mcdermott/phlox-gw/internal/httpapi"
	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("load config", "error", err)
		os.Exit(1)
	}

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		logger.Error("open database", "path", cfg.DBPath, "error", err)
		os.Exit(1)
	}
	defer db.Close()

	adminHash, err := auth.HashPassword("admin")
	if err != nil {
		logger.Error("hash seed password", "error", err)
		os.Exit(1)
	}
	if err := db.EnsureSeedData(adminHash); err != nil {
		logger.Error("seed database", "error", err)
		os.Exit(1)
	}

	handler, err := httpapi.New(httpapi.Options{
		Config:     cfg,
		Store:      db,
		Frontend:   phloxgw.Frontend,
		Logger:     logger,
		HTTPClient: &http.Client{Timeout: 10 * time.Minute},
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
		logger.Info("phlox-gw listening", "addr", cfg.Addr, "db", cfg.DBPath)
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
}
