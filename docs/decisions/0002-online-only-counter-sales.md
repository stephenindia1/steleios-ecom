# ADR 0002 — Counter sales are online-only

Date: 3 September 2026 · **Status: superseded by [ADR 0003](0003-offline-counter-sales.md)** on 3 September 2026

> **Superseded.** The owner decided that a sale must not stop when connectivity
> drops, and that offline sales sync on reconnect. ADR 0003 records that
> decision and the design that makes it safe. The analysis below is retained
> because it names the four failure modes ADR 0003 has to answer — and it does
> answer them, by leasing stock to the till in advance rather than letting the
> till guess.

## Decision

**The counter till sells only while it can reach the server.** When it cannot, it says so and refuses to complete a sale. Offline queue-and-forward selling is **rejected**, not deferred.

## Context

Steleios sells the same stock through two channels at once: the storefront and the counter. Stock is tracked per batch, and a sale must pick a specific batch by FEFO, at that batch's effective price — which may carry a near-expiry markdown that differs from its siblings (BR-BAT-31).

Three of the decisions a sale requires can only be made by the server:

1. **Which batch to allocate**, in FEFO order across every batch of the variant (BR-BAT-10).
2. **What price to charge**, because the price belongs to the allocated batch, not to the variant (BR-BAT-31).
3. **Whether the stock is available at all**, which is a single atomic conditional update against a shared pool (BR-BAT-11).

An offline till holds none of that. It holds a stale snapshot.

## Consequences of the alternative

Had we allowed queue-and-forward, the failures would be these — and they are not edge cases, they are the normal behaviour of the design:

- **Overselling.** Two tills offline, or one till and the storefront, sell the same last unit. Neither can see the other's reservation, because a reservation is a row in the database the till cannot reach. Someone's order is cancelled after the fact.
- **Wrong price charged.** A markdown applied while the till was offline is not reflected; the customer is charged the pre-markdown price, or the shop sells at a markdown that has since been withdrawn. Either way the invoice and the ledger disagree.
- **Wrong batch on the invoice.** Batch traceability underpins recall (BR-BAT-25) and allergen provenance (BR-ATR-12). A till guessing the batch produces an invoice that cannot be trusted for either.
- **Reconciliation debt.** Every offline sale becomes a case to resolve on reconnect, and the resolutions are all bad: cancel a completed sale, absorb the price difference, or correct stock after the fact.

The offline capability would buy continuity during an outage. It would cost correctness in exactly the areas — money, stock and traceability — where this platform's rules are strictest. That is not a trade worth making for a retail counter, where the outage is usually minutes and the customer is standing in front of you.

## What we do instead

- The till states plainly that it is offline and retries in the background.
- A partly entered basket is preserved locally so the operator does not re-scan it. **Preserving input is not completing a sale**; only the former happens offline (BR-RCV-41).
- Till connectivity is monitored and alerts past a threshold, so a shop that cannot sell is visible to someone who can act on it (BR-RCV-43).
- Because selling now depends on latency, scan-to-confirm carries a hard 200 ms p95 budget: online-only turns latency into a queue at the counter (BR-RCV-44, BR-SCN-35).

## Revisiting

Reopening this decision requires answering, concretely: how two offline tills avoid selling the same unit, and how a batch markdown applied during the outage reaches the price charged. Without answers to both, the answer stays no.
