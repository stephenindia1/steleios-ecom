-- +goose Up
-- +goose StatementBegin

-- Correcting the row-level security on identities and the client tables.
--
-- ===========================================================================
-- THE BUG THIS FIXES
-- ===========================================================================
--
-- Migration 00005 put a tenant-scoped policy on `identities`:
--
--     using (exists (select 1 from staff s
--                     where s.identity_id = identities.id
--                       and s.tenant_id = current_tenant_id()))
--
-- A policy with USING and no WITH CHECK has its USING expression applied as
-- WITH CHECK for writes. So creating an identity required it to ALREADY have a
-- membership in the current shop — which a brand new user cannot have. Two
-- things were therefore impossible:
--
--   1. CREATING A USER. Onboarding an owner, adding a staff member: refused
--      with "new row violates row-level security policy".
--
--   2. SIGNING IN. Resolving an identity by email happens BEFORE a shop is
--      known — that is the whole point of a shop switcher. With no tenant set,
--      current_tenant_id() is NULL, the policy matches nothing, and the lookup
--      returns no rows. Every login would fail.
--
-- Migration 00005's own comment said "authentication necessarily happens before
-- a tenant is chosen" and then applied a tenant policy anyway. The comment was
-- right and the policy was wrong.
--
-- The correction: an identity is NOT tenant-scoped. One identity can belong to
-- several shops (migration 00005), so scoping it to one is a category error.
--
-- What actually protects identities:
--   - an identity alone grants NOTHING. Access comes from staff membership,
--     which IS tenant-scoped and still behind row-level security.
--   - the identity module is the only code that reaches them, through the
--     explicitly-named ReadSystem/DoSystem path (ADR 0007).
--   - they hold no business data — no orders, no customers, no money.

drop policy if exists identity_visible_through_membership on identities;

alter table identities no force row level security;
alter table identities disable row level security;

comment on table identities is
    'Logins. Deliberately NOT tenant-scoped: authentication precedes tenancy, and one identity may hold memberships in several shops. Protection comes from staff membership, which is tenant-scoped. See migration 00016.';

-- ---------------------------------------------------------------------------
-- The client tables had the same defect, for the same reason.
--
-- A client, its documents, its owners and its acceptances are all created
-- during ONBOARDING, before any shop exists to scope them to. Their USING
-- policies were being applied as WITH CHECK, so onboarding a client was
-- refused exactly as creating a user was.
--
-- They keep their read policies — a shop sees its own client and no other —
-- and gain explicit write policies. Writes are already gated by authorization:
-- client:manage is a PLATFORM action that no shop role holds (BR-ADM-14), so
-- the check that matters happens before the query is built.
-- ---------------------------------------------------------------------------

drop policy if exists client_of_current_tenant on clients;

create policy client_read_of_current_tenant on clients
    for select using (
        exists (select 1 from tenants t
                 where t.client_id = clients.id
                   and t.id = current_tenant_id())
    );

-- Onboarding creates a client before any shop exists to scope it to.
create policy client_write on clients for insert with check (true);
create policy client_update on clients for update using (true) with check (true);

drop policy if exists group_of_current_tenant on store_groups;

create policy group_read_of_current_tenant on store_groups
    for select using (
        exists (select 1 from tenants t
                 where t.group_id = store_groups.id
                   and t.id = current_tenant_id())
    );

create policy group_write on store_groups for insert with check (true);
create policy group_update on store_groups for update using (true) with check (true);

drop policy if exists acceptance_of_current_tenant on client_acceptances;

create policy acceptance_read_of_current_tenant on client_acceptances
    for select using (
        exists (select 1 from tenants t
                 where t.client_id = client_acceptances.client_id
                   and t.id = current_tenant_id())
    );

create policy acceptance_write on client_acceptances for insert with check (true);

-- Tenants themselves: a shop reads its own row, and provisioning creates them.
drop policy if exists tenant_self on tenants;

create policy tenant_read_self on tenants
    for select using (id = current_tenant_id());

create policy tenant_write on tenants for insert with check (true);
create policy tenant_update on tenants for update using (true) with check (true);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop policy if exists tenant_update on tenants;
drop policy if exists tenant_write on tenants;
drop policy if exists tenant_read_self on tenants;
create policy tenant_self on tenants using (id = current_tenant_id()) with check (id = current_tenant_id());

drop policy if exists acceptance_write on client_acceptances;
drop policy if exists acceptance_read_of_current_tenant on client_acceptances;
create policy acceptance_of_current_tenant on client_acceptances
    using (exists (select 1 from tenants t where t.client_id = client_acceptances.client_id and t.id = current_tenant_id()));

drop policy if exists group_update on store_groups;
drop policy if exists group_write on store_groups;
drop policy if exists group_read_of_current_tenant on store_groups;
create policy group_of_current_tenant on store_groups
    using (exists (select 1 from tenants t where t.group_id = store_groups.id and t.id = current_tenant_id()));

drop policy if exists client_update on clients;
drop policy if exists client_write on clients;
drop policy if exists client_read_of_current_tenant on clients;
create policy client_of_current_tenant on clients
    using (exists (select 1 from tenants t where t.client_id = clients.id and t.id = current_tenant_id()));

alter table identities enable row level security;
alter table identities force row level security;
create policy identity_visible_through_membership on identities
    using (exists (select 1 from staff s where s.identity_id = identities.id and s.tenant_id = current_tenant_id()));
-- +goose StatementEnd
