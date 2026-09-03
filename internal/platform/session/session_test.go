package session_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stephenindia1/steleios-ecom/internal/platform/authz"
	"github.com/stephenindia1/steleios-ecom/internal/platform/clock"
	"github.com/stephenindia1/steleios-ecom/internal/platform/config"
	"github.com/stephenindia1/steleios-ecom/internal/platform/postgres"
	"github.com/stephenindia1/steleios-ecom/internal/platform/session"
)

// Integration tests against a real PostgreSQL. Sessions are the credential the
// whole system rests on, so what is under test is the behaviour of the store
// against the actual database — expiry, revocation and the constraint that a
// revoked session cannot come back (GO-093).

func skipWithoutDB(t *testing.T) string {
	t.Helper()

	if v := os.Getenv("POSTGRES_DSN"); v != "" {
		return v
	}
	if os.Getenv("CI") != "" {
		t.Fatal("POSTGRES_DSN is unset in CI: these tests must run there")
	}
	t.Skip("POSTGRES_DSN unset; skipping session integration tests")
	return ""
}

type harness struct {
	store *session.Store
	pool  *postgres.Pool
	clk   *clock.Fake
	id    uuid.UUID
}

func newHarness(t *testing.T, ttl time.Duration) harness {
	t.Helper()

	dsn := skipWithoutDB(t)
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))

	pool, err := postgres.New(context.Background(), config.Postgres{
		DSN: dsn, MaxConns: 6, MinConns: 1,
		MaxConnLifetime: time.Hour, MaxConnIdleTime: 10 * time.Minute,
		ConnectTimeout: 5 * time.Second, StatementTimeout: 5 * time.Second,
		IdleInTxTimeout: 10 * time.Second, HealthCheckPeriod: 30 * time.Second,
	}, log)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	clk := clock.NewFake(time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC))
	uow := postgres.NewUnitOfWork(pool)

	store, err := session.New(pool, uow, clk, ttl)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	// A throwaway identity to hang sessions from.
	id := uuid.New()
	ctx := context.Background()
	err = uow.DoSystem(ctx, func(r postgres.Repos) error {
		_, err := r.Querier().Exec(ctx,
			`insert into identities (id, email, full_name, password_hash)
			 values ($1, $2, 'Session Test', 'argon2-placeholder')`,
			id, id.String()+"@session.test")
		return err
	})
	if err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	t.Cleanup(func() {
		_ = uow.DoSystem(context.Background(), func(r postgres.Repos) error { //nolint:errcheck // teardown
			_, err := r.Querier().Exec(context.Background(), `delete from sessions where identity_id = $1`, id)
			if err != nil {
				return err
			}
			_, err = r.Querier().Exec(context.Background(), `delete from identities where id = $1`, id)
			return err
		})
	})

	return harness{store: store, pool: pool, clk: clk, id: id}
}

func TestIssueAndResolve(t *testing.T) {
	h := newHarness(t, time.Hour)
	ctx := context.Background()

	token, issued, err := h.store.Issue(ctx, h.id, authz.ActorAdmin, "203.0.113.7", "test-agent")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if token == "" {
		t.Fatal("Issue returned an empty token")
	}
	if issued.CSRFSecret == "" {
		t.Error("no CSRF secret was bound to the session")
	}

	resolved, err := h.store.Resolve(ctx, token)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.IdentityID != h.id {
		t.Errorf("resolved identity = %s, want %s", resolved.IdentityID, h.id)
	}
	if resolved.ActorType != authz.ActorAdmin {
		t.Errorf("actor type = %q, want admin", resolved.ActorType)
	}
	if resolved.CSRFSecret != issued.CSRFSecret {
		t.Error("the resolved CSRF secret differs from the issued one")
	}
	if resolved.HasTenant() {
		t.Error("a new session should have no shop chosen yet")
	}
}

