package identity_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stephenindia1/steleios-ecom/internal/identity"
	"github.com/stephenindia1/steleios-ecom/internal/platform/authz"
	"github.com/stephenindia1/steleios-ecom/internal/platform/config"
	"github.com/stephenindia1/steleios-ecom/internal/platform/httpx"
	"github.com/stephenindia1/steleios-ecom/internal/platform/policy"
	"github.com/stephenindia1/steleios-ecom/internal/platform/ratelimit"
)

// These tests drive real HTTP requests through the real middleware chain with
// the real policies from the catalogue. That is the point: the handlers are
// thin, and almost everything worth asserting here is a property of the chain
// around them — that an unauthenticated caller is refused, that CSRF is
// enforced, that the cookies carry the attributes they claim to.
//
// The service beneath uses the same fakes as service_test.go.

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

// client is a browser: it keeps cookies between requests and echoes the CSRF
// cookie in the header, exactly as the frontend does.
type client struct {
	t      *testing.T
	mux    http.Handler
	cookie map[string]string
}

type httpFixture struct {
	fixture
	router *httpx.Router
}

func newHTTPFixture(t *testing.T) httpFixture {
	t.Helper()

	f := newFixture(t)
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))

	cfg := config.Config{
		Env: config.EnvTest,
		HTTP: config.HTTP{
			MaxBodyBytes:   4096,
			RequestTimeout: 5 * time.Second,
		},
		Security: config.Security{
			HSTSMaxAge:   time.Hour,
			ReauthWindow: 15 * time.Minute,
			// Secure is off in the test config because httptest speaks plain
			// HTTP; production config validation refuses to start without it.
			CookieSecure: false,
		},
	}

	rt, err := httpx.NewRouter(httpx.Deps{
		Cfg:      cfg,
		Log:      log,
		Clock:    f.clk,
		Authz:    authz.NewRBAC(),
		Limiter:  ratelimit.AllowAll{},
		Sessions: f.svc,
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	identity.Mount(rt.Group(""),
		identity.NewHandler(f.svc, httpx.NewCookieBuilder(cfg), time.Hour,
			cfg.Security.ReauthWindow, log))

	return httpFixture{fixture: f, router: rt}
}

func (f httpFixture) client(t *testing.T) *client {
	return &client{t: t, mux: f.router.Handler(), cookie: map[string]string{}}
}

// do sends a request, carrying cookies in and storing cookies out.
func (c *client) do(method, path string, body any) *httptest.ResponseRecorder {
	c.t.Helper()

	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(buf)
	}

	req := httptest.NewRequest(method, path, reader)
	req.RemoteAddr = "203.0.113.10:44321"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for name, value := range c.cookie {
		req.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	// A browser reads the CSRF cookie and echoes it. An attacker on another
	// origin can cause the cookie to be sent but cannot read it to do this.
	if csrf, ok := c.cookie["csrf_token"]; ok {
		req.Header.Set("X-CSRF-Token", csrf)
	}

	rec := httptest.NewRecorder()
	c.mux.ServeHTTP(rec, req)

	for _, ck := range rec.Result().Cookies() { //nolint:bodyclose // httptest recorder
		if ck.MaxAge < 0 {
			delete(c.cookie, ck.Name)
			continue
		}
		c.cookie[ck.Name] = ck.Value
	}
	return rec
}

// getCSRF performs the handshake a page does before it can post anything.
func (c *client) getCSRF() {
	c.t.Helper()
	if rec := c.do(http.MethodGet, "/api/v1/auth/csrf", nil); rec.Code != http.StatusOK {
		c.t.Fatalf("GET /csrf = %d, want 200", rec.Code)
	}
}

// signIn runs the whole sign-in handshake and returns the login response.
func (c *client) signIn(email, password string) *httptest.ResponseRecorder {
	c.t.Helper()
	c.getCSRF()
	return c.do(http.MethodPost, "/api/v1/auth/login",
		map[string]string{"email": email, "password": password})
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return out
}

// errorBody is the shape httpx returns for every failure.
type errorBody struct {
	Error struct {
		Code    string            `json:"code"`
		Message string            `json:"message"`
		Fields  map[string]string `json:"fields"`
	} `json:"error"`
}

func cookieNamed(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() { //nolint:bodyclose // httptest recorder
		if c.Name == name {
			return c
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// The CSRF handshake
// ---------------------------------------------------------------------------

func TestSignInIsRefusedWithoutACSRFToken(t *testing.T) {
	t.Parallel()

	f := newHTTPFixture(t)
	f.user(t, "asha@shop.example", "correct horse battery", shopA)

	c := f.client(t) // deliberately skips the /csrf handshake
	rec := c.do(http.MethodPost, "/api/v1/auth/login",
		map[string]string{"email": "asha@shop.example", "password": "correct horse battery"})

	// Login CSRF is a real attack: the victim is silently signed into the
	// ATTACKER'S account and everything they do afterwards accrues there.
	if rec.Code != http.StatusForbidden {
		t.Fatalf("login without CSRF = %d, want 403", rec.Code)
	}
	if cookieNamed(rec, "steleios_session") != nil {
		t.Error("a refused login issued a session cookie")
	}
}

func TestCSRFTokenIsIssuedToAnAnonymousCaller(t *testing.T) {
	t.Parallel()

	f := newHTTPFixture(t)
	c := f.client(t)

	rec := c.do(http.MethodGet, "/api/v1/auth/csrf", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /csrf = %d, want 200", rec.Code)
	}

	cookie := cookieNamed(rec, "csrf_token")
	if cookie == nil {
		t.Fatal("no csrf cookie was set")
	}
	// It MUST be readable by script: the browser has to echo it in a header,
	// and that echo is the entire mechanism.
	if cookie.HttpOnly {
		t.Error("the csrf cookie is HttpOnly, so no browser can echo it")
	}

	body := decode[struct {
		Token string `json:"csrf_token"`
	}](t, rec)
	if body.Token != cookie.Value {
		t.Errorf("body token %q does not match cookie %q", body.Token, cookie.Value)
	}
}

func TestCSRFTokensDifferBetweenCallers(t *testing.T) {
	t.Parallel()

	f := newHTTPFixture(t)
	first := f.client(t)
	second := f.client(t)
	first.getCSRF()
	second.getCSRF()

	if first.cookie["csrf_token"] == second.cookie["csrf_token"] {
		t.Fatal("two callers were issued the same csrf token")
	}
}

// ---------------------------------------------------------------------------
// Sign-in
// ---------------------------------------------------------------------------

func TestSignInIssuesASession(t *testing.T) {
	t.Parallel()

	f := newHTTPFixture(t)
	f.user(t, "asha@shop.example", "correct horse battery", shopA)

	c := f.client(t)
	rec := c.signIn("asha@shop.example", "correct horse battery")
	if rec.Code != http.StatusOK {
		t.Fatalf("login = %d (%s), want 200", rec.Code, rec.Body.String())
	}

	session := cookieNamed(rec, "steleios_session")
	if session == nil {
		t.Fatal("no session cookie")
	}
	// The single most valuable attribute on this cookie: an XSS bug cannot read
	// it, so it cannot be exfiltrated (BR-IDN-02).
	if !session.HttpOnly {
		t.Error("the session cookie is not HttpOnly")
	}
	if session.SameSite != http.SameSiteLaxMode {
		t.Errorf("session cookie SameSite = %v, want Lax", session.SameSite)
	}
	if session.Path != "/" {
		t.Errorf("session cookie path = %q, want /", session.Path)
	}
}

// TestTheCSRFCookieNeverCarriesTheSessionToken guards a mistake that would
// silently undo HttpOnly.
//
// The CSRF cookie is readable by JavaScript by design. If the same value were
// also the session token, then any XSS on the page could read the session out
// of the CSRF cookie — and the HttpOnly flag on the session cookie, the whole
// reason for storing it in a cookie rather than localStorage, would protect
// nothing.
func TestTheCSRFCookieNeverCarriesTheSessionToken(t *testing.T) {
	t.Parallel()

	f := newHTTPFixture(t)
	f.user(t, "asha@shop.example", "correct horse battery", shopA)

	c := f.client(t)
	rec := c.signIn("asha@shop.example", "correct horse battery")

	session := cookieNamed(rec, "steleios_session")
	csrf := cookieNamed(rec, "csrf_token")
	if session == nil || csrf == nil {
		t.Fatalf("missing cookies: session=%v csrf=%v", session, csrf)
	}
	if csrf.HttpOnly {
		t.Error("the csrf cookie is HttpOnly and cannot be echoed")
	}
	if csrf.Value == session.Value {
		t.Fatal("the csrf cookie carries the session token, defeating HttpOnly")
	}
}

func TestSignInReplacesThePreAuthCSRFToken(t *testing.T) {
	t.Parallel()

	f := newHTTPFixture(t)
	f.user(t, "asha@shop.example", "correct horse battery", shopA)

	c := f.client(t)
	c.getCSRF()
	before := c.cookie["csrf_token"]

	c.do(http.MethodPost, "/api/v1/auth/login",
		map[string]string{"email": "asha@shop.example", "password": "correct horse battery"})

	// Keeping the anonymous token across authentication would let whoever
	// planted it keep using it — session fixation with extra steps.
	if c.cookie["csrf_token"] == before {
		t.Fatal("the pre-authentication csrf token survived sign-in")
	}
}

func TestSignInFailuresAreIndistinguishableOverHTTP(t *testing.T) {
	t.Parallel()

	f := newHTTPFixture(t)
	f.user(t, "asha@shop.example", "correct horse battery", shopA)
	blocked := f.user(t, "blocked@shop.example", "correct horse battery", shopA)
	blocked.Status = identity.StatusBlocked
	f.repo.add(blocked)

	cases := []struct {
		name            string
		email, password string
	}{
		{"unknown address", "nobody@shop.example", "correct horse battery"},
		{"wrong password", "asha@shop.example", "wrong password entirely"},
		{"blocked account", "blocked@shop.example", "correct horse battery"},
	}

	var first string
	for i, tc := range cases {
		c := f.client(t)
		rec := c.signIn(tc.email, tc.password)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", tc.name, rec.Code)
		}
		body := decode[errorBody](t, rec)

		// The request id differs per request by design, so compare the parts
		// that describe the failure.
		got := body.Error.Code + "|" + body.Error.Message
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Errorf("%s answered %q, but the first case answered %q — the login form is an account oracle",
				tc.name, got, first)
		}
	}
}

func TestSignInValidatesItsInput(t *testing.T) {
	t.Parallel()

	f := newHTTPFixture(t)

	cases := []struct {
		name  string
		body  any
		want  int
		field string
	}{
		{"no email", map[string]string{"password": "correct horse battery"}, http.StatusUnprocessableEntity, "email"},
		{"no password", map[string]string{"email": "asha@shop.example"}, http.StatusUnprocessableEntity, "password"},
		{"empty object", map[string]string{}, http.StatusUnprocessableEntity, "email"},
		{"unknown field", map[string]string{"email": "a@b.c", "password": "x", "admin": "true"}, http.StatusBadRequest, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := f.client(t)
			c.getCSRF()
			rec := c.do(http.MethodPost, "/api/v1/auth/login", tc.body)

			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d (%s)", rec.Code, tc.want, rec.Body.String())
			}
			if tc.field == "" {
				return
			}
			body := decode[errorBody](t, rec)
			if _, ok := body.Error.Fields[tc.field]; !ok {
				t.Errorf("no message for %q in %v", tc.field, body.Error.Fields)
			}
		})
	}
}

