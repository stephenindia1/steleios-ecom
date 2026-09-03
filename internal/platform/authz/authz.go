// Package authz is the sole authorization primitive in Steleios
// (docs/03 §3.4, §6.1, CLAUDE.md rule 1).
//
// There is exactly one authorization question in the codebase: may this actor
// perform this action on this resource? It is answered here and nowhere else.
// Comparing a role inline (`if user.Role == "admin"`) anywhere outside this
// package is prohibited and blocked by the lint configuration (SEC-10).
//
// Can returns an error rather than a bool, so a caller cannot accidentally
// ignore the answer the way a discarded boolean allows (SEC-11).
package authz

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

// Errors returned by an Enforcer. Callers match with errors.Is.
var (
	// ErrDenied means the actor may not perform the action. The HTTP layer
	// renders this as 403 or 404 depending on the resource, and never explains
	// why (SEC-12, BR-IDN-06).
	ErrDenied = errors.New("authz: denied")

	// ErrUnauthenticated means no actor was established. Distinct from denial
	// because the remedy differs: sign in, versus you may not.
	ErrUnauthenticated = errors.New("authz: unauthenticated")

	// ErrUnavailable means the decision could not be reached — a dependency was
	// down. It is returned instead of an allow, because every check fails
	// closed (BR-SEC-11).
	ErrUnavailable = errors.New("authz: decision unavailable")
)

// ActorType classifies the principal behind a request.
type ActorType string

// The principal types Steleios recognises.
const (
	// ActorGuest is an unauthenticated visitor with a cart. Guests can check
	// out (BR-IDN-09), so they are a real principal, not an absence of one.
	ActorGuest ActorType = "guest"
	// ActorCustomer is a signed-in customer.
	ActorCustomer ActorType = "customer"
	// ActorAdmin is a staff member with roles.
	ActorAdmin ActorType = "admin"
	// ActorSystem is a worker running a scheduled or queued task. It acts under
	// a named identity with an explicit permission set, never with implicit
	// privilege (WRK-04).
	ActorSystem ActorType = "system"
	// ActorProvider is an external system authenticated by signature, such as a
	// Razorpay webhook (BR-PAY-04).
	ActorProvider ActorType = "provider"
)

// Role is a named set of permissions granted to staff (docs/02 §15).
//
// There are two disjoint worlds of roles, and keeping them apart is a security
// boundary rather than a naming convention:
//
//   - SHOP roles (owner, manager, counter_sales, …) operate a business. They
//     reach orders, stock, customers and money, confined to one shop by
//     row-level security.
//
//   - PLATFORM roles (saas_admin, saas_support) operate the SaaS itself:
//     provisioning clients and shops, subscriptions, platform health. They hold
//     NO shop-data actions at all.
//
// The vendor creates and manages the SaaS; the client owns and operates the
// business (docs/09 §6). A platform role that could read a shop's orders would
// make the vendor a participant in its customers' businesses, which is exactly
// what that division rules out. TestPlatformAndShopRolesAreDisjoint asserts it.
type Role string

// The shop roles. Customers hold no roles; their access is by ownership.
const (
	// RoleOwner is the business owner: everything in their shop, including user
	// and role management. It is the role that cannot be locked out.
	RoleOwner Role = "owner"
	// RoleAdmin is the CLIENT's own technical administrator — the owner's
	// counterpart inside the business, with the same grants. It is not the
	// vendor: the vendor's roles are the saas_* ones below.
	RoleAdmin Role = "admin"
	// RoleManager runs the store day to day: orders, stock, catalog, pricing,
	// purchasing, marketing and reports. Deliberately NOT user management and
	// NOT tax rates — a manager must not be able to grant themselves more, and
	// a GST rate change is an owner-level, notification-backed act (BR-TAX-09).
	RoleManager Role = "manager"
	// RoleDataEntry is a data entry executive: they create and correct product
	// records. Deliberately NOT pricing, NOT orders, and NOT customer data —
	// the largest group of staff accounts should have the least reach into
	// money and personal information.
	RoleDataEntry Role = "data_entry"
	// RoleCounterSales is a till operator: scan or key a code, pick a batch,
	// take payment, award loyalty points (docs/02 §1B). Deliberately NOT
	// refunds — a return at the till is routed to a manager, because a refund
	// is the one counter action that moves money outward (BR-ADM-04).
	RoleCounterSales Role = "counter_sales"
	// There is deliberately no accountant role. The shop's chartered accountant
	// is the shop's relationship, not the vendor's: the owner exports the
	// records and hands them over (docs/09 §6). Granting a third party a login
	// would make the vendor a party to that arrangement, which is exactly what
	// is not wanted.
	//
	// RoleDelivery is a delivery person: see their assigned deliveries, mark
	// one delivered, and assert that the customer paid.
	//
	// Deliberately NOT payment verification. Under UPI-only, presenting one's
	// own QR instead of the shop's is the sole remaining way to divert money,
	// so the person who takes a payment must never be the person who confirms
	// it arrived (BR-STO-31).
	RoleDelivery Role = "delivery"

	RoleViewer     Role = "viewer"
	RoleSupport    Role = "support"
	RoleOps        Role = "ops"
	RoleFinance    Role = "finance"
	RoleCatalog    Role = "catalog"
	RoleMarketing  Role = "marketing"
	RolePurchasing Role = "purchasing"
)

