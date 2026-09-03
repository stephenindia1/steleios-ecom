# ADR 0005 — Vendor-hosted instance per shop, local till client, PostgreSQL-only

Date: 3 September 2026 · Status: accepted · Decided by: the owner
Resolves the two open decisions in [docs/10](../10-deployment-and-installation.md)
Builds on [ADR 0003](0003-offline-counter-sales.md) and [ADR 0004](0004-tenancy.md)

## Decisions

1. **The vendor hosts everything.** One single-tenant instance per shop, run by the vendor. Storefront, API, database, workers.
2. **The till is the only thing installed in the shop.** It holds a stock lease and keeps selling through an outage (ADR 0003).
3. **Hosted instances run PostgreSQL only.** No Redis for a single-shop instance.

## What this changes about "installed locally"

The question that started this was whether a shop installs Steleios. The answer turned out to be: **the shop installs a till, not a system.**

| | Runs where | Holds |
|---|---|---|
| Storefront, API, workers, database | Vendor's infrastructure, one instance per shop | Everything |
| Till client | The shop's counter PC | Its lease, its unsynced sales, its printer and scanner |

That is a far better division than a full local install, for reasons that only became clear once both questions were on the table together:

- **One source of truth for stock.** A full local install plus a hosted storefront means two systems that can disconnect from each other and must then agree about stock — the ADR 0003 problem again, at a larger scale and with no counter operator present to see it go wrong. Hosting everything removes the second system.
- **Offline is already solved.** The lease mechanism exists. Extending it to the till is the design that was already written; there is nothing new to invent.
- **Updates become the vendor's job.** Instead of upgrading N shop installations, the vendor upgrades N instances it controls — and can stage, monitor and roll back.
- **Backups become the vendor's job.** This is the largest practical gain. A shop's entire trading history sitting on a shop PC with a backup nobody has ever restored is a business-ending event waiting to happen, and it would be blamed on the vendor regardless of whose responsibility it nominally was. Now it is genuinely the vendor's, done properly, once.

## PostgreSQL-only

Steleios uses Redis for sessions, cart, queue, rate limiting and idempotency (docs/05 §5). Those choices are correct **at scale**. A single shop with a handful of tills has no such scale: the load is trivial, and PostgreSQL handles all five workloads comfortably.

Running one instance per shop makes this matter more than it would otherwise. Halving the infrastructure per instance — one datastore instead of two, one backup to get right, one thing to monitor — multiplies across every customer.

This is cheap **because the ports are already narrow**. Domain code depends on `SessionStore`, `RateLimiter`, `Cache` and the queue interfaces, never on a Redis client (RD-000). A PostgreSQL implementation is a new adapter behind existing interfaces, not a rewrite. That is the payoff for having drawn the boundary in the first place, and it is worth noticing: the discipline that felt like overhead in Phase 1 is what makes this a configuration choice rather than a project.

| Concern | Redis | PostgreSQL-only |
|---|---|---|
| Sessions | `sess:{id}` with TTL | Table with an expiry column, swept |
| Idempotency | `idem:{key}` with TTL | Table with a unique key and an expiry, swept |
| Rate limiting | Atomic INCR in Lua | Atomic upsert on a windowed counter row |
| Queue | asynq | `FOR UPDATE SKIP LOCKED` over a jobs table (DB-033) |
| Cart hot state | `cart:{id}` | The durable cart row, which already exists (BR-CRT-10) |

The profile is chosen by configuration. Redis remains the implementation for a large or consolidated deployment, and the interfaces do not change.

## Consequences

- **The till is a real application**, not a browser tab: a static Go binary on the counter PC serving a local UI, holding its lease and unsynced sales in an embedded store, driving the receipt printer and reading the scanner. It is its own build phase.
- **Connectivity is now the shop's dependency**, not the vendor's. The lease is what makes that acceptable, and the lease bounds are what make it safe (ADR 0003).
- **The vendor carries availability.** A hosted instance being down is a shop unable to take online orders and a till running on its lease. Uptime, backups and restore drills are now core product obligations, not operational hygiene.
- **ADR 0004 is unchanged and now cheaper to honour.** Single-tenant per instance, `tenant_id` present and constant, row-level security written and dormant. Consolidating many shops into one deployment later remains a switch rather than a rewrite.
- **Licensing binds to the hosted instance** (BR-LIC-05), and tills enrol against it as registered devices (BR-OFF-36). Activation gets simpler: the vendor controls the instance, so the code is redeemed once against a system the vendor already operates.

## Revisiting

If a customer genuinely cannot depend on connectivity at all — no broadband, mobile-only, frequent multi-day outages — the honest answer is a full local install, and that is a different product configuration rather than a setting. It should be quoted and supported as such, not promised casually.
