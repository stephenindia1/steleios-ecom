# Steleios — Architecture

Technical design for the Steleios commerce platform.
Companion to [02-features-and-business-rules.md](02-features-and-business-rules.md), which owns the functional spec.

Status: draft · 3 September 2026

---

## 1. Stack

| Layer | Choice |
|---|---|
| Backend | Go — `chi/v5` router, `pgx/v5` + `sqlc`, `goose` migrations, `hibiken/asynq` queue, `log/slog` |
| Frontend (storefront) | Vue 3 + Pinia, SSR via Nuxt 3 — see §6 |
| Frontend (admin + reporting) | Vue 3 + Pinia, Vite SPA |
| Tooling | Bun (install, scripts, test), Vite |
| Database | PostgreSQL |
| Cache / sessions / queue / rate limit | Redis |
| Payments | Razorpay (Standard Checkout + Webhooks) |
| Currency | INR, `int64` paise |

**Deliberately absent.** No ORM — commerce queries get complex and should be readable in review. No `float64` anywhere near money. No separate message broker; Redis + asynq is sufficient until it demonstrably isn't. No microservices at launch: one `api` binary, one `worker` binary, clean package seams so a split stays possible.

---

## 2. Request spine

The standard layering — routes → middleware → services → repositories — with two additions: a webhook lane that bypasses session auth, and an async lane so nothing slow sits in the request path.

```
CLIENTS      storefront (Nuxt)   admin + reporting (Vite SPA)   Razorpay servers
                    │                      │                          │
                    ▼                      ▼                          ▼
ROUTES              chi  /api/v1/*                          POST /webhooks/razorpay
                    │                                                 │
                    ▼                                                 ▼
MIDDLEWARE   request-id → slog → recover → CORS →           raw-body buffer →
             session → RBAC → rate limit →                  HMAC verify (webhook secret)
             validate → idempotency                         (no session, no CSRF — by design)
                    │                                                 │
                    └──────────────────────┬──────────────────────────┘
                                           ▼
SERVICES     catalog · cart · pricing · inventory · order · payment ·
             shipping · notify · audit · reporting
                                           │
                                           ▼
REPOSITORIES sqlc + pgx          redis client          asynq enqueue
                    │                  │                     │
                    ▼                  ▼                     ▼
STORES        PostgreSQL           Redis          worker: email · invoice ·
                                                  settlement import · reservation sweeper
```

The webhook lane carries no session and no CSRF token. Its only authentication is the HMAC signature over the **raw** body, so it needs its own route group with body buffering registered *before* any JSON-decoding middleware. That exemption is declared explicitly in the router with a comment stating why (BR-PAY-06).

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

## 5. Razorpay integration

```
1. POST /api/v1/checkout      Go recomputes the cart, reserves stock, creates a local
                              order (pending_payment), calls Razorpay Orders API with
                              amount in paise, currency INR, receipt = order.number,
                              notes = {order_id}. Returns rzp_order_id + public key_id.

2. Vue opens Razorpay         Client passes only rzp_order_id.
   Standard Checkout          The amount never travels from the browser.

3. Browser returns            razorpay_payment_id, razorpay_order_id, razorpay_signature
   verify: hmac.Equal(HMAC_SHA256(order_id+"|"+payment_id, KEY_SECRET), sig)
   → this unlocks the confirmation page only. It does NOT mark the order paid.

4. POST /webhooks/razorpay    X-Razorpay-Signature = HMAC_SHA256(raw body, WEBHOOK_SECRET)
   // WEBHOOK_SECRET is a DIFFERENT secret from KEY_SECRET. Read the raw body before
   // any decoding; compare with hmac.Equal, never ==.

   insert into webhook_events (id) values ($eventID) on conflict do nothing
   → 0 rows?  already processed — return 200 and stop.
   → else: verify captured amount == stored order total,
           convert reservation to decrement, transition order -> paid,
           enqueue invoice + confirmation on asynq, return 200 fast.

5. Events handled             payment.captured · payment.failed · order.paid
                              refund.created  · refund.processed
```

