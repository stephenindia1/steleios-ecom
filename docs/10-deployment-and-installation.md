# Steleios — Deployment and Installation

How Steleios is deployed and what a shop actually installs.
Decided in [ADR 0005](decisions/0005-hosted-instances-local-tills.md), building on [ADR 0003](decisions/0003-offline-counter-sales.md) (offline selling) and [ADR 0004](decisions/0004-tenancy.md) (tenancy).

Status: draft · 3 September 2026

---

## 0. What runs where

**The shop installs a till, not a system.**

| | Runs where | Holds |
|---|---|---|
| Storefront, API, workers, database | **Vendor's infrastructure** — one single-tenant instance per shop | Everything |
| **Till client** | **The shop's counter PC** | Its stock lease, its unsynced sales, its printer and scanner |

The counter must survive an internet outage, which is why the till is local. Everything else is hosted, because a shop's PC and broadband cannot serve customers and search crawlers 24/7, and because two systems that can disconnect from each other must then agree about stock — the problem ADR 0003 exists to avoid, at a larger scale and with nobody watching.

Offline selling is handled by the **lease** already designed in ADR 0003: the till carries specific batches, quantities and prices reserved to it in advance, so it keeps selling through an outage without ever overselling.

---

## 1. The hosted instance

One instance per shop, single-tenant (ADR 0004), run by the vendor.

```
steleios-api      HTTP API, admin UI, storefront (Nuxt SSR)
steleios-worker   background jobs
steleios-migrate  schema migrations
PostgreSQL        the only datastore (§2)
```

Go compiles these to static binaries with `CGO_ENABLED=0` — no runtime, no dependency chain, one file each.

| ID | Rule |
|---|---|
| DEP-01 | Each shop gets its **own database**. Isolation is physical, so a query bug cannot reach another shop's data (ADR 0004). |
| DEP-02 | `[MONEY]` **Backups are the vendor's responsibility**: automated, encrypted, offsite, retained per BR-DAT-01, and **restore-tested on a schedule**. A backup nobody has restored is a hypothesis. This is the single largest practical gain from hosting, and squandering it would be worse than never having claimed it. |
| DEP-03 | Instances are upgraded by the vendor, staged and monitored, with rollback. An instance on a perpetual fallback version is **not** upgraded, and the deployment system must support that indefinitely (BR-LIC-39b). |
| DEP-04 | `[SEC]` Production configuration is enforced by `config.Validate`: TLS, secure cookies, JSON logs, no debug level, a real CORS allowlist. An instance that fails validation does not start (HLT-005). |
| DEP-05 | `[MONEY]` **Availability is now a product obligation.** A hosted instance being down means a shop cannot take online orders and its till is running on its lease. Uptime, backup and restore are core commitments, not operational hygiene. |
| DEP-06 | Instance health, licence state, lease utilisation and till connectivity are monitored centrally, so the vendor sees a shop's problem before the shop reports it (doc 06 §5). |

---

## 2. PostgreSQL only

A single-shop instance runs **no Redis**. Sessions, idempotency, rate limiting, the queue and cart hot state all use PostgreSQL.

| Concern | Hosted-at-scale (Redis) | Single-shop instance (PostgreSQL) |
|---|---|---|
| Sessions | `sess:{id}` with TTL | Table with an expiry column, swept |
| Idempotency | `idem:{key}` with TTL | Unique key plus expiry, swept |
| Rate limiting | Atomic `INCR` in Lua | Atomic upsert on a windowed counter row |
| Queue | asynq | `FOR UPDATE SKIP LOCKED` over a jobs table (DB-033) |
| Cart hot state | `cart:{id}` | The durable cart row, which already exists (BR-CRT-10) |

