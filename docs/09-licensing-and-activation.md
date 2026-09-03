# Steleios — Licensing and Activation

Engraved rules for how a shop is licensed to use the system. **Normative: MUST / MUST NOT.**
Companions: [02-features-and-business-rules.md](02-features-and-business-rules.md) · [06-observability-and-event-logging.md](06-observability-and-event-logging.md).

Status: draft · 3 September 2026

---

## 0. The model

Steleios is **sold to shop owners**. An owner pays the vendor, and the vendor issues an **activation code** that opens the system for a stated period. When the period ends, it must be renewed.

Three properties govern every rule below, and they are in tension:

| | Requirement | Why it is hard |
|---|---|---|
| **L1** | **The licence must work offline.** | The counter can run disconnected (ADR 0003). A licence that needs a call home would stop a shop trading during exactly the outage the offline mode exists to survive. |
| **L2** | **The licence must be unforgeable.** | It is the vendor's revenue. A licence a shop can edit in its own database is not a licence. |
| **L3** | **Expiry must never destroy a business.** | A hard lock at 9am on a Monday takes a shop's ability to trade. That is a disproportionate response to an unpaid invoice, and it is how a vendor loses a customer permanently. |

L1 and L2 together rule out both a simple database flag (forgeable) and a call-home check (fails offline). The answer is a **cryptographically signed licence, verified locally**.

L3 is a business decision as much as a technical one, and §3 states it explicitly rather than leaving it to whoever writes the code.

---

## 1. The licence

| ID | Rule |
|---|---|
| BR-LIC-01 | A licence is a **signed token**: a payload plus an Ed25519 signature over it. The vendor holds the private key; every installation embeds only the public key. |
| BR-LIC-02 | `[SEC]` The signing private key **never** exists in this repository, in any installation, in any build artefact, or in any log. It lives in the vendor's key management system, and issuing a licence is an operation performed there. |
| BR-LIC-03 | `[SEC]` Signature verification uses `crypto/ed25519` from the standard library. No custom cryptography, no home-made obfuscation-as-security (GO-076, GO-077). |
| BR-LIC-04 | The payload carries: licence id, licensee name and identifier, issue date, `valid_from`, `valid_until`, plan, entitlement limits (§2), and the signing key id. |
| BR-LIC-05 | `[SEC]` The payload includes an **installation binding** — the installation's identifier, generated at first run. A licence issued to one shop MUST NOT activate another. |
| BR-LIC-06 | `[MONEY]` Every field that affects entitlement is inside the signed payload. Nothing that grants access is read from an unsigned source: a row a shop can edit is not a control. |
| BR-LIC-07 | Verification happens in exactly one package, `platform/licence`, and its result is carried on the request context. Scattered expiry checks are prohibited (DRY §6.1). |
| BR-LIC-08 | Licence state is cached in memory and re-verified on a schedule and at every startup. Verification is local and cheap; it MUST NOT add a network call to a request. |

### Activation

| ID | Rule |
|---|---|
| BR-LIC-10 | The owner receives a **short, human-typeable activation code** — not the signed token, which is far too long to read over a phone. Activation exchanges the code with the vendor's licence service, once, over TLS, and receives the signed licence. |
| BR-LIC-11 | After activation the installation runs **entirely offline** for the licence term. There is no periodic call home, because there is no connectivity guarantee to depend on (L1). |
| BR-LIC-12 | `[SEC]` An activation code is single-use and bound to the licence it redeems. Redeeming it a second time is refused, and the attempt is recorded by the vendor. |
| BR-LIC-13 | A signed licence file may also be supplied directly, for a shop with no connectivity at all at install time. This is the offline activation path and it is supported, not an afterthought — the same arrangement JetBrains offers for air-gapped machines. |
| BR-LIC-13a | A **licence server** is supported for chains: one on-premises service holds the entitlement and issues short-lived leases to each installation, so a multi-shop owner manages one licence rather than one per till. The lease is itself a signed licence with a short term, so an installation that loses contact with the licence server keeps working until the lease expires (L1). |
| BR-LIC-14 | Renewal replaces the licence. The new licence takes effect on verification; the old one is retained for audit. Renewing early is permitted and simply extends `valid_until`. |
| BR-LIC-15 | `[SEC]` Activation, renewal and any entitlement change are audited with the actor, the licence id and the resulting term (BR-ADM-06), and emit events (`licence.activated`, `licence.renewed`, `licence.expiring`, `licence.expired`, `licence.rejected`). |

---

## 2. Entitlements

The licence states what is included. Limits are enforced, but always in the direction of refusing *new* things rather than breaking existing ones.

