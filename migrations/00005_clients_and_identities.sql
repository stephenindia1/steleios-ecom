-- +goose Up
-- +goose StatementBegin

-- Clients, shops and identities (ADR 0007).
--
-- The hierarchy the business model requires:
--
--   client    the business that buys Steleios; has a client code
--     └── tenant   one SHOP. An owner with two shops has two tenants.
--           └── staff   a person's membership of that shop, with roles
--
--   identity  a login. One person, one password, possibly several shops.
--
-- Isolation stays at the TENANT level: a shop is the unit of isolation, and
-- row-level security is enforced per tenant (migration 00004). Belonging to the
-- same client grants no cross-shop visibility — cross-shop reporting is a
-- separate system, built later, and deliberately not a query path here
-- (ADR 0007).

-- ---------------------------------------------------------------------------
-- Clients
-- ---------------------------------------------------------------------------
create table clients (
    id           uuid primary key default gen_random_uuid(),
    -- The human-facing identifier: quoted on invoices, given to support, shown
    -- in the admin header. Generated, never chosen, and never reused.
    client_code  text not null unique,
    legal_name   text not null,
    gstin        text,
    contact_email citext not null,
    contact_phone text,
    status       text not null default 'active'
                 check (status in ('active','suspended','closed')),
    created_at   timestamptz not null default now(),
    updated_at   timestamptz not null default now()
);

create index clients_status on clients (status) where status = 'active';

-- Client codes are sequential and zero-padded (STL-C-000001). Sequential is
-- fine here: a client code identifies a business to its own vendor, it is not
-- a capability and it guards nothing.
create sequence client_code_seq start 1;

create or replace function next_client_code() returns text
language sql volatile as $$
    select 'STL-C-' || lpad(nextval('client_code_seq')::text, 6, '0');
$$;

insert into clients (id, client_code, legal_name, contact_email)
values ('00000000-0000-0000-0000-0000000000c1', next_client_code(),
        'Steleios default client', 'owner@steleios.test');

-- ---------------------------------------------------------------------------
-- Tenants belong to a client. A tenant is a SHOP.
-- ---------------------------------------------------------------------------
alter table tenants add column client_id uuid references clients(id);
alter table tenants add column shop_code text;
alter table tenants add column timezone  text not null default 'Asia/Kolkata';

update tenants set client_id = '00000000-0000-0000-0000-0000000000c1'
 where client_id is null;

alter table tenants alter column client_id set not null;

create index tenants_client on tenants (client_id, status);
create unique index tenants_shop_code_per_client on tenants (client_id, shop_code)
    where shop_code is not null;

-- ---------------------------------------------------------------------------
-- Identities — a login
--
-- An identity is NOT tenant-scoped. Authentication necessarily happens before a
-- tenant is chosen: the system cannot know which shop someone is signing in to
-- until it knows who they are. This is the one legitimate path that runs
-- outside tenant context, and it is confined to the identity module
-- (ADR 0007).
--
-- An identity by itself grants nothing. Access comes from staff membership,
-- which is tenant-scoped and behind row-level security.
-- ---------------------------------------------------------------------------
create table identities (
    id             uuid primary key default gen_random_uuid(),
    email          citext not null unique,
    phone          text unique,                -- E.164
    full_name      text not null,
    password_hash  text not null,              -- Argon2id (BR-IDN-01)
    status         text not null default 'active'
                   check (status in ('active','suspended','disabled')),
    email_verified_at timestamptz,
    phone_verified_at timestamptz,
    last_login_at  timestamptz,
    failed_logins  int not null default 0 check (failed_logins >= 0),
    locked_until   timestamptz,                -- temporary only (BR-IDN-11)
    last_reauth_at timestamptz,                -- BR-ADM-07
    created_at     timestamptz not null default now(),
    updated_at     timestamptz not null default now()
);

create index identities_status on identities (status) where status = 'active';

-- ---------------------------------------------------------------------------
-- staff becomes a MEMBERSHIP: this identity, in this shop, with these roles.
--
-- One owner with two shops is one identity and two memberships — one login,
-- two tenants, and a shop switcher. Their roles may differ per shop.
-- ---------------------------------------------------------------------------
alter table staff add column identity_id uuid references identities(id) on delete restrict;

-- Migrate the existing staff rows into identities, preserving their access.
insert into identities (email, full_name, password_hash, status)
select s.email, s.full_name, s.password_hash, s.status
  from staff s
 where not exists (select 1 from identities i where i.email = s.email);

update staff s set identity_id = i.id
  from identities i
 where i.email = s.email and s.identity_id is null;

alter table staff alter column identity_id set not null;

-- Credentials now live on the identity and nowhere else. Leaving a second copy
-- on staff would mean two passwords for one person and two places to get a
-- rotation wrong.
alter table staff drop column password_hash;
alter table staff drop column locked_until;
alter table staff drop column failed_logins;
alter table staff drop column last_reauth_at;

-- One membership per identity per shop.
create unique index staff_identity_per_tenant on staff (tenant_id, identity_id);
create index staff_identity on staff (identity_id);

-- ---------------------------------------------------------------------------
-- Identities are visible only through membership of the current shop.
--
-- Auth reads identities before a tenant exists, through the identity module's
-- own privileged path. Everything after login sees only colleagues in the shop
-- it is signed in to.
-- ---------------------------------------------------------------------------
alter table identities enable row level security;
alter table identities force  row level security;

create policy identity_visible_through_membership on identities
    using (
        exists (
            select 1 from staff s
             where s.identity_id = identities.id
               and s.tenant_id = current_tenant_id()
        )
    );

-- Clients are visible to the shops that belong to them.
alter table clients enable row level security;
alter table clients force  row level security;

create policy client_of_current_tenant on clients
    using (
        exists (
            select 1 from tenants t
             where t.client_id = clients.id
               and t.id = current_tenant_id()
        )
    );

grant select, insert, update on identities to steleios_app;
grant select on clients to steleios_app;
grant usage, select on sequence client_code_seq to steleios_app;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop policy if exists client_of_current_tenant on clients;
drop policy if exists identity_visible_through_membership on identities;
alter table clients    no force row level security;
alter table clients    disable row level security;
alter table identities no force row level security;
alter table identities disable row level security;
drop index if exists staff_identity;
drop index if exists staff_identity_per_tenant;
alter table staff add column password_hash text not null default '';
alter table staff add column locked_until timestamptz;
alter table staff add column failed_logins int not null default 0;
alter table staff add column last_reauth_at timestamptz;
alter table staff drop column if exists identity_id;
drop table if exists identities;
drop index if exists tenants_shop_code_per_client;
drop index if exists tenants_client;
alter table tenants drop column if exists timezone;
alter table tenants drop column if exists shop_code;
alter table tenants drop column if exists client_id;
drop function if exists next_client_code();
drop sequence if exists client_code_seq;
drop table if exists clients;
-- +goose StatementEnd