| ID | Rule |
|---|---|
| DEP-10 | The datastore profile is chosen by **configuration**, not by a code branch in domain logic. Domain code depends on `SessionStore`, `RateLimiter`, `Cache` and the queue interfaces, never on a client (RD-000). |
| DEP-11 | Both profiles satisfy the same interfaces and the same tests. A behavioural difference between them is a defect, not a profile characteristic. |
| DEP-12 | `[MONEY]` The PostgreSQL rate limiter and idempotency store fail closed exactly as the Redis ones do (BR-SEC-11, RD-011). |
| DEP-13 | Redis remains supported for a consolidated or high-volume deployment. Adding it is configuration, not migration. |

Redis is correct **at scale**. One shop with a handful of tills has no such scale, and one datastore instead of two means one backup to get right and one thing to monitor — multiplied across every customer.

---

## 3. The till client

The only thing installed in the shop. A static Go binary on the counter PC that serves a local UI in the browser, holds its lease and unsynced sales in an embedded store, and talks to the hosted instance.

| ID | Rule |
|---|---|
| DEP-20 | The till is a **real application**, not a browser tab: it must work with the network down, drive a receipt printer, read a barcode scanner, and survive the PC being switched off mid-sale. |
| DEP-21 | Local durable storage is an embedded database (single file, no service to install or supervise). It holds the lease, unsynced sales and a partly-entered basket — nothing else. |
| DEP-22 | `[SEC]` Local storage is encrypted at rest and holds **no card data and no customer PII** beyond what the receipt requires (BR-OFF-24, BR-CPM-03). |
| DEP-23 | `[SEC]` The till authenticates as a registered device with its own credentials, enrolled against the shop's instance. It is a staff actor, not an anonymous client (BR-OFF-36). |
| DEP-24 | `[SEC]` A lost or stolen till is handled by **revoking its lease and its credentials** server-side, which releases the stock it held (BR-OFF-15). |
| DEP-25 | The till auto-updates from signed packages, with rollback on failure. It must not require a shopkeeper to update it manually. |
| DEP-26 | Installation is one signed installer that registers a service, starts on boot, and ends at an enrolment screen. Nothing else. |
| DEP-27 | Scanner input is accepted as a fast keystroke burst; the numeric keypad path is equally first-class, since most shops have no scanner (BR-SCN-31, BR-SCN-32). |

---

## 4. What installation has to get right

Installing software is the easy part. These decide whether the product survives contact with real shops.

| Concern | Requirement |
|---|---|
| **Enrolment** | The till pairs with the shop's instance using a short code. No configuration files, no URLs typed by a shopkeeper. |
| **Service supervision** | Auto-start on boot, restart on crash. A shop opens at 8am and nobody there knows what a service is. |
| **Time** | NTP configured at install. The clock anchor refuses rollback (BR-LIC-41), and a badly-set clock otherwise becomes a support call that looks like a licensing bug. |
| **Printer and scanner** | Detected and tested during installation, not discovered to be broken at the first sale. |
| **Diagnostics** | One action produces a support bundle: version, licence state, lease state, sync backlog, recent errors — secrets and PII redacted (BR-SEC-07). |
| **Uninstall** | Removes the software, **never unsynced sales**. Uninstalling with sales pending must refuse until they sync, or export them (BR-OFF-34). |

---

## 5. Licensing

Licensing is unchanged (docs/09) and gets simpler under this model: the licence binds to the hosted instance the vendor already operates.

- The owner activates with a short code; the instance exchanges it once for a signed licence and then runs on it for the term (BR-LIC-10, BR-LIC-11).
- Tills enrol against the instance and are counted against `max_tills` (BR-LIC-20). Exceeding it blocks a **new** till; it never disables one already in use.
- A perpetual fallback pins that instance's version (BR-LIC-30), so the deployment system must support an instance that never upgrades again.
- Expiry never hard-locks and never withholds data, in any state (BR-LIC-35, BR-LIC-36).

---

## 6. When a shop truly cannot depend on connectivity

No broadband, mobile-only, or frequent multi-day outages: the honest answer is a full local install. That is a **different product configuration**, not a setting — it reintroduces local backups, local upgrades and a second system that must agree about stock. It should be quoted, built and supported as such, never promised casually to close a sale.
