# Steleios — Architecture

Technical design for the Steleios commerce platform.
Companion to [02-features-and-business-rules.md](02-features-and-business-rules.md), which owns the functional spec.

Status: draft · 3 September 2026 — revised after ADRs 0006–0008

---

## What Steleios is

**A hosted, multi-tenant SaaS that runs a shop's counter, stock and books, and its online storefront.** Sold by subscription to shop owners.

Three decisions shape everything below, and each removed more than it added:

| | Decision | Consequence |
|---|---|---|
| [ADR 0006](decisions/0006-fully-online.md) | **Fully online.** Nothing installed at the shop. | No offline selling, no stock leases, no sync, no local storage. Licensing collapses to a database row. |
| [ADR 0007](decisions/0007-multi-tenant-saas.md) | **Multi-tenant.** `client → group → shop`. | Isolation is per **shop**, enforced by PostgreSQL row-level security. |
| [ADR 0008](decisions/0008-no-payment-processing.md) | **Records payments, never processes them.** | No gateway, no card data, no PCI scope. Reconciliation is the primary financial control. |

---

## 1. Stack

| Layer | Choice |
|---|---|
| Backend | Go — `chi/v5` router, `pgx/v5` + `sqlc`, `goose` migrations, `log/slog` |
| Frontend (storefront) | Vue 3 + Pinia, SSR via Nuxt 3 — see §6 |
| Frontend (admin, counter, delivery) | Vue 3 + Pinia, Vite SPA |
| Tooling | Bun (install, scripts, test), Vite, TypeScript 7 |
| Database | PostgreSQL — sessions, cart, queue, rate limiting and idempotency included ([ADR 0005](decisions/0005-hosted-instances-local-tills.md)) |
| Tenant isolation | PostgreSQL row-level security, `FORCE`, non-superuser app role |
| Payments (shop → customer) | **None.** Recorded, never processed (ADR 0008) |
| Payments (owner → vendor) | Razorpay Subscriptions — the platform subscription only, on the billing boundary (docs/09 §4A) |
| Currency | INR, `int64` paise |

**Deliberately absent.** No ORM — commerce queries get complex and should be readable in review. No `float64` anywhere near money. **No payment gateway, no card data, no payment credentials of any kind.** No Redis at current scale: one datastore means one backup to get right, and the ports are narrow enough that adding it later is an adapter rather than a rewrite (`SessionStore`, `RateLimiter`, `Cache`). No microservices: one `api` binary, one `worker` binary, clean package seams so a split stays possible.

---

## 2. Request spine

The standard layering — routes → middleware → services → repositories — with two additions: a signature-verified lane that bypasses session auth, and an async lane so nothing slow sits in the request path.

```
CLIENTS   storefront (Nuxt)  admin (SPA)  counter (SPA)  delivery (SPA)   couriers
                 │                │            │              │              │
                 ▼                ▼            ▼              ▼              ▼
ROUTES                    chi  /api/v1/*                            POST /webhooks/*
                 │                                                           │
                 ▼                                                           ▼
MIDDLEWARE  request-id → slog → recover → CORS → rate-limit(ip) →    raw-body buffer →
            session → TENANT → RBAC → ownership →                    HMAC verify
            rate-limit(actor) → validate → idempotency               (no session, no CSRF)
                 │                                                           │
                 └────────────────────────────┬──────────────────────────────┘
                                              ▼
SERVICES   catalog · cart · pricing · inventory · order · document ·
           delivery · custody · payment-record · reconciliation ·
           shipping · notify · audit · reporting
                                              │
                                              ▼
REPOSITORIES              sqlc + pgx              queue enqueue
                                              │
                                              ▼
STORES     PostgreSQL — data, sessions, cart, queue, rate limits, idempotency
                        row-level security scoped to the current SHOP
                                              │
                                              ▼
WORKERS    invoice render · notifications · statement import ·
           reservation sweeper · custody ageing · outbox relay
```

Two things in that chain carry most of the weight:

**`TENANT`** resolves the current shop from the session and sets it transaction-locally, so every query below it is confined to that shop by the database itself. The tenant never comes from a request field (ADR 0007).

**The webhook lane** carries no session and no CSRF token — its only authentication is the HMAC signature over the **raw** body, so body buffering is registered *before* any JSON decoding. No payment provider uses it now; courier tracking is its next legitimate consumer. The exemption is declared explicitly in the router with a comment stating why.

---

## 3. Package layout

