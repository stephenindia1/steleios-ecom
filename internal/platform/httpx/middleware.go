package httpx

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/stephenindia1/steleios-ecom/internal/platform/authz"
	"github.com/stephenindia1/steleios-ecom/internal/platform/ids"
	"github.com/stephenindia1/steleios-ecom/internal/platform/logging"
	"github.com/stephenindia1/steleios-ecom/internal/platform/policy"
	"github.com/stephenindia1/steleios-ecom/internal/platform/ratelimit"
)

// The middleware chain is assembled here and nowhere else (SEC-06). Modules
// never construct middleware; they declare a policy and this file derives the
// protection from it.
//
// Ordering is load-bearing and documented inline. Two constraints in
// particular: IP rate limiting precedes authentication so unauthenticated
// floods die cheaply, and actor rate limiting follows it because it needs an
// identity (SEC-07).
//
// Every middleware fails closed. If a check cannot complete — Redis down, the
// session store unreachable — the request is refused, never admitted
// (BR-SEC-11, SEC-08).

const ctxKeyLogger ctxKey = 100

// Header names used by the chain.
const (
	headerRequestID     = "X-Request-Id"
	headerCorrelationID = "X-Correlation-Id"
	headerCSRFToken     = "X-CSRF-Token"
	headerIdempotency   = "Idempotency-Key"
	headerSignature     = "X-Razorpay-Signature"
	headerRateSubject   = "X-Rate-Subject"
	cookieCSRF          = "csrf_token"
	cookieSession       = "steleios_session"
)

// chain returns the middleware for a policy, outermost first.
func (rt *Router) chain(p policy.Policy) []func(http.Handler) http.Handler {
	mw := make([]func(http.Handler) http.Handler, 0, 16) // DB-024

	//  1. Correlation. Everything downstream logs under this id (OBS-010).
	mw = append(mw, rt.requestID)
	//  2. Client address, honoured only from a trusted proxy.
	mw = append(mw, rt.realIP)
	//  3. Panic recovery, before anything that can panic (SEC-07).
	mw = append(mw, rt.recoverer)
	//  4. Access log, so even a refused request leaves one line (LOG-006).
	mw = append(mw, rt.accessLog(p))
	//  5. Deadline. No request runs unbounded (GO-033).
	mw = append(mw, rt.timeout(p))
	//  6. Body ceiling, before any parsing. Signature routes buffer here so the
	//     raw bytes are available for HMAC before decoding (BR-PAY-05, DB-026).
	mw = append(mw, rt.bodyLimit(p))
	//  7. Security headers on every response, including errors.
	mw = append(mw, rt.securityHeaders)
	//  8. CORS against a strict allowlist.
	mw = append(mw, rt.cors)
	//  9. IP rate limit: cheap, pre-auth, so a flood costs us nothing.
	mw = append(mw, rt.rateLimitIP(p))
	// 10. Signature verification for provider webhooks.
	if p.Auth == policy.AuthSignature {
		mw = append(mw, rt.verifySignature(p))
	}
	// 11. Resolve the actor. This does not require one; step 13 does.
	if p.Auth != policy.AuthSignature && p.Auth != policy.AuthNone {
		mw = append(mw, rt.loadSession(p))
	}
	// 12. CSRF, after the session so the token can be bound to it.
	if p.CSRF {
		mw = append(mw, rt.csrf)
	}
	// 13. Require the authentication the policy declares.
	mw = append(mw, rt.requireAuth(p))
	// 14. Permission, then ownership — both through the one enforcer (SEC-10).
	if p.Permission != "" {
		mw = append(mw, rt.requirePermission(p))
	}
	if !p.Ownership.IsZero() {
		mw = append(mw, rt.requireOwnership(p))
	}
	// 15. Actor rate limit. Needs the identity resolved above.
	mw = append(mw, rt.rateLimitActor(p))
	// 16. Idempotency, last, so a replay short-circuits the handler only after
	//     every security check has passed (BR-CHK-02).
	if p.Idempotent {
		mw = append(mw, rt.idempotency(p))
	}

	return mw
}

