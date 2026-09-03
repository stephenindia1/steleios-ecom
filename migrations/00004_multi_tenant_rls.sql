-- +goose Up
-- +goose StatementBegin

-- Multi-tenant SaaS (ADR 0007).
--
-- ADR 0004 put tenant_id on every tenant-scoped table and left row-level
-- security written and dormant, precisely so this migration would be a switch
-- rather than a rewrite. This is that switch.
--
-- The security model: isolation is enforced by PostgreSQL, not by developers
-- remembering a WHERE clause. A query that forgets its tenant predicate returns
-- nothing instead of returning another shop's data (BR-LIC-61, ADR 0007).

-- ---------------------------------------------------------------------------
-- The application role
--
-- RLS does not apply to superusers, and by default it does not apply to a
-- table's owner either. The application therefore MUST NOT connect as either.
-- ---------------------------------------------------------------------------
do $$
begin
    if not exists (select 1 from pg_roles where rolname = 'steleios_app') then
        create role steleios_app login password 'CHANGE_ME_IN_DEPLOYMENT';
    end if;
end;
$$;

-- Least privilege: the application reads and writes rows. It does not own the
-- schema, cannot alter it, and cannot disable a policy (least privilege).
grant usage on schema public to steleios_app;
grant select, insert, update, delete on all tables in schema public to steleios_app;
grant usage, select on all sequences in schema public to steleios_app;
alter default privileges in schema public
    grant select, insert, update, delete on tables to steleios_app;

-- The audit log stays append-only for the application, at the privilege level
-- as well as by trigger (BR-ADM-05). Defence in depth: revoking the privilege
-- means a dropped trigger is not enough to rewrite history.
revoke update, delete on audit_log from steleios_app;
revoke delete on domain_events from steleios_app;

-- ---------------------------------------------------------------------------
-- Fail-closed tenant resolution
--
-- When app.tenant_id is not set, this returns NULL. Every policy compares
-- tenant_id = current_tenant_id(), and `x = NULL` is NULL, which is not true —
-- so an unset tenant sees NOTHING rather than everything. A forgotten
-- set_config is an empty result, never a cross-tenant leak (BR-SEC-11).
-- ---------------------------------------------------------------------------
create or replace function current_tenant_id() returns uuid
language plpgsql stable as $$
declare
    raw text := current_setting('app.tenant_id', true);
begin
    if raw is null or raw = '' then
        return null;   -- denies every row; never a wildcard
    end if;
    return raw::uuid;
exception
    when invalid_text_representation then
        -- A malformed tenant id is a bug or an attack. Deny rather than error
        -- out of a policy, which would be indistinguishable from an outage.
        return null;
end;
$$;

-- ---------------------------------------------------------------------------
-- Policies
--
-- FORCE is essential: without it the table owner bypasses RLS, and the owner is
-- exactly who runs migrations and ad-hoc queries in production.
-- ---------------------------------------------------------------------------

alter table staff                  enable row level security;
alter table staff                  force  row level security;
alter table staff_role_assignments enable row level security;
alter table staff_role_assignments force  row level security;
alter table audit_log              enable row level security;
alter table audit_log              force  row level security;
alter table domain_events          enable row level security;
alter table domain_events          force  row level security;
alter table webhook_events         enable row level security;
alter table webhook_events         force  row level security;
alter table licences               enable row level security;
alter table licences               force  row level security;

create policy tenant_isolation on staff
    using (tenant_id = current_tenant_id())
    with check (tenant_id = current_tenant_id());

create policy tenant_isolation on staff_role_assignments
    using (tenant_id = current_tenant_id())
    with check (tenant_id = current_tenant_id());

create policy tenant_isolation on audit_log
    using (tenant_id = current_tenant_id())
    with check (tenant_id = current_tenant_id());

create policy tenant_isolation on domain_events
    using (tenant_id = current_tenant_id())
    with check (tenant_id = current_tenant_id());

create policy tenant_isolation on webhook_events
    using (tenant_id = current_tenant_id())
    with check (tenant_id = current_tenant_id());

create policy tenant_isolation on licences
    using (tenant_id = current_tenant_id())
    with check (tenant_id = current_tenant_id());

-- A tenant sees only its own row. Provisioning a new tenant is a vendor
-- operation performed by the owning role, not by the application.
alter table tenants enable row level security;
alter table tenants force  row level security;

create policy tenant_self on tenants
    using (id = current_tenant_id())
    with check (id = current_tenant_id());

-- Global reference data stays readable by every tenant and writable by none:
-- units of measure and role definitions are identical for everyone, and
-- copying them per tenant would create drift (ADR 0004).
revoke insert, update, delete on uoms        from steleios_app;
revoke insert, update, delete on staff_roles from steleios_app;

-- The time anchor is installation-wide, not tenant-scoped, and the application
-- only ever moves it forward.
revoke delete on time_anchor from steleios_app;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop policy if exists tenant_self on tenants;
drop policy if exists tenant_isolation on licences;
drop policy if exists tenant_isolation on webhook_events;
drop policy if exists tenant_isolation on domain_events;
drop policy if exists tenant_isolation on audit_log;
drop policy if exists tenant_isolation on staff_role_assignments;
drop policy if exists tenant_isolation on staff;
alter table tenants                no force row level security;
alter table tenants                disable row level security;
alter table licences               no force row level security;
alter table licences               disable row level security;
alter table webhook_events         no force row level security;
alter table webhook_events         disable row level security;
alter table domain_events          no force row level security;
alter table domain_events          disable row level security;
alter table audit_log              no force row level security;
alter table audit_log              disable row level security;
alter table staff_role_assignments no force row level security;
alter table staff_role_assignments disable row level security;
alter table staff                  no force row level security;
alter table staff                  disable row level security;
-- +goose StatementEnd