func TestTheTokenIsNeverStored(t *testing.T) {
	h := newHarness(t, time.Hour)
	ctx := context.Background()

	token, _, err := h.store.Issue(ctx, h.id, authz.ActorAdmin, "", "")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// The property that matters most in this package. A read of the sessions
	// table must be useless to whoever obtains it: the stored value cannot be
	// presented as a cookie.
	var found int
	err = h.pool.ReadSystem(ctx, func(r postgres.Repos) error {
		return r.Querier().QueryRow(ctx,
			`select count(*) from sessions where token_sha256 = $1`, token).Scan(&found)
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if found != 0 {
		t.Fatal("the raw token is stored in the database; a table read would hand over live sessions")
	}

	// And the row does exist — under its hash.
	err = h.pool.ReadSystem(ctx, func(r postgres.Repos) error {
		return r.Querier().QueryRow(ctx,
			`select count(*) from sessions where identity_id = $1`, h.id).Scan(&found)
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if found != 1 {
		t.Fatalf("expected exactly one session row, found %d", found)
	}
}

func TestResolveRejectsUnknownAndExpired(t *testing.T) {
	h := newHarness(t, time.Hour)
	ctx := context.Background()

	if _, err := h.store.Resolve(ctx, ""); !errors.Is(err, session.ErrNotFound) {
		t.Errorf("empty token = %v, want ErrNotFound", err)
	}
	if _, err := h.store.Resolve(ctx, "a-token-that-was-never-issued"); !errors.Is(err, session.ErrNotFound) {
		t.Errorf("unknown token = %v, want ErrNotFound", err)
	}

	token, _, err := h.store.Issue(ctx, h.id, authz.ActorAdmin, "", "")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := h.store.Resolve(ctx, token); err != nil {
		t.Fatalf("a fresh session should resolve: %v", err)
	}

	// Expiry is checked against the injected clock, so this needs no sleeping
	// (GO-056).
	h.clk.Advance(2 * time.Hour)

	if _, err := h.store.Resolve(ctx, token); !errors.Is(err, session.ErrNotFound) {
		t.Errorf("an expired session resolved: %v", err)
	}
}

func TestRevoke(t *testing.T) {
	h := newHarness(t, time.Hour)
	ctx := context.Background()

	token, _, err := h.store.Issue(ctx, h.id, authz.ActorAdmin, "", "")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	if err := h.store.Revoke(ctx, token, session.ReasonSignedOut); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := h.store.Resolve(ctx, token); !errors.Is(err, session.ErrNotFound) {
		t.Errorf("a revoked session still resolves: %v", err)
	}

	// Revoking twice is harmless; sign-out is not a thing that should error
	// because somebody clicked it twice.
	if err := h.store.Revoke(ctx, token, session.ReasonSignedOut); err != nil {
		t.Errorf("revoking an already-revoked session errored: %v", err)
	}
}

func TestRevokeAllSignsOutEverywhere(t *testing.T) {
	h := newHarness(t, time.Hour)
	ctx := context.Background()

	// Several devices, as a real owner would have: a counter, a phone, an
	// office machine.
	var tokens []string
	for range 3 {
		token, _, err := h.store.Issue(ctx, h.id, authz.ActorAdmin, "", "")
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		tokens = append(tokens, token)
	}

	// This is what server-side sessions buy, and why a self-contained token
	// would not do: a password change must sign every device out at once
	// (BR-IDN-03, BR-IDN-07).
	count, err := h.store.RevokeAll(ctx, h.id, session.ReasonPasswordChanged)
	if err != nil {
		t.Fatalf("RevokeAll: %v", err)
	}
	if count != 3 {
		t.Errorf("revoked %d sessions, want 3", count)
	}

	for i, token := range tokens {
		if _, err := h.store.Resolve(ctx, token); !errors.Is(err, session.ErrNotFound) {
			t.Errorf("session %d survived a sign-out-everywhere: %v", i, err)
		}
	}
}

func TestARevokedSessionCanNeverComeBack(t *testing.T) {
	h := newHarness(t, time.Hour)
	ctx := context.Background()

	token, _, err := h.store.Issue(ctx, h.id, authz.ActorAdmin, "", "")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if err := h.store.Revoke(ctx, token, session.ReasonUserBlocked); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	// Enforced by the database, not by the store. Un-revoking would silently
	// resurrect a credential somebody deliberately killed after a compromise.
	uow := postgres.NewUnitOfWork(h.pool)
	err = uow.DoSystem(ctx, func(r postgres.Repos) error {
		_, err := r.Querier().Exec(ctx,
			`update sessions set revoked_at = null where identity_id = $1`, h.id)
		return err
	})
	if err == nil {
		t.Fatal("a revoked session was restored; the database should refuse")
	}

	// And it stays unusable.
	if _, err := h.store.Resolve(ctx, token); !errors.Is(err, session.ErrNotFound) {
		t.Errorf("the revoked session resolved after the restore attempt: %v", err)
	}
}

func TestTouchDoesNotWriteOnEveryRequest(t *testing.T) {
	h := newHarness(t, time.Hour)
	ctx := context.Background()

	token, sess, err := h.store.Issue(ctx, h.id, authz.ActorAdmin, "", "")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// SES-004: sliding the expiry on every request would turn the busiest read
	// in the system into a write. It slides at most once a minute.
	wrote, err := h.store.Touch(ctx, token, sess)
	if err != nil {
		t.Fatalf("Touch: %v", err)
	}
	if wrote {
		t.Error("Touch wrote immediately after the session was issued")
	}

	h.clk.Advance(90 * time.Second)
	resolved, err := h.store.Resolve(ctx, token)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	wrote, err = h.store.Touch(ctx, token, resolved)
	if err != nil {
		t.Fatalf("Touch: %v", err)
	}
	if !wrote {
		t.Error("Touch did not slide the expiry after the interval had passed")
	}

	// The slide must actually extend the session.
	after, err := h.store.Resolve(ctx, token)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !after.ExpiresAt.After(sess.ExpiresAt) {
		t.Errorf("expiry did not move: was %s, now %s", sess.ExpiresAt, after.ExpiresAt)
	}
}

func TestSelectShop(t *testing.T) {
	h := newHarness(t, time.Hour)
	ctx := context.Background()

	token, _, err := h.store.Issue(ctx, h.id, authz.ActorAdmin, "", "")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	shop, err := seededShop()
	if err != nil {
		t.Fatalf("seeded shop id: %v", err)
	}

	if err := h.store.SelectShop(ctx, token, shop); err != nil {
		t.Fatalf("SelectShop: %v", err)
	}

	resolved, err := h.store.Resolve(ctx, token)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !resolved.HasTenant() {
		t.Fatal("the session has no shop after one was selected")
	}
	if resolved.TenantID != shop.UUID() {
		t.Errorf("bound to %s, want %s", resolved.TenantID, shop)
	}

	// Selecting a shop on a revoked session must fail rather than silently do
	// nothing — otherwise a signed-out session appears to switch shops.
	if err := h.store.Revoke(ctx, token, session.ReasonSignedOut); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if err := h.store.SelectShop(ctx, token, shop); !errors.Is(err, session.ErrNotFound) {
		t.Errorf("SelectShop on a revoked session = %v, want ErrNotFound", err)
	}
}

func TestSweepRemovesExpiredSessions(t *testing.T) {
	h := newHarness(t, time.Hour)
	ctx := context.Background()

	if _, _, err := h.store.Issue(ctx, h.id, authz.ActorAdmin, "", ""); err != nil {
		t.Fatalf("Issue: %v", err)
	}

	h.clk.Advance(2 * time.Hour)

	removed, err := h.store.Sweep(ctx, 24*time.Hour)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if removed < 1 {
		t.Errorf("swept %d sessions, expected at least the expired one", removed)
	}

	var remaining int
	err = h.pool.ReadSystem(ctx, func(r postgres.Repos) error {
		return r.Querier().QueryRow(ctx,
			`select count(*) from sessions where identity_id = $1`, h.id).Scan(&remaining)
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if remaining != 0 {
		t.Errorf("%d expired sessions survived the sweep", remaining)
	}
}

func TestNewValidatesItsDependencies(t *testing.T) {
	t.Parallel()

	clk := clock.NewFake(time.Now())

	if _, err := session.New(nil, nil, clk, time.Hour); err == nil {
		t.Error("New accepted nil dependencies")
	}
	if _, err := session.New(nil, nil, nil, time.Hour); err == nil {
		t.Error("New accepted a nil clock")
	}
}
