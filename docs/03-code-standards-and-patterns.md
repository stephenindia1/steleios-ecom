# Steleios — Code Standards and Patterns

Binding engineering standards for the Steleios platform.
Companions: [01-architecture.md](01-architecture.md) (system design) · [02-features-and-business-rules.md](02-features-and-business-rules.md) (functional spec, `BR-*` rules).

Status: draft · 3 September 2026

---

## 0. The four mandates

| Mandate | What it means here |
|---|---|
| **Object-oriented** | Structs are objects with private state and behaviour. Interfaces are contracts. Composition, never inheritance. No package-level mutable state, no free-floating functions holding business logic. |
| **Module-based factory** | Every domain is a self-contained module built by a factory from a single dependency container. Modules mount their own routes and workers. `cmd/api` wires; it never implements. |
| **Middleware and security first** | Security is structural, not discretionary. A route **cannot** be registered without declaring a security policy. The policy zero value is invalid and fails at boot. |
| **DRY always** | Exactly one implementation of each cross-cutting concern, listed in §6. Duplication of *behaviour* is a defect; duplication of *data* for historical integrity is deliberate and protected (§6.3). |

Where these conflict, the order of precedence is: **security → correctness → DRY → elegance.**

---

## 1. Object-oriented Go

Go has no classes and no inheritance. Everything below is what OOP means in this codebase.

### 1.1 Objects are structs with unexported state

```go
package order

// Service is the order aggregate's behaviour. All state is private.
type Service struct {
    repo      Repository
    inventory InventoryPort
    pricing   PricingPort
    audit     audit.Recorder
    clock     clock.Clock
    log       *slog.Logger
}
```

**Rules**

- **OOP-01** — Struct fields carrying dependencies or state are unexported. Exported fields are permitted only on DTOs and database row structs.
- **OOP-02** — Every object is constructed by a `New*` constructor that validates its dependencies and returns `(T, error)`. The zero value of a service is never usable.
- **OOP-03** — No package-level mutable variables. Configuration, clocks, loggers and clients are injected. `init()` may register a factory (§2.2) and nothing else.
- **OOP-04** — Behaviour lives on methods of the object that owns the state. A function taking a service as its first parameter is a method written in the wrong place.

```go
func NewService(repo Repository, inv InventoryPort, pr PricingPort,
    aud audit.Recorder, clk clock.Clock, log *slog.Logger) (*Service, error) {

    if repo == nil || inv == nil || pr == nil || aud == nil || clk == nil {
        return nil, errors.New("order: nil dependency")   // fail closed at construction
    }
    return &Service{repo: repo, inventory: inv, pricing: pr,
        audit: aud, clock: clk, log: log}, nil
}
```

### 1.2 Interfaces are contracts, declared by the consumer

- **OOP-05** — Interfaces are declared in the package that **uses** them, named for the role they play (`InventoryPort`, `PricingPort`), not the type that satisfies them.
- **OOP-06** — Interfaces stay small. Three methods is a lot. If an interface grows past five, the boundary is wrong.
- **OOP-07** — Every service dependency crossing a module boundary is an interface. Concrete cross-module types are prohibited — that is what makes each module unit-testable with fakes and satisfies the project's "mock the data layer" convention.

```go
// internal/order/ports.go — what the ORDER module needs, expressed on its terms.
type InventoryPort interface {
    Reserve(ctx context.Context, lines []Line, until time.Time) (ReservationID, error)
    Commit(ctx context.Context, id ReservationID) error
    Release(ctx context.Context, id ReservationID) error
}
```

### 1.3 Composition, never inheritance

- **OOP-08** — Shared behaviour is obtained by embedding a collaborator or wrapping an interface (decorator), never by "base struct" hierarchies.
- **OOP-09** — Cross-cutting behaviour on a service — audit, caching, metrics, retry — is a **decorator** implementing the same interface, applied in the module factory. It is never an `if` inside the business method.

```go
// internal/order/decorator_audit.go
type auditedService struct {
    inner Servicer            // same interface
    audit audit.Recorder
}

func (a *auditedService) Cancel(ctx context.Context, id OrderID, reason string) error {
    if err := a.inner.Cancel(ctx, id, reason); err != nil {
        return err
    }
    return a.audit.Record(ctx, audit.Entry{Action: "order.cancel", Resource: id.String(), Reason: reason})
}
```