func TestSignInRejectsANonJSONBody(t *testing.T) {
	t.Parallel()

	f := newHTTPFixture(t)
	c := f.client(t)
	c.getCSRF()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
		strings.NewReader("email=asha&password=hunter2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", c.cookie["csrf_token"])
	req.AddCookie(&http.Cookie{Name: "csrf_token", Value: c.cookie["csrf_token"]})

	rec := httptest.NewRecorder()
	f.router.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// The authenticated routes
// ---------------------------------------------------------------------------

func TestAuthenticatedRoutesRefuseAnAnonymousCaller(t *testing.T) {
	t.Parallel()

	f := newHTTPFixture(t)

	routes := []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/api/v1/auth/me", nil},
		{http.MethodPost, "/api/v1/auth/shop", map[string]string{"tenant_id": shopA}},
		{http.MethodPost, "/api/v1/auth/password", map[string]string{"current_password": "a", "new_password": "b"}},
		{http.MethodPost, "/api/v1/auth/logout", nil},
		{http.MethodPost, "/api/v1/auth/logout-everywhere", nil},
	}

	for _, r := range routes {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			c := f.client(t)
			c.getCSRF() // so the refusal is about the session, not the token
			rec := c.do(r.method, r.path, r.body)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status %d, want 401 (%s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestAuthenticatedWritesRequireCSRF(t *testing.T) {
	t.Parallel()

	f := newHTTPFixture(t)
	f.user(t, "asha@shop.example", "correct horse battery", shopA)

	c := f.client(t)
	c.signIn("asha@shop.example", "correct horse battery")

	// Drop the echoed header the way a cross-origin form post would: the cookie
	// still travels, but the attacker cannot read it to build the header.
	delete(c.cookie, "csrf_token")
	rec := c.do(http.MethodPost, "/api/v1/auth/logout", nil)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", rec.Code)
	}
}

func TestMeDescribesTheResolvedActor(t *testing.T) {
	t.Parallel()

	f := newHTTPFixture(t)
	user := f.user(t, "asha@shop.example", "correct horse battery", shopA)

	c := f.client(t)
	c.signIn("asha@shop.example", "correct horse battery")

	rec := c.do(http.MethodGet, "/api/v1/auth/me", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d (%s), want 200", rec.Code, rec.Body.String())
	}

	body := decode[struct {
		IdentityID string   `json:"identity_id"`
		ActorType  string   `json:"actor_type"`
		Roles      []string `json:"roles"`
	}](t, rec)

	if body.IdentityID != user.ID.String() {
		t.Errorf("identity_id = %q, want %q", body.IdentityID, user.ID)
	}
	if body.ActorType != string(authz.ActorAdmin) {
		t.Errorf("actor_type = %q, want admin", body.ActorType)
	}
	// The single shop was selected automatically at sign-in, so the roles of
	// that membership are already on the actor.
	if len(body.Roles) != 1 || body.Roles[0] != string(authz.RoleManager) {
		t.Errorf("roles = %v, want [manager]", body.Roles)
	}
}

// ---------------------------------------------------------------------------
// Shop selection
// ---------------------------------------------------------------------------

func TestSelectingAShopYouDoNotBelongToIsNotFound(t *testing.T) {
	t.Parallel()

	f := newHTTPFixture(t)
	f.user(t, "asha@shop.example", "correct horse battery", shopA)

	c := f.client(t)
	c.signIn("asha@shop.example", "correct horse battery")

	rec := c.do(http.MethodPost, "/api/v1/auth/shop", map[string]string{"tenant_id": shopB})

	// 404 rather than 403: a 403 would confirm that shop B exists, which is a
	// tenant-enumeration oracle for anyone with any account (SEC-12).
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d (%s), want 404", rec.Code, rec.Body.String())
	}
}

func TestAMalformedShopIDAnswersLikeAShopYouDoNotHave(t *testing.T) {
	t.Parallel()

	f := newHTTPFixture(t)
	f.user(t, "asha@shop.example", "correct horse battery", shopA)

	c := f.client(t)
	c.signIn("asha@shop.example", "correct horse battery")

	rec := c.do(http.MethodPost, "/api/v1/auth/shop", map[string]string{"tenant_id": "not-a-uuid"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
}

func TestSelectingOneOfYourShops(t *testing.T) {
	t.Parallel()

	f := newHTTPFixture(t)
	f.user(t, "owner@shop.example", "correct horse battery", shopA, shopB)

	c := f.client(t)
	rec := c.signIn("owner@shop.example", "correct horse battery")

	login := decode[struct {
		NeedsShopSelection bool `json:"needs_shop_selection"`
		Shops              []struct {
			TenantID string   `json:"tenant_id"`
			Roles    []string `json:"roles"`
		} `json:"shops"`
	}](t, rec)

	if !login.NeedsShopSelection {
		t.Error("an owner of two shops was not asked to choose")
	}
	if len(login.Shops) != 2 {
		t.Fatalf("got %d shops, want 2", len(login.Shops))
	}

	rec = c.do(http.MethodPost, "/api/v1/auth/shop", map[string]string{"tenant_id": shopB})
	if rec.Code != http.StatusOK {
		t.Fatalf("select shop = %d (%s), want 200", rec.Code, rec.Body.String())
	}

	// The roles on the actor now come from the chosen shop's membership.
	me := decode[struct {
		Roles []string `json:"roles"`
	}](t, c.do(http.MethodGet, "/api/v1/auth/me", nil))
	if len(me.Roles) != 1 {
		t.Fatalf("roles after choosing = %v, want one", me.Roles)
	}
}

// ---------------------------------------------------------------------------
// Password change
// ---------------------------------------------------------------------------

func TestChangePasswordOverHTTP(t *testing.T) {
	t.Parallel()

	f := newHTTPFixture(t)
	f.user(t, "asha@shop.example", "correct horse battery", shopA)

	c := f.client(t)
	c.signIn("asha@shop.example", "correct horse battery")

	rec := c.do(http.MethodPost, "/api/v1/auth/password", map[string]string{
		"current_password": "correct horse battery",
		"new_password":     "a quite different passphrase",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d (%s), want 200", rec.Code, rec.Body.String())
	}

	// The change signed every session out, this one included. The cookies were
	// cleared, so the browser's state matches the server's.
	if _, ok := c.cookie["steleios_session"]; ok {
		t.Error("the session cookie survived a password change")
	}

	// And the old password no longer works.
	fresh := f.client(t)
	if rec := fresh.signIn("asha@shop.example", "correct horse battery"); rec.Code != http.StatusUnauthorized {
		t.Errorf("the old password still signs in: %d", rec.Code)
	}
	fresh = f.client(t)
	if rec := fresh.signIn("asha@shop.example", "a quite different passphrase"); rec.Code != http.StatusOK {
		t.Errorf("the new password does not sign in: %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestChangePasswordRejections(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		current, next string
		wantStatus    int
		wantField     string
	}{
		{"wrong current password", "not my password", "a quite different passphrase",
			http.StatusUnprocessableEntity, "current_password"},
		{"new password too short", "correct horse battery", "short",
			http.StatusUnprocessableEntity, "new_password"},
		{"new password unchanged", "correct horse battery", "correct horse battery",
			http.StatusUnprocessableEntity, "new_password"},
		{"new password too common", "correct horse battery", "password123",
			http.StatusUnprocessableEntity, "new_password"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newHTTPFixture(t)
			f.user(t, "asha@shop.example", "correct horse battery", shopA)

			c := f.client(t)
			c.signIn("asha@shop.example", "correct horse battery")

			rec := c.do(http.MethodPost, "/api/v1/auth/password", map[string]string{
				"current_password": tc.current,
				"new_password":     tc.next,
			})
			if rec.Code != tc.wantStatus {
				t.Fatalf("status %d (%s), want %d", rec.Code, rec.Body.String(), tc.wantStatus)
			}

			body := decode[errorBody](t, rec)
			msg, ok := body.Error.Fields[tc.wantField]
			if !ok {
				t.Fatalf("no message for %q in %v", tc.wantField, body.Error.Fields)
			}
			// Nothing internal may reach the client through a field message
			// (BR-SEC-09).
			if strings.Contains(msg, "passwd:") || strings.Contains(msg, "identity:") {
				t.Errorf("field message leaks an internal error: %q", msg)
			}
			// The session survived a rejected change.
			if _, live := c.cookie["steleios_session"]; !live {
				t.Error("a rejected password change signed the person out")
			}
		})
	}
}

// TestALockedAccountReachesOnlyPasswordChangeOverHTTP is the end-to-end form of
// the recovery lock (BR-REC-20).
//
// After the vendor issues a generated password, the account may do exactly one
// thing. It is enforced by the actor carrying no roles at all, which is why it
// holds for every route rather than for the ones somebody remembered.
func TestALockedAccountReachesOnlyPasswordChangeOverHTTP(t *testing.T) {
	t.Parallel()

	f := newHTTPFixture(t)
	user := lockedUser(t, f.fixture, "asha@shop.example", "issued by the vendor")

	c := f.client(t)
	rec := c.signIn("asha@shop.example", "issued by the vendor")
	if rec.Code != http.StatusOK {
		t.Fatalf("locked account could not sign in: %d (%s)", rec.Code, rec.Body.String())
	}

	login := decode[struct {
		MustChangePassword bool `json:"must_change_password"`
	}](t, rec)
	if !login.MustChangePassword {
		t.Error("the login response did not say the password must change")
	}

	// Choosing a shop is refused with a message that says what to do.
	rec = c.do(http.MethodPost, "/api/v1/auth/shop", map[string]string{"tenant_id": shopA})
	if rec.Code != http.StatusConflict {
		t.Errorf("locked account selecting a shop = %d, want 409", rec.Code)
	}

	// The actor carries no roles, so nothing gated on a permission is reachable.
	me := decode[struct {
		Roles []string `json:"roles"`
	}](t, c.do(http.MethodGet, "/api/v1/auth/me", nil))
	if len(me.Roles) != 0 {
		t.Errorf("a locked account carries roles %v", me.Roles)
	}

	// The one permitted action works.
	rec = c.do(http.MethodPost, "/api/v1/auth/password", map[string]string{
		"current_password": "issued by the vendor",
		"new_password":     "a passphrase of my own",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("locked account could not change its password: %d (%s)", rec.Code, rec.Body.String())
	}

	// And now it is an ordinary account again.
	fresh := f.client(t)
	rec = fresh.signIn("asha@shop.example", "a passphrase of my own")
	if rec.Code != http.StatusOK {
		t.Fatalf("sign-in after unlocking = %d", rec.Code)
	}
	after := decode[struct {
		MustChangePassword bool `json:"must_change_password"`
	}](t, rec)
	if after.MustChangePassword {
		t.Error("the account is still locked after changing its password")
	}
	_ = user
}

// ---------------------------------------------------------------------------
// Sign-out
// ---------------------------------------------------------------------------

func TestSignOutEndsTheSession(t *testing.T) {
	t.Parallel()

	f := newHTTPFixture(t)
	f.user(t, "asha@shop.example", "correct horse battery", shopA)

	c := f.client(t)
	c.signIn("asha@shop.example", "correct horse battery")

	rec := c.do(http.MethodPost, "/api/v1/auth/logout", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout = %d (%s), want 204", rec.Code, rec.Body.String())
	}
	if _, ok := c.cookie["steleios_session"]; ok {
		t.Error("the session cookie survived sign-out")
	}
	if _, ok := c.cookie["csrf_token"]; ok {
		t.Error("the csrf cookie survived sign-out")
	}
}

// TestAClearedSessionCookieAlsoDiesServerSide checks that sign-out revokes the
// session and does not merely ask the browser to forget it. A cookie the
// browser dropped is still a live credential to anyone who captured it.
func TestAClearedSessionCookieAlsoDiesServerSide(t *testing.T) {
	t.Parallel()

	f := newHTTPFixture(t)
	f.user(t, "asha@shop.example", "correct horse battery", shopA)

	c := f.client(t)
	c.signIn("asha@shop.example", "correct horse battery")

	stolenSession := c.cookie["steleios_session"]
	stolenCSRF := c.cookie["csrf_token"]

	c.do(http.MethodPost, "/api/v1/auth/logout", nil)

	// Replay the captured cookies from a different "browser".
	thief := f.client(t)
	thief.cookie["steleios_session"] = stolenSession
	thief.cookie["csrf_token"] = stolenCSRF

	rec := thief.do(http.MethodGet, "/api/v1/auth/me", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a revoked session still resolves: %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestSignOutEverywhereEndsEverySession(t *testing.T) {
	t.Parallel()

	f := newHTTPFixture(t)
	f.user(t, "asha@shop.example", "correct horse battery", shopA)

	till := f.client(t)
	till.signIn("asha@shop.example", "correct horse battery")
	phone := f.client(t)
	phone.signIn("asha@shop.example", "correct horse battery")

	rec := phone.do(http.MethodPost, "/api/v1/auth/logout-everywhere", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("logout-everywhere = %d (%s), want 200", rec.Code, rec.Body.String())
	}

	body := decode[struct {
		Ended int64 `json:"sessions_ended"`
	}](t, rec)
	if body.Ended != 2 {
		t.Errorf("sessions_ended = %d, want 2", body.Ended)
	}

	// The other device is out too. That is the point of the endpoint.
	if rec := till.do(http.MethodGet, "/api/v1/auth/me", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("the other session survived: %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// The route table
// ---------------------------------------------------------------------------

// TestEveryIdentityRouteCarriesTheExpectedPolicy pins the module's security
// surface. Changing a policy here has to be a deliberate edit to this table,
// which is what makes it reviewable (SEC-03, TST-02).
func TestEveryIdentityRouteCarriesTheExpectedPolicy(t *testing.T) {
	t.Parallel()

	f := newHTTPFixture(t)

	want := map[string]string{
		"GET /api/v1/auth/csrf":               policy.Public.Name,
		"POST /api/v1/auth/login":             policy.AuthAttempt.Name,
		"GET /api/v1/auth/me":                 policy.SignedIn.Name,
		"POST /api/v1/auth/shop":              policy.SignedIn.Name,
		"POST /api/v1/auth/password":          policy.SignedIn.Name,
		"POST /api/v1/auth/logout":            policy.SignedIn.Name,
		"POST /api/v1/auth/logout-everywhere": policy.SignedIn.Name,
		// SignedIn and not a Reauth policy, deliberately: this is how a person
		// EARNS re-authentication, so gating it behind one would be a loop
		// nobody could enter.
		"POST /api/v1/auth/reauth": policy.SignedIn.Name,
	}

	got := map[string]string{}
	for _, r := range f.router.Routes() {
		got[r.Method+" "+r.Pattern] = r.Policy.Name

		// Nothing in this module may require a permission: an account locked
		// into changing its password holds none, and it must still reach these
		// routes (BR-REC-20).
		if r.Policy.Permission != "" {
			t.Errorf("%s %s requires permission %q; identity routes must not",
				r.Method, r.Pattern, r.Policy.Permission)
		}
	}

	if len(got) != len(want) {
		t.Errorf("registered %d routes, expected %d: %v", len(got), len(want), got)
	}
	for route, policyName := range want {
		if got[route] != policyName {
			t.Errorf("%s has policy %q, want %q", route, got[route], policyName)
		}
	}
}

// TestOnlyTheHandshakeAndSignInArePublic states the module's public surface in
// one place, so adding a public route is a visible change.
func TestOnlyTheHandshakeAndSignInArePublic(t *testing.T) {
	t.Parallel()

	f := newHTTPFixture(t)

	public := []string{}
	for _, r := range f.router.Routes() {
		if r.Policy.IsPublic() {
			public = append(public, r.Method+" "+r.Pattern)
		}
	}

	want := []string{"GET /api/v1/auth/csrf", "POST /api/v1/auth/login"}
	if len(public) != len(want) {
		t.Fatalf("public routes = %v, want %v", public, want)
	}
	for i := range want {
		if public[i] != want[i] {
			t.Errorf("public route %d = %q, want %q", i, public[i], want[i])
		}
	}
}

// ---------------------------------------------------------------------------
// Re-authentication
// ---------------------------------------------------------------------------

func TestReauthRequiresTheRightPassword(t *testing.T) {
	t.Parallel()

	f := newHTTPFixture(t)
	f.user(t, "asha@shop.example", "correct horse battery", shopA)

	c := f.client(t)
	c.signIn("asha@shop.example", "correct horse battery")

	rec := c.do(http.MethodPost, "/api/v1/auth/reauth", map[string]string{"password": "not it"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("wrong password = %d, want 422 (%s)", rec.Code, rec.Body.String())
	}

	rec = c.do(http.MethodPost, "/api/v1/auth/reauth", map[string]string{"password": "correct horse battery"})
	if rec.Code != http.StatusOK {
		t.Fatalf("right password = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
}

// TestAWrongReauthPasswordCountsTowardsLockout stops this endpoint being an
// unlimited password oracle for anyone who picks up an unlocked console.
func TestAWrongReauthPasswordCountsTowardsLockout(t *testing.T) {
	t.Parallel()

	f := newHTTPFixture(t)
	f.user(t, "asha@shop.example", "correct horse battery", shopA)

	c := f.client(t)
	c.signIn("asha@shop.example", "correct horse battery")

	before := f.repo.failedLogins
	c.do(http.MethodPost, "/api/v1/auth/reauth", map[string]string{"password": "not it"})
	if f.repo.failedLogins == before {
		t.Fatal("a wrong re-authentication password was not counted; the endpoint is an unlimited oracle")
	}
}

// TestReauthClearsTheFailureCounter: the person just proved the password, so
// counting earlier mistypes against them would lock them out for succeeding.
func TestReauthClearsTheFailureCounter(t *testing.T) {
	t.Parallel()

	f := newHTTPFixture(t)
	f.user(t, "asha@shop.example", "correct horse battery", shopA)

	c := f.client(t)
	c.signIn("asha@shop.example", "correct horse battery")

	before := f.repo.successes
	c.do(http.MethodPost, "/api/v1/auth/reauth", map[string]string{"password": "correct horse battery"})
	if f.repo.successes == before {
		t.Error("a successful re-authentication did not clear the failure counter")
	}
}
