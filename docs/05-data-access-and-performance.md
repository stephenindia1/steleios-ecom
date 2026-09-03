# Steleios — Data Access and Performance

Engraved rules for every query, cache read and data structure. **Normative: MUST / MUST NOT.**
Companions: [03-code-standards-and-patterns.md](03-code-standards-and-patterns.md) · [04-go-and-typescript-standards.md](04-go-and-typescript-standards.md) · [06-observability-and-event-logging.md](06-observability-and-event-logging.md).

Status: draft · 3 September 2026

---

## 0. The mandate

> **Every query has a known time complexity and a known space complexity, and both are bounded by the size of the answer — not by the size of the table.**

A query whose cost grows with total rows stored is a defect the day it is written, regardless of how fast it runs on a development database with 200 orders. Catalog, orders, audit and webhook tables all grow monotonically and forever.

**Three questions every data access MUST answer, in the PR description:**

1. What index serves it, and what is the plan? (`EXPLAIN (ANALYZE, BUFFERS)` output attached.)
2. What bounds the number of rows returned?
3. What bounds the memory held while producing the result?

A query that cannot answer all three does not merge.

---

## 1. Time complexity

| ID | Rule |
|---|---|
| DB-001 | Every query MUST be served by an index appropriate to its access pattern. A `Seq Scan` on a table that grows unboundedly is a merge blocker. Seq Scan is acceptable only on small, bounded lookup tables (states, pincode zones, GST slabs) where the planner correctly prefers it. |
| DB-002 | **No N+1.** Loading a collection and then querying per element is prohibited. Use one join, or a single batched `WHERE id = ANY($1)`, or a `LATERAL` join for top-N-per-group. Enforced by review and by the query-count assertion in repository tests. |
| DB-003 | Composite index column order MUST be: equality predicates, then range predicates, then `ORDER BY` columns. An index whose leading column is not an equality predicate of the query is not serving it. |
| DB-004 | Every foreign key MUST have an index on the referencing side. An unindexed FK turns every parent delete or update into a full scan of the child table. |
| DB-005 | Hot filtered queries MUST use partial indexes rather than filtering a large index: `create index on orders (placed_at desc) where status = 'pending_payment';`. |
| DB-006 | `ORDER BY` on any list endpoint MUST be index-backed. An in-memory sort of an unbounded set is prohibited. |
| DB-007 | Pagination on any growing table MUST be **keyset (cursor)**, not `OFFSET`. `OFFSET n` scans and discards `n` rows — its cost grows with page depth. |
| DB-008 | Exact `COUNT(*)` over a growing table is prohibited in a request path. Use `LIMIT n+1` to answer "is there more", `pg_class.reltuples` for approximate totals, or a maintained counter. |
| DB-009 | `LIKE '%term%'` and `ILIKE '%term%'` are prohibited for search. Use `pg_trgm` GIN indexes or full-text search with a `tsvector` column and GIN index (BR-CAT search rules). |
| DB-010 | Functions in predicates MUST match an expression index, or be rewritten: `where lower(email) = $1` requires `create index on customers (lower(email))`. |
| DB-011 | Analytical aggregation over orders MUST NOT run against live transactional tables in a request path. It runs against materialised views refreshed on a schedule (BR-RPT-01). |
| DB-012 | Every statement MUST have `statement_timeout` set by workload class: request path 3s, worker 30s, reporting refresh 300s. An unbounded statement is an availability defect. |
| DB-013 | Query plans for the ten highest-traffic queries are captured as committed baselines. A PR that changes one attaches the before and after plan. |

### Complexity budget by endpoint class

| Class | Bound | Example |
|---|---|---|
| Point read | `O(log n)` index seek | order by ID, variant by SKU |
| Keyset list | `O(log n + k)` for page size `k` | order history, product listing |
| Faceted search | `O(log n + k)` via GIN | catalog browse |
| Aggregation | `O(1)` against a materialised view | revenue by day |
| Batch worker | `O(k)` over a bounded claimed batch | reservation sweeper, settlement import |

Anything outside this table needs an explicit decision recorded in the PR.

---

## 2. Space complexity

