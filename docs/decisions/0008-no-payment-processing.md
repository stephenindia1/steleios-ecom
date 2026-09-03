# ADR 0008 — Steleios records payments, it does not process them

Date: 3 September 2026 · Status: accepted · Decided by: the owner
Amends [ADR 0001](0001-toolchain-and-compatibility.md) (Razorpay is no longer part of the stack)

## Decision

**Steleios never touches money.** No payment gateway, no card processing, no settlement API, no payment webhooks.

Payment happens through the shop's own arrangements — cash, the shop's UPI handle, the shop's card terminal, a bank transfer. Steleios **records what was paid, by what method, with what reference**, for accounting and reconciliation.

Razorpay is removed from the stack.

## What this means for each channel

| Channel | How the customer pays | What Steleios does |
|---|---|---|
| **Counter** | Cash, the shop's UPI QR, the shop's card terminal | Records the tender type, amount and reference |
| **Storefront** | Cash on delivery, or the shop's UPI/bank details arranged at confirmation | Takes the **order**, records payment when the shop confirms receipt |

The storefront becomes an **ordering** system rather than a checkout. A customer places an order; the shop confirms it and arranges payment; the payment is recorded when it arrives.

This is how a great many small Indian retailers already sell online, and it is honest about what the system is. But it is a real commercial trade and should be named rather than discovered: **an online store without online payment converts worse than one with it.** If online conversion later matters more than staying out of payments, that is a decision to revisit deliberately — not something to bolt on quietly.

## Why this is a good decision

### It removes an entire class of liability

No card data, ever — not even the tokenised kind. No PCI-DSS scope of any size. No gateway credentials to hold, rotate or leak. No customer money in flight through the vendor's software. The worst a bug can now do to a payment is **record it wrongly**, which is recoverable, rather than **take it wrongly**, which is not.

### It deletes the highest-risk code in the system

| Removed | Was |
|---|---|
| Razorpay orders, capture, refund API | The money path |
| Webhook signature verification, raw-body handling, two distinct secrets | The most security-sensitive code in the codebase |
| Webhook idempotency ledger and delivery retries | Duplicate-charge protection |
| Orphan payment detection and gateway reconciliation | Recovery from a charge with no order |
| Duplicate-capture detection and automated refund flows | Recovery from charging twice |
| Payment provider factory, test/live key selection | Provider abstraction |
| Card tokenisation, saved cards | PCI-adjacent surface |

Nearly all of it sat directly on the money path, where a defect costs a customer real money and costs the vendor its reputation.

### The invariants get simpler and stronger

The two invariants in docs/02 §8A were in tension: retrying protects against loss and risks a double charge; refusing to retry does the reverse.

**That tension is gone.** Steleios cannot double-charge because it never charges. `I1` becomes the shop's concern with its own bank and terminal, not the software's.

`I2` — nothing is lost — remains, and is now the whole job.

## The risk that replaces it

Recording rather than processing does not remove risk; it moves it, and the new risk must be taken as seriously as the old one:

> **A payment recorded that never happened, or a payment received that was never recorded.**

Nothing in the software verifies a payment at the moment of sale, in any channel. **Reconciliation against the bank statement, the UPI settlement and the card terminal batch is therefore the only control**, and it is the primary financial control of the entire product rather than an audit afterthought.

That is already designed (docs/02 §8A.6, `BR-CPM-20` to `BR-CPM-27`) and its scope now widens from the counter to every channel. It is a launch requirement, not a later phase: without it the business has no way to know whether the money it recorded actually arrived.

Two specific exposures, both already covered and both now more important:

- **An unmatched record past three working days** becomes an exception carrying its value *and the operator who recorded it*. This is what catches a mistyped reference, a payment that never arrived, and an operator recording a UPI reference for cash they kept.
- **Orders sit in `paid_unverified` until matched.** Fulfilment proceeds, but revenue reports separate verified from unverified takings, and totalling them as one number is prohibited.

## Consequences

- **The webhook lane stays in the platform**, but no longer serves payments. It is generic signature-verified ingress, and its next legitimate use is courier tracking updates. The policy, the raw-body buffering and the signature verifier remain; the Razorpay specifics go.
- **`webhook_events` stays** as the idempotency ledger for any provider callback, since the discipline is identical whatever the provider.
- **COD stops being a special payment path** and becomes what everything already is: a payment recorded on receipt.
- **The order state machine simplifies.** No `payment_failed` from a gateway, no capture states. An order is placed, then payment is recorded, then reconciled.
- **CLAUDE.md rule 15** — "the browser payment callback is not proof of payment" — no longer applies, because there is no callback. Its successor is stronger and applies everywhere: *no payment is verified by this system at all; reconciliation is the only proof.*

## Revisiting

If online conversion later justifies taking payments, the reconciliation machinery built for this model remains correct and a gateway sits alongside it as a **verified** confirmation source (`BR-CPM-01` already distinguishes `gateway` from `recorded`). The path back is open and was left deliberately.