// requestID assigns a correlation identifier and returns it on the response, so
// a customer's screenshot is enough to find the logs (OBS-012).
func (rt *Router) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A client-supplied id is accepted only as a correlation hint and is
		// never trusted as our own request id.
		requestID, err := ids.Token(16)
		if err != nil {
			// Without an id we cannot correlate anything, so refuse rather than
			// serve an untraceable request.
			writeError(w, "", Unavailable(fmt.Errorf("request id: %w", err)))
			return
		}

		correlationID := r.Header.Get(headerCorrelationID)
		if correlationID == "" || len(correlationID) > 64 {
			correlationID = requestID
		}

		ctx, log := logging.WithRequest(r.Context(), rt.deps.Log, requestID, correlationID)
		ctx = context.WithValue(ctx, ctxKeyRequestID, requestID)
		ctx = context.WithValue(ctx, ctxKeyLogger, log)

		w.Header().Set(headerRequestID, requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// realIP honours a forwarded address only when the immediate peer is a trusted
// proxy. Trusting the header unconditionally would let any client forge its own
// address and evade IP rate limiting.
func (rt *Router) realIP(next http.Handler) http.Handler {
	trusted := rt.deps.Cfg.HTTP.TrustedProxies

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer := peerIP(r.RemoteAddr)
		if len(trusted) > 0 && slices.Contains(trusted, peer) {
			if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
				// The left-most entry is the original client.
				if first, _, ok := strings.Cut(fwd, ","); ok {
					r.RemoteAddr = strings.TrimSpace(first)
				} else {
					r.RemoteAddr = strings.TrimSpace(fwd)
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// recoverer turns a panic into a 500 with a logged stack, never a stack trace on
// the wire (BR-SEC-09, GO-027).
func (rt *Router) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if errors.Is(rec.(error), http.ErrAbortHandler) { //nolint:errcheck,forcetypeassert // re-panic below covers the non-error case
					panic(rec)
				}
				requestID, _ := r.Context().Value(ctxKeyRequestID).(string)
				rt.log(r).Error("panic recovered",
					"panic", fmt.Sprint(rec),
					"route", routePattern(r),
				)
				writeError(w, requestID, Internal(fmt.Errorf("panic: %v", rec)))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// statusRecorder captures the status and size for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// accessLog emits exactly one line per request, at completion, with the route
// pattern rather than the raw path (LOG-006, LOG-007).
func (rt *Router) accessLog(p policy.Policy) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := rt.deps.Clock.Now()
			rec := &statusRecorder{ResponseWriter: w}

			next.ServeHTTP(rec, r)

			if rec.status == 0 {
				rec.status = http.StatusOK
			}
			actor := actorFrom(r.Context())
			rt.log(r).Info("request",
				"method", r.Method,
				"route", routePattern(r),
				"status", rec.status,
				"duration_ms", rt.deps.Clock.Now().Sub(start).Milliseconds(),
				"bytes", rec.bytes,
				"policy", p.Name,
				"actor_type", string(actor.Type),
				"actor_id", actor.ID,
			)
		})
	}
}

// timeout bounds the request. The policy may override the default.
func (rt *Router) timeout(p policy.Policy) func(http.Handler) http.Handler {
	d := p.Timeout
	if d <= 0 {
		d = rt.deps.Cfg.HTTP.RequestTimeout
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// bodyLimit caps the request body before anything parses it, and buffers the
// raw bytes for signature routes.
func (rt *Router) bodyLimit(p policy.Policy) func(http.Handler) http.Handler {
	limit := rt.deps.Cfg.HTTP.MaxBodyBytes
	if limit <= 0 {
		limit = 1 << 20
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}

			if !p.RawBody {
				next.ServeHTTP(w, r)
				return
			}

			// BR-PAY-05: read the exact bytes before any decoding, because the
			// HMAC is over the raw body. Re-marshalling parsed JSON would
			// change whitespace and key order and break verification.
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				requestID, _ := r.Context().Value(ctxKeyRequestID).(string)
				var maxErr *http.MaxBytesError
				if errors.As(err, &maxErr) {
					writeError(w, requestID, PayloadTooLarge(err))
					return
				}
				writeError(w, requestID, BadRequest(err))
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))

			ctx := context.WithValue(r.Context(), ctxKeyRawBody, raw)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// securityHeaders sets the headers that apply to every response.
func (rt *Router) securityHeaders(next http.Handler) http.Handler {
	hsts := fmt.Sprintf("max-age=%d; includeSubDomains", int(rt.deps.Cfg.Security.HSTSMaxAge.Seconds()))
	production := rt.deps.Cfg.Env.IsProduction()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		// The API serves JSON only, so nothing may be loaded or framed from it.
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'")
		if production {
			h.Set("Strict-Transport-Security", hsts)
		}
		next.ServeHTTP(w, r)
	})
}

