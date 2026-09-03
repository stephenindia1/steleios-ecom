// Package httpx owns routing, the middleware chain, request decoding and the
// error envelope.
//
// Its central guarantee: a route cannot be registered without a security
// policy. Every verb on [Group] takes a policy.Policy, the zero policy is
// invalid, and registration panics at startup rather than serving an
// unprotected endpoint (docs/03 §3.1, SEC-01, SEC-02).
//
// Registering routes directly on chi anywhere outside this package is
// prohibited and blocked by the lint configuration.
package httpx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"
	"github.com/stephenindia1/steleios-ecom/internal/platform/authz"
	"github.com/stephenindia1/steleios-ecom/internal/platform/clock"
	"github.com/stephenindia1/steleios-ecom/internal/platform/config"
	"github.com/stephenindia1/steleios-ecom/internal/platform/policy"
	"github.com/stephenindia1/steleios-ecom/internal/platform/ratelimit"
)

// ctxKey is the unexported key type for values this package puts on a context
// (GO-034).
type ctxKey int

const (
	ctxKeyActor ctxKey = iota
	ctxKeyPolicy
	ctxKeyRawBody
	ctxKeyRequestID
)

// HandlerFunc is the shape of every handler.
//
// Returning (Response, error) rather than writing to a ResponseWriter means a
// handler cannot emit a partial success and then fail, and that error mapping
// happens in exactly one place (GO-025).
type HandlerFunc func(ctx context.Context, req *Request) (Response, error)

// SessionResolver turns a session identifier into an actor.
//
// Declared here because httpx is the consumer; the implementation lives in the
// identity module (OOP-05).
type SessionResolver interface {
	Resolve(ctx context.Context, sessionID string) (authz.Actor, error)
}

// OwnerResolver reports which customer owns a resource, so that ownership can
// be enforced generically by the middleware rather than remembered in each
// handler (BR-ORD-05).
type OwnerResolver interface {
	OwnerOf(ctx context.Context, resourceType, resourceID string) (ownerID string, err error)
}

// StoredResponse is a previously returned response, replayed for a repeated
// idempotency key (BR-CHK-02).
type StoredResponse struct {
	Status int
	Body   []byte
}

// IdempotencyStore records and replays responses by key.
type IdempotencyStore interface {
	// Lookup returns a stored response, or nil if this key is new. Claiming the
	// key and returning nil means the caller owns this attempt.
	Lookup(ctx context.Context, actorID, key string) (*StoredResponse, error)
	// Save records the outcome for replay.
	Save(ctx context.Context, actorID, key string, status int, body []byte) error
}

// SignatureVerifier authenticates a provider webhook from the raw body
// (BR-PAY-04/05).
type SignatureVerifier interface {
	Verify(ctx context.Context, routePattern, signatureHeader string, rawBody []byte) (authz.Actor, error)
}

// Deps are what the router needs to build a middleware chain.
//
// A dependency that is nil is not "off": a route whose policy requires it
// refuses to register, so a missing session store cannot silently produce
// unauthenticated access (BR-SEC-11).
type Deps struct {
	Cfg      config.Config
	Log      *slog.Logger
	Clock    clock.Clock
	Authz    authz.Enforcer
	Limiter  ratelimit.Limiter
	Sessions SessionResolver
	Owners   OwnerResolver
	Idem     IdempotencyStore
	Verifier SignatureVerifier
}

// validate reports missing dependencies that are always required.
func (d Deps) validate() error {
	var errs []error
	if d.Log == nil {
		errs = append(errs, errors.New("logger is nil"))
	}
	if d.Clock == nil {
		errs = append(errs, errors.New("clock is nil"))
	}
	if d.Authz == nil {
		errs = append(errs, errors.New("authz enforcer is nil"))
	}
	if d.Limiter == nil {
		errs = append(errs, errors.New("rate limiter is nil"))
	}
	return errors.Join(errs...)
}

// RouteInfo records a registered route and its policy, for the startup route
// table (SEC-03) and for the test that asserts every route is protected
// (TST-02).
type RouteInfo struct {
	Method  string
	Pattern string
	Policy  policy.Policy
}

// Router is the application's HTTP surface.
type Router struct {
	mux    *chi.Mux
	deps   Deps
	routes []RouteInfo
	// preflighted records the patterns that already have an OPTIONS handler.
	// Several verbs share a pattern and chi panics on a duplicate registration,
	// so the first verb to claim a pattern registers its preflight.
	preflighted map[string]bool
}

