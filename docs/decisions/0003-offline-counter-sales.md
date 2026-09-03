# ADR 0003 — Offline counter sales, via stock leases

Date: 3 September 2026 · Status: accepted · Decided by: the owner
Supersedes [ADR 0002](0002-online-only-counter-sales.md)

## Decision

**A counter sale must not stop when connectivity drops.** The till keeps selling and syncs when it comes back online.

To make that safe rather than merely possible, the till does not sell from a guess. It sells from a **lease**: a quantity of specific batches, at specific prices, reserved to that till in advance by the server while it was online.

## The problem with the obvious implementation

"Sell offline and reconcile later" is the obvious design and it fails in four ways, all of them normal behaviour rather than edge cases (ADR 0002 sets these out in full):

1. Two tills — or a till and the storefront — sell the same last unit. **Overselling.**
2. A markdown applied during the outage never reaches the till. **Wrong price charged.**
3. The till guesses which batch it sold. **Traceability and recall break.**
4. Every offline sale becomes a case to resolve, and all the resolutions are bad. **Reconciliation debt.**

The decision is to keep offline selling *and* refuse those four outcomes. That requires the till to hold real authority over real stock, not a stale snapshot.

## The design: leases

While online, a till holds a **stock lease** — a set of `(batch, quantity, effective price)` grants with an expiry.

The leased quantity is **already reserved in the shared pool**, exactly as a checkout reservation is (BR-BAT-11). So:

- **Overselling becomes impossible.** The storefront and other tills cannot sell leased units, because those units are `reserved`. An offline till selling within its lease is selling stock nobody else can touch. When the lease is exhausted, offline selling of that variant stops — a refusal, not an oversell.
- **The price is not a guess.** The lease carries each batch's effective price, and a `price_valid_until`. Staleness is bounded and known rather than open-ended.
- **The batch is not a guess.** The lease is granted per batch, so the receipt records the real batch number and expiry. Recall and allergen provenance survive.
- **Reconciliation is arithmetic, not judgement.** On reconnect the till reports what it sold from the lease; the server converts those reservations to decrements and releases the remainder. There is no case to adjudicate.

The cost is that a till can only sell offline what it was leased. That is the right cost: it converts an unbounded correctness risk into a bounded availability limit that a shop can size for itself.

## What still cannot happen offline

- **Card and UPI payment.** These need the payment network; there is no local approval. Offline sales are **cash**, or a card taken on a standalone terminal and recorded with its reference for reconciliation.
- **Selling beyond the lease.** The till refuses and says why.
- **Selling a batch recalled during the outage.** Unavoidable — the till cannot know. Such sales are flagged on sync for customer contact, which is why the lease expiry is short.

## Invoice numbering

GST invoice numbers must be consecutive within a series. An offline till cannot draw from a central sequence, so **each till has its own invoice series** with a pre-allocated block, replenished with the lease. Multiple series are permitted provided each is itself consecutive, so this stays compliant while removing the round trip.

## Configurable per business

Connectivity is not the same everywhere, so the mode is a per-shop setting. All three use the same lease mechanism and the same correctness rules; they differ only in lease size, expiry and how sync is triggered.

| Mode | Behaviour | For |
|---|---|---|
| `online_only` *(default)* | Refuses to complete a sale while offline; no lease granted | Every shop, unless the owner deliberately chooses otherwise |
| `offline_capable` | Sells from its lease when offline, syncs the moment connectivity returns | Shops that are online normally but need resilience to drops |
| `offline_first` | Deliberately runs disconnected, syncs on a configured interval (default hourly) | Poor or metered connectivity, or a deliberate low-bandwidth operation |

**`online_only` is the default, and offline is opt-in.** Offline selling is safe within its bounds, but it is not free: it hands a device real stock authority, accepts a bounded window of price staleness, and accepts that a batch recalled during an outage may still be sold. Those are reasonable trades for a shop that needs them and pointless risk for a shop that does not. So a shop gets the strict behaviour unless its owner deliberately asks for something else — enabling an offline mode requires the `owner` role and is audited.

Changing the mode changes availability and limits. It never changes what is correct: no mode permits selling outside the lease, charging an unleased price, or guessing a batch.

## Bounds

Every one of these is a limit on how wrong things can get, so each is configured, not assumed:

| Bound | Purpose |
|---|---|
| Lease expiry (default 12 hours) | Caps price staleness and recall exposure |
| Maximum offline duration before selling stops | Forces resynchronisation |
| Maximum lease value and units per till | Caps the money at risk in one device |
| Sync deadline after reconnect | Stops a till hoarding unsynced sales |

## Consequences

- The till is a real client with local durable state, not a thin screen. That is a material increase in scope and is its own build phase.
- Lease grant, expiry and reclaim are new inventory operations, and the sweeper must reclaim leases from tills that never return.
- A lost or stolen till holds real stock authority: leases are revocable server-side, and revocation is part of the design rather than an afterthought.

## Revisiting

The design holds only while the lease is the *sole* source of offline sellable stock. Any change that lets a till sell outside its lease reintroduces every failure in ADR 0002, and requires reopening this decision.
