package policy

import (
	"time"

	"github.com/stephenindia1/steleios-ecom/internal/platform/authz"
	"github.com/stephenindia1/steleios-ecom/internal/platform/ratelimit"
)

// This file is the complete security surface of the application.
//
// Every route in Steleios is registered with one of the policies below. Nothing
// else may construct a Policy (SEC-04), and changing this file requires a
// security review on the pull request (SEC-05). Read top to bottom and you have
// read what protects the platform.

var (
	// Public is deliberately unauthenticated: the storefront catalog, health
	// endpoints, and anything a search or AI crawler must reach (SEO-001).
	// Every use of this policy is a review item.
	Public = Policy{
		Name:      "public",
		Auth:      AuthNone,
		RateLimit: ratelimit.PerIP(120, time.Minute),
	}

	// PublicCached is Public for responses served from the catalog cache, where
	// a higher ceiling is safe because the work per request is small.
	PublicCached = Policy{
		Name:      "public.cached",
		Auth:      AuthNone,
		RateLimit: ratelimit.PerIP(600, time.Minute),
	}

	// Probe serves liveness and readiness. Unlimited because throttling a
	// health check turns a transient spike into a rolling restart (HLT-001).
	Probe = Policy{
		Name:      "probe",
		Auth:      AuthNone,
		Unlimited: true,
		Timeout:   2 * time.Second,
	}

	// AuthAttempt covers login, registration, OTP send and password reset:
	// unauthenticated by necessity and therefore throttled hard, per address
	// AND per account, because either limit alone is trivially evaded
	// (BR-IDN-05, BR-IDN-11).
	AuthAttempt = Policy{
		Name: "auth.attempt",
		Auth: AuthNone,
		CSRF: true,
		RateLimit: ratelimit.Composite(
			ratelimit.PerIP(10, time.Hour),
			ratelimit.PerSubject(5, 15*time.Minute),
		),
	}

	// OTPSend is tighter still: an SMS costs money and an OTP flood is both a
	// bill and a nuisance to the person receiving them (BR-IDN-05).
	OTPSend = Policy{
		Name: "auth.otp.send",
		Auth: AuthNone,
		CSRF: true,
		RateLimit: ratelimit.Composite(
			ratelimit.PerIP(10, time.Hour),
			ratelimit.PerSubject(3, 15*time.Minute),
		),
	}

	// GuestOrSession covers cart operations, which must work before sign-in
	// (BR-CRT-02, BR-IDN-09).
	GuestOrSession = Policy{
		Name:      "cart.session",
		Auth:      AuthSessionOrGuest,
		CSRF:      true,
		RateLimit: ratelimit.PerActor(300, time.Minute),
	}

	// CustomerSession is a signed-in customer acting on their own account,
	// where the resource is implied by the session rather than named in the
	// path (their order list, their addresses).
	CustomerSession = Policy{
		Name:      "customer.session",
		Auth:      AuthSession,
		CSRF:      true,
		RateLimit: ratelimit.PerActor(300, time.Minute),
	}

	// CustomerOrderRead is the policy that stops the single most common
	// commerce vulnerability: GET /orders/{id} returning someone else's order
	// (BR-ORD-05).
	CustomerOrderRead = Policy{
		Name:      "customer.order.read",
		Auth:      AuthSession,
		Ownership: OwnedBy("order", "id"),
		RateLimit: ratelimit.PerActor(300, time.Minute),
	}

	// CustomerOrderWrite covers a customer cancelling or returning their own
	// order. Writes are throttled harder than reads.
	CustomerOrderWrite = Policy{
		Name:      "customer.order.write",
		Auth:      AuthSession,
		Ownership: OwnedBy("order", "id"),
		CSRF:      true,
		RateLimit: ratelimit.PerActor(60, time.Minute),
	}

	// CustomerAddressWrite covers address book changes.
	CustomerAddressWrite = Policy{
		Name:      "customer.address.write",
		Auth:      AuthSession,
		Ownership: OwnedBy("address", "id"),
		CSRF:      true,
		RateLimit: ratelimit.PerActor(60, time.Minute),
	}

	// Checkout creates an order. Idempotent so a double-clicked Pay button
	// cannot produce two orders (BR-CHK-02), and throttled so a bot cannot
	// squat limited stock by opening reservations (BR-CHK-05).
	Checkout = Policy{
		Name:       "checkout",
		Auth:       AuthSessionOrGuest,
		CSRF:       true,
		Idempotent: true,
		RateLimit:  ratelimit.PerActor(10, 10*time.Minute),
		Timeout:    20 * time.Second,
	}

	// ProviderWebhook carries no session and no CSRF, by design. Its only
	// authentication is the HMAC signature over the raw body, verified with the
	// webhook secret — a different secret from the API key secret
	// (BR-PAY-04/05/06).
	//
	// Do not "fix" the absence of CSRF here. The exemption is deliberate and
	// policy validation enforces that it stays deliberate.
	ProviderWebhook = Policy{
		Name:      "webhook.provider",
		Auth:      AuthSignature,
		RawBody:   true,
		RateLimit: ratelimit.PerIP(600, time.Minute),
		Timeout:   5 * time.Second, // BR-PAY-08
	}

	// SignedIn is any authenticated staff session, with NO permission required.
	//
	// It exists for the handful of routes every signed-in person must reach
	// regardless of what they may do: sign out, change their own password,
	// choose a shop, see who they are.
	//
	// It deliberately requires no role, which is what lets an account in the
	// post-recovery locked state reach exactly these routes and nothing else —
	// that account carries no roles at all, so any policy with a permission
	// would shut it out of changing the password it is locked into changing
	// (BR-REC-20).
	SignedIn = Policy{
		Name:      "signed_in",
		Auth:      AuthAdmin,
		CSRF:      true,
		RateLimit: ratelimit.PerActor(120, time.Minute),
	}

	// --- Staff -------------------------------------------------------------

	// AdminRead is any staff read: order lookup, customer lookup, reports.
	AdminRead = Policy{
		Name:       "admin.read",
		Auth:       AuthAdmin,
		Permission: authz.ActionOrderRead,
		RateLimit:  ratelimit.PerActor(600, time.Minute),
	}

	// AdminOps covers fulfilment: status transitions, dispatch, shipment
	// management.
	AdminOps = Policy{
		Name:       "admin.ops",
		Auth:       AuthAdmin,
		Permission: authz.ActionOrderWrite,
		CSRF:       true,
		RateLimit:  ratelimit.PerActor(300, time.Minute),
	}

	// AdminInventory covers stock adjustment, which always carries a reason and
	// an audit entry (BR-INV-11).
	AdminInventory = Policy{
		Name:       "admin.inventory",
		Auth:       AuthAdmin,
		Permission: authz.ActionInventoryWrite,
		CSRF:       true,
		RateLimit:  ratelimit.PerActor(300, time.Minute),
	}

	// AdminCatalog covers product, variant and media management.
	AdminCatalog = Policy{
		Name:       "admin.catalog",
		Auth:       AuthAdmin,
		Permission: authz.ActionCatalogWrite,
		CSRF:       true,
		RateLimit:  ratelimit.PerActor(300, time.Minute),
	}

	// AdminPricing covers price edits and manual markdown, which need a reason
	// and, past a threshold, a second approver (BR-BAT-37).
	AdminPricing = Policy{
		Name:         "admin.pricing",
		Auth:         AuthAdmin,
		Permission:   authz.ActionPricingWrite,
		CSRF:         true,
		Reauth:       true,
		DualApproval: true,
		RateLimit:    ratelimit.PerActor(60, time.Minute),
	}

	// AdminFinance covers refunds: re-authentication and second-actor approval,
	// because this is where money leaves (BR-ADM-04, BR-ADM-07).
	AdminFinance = Policy{
		Name:         "admin.finance",
		Auth:         AuthAdmin,
		Permission:   authz.ActionRefundWrite,
		CSRF:         true,
		Idempotent:   true,
		Reauth:       true,
		DualApproval: true,
		RateLimit:    ratelimit.PerActor(60, time.Minute),
	}

	// AdminTax covers GST rate entry, which is versioned, approved and audited
	// (BR-TAX-09).
	AdminTax = Policy{
		Name:         "admin.tax",
		Auth:         AuthAdmin,
		Permission:   authz.ActionTaxWrite,
		CSRF:         true,
		Reauth:       true,
		DualApproval: true,
		RateLimit:    ratelimit.PerActor(30, time.Minute),
	}

	// AdminPurchasing covers suppliers, receipts and returns to vendor.
	AdminPurchasing = Policy{
		Name:       "admin.purchasing",
		Auth:       AuthAdmin,
		Permission: authz.ActionPurchasingWrite,
		CSRF:       true,
		RateLimit:  ratelimit.PerActor(300, time.Minute),
	}

	// AdminMarketing covers campaigns and loyalty configuration.
	AdminMarketing = Policy{
		Name:       "admin.marketing",
		Auth:       AuthAdmin,
		Permission: authz.ActionMarketingWrite,
		CSRF:       true,
		RateLimit:  ratelimit.PerActor(300, time.Minute),
	}

	// AdminExport covers exports containing customer contact details. Tightly
	// throttled and always audited with the row count and filter (BR-RPT-05,
	// BR-CMP-17).
	AdminExport = Policy{
		Name:       "admin.export",
		Auth:       AuthAdmin,
		Permission: authz.ActionMarketingExport,
		CSRF:       true,
		Reauth:     true,
		RateLimit:  ratelimit.PerActor(5, time.Hour),
		Timeout:    60 * time.Second,
	}

	// AdminUsers covers role management, which invalidates the target's
	// sessions and is always audited (BR-ADM-03).
	AdminUsers = Policy{
		Name:         "admin.users",
		Auth:         AuthAdmin,
		Permission:   authz.ActionUserManage,
		CSRF:         true,
		Reauth:       true,
		DualApproval: true,
		RateLimit:    ratelimit.PerActor(30, time.Minute),
	}
)

// All returns every policy in the catalogue.
//
// The startup self-check validates all of them before the server binds, so a
// malformed policy stops the process rather than serving one request
// unprotected (SEC-01, TST-02).
func All() []Policy {
	return []Policy{
		Public, PublicCached, Probe,
		AuthAttempt, OTPSend, SignedIn,
		GuestOrSession, CustomerSession,
		CustomerOrderRead, CustomerOrderWrite, CustomerAddressWrite,
		Checkout, ProviderWebhook,
		AdminRead, AdminOps, AdminInventory, AdminCatalog, AdminPricing,
		AdminFinance, AdminTax, AdminPurchasing, AdminMarketing,
		AdminExport, AdminUsers,
	}
}