// NewRouter builds the router.
//
// It fails rather than starting with a missing dependency: an API that boots
// without an authorization enforcer is worse than one that does not boot.
func NewRouter(d Deps) (*Router, error) {
	if err := d.validate(); err != nil {
		return nil, fmt.Errorf("httpx: %w", err)
	}
	return &Router{mux: chi.NewMux(), deps: d, preflighted: map[string]bool{}}, nil
}

// Group returns a route group mounted at prefix.
func (rt *Router) Group(prefix string) *Group {
	return &Group{rt: rt, prefix: prefix}
}

// Routes returns every registered route with its policy, sorted for stable
// output.
func (rt *Router) Routes() []RouteInfo {
	out := make([]RouteInfo, len(rt.routes)) // DB-024
	copy(out, rt.routes)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Pattern != out[j].Pattern {
			return out[i].Pattern < out[j].Pattern
		}
		return out[i].Method < out[j].Method
	})
	return out
}

// LogRouteTable writes the complete security surface of the running process to
// the log at startup (SEC-03).
//
// This is the artefact that makes "what protects this endpoint" answerable
// without reading code, and it is why public routes are worth highlighting.
func (rt *Router) LogRouteTable() {
	routes := rt.Routes()
	public := 0
	for _, r := range routes {
		if r.Policy.IsPublic() {
			public++
		}
		rt.deps.Log.Info("route registered",
			"method", r.Method,
			"route", r.Pattern,
			"policy", r.Policy.Name,
			"detail", r.Policy.Describe(),
			"public", r.Policy.IsPublic(),
		)
	}
	rt.deps.Log.Info("route table complete", "routes", len(routes), "public_routes", public)
}

// Handler returns the http.Handler to serve.
func (rt *Router) Handler() http.Handler { return rt.mux }

// Group registers routes under a prefix. It is the only way to register a
// route, and every verb requires a policy.
//
// A group is a path prefix, not a chi sub-router: every route is registered on
// the one mux with its full pattern. That keeps the recorded pattern, the
// matched pattern and the registered pattern identical, so the startup route
// table cannot disagree with what is actually served (SEC-03).
type Group struct {
	rt     *Router
	prefix string
}

// Mount creates a nested group under pattern.
func (g *Group) Mount(pattern string, fn func(*Group)) {
	fn(&Group{rt: g.rt, prefix: g.prefix + pattern})
}

// GET registers a read route.
func (g *Group) GET(pattern string, p policy.Policy, h HandlerFunc) {
	g.route(http.MethodGet, pattern, p, h)
}

// POST registers a create route.
func (g *Group) POST(pattern string, p policy.Policy, h HandlerFunc) {
	g.route(http.MethodPost, pattern, p, h)
}

// PUT registers a replace route.
func (g *Group) PUT(pattern string, p policy.Policy, h HandlerFunc) {
	g.route(http.MethodPut, pattern, p, h)
}

// PATCH registers a partial update route.
func (g *Group) PATCH(pattern string, p policy.Policy, h HandlerFunc) {
	g.route(http.MethodPatch, pattern, p, h)
}

// DELETE registers a delete route.
func (g *Group) DELETE(pattern string, p policy.Policy, h HandlerFunc) {
	g.route(http.MethodDelete, pattern, p, h)
}

// route is the single registration path. Every route in the application passes
// through here, which is what makes the policy requirement structural rather
// than conventional.
func (g *Group) route(method, pattern string, p policy.Policy, h HandlerFunc) {
	full := g.prefix + pattern

	// A malformed or zero policy is a programmer error caught at startup, never
	// a runtime 500 and never an unprotected endpoint (SEC-01, GO-027).
	if err := p.Validate(); err != nil {
		panic(fmt.Sprintf("httpx: invalid policy on %s %s: %v", method, full, err))
	}
	if err := g.rt.requirementsFor(p); err != nil {
		panic(fmt.Sprintf("httpx: %s %s needs %v", method, full, err))
	}

	g.rt.routes = append(g.rt.routes, RouteInfo{Method: method, Pattern: full, Policy: p})

	chain := g.rt.chain(p)
	handler := g.rt.adapt(p, h)
	for i := len(chain) - 1; i >= 0; i-- {
		handler = chain[i](handler)
	}
	g.rt.mux.Method(method, full, handler)

	g.rt.registerPreflight(full)
}

