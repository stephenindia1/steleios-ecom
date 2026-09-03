// Package ratelimit is the sole implementation of request throttling
// (docs/03 §6.1, BR-SEC-10).
//
// Limits are declared as data on a security policy, not as middleware chosen at
// a call site, so the throttle on every route is visible in one file
// (platform/policy/catalogue.go).
//
// Every limiter fails closed: if the backing store cannot answer, the request is
// refused rather than admitted (BR-SEC-11, RD-011).
package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Errors returned by a Limiter.
var (
	// ErrLimited means the caller exceeded the rule. Retry-After is carried on
	// the Result, not the error.
	ErrLimited = errors.New("ratelimit: limit exceeded")

	// ErrUnavailable means the backing store could not answer. Callers refuse
	// the request; they MUST NOT treat it as an allow.
	ErrUnavailable = errors.New("ratelimit: unavailable")
)

// Scope names what a rule counts against.
type Scope string

// The scopes. IP is cheap and applies before authentication; Actor requires an
// identity and therefore applies after it; Subject is an explicit business key
// such as a phone number or a coupon code.
const (
	// ScopeIP counts per client address. Applied pre-auth so unauthenticated
	// floods die cheaply (SEC-07).
	ScopeIP Scope = "ip"
	// ScopeActor counts per authenticated principal, or per guest session where
	// no principal exists.
	ScopeActor Scope = "actor"
	// ScopeSubject counts per business key supplied by the handler — a phone
	// number for OTP (BR-IDN-05), an account for login (BR-IDN-11).
	ScopeSubject Scope = "subject"
)

// Valid reports whether s is a known scope.
func (s Scope) Valid() error {
	switch s {
	case ScopeIP, ScopeActor, ScopeSubject:
		return nil
	default:
		return fmt.Errorf("ratelimit: unknown scope %q", string(s))
	}
}

// Rule is one limit: at most Limit requests per Window within Scope.
type Rule struct {
	Scope  Scope
	Limit  int
	Window time.Duration
}

// Validate reports whether the rule is usable.
func (r Rule) Validate() error {
	if err := r.Scope.Valid(); err != nil {
		return err
	}
	if r.Limit <= 0 {
		return fmt.Errorf("ratelimit: limit must be positive, got %d", r.Limit)
	}
	if r.Window <= 0 {
		return fmt.Errorf("ratelimit: window must be positive, got %s", r.Window)
	}
	return nil
}

// String renders the rule for the startup route table.
func (r Rule) String() string {
	return fmt.Sprintf("%d/%s per %s", r.Limit, r.Window, r.Scope)
}

// Spec is the set of rules attached to a policy. All of them must pass.
//
// The zero Spec has no rules, which means unlimited. Policy validation rejects
// that for anything but a deliberately unlimited internal route, so an omitted
// limit cannot slip through unnoticed.
type Spec struct {
	Rules []Rule
}

// IsZero reports whether the spec declares no limits.
func (s Spec) IsZero() bool { return len(s.Rules) == 0 }

// Validate reports whether every rule in the spec is usable.
func (s Spec) Validate() error {
	var errs []error
	for i, r := range s.Rules {
		if err := r.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("rule %d: %w", i, err))
		}
	}
	return errors.Join(errs...)
}

// PerIP builds a spec limiting n requests per window per client address.
func PerIP(n int, window time.Duration) Spec {
	return Spec{Rules: []Rule{{Scope: ScopeIP, Limit: n, Window: window}}}
}

// PerActor builds a spec limiting n requests per window per principal.
func PerActor(n int, window time.Duration) Spec {
	return Spec{Rules: []Rule{{Scope: ScopeActor, Limit: n, Window: window}}}
}

// PerSubject builds a spec limiting n requests per window per business key.
func PerSubject(n int, window time.Duration) Spec {
	return Spec{Rules: []Rule{{Scope: ScopeSubject, Limit: n, Window: window}}}
}

// Composite merges specs so a route can be limited on several scopes at once —
// login is limited per IP and per account independently (BR-IDN-11), because
// either alone is trivially evaded.
func Composite(specs ...Spec) Spec {
	total := 0
	for _, s := range specs {
		total += len(s.Rules)
	}
	out := Spec{Rules: make([]Rule, 0, total)} // DB-024
	for _, s := range specs {
		out.Rules = append(out.Rules, s.Rules...)
	}
	return out
}

// Result describes the outcome of one limit check.
type Result struct {
	Allowed    bool
	Limit      int
	Remaining  int
	RetryAfter time.Duration
	// Rule is the rule that produced this result, so a denial can name which
	// limit was hit in the log without guessing (MET-003).
	Rule Rule
}

// Limiter counts requests against a rule.
//
// Key is the already-scoped identity — an address, an actor id, a phone number.
// The limiter does not know what the key means; the middleware resolves it.
type Limiter interface {
	Allow(ctx context.Context, rule Rule, key string) (Result, error)
}

// AllowAll is a Limiter that permits everything. It exists for tests and for
// local development without Redis. It MUST NOT be wired in staging or
// production; the composition root refuses to build with it outside local.
type AllowAll struct{}

// Allow always permits.
func (AllowAll) Allow(_ context.Context, rule Rule, _ string) (Result, error) {
	return Result{Allowed: true, Limit: rule.Limit, Remaining: rule.Limit, Rule: rule}, nil
}

// DenyAll is a Limiter that refuses everything, used in tests to assert that a
// route is genuinely gated rather than incidentally passing.
type DenyAll struct{}

// Allow always refuses.
func (DenyAll) Allow(_ context.Context, rule Rule, _ string) (Result, error) {
	return Result{Allowed: false, Limit: rule.Limit, RetryAfter: rule.Window, Rule: rule}, nil
}
