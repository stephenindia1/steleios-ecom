package identity_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stephenindia1/steleios-ecom/internal/identity"
	"github.com/stephenindia1/steleios-ecom/internal/platform/authz"
	"github.com/stephenindia1/steleios-ecom/internal/platform/config"
	"github.com/stephenindia1/steleios-ecom/internal/platform/postgres"
	"github.com/stephenindia1/steleios-ecom/internal/platform/tenant"
)

// Integration tests for the repository, against a real PostgreSQL running the
// real migrations, connected as the real application role.
//
// These exist because of a bug they would have caught and the service tests
// could not. MembershipsOf joined four tenant-scoped tables on the system path,
// where current_tenant_id() is NULL, so it returned NOTHING — for every user,
// always. Sign-in "succeeded" and handed back an empty shop list, which meant
// nobody could reach a shop at all. The fakes in service_test.go answered
// happily; only the database knows about row-level security (migration 00017).
//
// The lesson generalises: a repository that reads tenant-scoped tables cannot
// be verified by a fake, because the thing that can go wrong lives in the
// database, not in the Go.

func repoDSN(t *testing.T) string {
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

func adminConn(t *testing.T) *pgx.Conn {
	t.Helper()

	dsn := os.Getenv("POSTGRES_ADMIN_DSN")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("POSTGRES_ADMIN_DSN is unset in CI: seeding needs privileged DDL/DML")
		}
		t.Skip("POSTGRES_ADMIN_DSN unset; skipping tests that seed the schema")
	}

	// Seeding runs privileged and the code under test does not. Running the
	// code under test with more privilege than production is exactly how an
	// isolation bug hides — a superuser is exempt from row-level security
	// entirely.
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("admin connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) }) //nolint:errcheck // teardown
	return conn
}