```
cmd/
  api/main.go              // HTTP server
  worker/main.go           // asynq consumer — same services, no router
internal/
  catalog/  inventory/  cart/  pricing/  order/  payment/
  shipping/ identity/   notify/ audit/   reporting/
      // each: model.go  service.go  repository.go  handler.go  *_test.go
  platform/
    postgres/  redis/  httpx/  logging/  config/  money/
migrations/                // goose, checked in, forward-only
web/
  storefront/  admin/      // two apps, shared api-client + ui packages
docs/
```

Package-per-domain with interfaces at the service boundary. `platform/` holds no business rules. Handlers validate request shape; services enforce meaning; repositories hold no logic beyond query execution and mapping.

---

## 4. Domain model — the shapes that are expensive to change

Five shapes are painful to alter after launch: variants, inventory, order line snapshots, the payment ledger, and the webhook ledger. Get these right in Phase 1 and the rest is ordinary CRUD.

### 4.1 Catalog

```sql
create table uoms (                    -- reference data, seeded by migration (BR-UOM-19)
  code       text primary key,        -- 'GRAM', 'KG', 'ML', 'LTR', 'MM', 'PCS', 'BOX'
  dimension  text not null check (dimension in ('mass','volume','length','area','count')),
  uqc        text not null,           -- GST Unique Quantity Code: KGS, GMS, LTR, NOS...
  symbol     text not null
);

create table products (
  id            uuid primary key default gen_random_uuid(),
  slug          text not null unique,
  title         text not null,
  status        text not null check (status in ('draft','active','archived')),
  hsn_code      text,                 -- GST classification, needed on the invoice
  gst_rate_bps  int  not null,        -- 500 = 5%, 1800 = 18%. Never a float.
  base_uom      text not null references uoms(code),  -- stock-keeping unit (BR-UOM-02)
  created_at    timestamptz not null default now()
);

create table variants (
  id            uuid primary key default gen_random_uuid(),
  product_id    uuid not null references products(id) on delete cascade,
  sku           text not null unique,
  options       jsonb not null,        -- {"size":"M","colour":"olive"}
  price_paise   bigint not null check (price_paise >= 0),   -- per SALE unit (BR-UOM-09)
  mrp_paise     bigint,                -- legal metrology: MRP must be displayed
  sale_uom      text not null references uoms(code),
  base_per_sale bigint not null check (base_per_sale > 0),  -- integer factor (BR-UOM-03)
  unit_weight_mg bigint not null default 0,  -- per BASE unit; shipping weight derives (BR-UOM-16)
  unique (product_id, options)
);
```

Options as a constrained `jsonb` with a unique index — not a wide table of nullable size/colour columns, and not an unqueryable blob.

`sale_uom` and `base_per_sale` are what let three pack sizes draw on one pool of grams. The factor is an integer, and the base unit is chosen fine enough that it always is (BR-UOM-02/03) — the same discipline as paise, for the same reason. `sale_uom.dimension` must equal `product.base_uom.dimension`, enforced at save time and mirrored by distinct Go types so a mass can never be assigned to a volume (BR-UOM-04).

### 4.2 Inventory

```sql
create table inventory (
  variant_id  uuid primary key references variants(id),
  on_hand     int not null check (on_hand >= 0),
  reserved    int not null default 0 check (reserved >= 0),
  check (reserved <= on_hand)
);
-- "available" is on_hand - reserved. Computed on read, never stored.

-- Reserve atomically. Zero rows returned = insufficient stock. No read-then-write.
update inventory
   set reserved = reserved + $2
 where variant_id = $1
   and on_hand - reserved >= $2
returning on_hand - reserved as available;
```

Reservations expire; a sweeper on asynq releases anything past `expires_at` that never reached payment. This is what stops overselling when two customers buy the last unit simultaneously.

```sql
create table stock_reservations (
  id           uuid primary key,
  cart_id      uuid not null,
  variant_id   uuid not null references variants(id),
  qty          int not null check (qty > 0),
  expires_at   timestamptz not null,
  released_at  timestamptz
);
create index on stock_reservations (expires_at) where released_at is null;
```

### 4.3 Order lines — snapshot everything

```sql
create table order_lines (
  id                uuid primary key,
  order_id          uuid not null references orders(id),
  variant_id        uuid not null references variants(id),  -- reporting only
  sku               text   not null,   -- ┐
  title             text   not null,   -- │ snapshots taken at purchase.
  unit_price_paise  bigint not null,   -- │ An order is a historical record; it must
  gst_rate_bps      int    not null,   -- │ never re-read live product data.
  qty               int    not null check (qty > 0),
  tax_paise         bigint not null
);
```