| ID | Rule |
|---|---|
| DB-020 | Result sets MUST be bounded. Every list query carries a server-enforced `LIMIT`; the maximum page size is a constant in `platform/httpx/page.go` and MUST NOT be overridable by the client beyond that cap (BR-RPT-04). |
| DB-021 | `SELECT *` is prohibited. Columns are enumerated explicitly, so the payload is known, index-only scans stay possible, and a schema change cannot silently widen every response. |
| DB-022 | Large columns (`payload jsonb` on `webhook_events`, media metadata, review text) MUST NOT be selected unless the caller needs them. List endpoints select summary columns only. |
| DB-023 | Exports and bulk jobs MUST stream: iterate `pgx.Rows` and write incrementally, or use `COPY TO`. Materialising an unbounded result into a slice is prohibited. |
| DB-024 | Slices whose final size is known MUST be preallocated: `make([]Line, 0, len(ids))`. Repeated `append` growth on a hot path is avoidable garbage. |
| DB-025 | A subslice of a large buffer MUST NOT be retained beyond the buffer's useful life — it pins the whole backing array. Copy when narrowing. |
| DB-026 | Request and response bodies are size-limited by middleware before parsing (doc 03, chain step 6). |
| DB-027 | Batch operations process in bounded chunks with a documented chunk size. "Load everything then loop" is prohibited. |
| DB-028 | In-memory caches (if any) MUST be bounded with an eviction policy. An unbounded `map` used as a cache is a memory leak. |

---

## 3. Writes, locking and concurrency

| ID | Rule |
|---|---|
| DB-030 | Read-modify-write against the database is prohibited where an atomic conditional `UPDATE` will do. Stock reservation is the canonical case (BR-INV-02). |
| DB-031 | `SELECT ... FOR UPDATE` locks the narrowest possible row set, and only inside a transaction that does no external I/O (doc 03, MOD-09). |
| DB-032 | Multi-row locks MUST be acquired in a deterministic order (sort by primary key) to prevent deadlock. |
| DB-033 | Queue-like scans use `FOR UPDATE SKIP LOCKED` so concurrent workers do not serialise on each other. |
| DB-034 | Transactions are short. `idle_in_transaction_session_timeout` is set. A transaction held open across a network call is a merge blocker. |
| DB-035 | Bulk writes use `COPY` or multi-row `INSERT`, not a loop of single inserts. |
| DB-036 | Upserts use `INSERT ... ON CONFLICT`, which is atomic. `SELECT`-then-`INSERT` has a race by construction (BR-PAY-07). |
| DB-037 | Migrations MUST be non-blocking on large tables: `CREATE INDEX CONCURRENTLY`, add columns nullable then backfill in batches, never a table rewrite in a deploy transaction. |
| DB-038 | Every table that grows without bound and is queried by time — `orders`, `audit_log`, `webhook_events`, `stock_movements`, `domain_events` — has a declared partitioning strategy (monthly range) and an archival policy before it reaches 50M rows. |

---

## 4. Repository layer

| ID | Rule |
|---|---|
| DB-040 | All SQL lives in `.sql` files compiled by `sqlc`. Query construction by string concatenation or `fmt.Sprintf` is prohibited without exception (BR-SEC-03). |
| DB-041 | Dynamic filtering uses a bounded, allowlisted set of predicates assembled from typed values — never client-supplied column or direction names interpolated into SQL. Sort fields are matched against an allowlist enum. |
| DB-042 | Repositories map rows to domain types and do nothing else. Business rules in a repository are a defect (doc 03). |
| DB-043 | Every query takes `ctx` and respects its deadline. `noctx` is enabled. |
| DB-044 | `rows.Err()` is checked and `rows.Close()` deferred on every iteration. `rowserrcheck` and `sqlclosecheck` are enabled. |
| DB-045 | Connection pool size is configured explicitly from measurement, not left to default, and is documented alongside the database's `max_connections`. |
| DB-046 | Repository tests assert the query count for the operation, so an N+1 introduced later fails the test rather than the production database. |

---

## 5. Redis

Redis is the single backing store for **sessions, cart hot state, the job queue, rate limiting and idempotency**. Client: `github.com/redis/go-redis/v9`, wrapped once in `platform/redis`. Queue: `github.com/hibiken/asynq`, which runs on the same Redis. No second broker, no second cache.

```go
// internal/platform/redis/client.go — the only place go-redis is constructed.
func New(cfg config.Redis, log *slog.Logger) (*Client, error) {
    rdb := redis.NewClient(&redis.Options{
        Addr:            cfg.Addr,
        Username:        cfg.Username,
        Password:        cfg.Password,          // from env only (BR-SEC-07)
        DB:              cfg.DB,
        PoolSize:        cfg.PoolSize,          // measured, not defaulted (DB-045)
        MinIdleConns:    cfg.MinIdleConns,
        ReadTimeout:     cfg.ReadTimeout,       // every call bounded (GO-033)
        WriteTimeout:    cfg.WriteTimeout,
        MaxRetries:      2,
        TLSConfig:       cfg.TLS(),             // TLS in every non-local environment
    })
    if err := rdb.Ping(ctx).Err(); err != nil {
        return nil, fmt.Errorf("redis: ping: %w", err)   // fail closed at startup
    }
    rdb.AddHook(otelHook{}, metricsHook{}, redactingLogHook{log})
    return &Client{rdb: rdb}, nil
}
```