This is how BR-ORD-06 (every transition writes an audit row) is guaranteed structurally rather than by remembering to call the audit writer in each method.

### 1.4 Domain types, not primitives

- **OOP-10** — Money is `money.Paise`, a defined `int64` type with methods. Passing a raw `int64` or any float into a pricing function must not compile.
- **OOP-11** — Identifiers are defined types (`OrderID`, `VariantID`), not bare `string`/`uuid.UUID`, so arguments cannot be transposed.
- **OOP-12** — Enumerations (order status, payment method, reason codes) are defined string types with an exhaustive `Valid()` method and a parser that rejects unknown values.

### 1.5 TypeScript side

- **OOP-13** — No `any`, ever. `unknown` plus a narrowing guard where a type is genuinely open.
- **OOP-14** — API access is through typed service classes (`OrderService`, `CartService`) built by a factory from one configured HTTP client. Components never call `fetch` and never construct URLs.
- **OOP-15** — Business logic lives in services and composables. Components are presentational. Pinia stores hold state and delegate to services; they contain no arithmetic on money (BR-PRC-01).

---

## 2. Module-based factory pattern

### 2.1 The module contract

Every domain — catalog, inventory, cart, pricing, order, payment, shipping, identity, notify, audit, reporting — is a module satisfying one interface.

```go
// internal/platform/module/module.go
package module

// Module is a self-contained domain unit. It owns its services, its routes
// and its background workers, and exposes nothing else.
type Module interface {
    Name() string
    Routes(g *httpx.Group)          // mounts its own routes, each with a Policy
    Workers(mux *asynq.ServeMux)    // registers its own async handlers
    Health(ctx context.Context) error
}
```

Each module package exposes exactly one constructor, `New`, which is its factory. There is no
generic `Factory` type and no service-locator registry: **Go resolves dependencies at compile
time, at the composition root.** A module's `New` names every collaborator it needs in its
signature, so a missing or mistyped dependency is a build failure, not a startup panic and
certainly not a runtime `nil`.

```go
// Accept interfaces, return concrete types — the caller keeps full type information,
// and only the consumer decides what to abstract.
func New(d *module.Deps, inv InventoryPort, pr PricingPort) (*Mod, error)
```

### 2.2 The dependency container

One container, built once in `main`, passed to every factory. This is the DRY point for infrastructure: no module opens its own database pool, Redis client or logger.

```go
// internal/platform/module/deps.go
type Deps struct {
    Cfg    config.Config
    DB     *pgxpool.Pool
    UoW    postgres.UnitOfWork      // §2.5
    Redis  redis.UniversalClient
    Queue  *asynq.Client
    Log    *slog.Logger
    Clock  clock.Clock
    Audit  audit.Recorder
    Authz  authz.Enforcer
}

func (d *Deps) Validate() error {  // fail closed before any module is built
    // every field required; returns a joined error naming what is missing
}
```

- **MOD-01** — A module receives `*Deps` and nothing else. Reaching for a global, or constructing its own infrastructure client, is prohibited.
- **MOD-02** — `Deps.Validate()` runs before any factory. A missing dependency is a startup failure, never a nil-pointer panic at request time.

### 2.3 A module implementation

```go
// internal/order/module.go
package order

type Mod struct {
    svc     Servicer
    handler *Handler
    worker  *Worker
}

// New is the module factory. Collaborators are named in the signature, so the
// compiler enforces the wiring. It builds the object graph, applies decorators,
// and returns a concrete *Mod. Nothing else in this file is exported.
func New(d *module.Deps, inv InventoryPort, pricing PricingPort) (*Mod, error) {
    repo := newPostgresRepository(d.DB)

    core, err := NewService(repo, inv, pricing, d.Audit, d.Clock, d.Log)
    if err != nil {
        return nil, fmt.Errorf("order: %w", err)
    }

    // Decorators, outermost last. Order is deliberate and documented.
    var svc Servicer = core
    svc = &auditedService{inner: svc, audit: d.Audit}
    svc = &instrumentedService{inner: svc, log: d.Log}

    return &Mod{
        svc:     svc,
        handler: NewHandler(svc, d.Log),
        worker:  NewWorker(svc, d.Log),
    }, nil
}

func (m *Mod) Name() string { return "order" }

func (m *Mod) Routes(g *httpx.Group) {
    g.Mount("/orders", func(g *httpx.Group) {
        g.GET("/{id}",        policy.CustomerOwnedRead, m.handler.Get)
        g.GET("/",            policy.CustomerSession,   m.handler.List)
        g.POST("/{id}/cancel",policy.CustomerOwnedWrite,m.handler.Cancel)

        g.PATCH("/{id}/status", policy.AdminOps,        m.handler.SetStatus)
    })
}

func (m *Mod) Workers(mux *asynq.ServeMux) {
    mux.HandleFunc(TaskExpirePending, m.worker.ExpirePending)
}
```

