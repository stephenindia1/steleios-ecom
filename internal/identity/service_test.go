package identity_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stephenindia1/steleios-ecom/internal/identity"
	"github.com/stephenindia1/steleios-ecom/internal/platform/audit"
	"github.com/stephenindia1/steleios-ecom/internal/platform/authz"
	"github.com/stephenindia1/steleios-ecom/internal/platform/clock"
	"github.com/stephenindia1/steleios-ecom/internal/platform/passwd"
	"github.com/stephenindia1/steleios-ecom/internal/platform/session"
	"github.com/stephenindia1/steleios-ecom/internal/platform/tenant"
)

// The service is tested against fakes rather than a database (GO-093): what is
// under test is the sequence of security decisions — when to count a failure,
// when to lock out, what an actor carries — and none of that is a property of
// PostgreSQL. The repository has its own integration tests.

const (
	shopA = "00000000-0000-0000-0000-000000000001"
	shopB = "00000000-0000-0000-0000-000000000002"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

type fakeRepo struct {
	byEmail map[string]identity.Identity
	byID    map[uuid.UUID]identity.Identity
	members map[uuid.UUID][]identity.Membership

	failErr error

	failedLogins  int
	lockedUntil   *time.Time
	successes     int
	passwordSaved string
}

func newRepo() *fakeRepo {
	return &fakeRepo{
		byEmail: map[string]identity.Identity{},
		byID:    map[uuid.UUID]identity.Identity{},
		members: map[uuid.UUID][]identity.Membership{},
	}
}

func (f *fakeRepo) add(i identity.Identity, m ...identity.Membership) {
	f.byEmail[i.Email] = i
	f.byID[i.ID] = i
	f.members[i.ID] = m
}

func (f *fakeRepo) FindByEmail(_ context.Context, email string) (identity.Identity, error) {
	if f.failErr != nil {
		return identity.Identity{}, f.failErr
	}
	i, ok := f.byEmail[email]
	if !ok {
		return identity.Identity{}, identity.ErrNoSuchUser
	}
	return i, nil
}

func (f *fakeRepo) FindByID(_ context.Context, id uuid.UUID) (identity.Identity, error) {
	if f.failErr != nil {
		return identity.Identity{}, f.failErr
	}
	i, ok := f.byID[id]
	if !ok {
		return identity.Identity{}, identity.ErrNoSuchUser
	}
	return i, nil
}

func (f *fakeRepo) MembershipsOf(_ context.Context, id uuid.UUID) ([]identity.Membership, error) {
	return f.members[id], nil
}

func (f *fakeRepo) MembershipIn(_ context.Context, id uuid.UUID, t tenant.ID) (identity.Membership, error) {
	for _, m := range f.members[id] {
		if m.TenantID == t && m.IsActive() {
			return m, nil
		}
	}
	return identity.Membership{}, identity.ErrNotAMember
}

func (f *fakeRepo) RecordFailedLogin(_ context.Context, id uuid.UUID, lockUntil *time.Time) error {
	f.failedLogins++
	if lockUntil != nil {
		f.lockedUntil = lockUntil
	}

	// The real repository does `failed_logins = failed_logins + 1` in the row,
	// so the NEXT read sees the higher count — which is what drives the
	// lockout threshold. A fake that only counted its own calls would leave the
	// stored identity at zero for ever, and the threshold would never be
	// reached. An unfaithful fake tests nothing.
	i, ok := f.byID[id]
	if !ok {
		return nil
	}
	i.FailedLogins++
	i.LockedUntil = lockUntil
	f.byID[id] = i
	f.byEmail[i.Email] = i
	return nil
}

func (f *fakeRepo) RecordSuccessfulLogin(_ context.Context, _ uuid.UUID, _ time.Time) error {
	f.successes++
	return nil
}

func (f *fakeRepo) UpdatePassword(_ context.Context, id uuid.UUID, hash string, _ time.Time) error {
	f.passwordSaved = hash
	i := f.byID[id]
	i.PasswordHash = hash
	i.MustChangePassword = false
	f.byID[id] = i
	f.byEmail[i.Email] = i
	return nil
}

type fakeSessions struct {
	issued    map[string]session.Session
	revoked   map[string]string
	revokeAll int
	nextToken int
}

func newSessions() *fakeSessions {
	return &fakeSessions{issued: map[string]session.Session{}, revoked: map[string]string{}}
}

func (f *fakeSessions) Issue(_ context.Context, id uuid.UUID, at authz.ActorType, _, _ string) (string, session.Session, error) {
	f.nextToken++
	token := uuid.New().String()
	s := session.Session{IdentityID: id, ActorType: at, Fingerprint: "fp"}
	f.issued[token] = s
	return token, s, nil
}

func (f *fakeSessions) Resolve(_ context.Context, token string) (session.Session, error) {
	if _, gone := f.revoked[token]; gone {
		return session.Session{}, session.ErrNotFound
	}
	s, ok := f.issued[token]
	if !ok {
		return session.Session{}, session.ErrNotFound
	}
	return s, nil
}

func (f *fakeSessions) Touch(context.Context, string, session.Session) (bool, error) {
	return false, nil
}

func (f *fakeSessions) SelectShop(_ context.Context, token string, t tenant.ID) error {
	s, ok := f.issued[token]
	if !ok {
		return session.ErrNotFound
	}
	s.TenantID = t.UUID()
	f.issued[token] = s
	return nil
}

func (f *fakeSessions) Revoke(_ context.Context, token, reason string) error {
	f.revoked[token] = reason
	return nil
}

func (f *fakeSessions) RevokeAll(_ context.Context, id uuid.UUID, reason string) (int64, error) {
	var n int64
	for token, s := range f.issued {
		if s.IdentityID == id {
			f.revoked[token] = reason
			n++
		}
	}
	f.revokeAll++
	return n, nil
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

type fixture struct {
	svc    *identity.Service
	repo   *fakeRepo
	sess   *fakeSessions
	audit  *audit.Recording
	clk    *clock.Fake
	hasher *passwd.Hasher
}

func newFixture(t *testing.T) fixture {
	t.Helper()

	// Cheap parameters: the algorithm under test here is the sign-in sequence,
	// not Argon2id, which has its own tests.
	h, err := passwd.New(passwd.Params{
		Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
	})
	if err != nil {
		t.Fatalf("hasher: %v", err)
	}

	repo := newRepo()
	sess := newSessions()
	rec := &audit.Recording{}
	clk := clock.NewFake(time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC))
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))

	svc, err := identity.NewService(repo, sess, h, rec, clk, log)
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	return fixture{svc: svc, repo: repo, sess: sess, audit: rec, clk: clk, hasher: h}
}

