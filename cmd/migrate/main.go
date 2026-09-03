// Command migrate applies the embedded database migrations.
//
// Migrations are forward-only and never edited after merge (BR-VER-07). "down"
// exists for local development only and refuses to run outside it, because a
// down migration against production data is a data-loss event dressed as a
// convenience.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver, for goose only
	"github.com/pressly/goose/v3"
	"github.com/stephenindia1/steleios-ecom/internal/platform/config"
	"github.com/stephenindia1/steleios-ecom/internal/platform/logging"
	"github.com/stephenindia1/steleios-ecom/migrations"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err) //nolint:forbidigo // GO-080: may fail before the logger exists
		os.Exit(1)
	}
}

func run() error {
	command := flag.String("command", "up", "up | down | status | version | redo")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := logging.New(cfg, os.Stdout).With("process", "migrate")

	if *command == "down" && cfg.Env != config.EnvLocal && cfg.Env != config.EnvTest {
		return fmt.Errorf("refusing to run %q in %s: migrations are forward-only outside local and test (BR-VER-07)",
			*command, cfg.Env)
	}

	db, err := sql.Open("pgx", cfg.Postgres.DSN)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Error("close failed", "error", err.Error())
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}

	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(gooseLogger{log: log})
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set dialect: %w", err)
	}

	log.Info("applying migrations",
		"command", *command,
		"env", string(cfg.Env),
		"database", cfg.Redacted()["postgres_dsn"])

	if err := goose.RunContext(ctx, *command, db, "."); err != nil {
		return fmt.Errorf("goose %s: %w", *command, err)
	}

	version, err := goose.GetDBVersionContext(ctx, db)
	if err != nil && !errors.Is(err, goose.ErrNoNextVersion) {
		return fmt.Errorf("read schema version: %w", err)
	}

	// The schema version is part of what "what exactly is running" means
	// (HLT-004), so it is logged on every migration run.
	log.Info("migrations complete", "schema_version", version)
	return nil
}

// gooseLogger routes goose's output through slog, so a migration run produces
// the same structured stream as everything else (LOG-001).
type gooseLogger struct{ log *slog.Logger }

func (g gooseLogger) Fatalf(format string, v ...any) {
	g.log.Error(fmt.Sprintf(format, v...), "severity", "fatal")
	os.Exit(1)
}

func (g gooseLogger) Printf(format string, v ...any) {
	g.log.Info(fmt.Sprintf(format, v...))
}