### 2.4 Assembly — explicit, not magic

The composition root is one function, shared by both binaries. Dependency order is the call
order, which the compiler checks.

```go
// internal/app/build.go — the single composition root.
func Build(d *module.Deps) ([]module.Module, error) {
    cat, err := catalog.New(d)
    if err != nil { return nil, err }

    inv, err := inventory.New(d)
    if err != nil { return nil, err }

    prc, err := pricing.New(d, cat.Catalog())        // returns the port the consumer declared
    if err != nil { return nil, err }

    pay, err := payment.New(d)
    if err != nil { return nil, err }

    ord, err := order.New(d, inv.Reservations(), prc.Quotes())
    if err != nil { return nil, err }

    crt, err := cart.New(d, cat.Catalog(), prc.Quotes())
    if err != nil { return nil, err }

    return []module.Module{cat, inv, prc, pay, ord, crt}, nil
}
```

```go
// cmd/api/main.go
mods, err := app.Build(deps)     // same call in cmd/worker/main.go
if err != nil {
    return fmt.Errorf("build modules: %w", err)
}
for _, m := range mods {
    m.Routes(root.Group(m.Name()))   // worker calls m.Workers(mux) instead
}
```

- **MOD-03** — Wiring is explicit code in `internal/app.Build`, never `init()`-time side effects and never string-keyed lookup. The full set of mounted modules must be readable on one screen — an import that silently mounts routes is a security review failure.
- **MOD-04** — `cmd/api` and `cmd/worker` contain process concerns only: flags, signals, server lifecycle. No business branching, no SQL, no HTTP handling.
- **MOD-05** — Both binaries call the **same** `app.Build`. The worker calls `Workers()` and skips `Routes()`. There is one object graph definition, not two.
- **MOD-06** — A module exposes its ports as small accessor methods returning the consumer's interface (`inv.Reservations()`). Importing another module's concrete service, repository or handler type is prohibited and enforced by a `depguard` rule.
- **MOD-07** — Module dependencies form a directed acyclic graph. A cycle means the boundary is wrong; extract the shared concept into its own module rather than introducing a locator or a callback to break it.

### 2.5 Unit of Work — transactions across modules

Checkout touches inventory, orders, payments and audit atomically (BR-CHK-01). Passing `pgx.Tx` between modules would leak the data layer everywhere, so a Unit of Work factory produces transaction-scoped repositories.

```go
// internal/platform/postgres/uow.go
type UnitOfWork interface {
    Do(ctx context.Context, fn func(Repos) error) error   // commits, or rolls back on any error
}

// Repos is the factory output: every repository bound to the same transaction.
type Repos struct {
    Orders    OrderRepo
    Inventory InventoryRepo
    Payments  PaymentRepo
    Audit     AuditRepo
}
```

```go
func (s *Service) Checkout(ctx context.Context, cmd CheckoutCmd) (*Order, error) {
    var out *Order
    err := s.uow.Do(ctx, func(r postgres.Repos) error {
        priced, err := s.pricing.Quote(ctx, cmd.CartID)     // BR-PRC-01
        if err != nil { return err }
        if err := r.Inventory.Reserve(ctx, priced.Lines, s.clock.Now().Add(ReservationTTL)); err != nil {
            return err                                      // BR-INV-02, atomic
        }
        out, err = r.Orders.Create(ctx, priced)              // BR-CHK-01
        if err != nil { return err }
        return r.Audit.Record(ctx, audit.Of(ctx, "order.create", out.ID))
    })
    return out, err
}
```

