package policy_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stephenindia1/steleios-ecom/internal/platform/authz"
	"github.com/stephenindia1/steleios-ecom/internal/platform/policy"
	"github.com/stephenindia1/steleios-ecom/internal/platform/ratelimit"
)

// The guarantee under test: a policy that is wrong cannot reach production,
// because Validate runs for every route at startup (SEC-01).

func TestZeroPolicyIsInvalid(t *testing.T) {
	t.Parallel()

	// This is the load-bearing case. A struct literal that forgets to set Auth
	// must fail, or a forgotten field ships an unauthenticated endpoint.
	var zero policy.Policy
	err := zero.Validate()
	if err == nil {
		t.Fatal("the zero Policy validated; a forgotten field would ship unprotected")
	}
	if !strings.Contains(err.Error(), "auth mode is unset") {
		t.Errorf("error should name the unset auth mode: %v", err)
	}
	if !strings.Contains(err.Error(), "no name") {
		t.Errorf("error should name the missing policy name: %v", err)
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	// A minimal valid policy, mutated one field at a time.
	base := func() policy.Policy {
		return policy.Policy{
			Name:      "test",
			Auth:      policy.AuthSession,
			RateLimit: ratelimit.PerActor(10, time.Minute),
		}
	}

	cases := []struct {
		name    string
		mutate  func(*policy.Policy)
		wantErr string
	}{
		{name: "a minimal policy is valid", mutate: func(*policy.Policy) {}},
		{
			name:   "explicit unlimited is valid",
			mutate: func(p *policy.Policy) { p.RateLimit = ratelimit.Spec{}; p.Unlimited = true },
		},
		{
			name: "a complete admin policy is valid",
			mutate: func(p *policy.Policy) {
				p.Auth = policy.AuthAdmin
				p.Permission = authz.ActionRefundWrite
				p.CSRF, p.Reauth, p.DualApproval, p.Idempotent = true, true, true, true
			},
		},

		// Rate limiting must be a decision, never an omission.
		{
			name:    "no rate limit and no explicit waiver",
			mutate:  func(p *policy.Policy) { p.RateLimit = ratelimit.Spec{} },
			wantErr: "no rate limit declared",
		},
		{
			name:    "unlimited alongside rules is contradictory",
			mutate:  func(p *policy.Policy) { p.Unlimited = true },
			wantErr: "Unlimited set alongside",
		},
		{
			name: "a rule with a zero limit",
			mutate: func(p *policy.Policy) {
				p.RateLimit = ratelimit.Spec{Rules: []ratelimit.Rule{{Scope: ratelimit.ScopeIP, Limit: 0, Window: time.Minute}}}
			},
			wantErr: "limit must be positive",
		},
		{
			name: "a rule with a zero window",
			mutate: func(p *policy.Policy) {
				p.RateLimit = ratelimit.Spec{Rules: []ratelimit.Rule{{Scope: ratelimit.ScopeIP, Limit: 1}}}
			},
			wantErr: "window must be positive",
		},
		{
			name: "a rule with an unknown scope",
			mutate: func(p *policy.Policy) {
				p.RateLimit = ratelimit.Spec{Rules: []ratelimit.Rule{{Scope: "everyone", Limit: 1, Window: time.Minute}}}
			},
			wantErr: "unknown scope",
		},

		// The webhook rules. Each of these prevents a specific way of breaking
		// signature verification (BR-PAY-04/05/06).
		{
			name:    "signature auth without a buffered raw body",
			mutate:  func(p *policy.Policy) { p.Auth = policy.AuthSignature },
			wantErr: "requires RawBody",
		},
		{
			name:    "signature auth demanding CSRF",
			mutate:  func(p *policy.Policy) { p.Auth = policy.AuthSignature; p.RawBody = true; p.CSRF = true },
			wantErr: "must not require CSRF",
		},
		{
			name: "signature auth carrying a permission",
			mutate: func(p *policy.Policy) {
				p.Auth, p.RawBody, p.Permission = policy.AuthSignature, true, authz.ActionOrderWrite
			},
			wantErr: "carry no ownership or permissions",
		},
		{
			name:    "raw body buffering outside signature auth",
			mutate:  func(p *policy.Policy) { p.RawBody = true },
			wantErr: "only valid with AuthSignature",
		},

		// Ownership.
		{
			name:    "ownership without a resource type",
			mutate:  func(p *policy.Policy) { p.Ownership = policy.OwnerSource{PathParam: "id"} },
			wantErr: "no resource type",
		},
		{
			name:    "ownership without a path parameter",
			mutate:  func(p *policy.Policy) { p.Ownership = policy.OwnerSource{ResourceType: "order"} },
			wantErr: "no path parameter",
		},
		{
			name: "ownership on an unauthenticated route",
			mutate: func(p *policy.Policy) {
				p.Auth = policy.AuthNone
				p.Ownership = policy.OwnedBy("order", "id")
			},
			wantErr: "requires an authenticated actor",
		},

		// Staff-only controls.
		{
			name:    "reauth on a customer policy",
			mutate:  func(p *policy.Policy) { p.Reauth = true },
			wantErr: "admin policies only",
		},
		{
			name:    "dual approval on a customer policy",
			mutate:  func(p *policy.Policy) { p.DualApproval = true },
			wantErr: "admin policies only",
		},

		{
			name:    "negative timeout",
			mutate:  func(p *policy.Policy) { p.Timeout = -time.Second },
			wantErr: "negative timeout",
		},
		{
			name:    "missing name",
			mutate:  func(p *policy.Policy) { p.Name = "" },
			wantErr: "no name",
		},
		{
			name:    "auth mode above the defined range",
			mutate:  func(p *policy.Policy) { p.Auth = policy.AuthMode(99) },
			wantErr: "auth mode is unset",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := base()
			tc.mutate(&p)
			err := p.Validate()

			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected valid, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error mentioning %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestCatalogueIsEntirelyValid(t *testing.T) {
	t.Parallel()

	// TST-02 at the policy layer: every policy the application can attach to a
	// route is well formed. The router runs this at startup; running it here
	// means a bad policy fails the build rather than the boot.
	all := policy.All()
	if len(all) < 10 {
		t.Fatalf("the catalogue has only %d policies; that looks like a truncated file", len(all))
	}

	seen := make(map[string]struct{}, len(all))
	for _, p := range all {
		t.Run(p.Name, func(t *testing.T) {
			if err := p.Validate(); err != nil {
				t.Fatalf("catalogue policy %q is invalid: %v", p.Name, err)
			}
		})
		if _, dup := seen[p.Name]; dup {
			t.Errorf("duplicate policy name %q: logs and metrics could not tell them apart", p.Name)
		}
		seen[p.Name] = struct{}{}
	}
}

func TestPublicSurfaceIsSmallAndDeliberate(t *testing.T) {
	t.Parallel()

	// Every unauthenticated policy is a review item (SEC-04). This test does
	// not forbid them; it pins the list, so adding one is a visible diff rather
	// than a quiet change.
	expected := map[string]bool{
		"public":        true,
		"public.cached": true,
		"probe":         true,
		"auth.attempt":  true,
		"auth.otp.send": true,
		// A CORS preflight carries no credentials by definition — the browser
		// sends OPTIONS with no cookies and none of the headers it is asking
		// about — so there is nothing for it to authenticate. It reveals only
		// which origins are allowlisted, to a caller who could discover that by
		// trying.
		"cors.preflight": true,
	}

	for _, p := range policy.All() {
		if !p.IsPublic() {
			continue
		}
		if !expected[p.Name] {
			t.Errorf("policy %q is unauthenticated but is not in the reviewed public list; "+
				"if that is intended, add it here and to the security review", p.Name)
		}
		delete(expected, p.Name)
	}
	for name := range expected {
		t.Errorf("expected public policy %q is missing from the catalogue", name)
	}
}

func TestStateChangingPoliciesRequireCSRF(t *testing.T) {
	t.Parallel()

	// Any policy that can be attached to a write must carry CSRF, except the
	// signature-authenticated webhook, whose exemption is deliberate and
	// validated separately (BR-PAY-06).
	readOnly := map[string]bool{
		"public": true, "public.cached": true, "probe": true,
		"customer.order.read": true, "admin.read": true, "platform.read": true,
		// A preflight changes nothing: it asks whether a request WOULD be
		// allowed and the answer is a set of headers. Requiring CSRF on it would
		// make every cross-origin request impossible, since the browser sends no
		// custom header on a preflight — that is the whole point of sending one.
		"cors.preflight": true,
	}

	for _, p := range policy.All() {
		if readOnly[p.Name] || p.Auth == policy.AuthSignature {
			continue
		}
		if !p.CSRF {
			t.Errorf("policy %q can be attached to a state-changing route but does not require CSRF", p.Name)
		}
	}
}

func TestMoneyPoliciesAreProtected(t *testing.T) {
	t.Parallel()

	// The policies where money moves carry the extra controls the business
	// rules demand: refunds need re-auth and a second approver (BR-ADM-04,
	// BR-ADM-07); checkout must be idempotent (BR-CHK-02).
	byName := map[string]policy.Policy{}
	for _, p := range policy.All() {
		byName[p.Name] = p
	}

	finance := byName["admin.finance"]
	if !finance.Reauth {
		t.Error("admin.finance must require re-authentication (BR-ADM-07)")
	}
	if !finance.DualApproval {
		t.Error("admin.finance must require a second approver (BR-ADM-04)")
	}
	if !finance.Idempotent {
		t.Error("admin.finance must be idempotent: a retried refund must not pay twice")
	}

	checkout := byName["checkout"]
	if !checkout.Idempotent {
		t.Error("checkout must be idempotent (BR-CHK-02)")
	}
	if checkout.RateLimit.IsZero() {
		t.Error("checkout must be rate limited to prevent reservation squatting (BR-CHK-05)")
	}

	tax := byName["admin.tax"]
	if !tax.Reauth || !tax.DualApproval {
		t.Error("admin.tax must require re-auth and a second approver (BR-TAX-09)")
	}
}

func TestAuthAttemptsAreLimitedOnBothScopes(t *testing.T) {
	t.Parallel()

	// BR-IDN-05, BR-IDN-11: an IP limit alone is evaded with a botnet, and a
	// per-account limit alone lets one address spray many accounts. Both.
	for _, name := range []string{"auth.attempt", "auth.otp.send"} {
		var p policy.Policy
		for _, c := range policy.All() {
			if c.Name == name {
				p = c
			}
		}

		scopes := map[ratelimit.Scope]bool{}
		for _, r := range p.RateLimit.Rules {
			scopes[r.Scope] = true
		}
		if !scopes[ratelimit.ScopeIP] {
			t.Errorf("%s must be limited per IP", name)
		}
		if !scopes[ratelimit.ScopeSubject] {
			t.Errorf("%s must be limited per subject (account or phone number)", name)
		}
	}
}

func TestWebhookPolicyShape(t *testing.T) {
	t.Parallel()

	var p policy.Policy
	for _, c := range policy.All() {
		if c.Name == "webhook.provider" {
			p = c
		}
	}

	if p.Auth != policy.AuthSignature {
		t.Error("the webhook must be authenticated by signature (BR-PAY-04)")
	}
	if !p.RawBody {
		t.Error("the webhook must buffer the raw body for HMAC verification (BR-PAY-05)")
	}
	if p.CSRF {
		t.Error("the webhook must not require CSRF: a provider has no session (BR-PAY-06)")
	}
	if p.Timeout <= 0 || p.Timeout > 5*time.Second {
		t.Errorf("webhook timeout = %s, want a ceiling of 5s (BR-PAY-08)", p.Timeout)
	}
}

func TestDescribe(t *testing.T) {
	t.Parallel()

	// Describe produces the startup route table (SEC-03), so it must render
	// every field a reviewer needs and never panic on a sparse policy.
	p := policy.Policy{
		Name:       "x",
		Auth:       policy.AuthAdmin,
		Permission: authz.ActionRefundWrite,
		Ownership:  policy.OwnedBy("order", "id"),
		CSRF:       true,
		Idempotent: true,
		Reauth:     true,
		RateLimit:  ratelimit.Composite(ratelimit.PerIP(1, time.Minute), ratelimit.PerActor(2, time.Hour)),
	}

	got := p.Describe()
	for _, want := range []string{"auth=admin", "refund:write", "order{id}", "csrf", "idempotent", "reauth", "per ip", "per actor"} {
		if !strings.Contains(got, want) {
			t.Errorf("Describe() = %q, missing %q", got, want)
		}
	}

	sparse := policy.Policy{Name: "y", Auth: policy.AuthNone, Unlimited: true}
	got = sparse.Describe()
	for _, want := range []string{"auth=none", "perm=-", "owns=-", "unlimited", "flags=-"} {
		if !strings.Contains(got, want) {
			t.Errorf("sparse Describe() = %q, missing %q", got, want)
		}
	}
}

func TestAuthModeString(t *testing.T) {
	t.Parallel()

	cases := map[policy.AuthMode]string{
		policy.AuthNone:           "none",
		policy.AuthSession:        "session",
		policy.AuthSessionOrGuest: "session-or-guest",
		policy.AuthAdmin:          "admin",
		policy.AuthSignature:      "signature",
		policy.AuthMode(0):        "unset",
		policy.AuthMode(200):      "unset",
	}
	for mode, want := range cases {
		if got := mode.String(); got != want {
			t.Errorf("AuthMode(%d).String() = %q, want %q", mode, got, want)
		}
	}
}