// registerPreflight attaches an OPTIONS handler to a pattern.
//
// Without it a browser cannot make ANY cross-origin request that carries a JSON
// content type or a custom header, because those trigger a preflight and chi
// answers 405 for an unregistered method — before the CORS middleware runs, so
// the middleware's own OPTIONS branch is never reached. That failure is
// invisible from the server's side and looks like a CORS misconfiguration from
// the browser's.
//
// It is attached automatically rather than declared by a module, because a
// preflight is a property of the ROUTE EXISTING, not a decision anyone should
// have to remember. Forgetting it on one route is exactly the bug this replaces.
func (rt *Router) registerPreflight(pattern string) {
	if rt.preflighted[pattern] {
		return
	}
	rt.preflighted[pattern] = true

	// The cors middleware answers OPTIONS itself, so this handler is unreachable
	// in practice. It returns 204 rather than panicking, because "unreachable"
	// should degrade to the correct answer rather than to a 500.
	noContent := func(context.Context, *Request) (Response, error) {
		return NoContent(), nil
	}

	p := policy.Preflight
	chain := rt.chain(p)
	handler := rt.adapt(p, noContent)
	for i := len(chain) - 1; i >= 0; i-- {
		handler = chain[i](handler)
	}
	rt.mux.Method(http.MethodOptions, pattern, handler)
}

// requirementsFor reports dependencies a policy needs that the router does not
// have. Registering such a route would produce an endpoint whose protection
// silently does nothing, so it is refused at startup.
func (rt *Router) requirementsFor(p policy.Policy) error {
	var missing []string

	switch p.Auth {
	case policy.AuthSession, policy.AuthSessionOrGuest, policy.AuthAdmin:
		if rt.deps.Sessions == nil {
			missing = append(missing, "a session resolver")
		}
	case policy.AuthSignature:
		if rt.deps.Verifier == nil {
			missing = append(missing, "a signature verifier")
		}
	}
	if !p.Ownership.IsZero() && rt.deps.Owners == nil {
		missing = append(missing, "an owner resolver")
	}
	if p.Idempotent && rt.deps.Idem == nil {
		missing = append(missing, "an idempotency store")
	}

	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("%v, which is not configured", missing)
}

// adapt turns a HandlerFunc into an http.Handler, mapping the returned error to
// a response through the single mapping in errors.go.
func (rt *Router) adapt(p policy.Policy, h HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		req := &Request{r: r, w: w}
		if raw, ok := ctx.Value(ctxKeyRawBody).([]byte); ok {
			req.body = raw
		}

		resp, err := h(ctx, req)
		if err != nil {
			rt.fail(w, r, p, err)
			return
		}

		if err := write(w, resp); err != nil {
			// The status is already sent, so this can only be logged.
			rt.log(r).Error("response write failed", "error", err.Error(), "policy", p.Name)
		}
	})
}

// fail renders an error and logs it once, at the boundary that handled it
// (GO-026).
func (rt *Router) fail(w http.ResponseWriter, r *http.Request, p policy.Policy, err error) {
	e := FromError(err)
	requestID, _ := r.Context().Value(ctxKeyRequestID).(string)

	log := rt.log(r).With(
		"policy", p.Name,
		"route", routePattern(r),
		"status", e.Status,
		"code", string(e.Code),
	)

	switch {
	case e.Status >= 500:
		log.Error("request failed", "error", err.Error())
	case e.Status == http.StatusTooManyRequests,
		e.Status == http.StatusForbidden,
		e.Status == http.StatusUnauthorized:
		// Security-relevant refusals are always at least warn, with the reason
		// (LOG-011).
		log.Warn("request refused", "reason", err.Error())
	default:
		// An expected business rejection is not an error (LOG-005).
		log.Info("request rejected", "reason", err.Error())
	}

	writeError(w, requestID, e)
}

// log returns the request-scoped logger, falling back to the base logger.
func (rt *Router) log(r *http.Request) *slog.Logger {
	if l, ok := r.Context().Value(ctxKeyLogger).(*slog.Logger); ok {
		return l
	}
	return rt.deps.Log
}

// routePattern returns the matched pattern for logging (LOG-007).
func routePattern(r *http.Request) string {
	if rc := chi.RouteContext(r.Context()); rc != nil {
		if p := rc.RoutePattern(); p != "" {
			return p
		}
	}
	return "unmatched"
}