- **MOD-08** — `pgx.Tx` never appears in a service signature. Multi-repository atomicity goes through `UnitOfWork.Do`.
- **MOD-09** — A `UnitOfWork.Do` block contains no network I/O to third parties (Razorpay, courier, email). External calls happen before the transaction or after commit, via the queue. Transactions are short: they hold locks, and a slow transaction is a availability incident (see 05 §4).

### 2.6 Provider factories

The same pattern below the module level, wherever an implementation is selected at runtime.

```go
// internal/payment/provider.go
type Provider interface {
    CreateOrder(ctx context.Context, o OrderRef, amt money.Paise) (ProviderOrder, error)
    VerifyCallback(sig CallbackSignature) error
    VerifyWebhook(rawBody []byte, header string) error
    Refund(ctx context.Context, p PaymentRef, amt money.Paise, idem string) (Refund, error)
}

// NewProvider is the factory. Selection is by configuration ONLY (BR-PAY-15):
// no request field, header or query parameter may reach this function.
func NewProvider(cfg config.Payments, log *slog.Logger) (Provider, error) {
    switch cfg.Provider {
    case "razorpay":
        return razorpay.New(cfg.Razorpay, log)   // keys chosen by cfg.Env, never by input
    default:
        return nil, fmt.Errorf("payment: unknown provider %q", cfg.Provider)  // fail closed
    }
}
```

Same shape for `notify.NewChannel` (email/SMS/WhatsApp), `shipping.NewCourier`, and `search.NewIndex` (Postgres FTS or Meilisearch — the interface is why that decision can stay open, per 01-architecture §8).

---

## 3. Middleware and security first

### 3.1 A route cannot exist without a policy

This is the central structural guarantee. `httpx.Group` wraps chi so that **the security policy is a required argument on every route registration**. Forgetting authorization is not possible; choosing `policy.Public` is possible, and it is greppable and reviewable.

```go
// internal/platform/httpx/group.go
type HandlerFunc func(context.Context, *Request) (Response, error)

type Group struct {
    r     chi.Router
    deps  *Deps
    chain *chainBuilder
}

// Every verb takes a Policy. There is no overload without one.
func (g *Group) GET(pattern string, p policy.Policy, h HandlerFunc)    { g.route("GET", pattern, p, h) }
func (g *Group) POST(pattern string, p policy.Policy, h HandlerFunc)   { g.route("POST", pattern, p, h) }
func (g *Group) PATCH(pattern string, p policy.Policy, h HandlerFunc)  { g.route("PATCH", pattern, p, h) }
func (g *Group) DELETE(pattern string, p policy.Policy, h HandlerFunc) { g.route("DELETE", pattern, p, h) }

func (g *Group) route(method, pattern string, p policy.Policy, h HandlerFunc) {
    if err := p.Validate(); err != nil {
        // Startup panic, not a runtime 500. A malformed or zero policy
        // must never reach production traffic.
        panic(fmt.Sprintf("httpx: invalid policy on %s %s: %v", method, pattern, err))
    }
    g.r.Method(method, pattern, g.chain.build(p).ThenFunc(adapt(h)))
}
```

- **SEC-01** — The zero `policy.Policy` is invalid. `Validate()` rejects it, so a struct-literal mistake fails at boot rather than serving an unauthenticated endpoint.
- **SEC-02** — Route registration happens only through `httpx.Group`. Direct `chi.Router` use outside `platform/httpx` is prohibited and lint-enforced.
- **SEC-03** — A startup self-check enumerates every mounted route with its policy and logs the table at `info`. This is the auditable security surface of the application.

### 3.2 The policy catalogue — one file, the whole security surface

