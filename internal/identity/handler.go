package identity

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"time"

	"github.com/stephenindia1/steleios-ecom/internal/platform/httpx"
	"github.com/stephenindia1/steleios-ecom/internal/platform/ids"
	"github.com/stephenindia1/steleios-ecom/internal/platform/passwd"
	"github.com/stephenindia1/steleios-ecom/internal/platform/tenant"
)

// Handler is the HTTP surface of the identity module.
//
// Handlers decode, delegate and shape a response. Every security decision is
// in the service; nothing here is a business rule (SEC-13).
type Handler struct {
	svc     *Service
	cookies httpx.CookieBuilder
	ttl     time.Duration
	log     *slog.Logger
}

// NewHandler builds the handler.
func NewHandler(svc *Service, cookies httpx.CookieBuilder, ttl time.Duration, log *slog.Logger) *Handler {
	return &Handler{svc: svc, cookies: cookies, ttl: ttl, log: log}
}

// ---------------------------------------------------------------------------
// Requests and responses
// ---------------------------------------------------------------------------

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Validate checks shape only. Whether the credentials are right is the
// service's business, and saying anything more specific here would leak.
func (r loginRequest) Validate() map[string]string {
	fields := map[string]string{}
	if r.Email == "" {
		fields["email"] = "Enter your email address."
	}
	if r.Password == "" {
		fields["password"] = "Enter your password."
	}
	return fields
}

type shopSummary struct {
	TenantID   string   `json:"tenant_id"`
	ShopCode   string   `json:"shop_code"`
	ShopName   string   `json:"shop_name"`
	ClientCode string   `json:"client_code"`
	Roles      []string `json:"roles"`
}

type loginResponse struct {
	// Deliberately minimal. A login response is the least authenticated thing
	// in the system, and every field here is one an attacker sees for free
	// after guessing a password.
	Name               string        `json:"name"`
	MustChangePassword bool          `json:"must_change_password"`
	NeedsShopSelection bool          `json:"needs_shop_selection"`
	Shops              []shopSummary `json:"shops"`
}

type selectShopRequest struct {
	TenantID string `json:"tenant_id"`
}