// The platform roles. These belong to the VENDOR and manage the SaaS: which
// clients exist, their shops, their subscriptions, and the health of the
// service. They hold no shop-data actions whatsoever.
const (
	// RoleSaaSAdmin creates and manages SaaS clients: onboarding a business,
	// provisioning its shops, managing its subscription, suspending or closing
	// it. It cannot read or write any shop's orders, stock, customers or
	// documents.
	RoleSaaSAdmin Role = "saas_admin"
	// RoleSaaSSupport is vendor support: read-only over client and subscription
	// state, so a question about billing or provisioning can be answered
	// without touching the client's business data.
	RoleSaaSSupport Role = "saas_support"
)

// PlatformRoles are the vendor-side roles, kept in one list so the disjointness
// test and the admin UI cannot drift from each other.
func PlatformRoles() []Role { return []Role{RoleSaaSAdmin, RoleSaaSSupport} }

// AllRoles returns every role the system grants anything to.
//
// It is derived from the grant table rather than typed out again, because the
// two lists drifting is not hypothetical: delivery, saas_admin and saas_support
// were granted actions here and were missing from the staff_roles table for
// three migrations, which made them impossible to assign (migration 00018). The
// integration test compares this against the database.
//
// Sorted, so callers and test failures are deterministic.
func AllRoles() []Role {
	out := make([]Role, 0, len(grants)) // DB-024
	for r := range grants {
		out = append(out, r)
	}
	slices.Sort(out)
	return out
}

// IsPlatform reports whether r is a vendor-side role.
func (r Role) IsPlatform() bool { return slices.Contains(PlatformRoles(), r) }

// Action is a permission, named `<resource>:<verb>`.
type Action string

// The actions the platform checks. Adding one here without granting it to any
// role means nobody can perform it — which is the correct default (fail closed).
const (
	ActionOrderRead       Action = "order:read"
	ActionOrderWrite      Action = "order:write"
	ActionCatalogRead     Action = "catalog:read"
	ActionCatalogWrite    Action = "catalog:write"
	ActionInventoryRead   Action = "inventory:read"
	ActionInventoryWrite  Action = "inventory:write"
	ActionRefundWrite     Action = "refund:write"
	ActionPricingWrite    Action = "pricing:write"
	ActionTaxWrite        Action = "tax:write"
	ActionCustomerRead    Action = "customer:read"
	ActionPurchasingRead  Action = "purchasing:read"
	ActionPurchasingWrite Action = "purchasing:write"
	ActionMarketingWrite  Action = "marketing:write"
	ActionMarketingExport Action = "marketing:export"
	ActionLoyaltyWrite    Action = "loyalty:write"
	ActionReportRead      Action = "report:read"
	ActionUserManage      Action = "user:manage"

	// ActionDeliveryUpdate lets a delivery person mark an assigned delivery
	// as delivered and assert that payment was taken.
	ActionDeliveryUpdate Action = "delivery:update"
	// ActionPaymentVerify confirms that a recorded payment actually arrived in
	// the shop's account. It is the checker half of a maker-checker control and
	// MUST NOT be held by anyone who can take a payment (BR-STO-31).
	ActionPaymentVerify Action = "payment:verify"
	// ActionDocumentRead reads the document trail: invoices, credit and debit
	// notes, purchase invoices, delivery challans.
	ActionDocumentRead Action = "document:read"
	// ActionDocumentExport exports a period's documents and figures for
	// accounting. Separate from reading because an export takes a copy of the
	// shop's books out of the system, and is audited with its row count and
	// period (BR-DOC-63).
	ActionDocumentExport Action = "document:export"
)

