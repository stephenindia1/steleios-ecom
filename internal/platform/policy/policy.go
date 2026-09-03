// Package policy defines the security policy attached to every route, and the
// catalogue of policies the application uses.
//
// This is the structural guarantee at the centre of the design: a route cannot
// be registered without a Policy, and the zero Policy is invalid. Forgetting
// authorization is therefore not possible; choosing to be public is possible,
// and it is greppable and reviewable (docs/03 §3.1, CLAUDE.md rule 2).
//
// Policies are defined in catalogue.go and nowhere else (SEC-04). Constructing
// one inline at a call site defeats the purpose of having a single reviewable
// security surface, and is rejected at review.
package policy

import (
	"errors"
	"fmt"
	"time"

	"github.com/stephenindia1/steleios-ecom/internal/platform/authz"
	"github.com/stephenindia1/steleios-ecom/internal/platform/ratelimit"
)

// AuthMode says how a request is authenticated.
//
// The zero value is deliberately invalid: a Policy literal that forgets to set
// Auth fails Validate and panics at startup rather than serving traffic
// unauthenticated (SEC-01).
type AuthMode uint8

const (
	// authUnset is the zero value. It is never valid.
	authUnset AuthMode = iota

	// AuthNone is deliberately unauthenticated. Every policy using it is a
	// review item.
	AuthNone

	// AuthSession requires a signed-in customer.
	AuthSession

	// AuthSessionOrGuest accepts a signed-in customer or a guest with a cart
	// session. Guest checkout must remain possible (BR-IDN-09).
	AuthSessionOrGuest

	// AuthAdmin requires a staff session with roles.
	AuthAdmin

	// AuthSignature authenticates by HMAC over the raw request body, with no
	// session and no CSRF. Used only by provider webhooks (BR-PAY-04/06).
	AuthSignature
)

// String renders the mode for the startup route table.
func (m AuthMode) String() string {
	switch m {
	case AuthNone:
		return "none"
	case AuthSession:
		return "session"
	case AuthSessionOrGuest:
		return "session-or-guest"
	case AuthAdmin:
		return "admin"
	case AuthSignature:
		return "signature"
	default:
		return "unset"
	}
}

// Valid reports whether the mode was set to something meaningful.
func (m AuthMode) Valid() error {
	if m == authUnset || m > AuthSignature {
		return errors.New("auth mode is unset")
	}
	return nil
}

// OwnerSource says where the middleware finds the owning customer of the
// resource a request addresses, so that ownership can be enforced generically
// (BR-ORD-05).
type OwnerSource struct {
	// ResourceType names the kind of resource, e.g. "order".
	ResourceType string
	// PathParam is the route parameter holding the resource id, e.g. "id".
	PathParam string
}

// IsZero reports whether no ownership check is configured.
func (o OwnerSource) IsZero() bool { return o.ResourceType == "" && o.PathParam == "" }

// Validate reports whether a configured ownership rule is complete.
func (o OwnerSource) Validate() error {
	if o.IsZero() {
		return nil
	}
	if o.ResourceType == "" {
		return errors.New("ownership rule has no resource type")
	}
	if o.PathParam == "" {
		return errors.New("ownership rule has no path parameter")
	}
	return nil
}

// OwnedBy builds an ownership rule for a resource identified by a path
// parameter.
func OwnedBy(resourceType, pathParam string) OwnerSource {
	return OwnerSource{ResourceType: resourceType, PathParam: pathParam}
}

// Policy is the complete security description of a route.
//
// Everything the middleware chain does is derived from this value, so reading
// the catalogue tells you exactly what protects every endpoint.
type Policy struct {
	// Name identifies the policy in logs, metrics and the startup route table.
	Name string

	// Auth is how the request is authenticated.
	Auth AuthMode

	// Permission, when set, is the action the actor must be granted. Ownership
	// and permission are independent: a customer reaches their own order by
	// ownership with no permission at all.
	Permission authz.Action

	// Ownership, when set, requires that the actor owns the addressed resource.
	Ownership OwnerSource

	// RateLimit is the throttle. A policy with no limits must set Unlimited
	// explicitly, so an omission cannot pass as a decision.
	RateLimit ratelimit.Spec

	// Unlimited records a deliberate absence of rate limiting.
	Unlimited bool

	// CSRF requires a valid double-submit token on state-changing requests.
	CSRF bool

	// Idempotent requires an Idempotency-Key header and replays the stored
	// response for a repeat (BR-CHK-02).
	Idempotent bool

	// RawBody buffers the unparsed body for signature verification. It MUST be
	// set for AuthSignature routes and MUST NOT be set for any other
	// (BR-PAY-05).
	RawBody bool

	// Reauth requires the actor to have re-authenticated recently, for
	// high-consequence admin actions (BR-ADM-07).
	Reauth bool

	// DualApproval marks an action that needs a second actor's approval. The
	// middleware records the requirement; the service enforces it, because the
	// approval is part of the business transaction (BR-ADM-04).
	DualApproval bool

	// Timeout overrides the default per-request deadline.
	Timeout time.Duration
}

