# Steleios — Engraved Rules

**These rules are binding on every contributor and every AI session working in this repository. They are not suggestions, defaults, or starting points. Code that violates them does not merge.**

Project: Indian D2C commerce platform.
Stack: **Go** backend · **Vue 3 + Pinia** frontend (Bun, Vite; Nuxt 3 SSR for the storefront) · **PostgreSQL** · **Redis** (sessions, cart, queue, rate limiting, idempotency) · **Razorpay** · **INR / paise**.

---

## The specification

Read the document that governs what you are touching. Do not infer a rule that is written down.

| Doc | Governs |
|---|---|
| [docs/01-architecture.md](docs/01-architecture.md) | System design, module boundaries, schema shapes, Razorpay flow, build order |
| [docs/02-features-and-business-rules.md](docs/02-features-and-business-rules.md) | **All functional behaviour.** Numbered `BR-*` rules — the source of truth |
| [docs/03-code-standards-and-patterns.md](docs/03-code-standards-and-patterns.md) | OOP, module factory, middleware, authorization, DRY registry |
| [docs/04-go-and-typescript-standards.md](docs/04-go-and-typescript-standards.md) | Language standards, `GO-*` / `TS-*` |
| [docs/05-data-access-and-performance.md](docs/05-data-access-and-performance.md) | Query complexity, Redis, sessions, queue, `DB-*` / `RD-*` / `SES-*` / `QUE-*` |
| [docs/06-observability-and-event-logging.md](docs/06-observability-and-event-logging.md) | Logging, domain events, audit, metrics, tracing, `OBS-*` / `EVT-*` / `LOG-*` |
| [docs/07-seo-and-ai-discoverability.md](docs/07-seo-and-ai-discoverability.md) | SSR, structured data, crawl architecture, AI answer surfaces, `SEO-*` |
| [docs/08-design-system.md](docs/08-design-system.md) | Palette, icons, type, motion, `DS-*` |
| [docs/09-licensing-and-activation.md](docs/09-licensing-and-activation.md) | Activation, entitlements, expiry behaviour, `BR-LIC-*` |

Every rule has a stable ID. **Cite the ID in code comments, test names, commit messages and PR descriptions.** A rule without a citation in the code that implements it is unenforced.

---

## 1. Security first — no exceptions

1. **Authorization on every state-changing action**, checked against *this actor* for *this resource*, every time. Enforced in middleware **and** re-asserted in the service — workers and internal callers never traverse HTTP. Hiding a UI control is not access control.
2. **A route cannot be registered without a security policy.** All routes go through `httpx.Group`; every verb takes a `policy.Policy`; the zero value is invalid and panics at startup. Policies are defined only in `platform/policy/catalogue.go`.
3. **Fail closed.** If an authorization, price, stock or rate-limit check cannot complete, refuse the request. Never admit on infrastructure error.
4. **The server is the sole authority** on price, tax, discount, stock and shipping. Client input proposes; the server decides and recomputes from the database.
5. **All input is hostile.** Validate type, range, format and business rule server-side, at the boundary *and* at every state change. Client-side validation is UX only.
6. Parameterized queries only (sqlc). Escape output. CSRF on state-changing requests. Explicit request structs — no mass assignment. Argon2id passwords. Secrets from env only.
7. **Never log secrets or PII.** Redaction is centralised in `platform/logging.Redact`.
8. Constant-time comparison (`hmac.Equal`, `crypto/subtle`) for every secret, token and signature.
9. Flag security-relevant decisions explicitly in the PR so they get reviewed.

## 2. Money

10. **Every amount is `int64` paise** via `money.Paise`. Floats MUST NOT appear in any pricing, tax, discount, refund, loyalty or settlement path.
11. **Every quantity is an integer in base UoM.** Conversion factors are integers; a conversion that is not exact is a configuration error, never a rounding.
12. Rounding is round-half-up, per line, then summed — in exactly one function.
13. **Snapshot everything on the order.** Price, title, SKU, GST rate, rate-row ID, UoM, conversion factor, batch allocation, return window. An order is a historical record and MUST NOT re-read live data.
14. Idempotency keys on order creation; webhook idempotency on the provider event ID. Duplicates *will* arrive.
15. The browser payment callback is **not** proof of payment. Only a verified webhook advances an order to paid.

## 3. Architecture

16. **OOP in Go means** structs with unexported state, constructors that validate and fail closed, interfaces declared by the consumer, and **composition — never inheritance**. No package-level mutable state.
17. **Module-based factory**: each domain has one `New` naming its collaborators in the signature. Wiring is explicit code in `internal/app.Build`. **No service locator, no DI container, no `init()` registration.** Go resolves dependencies at compile time.
18. Layering is absolute: **routes → middleware → services → repositories**. Handlers decode and delegate. Repositories map rows. Business rules live only in services.
19. Cross-module access is through the consumer's interface. Importing another module's concrete types is prohibited.
20. Multi-repository atomicity goes through `UnitOfWork.Do`. `pgx.Tx` never appears in a service signature. No external I/O inside a transaction.
21. **Follow how Go works.** Where a pattern from another language fights Go's idiom, Go wins.

