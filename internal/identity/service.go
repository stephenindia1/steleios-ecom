package identity

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/stephenindia1/steleios-ecom/internal/platform/audit"
	"github.com/stephenindia1/steleios-ecom/internal/platform/authz"
	"github.com/stephenindia1/steleios-ecom/internal/platform/clock"
	"github.com/stephenindia1/steleios-ecom/internal/platform/logging"
	"github.com/stephenindia1/steleios-ecom/internal/platform/passwd"
	"github.com/stephenindia1/steleios-ecom/internal/platform/session"
	"github.com/stephenindia1/steleios-ecom/internal/platform/tenant"
)

// Lockout policy (BR-IDN-11).
const (
	// maxFailedLogins before a temporary lockout. Generous enough that an
	// honest person mistyping does not get locked out of their own till.
	maxFailedLogins = 5
	// lockoutDuration is short and self-clearing. Long enough to make online
	// guessing pointless, short enough that it is not a denial of service
	// anyone can inflict on anyone.
	lockoutDuration = 15 * time.Minute
)

// Sessions is the part of the session store this service uses.
type Sessions interface {
	Issue(ctx context.Context, identityID uuid.UUID, actorType authz.ActorType, ip, userAgent string) (string, session.Session, error)
	Resolve(ctx context.Context, token string) (session.Session, error)
	Touch(ctx context.Context, token string, sess session.Session) (bool, error)
	SelectShop(ctx context.Context, token string, tenantID tenant.ID) error
	Revoke(ctx context.Context, token, reason string) error
	RevokeAll(ctx context.Context, identityID uuid.UUID, reason string) (int64, error)
}

// Service is the identity module's behaviour.
type Service struct {
	repo     Repository
	sessions Sessions
	hasher   *passwd.Hasher
	audit    audit.Recorder
	clock    clock.Clock
	log      *slog.Logger

	// dummyHash is verified against when no user matches, so that a request for
	// an unknown address costs the same as one for a real account. See
	// Authenticate.
	dummyHash string
}

// NewService builds the service.
func NewService(repo Repository, sessions Sessions, hasher *passwd.Hasher,
	aud audit.Recorder, clk clock.Clock, log *slog.Logger) (*Service, error) {

	if repo == nil || sessions == nil || hasher == nil || aud == nil || clk == nil || log == nil {
		return nil, errors.New("identity: nil dependency")
	}

	// Hashed once at construction. The value is irrelevant; the COST of
	// verifying it is the point.
	dummy, err := hasher.Hash("a value nobody will ever authenticate with")
	if err != nil {
		return nil, fmt.Errorf("identity: build timing guard: %w", err)
	}

	return &Service{
		repo: repo, sessions: sessions, hasher: hasher,
		audit: aud, clock: clk, log: log, dummyHash: dummy,
	}, nil
}

