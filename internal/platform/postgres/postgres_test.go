package postgres_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stephenindia1/steleios-ecom/internal/platform/config"
	"github.com/stephenindia1/steleios-ecom/internal/platform/postgres"
)

// These are integration tests against a real PostgreSQL. Mocking the database
// here would prove nothing: what is under test is transaction behaviour, which
// only the database can demonstrate (GO-093).
//
// They skip when POSTGRES_DSN is unset so a contributor without a database can
// still run the suite — but they FAIL in CI, where the database is provided,
// because a test that silently never runs is worse than no test at all.

func dsn(t *testing.T) string {
	t.Helper()

	if v := os.Getenv("POSTGRES_DSN"); v != "" {
		return v
	}
	if os.Getenv("CI") != "" {
		t.Fatal("POSTGRES_DSN is unset in CI: these tests must run there")
	}
	t.Skip("POSTGRES_DSN unset; skipping database integration tests")
	return ""
}

func testConfig(t *testing.T) config.Postgres {
	t.Helper()
	return config.Postgres{
		DSN:               dsn(t),
		MaxConns:          8,
		MinConns:          1,
		MaxConnLifetime:   time.Hour,
		MaxConnIdleTime:   10 * time.Minute,
		ConnectTimeout:    5 * time.Second,
		StatementTimeout:  3 * time.Second,
		IdleInTxTimeout:   10 * time.Second,
		HealthCheckPeriod: 30 * time.Second,
	}
}

func newPool(t *testing.T) *postgres.Pool {
	t.Helper()

	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	pool, err := postgres.New(context.Background(), testConfig(t), log)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// scratch creates a table for one test and drops it afterwards, so tests do not
// depend on application schema or on each other.
//
// DDL runs on an ADMIN connection, not the application pool. The application
// role deliberately cannot create tables (least privilege, migration 00004),
// which is correct — so test setup uses the privileged connection exactly as
// migrations do, while the code under test runs as the application role.
// Setting up a test with more privilege than the code has is normal; running
// the code with more privilege than production is how isolation bugs hide.
func scratch(t *testing.T, name string) {
	t.Helper()
	ctx := context.Background()

	adminDSN := os.Getenv("POSTGRES_ADMIN_DSN")
	if adminDSN == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("POSTGRES_ADMIN_DSN is unset in CI: transaction tests need it for scratch DDL")
		}
		t.Skip("POSTGRES_ADMIN_DSN unset; skipping tests that need scratch tables")
	}

	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatalf("admin connect: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) }) //nolint:errcheck // test teardown

	exec := func(sql string) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql); err != nil {
			t.Fatalf("scratch %s: %v", sql, err)
		}
	}

	exec("drop table if exists " + name)
	exec("create table " + name + " (id int primary key, note text)")
	// The application role must be able to use it, the same way it can use any
	// table a migration creates.
	exec("grant select, insert, update, delete on " + name + " to steleios_app")

	t.Cleanup(func() { exec("drop table if exists " + name) })
}

