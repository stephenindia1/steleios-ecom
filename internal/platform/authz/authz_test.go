package authz_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stephenindia1/steleios-ecom/internal/platform/authz"
)

// These tests guard CLAUDE.md rule 1 and BR-ADM-01/02, BR-ORD-05. The most
// important case in the file is TestCustomerCannotReachAnotherCustomersOrder:
// that is the single most common commerce vulnerability.

func customer(id string) authz.Actor {
	return authz.Actor{ID: id, Type: authz.ActorCustomer}
}

func staff(id string, roles ...authz.Role) authz.Actor {
	return authz.Actor{ID: id, Type: authz.ActorAdmin, Roles: roles}
}

func order(id, owner string) authz.Resource {
	return authz.Resource{Type: "order", ID: id, OwnerID: owner}
}

func TestCustomerCannotReachAnotherCustomersOrder(t *testing.T) {
	t.Parallel()

	e := authz.NewRBAC()
	ctx := context.Background()

	// The passing case: my own order.
	if err := e.Can(ctx, customer("cust-1"), authz.ActionOrderRead, order("ord-1", "cust-1")); err != nil {
		t.Fatalf("a customer must reach their own order: %v", err)
	}

	// The failing case that matters: someone else's order.
	err := e.Can(ctx, customer("cust-1"), authz.ActionOrderRead, order("ord-2", "cust-2"))
	if !errors.Is(err, authz.ErrDenied) {
		t.Fatalf("BR-ORD-05: reading another customer's order = %v, want ErrDenied", err)
	}
}