// Authenticate signs a person in.
//
// Every failure path returns a distinct error for logging and counting, and the
// HANDLER collapses them into one message (IsAuthFailure). The distinction must
// never reach the client, or the login form tells an attacker which addresses
// have accounts (BR-IDN-06).
func (s *Service) Authenticate(ctx context.Context, email, password, ip, userAgent string) (Authenticated, error) {
	now := s.clock.Now()
	email = strings.TrimSpace(email)

	ident, err := s.repo.FindByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, ErrNoSuchUser) {
			// TIMING. An unknown address must cost the same as a known one, or
			// the response time alone reveals which accounts exist — a
			// difference of ~100ms that is trivially measurable over a few
			// requests. So the password is verified against a real hash whose
			// result is discarded.
			_ = s.hasher.Verify(s.dummyHash, password) //nolint:errcheck // deliberately discarded

			s.logAttempt(ctx, "unknown_user", email, ip)
			return Authenticated{}, ErrNoSuchUser
		}
		return Authenticated{}, err
	}

	if err := ident.CanSignIn(now); err != nil {
		// Still verify, for the same timing reason: a suspended account must
		// not answer faster than an active one.
		_ = s.hasher.Verify(ident.PasswordHash, password) //nolint:errcheck // deliberately discarded

		s.logAttempt(ctx, statusReason(err), email, ip)
		return Authenticated{}, err
	}

	if err := s.hasher.Verify(ident.PasswordHash, password); err != nil {
		s.recordFailure(ctx, ident, now)
		s.logAttempt(ctx, "bad_password", email, ip)
		return Authenticated{}, ErrBadPassword
	}

	// A generated password that was never used in time is dead (BR-REC-24).
	if ident.PasswordExpiresAt != nil && !now.Before(*ident.PasswordExpiresAt) {
		s.logAttempt(ctx, "password_expired", email, ip)
		return Authenticated{}, ErrPasswordExpired
	}

	memberships, err := s.repo.MembershipsOf(ctx, ident.ID)
	if err != nil {
		return Authenticated{}, err
	}
	active := activeOnly(memberships)

	token, sess, err := s.sessions.Issue(ctx, ident.ID, authz.ActorAdmin, ip, userAgent)
	if err != nil {
		return Authenticated{}, err
	}

	if err := s.repo.RecordSuccessfulLogin(ctx, ident.ID, now); err != nil {
		// The sign-in succeeded; failing to clear a counter must not undo it.
		s.log.ErrorContext(ctx, "could not record successful login",
			"identity_id", ident.ID.String(), "error", err.Error())
	}

	// The plaintext is in hand for the only moment it ever is, so this is the
	// only opportunity to upgrade a hash made under weaker parameters.
	s.rehashIfNeeded(ctx, ident, password, now)

	// Vendor staff have no shops at all, so an empty list means two very
	// different things and the caller must be able to tell them apart: a shop
	// worker with no membership is broken, a platform user with none is normal.
	platformRoles, err := s.repo.PlatformRolesOf(ctx, ident.ID)
	if err != nil {
		return Authenticated{}, err
	}

	result := Authenticated{
		Token:              token,
		CSRFSecret:         sess.CSRFSecret,
		Identity:           ident,
		Memberships:        active,
		PlatformRoles:      platformRoles,
		MustChangePassword: ident.MustChangePassword,
		NeedsShopSelection: len(active) > 1,
	}

	// A single shop is selected automatically: making a person choose from a
	// list of one is friction with no security value.
	if len(active) == 1 && !ident.MustChangePassword && !result.IsPlatform() {
		if err := s.sessions.SelectShop(ctx, token, active[0].TenantID); err != nil {
			return Authenticated{}, err
		}
	}

	_ = s.audit.Record(ctx, audit.Entry{ //nolint:errcheck // logged inside
		Action:       "identity.signed_in",
		ResourceType: "identity",
		ResourceID:   ident.ID.String(),
	})
	return result, nil
}

// SelectShop binds the session to one of the person's shops.
func (s *Service) SelectShop(ctx context.Context, token string, t tenant.ID) (Membership, error) {
	sess, err := s.session(ctx, token)
	if err != nil {
		return Membership{}, err
	}

	ident, err := s.repo.FindByID(ctx, sess.IdentityID)
	if err != nil {
		return Membership{}, err
	}
	// The locked state permits exactly one action, and this is not it
	// (BR-REC-20).
	if ident.MustChangePassword {
		return Membership{}, ErrMustChangePassword
	}
	if err := ident.CanSignIn(s.clock.Now()); err != nil {
		return Membership{}, err
	}

	// The check that matters: a person may only bind to a shop they belong to.
	// Without it, a valid session could scope itself to any tenant id it liked,
	// and row-level security would faithfully serve that shop's data.
	m, err := s.repo.MembershipIn(ctx, sess.IdentityID, t)
	if err != nil {
		return Membership{}, err
	}

	if err := s.sessions.SelectShop(ctx, token, t); err != nil {
		return Membership{}, err
	}

	_ = s.audit.Record(ctx, audit.Entry{ //nolint:errcheck // logged inside
		Action:       "identity.shop_selected",
		ResourceType: "tenant",
		ResourceID:   t.String(),
	})
	return m, nil
}

