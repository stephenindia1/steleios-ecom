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

## 4A. Collecting the subscription — Razorpay, on the billing boundary only

The owner pays the vendor by **Razorpay**: a subscription for monthly recurring, or a payment link for annual. This is the **only** gateway in the system, and it sits on the vendor↔owner boundary.

> **It is not the shop's payment path.** The shop's own sales are recorded and never processed (ADR 0008). Nothing here touches an order, an invoice, stock or a customer. Conflating the two would reintroduce exactly the risk ADR 0008 removed.

| ID | Rule |
|---|---|
| BR-BIL-01 | Billing lives in its own module, **vendor-scoped, outside tenant data**. It reads and writes subscription state; it has no access to a shop's orders, customers or stock (BR-LIC-53). |
| BR-BIL-02 | `[MONEY]` Monthly billing uses **Razorpay Subscriptions**. Annual may be a subscription or a one-time payment link — annual is simpler and avoids a mandate entirely, so it is the default offer. |
| BR-BIL-03 | `[LEGAL]` Recurring collection in India requires an **RBI e-mandate** — UPI Autopay or a card mandate — with its pre-debit notification and per-transaction limits. This is handled by Razorpay Subscriptions, not by an in-house scheduler, and an in-house recurring charger MUST NOT be built. |
| BR-BIL-04 | `[SEC][MONEY]` Subscription state changes **only** on a signature-verified webhook, never on a browser redirect. The redirect is a UX signal; the webhook is the fact. Same discipline as any provider callback. |
| BR-BIL-05 | `[SEC]` Webhook signatures are verified with `hmac.Equal` over the **raw body**, using the webhook secret — a distinct secret from the API key secret, and not interchangeable in configuration. |
| BR-BIL-06 | `[MONEY]` Webhook handling is idempotent, keyed on the provider's event id in the existing `webhook_events` ledger. Providers redeliver; a duplicate must not extend a term twice. |
| BR-BIL-07 | Events handled: `subscription.charged`, `subscription.pending`, `subscription.halted`, `subscription.cancelled`, `payment.failed`, `refund.processed`. Unrecognised events are acknowledged and recorded, never rejected. |
| BR-BIL-08 | `[MONEY]` A successful charge extends `valid_until` (BR-LIC-05). A failed charge does **not** immediately restrict: it starts the reminder and grace sequence (§3), because a card that expired over a weekend must not close a shop's counter on Monday. |
| BR-BIL-09 | `[SEC]` Gateway credentials are vendor credentials held in the vendor's environment. They are never per-tenant, never exposed to a shop, and never selectable by anything in a request (test/live is chosen by deployment environment alone). |
| BR-BIL-10 | `[SEC][LEGAL]` No card data reaches Steleios, here either. Mandates and instruments live with the provider; the system holds provider identifiers and nothing more (BR-CPM-03). |
| BR-BIL-11 | `[LEGAL]` The vendor issues the owner a **GST invoice** for the subscription. That invoice is the vendor's own outward supply and is entirely separate from the shop's invoice series (BR-DOC-06). |
| BR-BIL-12 | `[MONEY]` The owner sees their billing history, current plan, next charge date and invoices, and can cancel. A subscription that cannot be cancelled without contacting support is a dark pattern. |
| BR-BIL-13 | Billing events (`billing.charged`, `billing.failed`, `billing.mandate_created`, `billing.cancelled`, `billing.refunded`) are emitted per doc 06 §3 and audited. |
| BR-BIL-14 | `[MONEY]` Manual collection — bank transfer against an invoice — remains supported. Early customers are often billed by hand, and the entitlement model must not assume a gateway is in the loop. |

---

## 5. Vendor side

| ID | Rule |
|---|---|
| BR-LIC-50 | Issuing an activation code is a vendor operation with its own audit trail: who issued, to which tenant, for what term and plan, against which payment. |
| BR-LIC-51 | `[SEC]` Vendor administrative access to a tenant's instance is itself audited, and appears in that tenant's audit log. A customer must be able to see when the vendor looked at their data. |
| BR-LIC-52 | Subscription state, upcoming expiries and lapsed tenants are reported to the vendor, so a shop about to lose its counter is contacted before it happens rather than after. |
| BR-LIC-53 | `[SEC]` Billing data and shop operational data are separated. The billing system needs the subscription state; it does not need the shop's orders or customers. |

---

## 6. Scope of responsibility

**Steleios records sales and inventory.** That is the product: what was sold, what was received, what is in stock, what was paid, and the documents that go with them.