func repoPool(t *testing.T) *postgres.Pool {
	t.Helper()

	pool, err := postgres.New(context.Background(), config.Postgres{
		DSN:               repoDSN(t),
		MaxConns:          4,
		MinConns:          1,
		MaxConnLifetime:   time.Hour,
		MaxConnIdleTime:   10 * time.Minute,
		ConnectTimeout:    5 * time.Second,
		StatementTimeout:  3 * time.Second,
		IdleInTxTimeout:   10 * time.Second,
		HealthCheckPeriod: 30 * time.Second,
	}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// seeded is one person with one shop, all rows removed afterwards.
type seeded struct {
	identityID uuid.UUID
	email      string
	tenantID   tenant.ID
	shopCode   string
	clientCode string
}

// seed inserts a client, a shop, an identity, a staff row and a role, and
// removes them again. suffix keeps parallel tests from colliding on the unique
// codes.
func seed(t *testing.T, role authz.Role, suffix string) seeded {
	t.Helper()

	ctx := context.Background()
	conn := adminConn(t)

	// A per-run token keeps the unique codes unique across runs, so a crash
	// that skips cleanup cannot wedge the next run on a duplicate key.
	suffix += "-" + uuid.NewString()[:8]

	var s seeded
	s.shopCode = "T" + suffix
	s.clientCode = "TC" + suffix
	s.email = "person-" + suffix + "@test.example"

	var clientID, tenantID, staffID uuid.UUID
	err := conn.QueryRow(ctx, `
with c as (
  insert into clients (client_code, legal_name, contact_email, status)
  values ($1, 'Test Client', $2, 'active') returning id
), t as (
  insert into tenants (slug, legal_name, client_id, shop_code, status)
  select $3, 'Test Shop', c.id, $4, 'active' from c returning id, client_id
), i as (
  insert into identities (email, full_name, password_hash, status)
  values ($5, 'Test Person', 'not-a-real-hash', 'active') returning id
), s as (
  insert into staff (tenant_id, identity_id, status)
  select t.id, i.id, 'active' from t, i returning id, tenant_id
), r as (
  insert into staff_role_assignments (staff_id, role_code, granted_by, tenant_id)
  select s.id, $6, s.id, s.tenant_id from s returning staff_id
)
select (select client_id from t), (select id from t), (select id from i), (select id from s)`,
		s.clientCode, "client-"+suffix+"@test.example", "slug-"+suffix, s.shopCode,
		s.email, string(role),
	).Scan(&clientID, &tenantID, &s.identityID, &staffID)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	s.tenantID = tenant.ID(tenantID)

	// Deleted child-first, since every one of these is a foreign key away from
	// the next. Each statement carries its own argument: a shared argument list
	// across statements is a silent no-op, because a statement only ever sees
	// the parameters it names, numbered from $1.
	t.Cleanup(func() {
		ctx := context.Background()
		steps := []struct {
			sql string
			arg any
		}{
			{`delete from staff_role_assignments where staff_id = $1`, staffID},
			{`delete from sessions where identity_id = $1`, s.identityID},
			{`delete from staff where id = $1`, staffID},
			{`delete from tenants where id = $1`, tenantID},
			{`delete from clients where id = $1`, clientID},
			{`delete from identities where id = $1`, s.identityID},
		}
		for _, step := range steps {
			if _, err := conn.Exec(ctx, step.sql, step.arg); err != nil {
				// Fail rather than log: a test that leaves rows behind wedges
				// every later run on the unique codes below, and the failure
				// would surface far from its cause.
				t.Errorf("cleanup %q: %v", step.sql, err)
			}
		}
	})
	return s
}

// TestMembershipsAreReadableBeforeAShopIsChosen is the regression test for the
// bug in migration 00017.
//
// It runs as the application role with NO tenant set, which is the state every
// sign-in is in, because choosing a shop is what this lookup is for.
func TestMembershipsAreReadableBeforeAShopIsChosen(t *testing.T) {
	pool := repoPool(t)
	s := seed(t, authz.RoleManager, "mem1")

	repo := identity.NewRepository(pool, postgres.NewUnitOfWork(pool))

	got, err := repo.MembershipsOf(context.Background(), s.identityID)
	if err != nil {
		t.Fatalf("MembershipsOf: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d memberships, want 1 — a person who cannot see their shops cannot sign in anywhere", len(got))
	}

	m := got[0]
	if m.TenantID != s.tenantID {
		t.Errorf("tenant = %s, want %s", m.TenantID, s.tenantID)
	}
	if m.ShopCode != s.shopCode {
		t.Errorf("shop_code = %q, want %q", m.ShopCode, s.shopCode)
	}
	if m.ClientCode != s.clientCode {
		t.Errorf("client_code = %q, want %q", m.ClientCode, s.clientCode)
	}
	if !m.IsActive() {
		t.Errorf("status = %q, want active", m.Status)
	}
	if len(m.Roles) != 1 || m.Roles[0] != authz.RoleManager {
		t.Errorf("roles = %v, want [manager]", m.Roles)
	}
}

// TestMembershipsOfReturnsOnlyThatIdentitysShops is the isolation half.
//
// The membership lookup deliberately bypasses row-level security, so its own
// filter is the only thing keeping two clients' staff apart. That filter needs
// a test more than a policy would, not less.
func TestMembershipsOfReturnsOnlyThatIdentitysShops(t *testing.T) {
	pool := repoPool(t)
	mine := seed(t, authz.RoleManager, "isoA")
	theirs := seed(t, authz.RoleOwner, "isoB")

	repo := identity.NewRepository(pool, postgres.NewUnitOfWork(pool))

	got, err := repo.MembershipsOf(context.Background(), mine.identityID)
	if err != nil {
		t.Fatalf("MembershipsOf: %v", err)
	}
	for _, m := range got {
		if m.TenantID == theirs.tenantID {
			t.Fatalf("identity %s can see another client's shop %s", mine.identityID, m.TenantID)
		}
	}
	if len(got) != 1 {
		t.Fatalf("got %d memberships, want exactly the one seeded", len(got))
	}
}

func TestMembershipInFindsTheRightShopAndRefusesTheRest(t *testing.T) {
	pool := repoPool(t)
	mine := seed(t, authz.RoleCounterSales, "inA")
	theirs := seed(t, authz.RoleOwner, "inB")

	repo := identity.NewRepository(pool, postgres.NewUnitOfWork(pool))
	ctx := context.Background()

	m, err := repo.MembershipIn(ctx, mine.identityID, mine.tenantID)
	if err != nil {
		t.Fatalf("MembershipIn(own shop): %v", err)
	}
	if len(m.Roles) != 1 || m.Roles[0] != authz.RoleCounterSales {
		t.Errorf("roles = %v, want [counter_sales]", m.Roles)
	}

	// The check that stops a valid session scoping itself to any tenant id it
	// likes, which row-level security would then faithfully serve.
	if _, err := repo.MembershipIn(ctx, mine.identityID, theirs.tenantID); err == nil {
		t.Fatal("MembershipIn returned another client's shop")
	}
}

func TestFindByEmailWorksWithoutATenant(t *testing.T) {
	pool := repoPool(t)
	s := seed(t, authz.RoleManager, "email1")

	repo := identity.NewRepository(pool, postgres.NewUnitOfWork(pool))
	ctx := context.Background()

	got, err := repo.FindByEmail(ctx, s.email)
	if err != nil {
		t.Fatalf("FindByEmail: %v", err)
	}
	if got.ID != s.identityID {
		t.Errorf("id = %s, want %s", got.ID, s.identityID)
	}

	if _, err := repo.FindByEmail(ctx, "nobody-at-all@test.example"); err == nil {
		t.Error("FindByEmail found an address that does not exist")
	}
}

// TestTheApplicationRoleStillCannotReadStaffDirectly confirms the fix did not
// quietly disable the isolation it works around.
//
// The bypass is confined to the function. The tables themselves must still
// answer nothing to an application connection with no tenant set.
func TestTheApplicationRoleStillCannotReadStaffDirectly(t *testing.T) {
	pool := repoPool(t)
	seed(t, authz.RoleManager, "direct1")

	ctx := context.Background()
	for _, table := range []string{"staff", "tenants", "staff_role_assignments", "clients"} {
		var n int
		err := pool.ReadSystem(ctx, func(r postgres.Repos) error {
			return r.Querier().QueryRow(ctx, "select count(*) from "+table).Scan(&n)
		})
		if err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("%s returned %d rows with no tenant set; isolation is off", table, n)
		}
	}
}

// TestTheDatabaseRoleCatalogueMatchesTheCode is what stops the two lists
// drifting apart again.
//
// staff_role_assignments.role_code has a foreign key to staff_roles, so a role
// that authz grants actions to but the table does not list cannot be assigned
// to anyone — it exists in the code, is documented, is reasoned about, and does
// nothing. That is exactly what had happened to delivery, saas_admin and
// saas_support (migration 00018).
//
// The reverse direction matters too: a code in the table that authz does not
// know is a role that can be granted and confers nothing, which is worse,
// because the grant appears to have worked.
func TestTheDatabaseRoleCatalogueMatchesTheCode(t *testing.T) {
	conn := adminConn(t)
	ctx := context.Background()

	rows, err := conn.Query(ctx, `select code from staff_roles order by code`)
	if err != nil {
		t.Fatalf("read staff_roles: %v", err)
	}
	defer rows.Close()

	inDB := map[string]bool{}
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			t.Fatalf("scan: %v", err)
		}
		inDB[code] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	inCode := map[string]bool{}
	for _, r := range authz.AllRoles() {
		inCode[string(r)] = true
		if !inDB[string(r)] {
			t.Errorf("authz grants actions to %q but staff_roles does not list it: it cannot be assigned to anybody", r)
		}
	}
	for code := range inDB {
		if !inCode[code] {
			t.Errorf("staff_roles lists %q but authz grants it nothing: granting it would appear to work and confer no access", code)
		}
	}
}
