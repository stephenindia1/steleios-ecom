package ratelimit_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stephenindia1/steleios-ecom/internal/platform/ratelimit"
)

func TestScopeValid(t *testing.T) {
	t.Parallel()

	for _, s := range []ratelimit.Scope{ratelimit.ScopeIP, ratelimit.ScopeActor, ratelimit.ScopeSubject} {
		if err := s.Valid(); err != nil {
			t.Errorf("%s should be valid: %v", s, err)
		}
	}
	for _, s := range []ratelimit.Scope{"", "user", "IP", "global"} {
		if err := s.Valid(); err == nil {
			t.Errorf("Scope(%q) should be invalid", s)
		}
	}
}

func TestRuleValidate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		rule    ratelimit.Rule
		wantErr string
	}{
		{
			name: "a normal rule",
			rule: ratelimit.Rule{Scope: ratelimit.ScopeIP, Limit: 10, Window: time.Minute},
		},
		{
			name:    "unknown scope",
			rule:    ratelimit.Rule{Scope: "everyone", Limit: 10, Window: time.Minute},
			wantErr: "unknown scope",
		},
		{
			name:    "zero limit would refuse everything",
			rule:    ratelimit.Rule{Scope: ratelimit.ScopeIP, Limit: 0, Window: time.Minute},
			wantErr: "limit must be positive",
		},
		{
			name:    "negative limit",
			rule:    ratelimit.Rule{Scope: ratelimit.ScopeIP, Limit: -1, Window: time.Minute},
			wantErr: "limit must be positive",
		},
		{
			name:    "zero window would divide by zero when bucketing",
			rule:    ratelimit.Rule{Scope: ratelimit.ScopeIP, Limit: 10},
			wantErr: "window must be positive",
		},
		{
			name:    "negative window",
			rule:    ratelimit.Rule{Scope: ratelimit.ScopeIP, Limit: 10, Window: -time.Second},
			wantErr: "window must be positive",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.rule.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected valid, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestSpecConstructors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		spec  ratelimit.Spec
		scope ratelimit.Scope
	}{
		{name: "PerIP", spec: ratelimit.PerIP(10, time.Minute), scope: ratelimit.ScopeIP},
		{name: "PerActor", spec: ratelimit.PerActor(10, time.Minute), scope: ratelimit.ScopeActor},
		{name: "PerSubject", spec: ratelimit.PerSubject(10, time.Minute), scope: ratelimit.ScopeSubject},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if len(tc.spec.Rules) != 1 {
				t.Fatalf("expected one rule, got %d", len(tc.spec.Rules))
			}
			if tc.spec.Rules[0].Scope != tc.scope {
				t.Errorf("scope = %s, want %s", tc.spec.Rules[0].Scope, tc.scope)
			}
			if tc.spec.IsZero() {
				t.Error("a constructed spec must not report as zero")
			}
			if err := tc.spec.Validate(); err != nil {
				t.Errorf("constructed spec is invalid: %v", err)
			}
		})
	}
}

func TestComposite(t *testing.T) {
	t.Parallel()

	// BR-IDN-11: login is limited per IP and per account independently, because
	// either limit alone is trivially evaded — an IP limit by a botnet, an
	// account limit by spraying many accounts from one address.
	spec := ratelimit.Composite(
		ratelimit.PerIP(10, time.Hour),
		ratelimit.PerSubject(5, 15*time.Minute),
	)

	if len(spec.Rules) != 2 {
		t.Fatalf("composite has %d rules, want 2", len(spec.Rules))
	}

	scopes := map[ratelimit.Scope]int{}
	for _, r := range spec.Rules {
		scopes[r.Scope] = r.Limit
	}
	if scopes[ratelimit.ScopeIP] != 10 {
		t.Errorf("IP limit = %d, want 10", scopes[ratelimit.ScopeIP])
	}
	if scopes[ratelimit.ScopeSubject] != 5 {
		t.Errorf("subject limit = %d, want 5", scopes[ratelimit.ScopeSubject])
	}

	if got := ratelimit.Composite(); !got.IsZero() {
		t.Error("composing nothing should produce the zero spec")
	}
}

func TestZeroSpec(t *testing.T) {
	t.Parallel()

	// The zero spec means "no limits". Policy validation rejects it unless the
	// policy explicitly sets Unlimited, so silence cannot pass as a decision.
	var zero ratelimit.Spec
	if !zero.IsZero() {
		t.Error("the zero Spec should report as zero")
	}
	if err := zero.Validate(); err != nil {
		t.Errorf("the zero Spec is structurally valid (policy decides whether it is allowed): %v", err)
	}
}

func TestSpecValidateReportsEveryBadRule(t *testing.T) {
	t.Parallel()

	spec := ratelimit.Spec{Rules: []ratelimit.Rule{
		{Scope: ratelimit.ScopeIP, Limit: 10, Window: time.Minute},   // fine
		{Scope: "nope", Limit: 10, Window: time.Minute},              // bad scope
		{Scope: ratelimit.ScopeActor, Limit: 0, Window: time.Minute}, // bad limit
	}}

	err := spec.Validate()
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"rule 1", "rule 2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should identify %s: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "rule 0") {
		t.Errorf("the valid rule should not be reported: %v", err)
	}
}

func TestRuleString(t *testing.T) {
	t.Parallel()

	// Rendered into the startup route table (SEC-03), so it must name the
	// limit, the window and the scope.
	got := ratelimit.Rule{Scope: ratelimit.ScopeActor, Limit: 300, Window: time.Minute}.String()
	for _, want := range []string{"300", "1m0s", "actor"} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, missing %q", got, want)
		}
	}
}

func TestTestDoubles(t *testing.T) {
	t.Parallel()

	rule := ratelimit.Rule{Scope: ratelimit.ScopeIP, Limit: 5, Window: time.Minute}
	ctx := context.Background()

	res, err := ratelimit.AllowAll{}.Allow(ctx, rule, "any")
	if err != nil || !res.Allowed {
		t.Errorf("AllowAll = %+v, %v; want allowed", res, err)
	}
	if res.Limit != rule.Limit {
		t.Errorf("AllowAll should echo the rule's limit, got %d", res.Limit)
	}

	res, err = ratelimit.DenyAll{}.Allow(ctx, rule, "any")
	if err != nil {
		t.Fatalf("DenyAll returned an error: %v", err)
	}
	if res.Allowed {
		t.Error("DenyAll allowed a request")
	}
	// A refusal must carry a retry hint, or the client cannot back off
	// correctly.
	if res.RetryAfter <= 0 {
		t.Errorf("RetryAfter = %s, want a positive duration", res.RetryAfter)
	}
}