## 4. DRY

22. Exactly one implementation of each cross-cutting concern — see the registry in [docs/03 §6.1](docs/03-code-standards-and-patterns.md). A second one is a review rejection.
23. **Duplication that is deliberate MUST NOT be "refactored away"**: order-line snapshots, address snapshots, defence-in-depth authorization, per-consumer port interfaces. DRY applies to behaviour and rules, not to facts recorded at a point in time. See docs/03 §6.3.

## 5. Data and performance

24. Every query has a **known time and space complexity**, bounded by the size of the answer, not the size of the table. New or changed queries attach an `EXPLAIN (ANALYZE, BUFFERS)` plan to the PR.
25. No `SELECT *`. No N+1. No `OFFSET` pagination on growing tables. Every result set bounded. Every foreign key indexed. Every statement has a timeout.
26. Atomic conditional `UPDATE` over read-then-write — always, and especially for stock.
27. Redis: every key has a TTL and is built in `platform/redis/keys.go`. `KEYS` is prohibited. Cache invalidation is explicit on write, never TTL expiry.
28. Redis is a cache and coordination store. Its loss MUST degrade the system, never corrupt it.

## 6. Versioning — date based, append only

29. **Nothing that affects money or a legal document is ever updated in place.** GST rates, price lists, shipping slabs, loyalty rates, return windows, UoM factors, coupon definitions and policy documents are **effective-dated and append-only**.
30. The value in force is resolved by **date lookup against the transaction's date**, never "the current row". Overlaps and gaps are prevented by database exclusion constraints.
31. Transactional records snapshot the **version ID** they used, not just the value.
32. A mistake is corrected by a new version plus a compensating record. History is never edited.

## 7. Observability

33. A state change emits a **domain event**. An actor-initiated state change also writes an **audit entry**. Neither is replaceable by a log line.
34. Events are written in the **same transaction** as the change they describe (transactional outbox), are immutable, past-tense, and versioned.
35. **Failure and rejection paths emit events too.** A rule that silently prevents something cannot be probed.
36. `request_id` and `correlation_id` propagate into every log line, event, audit row, query comment, queue payload and outbound header — and survive retries.
37. Given an order number or an `X-Request-Id`, an engineer must be able to reconstruct what happened without adding instrumentation and redeploying.
38. Instrumentation lives in middleware and decorators. Never inside business methods.

## 8. Discovery

39. Every fact that matters to search or an AI answer surface — title, price, availability, rating, shipping, returns — is in the **server-rendered HTML**. Most AI crawlers do not execute JavaScript.
40. JSON-LD is server-rendered and generated from the same view model as the visible page. It MUST match what the customer sees.

## 9. Testing — mandatory

41. **No feature is done without tests, in the same change.**
42. Every `BR-*` rule needs at least one **passing and one failing** case, and the test names the rule. Priority: `[MONEY]` → `[SEC]` → `[LEGAL]` → state transitions → the rest.
43. Test edge cases, boundaries, invalid/missing/malformed input and error paths — not only the happy path.
44. Services are tested against fakes; repositories against real PostgreSQL via testcontainers. `go test -race` always. No `time.Sleep` for synchronisation — use the injected clock.
45. Every fixed bug gets a regression test in the same PR.

## 10. CI gates — cheapest first, no bypass

```
1  gofumpt · golangci-lint · biome
2  go vet · tsc --noEmit
3  go test -race ./... · bun test · playwright
4  govulncheck · bun audit · gitleaks
```

46. CI blocks merge. No `--no-verify`, no force-merge, unless explicitly authorized by the user.
47. Minimize dependencies — stdlib first, justify each addition, pin versions, commit lockfiles.

---

## Working agreements for AI sessions

- **Read the relevant doc before writing code.** These documents are the specification; your memory of a similar project is not.
- **Cite rule IDs** in the code you write and the tests you add.
- **When a request conflicts with an engraved rule**, say so in one or two sentences, then proceed under a stated assumption or ask — do not silently pick the convenient path. Security-relevant conflicts are always raised, never absorbed.
- **When a rule is missing** for something you are building, write the rule into the right doc as part of the change. The specification is expected to grow with the code.
- **Do not weaken a rule to make a task easier.** Propose the change explicitly instead.
- Placeholder values still needing confirmation are listed in [docs/02 Appendix A](docs/02-features-and-business-rules.md) and the open decisions in [docs/01 §8](docs/01-architecture.md). Do not silently assume one.