// Platform actions. These operate on the SaaS — which clients exist, their
// shops and their subscriptions — and never on a shop's business data.
const (
	// ActionClientRead reads client and shop provisioning state.
	ActionClientRead Action = "client:read"
	// ActionClientManage onboards a client, provisions its shops, and suspends
	// or closes it.
	ActionClientManage Action = "client:manage"
	// ActionSubscriptionRead reads subscription and billing state.
	ActionSubscriptionRead Action = "subscription:read"
	// ActionSubscriptionManage issues activation codes, changes plans, and
	// records or cancels subscriptions.
	ActionSubscriptionManage Action = "subscription:manage"
	// ActionPlatformOperate covers service-level operations: health, migrations,
	// staged rollouts. Not a client's data under any circumstances.
	ActionPlatformOperate Action = "platform:operate"
	// ActionSupportSessionRequest asks a client for time-boxed access to
	// diagnose a fault in their data (docs/09 §4B).
	//
	// It grants NOTHING by itself. It is the ability to ask, and the client
	// grants or refuses. The resulting access is separate, scoped to that
	// client, time-boxed, read-only by default, visible to them while it runs,
	// revocable by them, and audited into their own log.
	//
	// It is a platform action rather than a shop one precisely because holding
	// it must not imply holding any business-data action (BR-SUP-10).
	ActionSupportSessionRequest Action = "support_session:request"
)

// shopActions is every action that touches a client's business. A platform role
// holding any of these would breach the vendor/client division (docs/09 §6), so
// the list exists to be asserted against rather than to be read.
func shopActions() []Action {
	return []Action{
		ActionOrderRead, ActionOrderWrite, ActionCatalogRead, ActionCatalogWrite,
		ActionInventoryRead, ActionInventoryWrite, ActionRefundWrite,
		ActionPricingWrite, ActionTaxWrite, ActionCustomerRead,
		ActionPurchasingRead, ActionPurchasingWrite, ActionMarketingWrite,
		ActionMarketingExport, ActionLoyaltyWrite, ActionReportRead,
		ActionUserManage, ActionDeliveryUpdate, ActionPaymentVerify,
		ActionDocumentRead, ActionDocumentExport,
	}
}

// platformActions is every action that operates the SaaS.
func platformActions() []Action {
	return []Action{
		ActionClientRead, ActionClientManage,
		ActionSubscriptionRead, ActionSubscriptionManage,
		ActionPlatformOperate, ActionSupportSessionRequest,
	}
}

// ShopActions returns the business-data actions, for tests and admin UIs.
func ShopActions() []Action { return slices.Clone(shopActions()) }

// PlatformActions returns the SaaS-operation actions.
func PlatformActions() []Action { return slices.Clone(platformActions()) }

// Actor is the principal performing an action.
//
// It is a value type: it is resolved once by the session middleware and passed
// down, never mutated by a service.
type Actor struct {
	ID    string
	Type  ActorType
	Roles []Role
	// SessionFingerprint is a hashed reference to the session, safe to log
	// (SES-010).
	SessionFingerprint string
}

// IsAuthenticated reports whether an identified principal is present. A guest is
// a principal but is not authenticated.
func (a Actor) IsAuthenticated() bool {
	return a.ID != "" && a.Type != "" && a.Type != ActorGuest
}

// Resource identifies what is being acted upon.
//
// OwnerID is the customer who owns it, where ownership is meaningful. An empty
// OwnerID on a resource type that is owned means "unknown owner", which is
// treated as a denial rather than as public.
type Resource struct {
	Type    string
	ID      string
	OwnerID string
}

// Enforcer answers the one authorization question.
type Enforcer interface {
	Can(ctx context.Context, actor Actor, action Action, res Resource) error
}