| ID | Rule |
|---|---|
| RD-000 | `go-redis` is constructed in exactly one place and reached only through `platform/redis`. Importing `github.com/redis/go-redis/v9` outside that package is prohibited (`depguard`). Domain code depends on narrow interfaces (`SessionStore`, `RateLimiter`, `Cache`), never on a Redis client. |

### 5.1 Sessions

| ID | Rule |
|---|---|
| SES-001 | A session is an opaque 256-bit identifier from `crypto/rand`, base64url-encoded, stored at `sess:{id}`. It carries no data and is not a JWT (BR-IDN-02). |
| SES-002 | The session record holds actor ID, roles, CSRF secret, issued-at, last-seen, IP and user-agent fingerprint. It MUST NOT hold cart contents, PII beyond the actor reference, or anything the client can influence. |
| SES-003 | The cookie is `HttpOnly; Secure; SameSite=Lax; Path=/`, with no `Domain` attribute unless subdomain sharing is required. |
| SES-004 | TTL is 30 days sliding for customers, 12 hours for admin (BR-ADM-07). The slide is applied at most once per minute to avoid a write on every request. |
| SES-005 | Session lookup is a single `GET` — `O(1)`. Enumerating sessions to find a user's is prohibited; a per-user index set `sess:user:{id}` maintained on create and delete serves "sign out everywhere" (BR-IDN-03). |
| SES-006 | Rotate the session ID on privilege change — login, role change, password change — carrying state to a new key and deleting the old, to close session fixation. |
| SES-007 | Invalidation is a `DEL` plus removal from the user index, applied on logout, password change, reset, and role change (BR-IDN-03, BR-IDN-07, BR-ADM-03). |
| SES-008 | If Redis is unavailable, authenticated requests fail closed with 503. A missing session is never treated as a valid one (BR-SEC-11). |
| SES-009 | Session values are serialised with a versioned encoder. A decode failure invalidates the session rather than partially populating an actor. |
| SES-010 | Session IDs MUST NOT be logged. The audit log records the actor and a session fingerprint hash, never the identifier itself (BR-SEC-07). |

### 5.2 Queue

| ID | Rule |
|---|---|
| QUE-001 | All asynchronous work runs on asynq. A goroutine detached from a request handler is prohibited (GO-051). |
| QUE-002 | Queues are named by priority — `critical` (payment follow-up, invoice), `default` (email, SMS), `low` (exports, reindex, reporting refresh) — with explicit weights. A slow low-priority job MUST NOT delay a payment confirmation. |
| QUE-003 | Every task handler is idempotent; retries are guaranteed (BR-PAY-07, BR-NOT-05, WRK-03). |
| QUE-004 | Task payloads are versioned structs carrying only identifiers plus the request ID and actor. Payloads MUST NOT carry secrets, PII, or a full domain object — the handler reloads current state from PostgreSQL. |
| QUE-005 | Retry policy is per task type with exponential backoff and a maximum attempt count, after which the task moves to the archive with an alert (BR-NOT-02). |
| QUE-006 | Task timeouts are explicit. An unbounded handler blocks a worker slot indefinitely. |
| QUE-007 | Uniqueness constraints (`asynq.Unique`) are used for tasks that must not queue twice — reservation sweeps, settlement imports, view refreshes. |
| QUE-008 | Scheduled/periodic tasks are declared in one place and are safe to run concurrently across instances, guarded by a Redis lock with a TTL (DB/RD lock rules, GO-057). |
| QUE-009 | The enqueue MUST be inside the same PostgreSQL transaction as the state change it follows — via the transactional outbox (doc 06 §3) — or it is not guaranteed to happen. A bare enqueue after commit is permitted only where losing the task is acceptable and that is stated in a comment. |
| QUE-010 | Queue depth, oldest-pending age, processing latency, retry count and dead-letter size are exported as metrics with alerts (doc 06 §5). |
| QUE-011 | Worker concurrency is configured explicitly and sized against the database pool; workers MUST NOT be able to exhaust PostgreSQL connections. |

### 5.3 Redis command discipline

