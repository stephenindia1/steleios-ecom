// Command api serves the Steleios HTTP API.
//
// It contains process concerns only — flags, signals, server lifecycle. No
// business branching, no SQL, no HTTP handling (MOD-04).
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/hibiken/asynq"
	"github.com/stephenindia1/steleios-ecom/internal/app"
	"github.com/stephenindia1/steleios-ecom/internal/platform/audit"
	"github.com/stephenindia1/steleios-ecom/internal/platform/authz"
	"github.com/stephenindia1/steleios-ecom/internal/platform/clock"
	"github.com/stephenindia1/steleios-ecom/internal/platform/config"
	"github.com/stephenindia1/steleios-ecom/internal/platform/logging"
	"github.com/stephenindia1/steleios-ecom/internal/platform/module"
	"github.com/stephenindia1/steleios-ecom/internal/platform/postgres"
	"github.com/stephenindia1/steleios-ecom/internal/platform/redis"
)

func main() {
	if err := run(); err != nil {
		// Nothing else is guaranteed to be initialised at this point, so this
		// is the one place a bare write to stderr is correct.
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err) //nolint:forbidigo // GO-080: startup failure before the logger exists
		os.Exit(1)
	}
}

func run() error {
	// Signals first, so a Ctrl-C during startup is honoured rather than
	// leaving a half-built process behind.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// HLT-005: configuration is validated once and the process exits non-zero
	// if it is invalid. There is no degraded start.
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := logging.New(cfg, os.Stdout)
	clk := clock.System{}

	db, err := postgres.New(ctx, cfg.Postgres, log)
	if err != nil {
		return err
	}
	defer db.Close()

	rdb, err := redis.New(ctx, cfg.Redis, log)
	if err != nil {
		return err
	}
	defer func() {
		if err := rdb.Close(); err != nil {
			log.Error("redis close failed", "error", err.Error())
		}
	}()

	queue := asynq.NewClient(asynq.RedisClientOpt{
		Addr:     cfg.Redis.Addr,
		Username: cfg.Redis.Username,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})
	defer func() {
		if err := queue.Close(); err != nil {
			log.Error("queue close failed", "error", err.Error())
		}
	}()

	auditor, err := audit.NewWriter(db, clk)
	if err != nil {
		return err
	}

	deps := &module.Deps{
		Cfg:   cfg,
		Log:   log,
		Clock: clk,
		DB:    db,
		UoW:   postgres.NewUnitOfWork(db),
		Redis: rdb,
		Queue: queue,
		Audit: auditor,
		Authz: authz.NewRBAC(),
	}

	mods, err := app.Build(deps)
	if err != nil {
		return err
	}

	router, err := app.Router(deps, mods, rdb)
	if err != nil {
		return err
	}

	// HLT-004: one structured line describing exactly what is running, with
	// secrets redacted, followed by the complete route table — the auditable
	// security surface of the process (SEC-03).
	log.Info("starting", "config", cfg.Redacted(), "modules", len(mods))
	router.LogRouteTable()

	srv := &http.Server{
		Addr:              cfg.HTTP.Addr,
		Handler:           router.Handler(),
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderLimit,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
		ErrorLog:          nil, // net/http errors are surfaced through slog below
	}

	// The server runs in its own goroutine with a known owner and termination
	// condition: it ends when Shutdown is called (GO-050).
	serverErr := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.HTTP.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	select {
	case err := <-serverErr:
		return err
	case <-ctx.Done():
		log.Info("shutdown signal received", "grace", cfg.HTTP.ShutdownGrace.String())
	}

	// Drain in-flight requests before closing the pools the deferred calls
	// above will release. Without the grace period, a customer mid-checkout
	// gets a connection reset.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownGrace)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown failed after %s: %w", cfg.HTTP.ShutdownGrace, err)
	}

	log.Info("stopped cleanly")
	return nil
}