```go
// internal/platform/policy/catalogue.go
package policy

var (
    // Public — explicitly unauthenticated. Every entry here is a review item.
    Public = Policy{
        Name: "public", Auth: AuthNone,
        RateLimit: ratelimit.PerIP(120, time.Minute),
    }

    // Authentication endpoints: unauthenticated but heavily throttled (BR-IDN-05, BR-IDN-11).
    AuthAttempt = Policy{
        Name: "auth.attempt", Auth: AuthNone, CSRF: true,
        RateLimit: ratelimit.Composite(
            ratelimit.PerIP(10, time.Hour),
            ratelimit.PerSubject(5, 15*time.Minute),
        ),
    }

    CustomerSession    = Policy{Name: "customer", Auth: AuthSession, CSRF: true,
                                RateLimit: ratelimit.PerActor(300, time.Minute)}

    CustomerOwnedRead  = Policy{Name: "customer.owned.read", Auth: AuthSession,
                                Ownership: OwnerFromPath("id"),          // BR-ORD-05
                                RateLimit: ratelimit.PerActor(300, time.Minute)}

    CustomerOwnedWrite = Policy{Name: "customer.owned.write", Auth: AuthSession, CSRF: true,
                                Ownership: OwnerFromPath("id"),
                                RateLimit: ratelimit.PerActor(60, time.Minute)}

    Checkout           = Policy{Name: "checkout", Auth: AuthSessionOrGuest, CSRF: true,
                                Idempotent: true,                        // BR-CHK-02
                                RateLimit: ratelimit.PerActor(10, 10*time.Minute)} // BR-CHK-05

    AdminOps           = Policy{Name: "admin.ops", Auth: AuthAdmin, Permission: "order:write",
                                CSRF: true, RateLimit: ratelimit.PerActor(600, time.Minute)}

    AdminFinance       = Policy{Name: "admin.finance", Auth: AuthAdmin, Permission: "refund:write",
                                CSRF: true, Reauth: true, DualApproval: true} // BR-ADM-04, BR-ADM-07

    // Webhook — no session, no CSRF, by design. Authenticated by HMAC only.
    // See BR-PAY-04/05/06. Do not "fix" this.
    ProviderWebhook    = Policy{Name: "webhook.provider", Auth: AuthSignature,
                                RawBody: true, CSRF: false,
                                RateLimit: ratelimit.PerIP(600, time.Minute)}
)
```

- **SEC-04** — Policies are defined only in this file. A policy constructed inline at a call site is prohibited — it defeats the purpose of having one reviewable surface.
- **SEC-05** — Adding or altering a policy requires a security review on the pull request. The file carries a CODEOWNERS entry.

### 3.3 The middleware chain — built from the policy, ordered deliberately

```
 1  RequestID           correlation id, propagated to logs, audit and the queue
 2  RealIP              trusted proxy header only; never a raw client-supplied header
 3  Recoverer           panic -> 500 + error log, never a stack trace to the client (BR-SEC-09)
 4  Logger              slog JSON, PII-redacting (BR-NOT-06, BR-PAY-16)
 5  Timeout             per-policy deadline
 6  BodyLimit           before parsing; RawBody policies buffer here for HMAC (BR-PAY-05)
 7  SecurityHeaders     HSTS, X-Content-Type-Options, Referrer-Policy, CSP, frame-ancestors
 8  CORS                strict allowlist from config
 9  RateLimitIP         cheap, pre-auth: unauthenticated floods die here
10  SignatureVerify     AuthSignature only — HMAC over the raw body, hmac.Equal (BR-PAY-03/04)
11  SessionLoad         resolves the actor; does not require one
12  CSRF                policy.CSRF and cookie-authenticated request
13  RequireAuth         enforces Auth mode; fails closed
14  RequirePermission   RBAC check (BR-ADM-01)
15  RateLimitActor      per-authenticated-actor budget (needs the actor, so it sits after auth)
16  Idempotency         policy.Idempotent — replay returns the stored response (BR-CHK-02/03)
17  Validate            decode + validate the typed request struct (BR-SEC-02, BR-SEC-06)
18  Ownership           resource-level check inside the handler's transaction
```

- **SEC-06** — The chain is assembled in exactly one place (`httpx.chainBuilder.build`). Modules never construct middleware.
- **SEC-07** — Ordering rationale is documented inline. Two constraints are load-bearing: **IP rate limiting precedes authentication** so unauthenticated floods are cheap, and **actor rate limiting follows it** because it needs an identity. Recoverer precedes everything that can panic.
- **SEC-08** — Every middleware **fails closed**. If a check cannot complete — Redis unreachable for the rate limiter, session store unavailable — the request is refused, not admitted (BR-SEC-11).
- **SEC-09** — Authorization is enforced in middleware **and** re-asserted in the service, because internal and worker callers bypass HTTP entirely (BR-ADM-01, BR-ADM-02).

### 3.4 Authorization is one primitive, used everywhere

```go
// internal/platform/authz/enforcer.go
type Enforcer interface {
    // Can answers: may this actor perform this action on this resource?
    // The only authorization question anywhere in the codebase.
    Can(ctx context.Context, act Actor, action Action, res Resource) error
}
```

