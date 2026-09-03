package identity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hibiken/asynq"
	"github.com/stephenindia1/steleios-ecom/internal/platform/httpx"
	"github.com/stephenindia1/steleios-ecom/internal/platform/module"
	"github.com/stephenindia1/steleios-ecom/internal/platform/passwd"
	"github.com/stephenindia1/steleios-ecom/internal/platform/policy"
	"github.com/stephenindia1/steleios-ecom/internal/platform/session"
)

// Mod is the identity module (docs/03 §2).
type Mod struct {
	svc     *Service
	handler *Handler
	store   *session.Store
}

// New is the module factory.
//
// It owns the session store rather than receiving one, because the session
// store is identity's own state: nothing else in the system issues or revokes a
// session, and a second owner would be a second place sessions could be created
// (MOD-01).
func New(d *module.Deps) (*Mod, error) {
	if err := d.Validate(); err != nil {
		return nil, fmt.Errorf("identity: %w", err)
	}

	// Staff sessions use the ADMIN ttl, which is much shorter than a customer's.
	// A till left signed in overnight is a real risk in a shop, in a way that a
	// customer's month-long session on their own phone is not (BR-ADM-07).
	ttl := d.Cfg.Security.AdminSessionTTL

	store, err := session.New(d.DB, d.UoW, d.Clock, ttl)
	if err != nil {
		return nil, fmt.Errorf("identity: %w", err)
	}

	svc, err := NewService(NewRepository(d.DB, d.UoW), store,
		passwd.NewDefault(), d.Audit, d.Clock, d.Log)
	if err != nil {
		return nil, err
	}

	return &Mod{
		svc:     svc,
		handler: NewHandler(svc, httpx.NewCookieBuilder(d.Cfg), ttl, d.Log),
		store:   store,
	}, nil
}

// Name identifies the module.
func (m *Mod) Name() string { return "identity" }

// Service exposes the identity service to the composition root, which passes it
// to httpx as the session resolver. Nothing else may reach it (MOD-01).
func (m *Mod) Service() *Service { return m.svc }

// Sessions exposes the session store for the sweep worker.
func (m *Mod) Sessions() *session.Store { return m.store }

// Routes mounts the identity endpoints.
//
// Read the policies down the right-hand side and the access rules are plain:
// one unauthenticated pair to get in, and everything else needing a staff
// session but NO permission, because these are the routes a person must be able
// to reach whatever they are allowed to do — including an account locked into
// changing its password (BR-REC-20).
func (m *Mod) Routes(g *httpx.Group) { Mount(g, m.handler) }

// Mount registers the identity routes for a handler.
//
// Separate from Routes so the end-to-end tests drive the REAL policies through
// the real middleware chain. A test that registered its own routes would prove
// the handlers work and nothing about what protects them, which is the half
// that matters.
func Mount(g *httpx.Group, h *Handler) {
	g.Mount("/api/v1/auth", func(auth *httpx.Group) {
		auth.GET("/csrf", policy.Public, h.CSRFToken)
		auth.POST("/login", policy.AuthAttempt, h.Login)

		auth.GET("/me", policy.SignedIn, h.Me)
		auth.POST("/shop", policy.SignedIn, h.SelectShop)
		auth.POST("/password", policy.SignedIn, h.ChangePassword)
		auth.POST("/logout", policy.SignedIn, h.Logout)
		auth.POST("/logout-everywhere", policy.SignedIn, h.LogoutEverywhere)
	})
}

// sweepInterval bounds how long a dead session row survives. It is a
// housekeeping figure, not a security one: an expired session stops working the
// moment it expires, whether or not its row has been reclaimed.
const sweepInterval = time.Hour

// revokedRetention keeps revoked rows briefly, so that "why was I signed out"
// is answerable from the live table and not only from the audit log.
const revokedRetention = 7 * 24 * time.Hour

// TaskSweepSessions is the queued task that removes dead session rows.
const TaskSweepSessions = "identity:sweep_sessions"

// Schedule runs the sweep on the low-priority queue.
func (m *Mod) Schedule() []module.PeriodicTask {
	return []module.PeriodicTask{{
		Cron: fmt.Sprintf("@every %s", sweepInterval),
		Task: asynq.NewTask(TaskSweepSessions, nil, asynq.Queue("low")),
	}}
}

// Workers registers the session sweep.
func (m *Mod) Workers(mux *asynq.ServeMux) {
	mux.HandleFunc(TaskSweepSessions, func(ctx context.Context, _ *asynq.Task) error {
		removed, err := m.store.Sweep(ctx, revokedRetention)
		if err != nil {
			return err
		}
		if removed > 0 {
			// Worth a line: a sudden spike is either a mass revocation or a
			// sweep that had not been running.
			m.svc.log.InfoContext(ctx, "swept dead sessions", "removed", removed)
		}
		return nil
	})
}

// Health reports whether identity can serve.
//
// It resolves a token that cannot exist. A live database answers "not found";
// an unreachable one answers with an error, which is the distinction readiness
// needs (HLT-003).
func (m *Mod) Health(ctx context.Context) error {
	_, err := m.store.Resolve(ctx, "health-probe-token-that-matches-nothing")
	if err != nil && !errors.Is(err, session.ErrNotFound) {
		return err
	}
	return nil
}
