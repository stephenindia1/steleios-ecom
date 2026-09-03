# ADR 0006 — Steleios is fully online

Date: 3 September 2026 · Status: accepted · Decided by: the owner
**Supersedes [ADR 0003](0003-offline-counter-sales.md)** · amends [ADR 0005](0005-hosted-instances-local-tills.md)

## Decision

**Nothing is installed at the shop.** The counter runs in a browser against the vendor-hosted instance. There is no offline selling.

During an internet outage the counter stops, and the shop falls back to a paper bill book, entering the sales afterwards — which is what most small retailers already do today.

## Why

The trigger was support cost, which for a small vendor is the dominant cost: one shop with a printer that will not print consumes more of a day than ten happy customers pay for. But the decision turned out to be right for three independent reasons.

### 1. Support

Nothing to install, nothing to update, no admin rights, no per-PC variation, no installer that fails on one particular Windows build. "Open the browser" is the entire installation procedure and the first line of every support call.

### 2. The code never leaves the vendor's infrastructure

A locally installed product ships a binary and a database to every customer. Both can be decompiled, inspected and copied, and a determined customer or competitor has everything they need. Hosting removes that surface entirely: there is no artefact to take.

This is not the primary reason, but it is a real one, and it is worth stating because it is a benefit that cannot be recovered later — once binaries have shipped, they have shipped.

### 3. Licensing collapses to something trivial

This is the largest simplification, and it was not obvious until the decision was made.

The offline requirement was what made licensing hard. A licence that had to be verified on a disconnected machine forced: Ed25519-signed tokens, a signing key to protect and rotate, installation binding, clock-rollback detection, offline activation files, a licence server for chains, and revocation that could only take effect at renewal.

**All of it disappears.** With one hosted instance per shop, a subscription is a row the vendor owns. Entitlement is a database read. Revocation is immediate. There is nothing to forge because there is nothing on the customer's side to forge.

## What this deletes from the build

| Removed | Was needed for |
|---|---|
| Stock leases — grant, refresh, expiry, revocation, reclaim | Offline selling |
| Offline sale capture, sync and its idempotency | Offline selling |
| Sync reconciliation queue and conflict resolution | Offline selling |
| Per-till invoice series | Offline invoice numbering without a round trip |
| Till-local encrypted storage and device enrolment | Offline selling |
| Recalled-batch-sold-while-offline exposure | Offline selling |
| Signed licence tokens, signing key management, key rotation | Offline licence verification |
| Clock-rollback detection and the time anchor | Offline licence verification |
| Installation binding and offline activation files | Offline licence verification |
| Licence server for chains | Offline licence verification |

Roughly a third of the remaining specification, and the third with the highest defect risk, since almost all of it sat on the money path.

## What this costs

**An internet outage stops the counter.** That is the whole cost, and it is real.

Mitigation is operational rather than architectural, and cheaper than the engineering it replaces: a **4G hotspot as automatic failover** costs a few hundred rupees a month and fixes connectivity for the storefront, the card terminal and the till at once. Building offline mode would have cost months and carried permanent complexity on the money path.

Where a shop genuinely cannot get reliable connectivity, the honest answer is that Steleios is not yet the right product for them — not a promise to build offline mode for one customer.

## Consequences

- The vendor now carries availability outright. A hosted instance being down is a shop that cannot trade at all, with no lease to fall back on. Uptime and incident response become the core operational obligation (DEP-05).
- The vendor **can** disable a running installation. That capability now exists whether or not it is wanted, so it needs governance rather than a rule saying it does not exist (docs/09 §4).
- **JetBrains' perpetual fallback does not translate to hosted software.** There is no local copy to keep running. Retaining the rule as written would have been a promise the architecture cannot keep, so docs/09 replaces it with the honest hosted equivalent: a generous grace period, and a guarantee of data export that survives any billing state.
- The till is a browser page, not an application. It is no longer its own build phase.
- ADR 0005 stands on hosting, one instance per shop, and PostgreSQL-only. Its "local till client" section is superseded here.

## Revisiting

If offline selling is ever genuinely required, the lease design in ADR 0003 is the answer and is retained for that reason. It should be reintroduced as a deliberate project with its own reconciliation obligations, never bolted on to close a sale.