func (f fixture) user(t *testing.T, email, password string, shops ...string) identity.Identity {
	t.Helper()

	hash, err := f.hasher.Hash(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	i := identity.Identity{
		ID: uuid.New(), Email: email, FullName: "Test Person",
		Status: identity.StatusActive, PasswordHash: hash,
	}

	memberships := make([]identity.Membership, 0, len(shops))
	for _, s := range shops {
		tid, err := tenant.Parse(s)
		if err != nil {
			t.Fatalf("tenant: %v", err)
		}
		memberships = append(memberships, identity.Membership{
			StaffID: uuid.New(), TenantID: tid, Status: identity.StatusActive,
			Roles: []authz.Role{authz.RoleManager},
		})
	}
	f.repo.add(i, memberships...)
	return i
}

// ---------------------------------------------------------------------------
// Sign-in
// ---------------------------------------------------------------------------

func TestAuthenticateSucceeds(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.user(t, "owner@shop.test", "a good long password", shopA)

	got, err := f.svc.Authenticate(context.Background(), "owner@shop.test", "a good long password", "203.0.113.1", "agent")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got.Token == "" {
		t.Error("no session token was issued")
	}
	if got.NeedsShopSelection {
		t.Error("a person with one shop should not be asked to choose")
	}
	if f.repo.successes != 1 {
		t.Errorf("recorded %d successful logins, want 1", f.repo.successes)
	}
	if !f.audit.Has("identity.signed_in", got.Identity.ID.String()) {
		t.Error("the sign-in was not audited")
	}
}

func TestAuthenticateFailuresAreIndistinguishable(t *testing.T) {
	t.Parallel()

	// BR-IDN-06. Each of these must be reported to the client identically, so
	// the login form cannot be used to discover which addresses have accounts.
	// The service returns distinct errors for logging; IsAuthFailure is what
	// the handler uses to collapse them.
	f := newFixture(t)
	f.user(t, "real@shop.test", "a good long password", shopA)

	blocked := identity.Identity{
		ID: uuid.New(), Email: "blocked@shop.test", Status: identity.StatusBlocked,
		PasswordHash: mustHash(t, f.hasher, "a good long password"),
	}
	f.repo.add(blocked)

	suspended := identity.Identity{
		ID: uuid.New(), Email: "suspended@shop.test", Status: identity.StatusSuspended,
		PasswordHash: mustHash(t, f.hasher, "a good long password"),
	}
	f.repo.add(suspended)

	cases := []struct {
		name, email, password string
		want                  error
	}{
		{name: "no such user", email: "nobody@shop.test", password: "a good long password", want: identity.ErrNoSuchUser},
		{name: "wrong password", email: "real@shop.test", password: "the wrong password", want: identity.ErrBadPassword},
		{name: "blocked", email: "blocked@shop.test", password: "a good long password", want: identity.ErrBlocked},
		{name: "suspended", email: "suspended@shop.test", password: "a good long password", want: identity.ErrNotActive},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.svc.Authenticate(context.Background(), tc.email, tc.password, "", "")
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if !identity.IsAuthFailure(err) {
				t.Errorf("%v is not classified as an auth failure, so the handler would leak it", err)
			}
		})
	}
}

