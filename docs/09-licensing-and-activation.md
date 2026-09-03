# Steleios — Licensing and Subscription

Engraved rules for how a shop is licensed to use the system. **Normative: MUST / MUST NOT.**
Companions: [ADR 0006](decisions/0006-fully-online.md) · [10-deployment-and-installation.md](10-deployment-and-installation.md).

Status: draft · 3 September 2026 — rewritten after ADR 0006

---

## 0. The model

Steleios is **sold to shop owners as a hosted subscription**. An owner pays the vendor, receives an activation code, and that code opens their instance for a stated period.

Because Steleios is fully online (ADR 0006), licensing is simple in a way it could not otherwise be:

> **The subscription is a row in the vendor's database, on the vendor's infrastructure.** Entitlement is a read. Renewal is an update. Revocation is immediate.

Nothing about the licence lives on the customer's side, so there is nothing to forge, nothing to tamper with, and no cryptography to get right. An earlier draft of this document specified Ed25519-signed tokens, signing-key rotation, installation binding, clock-rollback detection and offline activation files — **all of that existed to solve offline verification, and all of it is gone.**

What remains is a billing state and a set of rules about how the system behaves in each one. The rules that matter are in §3, and they are commercial commitments rather than technical mechanisms.

---

## 1. Subscription and activation

| ID | Rule |
|---|---|
| BR-LIC-01 | A subscription record holds: tenant, plan, `valid_from`, `valid_until`, entitlements (§2), billing state, and its history. It is the single source of entitlement. |
| BR-LIC-02 | `[SEC]` Entitlement is read from the subscription record on the server. It MUST NOT be read from, or influenced by, anything the client sends. |
| BR-LIC-03 | The owner activates with a **short activation code**, issued by the vendor against a payment. Redemption sets `valid_from` and `valid_until` on the tenant's subscription. |
| BR-LIC-04 | `[SEC]` An activation code is single-use, bound to the tenant it was issued for, and expires if unredeemed. Redeeming it twice is refused and the attempt is recorded. |
| BR-LIC-05 | Renewal extends `valid_until`. Renewing early is permitted and simply extends the term; it never shortens it. |
| BR-LIC-06 | `[SEC]` Activation, renewal, plan change and any entitlement change are audited with the actor, the resulting term and the payment reference (BR-ADM-06), and emit events (`subscription.activated`, `subscription.renewed`, `subscription.expiring`, `subscription.lapsed`, `subscription.reinstated`). |
| BR-LIC-07 | Entitlement checking lives in exactly one package and its result is carried on the request context. Scattered expiry checks are prohibited (DRY §6.1). |
| BR-LIC-08 | Entitlement is evaluated per request from cached subscription state, refreshed on change. It MUST NOT add a network call or a cross-service dependency to a request. |

---

## 2. Entitlements

| Entitlement | Meaning |
|---|---|
| `plan` | Named tier, for support and reporting |
| `valid_from` / `valid_until` | The subscribed period |
| `max_tills` | Concurrent counter sessions |
| `max_staff` | Active staff accounts |
| `features` | Named capabilities: campaigns, loyalty, forecasting, storefront, multi-location |

| ID | Rule |
|---|---|
| BR-LIC-20 | `[MONEY]` A limit refuses the **next** thing, never the existing ones. Exceeding `max_tills` blocks opening another counter session; it never closes one mid-sale, because that would stop a shop trading over a licensing matter. |
| BR-LIC-21 | An unlicensed feature is **hidden, not broken**. It does not appear in the interface and its endpoints refuse with a clear licensing reason — never a generic 403, which sends the owner to support with the wrong question. |
| BR-LIC-22 | `[MONEY]` Data created under a feature stays intact when the feature lapses. Loyalty balances, campaign history and forecast data are the shop's records, not the vendor's leverage. |
| BR-LIC-23 | Entitlement checks reuse the authorization pattern: one primitive, returning an error, checked in middleware and re-asserted in the service (SEC-09, SEC-10). Licensing is a second axis alongside RBAC, never a replacement for it. |
| BR-LIC-24 | `[SEC]` A lapsed subscription MUST NOT weaken any security control. Licensing gates features; it never gates authentication, authorization, audit or rate limiting. |

---

## 3. Non-payment — what actually happens

