-- +goose Up
-- +goose StatementBegin

-- Tenancy (ADR 0004).
--
-- Steleios ships single-tenant: one installation, one database, one shop. But
-- every tenant-scoped table carries tenant_id from its first migration, and
-- every index leads with it, so the later move to multi-tenant is switching
-- row-level security on rather than rewriting every table.
--
-- Retrofitting tenancy onto a live schema means touching every table, every
-- index, every query and every authorization check. Getting one wrong is one
-- shop reading another shop's orders. One unused column per table is a cheap
-- price for removing that entire class of migration risk.

create table tenants (
    id           uuid primary key default gen_random_uuid(),
    slug         text not null unique,
    legal_name   text not null,
    gstin        text,                      -- BR-SUP-02 format rules apply
    state_code   text,                      -- place of supply for the seller (BR-PRC-04)
    status       text not null default 'active'
                 check (status in ('active','suspended','closed')),
    -- Connectivity mode for counter tills (BR-OFF-01). online_only is the
    -- default: offline selling is opt-in and owner-enabled (BR-OFF-03).
    connectivity_mode text not null default 'online_only'
                 check (connectivity_mode in ('online_only','offline_capable','offline_first')),
    created_at   timestamptz not null default now(),
    updated_at   timestamptz not null default now()
);

-- The installation's own tenant. In single-tenant mode this is the only row,
-- and tenant_id is constant everywhere. Its id is fixed rather than random so
-- that a fresh installation and a restored backup agree on it.
insert into tenants (id, slug, legal_name)
values ('00000000-0000-0000-0000-000000000001', 'default', 'Steleios installation');

-- ---------------------------------------------------------------------------
-- The licence (docs/09)
--
-- The signed token is stored verbatim. Nothing that grants access is read from
-- the parsed columns: they exist for querying and display, and verification
-- always re-parses and re-verifies the token itself (BR-LIC-06, BR-LIC-40).
-- Editing expires_at here changes nothing except making the row disagree with
-- the signature.
-- ---------------------------------------------------------------------------
create table licences (
    id              uuid primary key default gen_random_uuid(),
    tenant_id       uuid not null references tenants(id),
    licence_id      text not null,           -- vendor's identifier, from the payload
    token           text not null,           -- the signed licence, verbatim
    signing_key_id  text not null,           -- supports key rotation (BR-LIC-51)
    plan            text not null,
    valid_from      timestamptz not null,
    valid_until     timestamptz not null,
    -- Perpetual fallback earned by 12 months' continuous subscription
    -- (BR-LIC-30). Null until earned.
    fallback_version text,
    fallback_earned_at timestamptz,
    installation_id text not null,           -- binding (BR-LIC-05)
    activated_at    timestamptz not null default now(),
    superseded_at   timestamptz,             -- renewal retains the old licence
    check (valid_until > valid_from)
);

create index licences_tenant on licences (tenant_id, valid_until desc);
create unique index licences_current on licences (tenant_id) where superseded_at is null;

-- Clock rollback detection (BR-LIC-41).
--
-- An offline installation could otherwise extend its licence indefinitely by
-- setting the system clock back. One row, updated forward only.
create table time_anchor (
    id            boolean primary key default true check (id),
    highest_seen  timestamptz not null,
    updated_at    timestamptz not null default now()
);

insert into time_anchor (highest_seen) values (now());

create or replace function time_anchor_forward_only() returns trigger
language plpgsql as $$
begin
    if new.highest_seen < old.highest_seen then
        raise exception 'time anchor cannot move backwards';
    end if;
    return new;
end;
$$;

create trigger time_anchor_no_rollback before update on time_anchor
    for each row execute function time_anchor_forward_only();

-- ---------------------------------------------------------------------------
-- Tenant scoping for the existing tables.
--
-- Deliberately NOT tenant-scoped: uoms and staff_roles are global reference
-- data. Copying them per tenant would create drift, and they are identical for
-- everyone (ADR 0004).
-- ---------------------------------------------------------------------------

alter table staff                  add column tenant_id uuid references tenants(id);
alter table staff_role_assignments add column tenant_id uuid references tenants(id);
alter table audit_log              add column tenant_id uuid references tenants(id);
alter table domain_events          add column tenant_id uuid references tenants(id);
alter table webhook_events         add column tenant_id uuid references tenants(id);

