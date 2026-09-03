-- +goose Up
-- +goose StatementBegin

-- Store groups (ADR 0007).
--
-- Only multi-store businesses use these. A single-shop client has no group and
-- every tenant.group_id stays null — the column costs a single-shop client
-- nothing and saves a schema change when they open a second branch.
--
--   client              the business
--     └── group         a set of shops: a region, a brand, a franchise
--           └── tenant  one shop
--
-- IMPORTANT: group_id is DATA, NOT A PERMISSION.
--
-- Sharing a group grants no cross-shop visibility whatsoever. Row-level
-- security remains strictly per tenant (migration 00004), and no policy in this
-- system references group_id. Cross-shop reporting is a separate system built
-- later against exported data; it is deliberately not a query path here, so
-- that this system never needs a way to read across tenants (ADR 0007).
--
-- Anyone adding a policy or query that reads across a group is reintroducing
-- exactly the leak this design prevents.

create table store_groups (
    id          uuid primary key default gen_random_uuid(),
    client_id   uuid not null references clients(id),
    code        text not null,
    name        text not null,
    -- A group may nest: region within brand, for a larger chain. Nesting is
    -- structure for the future reporting system, and confers no access here.
    parent_id   uuid references store_groups(id),
    status      text not null default 'active'
                check (status in ('active','archived')),
    created_at  timestamptz not null default now(),
    updated_at  timestamptz not null default now()
);

create unique index store_groups_code_per_client on store_groups (client_id, code);
create index store_groups_client on store_groups (client_id, status);
create index store_groups_parent on store_groups (parent_id) where parent_id is not null;

-- A group belongs to exactly one client, and so does its parent. Without this,
-- a chain could be built across two unrelated businesses.
create or replace function store_group_parent_same_client() returns trigger
language plpgsql as $$
declare
    parent_client uuid;
begin
    if new.parent_id is null then
        return new;
    end if;
    if new.parent_id = new.id then
        raise exception 'a store group cannot be its own parent';
    end if;
    select client_id into parent_client from store_groups where id = new.parent_id;
    if parent_client is distinct from new.client_id then
        raise exception 'a store group and its parent must belong to the same client';
    end if;
    return new;
end;
$$;

create trigger store_groups_parent_client
    before insert or update on store_groups
    for each row execute function store_group_parent_same_client();

-- Null for a single-shop business; set once a client has more than one store.
alter table tenants add column group_id uuid references store_groups(id);

create index tenants_group on tenants (group_id) where group_id is not null;

-- A shop's group must belong to the shop's own client.
create or replace function tenant_group_same_client() returns trigger
language plpgsql as $$
declare
    group_client uuid;
begin
    if new.group_id is null then
        return new;
    end if;
    select client_id into group_client from store_groups where id = new.group_id;
    if group_client is distinct from new.client_id then
        raise exception 'a shop and its store group must belong to the same client';
    end if;
    return new;
end;
$$;

create trigger tenants_group_client
    before insert or update on tenants
    for each row execute function tenant_group_same_client();

-- Readable by the shops in it, on the same basis as their client: a shop may
-- know which group it is in. It may not see the other shops in that group.
alter table store_groups enable row level security;
alter table store_groups force  row level security;

create policy group_of_current_tenant on store_groups
    using (
        exists (
            select 1 from tenants t
             where t.group_id = store_groups.id
               and t.id = current_tenant_id()
        )
    );

grant select on store_groups to steleios_app;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop policy if exists group_of_current_tenant on store_groups;
alter table store_groups no force row level security;
alter table store_groups disable row level security;
drop trigger if exists tenants_group_client on tenants;
drop function if exists tenant_group_same_client();
drop index if exists tenants_group;
alter table tenants drop column if exists group_id;
drop trigger if exists store_groups_parent_client on store_groups;
drop function if exists store_group_parent_same_client();
drop table if exists store_groups;
-- +goose StatementEnd