func TestOwnershipEdgeCases(t *testing.T) {
	t.Parallel()

	e := authz.NewRBAC()
	ctx := context.Background()

	cases := []struct {
		name    string
		actor   authz.Actor
		res     authz.Resource
		wantErr error
	}{
		{
			name:  "owner matches",
			actor: customer("c1"),
			res:   order("o1", "c1"),
		},
		{
			name:  "guest owns their cart",
			actor: authz.Actor{ID: "guest-session-1", Type: authz.ActorGuest},
			res:   authz.Resource{Type: "cart", ID: "cart-1", OwnerID: "guest-session-1"},
		},
		{
			name:    "unknown owner is denied, never treated as public",
			actor:   customer("c1"),
			res:     authz.Resource{Type: "order", ID: "o1", OwnerID: ""},
			wantErr: authz.ErrDenied,
		},
		{
			name:    "anonymous actor cannot own anything",
			actor:   authz.Actor{Type: authz.ActorCustomer},
			res:     order("o1", "c1"),
			wantErr: authz.ErrUnauthenticated,
		},
		{
			name:    "resource type not in the owned allowlist is denied by default",
			actor:   customer("c1"),
			res:     authz.Resource{Type: "supplier", ID: "s1", OwnerID: "c1"},
			wantErr: authz.ErrDenied,
		},
		{
			name:    "owner id is compared exactly, not by prefix",
			actor:   customer("c1"),
			res:     order("o1", "c10"),
			wantErr: authz.ErrDenied,
		},
		{
			name:    "empty actor id with an empty owner id must not match",
			actor:   authz.Actor{ID: "", Type: authz.ActorCustomer},
			res:     authz.Resource{Type: "order", ID: "o1", OwnerID: ""},
			wantErr: authz.ErrUnauthenticated,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := e.Can(ctx, tc.actor, authz.ActionOrderRead, tc.res)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("expected allow, got %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("got %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestCustomerRolesAreIgnored(t *testing.T) {
	t.Parallel()

	// A customer record with a staff role attached — through a bug or an
	// attack — must not gain staff access. Customers are authorised by
	// ownership only.
	sneaky := authz.Actor{ID: "c1", Type: authz.ActorCustomer, Roles: []authz.Role{authz.RoleAdmin}}

	err := authz.NewRBAC().Can(context.Background(), sneaky,
		authz.ActionRefundWrite, authz.Resource{Type: "order", ID: "o1", OwnerID: "someone-else"})
	if !errors.Is(err, authz.ErrDenied) {
		t.Fatalf("a customer holding a staff role must not gain staff access: %v", err)
	}
}

func TestStaffPermissions(t *testing.T) {
	t.Parallel()

	e := authz.NewRBAC()
	ctx := context.Background()
	anyResource := authz.Resource{Type: "*"}

	cases := []struct {
		name    string
		actor   authz.Actor
		action  authz.Action
		allowed bool
	}{
		// Granted.
		{name: "viewer reads orders", actor: staff("s", authz.RoleViewer), action: authz.ActionOrderRead, allowed: true},
		{name: "support writes orders", actor: staff("s", authz.RoleSupport), action: authz.ActionOrderWrite, allowed: true},
		{name: "ops adjusts inventory", actor: staff("s", authz.RoleOps), action: authz.ActionInventoryWrite, allowed: true},
		{name: "finance refunds", actor: staff("s", authz.RoleFinance), action: authz.ActionRefundWrite, allowed: true},
		{name: "catalog writes catalog", actor: staff("s", authz.RoleCatalog), action: authz.ActionCatalogWrite, allowed: true},
		{name: "marketing exports", actor: staff("s", authz.RoleMarketing), action: authz.ActionMarketingExport, allowed: true},
		{name: "purchasing writes purchasing", actor: staff("s", authz.RolePurchasing), action: authz.ActionPurchasingWrite, allowed: true},
		{name: "admin manages users", actor: staff("s", authz.RoleAdmin), action: authz.ActionUserManage, allowed: true},
		{name: "several roles combine", actor: staff("s", authz.RoleViewer, authz.RoleFinance), action: authz.ActionRefundWrite, allowed: true},

		// Refused. Each of these is a privilege boundary someone will
		// eventually try to cross.
		{name: "viewer cannot write orders", actor: staff("s", authz.RoleViewer), action: authz.ActionOrderWrite},
		{name: "support cannot refund", actor: staff("s", authz.RoleSupport), action: authz.ActionRefundWrite},
		{name: "support cannot adjust stock", actor: staff("s", authz.RoleSupport), action: authz.ActionInventoryWrite},
		{name: "ops cannot refund", actor: staff("s", authz.RoleOps), action: authz.ActionRefundWrite},
		{name: "ops cannot change prices", actor: staff("s", authz.RoleOps), action: authz.ActionPricingWrite},
		{name: "finance cannot edit the catalog", actor: staff("s", authz.RoleFinance), action: authz.ActionCatalogWrite},
		{name: "catalog cannot refund", actor: staff("s", authz.RoleCatalog), action: authz.ActionRefundWrite},
		{name: "catalog cannot change tax rates", actor: staff("s", authz.RoleCatalog), action: authz.ActionTaxWrite},
		{name: "marketing cannot write orders", actor: staff("s", authz.RoleMarketing), action: authz.ActionOrderWrite},
		{name: "purchasing cannot refund", actor: staff("s", authz.RolePurchasing), action: authz.ActionRefundWrite},
		{name: "only admin manages users", actor: staff("s", authz.RoleOps, authz.RoleFinance), action: authz.ActionUserManage},
		{name: "only admin writes tax rates", actor: staff("s", authz.RoleFinance), action: authz.ActionTaxWrite},
		{name: "no roles grants nothing", actor: staff("s"), action: authz.ActionOrderRead},
		{name: "an unknown role grants nothing", actor: staff("s", authz.Role("superuser")), action: authz.ActionOrderRead},

		// Owner, manager and data entry executive.
		{name: "owner manages users", actor: staff("s", authz.RoleOwner), action: authz.ActionUserManage, allowed: true},
		{name: "owner writes tax rates", actor: staff("s", authz.RoleOwner), action: authz.ActionTaxWrite, allowed: true},
		{name: "owner refunds", actor: staff("s", authz.RoleOwner), action: authz.ActionRefundWrite, allowed: true},

		{name: "manager writes orders", actor: staff("s", authz.RoleManager), action: authz.ActionOrderWrite, allowed: true},
		{name: "manager adjusts stock", actor: staff("s", authz.RoleManager), action: authz.ActionInventoryWrite, allowed: true},
		{name: "manager changes prices", actor: staff("s", authz.RoleManager), action: authz.ActionPricingWrite, allowed: true},
		{name: "manager refunds", actor: staff("s", authz.RoleManager), action: authz.ActionRefundWrite, allowed: true},
		{name: "manager cannot manage users", actor: staff("s", authz.RoleManager), action: authz.ActionUserManage},
		{name: "manager cannot change tax rates", actor: staff("s", authz.RoleManager), action: authz.ActionTaxWrite},
		{name: "manager cannot export contact lists", actor: staff("s", authz.RoleManager), action: authz.ActionMarketingExport},

		{name: "data entry writes the catalog", actor: staff("s", authz.RoleDataEntry), action: authz.ActionCatalogWrite, allowed: true},
		{name: "data entry reads stock", actor: staff("s", authz.RoleDataEntry), action: authz.ActionInventoryRead, allowed: true},
		{name: "data entry cannot change prices", actor: staff("s", authz.RoleDataEntry), action: authz.ActionPricingWrite},
		{name: "data entry cannot read customer data", actor: staff("s", authz.RoleDataEntry), action: authz.ActionCustomerRead},
		{name: "data entry cannot read orders", actor: staff("s", authz.RoleDataEntry), action: authz.ActionOrderRead},
		{name: "data entry cannot adjust stock", actor: staff("s", authz.RoleDataEntry), action: authz.ActionInventoryWrite},
		{name: "data entry cannot refund", actor: staff("s", authz.RoleDataEntry), action: authz.ActionRefundWrite},
		{name: "data entry cannot manage users", actor: staff("s", authz.RoleDataEntry), action: authz.ActionUserManage},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := e.Can(ctx, tc.actor, tc.action, anyResource)
			if tc.allowed {
				if err != nil {
					t.Fatalf("expected allow, got %v", err)
				}
				return
			}
			if !errors.Is(err, authz.ErrDenied) {
				t.Fatalf("expected ErrDenied, got %v", err)
			}
		})
	}
}

func TestActorTypes(t *testing.T) {
	t.Parallel()

	e := authz.NewRBAC()
	ctx := context.Background()

	t.Run("system actor is granted by role, not by being internal", func(t *testing.T) {
		t.Parallel()

		// WRK-04: a worker runs as a named actor with an explicit permission
		// set. Being "internal" grants nothing.
		withRole := authz.Actor{ID: "worker:sweeper", Type: authz.ActorSystem, Roles: []authz.Role{authz.RoleOps}}
		if err := e.Can(ctx, withRole, authz.ActionInventoryWrite, authz.Resource{Type: "*"}); err != nil {
			t.Errorf("a system actor holding ops should adjust stock: %v", err)
		}

		bare := authz.Actor{ID: "worker:sweeper", Type: authz.ActorSystem}
		if err := e.Can(ctx, bare, authz.ActionInventoryWrite, authz.Resource{Type: "*"}); !errors.Is(err, authz.ErrDenied) {
			t.Errorf("a system actor with no roles must be denied, got %v", err)
		}
	})

	t.Run("provider actor has no granted actions", func(t *testing.T) {
		t.Parallel()

		// A webhook is authenticated by signature and may do exactly what its
		// route allows. It never carries permissions or ownership.
		p := authz.Actor{ID: "razorpay", Type: authz.ActorProvider}
		for _, action := range []authz.Action{authz.ActionOrderWrite, authz.ActionOrderRead, authz.ActionRefundWrite} {
			if err := e.Can(ctx, p, action, authz.Resource{Type: "order", ID: "o1", OwnerID: "razorpay"}); !errors.Is(err, authz.ErrDenied) {
				t.Errorf("provider granted %s: %v", action, err)
			}
		}
	})

	t.Run("missing actor type is unauthenticated", func(t *testing.T) {
		t.Parallel()

		err := e.Can(ctx, authz.Actor{ID: "x"}, authz.ActionOrderRead, order("o", "x"))
		if !errors.Is(err, authz.ErrUnauthenticated) {
			t.Errorf("got %v, want ErrUnauthenticated", err)
		}
	})

	t.Run("unknown actor type is denied", func(t *testing.T) {
		t.Parallel()

		err := e.Can(ctx, authz.Actor{ID: "x", Type: authz.ActorType("robot")}, authz.ActionOrderRead, order("o", "x"))
		if !errors.Is(err, authz.ErrDenied) {
			t.Errorf("got %v, want ErrDenied", err)
		}
	})
}

func TestCancelledContextFailsClosed(t *testing.T) {
	t.Parallel()

	// BR-SEC-11: if the decision cannot be reached, refuse. A cancelled request
	// must not be authorised on its way out.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := authz.NewRBAC().Can(ctx, customer("c1"), authz.ActionOrderRead, order("o1", "c1"))
	if !errors.Is(err, authz.ErrUnavailable) {
		t.Fatalf("got %v, want ErrUnavailable", err)
	}
	// It must not accidentally read as a plain denial either — the caller
	// distinguishes "not allowed" from "could not tell".
	if errors.Is(err, authz.ErrDenied) {
		t.Error("an unavailable decision must not present as a denial")
	}
}

func TestIsAuthenticated(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		actor authz.Actor
		want  bool
	}{
		{name: "customer with id", actor: customer("c1"), want: true},
		{name: "admin with id", actor: staff("s1", authz.RoleViewer), want: true},
		{name: "system actor", actor: authz.Actor{ID: "w", Type: authz.ActorSystem}, want: true},
		{name: "guest is a principal but not authenticated", actor: authz.Actor{ID: "sess", Type: authz.ActorGuest}},
		{name: "no id", actor: authz.Actor{Type: authz.ActorCustomer}},
		{name: "zero value", actor: authz.Actor{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.actor.IsAuthenticated(); got != tc.want {
				t.Errorf("IsAuthenticated() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGrantsAreNotMutableByCallers(t *testing.T) {
	t.Parallel()

	// ActionsFor returns a copy. If it returned the live slice, a caller could
	// grant themselves a permission by appending to it.
	before := authz.ActionsFor(authz.RoleViewer)
	if len(before) == 0 {
		t.Fatal("viewer should hold some actions")
	}

	mutated := authz.ActionsFor(authz.RoleViewer)
	mutated[0] = authz.ActionRefundWrite

	after := authz.ActionsFor(authz.RoleViewer)
	if after[0] == authz.ActionRefundWrite {
		t.Fatal("ActionsFor leaked the live grant table; a caller can escalate")
	}

	// And the enforcer still refuses the action that was written into the copy.
	err := authz.NewRBAC().Can(context.Background(), staff("s", authz.RoleViewer),
		authz.ActionRefundWrite, authz.Resource{Type: "*"})
	if !errors.Is(err, authz.ErrDenied) {
		t.Fatalf("viewer gained refund access: %v", err)
	}
}

func TestRolesAreEnumerable(t *testing.T) {
	t.Parallel()

	roles := authz.Roles()
	if len(roles) == 0 {
		t.Fatal("no roles defined")
	}

	// Every enumerated role must actually grant something; a role that grants
	// nothing is a UI option that silently does not work.
	for _, r := range roles {
		if len(authz.ActionsFor(r)) == 0 {
			t.Errorf("role %q grants no actions", r)
		}
	}

	// Sorted, so the admin UI and the startup table are stable.
	for i := 1; i < len(roles); i++ {
		if roles[i-1] > roles[i] {
			t.Errorf("Roles() is not sorted: %q before %q", roles[i-1], roles[i])
		}
	}
}

func TestEveryDefinedActionIsGrantedToSomeone(t *testing.T) {
	t.Parallel()

	// An action nobody holds is unreachable. That is the correct default while
	// a feature is being built, but it should be a deliberate, visible state —
	// so this test lists them rather than failing.
	all := []authz.Action{
		authz.ActionOrderRead, authz.ActionOrderWrite,
		authz.ActionCatalogRead, authz.ActionCatalogWrite,
		authz.ActionInventoryRead, authz.ActionInventoryWrite,
		authz.ActionRefundWrite, authz.ActionPricingWrite, authz.ActionTaxWrite,
		authz.ActionCustomerRead, authz.ActionPurchasingRead, authz.ActionPurchasingWrite,
		authz.ActionMarketingWrite, authz.ActionMarketingExport,
		authz.ActionLoyaltyWrite, authz.ActionReportRead, authz.ActionUserManage,
	}

	granted := map[authz.Action]bool{}
	for _, r := range authz.Roles() {
		for _, a := range authz.ActionsFor(r) {
			granted[a] = true
		}
	}

	for _, a := range all {
		if !granted[a] {
			t.Errorf("action %q is granted to no role and is therefore unreachable", a)
		}
	}
}
