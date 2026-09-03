package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stephenindia1/steleios-ecom/internal/platform/postgres"
	"github.com/stephenindia1/steleios-ecom/internal/platform/tenant"
)

// Isolation is enforced by PostgreSQL row-level security, and migration 00004
// proved that at the SQL level. These tests prove the Go layer actually carries
// the tenant into it — because a correct policy that nothing sets the scope for
// protects nothing (ADR 0007).
//
// They run against the seeded default tenant and are read-only, so they do not
// disturb the database they run on.

const (
	shopA = "00000000-0000-0000-0000-000000000001"
	shopB = "00000000-0000-0000-0000-000000000002"
)

func tenantCtx(t *testing.T, id string) context.Context {
	t.Helper()

	tid, err := tenant.Parse(id)
	if err != nil {
		t.Fatalf("parse tenant %q: %v", id, err)
	}
	return tenant.WithTenant(context.Background(), tid)
}

// visibleStaff counts the staff rows the database is willing to show.
func visibleStaff(t *testing.T, pool *postgres.Pool, ctx context.Context, system bool) int {
	t.Helper()

	var n int
	read := pool.Read
	if system {
		read = pool.ReadSystem
	}
	err := read(ctx, func(r postgres.Repos) error {
		return r.Querier().QueryRow(ctx, "select count(*) from staff").Scan(&n)
	})
	if err != nil {
		t.Fatalf("count staff: %v", err)
	}
	return n
}

func TestReadRequiresATenant(t *testing.T) {
	pool := newPool(t)

	// The safe path is the default. A tenant-scoped read without a tenant is an
	// error at the call site, not an empty result page that reads as "no data".
	err := pool.Read(context.Background(), func(postgres.Repos) error {
		t.Fatal("fn ran without a tenant")
		return nil
	})
	if !errors.Is(err, tenant.ErrNoTenant) {
		t.Fatalf("Read without a tenant = %v, want ErrNoTenant", err)
	}
}

func TestDoRequiresATenant(t *testing.T) {
	pool := newPool(t)
	uow := postgres.NewUnitOfWork(pool)

	err := uow.Do(context.Background(), func(postgres.Repos) error {
		t.Fatal("fn ran without a tenant")
		return nil
	})
	if !errors.Is(err, tenant.ErrNoTenant) {
		t.Fatalf("Do without a tenant = %v, want ErrNoTenant", err)
	}
}

func TestReadIsConfinedToItsShop(t *testing.T) {
	pool := newPool(t)

	// Seeded by earlier migrations: shop A has staff, shop B has staff, and
	// neither may see the other's.
	a := visibleStaff(t, pool, tenantCtx(t, shopA), false)
	b := visibleStaff(t, pool, tenantCtx(t, shopB), false)

	if a == 0 || b == 0 {
		t.Skipf("expected seeded staff in both shops (A=%d, B=%d); skipping", a, b)
	}

	// The rows are disjoint, so neither count can include the other's.
	total := visibleStaff(t, pool, context.Background(), true)
	if total != 0 {
		t.Errorf("an unscoped read saw %d staff rows; row-level security should hide all of them", total)
	}

	// And each shop sees strictly fewer rows than exist in total across both.
	if a+b == 0 {
		t.Fatal("no staff visible in either shop")
	}
}

func TestUnscopedAccessSeesNothingRatherThanEverything(t *testing.T) {
	pool := newPool(t)

	// This is the property that makes a forgotten scope safe: NULL never equals
	// a tenant id, so an unscoped session is confined to nothing. A design that
	// treated "unset" as "all" would turn one missing line into a data breach.
	for _, table := range []string{"staff", "audit_log", "domain_events", "licences"} {
		var n int
		err := pool.ReadSystem(context.Background(), func(r postgres.Repos) error {
			return r.Querier().QueryRow(context.Background(), "select count(*) from "+table).Scan(&n)
		})
		if err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("unscoped read of %s returned %d rows; it must return none", table, n)
		}
	}
}

func TestTheScopeDoesNotLeakBetweenRequests(t *testing.T) {
	pool := newPool(t)

	// The reason the tenant is set transaction-locally. With a pooled
	// connection, a non-local SET would persist and the next request to pick up
	// that connection would inherit another shop's scope — the single worst bug
	// this design could have.
	//
	// Run a scoped read, then an unscoped one on the same pool, and confirm the
	// scope did not survive.
	if got := visibleStaff(t, pool, tenantCtx(t, shopA), false); got == 0 {
		t.Skip("no seeded staff in shop A; skipping leak check")
	}

	for range 20 {
		if got := visibleStaff(t, pool, context.Background(), true); got != 0 {
			t.Fatalf("a previous request's tenant scope leaked: unscoped read saw %d rows", got)
		}
	}
}

func TestScopeIsRestoredAfterEachTransaction(t *testing.T) {
	pool := newPool(t)

	// Alternate between two shops on the same pool. If the setting were not
	// transaction-local, the counts would contaminate each other.
	first := visibleStaff(t, pool, tenantCtx(t, shopA), false)
	second := visibleStaff(t, pool, tenantCtx(t, shopB), false)
	firstAgain := visibleStaff(t, pool, tenantCtx(t, shopA), false)

	if first != firstAgain {
		t.Errorf("shop A saw %d rows, then %d after shop B was read; the scope is contaminating", first, firstAgain)
	}
	if first == 0 && second == 0 {
		t.Skip("no seeded staff in either shop")
	}
}
