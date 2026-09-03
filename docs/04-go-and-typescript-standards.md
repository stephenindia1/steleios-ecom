# Steleios — Language Standards

Engraved coding standards for Go and TypeScript. **Normative: MUST / MUST NOT.**
Companions: [03-code-standards-and-patterns.md](03-code-standards-and-patterns.md) (architecture patterns) · [05-data-access-and-performance.md](05-data-access-and-performance.md) (queries) · [02-features-and-business-rules.md](02-features-and-business-rules.md) (`BR-*`).

Status: draft · 3 September 2026

---

## 0. Principle

**Write Go the way Go is written.** This codebase is idiomatic Go first and a pattern catalogue second. Where a design pattern from another language conflicts with Go's idiom — inheritance hierarchies, service locators, dependency-injection containers, exceptions-as-control-flow, getter/setter ceremony — Go's idiom wins. The patterns in doc 03 are expressed *through* Go idiom, never against it.

The authorities, in order: the Go spec → [Effective Go](https://go.dev/doc/effective_go) → the [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments) → the standards below → team preference.

---

## 1. Formatting and tooling

| ID | Rule |
|---|---|
| GO-001 | Code MUST be `gofumpt`-formatted. CI fails on any diff. No manual alignment, no exceptions. |
| GO-002 | Imports MUST be grouped `stdlib` / `external` / `internal`, `goimports -local github.com/steleios/ecom`. |
| GO-003 | `golangci-lint run` MUST pass with the committed `.golangci.yml`. Disabling a linter requires a comment stating why, on the line, reviewed in the PR. |
| GO-004 | `//nolint` MUST name the linter and carry a reason: `//nolint:gosec // G404: not cryptographic, seeded per test`. A bare `//nolint` is rejected. |
| GO-005 | Build MUST be `CGO_ENABLED=0`, and the module MUST build with `-trimpath`. |
| GO-006 | Go version is pinned in `go.mod`. Upgrades are their own PR with the changelog reviewed. |

---

## 2. Naming

| ID | Rule |
|---|---|
| GO-010 | Package names are short, lowercase, singular, no underscores, no `util`, `common`, `helpers`, `misc`, `base`, `shared`. A package that cannot be named for what it *is* has no boundary. |
| GO-011 | No stutter. `order.Service`, not `order.OrderService`. `payment.Provider`, not `payment.PaymentProvider`. |
| GO-012 | Initialisms keep their case: `ID`, `URL`, `HTTP`, `API`, `SKU`, `GST`, `HSN`, `SQL`, `UPI`. `orderID`, never `orderId`. |
| GO-013 | Receiver names are 1–2 characters and identical across every method of a type. Never `self` or `this`. |
| GO-014 | Interface names describe a role. Single-method interfaces take the `-er` form (`Recorder`, `Enforcer`); role interfaces take the `-Port` suffix at module boundaries (`InventoryPort`). |
| GO-015 | Getters have no `Get` prefix: `o.Total()`, not `o.GetTotal()`. Setters are avoided entirely — objects expose behaviour, not mutable fields. |
| GO-016 | Exported identifiers MUST have a doc comment beginning with the identifier's name. |
| GO-017 | Booleans are named for the true state: `isPaid`, `allowsCOD`. Negative names (`notAllowed`, `disableX`) are prohibited. |
| GO-018 | Test names read as sentences: `TestService_Cancel_RejectsAfterPacked`. |

---

## 3. Errors

| ID | Rule |
|---|---|
| GO-020 | Errors are values and MUST be returned, never thrown, never signalled by a sentinel return value like `-1` or `""`. |
| GO-021 | An error MUST NOT be discarded. `_ = f()` is permitted only with a comment stating why the failure is genuinely irrelevant. `errcheck` is enabled with no exclusions for the `internal/` tree. |
| GO-022 | Wrapping MUST add context and preserve the chain: `fmt.Errorf("order %s: %w", id, err)`. Wrapping without adding information is noise; wrapping with `%v` destroys the chain and is prohibited. |
| GO-023 | Error strings are lowercase, no trailing punctuation, no capitalisation of the first word (unless it is a proper noun). |
| GO-024 | Expected conditions are sentinel errors (`var ErrOutOfStock = errors.New("out of stock")`) matched with `errors.Is`. Conditions carrying data are typed errors matched with `errors.As`. Never match on an error's string. |
| GO-025 | Each domain package declares its errors in `errors.go`. HTTP status mapping happens in exactly one place (`platform/httpx/errors.go`); services MUST NOT know about status codes. |
| GO-026 | Log an error **or** return it — never both. The boundary that stops the error logs it, once, with the request ID. |
| GO-027 | `panic` is permitted only for programmer error detected at startup (invalid policy, failed `Deps.Validate`). It MUST NOT be used for control flow or for any condition reachable from request data. `recover` exists only in the recovery middleware. |
| GO-028 | Errors returned to clients MUST be generic; the detail is logged server-side (BR-SEC-09). Driver errors and stack traces never cross the boundary. |

---

## 4. Context

| ID | Rule |
|---|---|
| GO-030 | `context.Context` is the first parameter of every function that performs I/O, named `ctx`. |
| GO-031 | A context MUST NOT be stored in a struct field. |
| GO-032 | `context.Background()` appears only in `main`, in tests, and at the root of a worker task. Never in a request path. |
| GO-033 | Every outbound call — database, Redis, Razorpay, courier — MUST carry a deadline. An unbounded external call is an availability defect. |
| GO-034 | Context values carry only request-scoped metadata (request ID, actor, logger) through typed, unexported key types. They MUST NOT carry optional parameters or dependencies. |
| GO-035 | Long-running loops MUST check `ctx.Err()` or select on `ctx.Done()`. |

---

## 5. Types, interfaces and composition

| ID | Rule |
|---|---|
| GO-040 | **Accept interfaces, return concrete types.** Constructors return `*Service`, not an interface. |
| GO-041 | An interface is introduced when a consumer needs to substitute an implementation — a second implementation, or a test double. Interfaces written speculatively are prohibited. |
| GO-042 | Interfaces are declared in the consuming package (doc 03, OOP-05). |
| GO-043 | Embedding is for composing behaviour, never for simulating inheritance. An embedded type MUST NOT be used to share a "base" implementation across siblings. |
| GO-044 | Domain primitives are defined types (`money.Paise`, `OrderID`, `Status`). A function taking two `string` parameters that could be transposed is a defect. |
| GO-045 | Enumerated types MUST have `String()`, `Valid() error`, and a `Parse` that rejects unknown input. Persisted values are the string form, never the ordinal. |
| GO-046 | Generics are used only where they remove real, existing duplication with identical behaviour. A generic with one instantiation is rejected. |
| GO-047 | Struct literals crossing a package boundary MUST use field names. Positional literals are permitted only for small internal value types. |
| GO-048 | `any`/`interface{}` in a signature requires a comment justifying it. It is prohibited in domain packages. |

---

## 6. Concurrency

| ID | Rule |
|---|---|
| GO-050 | Every goroutine MUST have an identified owner and a defined termination condition. A goroutine that can outlive its request is a leak. |
| GO-051 | A bare `go f()` in a request handler is prohibited. Background work goes to asynq, which gives it durability, retries and observability (doc 03, WRK-*). |
| GO-052 | Concurrent work within a request uses `errgroup.WithContext`, bounded by `SetLimit`. Unbounded fan-out is prohibited. |
| GO-053 | Shared mutable state is guarded by a mutex held for the shortest possible span, or eliminated. A mutex MUST be adjacent to the field it guards, with a comment naming what it protects. |
| GO-054 | Channels transfer ownership; mutexes protect state. Do not use a channel as a lock. |
| GO-055 | `go test -race` runs in CI on every PR. A race is a merge blocker, never "flaky". |
| GO-056 | Correctness MUST NOT depend on timing. Tests MUST NOT use `time.Sleep` to synchronise; use a fake clock, a channel, or `sync.WaitGroup`. |
| GO-057 | Cross-process mutual exclusion (reservation sweeper, settlement import, materialised-view refresh) uses a Redis lock with a TTL and a fencing token, or a Postgres advisory lock. Two instances MUST be safe to run. |

---

## 7. Structure and readability

| ID | Rule |
|---|---|
| GO-060 | Files within a domain package follow the fixed layout: `doc.go`, `model.go`, `errors.go`, `ports.go`, `service.go`, `repository.go`, `handler.go`, `worker.go`, `module.go`, plus `*_test.go`. |
| GO-061 | Every package has a `doc.go` stating its responsibility, its invariants, and the `BR-*` rules it owns. |
| GO-062 | Guard clauses and early returns. The happy path stays at the leftmost indentation. `else` after a `return` is prohibited. |
| GO-063 | Cyclomatic complexity per function ≤ 15 (`gocyclo`); nesting depth ≤ 4. Exceeding either means extracting a named function, not adding a comment. |
| GO-064 | A function that would need a comment to explain *what* it does should be split. Comments explain *why* — the rule, the constraint, the non-obvious trade-off. |
| GO-065 | Every non-obvious business decision in code MUST cite its rule: `// BR-INV-05: stock decrements only after payment confirmation.` |
| GO-066 | `defer` for cleanup, immediately after successful acquisition. `defer` inside a loop is prohibited; extract the body. |
| GO-067 | Named return values are used only to document multiple returns of the same type, or where a deferred function must modify the result. Naked `return` is prohibited. |
| GO-068 | Dead code, commented-out code and `TODO` without an owner and a ticket are rejected at review. |

---

## 8. Data, time and serialisation

| ID | Rule |
|---|---|
| GO-070 | Money is `money.Paise` (`int64`). Floats MUST NOT appear in any pricing, tax, discount, refund or settlement path. |
| GO-071 | Time is `time.Time`, stored UTC as `timestamptz`. Times MUST NOT be passed as strings. Business dates use `Asia/Kolkata` explicitly at the presentation boundary only. |
| GO-072 | `time.Now()` MUST NOT be called in domain code. Time comes from the injected `clock.Clock`, so behaviour is testable. |
| GO-073 | Durations are `time.Duration`, never `int` seconds. |
| GO-074 | JSON structs carry explicit tags. `omitempty` MUST NOT be used on money, quantity or status fields — a missing field and a zero field are different facts. |
| GO-075 | Inbound JSON is decoded with `DisallowUnknownFields` into an explicit request struct (BR-SEC-06). |
| GO-076 | Randomness for tokens, OTPs, IDs and nonces MUST come from `crypto/rand`. `math/rand` is prohibited outside tests and non-security jitter. |
| GO-077 | Secret comparison MUST use `crypto/subtle` or `hmac.Equal`. `==` on a secret, token or signature is prohibited (BR-PAY-03). |

---

## 9. Logging and observability

| ID | Rule |
|---|---|
| GO-080 | `log/slog` with the JSON handler only. `fmt.Print*`, `log.Print*` and `println` MUST NOT appear outside `main` startup banners. |
| GO-081 | Every log line carries: ISO-8601 UTC timestamp, level, module, request ID, and actor ID where one exists. |
| GO-082 | Log fields are structured key/value pairs. String interpolation into the message MUST NOT be used for data. |
| GO-083 | Levels: `error` needs a human; `warn` is recoverable and unexpected; `info` is a state change or key event; `debug` is off in production. A handled, expected condition is not an error. |
| GO-084 | Secrets and PII MUST pass through `platform/logging.Redact`. Phone, email, address, card, token, OTP, and raw provider payloads are redacted (BR-SEC-07, BR-NOT-06, BR-PAY-16). |
| GO-085 | The request ID propagates into the queue payload and into every downstream call, so one identifier traces a customer action end to end. |
| GO-086 | Metrics and traces are emitted at the middleware and repository layers only. Business code MUST NOT contain instrumentation calls — use a decorator (doc 03, OOP-09). |

---

## 10. Testing

| ID | Rule |
|---|---|
| GO-090 | Tests are table-driven with named subtests, and MUST cover the failing cases: invalid, missing, malformed, empty, boundary, and the error path — not only the happy path. |
| GO-091 | Every `BR-*` rule has at least one passing and one failing case, and the test names the rule. |
| GO-092 | `t.Parallel()` on every test that permits it. Tests MUST NOT share mutable global state. |
| GO-093 | Services are tested against fakes for their ports. Repositories are tested against real PostgreSQL via `testcontainers-go`. Mocking the database in a repository test proves nothing. |
| GO-094 | Assertions compare with `go-cmp`. Reflect-based deep equality on structs with unexported fields is prohibited. |
| GO-095 | Golden files are used for invoices and generated documents, and are reviewed as code when they change. |
| GO-096 | Coverage is a diagnostic, not a target. A PR that lowers coverage in `pricing`, `payment`, `inventory`, `order` or `authz` MUST justify it. |
| GO-097 | Every fixed bug gets a regression test reproducing it, committed in the same PR as the fix. |
| GO-098 | Fuzz targets exist for parsers and signature verification. |

---

## 11. Dependencies

| ID | Rule |
|---|---|
| GO-100 | Standard library first. A new dependency requires a PR comment justifying it against a stdlib alternative. |
| GO-101 | Versions pinned; `go.sum` committed; `go mod tidy` clean in CI. |
| GO-102 | New dependencies are vetted for maintenance, activity, CVEs and licence before adding. |
| GO-103 | `govulncheck` blocks merge. Findings are fixed or triaged with a documented decision — never silently ignored. |
| GO-104 | Third-party types MUST NOT appear in domain signatures. Wrap them at the platform boundary so the dependency stays replaceable. |

---

## 12. TypeScript and Vue

| ID | Rule |
|---|---|
| TS-001 | `strict: true` plus `noUncheckedIndexedAccess`, `exactOptionalPropertyTypes`, `noImplicitOverride`, `noFallthroughCasesInSwitch`. |
| TS-002 | `any` is prohibited. Use `unknown` with a narrowing guard. Non-null assertion (`!`) is prohibited outside tests. |
| TS-003 | Type assertions (`as`) require a comment justifying why the compiler cannot know. `as unknown as T` is prohibited. |
| TS-004 | Types shared with the backend are **generated** from the Go OpenAPI spec. Hand-written duplicates are prohibited (doc 03, FE-03). |
| TS-005 | Biome for lint and format; CI fails on any diff. |
| TS-006 | Vue components use `<script setup lang="ts">` with typed `defineProps`/`defineEmits`. The Options API is prohibited in new code. |
| TS-007 | Components are presentational. Business logic lives in composables and service classes (doc 03, OOP-15). A component over ~200 lines is a decomposition signal. |
| TS-008 | `v-html` is prohibited on any value that could contain user input (BR-SEC-04, BR-REV-03). |
| TS-009 | No money, tax or discount arithmetic in the frontend. Format what the server returned (BR-PRC-01). Amounts arrive as integer paise and are formatted by one shared `formatINR`. |
| TS-010 | Every async call has explicit loading and error states rendered. A silent failure is a defect. |
| TS-011 | Errors are typed (`AppError`) and normalised in `HttpClient`. `catch (e: any)` is prohibited. |
| TS-012 | Component tests use Testing Library and assert user-visible behaviour, not implementation. Snapshot-only tests do not count as coverage. |
| TS-013 | No barrel `index.ts` re-export files across module boundaries — they defeat tree-shaking and hide cycles. |
| TS-014 | Environment access is centralised in one typed config module, validated at startup. `import.meta.env` MUST NOT be read from components. |

---

## 13. Enforcement

Rules that a machine can check are checked by a machine. The rest are review items in doc 03 §8.

| Layer | Enforces |
|---|---|
| `gofumpt`, `goimports` | GO-001, GO-002 |
| `.golangci.yml` — `errcheck`, `gocyclo`, `nestif`, `revive`, `gosec`, `bodyclose`, `contextcheck`, `noctx`, `sqlclosecheck`, `rowserrcheck`, `forbidigo`, `depguard`, `nolintlint`, `exhaustive` | GO-003/004, GO-021, GO-027, GO-030–033, GO-045, GO-063, GO-080, and the import boundaries in doc 03 DRY-02 |
| `forbidigo` patterns | `time.Now` outside `platform/clock`, `math/rand`, `fmt.Print*`, `panic(` outside startup |
| `go test -race`, `go vet` | GO-055, general correctness |
| `govulncheck`, `bun audit`, `gitleaks` | GO-103, secret scanning |
| `tsc --noEmit`, Biome | TS-001–005 |
| Router self-check test | doc 03 SEC-01, TST-02 |

A rule that is repeatedly broken is either wrong or unenforced. Fix the rule or automate it — do not add another review checklist item.