| ID | Rule |
|---|---|
| RD-001 | `KEYS` is prohibited in all environments — it is `O(n)` over the entire keyspace and blocks the server. Use `SCAN` with a cursor, or maintain an index set. |
| RD-002 | Only `O(1)` or `O(log n)` commands on hot paths. `SMEMBERS`, `HGETALL`, `LRANGE 0 -1` on an unbounded collection are prohibited; page them. |
| RD-003 | Every key MUST have a TTL, or an explicit written justification for why it is permanent. Keys are constructed only by `platform/redis/keys.go` (doc 03 §6.1). |
| RD-004 | Multi-step read-modify-write against Redis MUST be a Lua script or a `WATCH`/`MULTI` transaction. Rate limiters and reservation counters are atomic or they are wrong. |
| RD-005 | Lua scripts MUST be `O(1)`-ish and MUST NOT loop over unbounded collections — the script blocks the whole server. |
| RD-006 | Related commands are pipelined. A loop of round-trips is a latency defect. |
| RD-007 | Cache reads use **read-through with explicit invalidation on write** (BR-CAT-13). TTL is a backstop, never the invalidation mechanism. |
| RD-008 | Cache stampedes are prevented with `singleflight` in-process plus a short lock, so a cold key does not produce N identical database queries. |
| RD-009 | Misses are cached negatively with a short TTL where a miss is expensive, to blunt enumeration probing. |
| RD-010 | Redis is a cache and a coordination store. It is never the only copy of durable data — sessions and carts have their PostgreSQL backing (BR-CRT-10). Redis loss MUST degrade the system, never corrupt it. |
| RD-011 | Redis unavailability fails **closed** for security functions (rate limiting, idempotency) and **open-with-degradation** for pure caches. This distinction is stated per call site (BR-SEC-11). |

---

## 6. API and frontend payloads

| ID | Rule |
|---|---|
| API-001 | Every collection endpoint is paginated with a cursor and a capped page size. There is no "return all" endpoint. |
| API-002 | Response payloads carry what the screen needs. Over-fetching to avoid a second endpoint is a defect; so is a chatty screen making ten calls. Add a purpose-built endpoint instead. |
| API-003 | Related-entity fan-out is resolved server-side in one query. The frontend MUST NOT loop over a list issuing per-item requests. |
| API-004 | List responses are shaped for the list — summary fields only. Detail is fetched on demand. |
| API-005 | Images are served in responsive variants from a CDN; the storefront MUST NOT download a full-resolution image for a thumbnail. |
| API-006 | Frontend list rendering is virtualised past 200 rows. Admin tables page server-side. |

---

## 7. Performance budgets

Measured at p95, excluding client network, under production-shaped data volumes.

| Path | Budget |
|---|---|
| Point read (`GET /orders/{id}`) | 50 ms |
| Catalog listing with facets | 150 ms |
| Cart read with re-pricing | 120 ms |
| Checkout initiation (transaction) | 400 ms |
| Webhook handler (to 200 response) | 200 ms, hard ceiling 5 s (BR-PAY-08) |
| Admin order search | 300 ms |
| Reporting endpoint (materialised) | 200 ms |
| Storefront product page LCP | 2.0 s |

| ID | Rule |
|---|---|
| PRF-001 | A PR that regresses a budgeted path beyond its budget does not merge without an explicit, recorded decision. |
| PRF-002 | Load tests run against production-shaped volumes — a million orders, not a thousand. A benchmark on an empty database proves nothing. |
| PRF-003 | `pg_stat_statements` is enabled; the weekly top-20 by total time is reviewed. Slow-query logging is on above 200 ms. |
| PRF-004 | Index usage is reviewed quarterly. Unused indexes are dropped — they cost write throughput and storage. |
| PRF-005 | Optimisation follows measurement. A performance change without a before-and-after number is rejected. |
| PRF-006 | Query duration, rows returned, and cache hit ratio are emitted as metrics per repository method (doc 06). |

---

## 8. Review checklist — data access

A pull request touching data access is rejected if any of these is true.

1. A new or changed query has no attached `EXPLAIN (ANALYZE, BUFFERS)` plan.
2. The plan shows a `Seq Scan` on a growing table, or row estimates off by more than an order of magnitude.
3. A query returns an unbounded result set, or uses `OFFSET` pagination on a growing table.
4. `SELECT *` appears anywhere.
5. A collection is loaded and then queried per element (N+1).
6. A new foreign key has no index.
7. A transaction contains a call to Razorpay, a courier, or any other external service.
8. SQL is built by string concatenation.
9. A Redis key has no TTL and no justification, or a prohibited command is used.
10. A growing table gained a column or an access pattern without a partitioning or archival note.
