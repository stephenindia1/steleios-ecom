// Package module defines the contract every domain unit satisfies and the
// dependency container it is built from (docs/03 §2).
//
// A module owns its services, its routes and its background workers, and
// exposes nothing else. Wiring is explicit code in internal/app.Build: there is
// no service locator and no dependency-injection container, because Go resolves
// dependencies at compile time and a missing collaborator should be a build
// failure rather than a startup panic (MOD-03).
package module

import (
	"context"
	"errors"
	"log/slog"

	"github.com/hibiken/asynq"
	"github.com/stephenindia1/steleios-ecom/internal/platform/audit"
	"github.com/stephenindia1/steleios-ecom/internal/platform/authz"
	"github.com/stephenindia1/steleios-ecom/internal/platform/clock"
	"github.com/stephenindia1/steleios-ecom/internal/platform/config"
	"github.com/stephenindia1/steleios-ecom/internal/platform/httpx"
	"github.com/stephenindia1/steleios-ecom/internal/platform/postgres"
	"github.com/stephenindia1/steleios-ecom/internal/platform/redis"
)

// Module is a self-contained domain unit.
type Module interface {
	// Name identifies the module in logs, metrics and the route table.
	Name() string
	// Routes mounts the module's own routes, each with a security policy.
	Routes(g *httpx.Group)
	// Workers registers the module's background task handlers.
	Workers(mux *asynq.ServeMux)
	// Health reports whether the module can serve, for readiness (HLT-003).
	Health(ctx context.Context) error
}

// Deps is the shared container every module factory receives.
//
// A module receives this and its explicit collaborators, and nothing else. It
// never opens its own database pool, Redis client or logger (MOD-01).
type Deps struct {
	Cfg   config.Config
	Log   *slog.Logger
	Clock clock.Clock

	DB    *postgres.Pool
	UoW   postgres.UnitOfWork
	Redis *redis.Client
	Queue *asynq.Client

	Audit audit.Recorder
	Authz authz.Enforcer
}

// Validate reports missing dependencies.
//
// It runs before any factory, so a nil dependency is a startup failure with a
// name attached rather than a nil-pointer panic on the first request that
// happens to need it (MOD-02, HLT-005).
func (d *Deps) Validate() error {
	var errs []error

	if d.Log == nil {
		errs = append(errs, errors.New("module deps: logger is nil"))
	}
	if d.Clock == nil {
		errs = append(errs, errors.New("module deps: clock is nil"))
	}
	if d.DB == nil {
		errs = append(errs, errors.New("module deps: database pool is nil"))
	}
	if d.UoW == nil {
		errs = append(errs, errors.New("module deps: unit of work is nil"))
	}
	if d.Redis == nil {
		errs = append(errs, errors.New("module deps: redis client is nil"))
	}
	if d.Queue == nil {
		errs = append(errs, errors.New("module deps: queue client is nil"))
	}
	if d.Audit == nil {
		errs = append(errs, errors.New("module deps: audit recorder is nil"))
	}
	if d.Authz == nil {
		errs = append(errs, errors.New("module deps: authz enforcer is nil"))
	}
	if err := d.Cfg.Validate(); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

// HealthOf aggregates module health for the readiness endpoint, returning a
// per-module report so an operator sees which one is failing rather than a bare
// "not ready" (HLT-003).
func HealthOf(ctx context.Context, mods []Module) map[string]string {
	out := make(map[string]string, len(mods)) // DB-024
	for _, m := range mods {
		if err := m.Health(ctx); err != nil {
			out[m.Name()] = err.Error()
			continue
		}
		out[m.Name()] = "ok"
	}
	return out
}
