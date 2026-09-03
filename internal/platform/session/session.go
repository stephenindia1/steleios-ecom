// Package session is the sole implementation of server-side sessions
// (docs/03 §6.1, docs/05 §5.1).
//
// A session is an opaque 256-bit token in an HttpOnly cookie; the record lives
// in PostgreSQL. The token itself is never stored — only its SHA-256 — so a
// read of the sessions table cannot be turned into a live session (SES-001,
// SES-002).
//
// Server-side sessions rather than a JWT: revocation, "sign out everywhere"
// and role changes all have to take effect immediately, and a self-contained
// token cannot be withdrawn once issued (BR-IDN-02, BR-IDN-03).
package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/stephenindia1/steleios-ecom/internal/platform/authz"
	"github.com/stephenindia1/steleios-ecom/internal/platform/clock"
	"github.com/stephenindia1/steleios-ecom/internal/platform/ids"
	"github.com/stephenindia1/steleios-ecom/internal/platform/postgres"
	"github.com/stephenindia1/steleios-ecom/internal/platform/tenant"
)

// Errors returned by the store.
var (
	// ErrNotFound means no live session matches the token. Expired, revoked and
	// never-existed are deliberately indistinguishable to the caller: telling
	// them apart would let an attacker probe which tokens were once real.
	ErrNotFound = errors.New("session: not found")
	// ErrExpired is returned only internally; callers see ErrNotFound.
	ErrExpired = errors.New("session: expired")
)

// Revocation reasons, matching the database's check constraint.
const (
	ReasonSignedOut           = "signed_out"
	ReasonSignedOutEverywhere = "signed_out_everywhere"
	ReasonPasswordChanged     = "password_changed"
	ReasonRoleChanged         = "role_changed"
	ReasonUserBlocked         = "user_blocked"
	ReasonRecovery            = "recovery"
	ReasonAdmin               = "admin"
)

// Session is a live sign-in.
type Session struct {
	IdentityID uuid.UUID
	// TenantID is the shop the session is currently acting in. Zero until the
	// person picks one, which an owner with several shops does after signing in.
	TenantID   uuid.UUID
	ActorType  authz.ActorType
	CSRFSecret string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	// Fingerprint is a hashed reference for logs and audit rows. The token
	// itself is never logged (SES-010).
	Fingerprint string
}

// HasTenant reports whether a shop has been chosen.
func (s Session) HasTenant() bool { return s.TenantID != uuid.Nil }

// Store issues, resolves and revokes sessions.
type Store struct {
	pool  *postgres.Pool
	uow   postgres.UnitOfWork
	clock clock.Clock
	ttl   time.Duration
	// slideAfter bounds how often a read turns into a write. Sliding the expiry
	// on every request would make every authenticated read a write, which is a
	// self-inflicted load problem on the busiest query in the system (SES-004).
	slideAfter time.Duration
}

// New returns the session store.
func New(pool *postgres.Pool, uow postgres.UnitOfWork, clk clock.Clock, ttl time.Duration) (*Store, error) {
	if pool == nil || uow == nil || clk == nil {
		return nil, errors.New("session: nil dependency")
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("session: ttl must be positive, got %s", ttl)
	}
	return &Store{pool: pool, uow: uow, clock: clk, ttl: ttl, slideAfter: time.Minute}, nil
}

// hashToken returns the stored form of a token.
//
// SHA-256 rather than a password hash: the token is 256 bits of crypto/rand,
// so there is nothing to brute force, and a slow hash on every request would
// cost far more than it bought.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

const insertSQL = `
insert into sessions (
  token_sha256, identity_id, tenant_id, actor_type, csrf_secret,
  created_at, last_seen_at, expires_at, ip, user_agent
) values ($1, $2, $3, $4, $5, $6, $6, $7, $8, $9)`

// Issue creates a session and returns the raw token.
//
// The token is returned exactly once, to be set as a cookie. It is not stored
// and cannot be recovered afterwards — if it is lost, the person signs in again.
func (s *Store) Issue(ctx context.Context, identityID uuid.UUID, actorType authz.ActorType, ip, userAgent string) (token string, sess Session, err error) {
	token, err = ids.SessionToken()
	if err != nil {
		return "", Session{}, fmt.Errorf("session: generate token: %w", err)
	}
	csrf, err := ids.Token(32)
	if err != nil {
		return "", Session{}, fmt.Errorf("session: generate csrf secret: %w", err)
	}

	now := s.clock.Now()
	sess = Session{
		IdentityID:  identityID,
		ActorType:   actorType,
		CSRFSecret:  csrf,
		CreatedAt:   now,
		LastSeenAt:  now,
		ExpiresAt:   now.Add(s.ttl),
		Fingerprint: ids.Fingerprint(token),
	}

	// Sessions precede tenancy, so this runs on the system path (ADR 0007).
	err = s.uow.DoSystem(ctx, func(r postgres.Repos) error {
		_, err := r.Querier().Exec(ctx, insertSQL,
			hashToken(token), identityID, nil, string(actorType), csrf,
			now, sess.ExpiresAt, nullable(ip), nullable(userAgent))
		return err
	})
	if err != nil {
		return "", Session{}, fmt.Errorf("session: issue: %w", err)
	}
	return token, sess, nil
}

