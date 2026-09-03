package authz_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/stephenindia1/steleios-ecom/internal/platform/authz"
)

// Privilege-boundary tests. These assert the shape of the role hierarchy rather
// than individual grants, so a future edit to the grant table that quietly
// widens a role fails here (BR-ADM-01, BR-ADM-03).

// full is the set of actions owner and admin hold.
func full() []authz.Action { return authz.ActionsFor(authz.RoleOwner) }

func TestOwnerAndAdminHoldTheSameGrants(t *testing.T) {
	t.Parallel()

	owner := authz.ActionsFor(authz.RoleOwner)
	admin := authz.ActionsFor(authz.RoleAdmin)

	slices.Sort(owner)
	slices.Sort(admin)

	if !slices.Equal(owner, admin) {
		t.Errorf("owner and admin have diverged:\n owner: %v\n admin: %v", owner, admin)
	}
}

func TestOnlyOwnerAndAdminCanManageUsers(t *testing.T) {
	t.Parallel()

	// BR-ADM-03: role management is the escalation path. If any other role
	// holds it, every other boundary in this file is decorative.
	allowed := map[authz.Role]bool{authz.RoleOwner: true, authz.RoleAdmin: true}

	for _, r := range authz.Roles() {
		holds := slices.Contains(authz.ActionsFor(r), authz.ActionUserManage)
		if holds && !allowed[r] {
			t.Errorf("role %q can manage users and therefore escalate itself", r)
		}
		if !holds && allowed[r] {
			t.Errorf("role %q should be able to manage users", r)
		}
	}
}

func TestOnlyOwnerAndAdminCanChangeTaxRates(t *testing.T) {
	t.Parallel()

	// BR-TAX-09: a GST rate change is legally consequential, carries a
	// government notification reference and needs a second approver. It stays
	// at owner level.
	allowed := map[authz.Role]bool{authz.RoleOwner: true, authz.RoleAdmin: true}

	for _, r := range authz.Roles() {
		if slices.Contains(authz.ActionsFor(r), authz.ActionTaxWrite) && !allowed[r] {
			t.Errorf("role %q can change GST rates", r)
		}
	}
}

func TestManagerIsStrictlyBelowOwner(t *testing.T) {
	t.Parallel()

	manager := authz.ActionsFor(authz.RoleManager)

	// Everything a manager holds, an owner holds.
	for _, a := range manager {
		if !slices.Contains(full(), a) {
			t.Errorf("manager holds %q which owner does not; the hierarchy is inverted", a)
		}
	}
	// And strictly less: a manager who could do everything is an owner.
	if len(manager) >= len(full()) {
		t.Errorf("manager holds %d actions and owner %d; manager must be strictly smaller",
			len(manager), len(full()))
	}
}

func TestDataEntryCannotReachMoneyOrPersonalData(t *testing.T) {
	t.Parallel()

	// The point of this role: many accounts, often on shared or unmanaged
	// devices, doing product data work. It must not be a route to customer PII
	// or to money.
	forbidden := []authz.Action{
		authz.ActionPricingWrite,
		authz.ActionRefundWrite,
		authz.ActionOrderRead,
		authz.ActionOrderWrite,
		authz.ActionCustomerRead,
		authz.ActionMarketingExport,
		authz.ActionTaxWrite,
		authz.ActionUserManage,
		authz.ActionInventoryWrite,
	}

	held := authz.ActionsFor(authz.RoleDataEntry)
	for _, a := range forbidden {
		if slices.Contains(held, a) {
			t.Errorf("data entry holds %q; it must not reach money or personal data", a)
		}
	}

	// It must still be able to do its actual job.
	for _, a := range []authz.Action{authz.ActionCatalogRead, authz.ActionCatalogWrite} {
		if !slices.Contains(held, a) {
			t.Errorf("data entry cannot %q, so it cannot do its job", a)
		}
	}
}