// grants maps each role to the actions it may perform. It is a package-level
// var only because it is immutable reference data built once at init of the
// RBAC value; nothing mutates it (OOP-03).
var grants = map[Role][]Action{
	RoleViewer: {
		ActionOrderRead, ActionCatalogRead, ActionInventoryRead,
		ActionCustomerRead, ActionReportRead, ActionPurchasingRead,
	},
	// Customer care. Holds payment verification because confirming a UPI credit
	// against the shop's account is exactly the kind of check a support desk
	// does all day — and support never takes a payment, so it is a valid
	// checker (BR-STO-31).
	RoleSupport: {
		ActionOrderRead, ActionOrderWrite, ActionCatalogRead,
		ActionInventoryRead, ActionCustomerRead, ActionReportRead,
		ActionPaymentVerify, ActionDocumentRead,
	},
	RoleOps: {
		ActionOrderRead, ActionOrderWrite, ActionCatalogRead,
		ActionInventoryRead, ActionInventoryWrite, ActionCustomerRead,
		ActionReportRead, ActionPurchasingRead,
	},
	RoleFinance: {
		ActionOrderRead, ActionRefundWrite, ActionCustomerRead,
		ActionReportRead, ActionPurchasingRead, ActionPaymentVerify,
		ActionDocumentRead, ActionDocumentExport,
	},
	RoleCatalog: {
		ActionCatalogRead, ActionCatalogWrite, ActionInventoryRead,
		ActionPricingWrite, ActionReportRead,
	},
	RoleMarketing: {
		ActionCatalogRead, ActionCustomerRead, ActionReportRead,
		ActionMarketingWrite, ActionMarketingExport, ActionLoyaltyWrite,
	},
	RolePurchasing: {
		ActionCatalogRead, ActionInventoryRead, ActionInventoryWrite,
		ActionPurchasingRead, ActionPurchasingWrite, ActionReportRead,
	},
	RoleAdmin: everything,
	RoleOwner: everything,

	// A manager runs the store but cannot grant permissions and cannot alter
	// tax rates. Both exclusions are deliberate: the first prevents self-
	// escalation, the second keeps a legally-consequential change at owner
	// level with its notification reference and second approver (BR-TAX-09).
	RoleManager: {
		ActionOrderRead, ActionOrderWrite, ActionCatalogRead, ActionCatalogWrite,
		ActionInventoryRead, ActionInventoryWrite, ActionRefundWrite,
		ActionPricingWrite, ActionCustomerRead, ActionPurchasingRead,
		ActionPurchasingWrite, ActionMarketingWrite, ActionLoyaltyWrite,
		ActionReportRead, ActionPaymentVerify, ActionDocumentRead,
		ActionDocumentExport,
	},

	// A data entry executive maintains product records and nothing else. No
	// pricing, no orders, no customer data, no exports. This is usually the
	// largest group of accounts and often the least controlled hardware, so it
	// gets the smallest reach into money and personal data.
	RoleDataEntry: {
		ActionCatalogRead, ActionCatalogWrite, ActionInventoryRead,
		ActionPurchasingRead,
	},

	// A counter sales user sells: resolve a code, choose a batch, take payment,
	// award or redeem loyalty points. They read stock but never adjust it, and
	// they never refund — a return at the till goes to a manager.
	//
	// They take payments, so they cannot verify them (BR-STO-31).
	RoleCounterSales: {
		ActionCatalogRead, ActionInventoryRead,
		ActionOrderRead, ActionOrderWrite,
		ActionLoyaltyWrite,
	},

	// A delivery person sees their assigned deliveries and marks them
	// delivered. That is all. They cannot browse orders, read customer records
	// beyond the delivery in hand, or verify that a payment arrived — the last
	// of which is the whole point of the maker-checker split.
	RoleDelivery: {
		ActionDeliveryUpdate,
	},

	// ---- Platform roles: the vendor operates the SaaS, not the business ----

	// The vendor's administrator. Creates and manages SaaS clients — onboards a
	// business, provisions its shops, manages its subscription — and holds no
	// action that reads or writes a shop's orders, stock, customers or books.
	RoleSaaSAdmin: {
		ActionClientRead, ActionClientManage,
		ActionSubscriptionRead, ActionSubscriptionManage,
		ActionPlatformOperate, ActionSupportSessionRequest,
	},

	// Vendor support: read-only over provisioning and billing, so a question
	// about a subscription can be answered without reaching into the client's
	// business.
	RoleSaaSSupport: {
		ActionClientRead, ActionSubscriptionRead, ActionSupportSessionRequest,
	},
}

