package httpx

import (
	"net/http"
	"time"

	"github.com/stephenindia1/steleios-ecom/internal/platform/config"
)

// Cookie construction lives here and nowhere else.
//
// The security attributes of a session cookie are the difference between a
// session an attacker can steal with a line of JavaScript and one they cannot.
// Deciding them at each call site means one of those call sites eventually gets
// it wrong, so there is exactly one place that builds them (DRY §6.1).

// CookieBuilder makes the application's cookies with consistent attributes.
type CookieBuilder struct {
	domain string
	secure bool
}

// NewCookieBuilder returns a builder for this deployment.
//
// Secure comes from configuration and is required in production, where
// config.Validate refuses to start without it (BR-SEC-01).
func NewCookieBuilder(cfg config.Config) CookieBuilder {
	return CookieBuilder{
		domain: cfg.Security.CookieDomain,
		secure: cfg.Security.CookieSecure,
	}
}

// Session returns the session cookie.
//
//   - HttpOnly: JavaScript cannot read it, so an XSS bug cannot exfiltrate the
//     session. This is the single most valuable attribute here and it is why a
//     token in localStorage was rejected (BR-IDN-02).
//   - Secure: never sent over plain HTTP.
//   - SameSite=Lax: not sent on cross-site POSTs, which blunts CSRF. It is not
//     sufficient on its own, which is why the double-submit token exists too.
//   - Path=/: the whole application, since every authenticated route needs it.
func (b CookieBuilder) Session(token string, ttl time.Duration) *http.Cookie {
	return &http.Cookie{
		Name:     cookieSession,
		Value:    token,
		Path:     "/",
		Domain:   b.domain,
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   b.secure,
		SameSite: http.SameSiteLaxMode,
	}
}

// CSRF returns the double-submit cookie.
//
// Deliberately NOT HttpOnly: the browser must read it to echo the value in a
// header, and that echo is the whole mechanism. It is safe because the cookie
// alone proves nothing — an attacker on another origin can cause the cookie to
// be SENT but cannot READ it to construct the matching header.
func (b CookieBuilder) CSRF(secret string, ttl time.Duration) *http.Cookie {
	return &http.Cookie{
		Name:     cookieCSRF,
		Value:    secret,
		Path:     "/",
		Domain:   b.domain,
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: false,
		Secure:   b.secure,
		SameSite: http.SameSiteLaxMode,
	}
}

// Clear returns cookies that expire the session immediately.
//
// Both are returned together: leaving a stale CSRF cookie behind after
// sign-out is untidy, and a CSRF token with no session is confusing to debug.
func (b CookieBuilder) Clear() []*http.Cookie {
	expire := func(name string, httpOnly bool) *http.Cookie {
		return &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			Domain:   b.domain,
			MaxAge:   -1,
			Expires:  time.Unix(0, 0),
			HttpOnly: httpOnly,
			Secure:   b.secure,
			SameSite: http.SameSiteLaxMode,
		}
	}
	return []*http.Cookie{expire(cookieSession, true), expire(cookieCSRF, false)}
}

// SessionToken returns the session token from a request, or "".
func SessionToken(req *Request) string {
	c, err := req.r.Cookie(cookieSession)
	if err != nil {
		return ""
	}
	return c.Value
}