func count(t *testing.T, pool *postgres.Pool, table string) int {
	t.Helper()

	var n int
	err := pool.ReadSystem(context.Background(), func(r postgres.Repos) error {
		return r.Querier().QueryRow(context.Background(), "select count(*) from "+table).Scan(&n)
	})
	if err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestConnectAndHealth(t *testing.T) {
	pool := newPool(t)

	if err := pool.Health(context.Background()); err != nil {
		t.Errorf("Health: %v", err)
	}

	inUse, idle, _ := pool.Stats()
	if inUse < 0 || idle < 0 {
		t.Errorf("pool stats are negative: in use %d, idle %d", inUse, idle)
	}
}

func TestConnectFailsFastOnABadDSN(t *testing.T) {
	_ = dsn(t) // skip consistently when there is no database at all

	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := testConfig(t)
	cfg.DSN = "postgres://nobody:wrong@127.0.0.1:1/nope?sslmode=disable"
	cfg.ConnectTimeout = 2 * time.Second

	// HLT-005: a process that cannot reach its database must fail to start,
	// not start and serve errors.
	if _, err := postgres.New(context.Background(), cfg, log); err == nil {
		t.Fatal("New succeeded against an unreachable database")
	}
}

func TestUnitOfWorkCommits(t *testing.T) {
	pool := newPool(t)
	scratch(t, "uow_commit_test")

	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()

	err := uow.DoSystem(ctx, func(r postgres.Repos) error {
		for i := range 3 {
			if _, err := r.Querier().Exec(ctx,
				"insert into uow_commit_test (id, note) values ($1, $2)", i, "kept"); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	if got := count(t, pool, "uow_commit_test"); got != 3 {
		t.Errorf("committed %d rows, want 3", got)
	}
}

func TestUnitOfWorkRollsBackOnError(t *testing.T) {
	pool := newPool(t)
	scratch(t, "uow_rollback_test")

	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	sentinel := errors.New("business rule refused")

	// The property that matters: work done before the failure must not survive.
	// This is what BR-CHK-01 relies on — reprice, reserve stock and create the
	// order, or none of it.
	err := uow.DoSystem(ctx, func(r postgres.Repos) error {
		if _, err := r.Querier().Exec(ctx,
			"insert into uow_rollback_test (id, note) values (1, 'should vanish')"); err != nil {
			return err
		}
		return sentinel
	})

	if !errors.Is(err, sentinel) {
		t.Fatalf("Do returned %v, want the caller's own error unwrapped", err)
	}
	if got := count(t, pool, "uow_rollback_test"); got != 0 {
		t.Errorf("%d rows survived a rolled-back transaction", got)
	}
}

func TestUnitOfWorkRollsBackOnPanic(t *testing.T) {
	pool := newPool(t)
	scratch(t, "uow_panic_test")

	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()

	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic did not propagate; a caller would never learn of it")
			}
		}()

		_ = uow.DoSystem(ctx, func(r postgres.Repos) error {
			if _, err := r.Querier().Exec(ctx,
				"insert into uow_panic_test (id, note) values (1, 'should vanish')"); err != nil {
				return err
			}
			panic("something went badly wrong mid-transaction")
		})
	}()

	// A panic must not leave a transaction open holding locks, and must not
	// commit half a change.
	if got := count(t, pool, "uow_panic_test"); got != 0 {
		t.Errorf("%d rows survived a panicking transaction", got)
	}
}

func TestUnitOfWorkRollsBackWhenTheContextIsCancelled(t *testing.T) {
	pool := newPool(t)
	scratch(t, "uow_cancel_test")

	uow := postgres.NewUnitOfWork(pool)
	ctx, cancel := context.WithCancel(context.Background())

	err := uow.DoSystem(ctx, func(r postgres.Repos) error {
		if _, err := r.Querier().Exec(ctx,
			"insert into uow_cancel_test (id, note) values (1, 'should vanish')"); err != nil {
			return err
		}
		// BR-RCV-05: the customer closed the tab mid-checkout. Rollback runs on
		// a context detached from the cancelled one, so the failure that caused
		// the cancellation cannot also prevent the cleanup.
		cancel()
		return ctx.Err()
	})

	if err == nil {
		t.Fatal("Do returned nil for a cancelled transaction")
	}
	if got := count(t, pool, "uow_cancel_test"); got != 0 {
		t.Errorf("%d rows survived a cancelled transaction", got)
	}
}

func TestUnitOfWorkIsAtomicAcrossSeveralStatements(t *testing.T) {
	pool := newPool(t)
	scratch(t, "uow_atomic_test")

	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()

	// A constraint violation partway through must undo everything before it —
	// the multi-repository atomicity checkout depends on (docs/03 §2.5).
	err := uow.DoSystem(ctx, func(r postgres.Repos) error {
		if _, err := r.Querier().Exec(ctx,
			"insert into uow_atomic_test (id, note) values (1, 'first')"); err != nil {
			return err
		}
		if _, err := r.Querier().Exec(ctx,
			"insert into uow_atomic_test (id, note) values (2, 'second')"); err != nil {
			return err
		}
		// Duplicate primary key.
		_, err := r.Querier().Exec(ctx,
			"insert into uow_atomic_test (id, note) values (1, 'clash')")
		return err
	})

	if err == nil {
		t.Fatal("the duplicate key did not surface as an error")
	}
	if got := count(t, pool, "uow_atomic_test"); got != 0 {
		t.Errorf("%d rows survived; the transaction was not atomic", got)
	}
}

func TestStatementTimeoutIsApplied(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()

	// DB-012: every statement is bounded. Without this, one pathological query
	// holds a connection until someone notices.
	start := time.Now() //nolint:forbidigo // measuring elapsed wall time is the point
	err := pool.ReadSystem(ctx, func(r postgres.Repos) error {
		_, err := r.Querier().Exec(ctx, "select pg_sleep(10)")
		return err
	})
	elapsed := time.Since(start) //nolint:forbidigo // as above

	if err == nil {
		t.Fatal("a 10-second statement completed; the statement timeout is not applied")
	}
	if elapsed > 8*time.Second {
		t.Errorf("statement ran for %s before failing; the timeout is not taking effect", elapsed)
	}
}

func TestReadDoesNotOpenATransaction(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()

	// Read exists so a single-statement lookup does not pay for a transaction.
	// If it silently opened one, idle-in-transaction timeouts and lock holding
	// would apply to every read in the system.
	var inTx bool
	err := pool.ReadSystem(ctx, func(r postgres.Repos) error {
		return r.Querier().QueryRow(ctx,
			"select pg_current_xact_id_if_assigned() is not null").Scan(&inTx)
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if inTx {
		t.Error("Read assigned a transaction id; it should not open a transaction")
	}
}
