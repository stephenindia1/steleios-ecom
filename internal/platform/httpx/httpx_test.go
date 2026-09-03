package httpx_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stephenindia1/steleios-ecom/internal/platform/authz"
	"github.com/stephenindia1/steleios-ecom/internal/platform/clock"
	"github.com/stephenindia1/steleios-ecom/internal/platform/config"
	"github.com/stephenindia1/steleios-ecom/internal/platform/httpx"
	"github.com/stephenindia1/steleios-ecom/internal/platform/policy"
	"github.com/stephenindia1/steleios-ecom/internal/platform/ratelimit"
)

// ---------------------------------------------------------------------------
// Fakes. Each one can be made to fail, because the failure behaviour — fail
// closed — is as important as the success behaviour (BR-SEC-11, SEC-08).
// ---------------------------------------------------------------------------

type fakeSessions struct {
	actor authz.Actor
	err   error
}

func (f fakeSessions) Resolve(context.Context, string) (authz.Actor, error) {
	return f.actor, f.err
}

type fakeOwners struct {
	owner string
	err   error
}

func (f fakeOwners) OwnerOf(context.Context, string, string) (string, error) {
	return f.owner, f.err
}

type fakeIdem struct {
	stored *httpx.StoredResponse
	err    error
	saved  int
}

func (f *fakeIdem) Lookup(context.Context, string, string) (*httpx.StoredResponse, error) {
	return f.stored, f.err
}

func (f *fakeIdem) Save(_ context.Context, _, _ string, status int, body []byte) error {
	f.saved++
	f.stored = &httpx.StoredResponse{Status: status, Body: body}
	return nil
}

type fakeVerifier struct {
	actor authz.Actor
	err   error
	body  []byte // what the verifier was handed, so the test can assert it is raw
}

func (f *fakeVerifier) Verify(_ context.Context, _, _ string, raw []byte) (authz.Actor, error) {
	f.body = raw
	return f.actor, f.err
}

type errLimiter struct{}

func (errLimiter) Allow(context.Context, ratelimit.Rule, string) (ratelimit.Result, error) {
	return ratelimit.Result{}, errors.New("redis is down")
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

func testConfig() config.Config {
	return config.Config{
		Env: config.EnvTest,
		HTTP: config.HTTP{
			MaxBodyBytes:   1024,
			RequestTimeout: 5 * time.Second,
			AllowedOrigins: []string{"https://shop.example"},
		},
		Security: config.Security{HSTSMaxAge: time.Hour},
	}
}

func testDeps() httpx.Deps {
	return httpx.Deps{
		Cfg:      testConfig(),
		Log:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Clock:    clock.NewFake(time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)),
		Authz:    authz.NewRBAC(),
		Limiter:  ratelimit.AllowAll{},
		Sessions: fakeSessions{actor: authz.Actor{Type: authz.ActorGuest}},
		Owners:   fakeOwners{},
		Idem:     &fakeIdem{},
		Verifier: &fakeVerifier{},
	}
}

func mustRouter(t *testing.T, d httpx.Deps) *httpx.Router {
	t.Helper()
	rt, err := httpx.NewRouter(d)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return rt
}

// okHandler records that it ran.
func okHandler(ran *bool) httpx.HandlerFunc {
	return func(context.Context, *httpx.Request) (httpx.Response, error) {
		if ran != nil {
			*ran = true
		}
		return httpx.OK(map[string]string{"ok": "yes"}), nil
	}
}

func do(rt *httpx.Router, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	rt.Handler().ServeHTTP(rec, req)
	return rec
}

func errorBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var env struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("error response is not the envelope: %v\n%s", err, rec.Body.String())
	}
	return env.Error
}

// ---------------------------------------------------------------------------
// Registration: the structural guarantee
// ---------------------------------------------------------------------------