// Validate reports whether the policy is internally consistent.
//
// It is called for every route at registration, and a failure panics during
// startup rather than being discovered by a request in production (SEC-01).
func (p Policy) Validate() error {
	var errs []error

	if p.Name == "" {
		errs = append(errs, errors.New("policy has no name"))
	}
	if err := p.Auth.Valid(); err != nil {
		errs = append(errs, err)
	}
	if err := p.Ownership.Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := p.RateLimit.Validate(); err != nil {
		errs = append(errs, err)
	}

	// A rate limit is either declared or explicitly waived. Silence is not an
	// option, because silence is how an unthrottled endpoint ships.
	if p.RateLimit.IsZero() && !p.Unlimited {
		errs = append(errs, errors.New("no rate limit declared and Unlimited not set"))
	}
	if !p.RateLimit.IsZero() && p.Unlimited {
		errs = append(errs, errors.New("Unlimited set alongside rate limit rules"))
	}

	// RawBody exists for signature verification and nothing else. Buffering the
	// body elsewhere would be a memory footgun with no purpose (DB-026).
	if p.RawBody && p.Auth != AuthSignature {
		errs = append(errs, errors.New("RawBody is only valid with AuthSignature"))
	}
	if p.Auth == AuthSignature {
		if !p.RawBody {
			errs = append(errs, errors.New("AuthSignature requires RawBody for HMAC verification (BR-PAY-05)"))
		}
		if p.CSRF {
			errs = append(errs, errors.New("AuthSignature must not require CSRF: a provider has no session (BR-PAY-06)"))
		}
		if !p.Ownership.IsZero() || p.Permission != "" {
			errs = append(errs, errors.New("AuthSignature actors carry no ownership or permissions"))
		}
	}

	// Ownership is a customer concept; an unauthenticated route has no owner to
	// compare against.
	if !p.Ownership.IsZero() && p.Auth == AuthNone {
		errs = append(errs, errors.New("ownership check requires an authenticated actor"))
	}

	// Reauth and dual approval are staff controls.
	if (p.Reauth || p.DualApproval) && p.Auth != AuthAdmin {
		errs = append(errs, errors.New("Reauth and DualApproval apply to admin policies only"))
	}

	if p.Timeout < 0 {
		errs = append(errs, fmt.Errorf("negative timeout %s", p.Timeout))
	}

	return errors.Join(errs...)
}

// IsPublic reports whether the policy deliberately serves unauthenticated
// requests. Used by the startup route table to highlight the public surface.
func (p Policy) IsPublic() bool { return p.Auth == AuthNone }

// Describe renders the policy as one line for the startup route table
// (SEC-03), so the security surface of the running process is in the logs.
func (p Policy) Describe() string {
	limits := "unlimited"
	if !p.RateLimit.IsZero() {
		limits = ""
		for i, r := range p.RateLimit.Rules {
			if i > 0 {
				limits += " + "
			}
			limits += r.String()
		}
	}

	flags := ""
	for _, f := range []struct {
		on   bool
		name string
	}{
		{p.CSRF, "csrf"},
		{p.Idempotent, "idempotent"},
		{p.RawBody, "rawbody"},
		{p.Reauth, "reauth"},
		{p.DualApproval, "dual-approval"},
	} {
		if f.on {
			if flags != "" {
				flags += ","
			}
			flags += f.name
		}
	}
	if flags == "" {
		flags = "-"
	}

	owner := "-"
	if !p.Ownership.IsZero() {
		owner = p.Ownership.ResourceType + "{" + p.Ownership.PathParam + "}"
	}

	perm := string(p.Permission)
	if perm == "" {
		perm = "-"
	}

	return fmt.Sprintf("auth=%s perm=%s owns=%s limits=%s flags=%s",
		p.Auth, perm, owner, limits, flags)
}