// ChangePassword replaces a password and signs every other device out.
func (s *Service) ChangePassword(ctx context.Context, token, current, next string) error {
	sess, err := s.session(ctx, token)
	if err != nil {
		return err
	}

	ident, err := s.repo.FindByID(ctx, sess.IdentityID)
	if err != nil {
		return err
	}

	if err := s.hasher.Verify(ident.PasswordHash, current); err != nil {
		return ErrBadPassword
	}
	if err := passwd.CheckPolicy(next); err != nil {
		return err
	}
	// BR-REC-23: the generated password must actually be replaced, not
	// re-entered.
	if err := s.hasher.Verify(ident.PasswordHash, next); err == nil {
		return ErrSamePassword
	}

	hash, err := s.hasher.Hash(next)
	if err != nil {
		return fmt.Errorf("identity: hash new password: %w", err)
	}

	now := s.clock.Now()
	if err := s.repo.UpdatePassword(ctx, ident.ID, hash, now); err != nil {
		return err
	}

	// BR-IDN-03, BR-IDN-07: a password change signs out everywhere. If the old
	// password was compromised, this is what removes the attacker — so it must
	// happen even though it is inconvenient for the person's other devices.
	if _, err := s.sessions.RevokeAll(ctx, ident.ID, session.ReasonPasswordChanged); err != nil {
		return err
	}

	_ = s.audit.Record(ctx, audit.Entry{ //nolint:errcheck // logged inside
		Action:       "identity.password_changed",
		ResourceType: "identity",
		ResourceID:   ident.ID.String(),
	})
	return nil
}

// SignOut ends this session.
func (s *Service) SignOut(ctx context.Context, token string) error {
	return s.sessions.Revoke(ctx, token, session.ReasonSignedOut)
}

// SignOutEverywhere ends every session for the signed-in identity.
func (s *Service) SignOutEverywhere(ctx context.Context, token string) (int64, error) {
	sess, err := s.session(ctx, token)
	if err != nil {
		return 0, err
	}
	return s.sessions.RevokeAll(ctx, sess.IdentityID, session.ReasonSignedOutEverywhere)
}

// Resolve turns a session token into an authorization actor.
//
// This satisfies httpx.SessionResolver and is therefore on the path of every
// authenticated request in the system.
func (s *Service) Resolve(ctx context.Context, token string) (authz.Actor, error) {
	sess, err := s.sessions.Resolve(ctx, token)
	if err != nil {
		if errors.Is(err, session.ErrNotFound) {
			return authz.Actor{}, authz.ErrUnauthenticated
		}
		// Anything else is the store failing, and the middleware must fail
		// closed rather than downgrade the person to a guest (SES-008).
		return authz.Actor{}, fmt.Errorf("%w: %w", authz.ErrUnavailable, err)
	}

	ident, err := s.repo.FindByID(ctx, sess.IdentityID)
	if err != nil {
		if errors.Is(err, ErrNoSuchUser) {
			return authz.Actor{}, authz.ErrUnauthenticated
		}
		return authz.Actor{}, fmt.Errorf("%w: %w", authz.ErrUnavailable, err)
	}

	// Checked on EVERY request, not only at sign-in. A user blocked five
	// minutes ago must stop working now, not when their session expires — and
	// their sessions are revoked too, so this is belt and braces on the path
	// that matters most.
	if err := ident.CanSignIn(s.clock.Now()); err != nil {
		return authz.Actor{}, authz.ErrUnauthenticated
	}

	actor := authz.Actor{
		ID:                 ident.ID.String(),
		Type:               sess.ActorType,
		SessionFingerprint: sess.Fingerprint,
	}

	// BR-REC-20: while locked, the actor carries NO roles. Every permission
	// check therefore fails, and the only reachable routes are the ones that
	// need none — change password, and sign out.
	if ident.MustChangePassword {
		return actor, nil
	}

	// Vendor staff. Their roles do not come from a shop, because they hold no
	// membership in one — the two worlds are disjoint and enforced as such by
	// the schema (BR-ADM-14, migration 00019). Checked BEFORE the membership
	// path rather than as a fallback: a fallback would mean a bug in the
	// membership lookup silently promoted a shop worker's session down this
	// branch, and the two must never be reachable from the same identity.
	platformRoles, err := s.repo.PlatformRolesOf(ctx, ident.ID)
	if err != nil {
		return authz.Actor{}, fmt.Errorf("%w: %w", authz.ErrUnavailable, err)
	}
	if len(platformRoles) > 0 {
		actor.Roles = platformRoles
		return actor, nil
	}

	// Roles come from the membership in the session's current shop, so the same
	// person can be a manager in one and a counter operator in another
	// (BR-ADM-12). No shop chosen means no roles yet, which is correct: a
	// session that has not picked a shop cannot act in one.
	if sess.HasTenant() {
		m, err := s.repo.MembershipIn(ctx, ident.ID, tenant.ID(sess.TenantID))
		if err != nil {
			if errors.Is(err, ErrNotAMember) {
				// The membership was removed while the session lived. Deny.
				return authz.Actor{}, authz.ErrUnauthenticated
			}
			return authz.Actor{}, fmt.Errorf("%w: %w", authz.ErrUnavailable, err)
		}
		actor.Roles = m.Roles
	}

	return actor, nil
}