func (r selectShopRequest) Validate() map[string]string {
	if r.TenantID == "" {
		return map[string]string{"tenant_id": "Choose a shop."}
	}
	return nil
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (r changePasswordRequest) Validate() map[string]string {
	fields := map[string]string{}
	if r.CurrentPassword == "" {
		fields["current_password"] = "Enter your current password."
	}
	if r.NewPassword == "" {
		fields["new_password"] = "Enter a new password."
	}
	return fields
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// Login authenticates and issues a session.
func (h *Handler) Login(ctx context.Context, req *httpx.Request) (httpx.Response, error) {
	var body loginRequest
	if err := req.Decode(&body); err != nil {
		return httpx.Response{}, err
	}

	result, err := h.svc.Authenticate(ctx, body.Email, body.Password,
		clientIP(req), req.Header("User-Agent"))
	if err != nil {
		// Every way a sign-in can fail collapses into one message here. The
		// service distinguished them for its own logs and counters; the client
		// gets nothing it could use to discover which addresses have accounts
		// (BR-IDN-06).
		if IsAuthFailure(err) || errors.Is(err, ErrPasswordExpired) {
			return httpx.Response{}, httpx.Unauthenticated(err)
		}
		return httpx.Response{}, err
	}

	shops := make([]shopSummary, 0, len(result.Memberships)) // DB-024
	for _, m := range result.Memberships {
		roles := make([]string, 0, len(m.Roles))
		for _, r := range m.Roles {
			roles = append(roles, string(r))
		}
		shops = append(shops, shopSummary{
			TenantID: m.TenantID.String(), ShopCode: m.ShopCode,
			ShopName: m.ShopName, ClientCode: m.ClientCode, Roles: roles,
		})
	}

	// A person with a valid password and no membership authenticates and can do
	// nothing. That is correct — membership is what grants access — but it must
	// be said plainly rather than presented as a broken empty screen.
	if len(shops) == 0 {
		h.log.WarnContext(ctx, "signed in with no active shop membership",
			"identity_id", result.Identity.ID.String())
	}

	// Both cookies are replaced, not merely set. The CSRF token the browser
	// carried into this request was issued to an anonymous caller; keeping it
	// after authentication would let someone who planted it earlier keep using
	// it, which is session fixation with extra steps.
	sessionCookie := h.cookies.Session(result.Token, h.ttl)
	csrfCookie := h.cookies.CSRF(result.CSRFSecret, h.ttl)

	return httpx.OK(loginResponse{
		Name:               result.Identity.FullName,
		MustChangePassword: result.MustChangePassword,
		NeedsShopSelection: result.NeedsShopSelection,
		Shops:              shops,
	}).WithCookies(sessionCookie, csrfCookie), nil
}

// SelectShop binds the session to one of the person's shops.
func (h *Handler) SelectShop(ctx context.Context, req *httpx.Request) (httpx.Response, error) {
	var body selectShopRequest
	if err := req.Decode(&body); err != nil {
		return httpx.Response{}, err
	}

	id, err := tenant.Parse(body.TenantID)
	if err != nil {
		// A malformed id is indistinguishable from one belonging to someone
		// else, so both answer the same way.
		return httpx.Response{}, httpx.NotFound(err)
	}

	m, err := h.svc.SelectShop(ctx, httpx.SessionToken(req), id)
	if err != nil {
		switch {
		case errors.Is(err, ErrNotAMember):
			// Not 403: a 403 would confirm the shop exists.
			return httpx.Response{}, httpx.NotFound(err)
		case errors.Is(err, ErrMustChangePassword):
			return httpx.Response{}, httpx.Conflict("Change your password before continuing.", err)
		default:
			return httpx.Response{}, err
		}
	}

	roles := make([]string, 0, len(m.Roles))
	for _, r := range m.Roles {
		roles = append(roles, string(r))
	}
	return httpx.OK(shopSummary{
		TenantID: m.TenantID.String(), ShopCode: m.ShopCode,
		ShopName: m.ShopName, ClientCode: m.ClientCode, Roles: roles,
	}), nil
}

// ChangePassword replaces the signed-in person's password.
func (h *Handler) ChangePassword(ctx context.Context, req *httpx.Request) (httpx.Response, error) {
	var body changePasswordRequest
	if err := req.Decode(&body); err != nil {
		return httpx.Response{}, err
	}

	err := h.svc.ChangePassword(ctx, httpx.SessionToken(req), body.CurrentPassword, body.NewPassword)
	switch {
	case err == nil:
	case errors.Is(err, ErrBadPassword):
		return httpx.Response{}, httpx.Validation(map[string]string{
			"current_password": "That is not your current password.",
		})
	case errors.Is(err, ErrSamePassword):
		return httpx.Response{}, httpx.Validation(map[string]string{
			"new_password": "Choose a password different from your current one.",
		})
	case errors.Is(err, passwd.ErrPolicy):
		// PolicyReason, not err.Error(): the reason is one of a fixed set of
		// authored sentences, so no internal detail can reach the client through
		// this path (BR-SEC-09, GO-028).
		return httpx.Response{}, httpx.Validation(map[string]string{
			"new_password": passwd.PolicyReason(err),
		})
	default:
		return httpx.Response{}, err
	}

	// The change signed every session out, including this one. Clearing the
	// cookies here means the browser matches reality instead of holding a
	// token that no longer works.
	return httpx.OK(map[string]string{
		"status": "password changed; please sign in again",
	}).WithCookies(h.cookies.Clear()...), nil
}

// Logout ends this session.
func (h *Handler) Logout(ctx context.Context, req *httpx.Request) (httpx.Response, error) {
	if err := h.svc.SignOut(ctx, httpx.SessionToken(req)); err != nil {
		return httpx.Response{}, err
	}
	return httpx.NoContent().WithCookies(h.cookies.Clear()...), nil
}

// LogoutEverywhere ends every session for this identity.
func (h *Handler) LogoutEverywhere(ctx context.Context, req *httpx.Request) (httpx.Response, error) {
	count, err := h.svc.SignOutEverywhere(ctx, httpx.SessionToken(req))
	if err != nil {
		return httpx.Response{}, err
	}
	return httpx.OK(map[string]int64{
		"sessions_ended": count,
	}).WithCookies(h.cookies.Clear()...), nil
}

type meResponse struct {
	IdentityID string   `json:"identity_id"`
	ActorType  string   `json:"actor_type"`
	Roles      []string `json:"roles"`
}

// Me describes the current session.
//
// Useful to a frontend deciding what to render, and to a person checking they
// are signed in as who they think. It reports the ACTOR the server resolved,
// not what the client believes, so a mismatch is visible.
func (h *Handler) Me(_ context.Context, req *httpx.Request) (httpx.Response, error) {
	actor := req.Actor()

	roles := make([]string, 0, len(actor.Roles))
	for _, r := range actor.Roles {
		roles = append(roles, string(r))
	}
	return httpx.OK(meResponse{
		IdentityID: actor.ID,
		ActorType:  string(actor.Type),
		Roles:      roles,
	}), nil
}

type csrfResponse struct {
	Token string `json:"csrf_token"`
}

// CSRFToken issues a double-submit token to a caller who has no session yet.
//
// This route has to exist. Sign-in is a POST and carries CSRF protection — a
// login CSRF is a real attack, where the victim is silently signed into the
// ATTACKER'S account and their subsequent activity accrues there — but a
// browser opening the app for the first time holds no token to submit. So the
// sign-in page fetches one here first.
//
// Handing an unauthenticated caller a token gives nothing away. The double
// submit works because a cross-origin attacker cannot READ a cookie on our
// domain, not because the value is secret from the person holding it. The token
// issued here is replaced by the session's own on sign-in.
func (h *Handler) CSRFToken(_ context.Context, _ *httpx.Request) (httpx.Response, error) {
	secret, err := ids.Token(32)
	if err != nil {
		return httpx.Response{}, err
	}
	return httpx.OK(csrfResponse{Token: secret}).
		WithCookies(h.cookies.CSRF(secret, h.ttl)), nil
}

// clientIP returns the caller's address for the audit trail.
//
// RemoteAddr has already been rewritten by the realIP middleware where a
// trusted proxy forwarded it, so this is the client's address and not the load
// balancer's.
func clientIP(req *httpx.Request) string {
	host, _, err := net.SplitHostPort(req.Underlying().RemoteAddr)
	if err != nil {
		return req.Underlying().RemoteAddr
	}
	return host
}