func TestUnknownUserStillCostsAPasswordVerification(t *testing.T) {
	t.Parallel()

	// The timing defence. An unknown address must cost roughly what a known one
	// does, or the response time alone reveals which accounts exist — a ~100ms
	// difference at production cost, trivially measurable over a few requests.
	//
	// Measuring the two is inherently noisy, so this asserts the thing that
	// actually causes the cost: that a verification happens at all. If the
	// early return were reintroduced, the unknown-user path would be orders of
	// magnitude faster than the known one.
	f := newFixture(t)
	f.user(t, "real@shop.test", "a good long password", shopA)
	ctx := context.Background()

	const attempts = 12
	var unknownTotal, knownTotal time.Duration

	for range attempts {
		start := time.Now()
		_, _ = f.svc.Authenticate(ctx, "nobody@shop.test", "a good long password", "", "")
		unknownTotal += time.Since(start)

		start = time.Now()
		_, _ = f.svc.Authenticate(ctx, "real@shop.test", "the wrong password", "", "")
		knownTotal += time.Since(start)
	}

	unknown := unknownTotal / attempts
	known := knownTotal / attempts

	// An early return would make the unknown path essentially free. Allowing a
	// 10x margin keeps this stable on a loaded CI machine while still failing
	// loudly if the verification is removed.
	if known > 0 && unknown*10 < known {
		t.Errorf("an unknown user answered in %s against %s for a known one; "+
			"the timing guard appears to be missing", unknown, known)
	}
}

func TestLockoutAfterRepeatedFailures(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	user := f.user(t, "owner@shop.test", "a good long password", shopA)
	ctx := context.Background()

	// BR-IDN-11: online guessing is stopped by a temporary lockout.
	for i := range 4 {
		if _, err := f.svc.Authenticate(ctx, user.Email, "wrong", "", ""); !errors.Is(err, identity.ErrBadPassword) {
			t.Fatalf("attempt %d: got %v, want ErrBadPassword", i, err)
		}
		if f.repo.lockedUntil != nil {
			t.Fatalf("locked out after only %d attempts", i+1)
		}
	}

	if _, err := f.svc.Authenticate(ctx, user.Email, "wrong", "", ""); !errors.Is(err, identity.ErrBadPassword) {
		t.Fatalf("fifth attempt: %v", err)
	}
	if f.repo.lockedUntil == nil {
		t.Fatal("no lockout was applied after the threshold")
	}

	// It must be TEMPORARY. A permanent lockout is a denial of service anyone
	// can inflict on anyone by guessing wrong on purpose.
	if !f.repo.lockedUntil.After(f.clk.Now()) {
		t.Error("the lockout is not in the future")
	}
	if f.repo.lockedUntil.Sub(f.clk.Now()) > time.Hour {
		t.Errorf("lockout of %s is too long to be a lockout rather than a ban",
			f.repo.lockedUntil.Sub(f.clk.Now()))
	}
}

