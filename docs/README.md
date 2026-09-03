# Steleios — Specification Index

The engraved, binding rules for this project. [`../CLAUDE.md`](../CLAUDE.md) is the always-loaded summary; these documents are the detail.

| Doc | Governs | Rule prefixes |
|---|---|---|
| [01-architecture.md](01-architecture.md) | System design, module boundaries, schema shapes, Razorpay flow, build order, open decisions | — |
| [02-features-and-business-rules.md](02-features-and-business-rules.md) | **All functional behaviour** — the source of truth | `BR-*` |
| [03-code-standards-and-patterns.md](03-code-standards-and-patterns.md) | OOP, module factory, middleware, authorization, DRY registry | `OOP-*` `MOD-*` `SEC-*` `FE-*` `WRK-*` `DRY-*` `TST-*` |
| [04-go-and-typescript-standards.md](04-go-and-typescript-standards.md) | Language standards, idiomatic Go, code quality | `GO-*` `TS-*` |
| [05-data-access-and-performance.md](05-data-access-and-performance.md) | Query complexity, Redis, sessions, queue, budgets | `DB-*` `RD-*` `SES-*` `QUE-*` `API-*` `PRF-*` |
| [06-observability-and-event-logging.md](06-observability-and-event-logging.md) | Logging, domain events, audit, metrics, tracing, probing | `OBS-*` `EVT-*` `LOG-*` `MET-*` `TRC-*` `PRB-*` `HLT-*` |
| [07-seo-and-ai-discoverability.md](07-seo-and-ai-discoverability.md) | SSR, structured data, crawl architecture, AI answer surfaces | `SEO-*` |
| [08-design-system.md](08-design-system.md) | Pastel palette with verified contrast, multi-tone icons, type and motion | `DS-*` |
| [09-licensing-and-activation.md](09-licensing-and-activation.md) | Activation codes, offline verification, entitlements, perpetual fallback | `BR-LIC-*` |
| [10-deployment-and-installation.md](10-deployment-and-installation.md) | Hosted instance per shop, local till client, PostgreSQL-only profile | `DEP-*` |

## Decisions

| ADR | Decision |
|---|---|
| [0001](decisions/0001-toolchain-and-compatibility.md) | Go 1.27.1, TypeScript 7.0.2, and the checked frontend/backend contract |
| [0002](decisions/0002-online-only-counter-sales.md) | *Superseded by 0003* — counter online-only |
| [0003](decisions/0003-offline-counter-sales.md) | Offline counter sales via stock leases; online-only is the default |
| [0004](decisions/0004-tenancy.md) | Single-tenant now, `tenant_id` everywhere from the first migration |
| [0005](decisions/0005-hosted-instances-local-tills.md) | Vendor-hosted instance per shop, local till client, PostgreSQL-only |

## Business rule sections (doc 02)

| § | Area | Prefix |
|---|---|---|
| 1 | Catalog | `BR-CAT-*` |
| 2 | Inventory | `BR-INV-*` |
| 2A | Suppliers, batches, expiry, FEFO, markdown | `BR-SUP-*` `BR-BAT-*` |
| 2B | Units of measure and conversion | `BR-UOM-*` |
| 3 | Pricing and tax | `BR-PRC-*` |
| 3A | GST rate versioning, effective-dated reference data | `BR-TAX-*` `BR-VER-*` |
| 4 | Cart | `BR-CRT-*` |
| 5 | Discounts and coupons | `BR-DSC-*` |
| 5A | Campaigns | `BR-CMP-*` |
| 5B | Loyalty points | `BR-LOY-*` |
| 6 | Customer identity | `BR-IDN-*` |
| 7 | Addresses and serviceability | `BR-ADR-*` |
| 8 | Checkout | `BR-CHK-*` |
| 9 | Payments, Razorpay, COD | `BR-PAY-*` `BR-COD-*` |
| 10 | Order lifecycle | `BR-ORD-*` |
| 11 | Fulfilment and shipping | `BR-FUL-*` |
| 12 | Customer returns, cancellations, refunds | `BR-RET-*` |
| 12A | Returns to supplier (RTV) | `BR-RTV-*` |
| 13 | Reviews | `BR-REV-*` |
| 14 | Notifications | `BR-NOT-*` |
| 15 | Admin, roles, audit | `BR-ADM-*` |
| 16 | Reporting | `BR-RPT-*` |
| 17 | Cross-cutting security | `BR-SEC-*` |
| 18 | Data retention and compliance | `BR-DAT-*` |

## Conventions

- Rule IDs are **stable forever**. Never renumber; deprecate instead.
- Severity tags: `[MONEY]` financial loss or wrong invoice · `[SEC]` vulnerability · `[LEGAL]` required by Indian law.
- Cite rule IDs in code comments, test names and PR descriptions.
- A rule needing a decision is listed in [02 Appendix A](02-features-and-business-rules.md) or [01 §8](01-architecture.md). Decisions, once made, are recorded in `decisions/` with the date and reasoning.

## Still to write

- `decisions/` — architecture decision records, starting with the open decisions in 01 §8, the AI crawler policy (SEO-060), the loyalty GST treatment (BR-LOY-07) and the time-of-supply determination (BR-TAX-03).
- `runbooks/` — the saved probe queries required by PRB-002.
