// Package app is the composition root.
//
// It is the one place the object graph is assembled (MOD-03). Dependency order
// is the call order, and the compiler checks it: a module names its
// collaborators in its constructor signature, so a missing or mistyped
// dependency is a build failure rather than a runtime nil.
//
// cmd/api and cmd/worker both call Build. There is one object graph definition,
// not two (MOD-05).
package app

import (
	"context"
	"fmt"

	"github.com/hibiken/asynq"
	"github.com/stephenindia1/steleios-ecom/internal/health"
	"github.com/stephenindia1/steleios-ecom/internal/platform/httpx"
	"github.com/stephenindia1/steleios-ecom/internal/platform/module"
	"github.com/stephenindia1/steleios-ecom/internal/platform/redis"
)

// Build assembles every module from the shared container.
//
// Modules are listed in dependency order. Adding one here is the only way it
// reaches production: an import that silently mounts routes is a security
// review failure (MOD-03).
func Build(d *module.Deps) ([]module.Module, error) {
	if err := d.Validate(); err != nil {
		return nil, fmt.Errorf("app: %w", err)
	}

	healthMod, err := health.New(d)
	if err != nil {
		return nil, err
	}

	// Phase 2 onwards, domain modules are wired here in dependency order:
	//
	//   cat, err := catalog.New(d)
	//   inv, err := inventory.New(d)
	//   prc, err := pricing.New(d, cat.Catalog())
	//   ord, err := order.New(d, inv.Reservations(), prc.Quotes())
	//
	// Each names its collaborators, so the graph is checked at compile time and
	// a cycle is a build error rather than a runtime surprise (MOD-07).

	return []module.Module{healthMod}, nil
}

// Router builds the HTTP router and mounts every module's routes.
//
// Route registration goes through httpx.Group, which requires a security policy
// on every verb, so this function cannot produce an unprotected endpoint
// (SEC-01, SEC-02).
func Router(d *module.Deps, mods []module.Module, rc *redis.Client) (*httpx.Router, error) {
	rt, err := httpx.NewRouter(httpx.Deps{
		Cfg:     d.Cfg,
		Log:     d.Log,
		Clock:   d.Clock,
		Authz:   d.Authz,
		Limiter: redis.NewLimiter(rc),
		Idem:    idempotencyAdapter{store: redis.NewIdempotencyStore(rc)},

		// Sessions, Owners and Verifier arrive with the identity, order and
		// payment modules. Until then, any policy needing one refuses to
		// register rather than mounting a route whose protection does nothing
		// (httpx.requirementsFor).
	})
	if err != nil {
		return nil, fmt.Errorf("app: build router: %w", err)
	}

	root := rt.Group("")
	for _, m := range mods {
		m.Routes(root)
	}

	return rt, nil
}

// Workers registers every module's background handlers on one mux.
func Workers(mods []module.Module) *asynq.ServeMux {
	mux := asynq.NewServeMux()
	for _, m := range mods {
		m.Workers(mux)
	}
	return mux
}

// idempotencyAdapter satisfies the interface httpx declares using the Redis
// store, which returns primitives so it need not import the HTTP layer
// (OOP-05).
type idempotencyAdapter struct {
	store *redis.IdempotencyStore
}

func (a idempotencyAdapter) Lookup(ctx context.Context, actorID, key string) (*httpx.StoredResponse, error) {
	status, body, found, err := a.store.Lookup(ctx, actorID, key)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return &httpx.StoredResponse{Status: status, Body: body}, nil
}

func (a idempotencyAdapter) Save(ctx context.Context, actorID, key string, status int, body []byte) error {
	return a.store.Save(ctx, actorID, key, status, body)
}