func TestSuccessfulLoginClearsFailures(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	user := f.user(t, "owner@shop.test", "a good long password", shopA)
	ctx := context.Background()

	if _, err := f.svc.Authenticate(ctx, user.Email, "wrong", "", ""); err == nil {
		t.Fatal("expected a failure")
	}
	if _, err := f.svc.Authenticate(ctx, user.Email, "a good long password", "", ""); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if f.repo.successes != 1 {
		t.Error("the successful login was not recorded, so the counter would never clear")
	}
}

// ---------------------------------------------------------------------------
// Shops
// ---------------------------------------------------------------------------

func TestOneShopIsSelectedAutomatically(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.user(t, "owner@shop.test", "a good long password", shopA)

	got, err := f.svc.Authenticate(context.Background(), "owner@shop.test", "a good long password", "", "")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	sess, err := f.sess.Resolve(context.Background(), got.Token)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !sess.HasTenant() {
		t.Error("a single shop should be selected automatically rather than shown as a list of one")
	}
}

func TestTwoShopsRequireAChoice(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.user(t, "owner@shop.test", "a good long password", shopA, shopB)

	got, err := f.svc.Authenticate(context.Background(), "owner@shop.test", "a good long password", "", "")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if !got.NeedsShopSelection {
		t.Error("an owner of two shops should be asked which one")
	}

	sess, err := f.sess.Resolve(context.Background(), got.Token)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if sess.HasTenant() {
		t.Error("a shop was chosen for them; with two, the person must choose")
	}
}

func TestCannotSelectAShopYouDoNotBelongTo(t *testing.T) {
	t.Parallel()

	// The check that stops a valid session scoping itself to any tenant id it
	// likes — after which row-level security would faithfully serve that shop's
	// data to the wrong person.
	f := newFixture(t)
	f.user(t, "owner@shop.test", "a good long password", shopA)

	got, err := f.svc.Authenticate(context.Background(), "owner@shop.test", "a good long password", "", "")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	other, err := tenant.Parse(shopB)
	if err != nil {
		t.Fatalf("tenant: %v", err)
	}

	if _, err := f.svc.SelectShop(context.Background(), got.Token, other); !errors.Is(err, identity.ErrNotAMember) {
		t.Fatalf("selecting someone else's shop = %v, want ErrNotAMember", err)
	}
}

// ---------------------------------------------------------------------------
// The locked state
// ---------------------------------------------------------------------------

func lockedUser(t *testing.T, f fixture, email, password string) identity.Identity {
	t.Helper()

	i := identity.Identity{
		ID: uuid.New(), Email: email, FullName: "Recovered Person",
		Status: identity.StatusActive, PasswordHash: mustHash(t, f.hasher, password),
		MustChangePassword: true,
	}
	tid, err := tenant.Parse(shopA)
	if err != nil {
		t.Fatalf("tenant: %v", err)
	}
	f.repo.add(i, identity.Membership{
		StaffID: uuid.New(), TenantID: tid, Status: identity.StatusActive,
		Roles: []authz.Role{authz.RoleOwner},
	})
	return i
}

func TestALockedAccountCarriesNoRoles(t *testing.T) {
	t.Parallel()

	// BR-REC-20, and the mechanism behind it. After a vendor-issued password
	// the account may change its password and do nothing else — enforced by the
	// actor carrying no roles, so every permission check fails.
	f := newFixture(t)
	lockedUser(t, f, "recovered@shop.test", "a generated password")

	got, err := f.svc.Authenticate(context.Background(), "recovered@shop.test", "a generated password", "", "")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if !got.MustChangePassword {
		t.Fatal("the locked state was not reported to the caller")
	}

	actor, err := f.svc.Resolve(context.Background(), got.Token)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(actor.Roles) != 0 {
		t.Errorf("a locked account carries roles %v; it must carry none", actor.Roles)
	}

	// Which means every real permission is denied.
	e := authz.NewRBAC()
	for _, a := range []authz.Action{authz.ActionOrderWrite, authz.ActionCatalogRead, authz.ActionReportRead} {
		if err := e.Can(context.Background(), actor, a, authz.Resource{Type: "*"}); !errors.Is(err, authz.ErrDenied) {
			t.Errorf("a locked account was allowed %q: %v", a, err)
		}
	}
}