// cors answers preflight and sets response headers for allowlisted origins
// only. An origin that is not listed gets no CORS headers at all, which the
// browser treats as a refusal.
func (rt *Router) cors(next http.Handler) http.Handler {
	allowed := rt.deps.Cfg.HTTP.AllowedOrigins

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && slices.Contains(allowed, origin) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Set("Access-Control-Allow-Headers",
				strings.Join([]string{"Content-Type", headerCSRFToken, headerIdempotency, headerCorrelationID}, ", "))
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			h.Set("Access-Control-Max-Age", "600")
			h.Add("Vary", "Origin")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// rateLimitIP applies the policy's IP-scoped rules before authentication.
func (rt *Router) rateLimitIP(p policy.Policy) func(http.Handler) http.Handler {
	rules := rulesForScope(p, ratelimit.ScopeIP)
	subjectRules := rulesForScope(p, ratelimit.ScopeSubject)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !rt.applyRules(w, r, p, rules, peerIP(r.RemoteAddr)) {
				return
			}
			// A subject rule counts against a business key the client names —
			// a phone number for OTP, an account for login. It is applied here,
			// pre-auth, because these routes are unauthenticated by nature.
			if len(subjectRules) > 0 {
				subject := r.Header.Get(headerRateSubject)
				if subject == "" {
					subject = peerIP(r.RemoteAddr)
				}
				if !rt.applyRules(w, r, p, subjectRules, subject) {
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// rateLimitActor applies the policy's actor-scoped rules after authentication.
func (rt *Router) rateLimitActor(p policy.Policy) func(http.Handler) http.Handler {
	rules := rulesForScope(p, ratelimit.ScopeActor)
	if len(rules) == 0 {
		return passthrough
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actor := actorFrom(r.Context())
			key := actor.ID
			if key == "" {
				key = peerIP(r.RemoteAddr)
			}
			if !rt.applyRules(w, r, p, rules, key) {
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// applyRules evaluates rules and writes the refusal if any is exceeded. It
// reports whether the request may continue.
func (rt *Router) applyRules(w http.ResponseWriter, r *http.Request, p policy.Policy, rules []ratelimit.Rule, key string) bool {
	requestID, _ := r.Context().Value(ctxKeyRequestID).(string)

	for _, rule := range rules {
		res, err := rt.deps.Limiter.Allow(r.Context(), rule, p.Name+":"+key)
		if err != nil {
			// Fail closed: an unavailable limiter must not become an open door
			// (RD-011, BR-SEC-11).
			rt.log(r).Error("rate limiter unavailable", "policy", p.Name, "error", err.Error())
			writeError(w, requestID, Unavailable(err))
			return false
		}
		if !res.Allowed {
			rt.log(r).Warn("rate limit exceeded",
				"policy", p.Name, "scope", string(rule.Scope), "limit", rule.Limit)
			writeError(w, requestID, RateLimited(int(res.RetryAfter.Seconds()), ratelimit.ErrLimited))
			return false
		}
	}
	return true
}

// verifySignature authenticates a provider webhook from the raw body.
func (rt *Router) verifySignature(p policy.Policy) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestID, _ := r.Context().Value(ctxKeyRequestID).(string)

			raw, ok := r.Context().Value(ctxKeyRawBody).([]byte)
			if !ok {
				// Unreachable: policy validation requires RawBody with
				// AuthSignature. Refuse rather than verify nothing.
				writeError(w, requestID, Internal(errors.New("raw body not buffered")))
				return
			}

			actor, err := rt.deps.Verifier.Verify(r.Context(), routePattern(r), r.Header.Get(headerSignature), raw)
			if err != nil {
				// A failed signature is a security event, not a client error
				// (LOG-011, BR-PAY-05).
				rt.log(r).Warn("webhook signature rejected",
					"policy", p.Name, "route", routePattern(r), "reason", err.Error())
				writeError(w, requestID, BadRequest(errors.New("signature verification failed")))
				return
			}

			ctx := context.WithValue(r.Context(), ctxKeyActor, actor)
			ctx = logging.WithActor(ctx, actor.ID, string(actor.Type))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// loadSession resolves the actor. It does not require one — requireAuth does —
// so that a guest-capable route can proceed without a session.
func (rt *Router) loadSession(p policy.Policy) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actor := authz.Actor{Type: authz.ActorGuest}

			if c, err := r.Cookie(cookieSession); err == nil && c.Value != "" {
				resolved, err := rt.deps.Sessions.Resolve(r.Context(), c.Value)
				switch {
				case err == nil:
					actor = resolved
				case errors.Is(err, authz.ErrUnauthenticated):
					// An expired or unknown session is simply not a session.
					// The route's own auth requirement decides what happens.
				default:
					// The store could not answer. Fail closed rather than
					// downgrade an authenticated customer to a guest (SES-008).
					requestID, _ := r.Context().Value(ctxKeyRequestID).(string)
					rt.log(r).Error("session store unavailable", "policy", p.Name, "error", err.Error())
					writeError(w, requestID, Unavailable(err))
					return
				}
			}

			ctx := context.WithValue(r.Context(), ctxKeyActor, actor)
			if actor.ID != "" {
				ctx = logging.WithActor(ctx, actor.ID, string(actor.Type))
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// csrf enforces the double-submit token on state-changing requests.
//
// SameSite=Lax alone is not sufficient: it does not protect top-level POST
// navigations in every browser, and it offers nothing once a subdomain is
// compromised.
func (rt *Router) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		requestID, _ := r.Context().Value(ctxKeyRequestID).(string)

		cookie, err := r.Cookie(cookieCSRF)
		header := r.Header.Get(headerCSRFToken)
		if err != nil || cookie.Value == "" || header == "" {
			rt.log(r).Warn("csrf token missing", "route", routePattern(r))
			writeError(w, requestID, Forbidden(errors.New("csrf token missing")))
			return
		}

		// GO-077: constant-time comparison. A timing side channel on a CSRF
		// token is a real, if slow, oracle.
		if subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(header)) != 1 {
			rt.log(r).Warn("csrf token mismatch", "route", routePattern(r))
			writeError(w, requestID, Forbidden(errors.New("csrf token mismatch")))
			return
		}

		next.ServeHTTP(w, r)
	})
}

// requireAuth enforces the authentication mode the policy declares.
func (rt *Router) requireAuth(p policy.Policy) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actor := actorFrom(r.Context())
			requestID, _ := r.Context().Value(ctxKeyRequestID).(string)

			var err error
			switch p.Auth {
			case policy.AuthNone, policy.AuthSignature:
				// Already established, or deliberately absent.

			case policy.AuthSessionOrGuest:
				if actor.Type != authz.ActorCustomer && actor.Type != authz.ActorGuest {
					err = fmt.Errorf("%w: a customer or guest session is required", authz.ErrUnauthenticated)
				}

			case policy.AuthSession:
				if actor.Type != authz.ActorCustomer || !actor.IsAuthenticated() {
					err = fmt.Errorf("%w: a signed-in customer is required", authz.ErrUnauthenticated)
				}

			case policy.AuthAdmin:
				if actor.Type != authz.ActorAdmin || !actor.IsAuthenticated() {
					err = fmt.Errorf("%w: a staff session is required", authz.ErrUnauthenticated)
				}

			default:
				// Unreachable: policy validation rejects an unset mode. Fail
				// closed anyway rather than assume.
				err = fmt.Errorf("%w: unknown auth mode", authz.ErrDenied)
			}

			if err != nil {
				rt.log(r).Warn("authentication required",
					"policy", p.Name, "route", routePattern(r), "actor_type", string(actor.Type))
				writeError(w, requestID, FromError(err))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// requirePermission checks the policy's action through the one enforcer.
func (rt *Router) requirePermission(p policy.Policy) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actor := actorFrom(r.Context())
			requestID, _ := r.Context().Value(ctxKeyRequestID).(string)

			// Resource is unnamed here: this is the coarse "may this actor do
			// this kind of thing at all" gate. The service re-asserts the check
			// against the specific resource (SEC-09, BR-ADM-02).
			if err := rt.deps.Authz.Can(r.Context(), actor, p.Permission, authz.Resource{Type: "*"}); err != nil {
				rt.log(r).Warn("permission denied",
					"policy", p.Name, "action", string(p.Permission), "reason", err.Error())
				writeError(w, requestID, Forbidden(err))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// requireOwnership enforces that the actor owns the addressed resource.
//
// This is the check that stops GET /orders/{someone-elses-id} (BR-ORD-05). It
// is generic so that no handler has to remember it.
func (rt *Router) requireOwnership(p policy.Policy) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actor := actorFrom(r.Context())
			requestID, _ := r.Context().Value(ctxKeyRequestID).(string)

			resourceID := chiParam(r, p.Ownership.PathParam)
			if resourceID == "" {
				writeError(w, requestID, NotFound(errors.New("no resource id in path")))
				return
			}

			ownerID, err := rt.deps.Owners.OwnerOf(r.Context(), p.Ownership.ResourceType, resourceID)
			if err != nil {
				if errors.Is(err, ErrNotFound) {
					// Indistinguishable from "not yours": both are 404, so the
					// response cannot be used to test whether an id exists
					// (SEC-12).
					writeError(w, requestID, NotFound(err))
					return
				}
				rt.log(r).Error("owner lookup failed",
					"policy", p.Name, "resource", p.Ownership.ResourceType, "error", err.Error())
				writeError(w, requestID, Unavailable(err))
				return
			}

			res := authz.Resource{Type: p.Ownership.ResourceType, ID: resourceID, OwnerID: ownerID}
			action := authz.Action(p.Ownership.ResourceType + ":read")
			if p.Permission != "" {
				action = p.Permission
			}

			if err := rt.deps.Authz.Can(r.Context(), actor, action, res); err != nil {
				rt.log(r).Warn("ownership denied",
					"policy", p.Name,
					"resource", p.Ownership.ResourceType,
					"resource_id", resourceID,
					"reason", err.Error())
				writeError(w, requestID, NotFound(err))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// idempotencyRecorder captures the response so it can be replayed.
type idempotencyRecorder struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (i *idempotencyRecorder) WriteHeader(code int) {
	i.status = code
	i.ResponseWriter.WriteHeader(code)
}

func (i *idempotencyRecorder) Write(b []byte) (int, error) {
	if i.status == 0 {
		i.status = http.StatusOK
	}
	i.buf.Write(b)
	return i.ResponseWriter.Write(b)
}

// idempotency replays a stored response for a repeated key, so a double-clicked
// Pay button cannot produce two orders (BR-CHK-02).
func (rt *Router) idempotency(p policy.Policy) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestID, _ := r.Context().Value(ctxKeyRequestID).(string)

			key := r.Header.Get(headerIdempotency)
			if key == "" || len(key) > 128 {
				writeError(w, requestID, BadRequest(
					fmt.Errorf("%s header is required on this endpoint", headerIdempotency)))
				return
			}

			actor := actorFrom(r.Context())
			stored, err := rt.deps.Idem.Lookup(r.Context(), actor.ID, key)
			if err != nil {
				// Fail closed: without the ledger we cannot promise the request
				// happens once, and creating a second order is worse than
				// refusing.
				rt.log(r).Error("idempotency store unavailable", "policy", p.Name, "error", err.Error())
				writeError(w, requestID, Unavailable(err))
				return
			}
			if stored != nil {
				rt.log(r).Info("idempotent replay", "policy", p.Name)
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.Header().Set("Idempotent-Replay", "true")
				w.WriteHeader(stored.Status)
				_, _ = w.Write(stored.Body) //nolint:errcheck // response committed
				return
			}

			rec := &idempotencyRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)

			// Only successful outcomes are replayable. Replaying a 500 would
			// make a transient failure permanent.
			if rec.status >= 200 && rec.status < 300 {
				if err := rt.deps.Idem.Save(r.Context(), actor.ID, key, rec.status, rec.buf.Bytes()); err != nil {
					rt.log(r).Error("idempotency save failed", "policy", p.Name, "error", err.Error())
				}
			}
		})
	}
}

// chiParam reads a path parameter from the routing context.
func chiParam(r *http.Request, name string) string { return chi.URLParam(r, name) }

// passthrough is a no-op middleware, used where a policy declares nothing to do.
func passthrough(next http.Handler) http.Handler { return next }

// rulesForScope returns the policy's rules for one scope.
func rulesForScope(p policy.Policy, scope ratelimit.Scope) []ratelimit.Rule {
	out := make([]ratelimit.Rule, 0, len(p.RateLimit.Rules)) // DB-024
	for _, r := range p.RateLimit.Rules {
		if r.Scope == scope {
			out = append(out, r)
		}
	}
	return out
}

// actorFrom returns the actor on the context, defaulting to a guest so that a
// caller can never mistake "not resolved" for "authenticated".
func actorFrom(ctx context.Context) authz.Actor {
	if a, ok := ctx.Value(ctxKeyActor).(authz.Actor); ok {
		return a
	}
	return authz.Actor{Type: authz.ActorGuest}
}

// peerIP extracts the address portion of a RemoteAddr.
func peerIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}