func TestCounterSalesCanSellButNotAdjustOrRefund(t *testing.T) {
	t.Parallel()

	held := authz.ActionsFor(authz.RoleCounterSales)

	// It must be able to run a till (docs/02 §1B).
	for _, a := range []authz.Action{
		authz.ActionCatalogRead,   // resolve a scanned or keyed code
		authz.ActionInventoryRead, // see sellable batches in the chooser
		authz.ActionOrderRead,
		authz.ActionOrderWrite, // create the counter sale
		authz.ActionLoyaltyWrite,
	} {
		if !slices.Contains(held, a) {
			t.Errorf("counter sales cannot %q, so it cannot run a till", a)
		}
	}

	// And nothing beyond it. A refund is the one counter action that moves
	// money outward, so it is routed to a manager instead (BR-ADM-04).
	for _, a := range []authz.Action{
		authz.ActionRefundWrite,
		authz.ActionInventoryWrite,
		authz.ActionPricingWrite,
		authz.ActionCatalogWrite,
		authz.ActionCustomerRead,
		authz.ActionMarketingExport,
		authz.ActionTaxWrite,
		authz.ActionUserManage,
	} {
		if slices.Contains(held, a) {
			t.Errorf("counter sales holds %q; a till operator must not have it", a)
		}
	}
}

func TestPaymentTakersCannotVerifyPayments(t *testing.T) {
	t.Parallel()

	// BR-STO-31, the maker-checker control. Under UPI-only, presenting one's
	// own QR instead of the shop's is the sole remaining way to divert money,
	// so whoever takes a payment must never be the one who confirms it landed.
	// If this test ever passes trivially because a role gained both, the
	// control is gone and nobody would notice at runtime.
	takers := []authz.Role{authz.RoleCounterSales, authz.RoleDelivery}

	for _, r := range takers {
		held := authz.ActionsFor(r)
		if slices.Contains(held, authz.ActionPaymentVerify) {
			t.Errorf("role %q both takes payments and can verify them; the maker-checker split is broken", r)
		}
	}

	// And the checkers must actually be able to check, or the queue never
	// clears and staff route around the control.
	for _, r := range []authz.Role{
		authz.RoleSupport, authz.RoleManager, authz.RoleFinance,
		authz.RoleOwner, authz.RoleAdmin,
	} {
		if !slices.Contains(authz.ActionsFor(r), authz.ActionPaymentVerify) {
			t.Errorf("role %q should be able to verify payments", r)
		}
	}
}

func TestDeliveryRoleIsMinimal(t *testing.T) {
	t.Parallel()

	held := authz.ActionsFor(authz.RoleDelivery)

	if !slices.Contains(held, authz.ActionDeliveryUpdate) {
		t.Error("a delivery person must be able to mark a delivery delivered")
	}

	// A delivery person carries goods, not authority. They handle no money at
	// all under UPI-only, and they must not be able to browse the business.
	for _, a := range []authz.Action{
		authz.ActionPaymentVerify,
		authz.ActionOrderWrite,
		authz.ActionCustomerRead,
		authz.ActionCatalogRead,
		authz.ActionInventoryRead,
		authz.ActionReportRead,
		authz.ActionRefundWrite,
		authz.ActionUserManage,
	} {
		if slices.Contains(held, a) {
			t.Errorf("delivery holds %q; the role must carry goods, not authority", a)
		}
	}

	if len(held) != 1 {
		t.Errorf("delivery holds %d actions (%v); it should hold exactly one", len(held), held)
	}
}

func TestPlatformAndShopRolesAreDisjoint(t *testing.T) {
	t.Parallel()

	// The vendor creates and manages the SaaS; the client owns and operates the
	// business (docs/09 §6). A platform role that could read a shop's orders
	// would make the vendor a participant in its customers' businesses — and a
	// shop role that could provision clients would let one customer reach
	// another. Both directions are asserted here because both are one careless
	// grant away.
	shop := map[authz.Action]bool{}
	for _, a := range authz.ShopActions() {
		shop[a] = true
	}
	platform := map[authz.Action]bool{}
	for _, a := range authz.PlatformActions() {
		platform[a] = true
	}

	for _, r := range authz.Roles() {
		held := authz.ActionsFor(r)

		if r.IsPlatform() {
			for _, a := range held {
				if shop[a] {
					t.Errorf("platform role %q holds shop action %q; the vendor must not reach a client's business", r, a)
				}
			}
			continue
		}

		for _, a := range held {
			if platform[a] {
				t.Errorf("shop role %q holds platform action %q; a client must not operate the SaaS", r, a)
			}
		}
	}
}

