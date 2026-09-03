// Command worker consumes the asynq queues.
//
// It builds the same modules from the same composition root as the API and
// registers their background handlers. There is one object graph definition,
// not two (MOD-05).
package main

import (
	"context"
	"fmt"
	"log/slog"
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
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err) //nolint:forbidigo // GO-080: startup failure before the logger exists
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := logging.New(cfg, os.Stdout).With("process", "worker")
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

	redisOpt := asynq.RedisClientOpt{
		Addr:     cfg.Redis.Addr,
		Username: cfg.Redis.Username,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	}

	queue := asynq.NewClient(redisOpt)
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

	srv := asynq.NewServer(redisOpt, asynq.Config{
		// QUE-011: worker concurrency is sized against the database pool. A
		// worker fleet must not be able to exhaust PostgreSQL connections.
		Concurrency: int(cfg.Postgres.MaxConns) / 2,

		// QUE-002: named queues with explicit weights, so a slow bulk export
		// can never delay a payment confirmation.
		Queues: map[string]int{
			"critical": 6, // payment follow-up, invoices
			"default":  3, // email, SMS
			"low":      1, // exports, reindex, reporting refresh
		},

		Logger: asynqLogger{log: log},
		ErrorHandler: asynq.ErrorHandlerFunc(func(ctx context.Context, task *asynq.Task, err error) {
			logging.FromContext(ctx, log).Error("task failed",
				"task_type", task.Type(),
				"error", err.Error())
		}),
	})

	mux := app.Workers(mods)

	log.Info("starting", "config", cfg.Redacted(), "modules", len(mods))

	// asynq's Run blocks and handles its own signal handling, so the worker
	// terminates on the same signals as the API.
	if err := srv.Run(mux); err != nil {
		return fmt.Errorf("worker: %w", err)
	}
	return nil
}

// asynqLogger adapts slog to asynq's logging interface, so worker output is the
// same structured JSON as everything else rather than a second log format
// appearing in the same stream (LOG-001).
type asynqLogger struct{ log *slog.Logger }

func (a asynqLogger) Debug(args ...any) { a.log.Debug(fmt.Sprint(args...)) }
func (a asynqLogger) Info(args ...any)  { a.log.Info(fmt.Sprint(args...)) }
func (a asynqLogger) Warn(args ...any)  { a.log.Warn(fmt.Sprint(args...)) }
func (a asynqLogger) Error(args ...any) { a.log.Error(fmt.Sprint(args...)) }

// Fatal is asynq's most severe level. It is logged at error rather than
// terminating the process here: the server's Run returns and the caller decides
// what to do (GO-027 — no os.Exit from a library-shaped call site).
func (a asynqLogger) Fatal(args ...any) { a.log.Error(fmt.Sprint(args...), "severity", "fatal") }