func TestNewRouterRequiresItsDependencies(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		bust func(*httpx.Deps)
		want string
	}{
		{name: "no logger", bust: func(d *httpx.Deps) { d.Log = nil }, want: "logger is nil"},
		{name: "no clock", bust: func(d *httpx.Deps) { d.Clock = nil }, want: "clock is nil"},
		{name: "no enforcer", bust: func(d *httpx.Deps) { d.Authz = nil }, want: "authz enforcer is nil"},
		{name: "no limiter", bust: func(d *httpx.Deps) { d.Limiter = nil }, want: "rate limiter is nil"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			d := testDeps()
			tc.bust(&d)
			_, err := httpx.NewRouter(d)
			if err == nil {
				t.Fatal("router built without a required dependency")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestRegisteringAnInvalidPolicyPanicsAtStartup(t *testing.T) {
	t.Parallel()

	// SEC-01: this is the whole point of the design. A zero policy is a
	// programmer error that must stop the process, not serve traffic.
	defer func() {
		rec := recover()
		if rec == nil {
			t.Fatal("registering a route with the zero policy did not panic")
		}
		msg, _ := rec.(string)
		if !strings.Contains(msg, "invalid policy") {
			t.Errorf("panic message = %q, want it to name the invalid policy", msg)
		}
	}()

	rt := mustRouter(t, testDeps())
	rt.Group("/api/v1").GET("/thing", policy.Policy{}, okHandler(nil))
}

func TestRegisteringWithoutARequiredDependencyPanics(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		bust func(*httpx.Deps)
		pol  policy.Policy
		want string
	}{
		{
			name: "session policy without a session resolver",
			bust: func(d *httpx.Deps) { d.Sessions = nil },
			pol:  policy.CustomerSession,
			want: "session resolver",
		},
		{
			name: "ownership policy without an owner resolver",
			bust: func(d *httpx.Deps) { d.Owners = nil },
			pol:  policy.CustomerOrderRead,
			want: "owner resolver",
		},
		{
			name: "idempotent policy without an idempotency store",
			bust: func(d *httpx.Deps) { d.Idem = nil },
			pol:  policy.Checkout,
			want: "idempotency store",
		},
		{
			name: "signature policy without a verifier",
			bust: func(d *httpx.Deps) { d.Verifier = nil },
			pol:  policy.ProviderWebhook,
			want: "signature verifier",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			defer func() {
				rec := recover()
				if rec == nil {
					t.Fatalf("registered a route whose protection could not work")
				}
				if msg, _ := rec.(string); !strings.Contains(msg, tc.want) {
					t.Errorf("panic = %q, want it to name %q", msg, tc.want)
				}
			}()

			d := testDeps()
			tc.bust(&d)
			mustRouter(t, d).Group("/api/v1").POST("/thing", tc.pol, okHandler(nil))
		})
	}
}

func TestEveryRegisteredRouteHasAValidPolicy(t *testing.T) {
	t.Parallel()

	// TST-02: the automated form of "a route cannot exist without protection".
	rt := mustRouter(t, testDeps())
	g := rt.Group("/api/v1")
	g.GET("/products", policy.PublicCached, okHandler(nil))
	g.POST("/cart/items", policy.GuestOrSession, okHandler(nil))
	g.GET("/orders/{id}", policy.CustomerOrderRead, okHandler(nil))
	g.POST("/checkout", policy.Checkout, okHandler(nil))
	g.POST("/webhooks/razorpay", policy.ProviderWebhook, okHandler(nil))
	g.Mount("/admin", func(a *httpx.Group) {
		a.PATCH("/orders/{id}/status", policy.AdminOps, okHandler(nil))
	})

	routes := rt.Routes()
	if len(routes) != 6 {
		t.Fatalf("registered %d routes, want 6", len(routes))
	}

	for _, r := range routes {
		if err := r.Policy.Validate(); err != nil {
			t.Errorf("%s %s carries an invalid policy: %v", r.Method, r.Pattern, err)
		}
		if r.Policy.Name == "" {
			t.Errorf("%s %s has an unnamed policy", r.Method, r.Pattern)
		}
	}

	// Nested groups must carry the full path, or the route table lies.
	var found bool
	for _, r := range routes {
		if r.Pattern == "/api/v1/admin/orders/{id}/status" {
			found = true
		}
	}
	if !found {
		t.Errorf("nested route pattern was not recorded with its prefix: %+v", routes)
	}
}

// ---------------------------------------------------------------------------
// Authentication and authorization
// ---------------------------------------------------------------------------

func TestAuthenticationRequirements(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		pol        policy.Policy
		actor      authz.Actor
		withCookie bool
		wantStatus int
	}{
		{
			name: "public route serves an anonymous request",
			pol:  policy.Public, wantStatus: http.StatusOK,
		},
		{
			name: "guest reaches a guest-or-session route",
			pol:  policy.GuestOrSession, actor: authz.Actor{Type: authz.ActorGuest},
			wantStatus: http.StatusOK,
		},
		{
			name: "guest is refused a customer route",
			pol:  policy.CustomerSession, actor: authz.Actor{Type: authz.ActorGuest},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "customer reaches a customer route",
			pol:  policy.CustomerSession, actor: authz.Actor{ID: "c1", Type: authz.ActorCustomer},
			withCookie: true, wantStatus: http.StatusOK,
		},
		{
			name: "customer is refused an admin route",
			pol:  policy.AdminRead, actor: authz.Actor{ID: "c1", Type: authz.ActorCustomer},
			withCookie: true, wantStatus: http.StatusUnauthorized,
		},
		{
			name: "admin without the permission is forbidden",
			pol:  policy.AdminOps,
			actor: authz.Actor{ID: "s1", Type: authz.ActorAdmin,
				Roles: []authz.Role{authz.RoleViewer}}, // viewer cannot write orders
			withCookie: true, wantStatus: http.StatusForbidden,
		},
		{
			name: "admin with the permission is served",
			pol:  policy.AdminOps,
			actor: authz.Actor{ID: "s1", Type: authz.ActorAdmin,
				Roles: []authz.Role{authz.RoleOps}},
			withCookie: true, wantStatus: http.StatusOK,
		},
		{
			name: "a customer id alone does not make an admin",
			pol:  policy.AdminRead,
			actor: authz.Actor{ID: "s1", Type: authz.ActorCustomer,
				Roles: []authz.Role{authz.RoleAdmin}},
			withCookie: true, wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			d := testDeps()
			d.Sessions = fakeSessions{actor: tc.actor}
			rt := mustRouter(t, d)
			rt.Group("/api/v1").GET("/thing", tc.pol, okHandler(nil))

			req := httptest.NewRequest(http.MethodGet, "/api/v1/thing", nil)
			if tc.withCookie {
				req.AddCookie(&http.Cookie{Name: "steleios_session", Value: "session-token"})
			}

			rec := do(rt, req)
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestOwnershipIsEnforcedGenerically(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		actor      authz.Actor
		owner      string
		ownerErr   error
		wantStatus int
	}{
		{
			name:  "own order is served",
			actor: authz.Actor{ID: "c1", Type: authz.ActorCustomer},
			owner: "c1", wantStatus: http.StatusOK,
		},
		{
			name:  "another customer's order is 404, not 403",
			actor: authz.Actor{ID: "c1", Type: authz.ActorCustomer},
			owner: "c2", wantStatus: http.StatusNotFound,
		},
		{
			name:     "a missing order is 404 — indistinguishable from not yours",
			actor:    authz.Actor{ID: "c1", Type: authz.ActorCustomer},
			ownerErr: httpx.ErrNotFound, wantStatus: http.StatusNotFound,
		},
		{
			name:     "an owner lookup failure fails closed with 503",
			actor:    authz.Actor{ID: "c1", Type: authz.ActorCustomer},
			ownerErr: errors.New("database down"), wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:  "an order with no recorded owner is not public",
			actor: authz.Actor{ID: "c1", Type: authz.ActorCustomer},
			owner: "", wantStatus: http.StatusNotFound,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			d := testDeps()
			d.Sessions = fakeSessions{actor: tc.actor}
			d.Owners = fakeOwners{owner: tc.owner, err: tc.ownerErr}

			rt := mustRouter(t, d)
			rt.Group("/api/v1").GET("/orders/{id}", policy.CustomerOrderRead, okHandler(nil))

			req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/ord-1", nil)
			req.AddCookie(&http.Cookie{Name: "steleios_session", Value: "t"})

			rec := do(rt, req)
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestDeniedResponseRevealsNothing(t *testing.T) {
	t.Parallel()

	// SEC-12: a 403 would confirm the order number is real. The body must not
	// name the owner, the resource, or the reason.
	d := testDeps()
	d.Sessions = fakeSessions{actor: authz.Actor{ID: "c1", Type: authz.ActorCustomer}}
	d.Owners = fakeOwners{owner: "c2-secret-customer"}

	rt := mustRouter(t, d)
	rt.Group("/api/v1").GET("/orders/{id}", policy.CustomerOrderRead, okHandler(nil))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/ord-real", nil)
	req.AddCookie(&http.Cookie{Name: "steleios_session", Value: "t"})
	rec := do(rt, req)

	body := rec.Body.String()
	for _, leak := range []string{"c2-secret-customer", "does not own", "ord-real"} {
		if strings.Contains(body, leak) {
			t.Errorf("denial response leaked %q: %s", leak, body)
		}
	}
	if e := errorBody(t, rec); e["code"] != "not_found" {
		t.Errorf("code = %v, want not_found", e["code"])
	}
}

// ---------------------------------------------------------------------------
// Failing closed
// ---------------------------------------------------------------------------

func TestDependencyFailuresRefuseTheRequest(t *testing.T) {
	t.Parallel()

	t.Run("rate limiter down", func(t *testing.T) {
		t.Parallel()

		d := testDeps()
		d.Limiter = errLimiter{}
		rt := mustRouter(t, d)

		ran := false
		rt.Group("/api/v1").GET("/thing", policy.Public, okHandler(&ran))

		rec := do(rt, httptest.NewRequest(http.MethodGet, "/api/v1/thing", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503: an unavailable limiter must not become an open door", rec.Code)
		}
		if ran {
			t.Error("the handler ran despite the limiter failing")
		}
	})

	t.Run("session store down", func(t *testing.T) {
		t.Parallel()

		d := testDeps()
		d.Sessions = fakeSessions{err: errors.New("redis down")}
		rt := mustRouter(t, d)

		ran := false
		rt.Group("/api/v1").GET("/thing", policy.CustomerSession, okHandler(&ran))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/thing", nil)
		req.AddCookie(&http.Cookie{Name: "steleios_session", Value: "t"})

		rec := do(rt, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503 (SES-008)", rec.Code)
		}
		if ran {
			t.Error("the handler ran despite the session store failing")
		}
	})

	t.Run("an expired session is simply not a session", func(t *testing.T) {
		t.Parallel()

		// Distinct from the store being down: an unknown session is a normal
		// condition and must produce 401, not 503.
		d := testDeps()
		d.Sessions = fakeSessions{err: authz.ErrUnauthenticated}
		rt := mustRouter(t, d)
		rt.Group("/api/v1").GET("/thing", policy.CustomerSession, okHandler(nil))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/thing", nil)
		req.AddCookie(&http.Cookie{Name: "steleios_session", Value: "expired"})

		if rec := do(rt, req); rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("idempotency store down", func(t *testing.T) {
		t.Parallel()

		d := testDeps()
		d.Idem = &fakeIdem{err: errors.New("redis down")}
		d.Sessions = fakeSessions{actor: authz.Actor{ID: "c1", Type: authz.ActorCustomer}}
		rt := mustRouter(t, d)

		ran := false
		rt.Group("/api/v1").POST("/checkout", policy.Checkout, okHandler(&ran))

		rec := do(rt, csrfPost("/api/v1/checkout", "tok", map[string]string{"Idempotency-Key": "k1"}))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503: without the ledger we cannot promise once-only", rec.Code)
		}
		if ran {
			t.Error("the handler ran despite the idempotency store failing")
		}
	})
}

func TestRateLimitRefusalCarriesRetryAfter(t *testing.T) {
	t.Parallel()

	d := testDeps()
	d.Limiter = ratelimit.DenyAll{}
	rt := mustRouter(t, d)
	rt.Group("/api/v1").GET("/thing", policy.Public, okHandler(nil))

	rec := do(rt, httptest.NewRequest(http.MethodGet, "/api/v1/thing", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 must carry Retry-After so a client backs off correctly")
	}
	// The response must not enumerate the limit.
	if body := rec.Body.String(); strings.Contains(body, "120") {
		t.Errorf("429 body leaks the configured limit: %s", body)
	}
}

// ---------------------------------------------------------------------------
// CSRF
// ---------------------------------------------------------------------------

func csrfPost(path, token string, headers map[string]string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "steleios_session", Value: "t"})
	if token != "" {
		req.AddCookie(&http.Cookie{Name: "csrf_token", Value: token})
		req.Header.Set("X-CSRF-Token", token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req
}

func TestCSRF(t *testing.T) {
	t.Parallel()

	newRouter := func(t *testing.T) *httpx.Router {
		d := testDeps()
		d.Sessions = fakeSessions{actor: authz.Actor{ID: "c1", Type: authz.ActorCustomer}}
		rt := mustRouter(t, d)
		g := rt.Group("/api/v1")
		g.POST("/thing", policy.CustomerSession, okHandler(nil))
		g.GET("/thing", policy.CustomerSession, okHandler(nil))
		return rt
	}

	t.Run("matching token passes", func(t *testing.T) {
		t.Parallel()
		rec := do(newRouter(t), csrfPost("/api/v1/thing", "tok-123", nil))
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("missing token is refused", func(t *testing.T) {
		t.Parallel()
		rec := do(newRouter(t), csrfPost("/api/v1/thing", "", nil))
		if rec.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", rec.Code)
		}
	})

	t.Run("mismatched token is refused", func(t *testing.T) {
		t.Parallel()
		req := csrfPost("/api/v1/thing", "tok-123", nil)
		req.Header.Set("X-CSRF-Token", "tok-456")
		if rec := do(newRouter(t), req); rec.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", rec.Code)
		}
	})

	t.Run("header without cookie is refused", func(t *testing.T) {
		t.Parallel()
		req := csrfPost("/api/v1/thing", "", nil)
		req.Header.Set("X-CSRF-Token", "tok-123")
		if rec := do(newRouter(t), req); rec.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403: a header alone is not double-submit", rec.Code)
		}
	})

	t.Run("safe methods are exempt", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/thing", nil)
		req.AddCookie(&http.Cookie{Name: "steleios_session", Value: "t"})
		if rec := do(newRouter(t), req); rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rec.Code)
		}
	})
}

// ---------------------------------------------------------------------------
// Signature-authenticated webhooks
// ---------------------------------------------------------------------------

func TestWebhookSignature(t *testing.T) {
	t.Parallel()

	const payload = `{"event":"payment.captured","id":"evt_1"}`

	t.Run("the verifier receives the exact raw bytes", func(t *testing.T) {
		t.Parallel()

		// BR-PAY-05: the HMAC is over the raw body. If anything re-marshals it
		// first, verification breaks in a way that only shows up in production.
		v := &fakeVerifier{actor: authz.Actor{ID: "razorpay", Type: authz.ActorProvider}}
		d := testDeps()
		d.Verifier = v
		rt := mustRouter(t, d)

		var handlerSaw []byte
		rt.Group("").POST("/webhooks/razorpay", policy.ProviderWebhook,
			func(_ context.Context, req *httpx.Request) (httpx.Response, error) {
				raw, err := req.RawBody()
				if err != nil {
					return httpx.Response{}, err
				}
				handlerSaw = raw
				return httpx.OK(map[string]bool{"received": true}), nil
			})

		req := httptest.NewRequest(http.MethodPost, "/webhooks/razorpay", strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Razorpay-Signature", "sig")

		if rec := do(rt, req); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		if string(v.body) != payload {
			t.Errorf("verifier saw %q, want the exact raw payload", v.body)
		}
		if string(handlerSaw) != payload {
			t.Errorf("handler saw %q, want the exact raw payload", handlerSaw)
		}
	})

	t.Run("a rejected signature never reaches the handler", func(t *testing.T) {
		t.Parallel()

		d := testDeps()
		d.Verifier = &fakeVerifier{err: errors.New("signature mismatch")}
		rt := mustRouter(t, d)

		ran := false
		rt.Group("").POST("/webhooks/razorpay", policy.ProviderWebhook, okHandler(&ran))

		req := httptest.NewRequest(http.MethodPost, "/webhooks/razorpay", strings.NewReader(payload))
		req.Header.Set("X-Razorpay-Signature", "wrong")

		rec := do(rt, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
		if ran {
			t.Error("the handler ran on an unverified webhook")
		}
		// The response must not explain what was wrong with the signature.
		if strings.Contains(rec.Body.String(), "mismatch") {
			t.Errorf("response leaked the verification reason: %s", rec.Body.String())
		}
	})

	t.Run("the webhook needs no session and no csrf token", func(t *testing.T) {
		t.Parallel()

		// BR-PAY-06: this exemption is deliberate. The test exists so that
		// "hardening" the route by adding CSRF breaks a test rather than
		// silently breaking payments.
		d := testDeps()
		d.Verifier = &fakeVerifier{actor: authz.Actor{ID: "razorpay", Type: authz.ActorProvider}}
		rt := mustRouter(t, d)
		rt.Group("").POST("/webhooks/razorpay", policy.ProviderWebhook, okHandler(nil))

		req := httptest.NewRequest(http.MethodPost, "/webhooks/razorpay", strings.NewReader(payload))
		req.Header.Set("X-Razorpay-Signature", "sig")
		// deliberately: no session cookie, no csrf cookie, no csrf header

		if rec := do(rt, req); rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
	})
}

// ---------------------------------------------------------------------------
// Idempotency
// ---------------------------------------------------------------------------

func TestIdempotency(t *testing.T) {
	t.Parallel()

	newRouter := func(t *testing.T, idem *fakeIdem, calls *int) *httpx.Router {
		d := testDeps()
		d.Idem = idem
		d.Sessions = fakeSessions{actor: authz.Actor{ID: "c1", Type: authz.ActorCustomer}}
		rt := mustRouter(t, d)
		rt.Group("/api/v1").POST("/checkout", policy.Checkout,
			func(context.Context, *httpx.Request) (httpx.Response, error) {
				*calls++
				return httpx.Created("/api/v1/orders/ord-1", map[string]string{"order": "ord-1"}), nil
			})
		return rt
	}

	t.Run("a repeat replays rather than acting twice", func(t *testing.T) {
		t.Parallel()

		// BR-CHK-02: a double-clicked Pay button must not create two orders.
		idem := &fakeIdem{}
		calls := 0
		rt := newRouter(t, idem, &calls)

		first := do(rt, csrfPost("/api/v1/checkout", "tok", map[string]string{"Idempotency-Key": "key-1"}))
		if first.Code != http.StatusCreated {
			t.Fatalf("first call status = %d, want 201 (body %s)", first.Code, first.Body.String())
		}

		second := do(rt, csrfPost("/api/v1/checkout", "tok", map[string]string{"Idempotency-Key": "key-1"}))
		if second.Code != http.StatusCreated {
			t.Errorf("replay status = %d, want the original 201", second.Code)
		}
		if calls != 1 {
			t.Errorf("handler ran %d times, want exactly 1", calls)
		}
		if second.Header().Get("Idempotent-Replay") != "true" {
			t.Error("a replay should be marked, so a client can tell")
		}
		if first.Body.String() != second.Body.String() {
			t.Errorf("replay body differs:\n first: %s\nsecond: %s", first.Body.String(), second.Body.String())
		}
	})

	t.Run("a missing key is refused", func(t *testing.T) {
		t.Parallel()

		calls := 0
		rt := newRouter(t, &fakeIdem{}, &calls)

		rec := do(rt, csrfPost("/api/v1/checkout", "tok", nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
		if calls != 0 {
			t.Error("the handler ran without an idempotency key")
		}
	})

	t.Run("an absurdly long key is refused", func(t *testing.T) {
		t.Parallel()

		calls := 0
		rt := newRouter(t, &fakeIdem{}, &calls)

		rec := do(rt, csrfPost("/api/v1/checkout", "tok",
			map[string]string{"Idempotency-Key": strings.Repeat("k", 200)}))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})
}

// ---------------------------------------------------------------------------
// Cross-cutting response behaviour
// ---------------------------------------------------------------------------

func TestEveryResponseCarriesARequestID(t *testing.T) {
	t.Parallel()

	// OBS-012: a customer reporting a failure from a screenshot must give
	// support enough to find the logs.
	d := testDeps()
	rt := mustRouter(t, d)
	g := rt.Group("/api/v1")
	g.GET("/ok", policy.Public, okHandler(nil))
	g.GET("/boom", policy.Public, func(context.Context, *httpx.Request) (httpx.Response, error) {
		return httpx.Response{}, errors.New("something broke")
	})

	for _, path := range []string{"/api/v1/ok", "/api/v1/boom"} {
		rec := do(rt, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Header().Get("X-Request-Id") == "" {
			t.Errorf("%s: no X-Request-Id header", path)
		}
	}

	// And the error envelope repeats it in the body.
	rec := do(rt, httptest.NewRequest(http.MethodGet, "/api/v1/boom", nil))
	body := errorBody(t, rec)
	if body["request_id"] == "" || body["request_id"] == nil {
		t.Error("the error envelope must carry the request id")
	}
}

func TestInternalErrorsRevealNothing(t *testing.T) {
	t.Parallel()

	// BR-SEC-09: no driver errors, no stack traces, no internal detail.
	rt := mustRouter(t, testDeps())
	rt.Group("/api/v1").GET("/boom", policy.Public,
		func(context.Context, *httpx.Request) (httpx.Response, error) {
			return httpx.Response{}, errors.New("pq: relation \"orders\" does not exist")
		})

	rec := do(rt, httptest.NewRequest(http.MethodGet, "/api/v1/boom", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "relation") {
		t.Errorf("the driver error reached the client: %s", rec.Body.String())
	}
	if e := errorBody(t, rec); e["code"] != "internal_error" {
		t.Errorf("code = %v, want internal_error", e["code"])
	}
}

func TestPanicBecomesA500WithoutAStackTrace(t *testing.T) {
	t.Parallel()

	rt := mustRouter(t, testDeps())
	rt.Group("/api/v1").GET("/panic", policy.Public,
		func(context.Context, *httpx.Request) (httpx.Response, error) {
			panic("boom: a nil map write")
		})

	rec := do(rt, httptest.NewRequest(http.MethodGet, "/api/v1/panic", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	body := rec.Body.String()
	for _, leak := range []string{"boom", "goroutine", "panic:"} {
		if strings.Contains(body, leak) {
			t.Errorf("panic detail %q reached the client: %s", leak, body)
		}
	}
}

func TestSecurityHeadersAreAlwaysSet(t *testing.T) {
	t.Parallel()

	rt := mustRouter(t, testDeps())
	rt.Group("/api/v1").GET("/thing", policy.Public, okHandler(nil))

	rec := do(rt, httptest.NewRequest(http.MethodGet, "/api/v1/thing", nil))

	want := map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Referrer-Policy":         "strict-origin-when-cross-origin",
		"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'; base-uri 'none'",
	}
	for h, v := range want {
		if got := rec.Header().Get(h); got != v {
			t.Errorf("%s = %q, want %q", h, got, v)
		}
	}
}

func TestCORSAllowlist(t *testing.T) {
	t.Parallel()

	rt := mustRouter(t, testDeps())
	rt.Group("/api/v1").GET("/thing", policy.Public, okHandler(nil))

	t.Run("an allowlisted origin is granted", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/thing", nil)
		req.Header.Set("Origin", "https://shop.example")
		rec := do(rt, req)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://shop.example" {
			t.Errorf("Allow-Origin = %q, want the allowlisted origin", got)
		}
	})

	t.Run("an unlisted origin gets no CORS headers at all", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/thing", nil)
		req.Header.Set("Origin", "https://evil.example")
		rec := do(rt, req)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("Allow-Origin = %q, want empty for an unlisted origin", got)
		}
	})
}

func TestBodyLimitIsEnforcedBeforeParsing(t *testing.T) {
	t.Parallel()

	// DB-026: the ceiling applies before anything decodes, so a huge body costs
	// us the read and nothing more.
	d := testDeps()
	d.Sessions = fakeSessions{actor: authz.Actor{ID: "c1", Type: authz.ActorCustomer}}
	rt := mustRouter(t, d)

	type body struct {
		Note string `json:"note"`
	}
	rt.Group("/api/v1").POST("/thing", policy.CustomerSession,
		func(_ context.Context, req *httpx.Request) (httpx.Response, error) {
			var b body
			if err := req.Decode(&b); err != nil {
				return httpx.Response{}, err
			}
			return httpx.OK(b), nil
		})

	huge := `{"note":"` + strings.Repeat("x", 4096) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/thing", strings.NewReader(huge))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "steleios_session", Value: "t"})
	req.AddCookie(&http.Cookie{Name: "csrf_token", Value: "tok"})
	req.Header.Set("X-CSRF-Token", "tok")

	rec := do(rt, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestRouteTableLogsWithoutPanicking(t *testing.T) {
	t.Parallel()

	// SEC-03: the startup route table is the auditable security surface. It
	// must render for every policy shape, including sparse ones.
	rt := mustRouter(t, testDeps())
	g := rt.Group("/api/v1")
	g.GET("/products", policy.PublicCached, okHandler(nil))
	g.POST("/checkout", policy.Checkout, okHandler(nil))
	g.GET("/orders/{id}", policy.CustomerOrderRead, okHandler(nil))

	rt.LogRouteTable() // must not panic

	routes := rt.Routes()
	for i := 1; i < len(routes); i++ {
		if routes[i-1].Pattern > routes[i].Pattern {
			t.Errorf("Routes() is not sorted: %q before %q", routes[i-1].Pattern, routes[i].Pattern)
		}
	}
}

// TestPreflightIsRegisteredForEveryRoute is the regression test for a bug that
// would have made the frontend unable to talk to the API at all.
//
// OPTIONS was never registered, so chi answered 405 before the CORS middleware
// ran — which meant the middleware's own OPTIONS branch was unreachable and
// every preflight failed. A browser sends a preflight for any cross-origin
// request carrying a JSON content type or a custom header, which is all of
// them. The failure is invisible server-side and looks like a CORS
// misconfiguration from the browser.
func TestPreflightIsRegisteredForEveryRoute(t *testing.T) {
	t.Parallel()

	rt := mustRouter(t, testDeps())
	g := rt.Group("/api/v1")
	g.GET("/things", policy.Public, okHandler(nil))
	g.POST("/things", policy.AuthAttempt, okHandler(nil))
	g.GET("/things/{id}", policy.Public, okHandler(nil))

	for _, path := range []string{"/api/v1/things", "/api/v1/things/abc"} {
		req := httptest.NewRequest(http.MethodOptions, path, nil)
		req.Header.Set("Origin", "https://shop.example")
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "content-type,x-csrf-token")

		rec := do(rt, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("OPTIONS %s = %d, want 204: the browser cannot make any cross-origin request to it", path, rec.Code)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://shop.example" {
			t.Errorf("OPTIONS %s allow-origin = %q, want the requesting origin", path, got)
		}
		if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
			t.Errorf("OPTIONS %s allow-credentials = %q; without it the browser sends no cookies", path, got)
		}
		if got := rec.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, headerCSRFTokenName) {
			t.Errorf("OPTIONS %s allow-headers = %q, missing the CSRF header the client must echo", path, got)
		}
	}
}

// headerCSRFTokenName is the wire name of the CSRF header, spelled out here so
// the test fails if the constant is ever renamed without the frontend knowing.
const headerCSRFTokenName = "X-CSRF-Token"

// TestPreflightFromAnUnknownOriginGetsNoCORSHeaders confirms the allowlist still
// does its job on the route that is now registered for every path.
//
// The refusal is the ABSENCE of the headers, not a status code: a browser
// treats a 204 with no Allow-Origin as a denial, and answering 403 instead
// would tell a scanner which origins are configured.
func TestPreflightFromAnUnknownOriginGetsNoCORSHeaders(t *testing.T) {
	t.Parallel()

	rt := mustRouter(t, testDeps())
	rt.Group("/api/v1").GET("/things", policy.Public, okHandler(nil))

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/things", nil)
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Access-Control-Request-Method", "GET")

	rec := do(rt, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("an unlisted origin was allowed: %q", got)
	}
}

// TestPreflightIsNotInTheRouteTable keeps the security surface readable.
//
// The startup route table is what an operator reads to answer "what protects
// this endpoint" (SEC-03). Listing an automatic OPTIONS beside every real route
// would double its length and hide the routes that matter.
func TestPreflightIsNotInTheRouteTable(t *testing.T) {
	t.Parallel()

	rt := mustRouter(t, testDeps())
	g := rt.Group("/api/v1")
	g.GET("/things", policy.Public, okHandler(nil))
	g.POST("/things", policy.AuthAttempt, okHandler(nil))

	for _, r := range rt.Routes() {
		if r.Method == http.MethodOptions {
			t.Errorf("preflight for %s appears in the route table", r.Pattern)
		}
	}
	if len(rt.Routes()) != 2 {
		t.Errorf("route table has %d entries, want the 2 real routes", len(rt.Routes()))
	}
}