**This is the section that matters, and it is a commercial commitment rather than a technical mechanism.**

JetBrains' perpetual fallback — keep the version you paid for, forever — does not translate to hosted software. There is no local copy to keep running, and pretending otherwise would be a promise the architecture cannot keep. The honest hosted equivalent is a generous grace period plus an unconditional guarantee about the shop's data.

| Stage | When | Behaviour |
|---|---|---|
| **Reminder** | 30, 14, 7, 3, 1 days before expiry | Banner to owner and managers; email to the owner. Everything works. |
| **Grace** | `valid_until` to +14 days | Everything still works, including selling. Prominent, unmissable warning on every admin screen and at the counter. |
| **Restricted** | After grace | **New sales and new orders are refused.** Everything else continues: fulfil existing orders, take deliveries, process returns and refunds, reconcile payments, read and export all data, close the books. |
| **Dormant** | After 90 days restricted | Read and export only. |

| ID | Rule |
|---|---|
| BR-LIC-30 | `[MONEY]` **The system is never hard-locked.** There is no state in which an owner cannot reach their own orders, customers, stock records or accounts. A billing dispute must not become a business's data being held hostage. |
| BR-LIC-31 | `[LEGAL]` **Data export works in every state, including dormant.** Invoices, orders, customers, stock and the audit log export in a machine-readable format at any time, without contacting support and without payment. This is decent practice, and for records the shop must retain for seven years it is effectively an obligation (BR-DAT-01, BR-DAT-02). |
| BR-LIC-32 | `[MONEY]` Restriction never strands an in-flight transaction. An order already placed can be fulfilled, refunded and invoiced; a payment already taken can be reconciled. Money already earned is never made unreachable. |
| BR-LIC-33 | A shop in grace, restricted or dormant state is never silently degraded. The state, the reason and the remedy appear on every admin screen and at the counter. An operator must never be left guessing why a sale was refused. |
| BR-LIC-34 | Payment restores full function **immediately**, with no data migration and no re-setup. |
| BR-LIC-35 | `[LEGAL]` Data is retained through dormancy and is deleted only on the owner's explicit instruction, or after a stated retention period with prior written notice. It is never deleted as a consequence of non-payment alone. |
| BR-LIC-36 | The grace period is 14 days rather than a token few. A retailer's counter is how they take money, and an administrative slip — a failed card, an owner travelling — must not close it. |

---

## 4. The vendor's kill switch

An earlier draft claimed the vendor could not remotely disable an installation. **Under hosting that is no longer true**, and stating it would be a comfortable fiction: the vendor operates the instance and can obviously stop it.

The capability exists, so it needs governance rather than denial.

| ID | Rule |
|---|---|
| BR-LIC-40 | `[SEC]` Suspending a tenant outside the normal billing states (§3) is an **exceptional action**, permitted only for a documented cause: a legal order, credible fraud, or abuse that endangers other customers. |
| BR-LIC-41 | `[SEC]` Suspension requires **two named vendor staff**, records the cause, and is audited immutably (BR-ADM-04, BR-ADM-05). It MUST NOT be available as a routine support action or a collections tactic. |
| BR-LIC-42 | `[LEGAL]` Even under suspension, **data export remains available** to the owner (BR-LIC-31). There is no state in which a shop's own records are withheld. |
| BR-LIC-43 | The owner is notified of a suspension, with the cause and the route to appeal, unless a legal order forbids it. |
| BR-LIC-44 | Suspensions are reviewed and reported. A capability that is never audited becomes a capability that is quietly used. |

---

## 5. Vendor side

| ID | Rule |
|---|---|
| BR-LIC-50 | Issuing an activation code is a vendor operation with its own audit trail: who issued, to which tenant, for what term and plan, against which payment. |
| BR-LIC-51 | `[SEC]` Vendor administrative access to a tenant's instance is itself audited, and appears in that tenant's audit log. A customer must be able to see when the vendor looked at their data. |
| BR-LIC-52 | Subscription state, upcoming expiries and lapsed tenants are reported to the vendor, so a shop about to lose its counter is contacted before it happens rather than after. |
| BR-LIC-53 | `[SEC]` Billing data and shop operational data are separated. The billing system needs the subscription state; it does not need the shop's orders or customers. |