If an order page joins live to `products`, a price edit silently rewrites last month's invoices.

### 4.4 Payment and webhook ledgers

```sql
create table payments (
  id             uuid primary key,
  order_id       uuid not null references orders(id),
  provider       text not null default 'razorpay',
  rzp_order_id   text not null unique,
  rzp_payment_id text unique,
  method         text,               -- upi | card | netbanking | wallet | cod
  amount_paise   bigint not null,
  fee_paise      bigint,             -- from settlement import
  status         text not null,      -- created|authorized|captured|failed|refunded
  raw            jsonb not null default '{}'
);

create table webhook_events (
  id           text primary key,     -- Razorpay event id — the idempotency key
  event        text not null,
  payload      jsonb not null,
  received_at  timestamptz not null default now(),
  processed_at timestamptz
);
```

`INSERT ... ON CONFLICT DO NOTHING` on `webhook_events`. Zero rows inserted means the event was already handled: return 200 and stop.

### 4.5 Order state machine

```
draft ─> pending_payment ─> paid ─────> packed ─> shipped ─> delivered
           │                  │                                 │
           │                  ├─> cancelled                     └─> returned ─> refunded
           ├─> payment_failed │
           ├─> expired        └─> awaiting_stock ─> packed
           └─> confirmed  (COD path)
```

Encoded as an explicit Go transition table with an allowed-from set per target — not scattered `if status ==` checks. Every transition writes an audit row: actor, from, to, reason, request ID.

---

## 5. Money without a gateway

Steleios never touches money (ADR 0008). Payment happens through the shop's own cash drawer, UPI handle and card terminal; the system records what was paid and reconciles it.

```
1. Order placed          Stock reserved, price and tax computed server-side.
   (storefront)          Goods leave on a DELIVERY CHALLAN, not an invoice.
                         Custody transfers to the named delivery person.

2. At the door           Goods photographed. Damaged or refused lines are removed
                         from the pending invoice before it exists.

3. Acceptance            TAX INVOICE issued - numbered, immutable, final lines only.
                         The refused lines were never billed, so nothing to credit.

4. Payment               Customer pays the invoice total to the SHOP's UPI.
                         Recorded with its reference. Order -> paid_unverified.
                         Custody and risk transfer to the customer.

5. Verification          A DIFFERENT person confirms the credit arrived.
                         Order -> paid. Whoever takes a payment never confirms it.

6. Reconciliation        Statement import matches credits to recorded payments.
                         Unmatched past 3 working days -> exception, with its value
                         and the operator who recorded it.
```

**Nothing in this system verifies a payment at the moment of sale.** Reconciliation is therefore not an audit afterthought — it is the primary financial control of the entire product, and a launch requirement rather than a later phase.

### What this buys, and what it costs

**Buys:** no card data ever, no PCI scope of any size, no gateway credentials to hold or leak, and no customer money in flight through the vendor's software. The worst a bug can do to a payment is **record it wrongly**, which is recoverable, rather than **take it wrongly**, which is not.

**Costs:** an online store without online payment converts worse than one with it. That is a real commercial trade, named rather than discovered (ADR 0008).

### India specifics that shape the schema

**Tax and invoicing.** GST splits by place of supply: intra-state to CGST + SGST, inter-state to IGST. `place_of_supply` is stored on the order at placement. HSN code per product and the per-line GST breakdown are invoice requirements, so they belong on the snapshot. MRP display is a legal metrology requirement, hence `mrp_paise` beside `price_paise`.

**Invoice numbering** is gapless per series per shop, allocated only on issue. A cancelled invoice keeps its number and is marked cancelled; deleting it would leave a gap, and a gap is what an auditor asks about first.

**Invoice timing** differs by channel: at the counter the invoice issues at the sale; on delivery it issues at the door after acceptance. The latter relies on treating the consignment as sent on approval, which **must be confirmed with the tax advisor before launch** (docs/02 §9A.3).

**The inward side matters as much as the outward one.** Purchase invoices are recorded against goods receipts, matched to what actually arrived, and reconciled against GSTR-2B — credit a supplier never filed is not credit the shop can claim.

## 6. Frontend and session model

### The rendering decision

A Vite SPA cannot rank product pages — Google renders JavaScript inconsistently, and organic search is usually the largest acquisition channel for a store like this. The fix is cheap now and expensive at Phase 4:

- **Storefront on Nuxt 3 with SSR** — still Vue 3, still Pinia, still Vite underneath.
- **Admin and reporting as a plain Vite SPA** — SEO is irrelevant there and a heavier bundle is fine.