- **SEC-10** — There is one `Enforcer`. Ad-hoc role comparisons (`if user.Role == "admin"`) anywhere outside `platform/authz` are prohibited and lint-enforced.
- **SEC-11** — `Can` returns an `error`, not a `bool`. A caller cannot accidentally ignore the result the way a discarded boolean allows.
- **SEC-12** — Denials return an opaque not-found or forbidden response and log the full reason server-side. The response must not reveal a resource's existence (BR-IDN-06).

### 3.5 Handlers are thin

```go
// internal/order/handler.go
func (h *Handler) Cancel(ctx context.Context, r *httpx.Request) (httpx.Response, error) {
    var req CancelRequest                        // explicit field list — no mass assignment (BR-SEC-06)
    if err := r.Decode(&req); err != nil {
        return nil, httpx.BadRequest(err)
    }
    if err := h.svc.Cancel(ctx, OrderID(r.Param("id")), req.Reason); err != nil {
        return nil, err                          // one error mapper, §6
    }
    return httpx.NoContent(), nil
}
```

- **SEC-13** — Handlers decode, delegate, and shape the response. Any business rule inside a handler is a defect.
- **SEC-14** — Request structs enumerate permitted fields explicitly. Binding a request body to a domain model or a database row struct is prohibited (BR-SEC-06).

---

## 4. Frontend patterns

### 4.1 Service factory over one configured client

```ts
// web/shared/api/factory.ts
export function createApi(cfg: ApiConfig): Api {
  const http = new HttpClient(cfg)          // base URL, credentials, CSRF header, retry, error mapping
  return {
    catalog:  new CatalogService(http),
    cart:     new CartService(http),
    orders:   new OrderService(http),
    checkout: new CheckoutService(http),
  }
}
```

- **FE-01** — One `HttpClient`. Credentials, the CSRF header, timeouts, retries and error normalisation are configured there and nowhere else.
- **FE-02** — Components and stores consume services. Neither calls `fetch` nor builds a URL.
- **FE-03** — Request and response types are **generated from the Go OpenAPI spec**. Hand-written duplicates of a backend type are prohibited — this is the DRY seam between Go and TypeScript.

### 4.2 Store factory

```ts
// web/shared/stores/createResourceStore.ts
export function createResourceStore<T, F>(name: string, svc: ResourceService<T, F>) {
  return defineStore(name, () => {
    const items = ref<T[]>([])
    const status = ref<Status>('idle')
    const error = ref<AppError | null>(null)
    async function load(filter: F) { /* one loading/error convention for every list */ }
    return { items, status, error, load }
  })
}

export const useOrders = createResourceStore('orders', api.orders)
```

- **FE-04** — List, pagination, loading and error handling come from the store factory. Reimplementing that per screen is prohibited.
- **FE-05** — Stores hold state and delegate. No money arithmetic, no tax, no discount logic — display what the server returned (BR-PRC-01).
- **FE-06** — Components are `<script setup>` and presentational. Shared behaviour goes into composables, not into a mixin or a base component.

---

## 5. Worker symmetry

- **WRK-01** — Workers call the same service objects as handlers. A task handler containing a duplicate of a service method is a defect.
- **WRK-02** — Task payloads carry the request ID and the actor, so a queued action is auditable and traceable to its origin (BR-ADM-06).
- **WRK-03** — Every task handler is idempotent. Retries are guaranteed, so re-execution must be safe (BR-NOT-05, BR-PAY-07).
- **WRK-04** — Worker authorization is explicit: a task runs as a named system actor with a defined permission set, never as "no actor" and never with implicit privilege.

---

## 6. DRY — the rule of one

### 6.1 Single-implementation registry

Each concern below has **exactly one** implementation. A second one anywhere is a review rejection.