func TestALockedAccountCannotSelectAShop(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	lockedUser(t, f, "recovered@shop.test", "a generated password")

	got, err := f.svc.Authenticate(context.Background(), "recovered@shop.test", "a generated password", "", "")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	shop, err := tenant.Parse(shopA)
	if err != nil {
		t.Fatalf("tenant: %v", err)
	}
	if _, err := f.svc.SelectShop(context.Background(), got.Token, shop); !errors.Is(err, identity.ErrMustChangePassword) {
		t.Errorf("a locked account selected a shop: %v", err)
	}
}

func TestChangingThePasswordClearsTheLock(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	lockedUser(t, f, "recovered@shop.test", "a generated password")
	ctx := context.Background()

	got, err := f.svc.Authenticate(ctx, "recovered@shop.test", "a generated password", "", "")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	if err := f.svc.ChangePassword(ctx, got.Token, "a generated password", "my own chosen password"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}

	// Signing in again must now be unlocked and carry roles.
	again, err := f.svc.Authenticate(ctx, "recovered@shop.test", "my own chosen password", "", "")
	if err != nil {
		t.Fatalf("Authenticate after change: %v", err)
	}
	if again.MustChangePassword {
		t.Error("the account is still locked after the password was changed")
	}

	actor, err := f.svc.Resolve(ctx, again.Token)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(actor.Roles) == 0 {
		t.Error("roles were not restored after the lock cleared")
	}
}

// ---------------------------------------------------------------------------
// Passwords
// ---------------------------------------------------------------------------

func TestChangePassword(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.user(t, "owner@shop.test", "the current password", shopA)
	ctx := context.Background()

	got, err := f.svc.Authenticate(ctx, "owner@shop.test", "the current password", "", "")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	t.Run("the current password must be right", func(t *testing.T) {
		err := f.svc.ChangePassword(ctx, got.Token, "not the current one", "a brand new password")
		if !errors.Is(err, identity.ErrBadPassword) {
			t.Errorf("got %v, want ErrBadPassword", err)
		}
	})

	t.Run("the new password must meet policy", func(t *testing.T) {
		err := f.svc.ChangePassword(ctx, got.Token, "the current password", "short")
		if !errors.Is(err, passwd.ErrPolicy) {
			t.Errorf("got %v, want ErrPolicy", err)
		}
	})

	t.Run("the new password must differ", func(t *testing.T) {
		err := f.svc.ChangePassword(ctx, got.Token, "the current password", "the current password")
		if !errors.Is(err, identity.ErrSamePassword) {
			t.Errorf("got %v, want ErrSamePassword", err)
		}
	})
}