| Entitlement | Meaning |
|---|---|
| `valid_from` / `valid_until` | The licensed period |
| `plan` | Named tier, for support and reporting |
| `max_shops` | How many shops this licence covers |
| `max_tills` | Concurrent registered counter devices |
| `max_staff` | Active staff accounts |
| `features` | Named capabilities: offline selling, campaigns, loyalty, forecasting, multi-warehouse |

| ID | Rule |
|---|---|
| BR-LIC-20 | `[MONEY]` A limit refuses the **next** thing, never the existing ones. Exceeding `max_tills` blocks registering another till; it never disables tills already in use, because that would stop a shop trading over a licensing matter. |
| BR-LIC-21 | An unlicensed feature is **hidden, not broken**. It does not appear in the interface and its endpoints refuse with a clear licensing reason — never a generic 403, which sends the owner to support with the wrong question. |
| BR-LIC-22 | `[MONEY]` Data already created under a feature stays intact when the feature lapses. Loyalty points, campaign history and forecast data are the shop's records, not the vendor's leverage. |
| BR-LIC-23 | Entitlement checks reuse the authorization pattern: one primitive, returning an error, checked in middleware and re-asserted in the service (SEC-09, SEC-10). Licensing is a second axis alongside RBAC, never a replacement for it. |
| BR-LIC-24 | `[SEC]` An unlicensed or expired installation MUST NOT weaken any security control. Licensing gates features; it never gates authentication, authorization, audit or rate limiting. |

---

## 3. Expiry — the JetBrains model

The reference model is **JetBrains**: a subscription activated by a code, usable offline once activated, with a **perpetual fallback licence** earned by continuous subscription. It is adopted here because it resolves the L3 tension better than a grace period alone.

### Perpetual fallback

| ID | Rule |
|---|---|
| BR-LIC-30 | `[MONEY]` **After 12 months of continuous subscription, the shop earns a perpetual fallback licence** for the version that was current at the start of that 12-month period. If they stop paying, they keep using that version indefinitely, fully functional — including selling. |
| BR-LIC-31 | The fallback version is **pinned at the moment it is earned** and recorded in the signed payload. Each further month of continuous subscription advances the pinned version by one month, so a long-standing customer's fallback keeps moving forward. |
| BR-LIC-32 | The fallback covers the software as it was. It carries **no updates, no new features and no support** — those are what the subscription buys. |
| BR-LIC-33 | `[MONEY]` A lapse in payment breaks continuity. Resubscribing starts a new 12-month accrual toward the next fallback; the previously earned fallback is never taken away. |

This matters more for a shop than it does for a developer's IDE. A retailer's till is not a tool they can put down for a week — it is how they take money. A model where a lapsed invoice stops a shop trading is one where an administrative slip becomes a lost day's revenue, and it destroys the trust the product depends on. The fallback means the worst case for a customer of a year or more is *running an older version*, not *closing the counter*.

### Before the fallback is earned

For a shop in its first 12 months, or one whose continuity has broken:

| Stage | When | Behaviour |
|---|---|---|
| **Reminder** | 30, 14, 7, 3, 1 days before expiry | Banner to owner and managers; email to the owner. Everything works. |
| **Grace** | `valid_until` to +7 days (configurable) | Everything still works. Prominent, unmissable warning on every admin screen and at the till. |
| **Restricted** | After grace | **New sales and new orders are refused.** Everything else continues: fulfil existing orders, take deliveries, process returns and refunds, read and export all data, close the books. |
| **Dormant** | After 90 days restricted | Read and export only. No writes except those needed to close out obligations. |

### In every state

| ID | Rule |
|---|---|
| BR-LIC-35 | `[MONEY]` **The system is never hard-locked.** There is no state in which an owner cannot reach their own orders, customers, stock records or accounts. A licensing dispute must not become a business's data being held hostage. |
| BR-LIC-36 | `[LEGAL]` **Data export is available in every state, including dormant.** Invoices, orders, customers, stock and the audit log export in a machine-readable format at any time. This is decent practice, and for records the shop must retain for seven years it is effectively an obligation (BR-DAT-01, BR-DAT-02). |
| BR-LIC-37 | `[MONEY]` Restriction never strands an in-flight transaction. An order already placed can be fulfilled, refunded and invoiced; a payment already taken can be reconciled; an offline till can still sync its sales (BR-OFF-30). Refusing a sync would delete revenue the shop has already earned. |
| BR-LIC-38 | A shop in grace, restricted or fallback state is never silently degraded. The state, the reason and the remedy appear on every admin screen and at the till. An operator must never be left guessing why a sale was refused. |
| BR-LIC-39 | Renewal restores full function immediately on verification, with no data migration and no re-setup. |
| BR-LIC-39a | `[SEC]` The vendor cannot remotely disable a running installation. Licensing is a term that expires, not a kill switch — a remote disable capability is an unacceptable risk to the shop and an unacceptable liability for the vendor. |

