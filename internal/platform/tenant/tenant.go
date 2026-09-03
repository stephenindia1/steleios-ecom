// Package tenant carries the current shop through a request.
//
// Steleios is multi-tenant and the unit of isolation is the SHOP (ADR 0007).
// Every query is confined to one shop by PostgreSQL row-level security, which
// reads the tenant from a session setting that this package supplies.
//
// The tenant comes from the authenticated session and nowhere else. A tenant id
// in a request body, query parameter or header is untrusted input and MUST NOT
// reach the database (BR-SEC-02).
package tenant

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// ID identifies a shop. It is a defined type so it cannot be confused with any
// other identifier in a function signature (GO-044).
type ID uuid.UUID

// ErrNoTenant means no shop was established for this operation.
//
// It is deliberately an error rather than a zero value: a tenant-scoped query
// running without a tenant would be confined to nothing by row-level security
// and return an empty result, which reads as "no data" rather than as the bug
// it is. Failing loudly is better than returning a plausible lie.
var ErrNoTenant = errors.New("tenant: no shop in context")

type ctxKey struct{}

// WithTenant returns a context scoped to one shop.
//
// Called by the session middleware once the actor and their current shop are
// resolved. Nothing else calls it, except tests and the onboarding path that
// creates a shop.
func WithTenant(ctx context.Context, id ID) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// FromContext returns the current shop.
func FromContext(ctx context.Context) (ID, bool) {
	id, ok := ctx.Value(ctxKey{}).(ID)
	return id, ok
}

// Require returns the current shop or ErrNoTenant.
//
// Repositories use this rather than FromContext so that a missing tenant is an
// error at the call site instead of an empty result page later.
func Require(ctx context.Context) (ID, error) {
	id, ok := FromContext(ctx)
	if !ok || uuid.UUID(id) == uuid.Nil {
		return ID{}, ErrNoTenant
	}
	return id, nil
}

// Parse converts a stored identifier into an ID.
func Parse(s string) (ID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return ID{}, fmt.Errorf("tenant: parse %q: %w", s, err)
	}
	return ID(id), nil
}

// String renders the identifier.
func (id ID) String() string { return uuid.UUID(id).String() }

// IsZero reports whether the identifier is unset.
func (id ID) IsZero() bool { return uuid.UUID(id) == uuid.Nil }

// UUID exposes the underlying value for the database layer.
func (id ID) UUID() uuid.UUID { return uuid.UUID(id) }