func TestChangingThePasswordSignsOutEverywhere(t *testing.T) {
	t.Parallel()

	// BR-IDN-03, BR-IDN-07. If the old password was compromised, this is what
	// removes the attacker — so it happens even though it inconveniences the
	// person's other devices.
	f := newFixture(t)
	f.user(t, "owner@shop.test", "the current password", shopA)
	ctx := context.Background()

	first, err := f.svc.Authenticate(ctx, "owner@shop.test", "the current password", "", "")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	second, err := f.svc.Authenticate(ctx, "owner@shop.test", "the current password", "", "")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	if err := f.svc.ChangePassword(ctx, first.Token, "the current password", "a brand new password"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}

	for name, token := range map[string]string{"the session that changed it": first.Token, "the other device": second.Token} {
		if _, err := f.svc.Resolve(ctx, token); !errors.Is(err, authz.ErrUnauthenticated) {
			t.Errorf("%s survived a password change: %v", name, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Resolve — on the path of every authenticated request
// ---------------------------------------------------------------------------

func TestResolveFailsClosedWhenTheStoreIsBroken(t *testing.T) {
	t.Parallel()

	// SES-008. A broken store must not downgrade an authenticated person to a
	// guest, because a guest is a principal that some routes accept.
	f := newFixture(t)
	f.user(t, "owner@shop.test", "a good long password", shopA)
	ctx := context.Background()

	got, err := f.svc.Authenticate(ctx, "owner@shop.test", "a good long password", "", "")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	f.repo.failErr = errors.New("database unreachable")

	_, err = f.svc.Resolve(ctx, got.Token)
	if !errors.Is(err, authz.ErrUnavailable) {
		t.Fatalf("Resolve with a broken store = %v, want ErrUnavailable", err)
	}
	if errors.Is(err, authz.ErrUnauthenticated) {
		t.Error("a store failure was reported as an authentication failure; the two must stay distinct")
	}
}

func TestResolveRejectsABlockedUserImmediately(t *testing.T) {
	t.Parallel()

	// Checked on every request, not only at sign-in. A user blocked five
	// minutes ago must stop working now.
	f := newFixture(t)
	user := f.user(t, "owner@shop.test", "a good long password", shopA)
	ctx := context.Background()

	got, err := f.svc.Authenticate(ctx, "owner@shop.test", "a good long password", "", "")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if _, err := f.svc.Resolve(ctx, got.Token); err != nil {
		t.Fatalf("Resolve before blocking: %v", err)
	}

	blocked := f.repo.byID[user.ID]
	blocked.Status = identity.StatusBlocked
	f.repo.byID[user.ID] = blocked

	if _, err := f.svc.Resolve(ctx, got.Token); !errors.Is(err, authz.ErrUnauthenticated) {
		t.Errorf("a blocked user's live session still resolved: %v", err)
	}
}

func TestResolveCarriesTheRolesOfTheCurrentShop(t *testing.T) {
	t.Parallel()

	// BR-ADM-12: the same person can hold different roles in different shops,
	// so roles come from the membership in the session's current shop rather
	// than from the identity.
	f := newFixture(t)

	hash := mustHash(t, f.hasher, "a good long password")
	id := uuid.New()
	a, err := tenant.Parse(shopA)
	if err != nil {
		t.Fatalf("tenant: %v", err)
	}
	b, err := tenant.Parse(shopB)
	if err != nil {
		t.Fatalf("tenant: %v", err)
	}

	f.repo.add(identity.Identity{
		ID: id, Email: "owner@shop.test", FullName: "Two Shop Owner",
		Status: identity.StatusActive, PasswordHash: hash,
	},
		identity.Membership{StaffID: uuid.New(), TenantID: a, Status: identity.StatusActive,
			Roles: []authz.Role{authz.RoleOwner}},
		identity.Membership{StaffID: uuid.New(), TenantID: b, Status: identity.StatusActive,
			Roles: []authz.Role{authz.RoleCounterSales}},
	)

	ctx := context.Background()
	got, err := f.svc.Authenticate(ctx, "owner@shop.test", "a good long password", "", "")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	if _, err := f.svc.SelectShop(ctx, got.Token, a); err != nil {
		t.Fatalf("SelectShop A: %v", err)
	}
	actor, err := f.svc.Resolve(ctx, got.Token)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(actor.Roles) != 1 || actor.Roles[0] != authz.RoleOwner {
		t.Errorf("in shop A the roles are %v, want [owner]", actor.Roles)
	}

	if _, err := f.svc.SelectShop(ctx, got.Token, b); err != nil {
		t.Fatalf("SelectShop B: %v", err)
	}
	actor, err = f.svc.Resolve(ctx, got.Token)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(actor.Roles) != 1 || actor.Roles[0] != authz.RoleCounterSales {
		t.Errorf("in shop B the roles are %v, want [counter_sales]", actor.Roles)
	}
}

func TestSignOut(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.user(t, "owner@shop.test", "a good long password", shopA)
	ctx := context.Background()

	got, err := f.svc.Authenticate(ctx, "owner@shop.test", "a good long password", "", "")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if err := f.svc.SignOut(ctx, got.Token); err != nil {
		t.Fatalf("SignOut: %v", err)
	}
	if _, err := f.svc.Resolve(ctx, got.Token); !errors.Is(err, authz.ErrUnauthenticated) {
		t.Errorf("the session survived sign-out: %v", err)
	}
}

func TestNewServiceValidatesDependencies(t *testing.T) {
	t.Parallel()

	if _, err := identity.NewService(nil, nil, nil, nil, nil, nil); err == nil {
		t.Error("NewService accepted nil dependencies")
	}
}

func mustHash(t *testing.T, h *passwd.Hasher, password string) string {
	t.Helper()
	hash, err := h.Hash(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	return hash
}
