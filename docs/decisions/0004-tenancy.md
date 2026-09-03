# ADR 0004 — Single-tenant now, multi-tenant-ready from the first migration

Date: 3 September 2026 · Status: accepted · Decided by: the owner
Relates to [docs/09 §6](../09-licensing-and-activation.md)

## Decision

Steleios ships **single-tenant**: each shop gets its own installation and its own database, and the activation code opens that installation.

But **every tenant-scoped table carries a `tenant_id` from its very first migration**, and every repository query carries a tenant predicate — even though, today, that value is a constant.

## Why not just build single-tenant

Because the change later is not a change of code, it is a change of every table. Retrofitting tenancy onto a live schema means adding a column to every table, backfilling it, adding it to every index, adding it to every query, and adding it to every authorization check. Each of those is a place to miss one — and a missed tenant predicate in a multi-tenant system is one shop reading another shop's orders. That is a data breach, not a bug.

Paying for one unused column per table now removes that entire class of migration risk later.

## Why not just build multi-tenant

Because today it buys nothing and costs the strongest isolation guarantee available. With separate databases, a query bug **cannot** leak across customers — the data is not reachable. With logical isolation, that safety depends on getting every predicate right, forever, including in every ad-hoc query anyone ever runs against production.

Until there is an operational reason to consolidate, physical separation is simply a better security posture, and it is free.

## What this means in practice

| | Today (single-tenant) | Later (multi-tenant) |
|---|---|---|
| Deployment | One per shop | One for many |
| `tenant_id` | Present, constant, seeded as the installation's own tenant | Real, one row per shop |
| Isolation | Physical — separate databases | Logical — PostgreSQL row-level security |
| Enforcement | The predicate is already in every query | The same predicates start doing work |
| Licence | Per installation | Per tenant |

**The move from one column to the other is switching row-level security on and making the tenant value vary. It is not a schema rewrite.**

## Rules this establishes

- Every tenant-scoped table carries `tenant_id`, not null, with a foreign key to `tenants`.
- Every index on a tenant-scoped table leads with `tenant_id`, so it is already the right shape for a multi-tenant query plan (DB-003).
- Every repository query filters on tenant. The tenant comes from the request context, never from a request field.
- Genuinely global reference data — units of measure, GST rates, role definitions, country data — is **not** tenant-scoped. It is the same for everyone, and copying it per tenant would create drift.
- Row-level security policies are written now and left permissive in single-tenant mode, so switching them on is a configuration change rather than new code.

## Consequences

- A small, permanent tax: one column, one index prefix, one predicate. Deliberate.
- The repository layer must make the tenant predicate hard to omit rather than relying on discipline (docs/09 BR-LIC-61). Relying on every developer remembering `where tenant_id = $1` is not an isolation strategy.
- Cross-tenant reporting for the vendor is not possible from a single installation, and should not be: aggregate telemetry, if wanted, is a separate opt-in mechanism, not a query across customers' data.

## Revisiting

The trigger to move is operational, not technical: when the cost of upgrading N installations exceeds the cost of running logical isolation carefully. Because the schema is already shaped for it, that will be a decision about operations rather than a rewrite.
