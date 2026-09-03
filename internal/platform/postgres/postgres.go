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
	"github.com/stephenindia1/steleios-ecom/internal/platform/tenant"
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

	if err := assertRLSApplies(pingCtx, pool); err != nil {
		pool.Close()
		return nil, err
	}

	log.Info("postgres connected",
		"max_conns", cfg.MaxConns,
		"min_conns", cfg.MinConns,
		"statement_timeout", cfg.StatementTimeout.String(),
	)

	return &Pool{pool: pool, log: log, cfg: cfg}, nil
}

// assertRLSApplies refuses to start if the connected role bypasses row-level
// security.
//
// This guard exists because the failure it prevents is silent and total.
// PostgreSQL exempts superusers and BYPASSRLS roles from row-level security
// entirely — not partially, and not with a warning. An instance configured with
// a superuser DSN would have every tenant policy in place, every test written,
// and no isolation whatsoever: one shop's queries would return every shop's
// data, and nothing would look wrong until a customer noticed.
//
// It fails closed in every environment, including local. Local development that
// silently has no isolation is worse than none at all, because it is where the
// bug would have been caught (ADR 0007, BR-SEC-11).
func assertRLSApplies(ctx context.Context, pool *pgxpool.Pool) error {
	var bypasses bool
	err := pool.QueryRow(ctx,
		`select rolsuper or rolbypassrls from pg_roles where rolname = current_user`,
	).Scan(&bypasses)
	if err != nil {
		return fmt.Errorf("postgres: check role privileges: %w", err)
	}

	if bypasses {
		var role string
		_ = pool.QueryRow(ctx, "select current_user").Scan(&role) //nolint:errcheck // best effort, for the message
		return fmt.Errorf(
			"postgres: refusing to start as role %q, which is a superuser or has BYPASSRLS: "+
				"row-level security would not apply and tenant isolation would be silently absent. "+
				"Connect as the application role (steleios_app), not the owner or a superuser",
			role)
	}
	return nil
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

// setTenant scopes the current transaction to one shop.
//
// `set_config(..., true)` is TRANSACTION-LOCAL. That matters enormously with a
// connection pool: a plain SET would persist on the connection and leak into
// whichever request picked it up next — potentially a different shop. Every
// tenant-scoped access therefore runs inside a transaction (ADR 0007).
func setTenant(ctx context.Context, q Querier) error {
	id, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	if _, err := q.Exec(ctx, "select set_config('app.tenant_id', $1, true)", id.String()); err != nil {
		return fmt.Errorf("postgres: scope to tenant: %w", err)
	}
	return nil
}

// UnitOfWork runs a function inside a single transaction.
//
// This is the only way to make several repositories atomic. Passing a pgx.Tx
// between modules would leak the data layer everywhere (MOD-08).
//
// Do requires a tenant; DoSystem is the explicit, greppable escape hatch for
// the few operations that legitimately have no shop.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(Repos) error) error
	DoSystem(ctx context.Context, fn func(Repos) error) error
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

// Do runs fn in a transaction scoped to the current shop.
//
// Every query inside is confined to that shop by row-level security. A missing
// tenant is an error rather than an unscoped transaction, because unscoped
// would silently see nothing and read as "no data" (ADR 0007).
func (u *unitOfWork) Do(ctx context.Context, fn func(Repos) error) error {
	return u.run(ctx, true, fn)
}

// DoSystem runs fn in a transaction with NO shop scope.
//
// Legitimate uses are few and each is deliberate: creating a client or shop
// during onboarding, vendor billing, and background work that spans shops. It
// is named rather than implicit so that `DoSystem` is greppable and every use
// is visible in review (BR-LIC-61).
//
// Row-level security still applies — an unscoped transaction sees NO
// tenant-scoped rows at all, because `tenant_id = NULL` is never true. This is
// not a bypass; it is the absence of a scope.
func (u *unitOfWork) DoSystem(ctx context.Context, fn func(Repos) error) error {
	return u.run(ctx, false, fn)
}

// run is the shared transaction body.
//
// MOD-09: fn MUST NOT perform network I/O to a third party. A transaction that
// waits on an external service holds locks for the duration of someone else's
// outage.
func (u *unitOfWork) run(ctx context.Context, scoped bool, fn func(Repos) error) (err error) {
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

	if scoped {
		if err = setTenant(ctx, tx); err != nil {
			return err
		}
	}

	if err = fn(Repos{q: tx}); err != nil {
		return err
	}

	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit: %w", err)
	}
	return nil
}

// Read runs fn in a read-only transaction scoped to the current shop.
//
// It is a transaction rather than a bare pool call for one reason: the tenant
// setting must be transaction-local, or it would persist on the pooled
// connection and leak into the next request. A read-only transaction is cheap,
// and the alternative — setting the scope non-locally and remembering to reset
// it — is the kind of thing that works until the day it does not.
func (p *Pool) Read(ctx context.Context, fn func(Repos) error) error {
	return p.read(ctx, true, fn)
}

// ReadSystem runs fn in a read-only transaction with NO shop scope.
//
// Used by the paths that legitimately precede a shop: resolving an identity at
// login, before the system knows which shop is being signed in to. Named so it
// is greppable, and it grants nothing — row-level security still hides every
// tenant-scoped row (ADR 0007).
func (p *Pool) ReadSystem(ctx context.Context, fn func(Repos) error) error {
	return p.read(ctx, false, fn)
}

func (p *Pool) read(ctx context.Context, scoped bool, fn func(Repos) error) error {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("postgres: begin read: %w", err)
	}
	// A read-only transaction has nothing to commit, so rollback is the normal
	// exit and its error is not interesting.
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }() //nolint:errcheck // read-only: nothing to lose

	if scoped {
		if err := setTenant(ctx, tx); err != nil {
			return err
		}
	}
	return fn(Repos{q: tx})
}