It is not an accounting package, not a tax agent, and not a payment processor.

### The client's accountant is the client's relationship

| ID | Rule |
|---|---|
| BR-RSP-20 | The shop **exports its records and gives them to its own chartered accountant.** The vendor provides the export; it has no relationship with, contract with, or responsibility toward that accountant. |
| BR-RSP-21 | There is deliberately **no accountant login**. Granting a third party access would make the vendor a party to an arrangement that is entirely the client's. The owner exports and hands the files over — the same way they would from any other system. |
| BR-RSP-22 | Because the export *is* the provision, it must be genuinely usable rather than a token CSV button: standard soft formats a CA actually works in, complete periods, and every figure traceable to its documents (BR-DOC-60 to BR-DOC-63, BR-DOC-53). An export a CA has to re-key defeats the purpose. |
| BR-RSP-23 | `[SEC]` Exports contain customer and supplier details, so they require an appropriate role and are audited with the row count and the period. What the shop then does with the file is the shop's responsibility (BR-RPT-05). |

Not processing payments removes several real liabilities. It does not remove all of them, and the difference matters enough to write down before it becomes a dispute.

### What the vendor is genuinely not responsible for

| | Why |
|---|---|
| **Handling the shop's money** | It never passes through the system. No PCI scope, no payment-institution role, no funds in flight (ADR 0008). |
| **Filing GST returns** | The system produces the figures; the shop or its accountant files them (BR-DOC-52). Steleios is not a tax agent. |
| **Accounting judgement** | Whether a credit is eligible, how something is classified, what is claimed — these are the accountant's decisions, and the system records rather than decides them. |
| **Whether the money actually arrived** | Reconciliation surfaces the exception; a human resolves it. The system reports the gap, it cannot close it. |
| **What the shop sells, to whom, at what price** | Entirely theirs. |

### What the vendor absolutely is responsible for

> **We generate the records their accounting is built on, and we are the custodian of those records.** "We only keep records" is not a defence when the records are wrong.

| ID | Rule |
|---|---|
| BR-RSP-01 | `[LEGAL][MONEY]` **Correctness of what the system computes.** A wrong GST split, a rate applied from the wrong date, a rounding error, a total that does not match its lines — these are defects, and they land the shop with a wrong invoice or a wrong return. This is why §3 tax rules, `money.Paise` and the single rounding function exist. |
| BR-RSP-02 | `[LEGAL]` **Integrity of the document trail.** Gapless numbering, immutable issued documents, correction only by credit or debit note. A gap in an invoice series is the shop's problem to explain, and it would be our fault (BR-DOC-01 to BR-DOC-05). |
| BR-RSP-03 | `[LEGAL]` **Retention and availability.** We host their books. Seven-year retention, backups that have been restore-tested, and export available in every billing state including dormant (BR-DAT-01, BR-LIC-31, DEP-02). Losing a shop's trading history is not recoverable by them, at any price. |
| BR-RSP-04 | `[MONEY]` **Completeness.** Every sale, return, receipt and payment recorded, with nothing silently dropped. A lost record is worse than a visibly failed one, because nobody goes looking for it. |
| BR-RSP-05 | `[LEGAL][SEC]` **Confidentiality.** Their books, customers and suppliers, held on our infrastructure, isolated per shop and never visible to another client (ADR 0007). |
| BR-RSP-06 | `[MONEY]` **Availability.** A hosted system that is down is a shop that cannot trade, with nothing to fall back on (ADR 0006, DEP-05). |
| BR-RSP-07 | `[LEGAL]` Where the system's output is used for a statutory filing, it must be **traceable to source** — any figure on a return resolves to the documents behind it (BR-DOC-53). An accountant who cannot verify a number will not use it, and should not. |

### How this is stated commercially

| ID | Rule |
|---|---|
| BR-RSP-10 | The terms state plainly: Steleios is a system of record, not an accountant, not a tax agent, and not a payment processor. The shop is responsible for its filings, its accounting decisions and its own reconciliation of exceptions the system raises. |
| BR-RSP-11 | The terms MUST NOT attempt to disclaim responsibility for the correctness, integrity, retention or confidentiality of the records the system itself produces and holds. A disclaimer that broad would be both unenforceable and dishonest, and would misrepresent what the product is for. |
| BR-RSP-12 | `[LEGAL]` Tax-treatment questions the system deliberately does not answer — time of supply at delivery (BR-TAX-03), GST on loyalty redemption (BR-LOY-07) — are flagged to the shop as requiring their advisor's confirmation, not silently defaulted. |