Two apps, one shared API client and UI package.

### Auth

Opaque session ID in an `HttpOnly; Secure; SameSite=Lax` cookie, session body in Redis at `sess:{id}`. Not a JWT in `localStorage` — that is XSS-readable and cannot be revoked. Server-side sessions make revocation, sign-out-everywhere and role changes trivial.

CSRF: double-submit token echoed in a header on every state-changing request. `SameSite` alone is not sufficient. Argon2id for passwords; phone OTP as a first-class login path.

### Cart

Server-authoritative, keyed by a cart-ID cookie so guests keep their cart. Hot copy in Redis, durable copy in PostgreSQL. Guest cart merges into the customer cart on login (sum quantities, clamp to available). Guest checkout must exist — forced account creation is the single biggest conversion leak in checkout.

### Pinia

`useCart`, `useCatalog`, `useAuth`, `useCheckout` — each thin, holding server responses and UI state. API calls live in composables over a generated client, not inline in components. **No pricing, tax or discount arithmetic in the frontend**; display what the server returned.

### Reporting

Aggregation endpoints backed by materialized views refreshed on an asynq cron — never ad-hoc analytical queries against live order tables. Server-side date ranges and pagination; the browser never receives a year of order rows.

### Redis key map

```
sess:{id}              session body            TTL 30d, sliding
cart:{id}              hot cart                TTL 30d
rl:{route}:{actor}     token bucket            TTL window
idem:{key}             idempotent response     TTL 24h
cat:product:{slug}     rendered product JSON   explicit invalidation on publish
asynq:*                queues, owned by asynq
```

One namespace per concern with a stated TTL. Catalog cache is invalidated on write, not left to expire — a stale price is a support ticket.

---


## 7. Build order

Sequenced so that each phase depends only on the ones before it, and so that **the earliest possible phase puts a real shop on the system**. The organising question is not "what is the product" but "what is the smallest thing a shop will pay for and use every day".

The answer is not the storefront. It is **the counter and the stock**. A shop can live without a website; it cannot live without a till that knows what it has.

### The critical path to one live shop

**P0 · Spine** — ✅ *complete*
Config, structured logging, the middleware chain with policy-gated routing, PostgreSQL pool and unit of work, migrations, health and readiness, the error envelope, RBAC, CI. Multi-tenant row-level security proven with cross-tenant tests.
*Ships: a deployable service that does nothing, correctly — with authorization, rate limiting, audit and tests already wired.*

**P1 · Tenancy, identity and onboarding** — *sign-in complete; onboarding in progress*
Client, shop and group provisioning. Identities, memberships, shop switching, password and phone-OTP login, sessions, the roles from docs/02 §15. Subscription state and entitlement checks.
*Ships: a real shop can be created and its owner can sign in. Nothing to sell yet — but every later phase needs this, and building it later means retrofitting a tenant into working code.*

Done: the schema through migration 00017, and `internal/identity` — sign-in with lockout and timing defence, session issue and revocation, shop selection, password change, and the session resolver every authenticated route in the system runs through. `GET /api/v1/auth/csrf`, `POST /login`, `GET /me`, `POST /shop`, `POST /password`, `POST /logout`, `POST /logout-everywhere`.
Remaining: client registration and onboarding services, OTP for contact changes, subscription state and entitlement checks.

**P2 · Catalog, UoM and stock**
Products, variants, options, media pipeline to object storage, categories, typed attributes with allergens and provenance. Base and sale units with integer conversion. Suppliers, goods receipts, **batches with expiry**, and the FEFO allocation statement.
*Ships: a shop can put its stock into the system and see what it has. This is the phase that makes Steleios different from a generic till, and it is where the schema decisions are expensive to change.*

**P3 · Counter sales — the first revenue-earning phase**
Code resolution by scan or keypad, the batch chooser, cart and pricing with GST, the order state machine, invoice issue and numbering, payment **recording**, receipt printing.
*Ships: **a shop can trade on Steleios.** This is the milestone that matters: from here, everything else is improvement rather than prerequisite.*

**P4 · Reconciliation and the books**
Statement import and matching, the exception queue, `paid_unverified` → `paid`, purchase invoices against receipts, credit and debit notes, receivables and payables ageing, GSTR-1 and 3B figures, accounting export.
*Ships: the shop's books balance. **Not deferrable** — without it nobody can tell whether recorded money actually arrived, which is the whole risk that comes with recording rather than processing payments (ADR 0008).*