**Return 200 quickly and queue the work.** Razorpay retries on non-2xx, and a slow handler turns one event into a pile of duplicates.

### Security decisions flagged for review

- Two distinct secrets (`KEY_SECRET`, `WEBHOOK_SECRET`) that must not be interchangeable in config.
- Constant-time signature comparison via `hmac.Equal`.
- The webhook route's exemption from session auth and CSRF is intentional and documented in the router.
- Test/live key selection is by deployment environment only — never influenced by a request field.

### India specifics that shape the schema

**Tax and invoicing.** GST splits by place of supply: intra-state → CGST + SGST, inter-state → IGST. `place_of_supply` is stored on the order at placement. HSN code per product and the per-line GST breakdown are invoice requirements, so they belong on the snapshot. MRP display is a legal metrology requirement — hence `mrp_paise` beside `price_paise`.

**Payment behaviour.** UPI dominates and often settles a beat after the customer returns to the site — another reason the callback cannot be the source of truth. COD is a large share of Indian volume and is a real feature: pincode allowlist, order-value cap, phone OTP, and its own state-machine branch. Saved cards go through Razorpay tokenization; card data never reaches Steleios. Subscriptions require RBI e-mandate, so Razorpay Subscriptions rather than an in-house scheduler.

**Reconciliation.** Settlements arrive T+2/T+3 net of fees, so the bank amount never matches order totals. Build the settlement import as a worker job early, or finance will build it in spreadsheets and you'll inherit that.

---

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

Sequenced because each phase depends on the one before it: the spine must exist before services can be tested, and inventory must be correct before money touches it.

**01 · Spine.** Config from env, slog JSON, chi with the full middleware chain, pgx pool, goose migrations, health and readiness, request IDs, one error envelope, Docker Compose with Postgres and Redis, CI green.
*Ships: a deployable service that does nothing, correctly — with logging, rate limiting and tests already wired.*

**02 · Catalog and the read path.** Products, variants, media, admin CRUD, listing with facets, Redis caching with explicit invalidation, Nuxt storefront browsing real data.
*Ships: a browsable catalog. The open decisions in §8 must be settled before this phase's migrations land.*

**03 · Identity and cart.** Redis sessions, password + phone OTP login, RBAC middleware, addresses, guest carts with merge-on-login, server-side pricing with GST isolated and unit-tested.
*Ships: a cart with a total you can trust.*

**04 · Checkout, payment, fulfilment.** Reservations, order state machine, Razorpay orders and webhooks with the event ledger, COD path, invoices, confirmation email on asynq, reservation sweeper.
*Ships: revenue. The phase not to compress — every shortcut here becomes a reconciliation problem later.*

**05 · Operations.** Admin order management, refunds, stock adjustment, courier dispatch and tracking, returns, settlement import, audit log surfaced as a per-order timeline.
*Ships: the ability to run the business without a database client open.*

**06 · Growth and reporting.** Discount engine, reviews, abandoned-cart recovery, materialized views, reporting dashboard.
*Ships: the levers you pull once the pipeline works.*

---

## 8. Decisions required before Phase 2

Everything above is reversible except these. Each changes a migration that will already hold production rows.

| Decision | Why it's blocking |
|---|---|
| Storefront rendering: Nuxt SSR or Vite SPA | Determines whether product pages can rank, and shapes the auth model (SSR needs the session cookie readable server-side). Recommendation: Nuxt. |
| Single warehouse or multi-location stock | Multi-location makes the inventory key `(variant_id, location_id)` and puts a location on every order line. Retrofitting rewrites every stock query. |
| INR only, or multi-currency | Multi-currency means prices carry a currency from the first migration. INR-only is much simpler and correct unless already selling abroad. |
| COD at launch | Adds a branch to the order state machine, an OTP step, and a risk-rules service. Blocks Phase 4. |
| Search: Postgres FTS + `pg_trgm`, or Meilisearch | Postgres handles a few thousand SKUs with no new infrastructure. The repository interface should hide which one you're on — so only the interface blocks Phase 2. |

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