### Versioning consequence

| ID | Rule |
|---|---|
| BR-LIC-39b | The fallback pins a **version**, so releases must be versioned, dated and independently installable, and an installation must be able to stay on an older version indefinitely. That constrains migrations: a schema change must not require a shop on a fallback version to upgrade in order to keep trading (BR-VER-07). |
| BR-LIC-39c | Security fixes are backported to supported fallback versions for a stated window. A customer running a fallback is still a customer whose shop can be attacked. |

---

## 4. Anti-tamper, proportionately

The licence is worth protecting, but a retail till is not a hostile environment worth an arms race. The aim is to make casual bypass ineffective and deliberate bypass evidently deliberate.

| ID | Rule |
|---|---|
| BR-LIC-40 | `[SEC]` Any change to the stored licence invalidates its signature, so editing the expiry date in the database does nothing except make the licence invalid. |
| BR-LIC-41 | `[SEC]` **Clock rollback is detected.** The installation records the highest timestamp it has ever observed, in the database and independently of the licence. Time moving backwards by more than a small tolerance is refused and alerted — otherwise an offline installation extends its licence indefinitely by setting the system clock back. |
| BR-LIC-42 | `[SEC]` The installation identifier is generated at first run, stored, and included in the signed payload (BR-LIC-05). Copying a database to a second installation produces a binding mismatch, which is refused and reported at the next activation or renewal. |
| BR-LIC-43 | Licence state changes and verification failures emit events and are audited, so a bypass attempt leaves a trace even where it succeeds (doc 06 §3). |
| BR-LIC-44 | Obfuscation is **not** used as a security measure. It costs maintainability and buys nothing against anyone motivated, and the signature is the actual control. |
| BR-LIC-45 | `[SEC]` A verification failure fails **to the restricted state, not to unlicensed-open and not to hard-locked**. A corrupt licence file must not either grant free use or stop a shop trading (BR-SEC-11 applied proportionately). |

---

## 5. Vendor side

| ID | Rule |
|---|---|
| BR-LIC-50 | Licence issuance is a vendor operation with its own audit trail: who issued, to whom, for what term and plan, and against which payment. |
| BR-LIC-51 | `[SEC]` Signing keys are rotatable. The payload carries a key id, and installations hold the current and previous public keys, so a rotation does not invalidate outstanding licences. |
| BR-LIC-52 | `[SEC]` Key compromise has a documented response: rotate, reissue outstanding licences, and publish a revocation list checked at activation and renewal. |
| BR-LIC-53 | Revocation applies at activation and renewal, not continuously — a continuous revocation check would be a call home and would break offline operation (L1). Revoking a licence therefore takes effect at its next renewal, which is the accepted limit of this design. |
| BR-LIC-54 | The vendor's licence service is not part of the shop's installation. A shop's ability to trade MUST NOT depend on the vendor's service being up. |

---

## 6. Open decision — tenancy

**This is the largest remaining architectural fork, and it blocks Phase 2 schema work.**

Selling to shop owners can mean either of two things, and they produce different databases:

| | Model | Consequence |
|---|---|---|
| **A** | **Single-tenant.** Each shop gets its own installation and its own database. The licence activates that installation. | Simplest security boundary — one shop's data is physically separate. Higher operational cost per customer: many deployments to run and upgrade. |
| **B** | **Multi-tenant.** One deployment serves many shops, each a tenant row. The licence covers a tenant. | One deployment to operate and upgrade. But **every table needs a tenant identifier, every query needs a tenant predicate, and every authorization check gains a tenancy dimension** — and one missed predicate leaks one shop's data to another. |

| ID | Rule |
|---|---|
| BR-LIC-60 | This decision MUST be recorded in `docs/decisions/` **before any Phase 2 migration is written**. Retrofitting tenancy onto a live schema means touching every table, every query and every authorization check, and getting one wrong is a data breach. |
| BR-LIC-61 | `[SEC]` If model B is chosen, tenant isolation is enforced at the **data layer** — PostgreSQL row-level security, or a repository layer that cannot construct a query without a tenant predicate. Relying on every developer remembering a `where tenant_id = $1` is not an isolation strategy. |