> **P0–P4 is the product.** A shop running these has a till, stock control with batches and expiry, and books that reconcile. Everything after this point is additional business, not a missing foundation.

### After the shop is live

**P5 · Storefront**
Nuxt SSR product and category pages, structured data, sitemaps, cart, order placement with UPI or payment-on-delivery. Stock shared with the counter from one pool.
*Ships: the online channel. Its value depends on P2's catalog being properly maintained, which is why it comes after.*

**P6 · Delivery**
Delivery challans, delivery mode with custody, proof-of-delivery capture, doorstep damage adjustment, invoice at the door, the maker-checker payment confirmation, custody ageing.
*Ships: fulfilment of storefront orders. Substantial — it carries custody, evidence and a two-person control — and it is worthless before P5 gives it orders to deliver.*

**P7 · Operations depth**
Returns both directions, RTV, courier integration and tracking, stock adjustments with reasons, the per-order event and audit timeline for support.
*Ships: running the business without a database client open.*

**P8 · Intelligence and growth**
Demand forecasting corrected for stockouts, replenishment suggestions capped by shelf life, near-expiry markdown, discounts, campaigns, loyalty, reviews, reporting dashboard.
*Ships: the levers you pull once the pipeline works. Every one of these is specced, and **not one of them should be built before a shop is trading daily on P0–P4.***

### What is deliberately not in the sequence

| Not built | Why |
|---|---|
| Payment gateway integration | ADR 0008 — recorded, never processed |
| Offline selling, stock leases, sync | ADR 0006 — fully online |
| Signed licence tokens, clock anchors | ADR 0006 — a subscription is a database row |
| Local installers and updaters | ADR 0006 — nothing is installed at the shop |
| Cross-shop reporting | ADR 0007 — a separate system, later, on exported data |

### Sequencing judgements worth arguing with

- **Reconciliation (P4) is on the critical path, not in "operations".** With no gateway confirming anything, it is the only mechanism that turns a recorded payment into a known fact. Shipping P3 without it would mean a shop trading with no way to know whether the money arrived.
- **The storefront comes after the counter.** It is the more exciting half and the weaker business case: a shop's daily pain is stock and the till. It also depends on catalog data that only becomes trustworthy once someone maintains it daily.
- **Delivery (P6) is bigger than it looks.** Custody, evidence, doorstep adjustment and a two-person control are four features wearing one name. It should not be estimated as "add delivery to orders".
- **Forecasting is last on purpose.** It needs a year of honest sales history to be worth anything, and a forecast built on three weeks of data is a confident-looking guess.

### Open decisions still blocking

| Decision | Blocks | Why it cannot be deferred |
|---|---|---|
| **Invoice timing at delivery** — confirm sale-on-approval treatment with the tax advisor | P6 | Determines whether the invoice issues at the door or at dispatch with credit notes. Same machinery, different flow, and it is a compliance question rather than a design one (docs/02 §9A.3). |
| **Single or multi-location stock per shop** | P2 | Multi-location makes the inventory key `(variant_id, location_id)` and puts a location on every movement. Retrofitting rewrites every stock query. |
| **Search: PostgreSQL FTS + `pg_trgm`, or a dedicated engine** | P2 (interface only) | Postgres handles a few thousand SKUs with no new infrastructure. The repository interface must hide which, so only the interface blocks. |
| **GST treatment of loyalty redemption** (BR-LOY-07) | P8 | Tax advice, not a design choice. |

Everything else that was open in earlier drafts is now decided and recorded in `docs/decisions/`.

---

## 9. Testing and CI

Gates run cheapest-first and block merge on failure.

```
1  gofmt · golangci-lint · biome         // seconds
2  go vet · tsc --noEmit                 // seconds
3  go test ./... (table-driven + testcontainers) · bun test · playwright
4  govulncheck · bun audit · gitleaks    // blocks merge on findings
```

- **Go:** table-driven unit tests for pricing, tax, discount allocation, and the state machine. `testcontainers-go` with real Postgres for repositories. `httptest` for middleware, including the negative cases — unsigned webhooks, wrong secret, replayed event ID.
- **Vue:** `bun test` with `@testing-library/vue` for components; stores tested directly.
- **E2E:** Playwright covers exactly one flow at first — browse → cart → Razorpay test mode → paid — then the COD and refund flows.

Every `BR-*` rule in the business rules document needs at least one passing and one failing case, prioritised `[MONEY]` → `[SEC]` → `[LEGAL]` → state transitions → everything else.
