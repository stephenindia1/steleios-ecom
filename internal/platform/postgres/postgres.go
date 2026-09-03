// Package postgres owns the database pool and the unit of work.
//
// It is the only package that imports pgx (DRY-02). Services depend on
// repository interfaces; repositories depend on this. A service signature never
// mentions a transaction (MOD-08).
package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stephenindia1/steleios-ecom/internal/platform/config"
)

// Pool is the application's database handle.
type Pool struct {
	pool *pgxpool.Pool
	log  *slog.Logger
	cfg  config.Postgres
}

// New opens and verifies the pool.
//
// It fails rather than returning a lazily-connecting handle: a process that
// starts without its database is a process that will serve errors, and finding
// out at boot is cheaper than finding out from a customer (HLT-005).
func New(ctx context.Context, cfg config.Postgres, log *slog.Logger) (*Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse dsn: %w", err)
	}

	// DB-045: pool size is configured explicitly, never left to a default, and
	// is sized against the server's max_connections together with the worker.
	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.MinConns = cfg.MinConns
	poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	poolCfg.HealthCheckPeriod = cfg.HealthCheckPeriod
	poolCfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout

	// Every statement is bounded, and a transaction cannot be left open holding
	// locks while a network call hangs (DB-012, DB-034).
	poolCfg.ConnConfig.RuntimeParams["statement_timeout"] =
		fmt.Sprintf("%d", cfg.StatementTimeout.Milliseconds())
	poolCfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] =
		fmt.Sprintf("%d", cfg.IdleInTxTimeout.Milliseconds())
	poolCfg.ConnConfig.RuntimeParams["application_name"] = "steleios-api"
	// Business rules are expressed in IST but stored in UTC; pinning the session
	// timezone removes any dependence on the server's locale (GO-071).
	poolCfg.ConnConfig.RuntimeParams["timezone"] = "UTC"

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: create pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}

	log.Info("postgres connected",
		"max_conns", cfg.MaxConns,
		"min_conns", cfg.MinConns,
		"statement_timeout", cfg.StatementTimeout.String(),
	)

	return &Pool{pool: pool, log: log, cfg: cfg}, nil
}

// Close releases the pool.
func (p *Pool) Close() { p.pool.Close() }

// Health reports whether the database is reachable, for readiness (HLT-002).
func (p *Pool) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := p.pool.Ping(ctx); err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	return nil
}

// Stats exposes pool utilisation for metrics (MET: db_pool_in_use).
func (p *Pool) Stats() (inUse, idle, waiting int32) {
	s := p.pool.Stat()
	return s.AcquiredConns(), s.IdleConns(), int32(s.EmptyAcquireCount()) //nolint:gosec // counters are non-negative
}

// Querier is the subset of pgx a repository needs. Repositories accept this so
// the same code runs inside and outside a transaction (OOP-06).
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Repos is the factory output of a unit of work: every repository bound to the
// same transaction (docs/03 §2.5).
//
// Concrete repositories are attached here as modules are built. Keeping one
// struct means a service asks for the repositories it needs without any module
// learning about pgx.
type Repos struct {
	q Querier
}

// Querier exposes the transaction-bound handle to repository constructors.
func (r Repos) Querier() Querier { return r.q }

// UnitOfWork runs a function inside a single transaction.
//
// This is the only way to make several repositories atomic. Passing a pgx.Tx
// between modules would leak the data layer everywhere (MOD-08).
type UnitOfWork interface {
	Do(ctx context.Context, fn func(Repos) error) error
}

// unitOfWork is the pgx-backed implementation.
type unitOfWork struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

// NewUnitOfWork returns the transaction runner for this pool.
func NewUnitOfWork(p *Pool) UnitOfWork {
	return &unitOfWork{pool: p.pool, log: p.log}
}

// Do runs fn in a transaction, committing on success and rolling back on any
// error or panic.
//
// MOD-09: fn MUST NOT perform network I/O to a third party. A transaction that
// waits on Razorpay holds locks for the duration of someone else's outage.
func (u *unitOfWork) Do(ctx context.Context, fn func(Repos) error) (err error) {
	tx, err := u.pool.BeginTx(ctx, pgx.TxOptions{
		// Read-committed is sufficient because contended writes use atomic
		// conditional updates and explicit row locks rather than relying on the
		// isolation level (DB-030, BR-INV-02, BR-BAT-11).
		IsoLevel: pgx.ReadCommitted,
	})
	if err != nil {
		return fmt.Errorf("postgres: begin: %w", err)
	}

	defer func() {
		if rec := recover(); rec != nil {
			// Roll back before re-panicking, so a panic does not leave a
			// transaction open holding locks.
			_ = tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // the panic is the error being reported
			panic(rec)
		}
		if err != nil {
			// context.WithoutCancel so the rollback still runs when the failure
			// was the context expiring.
			if rbErr := tx.Rollback(context.WithoutCancel(ctx)); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
				u.log.Error("transaction rollback failed", "error", rbErr.Error())
			}
		}
	}()

	if err = fn(Repos{q: tx}); err != nil {
		return err
	}

	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit: %w", err)
	}
	return nil
}

// Read runs fn outside a transaction, for single-statement reads that need no
// atomicity. Using the pool directly avoids the cost of a transaction per read.
func (p *Pool) Read(ctx context.Context, fn func(Repos) error) error {
	return fn(Repos{q: p.pool})
}