// everything is the full grant, shared by owner and admin. It is derived rather
// than typed twice, so a new action cannot be added to one and forgotten in the
// other (DRY).
var everything = []Action{
	ActionOrderRead, ActionOrderWrite, ActionCatalogRead, ActionCatalogWrite,
	ActionInventoryRead, ActionInventoryWrite, ActionRefundWrite,
	ActionPricingWrite, ActionTaxWrite, ActionCustomerRead,
	ActionPurchasingRead, ActionPurchasingWrite, ActionMarketingWrite,
	ActionMarketingExport, ActionLoyaltyWrite, ActionReportRead,
	ActionUserManage, ActionDeliveryUpdate, ActionPaymentVerify,
	ActionDocumentRead, ActionDocumentExport,
}

// ownedResourceTypes are resource types a customer may access by ownership
// rather than by role. Anything not listed here is staff-only by default —
// adding a resource type without deciding is a denial, not an allow.
var ownedResourceTypes = map[string]struct{}{
	"order":        {},
	"cart":         {},
	"address":      {},
	"payment":      {},
	"return":       {},
	"review":       {},
	"loyalty":      {},
	"notification": {},
}

// RBAC is the production Enforcer. It combines role grants for staff with
// ownership checks for customers.
//
// It holds no state and no dependencies, so it is safe to share.
type RBAC struct{}

// NewRBAC returns the production enforcer.
func NewRBAC() RBAC { return RBAC{} }

// Can answers whether actor may perform action on res.
//
// The order of checks matters. Authentication is established first, then
// ownership, then roles — so that a customer's own order is reachable without
// any role, and a staff role never silently substitutes for ownership.
func (RBAC) Can(ctx context.Context, actor Actor, action Action, res Resource) error {
	// Fail closed if the caller's context is already finished: a cancelled
	// request must not be authorized on the way out (BR-SEC-11).
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}

	if actor.Type == "" {
		return fmt.Errorf("%w: no actor", ErrUnauthenticated)
	}

	switch actor.Type {
	case ActorSystem:
		// A worker acts under an explicit permission set, granted the same way
		// staff are, so a task cannot quietly do more than it was built to do.
		return checkRoles(actor, action)

	case ActorProvider:
		// A provider is authenticated by signature and may only do what its
		// route allows; it never carries roles or ownership.
		return fmt.Errorf("%w: provider actors have no granted actions", ErrDenied)

	case ActorGuest, ActorCustomer:
		return checkOwnership(actor, action, res)

	case ActorAdmin:
		return checkRoles(actor, action)

	default:
		return fmt.Errorf("%w: unknown actor type %q", ErrDenied, actor.Type)
	}
}

// checkOwnership allows a customer or guest to act on a resource they own.
func checkOwnership(actor Actor, action Action, res Resource) error {
	if _, owned := ownedResourceTypes[res.Type]; !owned {
		return fmt.Errorf("%w: %s is not a customer-owned resource", ErrDenied, res.Type)
	}
	if actor.ID == "" {
		return fmt.Errorf("%w: anonymous actor cannot own %s", ErrUnauthenticated, res.Type)
	}
	// An unknown owner is a denial, never a public resource. This is the check
	// that stops GET /orders/{someone-elses-id} (BR-ORD-05).
	if res.OwnerID == "" {
		return fmt.Errorf("%w: %s %s has no recorded owner", ErrDenied, res.Type, res.ID)
	}
	if res.OwnerID != actor.ID {
		return fmt.Errorf("%w: actor %s does not own %s %s", ErrDenied, actor.ID, res.Type, res.ID)
	}
	return nil
}

// checkRoles allows a staff or system actor holding a granting role.
func checkRoles(actor Actor, action Action) error {
	if len(actor.Roles) == 0 {
		return fmt.Errorf("%w: actor %s holds no roles", ErrDenied, actor.ID)
	}
	for _, role := range actor.Roles {
		if slices.Contains(grants[role], action) {
			return nil
		}
	}
	return fmt.Errorf("%w: none of %v grants %s", ErrDenied, actor.Roles, action)
}

// ActionsFor returns the actions granted to a role, for the startup route table
// and for tests. The returned slice is a copy; callers cannot mutate the grants.
func ActionsFor(r Role) []Action { return slices.Clone(grants[r]) }

// Roles returns every defined role, for administration UIs and tests.
func Roles() []Role {
	out := make([]Role, 0, len(grants)) // DB-024
	for r := range grants {
		out = append(out, r)
	}
	slices.Sort(out)
	return out
}