| Concern | Sole location | Guards |
|---|---|---|
| Money type and arithmetic | `platform/money` | BR-PRC-01 |
| Rounding | `pricing.Round` | BR-PRC-03 |
| GST computation and split | `pricing.GST` | BR-PRC-04, BR-PRC-06 |
| Discount allocation across lines | `pricing.Allocate` | BR-DSC-13 |
| HMAC signature verification | `payment/razorpay.verify` | BR-PAY-03, BR-PAY-04 |
| Webhook idempotency ledger | `payment.ledger` | BR-PAY-07 |
| Order state transition table | `order.transitions` | BR-ORD-01, BR-ORD-02 |
| Stock reservation SQL | `inventory.reserve` | BR-INV-02 |
| Authorization check | `platform/authz.Enforcer` | BR-ADM-01 |
| Security policies | `platform/policy/catalogue.go` | §3.2 |
| Middleware chain assembly | `platform/httpx.chainBuilder` | SEC-06 |
| Request validation | `platform/httpx.Decode` | BR-SEC-02 |
| Error envelope and status mapping | `platform/httpx/errors.go` | BR-SEC-09 |
| Pagination and cursors | `platform/httpx/page.go` | BR-RPT-04 |
| Rate limit buckets | `platform/ratelimit` | BR-SEC-10 |
| Idempotency store | `platform/idem` | BR-CHK-02 |
| Audit writing | `platform/audit` | BR-ADM-05, BR-ADM-06 |
| Redis key construction | `platform/redis/keys.go` | 01-architecture §6 |
| Log redaction | `platform/logging/redact.go` | BR-NOT-06, BR-SEC-07 |
| ID generation | `platform/ids` | §0 conventions |
| Clock | `platform/clock` | testability |
| API types shared with the frontend | generated from OpenAPI | FE-03 |

### 6.2 Enforcement

- **DRY-01** — Every entry in §6.1 carries a package comment naming it as the sole implementation and citing its `BR-*` rules.
- **DRY-02** — Import-boundary lint rules prevent a module importing another module's internals, `chi` outside `platform/httpx`, `pgx` outside `platform/postgres` and repositories, and `crypto/hmac` outside `payment`.
- **DRY-03** — A copied block of business logic is a bug report, not a style comment.

### 6.3 Where duplication is deliberate — do not "DRY" these

Some duplication is a correctness requirement. Removing it breaks the business rules.

| Deliberate duplication | Why | Rule |
|---|---|---|
| Order line snapshots of SKU, title, price, GST rate | An order is a historical record. Normalising it back to a live join lets a price edit rewrite past invoices. | BR-ORD-03 |
| Address snapshot on the order | Editing a saved address must not alter a past invoice. | BR-ADR-05 |
| Authorization in middleware **and** service | Worker and internal callers never traverse the middleware chain. | BR-ADM-01, SEC-09 |
| Validation at the boundary **and** at each state change | Data validated at input is not still valid at commit time. | Global convention |
| Per-module port interfaces describing the same collaborator | Each consumer declares the contract it needs; merging them creates a shared god-interface. | OOP-05 |

**The distinction:** DRY applies to *behaviour and rules*. It does not apply to *facts recorded at a point in time*, or to *defence in depth*.

---

## 7. Testing obligations for these patterns

Beyond the per-rule coverage in 02 Appendix B:

- **TST-01** — Every module factory has a construction test asserting it fails closed on a missing dependency.
- **TST-02** — A router test enumerates every mounted route and asserts each has a valid, non-zero policy — the automated form of SEC-01.
- **TST-03** — For each policy, a table test asserts the built chain rejects: no session, wrong role, missing CSRF, exceeded rate limit, replayed idempotency key.
- **TST-04** — Webhook negative tests: absent signature, wrong secret, tampered body, replayed event ID, amount mismatch.
- **TST-05** — Decorator tests assert the audit decorator writes on success and does not write on failure.
- **TST-06** — Unit-of-work tests assert rollback on error at each step of checkout, leaving no reservation and no order.
- **TST-07** — Services are tested against fake ports, not a database. Repository tests use `testcontainers-go` against real PostgreSQL.

---

## 8. Review checklist

A pull request is rejected if any of these is true.

1. A route is registered outside `httpx.Group`, or with an inline policy.
2. A role or permission is compared outside `platform/authz`.
3. A service dependency is a concrete cross-module type rather than an interface.
4. A struct holding state has exported fields, or is usable at its zero value.
5. Business logic appears in a handler, a Vue component, or `cmd/`.
6. Money is represented as anything other than `money.Paise`.
7. A concern from §6.1 has gained a second implementation.
8. A deliberate duplication from §6.3 has been "refactored away".
9. A new `BR-*` rule has no test, or a changed rule has no updated test.
10. A middleware or check fails open on infrastructure error.