const resolveSQL = `
select identity_id, coalesce(tenant_id, '00000000-0000-0000-0000-000000000000'::uuid),
       actor_type, csrf_secret, created_at, last_seen_at, expires_at
  from sessions
 where token_sha256 = $1 and revoked_at is null`

// Resolve returns the live session for a token.
//
// A missing, expired or revoked session all return ErrNotFound. The caller —
// the session middleware — turns that into "no session", and the route's own
// policy decides whether that is acceptable (httpx.loadSession).
func (s *Store) Resolve(ctx context.Context, token string) (Session, error) {
	if token == "" {
		return Session{}, ErrNotFound
	}

	var sess Session
	var actorType string

	err := s.pool.ReadSystem(ctx, func(r postgres.Repos) error {
		return r.Querier().QueryRow(ctx, resolveSQL, hashToken(token)).Scan(
			&sess.IdentityID, &sess.TenantID, &actorType, &sess.CSRFSecret,
			&sess.CreatedAt, &sess.LastSeenAt, &sess.ExpiresAt)
	})
	if err != nil {
		// A no-rows error and any other read failure are different things: one
		// is "no session", the other is the database being unreachable, and
		// conflating them would turn an outage into a silent mass sign-out.
		if errors.Is(err, postgres.ErrNoRows) {
			return Session{}, ErrNotFound
		}
		return Session{}, fmt.Errorf("session: resolve: %w", err)
	}

	if !s.clock.Now().Before(sess.ExpiresAt) {
		return Session{}, ErrNotFound
	}

	sess.ActorType = authz.ActorType(actorType)
	sess.Fingerprint = ids.Fingerprint(token)
	return sess, nil
}

const slideSQL = `
update sessions
   set last_seen_at = $2, expires_at = $3
 where token_sha256 = $1 and revoked_at is null`

// Touch slides the session's expiry, at most once per slideAfter.
//
// Returns whether it wrote. Callers do not need to care; it exists so a test
// can assert that a read is not turning into a write on every request.
func (s *Store) Touch(ctx context.Context, token string, sess Session) (bool, error) {
	now := s.clock.Now()
	if now.Sub(sess.LastSeenAt) < s.slideAfter {
		return false, nil
	}

	err := s.uow.DoSystem(ctx, func(r postgres.Repos) error {
		_, err := r.Querier().Exec(ctx, slideSQL, hashToken(token), now, now.Add(s.ttl))
		return err
	})
	if err != nil {
		return false, fmt.Errorf("session: touch: %w", err)
	}
	return true, nil
}

const setTenantSQL = `
update sessions set tenant_id = $2
 where token_sha256 = $1 and revoked_at is null`

// SelectShop binds the session to a shop.
//
// Called after the person picks one from their memberships. The caller MUST
// have verified the membership first: this writes what it is told, and the
// tenant it writes becomes the scope every later query runs under (ADR 0007).
func (s *Store) SelectShop(ctx context.Context, token string, tenantID tenant.ID) error {
	err := s.uow.DoSystem(ctx, func(r postgres.Repos) error {
		tag, err := r.Querier().Exec(ctx, setTenantSQL, hashToken(token), tenantID.UUID())
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return err
		}
		return fmt.Errorf("session: select shop: %w", err)
	}
	return nil
}

const revokeSQL = `
update sessions set revoked_at = $2, revoked_reason = $3
 where token_sha256 = $1 and revoked_at is null`

// Revoke ends one session.
func (s *Store) Revoke(ctx context.Context, token, reason string) error {
	err := s.uow.DoSystem(ctx, func(r postgres.Repos) error {
		_, err := r.Querier().Exec(ctx, revokeSQL, hashToken(token), s.clock.Now(), reason)
		return err
	})
	if err != nil {
		return fmt.Errorf("session: revoke: %w", err)
	}
	return nil
}

const revokeAllSQL = `
update sessions set revoked_at = $2, revoked_reason = $3
 where identity_id = $1 and revoked_at is null`

// RevokeAll ends every session for an identity.
//
// This is what makes server-side sessions worth their cost: it is the action
// behind a password change, a role change, blocking a user and "sign out
// everywhere" (BR-IDN-03, BR-IDN-07, BR-REC-13). None of it is possible with a
// self-contained token.
func (s *Store) RevokeAll(ctx context.Context, identityID uuid.UUID, reason string) (int64, error) {
	var count int64
	err := s.uow.DoSystem(ctx, func(r postgres.Repos) error {
		tag, err := r.Querier().Exec(ctx, revokeAllSQL, identityID, s.clock.Now(), reason)
		if err != nil {
			return err
		}
		count = tag.RowsAffected()
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("session: revoke all: %w", err)
	}
	return count, nil
}

const sweepSQL = `
delete from sessions
 where (expires_at < $1) or (revoked_at is not null and revoked_at < $2)`

// Sweep removes expired and long-revoked sessions.
//
// Run by the worker. Sign-in history lives in the audit log (BR-ADM-06), so
// nothing is lost by deleting these — this table is live state, not a record.
func (s *Store) Sweep(ctx context.Context, revokedRetention time.Duration) (int64, error) {
	now := s.clock.Now()

	var removed int64
	err := s.uow.DoSystem(ctx, func(r postgres.Repos) error {
		tag, err := r.Querier().Exec(ctx, sweepSQL, now, now.Add(-revokedRetention))
		if err != nil {
			return err
		}
		removed = tag.RowsAffected()
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("session: sweep: %w", err)
	}
	return removed, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