func TestSaaSAdminCannotTouchAnyShopData(t *testing.T) {
	t.Parallel()

	// Stated as its own test because it is the single most important
	// consequence of the division, and the one somebody will eventually be
	// tempted to relax "just for support".
	e := authz.NewRBAC()
	ctx := context.Background()
	admin := authz.Actor{ID: "vendor-1", Type: authz.ActorAdmin, Roles: []authz.Role{authz.RoleSaaSAdmin}}

	for _, a := range authz.ShopActions() {
		if err := e.Can(ctx, admin, a, authz.Resource{Type: "*"}); !errors.Is(err, authz.ErrDenied) {
			t.Errorf("saas_admin was allowed shop action %q: %v", a, err)
		}
	}

	// And it must still be able to do its actual job.
	for _, a := range []authz.Action{
		authz.ActionClientManage, authz.ActionSubscriptionManage, authz.ActionPlatformOperate,
	} {
		if err := e.Can(ctx, admin, a, authz.Resource{Type: "*"}); err != nil {
			t.Errorf("saas_admin cannot %q, so it cannot manage the SaaS: %v", a, err)
		}
	}
}

func TestNoShopRoleCanProvisionClients(t *testing.T) {
	t.Parallel()

	// A shop owner runs their business, not the platform. If an owner could
	// provision clients, one customer could create or alter another.
	e := authz.NewRBAC()
	ctx := context.Background()

	for _, r := range []authz.Role{authz.RoleOwner, authz.RoleAdmin, authz.RoleManager} {
		actor := authz.Actor{ID: "s", Type: authz.ActorAdmin, Roles: []authz.Role{r}}
		for _, a := range authz.PlatformActions() {
			if err := e.Can(ctx, actor, a, authz.Resource{Type: "*"}); !errors.Is(err, authz.ErrDenied) {
				t.Errorf("shop role %q was allowed platform action %q: %v", r, a, err)
			}
		}
	}
}

func TestOnlyOwnerAdminAndMarketingCanExportContactData(t *testing.T) {
	t.Parallel()

	// BR-RPT-05, BR-CMP-17: an export of customer contact details is the
	// highest-volume data-loss path in the platform.
	allowed := map[authz.Role]bool{
		authz.RoleOwner: true, authz.RoleAdmin: true, authz.RoleMarketing: true,
	}

	for _, r := range authz.Roles() {
		if slices.Contains(authz.ActionsFor(r), authz.ActionMarketingExport) && !allowed[r] {
			t.Errorf("role %q can export customer contact data", r)
		}
	}
}

func TestNoRoleIsAccidentallyOmnipotent(t *testing.T) {
	t.Parallel()

	// Any role other than owner and admin holding every action would be a
	// silent super-user.
	for _, r := range authz.Roles() {
		if r == authz.RoleOwner || r == authz.RoleAdmin {
			continue
		}
		if len(authz.ActionsFor(r)) >= len(full()) {
			t.Errorf("role %q holds every action; it is an undeclared super-user", r)
		}
	}
}

func TestRoleGrantsAreEnforcedEndToEnd(t *testing.T) {
	t.Parallel()

	// The grant table and the enforcer must agree. A grant that the enforcer
	// does not honour is a permission that silently does nothing.
	e := authz.NewRBAC()
	ctx := context.Background()
	res := authz.Resource{Type: "*"}

	for _, r := range authz.Roles() {
		actor := authz.Actor{ID: "s", Type: authz.ActorAdmin, Roles: []authz.Role{r}}

		for _, a := range authz.ActionsFor(r) {
			if err := e.Can(ctx, actor, a, res); err != nil {
				t.Errorf("role %q is granted %q but the enforcer refuses: %v", r, a, err)
			}
		}

		for _, a := range full() {
			if slices.Contains(authz.ActionsFor(r), a) {
				continue
			}
			if err := e.Can(ctx, actor, a, res); !errors.Is(err, authz.ErrDenied) {
				t.Errorf("role %q is not granted %q but the enforcer allowed it: %v", r, a, err)
			}
		}
	}
}
