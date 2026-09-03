# Steleios — Deployment and Installation

How a shop actually gets Steleios running, given it is licensed per installation (ADR 0004) and must keep trading offline (ADR 0003).

Status: draft · 3 September 2026 — **contains two open decisions**

---

## 0. What "installed locally" has to mean

A shop's till must work when the internet does not (ADR 0003). That forces the counter and back-office to run **on hardware in the shop**, not in a browser tab pointed at a data centre.

But it does not follow that *everything* runs in the shop. Two parts of this system have opposite requirements:

| Part | Must run | Because |
|---|---|---|
| Counter, inventory, back-office | **In the shop** | Must survive an internet outage; must be fast at the till |
| Public storefront | **On the internet** | Customers and search crawlers must reach it 24/7 from anywhere; a shop's broadband and PC cannot serve it, and putting a shop's IP on the public internet is a security liability |

**This is Open Decision A, below.** It is the largest unresolved question in the installation story.

---

## 1. What ships

Go compiles to static binaries with `CGO_ENABLED=0`, so the application itself is trivially installable — three files, no runtime, no dependency hell:

```
steleios-api.exe      the HTTP API and admin/counter UI server
steleios-worker.exe   background jobs
steleios-migrate.exe  schema migrations
```

Everything else is the infrastructure they need.

---

## 2. Installation shapes

### Option 1 — Windows installer (MSI), native services

The shop already has a Windows PC. An MSI installs the binaries, PostgreSQL and Redis, registers them as Windows services set to start automatically, applies migrations, and opens the activation screen.

**For:** familiar to the shop, no virtualisation, survives reboots, one file to hand over.
**Against:** Redis has no supported Windows build — see Open Decision B. PostgreSQL on Windows is fine but the installer must own its lifecycle, upgrades and backups.

### Option 2 — Linux appliance on a mini-PC

The vendor ships (or the shop buys to spec) a small fanless machine with a prepared image: everything preinstalled, auto-starting, unattended-upgrades off, and a single activation step. Tills are browsers or tablets pointing at it over the shop LAN.

**For:** one hardware and software configuration to support instead of every Windows variant a shop might have. No Redis-on-Windows problem. Far fewer support calls, which for a small vendor is the dominant cost.
**Against:** hardware to source and ship; a physical box that can be unplugged, stolen or die.

### Option 3 — Docker Compose

The same `docker-compose.yml` used in development, plus an install script.

**For:** already exists; identical to development.
**Against:** Docker Desktop requires a paid licence for most businesses and needs WSL2 on Windows. For a retail shop this is a heavy, fragile dependency, and diagnosing a Docker problem over the phone with a shopkeeper is not a support model that scales.

**Recommendation: Option 2 for shops that will take it, Option 1 for those that insist on their own PC.** Option 3 stays a development and technical-customer path only.

---

## 3. What the installer must handle

Installing the software is the easy part. These are the parts that decide whether the product survives contact with real shops.

| Concern | Requirement |
|---|---|
| **Backups** | Automated, daily, verified, retained — and **offsite where connectivity allows**. A shop's entire trading history on one PC with no backup is a business-ending event waiting to happen, and it will be blamed on the vendor. Non-negotiable, and it must be on by default rather than an option someone forgets. |
| **Restore** | A documented, *tested* restore. A backup nobody has restored is a hypothesis. |
| **Updates** | Signed, versioned, one command or one button, with automatic rollback on failure. An installation must be able to stay on an older version indefinitely, because a perpetual-fallback customer may (BR-LIC-39b). |
| **Service supervision** | Auto-start on boot, restart on crash. A shop opens at 8am and nobody there knows what a service is. |
| **Local network** | Tills reach the server over the shop LAN by hostname, with TLS. Certificates must not require the shop to understand certificates. |
| **Diagnostics** | One command produces a support bundle: version, licence state, health, recent errors, migration status — with secrets and PII redacted (BR-SEC-07). |
| **Time** | NTP configured. The clock anchor refuses rollback (BR-LIC-41), and a badly-set clock at install time is otherwise a support call that looks like a licensing bug. |
| **Uninstall** | Removes the software, **never the data**. Data is exported first (BR-LIC-36). |

---

## 4. Open Decision A — where the public storefront lives

The counter must be local. The storefront cannot be. Three ways to resolve it:

| | Model | Consequence |
|---|---|---|
| **A1** | **Local only, no public storefront.** Steleios is a POS and inventory system. | Simplest. Loses the entire online sales channel and makes docs/07 (SEO and AI discoverability) moot. |
| **A2** | **Hosted storefront, local counter, syncing between them.** The vendor hosts the storefront; the shop's installation syncs catalog, stock and orders to it. | Keeps both channels. But stock is now shared across two systems that can be disconnected from each other — which is the ADR 0003 problem again, at a larger scale, and needs the same answer: the storefront sells from a **lease** of stock, not from a stale copy. |
| **A3** | **Hosted everything, local till as a cache.** The system is hosted; the till holds a lease and works offline (exactly ADR 0003). | Architecturally the cleanest — one source of truth, offline handled by the mechanism already designed for it. But it is no longer "installed locally", and it needs the shop to have connectivity most of the time. |

**Recommendation: A3, with A2 as the fallback for shops with poor connectivity.** The lease mechanism from ADR 0003 already solves the offline problem, and A3 avoids running two half-systems that have to agree about stock. It also makes updates the vendor's job rather than every shop's.

This needs deciding before Phase 2, because it determines whether the storefront and the counter share a database.

---

## 5. Open Decision B — Redis on a shop PC

Steleios uses Redis for sessions, cart, queue, rate limiting and idempotency (docs/05 §5). On a Linux appliance that is fine. On a Windows install it is a problem: there is no supported native Redis for Windows, leaving Memurai (a commercial licence per installation) or WSL2 (heavy, and another moving part to support).

| | Option | Consequence |
|---|---|---|
| **B1** | Linux appliance only, keep Redis | No problem to solve — folds into choosing Option 2 above |
| **B2** | Windows + Memurai | A per-installation cost and a third-party dependency in the critical path |
| **B3** | **A PostgreSQL-only profile for single-shop installs** | Sessions, idempotency, rate limiting and the queue move to PostgreSQL. One dependency instead of two, one backup instead of two, and materially less to go wrong in a shop. |

**Recommendation: B3.** For one shop with a handful of tills, Redis earns nothing — the load is trivial and PostgreSQL handles all four workloads comfortably. Redis exists in this design for scale that a single shop does not have.

This is cheap to do **because the ports are already narrow**: domain code depends on `SessionStore`, `RateLimiter`, `Cache` and the queue interfaces, never on a Redis client (RD-000). A PostgreSQL implementation is a new adapter behind existing interfaces, not a rewrite — which is the payoff for having drawn those boundaries in the first place.

Redis stays the implementation for a hosted or multi-shop deployment. The profile is chosen by configuration.

---

## 6. Licensing interaction

- Activation happens at the end of installation: the owner types the code, the installation exchanges it once for a signed licence, and then runs offline for the term (BR-LIC-10, BR-LIC-11).
- The installation identifier is generated at first run and binds the licence to this installation (BR-LIC-05). **Restoring a backup onto new hardware must not look like piracy** — the restore path either preserves the identifier or triggers a re-activation the vendor can approve.
- A shop on a perpetual fallback stays on a pinned version, so the updater must support *not* updating, indefinitely (BR-LIC-39b).
