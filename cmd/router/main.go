package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jonas/opencode-go-router/internal/auth"
	"github.com/jonas/opencode-go-router/internal/config"
	"github.com/jonas/opencode-go-router/internal/models"
	"github.com/jonas/opencode-go-router/internal/proxy"
	"github.com/jonas/opencode-go-router/internal/server"
	"github.com/jonas/opencode-go-router/internal/store"
	"github.com/jonas/opencode-go-router/internal/usage"
)

var version = "dev"

func main() {
	// Keep database, WAL and log files private to the process user.
	syscall.Umask(0o077)

	cfg, err := config.Load(version)
	if err != nil {
		slog.Error("configuration error", "error", err)
		os.Exit(1)
	}
	log := newLogger(cfg.LogFormat)

	st, err := store.Open(cfg.DatabasePath)
	if err != nil {
		log.Error("could not open database", "error", err)
		os.Exit(1)
	}
	defer st.Close()

	if err := bootstrapAdmin(cfg, st, log); err != nil {
		log.Error("could not initialize admin account", "error", err)
		os.Exit(1)
	}

	maxWindow, err := st.MaxLimitWindow(context.Background())
	if err != nil {
		log.Warn("could not read limit windows", "error", err)
	}
	if maxWindow < 90*86400 {
		maxWindow = 90 * 86400
	}
	tracker := usage.NewTracker(maxWindow)
	events, err := st.RecentTrackerEvents(context.Background(), time.Now().Unix()-maxWindow)
	if err != nil {
		log.Warn("could not preload usage tracker", "error", err)
	}
	for _, e := range events {
		tracker.Add(e.KeyID, e.CreatedAt, e.Cost, e.Input+e.Output+e.CacheRead+e.CacheWrite)
	}
	log.Info("usage tracker ready", "events", len(events), "window_days", maxWindow/86400)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ms := models.New(cfg, log)
	go ms.RefreshLoop(ctx)

	p := proxy.New(cfg, st, tracker, ms, log)

	srv, err := server.New(cfg, st, tracker, ms, p, log)
	if err != nil {
		log.Error("could not initialize server", "error", err)
		os.Exit(1)
	}

	go maintenance(ctx, cfg, st, tracker, log)

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       10 * time.Minute,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	go func() {
		log.Info("OpenCode Go Router listening",
			"addr", cfg.Addr,
			"version", version,
			"database", cfg.DatabasePath,
			"upstream_configured", cfg.APIKey != "",
		)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server failed", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	stop()
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Warn("graceful shutdown timed out", "error", err)
	}
}

func bootstrapAdmin(cfg config.Config, st *store.Store, log *slog.Logger) error {
	_, ok, err := st.AdminHash(context.Background())
	if err != nil {
		return err
	}
	if ok {
		// The environment variable bootstraps the first admin only; a password
		// changed in the dashboard stays authoritative. ADMIN_PASSWORD_RESET is
		// an explicit recovery switch.
		if cfg.AdminReset && cfg.AdminPassword != "" {
			newHash, err := auth.HashPassword(cfg.AdminPassword)
			if err != nil {
				return err
			}
			if err := st.SetAdminHash(context.Background(), newHash); err != nil {
				return err
			}
			if err := st.DeleteAllSessions(context.Background()); err != nil {
				return err
			}
			log.Warn("admin password reset from ADMIN_PASSWORD_RESET; all sessions signed out")
		}
		return nil
	}
	password := cfg.AdminPassword
	generated := false
	if password == "" {
		password = auth.GeneratePassword()
		generated = true
	}
	newHash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	created, err := st.EnsureAdmin(context.Background(), newHash)
	if err != nil {
		return err
	}
	if created {
		if generated {
			log.Warn("=====================================================================")
			log.Warn("No ADMIN_PASSWORD was configured. A temporary password was generated.")
			log.Warn("Sign in with this password and change it in Settings:", "password", password)
			log.Warn("=====================================================================")
		} else {
			log.Info("admin account created from ADMIN_PASSWORD")
		}
	}
	return nil
}

func maintenance(ctx context.Context, cfg config.Config, st *store.Store, tracker *usage.Tracker, log *slog.Logger) {
	cleanup := func() {
		days := cfg.RetentionDays
		if v, ok, err := st.Setting(context.Background(), "retention_days"); err == nil && ok {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				days = n
			}
		}
		// Never delete rows that are still needed to evaluate a rolling limit.
		if maxWindow, err := st.MaxLimitWindow(context.Background()); err == nil && maxWindow > 0 {
			need := int((maxWindow + 86399) / 86400)
			if need > days {
				days = need
			}
		}
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := st.Cleanup(cctx, time.Now().Unix(), days); err != nil {
			log.Warn("data cleanup failed", "error", err)
		}
	}
	cleanup()
	tracker.Prune(time.Now().Unix())

	cleanupTicker := time.NewTicker(6 * time.Hour)
	pruneTicker := time.NewTicker(10 * time.Minute)
	defer cleanupTicker.Stop()
	defer pruneTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-cleanupTicker.C:
			cleanup()
		case <-pruneTicker.C:
			tracker.Prune(time.Now().Unix())
		}
	}
}

func newLogger(format string) *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if strings.EqualFold(format, "json") {
		h = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(h)
}