// session resolves a token for the actions that need one.
//
// It translates "no live session" into the platform's unauthenticated error, so
// a token that expired between the middleware admitting the request and the
// handler running produces a 401 rather than a 500. Any other failure is the
// store being unreachable and is passed through untouched, so the caller still
// fails closed (SES-008).
func (s *Service) session(ctx context.Context, token string) (session.Session, error) {
	sess, err := s.sessions.Resolve(ctx, token)
	if err != nil {
		if errors.Is(err, session.ErrNotFound) {
			return session.Session{}, authz.ErrUnauthenticated
		}
		return session.Session{}, err
	}
	return sess, nil
}

// recordFailure counts a bad password and locks out past the threshold.
func (s *Service) recordFailure(ctx context.Context, ident Identity, now time.Time) {
	var lockUntil *time.Time
	if ident.FailedLogins+1 >= maxFailedLogins {
		until := now.Add(lockoutDuration)
		lockUntil = &until
	}
	if err := s.repo.RecordFailedLogin(ctx, ident.ID, lockUntil); err != nil {
		s.log.ErrorContext(ctx, "could not record failed login",
			"identity_id", ident.ID.String(), "error", err.Error())
	}
}

// rehashIfNeeded upgrades a hash made under weaker parameters.
func (s *Service) rehashIfNeeded(ctx context.Context, ident Identity, password string, now time.Time) {
	if !s.hasher.NeedsRehash(ident.PasswordHash) {
		return
	}
	hash, err := s.hasher.Hash(password)
	if err != nil {
		s.log.ErrorContext(ctx, "could not rehash password", "error", err.Error())
		return
	}
	if err := s.repo.UpdatePassword(ctx, ident.ID, hash, now); err != nil {
		// A failed upgrade must not fail the sign-in: the old hash still
		// verifies, so the person is no worse off than before.
		s.log.ErrorContext(ctx, "could not store rehashed password", "error", err.Error())
	}
}

// logAttempt records a failed sign-in.
//
// The EMAIL is redacted by the logging handler (BR-SEC-07); the reason is not,
// because distinguishing "unknown user" from "bad password" is exactly what
// makes an authentication log useful to whoever investigates.
func (s *Service) logAttempt(ctx context.Context, reason, email, ip string) {
	logging.FromContext(ctx, s.log).WarnContext(ctx, "sign-in failed",
		"reason", reason, "email", email, "ip", ip)
}

func statusReason(err error) string {
	switch {
	case errors.Is(err, ErrBlocked):
		return "blocked"
	case errors.Is(err, ErrLockedOut):
		return "locked_out"
	default:
		return "not_active"
	}
}

func activeOnly(all []Membership) []Membership {
	out := make([]Membership, 0, len(all)) // DB-024
	for _, m := range all {
		if m.IsActive() {
			out = append(out, m)
		}
	}
	return out
}