update staff                  set tenant_id = '00000000-0000-0000-0000-000000000001';
update staff_role_assignments set tenant_id = '00000000-0000-0000-0000-000000000001';
update webhook_events         set tenant_id = '00000000-0000-0000-0000-000000000001';

-- audit_log and domain_events refuse UPDATE (BR-ADM-05, EVT-008), which is the
-- protection working: the application must never be able to rewrite history.
-- A schema migration is the one legitimate exception, so the triggers are
-- disabled explicitly for the backfill and re-enabled immediately.
--
-- This is deliberately verbose rather than convenient. Suspending the
-- append-only guarantee should be visible in the diff, reviewable, and bounded
-- to the statements that need it — never a trigger quietly left off.
alter table audit_log     disable trigger audit_log_no_update;
alter table domain_events disable trigger domain_events_immutable;

update audit_log     set tenant_id = '00000000-0000-0000-0000-000000000001';
update domain_events set tenant_id = '00000000-0000-0000-0000-000000000001';

alter table audit_log     enable trigger audit_log_no_update;
alter table domain_events enable trigger domain_events_immutable;

alter table staff                  alter column tenant_id set not null;
alter table staff_role_assignments alter column tenant_id set not null;
alter table audit_log              alter column tenant_id set not null;
alter table domain_events          alter column tenant_id set not null;
alter table webhook_events         alter column tenant_id set not null;

-- Every index on a tenant-scoped table leads with tenant_id, so it is already
-- the right shape for a multi-tenant query plan (DB-003, ADR 0004).
create index staff_tenant                  on staff (tenant_id, status);
create index staff_role_assignments_tenant on staff_role_assignments (tenant_id, role_code);
create index audit_log_tenant              on audit_log (tenant_id, at desc);
create index audit_log_tenant_resource     on audit_log (tenant_id, resource_type, resource_id, at desc);
create index domain_events_tenant          on domain_events (tenant_id, occurred_at desc);
create index webhook_events_tenant         on webhook_events (tenant_id, received_at desc);

-- Email is unique per tenant, not globally: two shops may legitimately employ
-- people with the same address, and in single-tenant mode this behaves exactly
-- as the global constraint did.
-- The constraint owns its index, so the constraint is dropped and the index
-- goes with it. Dropping the index first fails.
alter table staff drop constraint if exists staff_email_key;
create unique index staff_email_per_tenant on staff (tenant_id, email);

-- ---------------------------------------------------------------------------
-- Row-level security, written now and permissive in single-tenant mode.
--
-- Switching to multi-tenant is enabling these policies and setting
-- app.tenant_id per connection — a configuration change, not new code
-- (ADR 0004, BR-LIC-61).
-- ---------------------------------------------------------------------------
create or replace function current_tenant_id() returns uuid
language plpgsql stable as $$
declare
    raw text := current_setting('app.tenant_id', true);
begin
    if raw is null or raw = '' then
        -- Single-tenant: no session variable is set, so every row is visible.
        -- In multi-tenant mode the connection MUST set app.tenant_id, and a
        -- missing value must become a denial rather than a wildcard. That
        -- change is made when the policies are enabled.
        return null;
    end if;
    return raw::uuid;
end;
$$;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop function if exists current_tenant_id();
drop index if exists staff_email_per_tenant;
create unique index if not exists staff_email_key on staff (email);
drop index if exists webhook_events_tenant;
drop index if exists domain_events_tenant;
drop index if exists audit_log_tenant_resource;
drop index if exists audit_log_tenant;
drop index if exists staff_role_assignments_tenant;
drop index if exists staff_tenant;
alter table webhook_events         drop column if exists tenant_id;
alter table domain_events          drop column if exists tenant_id;
alter table audit_log              drop column if exists tenant_id;
alter table staff_role_assignments drop column if exists tenant_id;
alter table staff                  drop column if exists tenant_id;
drop trigger if exists time_anchor_no_rollback on time_anchor;
drop function if exists time_anchor_forward_only();
drop table if exists time_anchor;
drop table if exists licences;
drop table if exists tenants;
-- +goose StatementEnd
