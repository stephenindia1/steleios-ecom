# ADR 0007 — Multi-tenant SaaS: client, group, shop

Date: 3 September 2026 · Status: accepted · Decided by: the owner
**Supersedes the timing of [ADR 0004](0004-tenancy.md)** · builds on [ADR 0006](0006-fully-online.md)

## Decision

Steleios is a **multi-tenant SaaS**. One deployment serves every business.

```
client            the business that buys Steleios — has a client code (STL-C-000001)
  └── group       OPTIONAL, multi-store businesses only — a region, brand or franchise
        └── tenant    one SHOP. An owner with two shops has two tenants.
              └── staff   a person's membership of that shop, with roles

identity          a login. One person, one password, possibly several shops.
```

**The unit of isolation is the shop.** Not the client, not the group.

## Why the shop, and not the business

Because shop workers must not be able to reach another shop's data — the till operator at the branch has no business seeing the main store's orders, customers or takings, even though the same owner owns both.

Isolating at the client level would have made every shop in a chain mutually visible, which is the opposite of what a chain owner wants. Isolating at the shop level and letting an *owner* hold memberships in several shops gives both: one login for the owner, complete separation for everyone else.

## group_id is data, not a permission

`tenants.group_id` exists so a future reporting system can aggregate a chain. **It grants nothing.**

No row-level security policy in this system references it. No query reads across it. Sharing a group gives a shop no visibility into its siblings whatsoever — verified below.

**Cross-shop reporting is a separate system, built later, against exported data.** Keeping it out means this system never needs a way to read across tenants, so there is no bypass path to protect, misuse or accidentally widen. Anyone adding a policy or query that reads across a group is reintroducing exactly the leak this design prevents.

## How isolation is enforced

By PostgreSQL, not by developers remembering a `WHERE` clause.

| Mechanism | Why |
|---|---|
| Row-level security on every tenant-scoped table | The database refuses, so a forgotten predicate cannot leak |
| `FORCE ROW LEVEL SECURITY` | Without it a table's owner bypasses RLS — and the owner is exactly who runs migrations and ad-hoc production queries |
| A dedicated `steleios_app` role, not superuser, not the table owner | Superusers and owners bypass RLS |
| `current_tenant_id()` returns NULL when unset | `tenant_id = NULL` is never true, so an unset tenant sees **nothing**. A forgotten `set_config` is an empty result, never a cross-tenant leak. |
| `WITH CHECK` on every policy | Stops a tenant *writing* a row it could not read |
| `REVOKE UPDATE, DELETE ON audit_log` | Append-only at the privilege level as well as by trigger, so a dropped trigger is not enough to rewrite history |

## Verified, not asserted

Run against a real database with two shops:

| Property | Result |
|---|---|
| Shop A reads staff | Sees only shop A |
| Shop B reads staff | Sees only shop B |
| **No tenant set** | **0 rows** — fails closed |
| Shop A inserts a row owned by shop B | `new row violates row-level security policy` |
| Shop A updates shop B's row | No rows affected; B's data intact |
| App role rewrites the audit log | `permission denied` — before the trigger even fires |
| Owner with two shops, signed in to one | Sees one membership and one shop, not two |
| Two shops in the same group, one signed in | **1 shop visible** — the group grants nothing |

## Consequences

- **The tenant must be set transaction-locally.** With a connection pool, a plain `SET` persists on the connection and would leak into the next request — potentially a different tenant. Every transaction sets `app.tenant_id` with `set_config(..., true)`, and the repository layer makes this impossible to skip rather than a thing to remember.
- **The tenant comes from the session, never from the request.** A tenant id in a body, a query parameter or a header is untrusted input and MUST NOT reach `set_config` (BR-SEC-02).
- **Cross-tenant isolation tests are mandatory** for every new tenant-scoped table: read, write, and the unset case. A table added without a policy is a leak, so the test suite must fail when one is missing rather than relying on review.
- **Identity lookup is the one path that legitimately runs outside tenant context** — authentication cannot know which shop someone is signing in to until it knows who they are. It is confined to the identity module and grants nothing by itself: access comes from membership, which is tenant-scoped.
- **ADR 0004 is vindicated rather than reversed.** It put `tenant_id` on every table and left the policies written and dormant precisely so this would be a switch. It was: one migration enabling RLS, no table rewritten, no query rewritten. The column that looked like overhead in Phase 1 is what made this a morning's work instead of a project.
- Backups, upgrades and availability are now vendor obligations for every customer at once. A bad deploy is every shop's bad deploy — staged rollout and fast rollback stop being good practice and become requirements.

## Revisiting

A customer needing physical isolation — a regulatory requirement, or a chain large enough to want its own deployment — can be given a dedicated deployment without any schema change, because the schema is identical. That is the reverse of the usual migration problem, and it is worth keeping.
