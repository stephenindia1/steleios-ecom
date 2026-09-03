-- +goose Up
-- +goose StatementBegin

-- ===========================================================================
-- WHERE VENDOR STAFF LIVE
-- ===========================================================================
--
-- BR-ADM-14 says the two worlds of roles are disjoint: a platform role holds no
-- shop action, a shop role holds no platform action. Until now that was asserted
-- by a Go test and by nothing else, and the SCHEMA had no opinion — a saas_admin
-- was a row in `staff`, which is tenant-scoped, so the vendor's own
-- administrator held a membership inside a client's business. Exactly the shape
-- the rule forbids, arrived at because there was nowhere else to put them.
--
-- Two changes, and the second is the one that matters:
--
--   1. platform_users is where vendor staff live. No tenant_id, because the
--      vendor operates the SaaS rather than a shop.
--
--   2. The disjointness becomes a DATABASE CONSTRAINT rather than a test.
--      staff_roles gains a `world` column, and each assignment table carries a
--      constant world matched by a composite foreign key. Granting saas_admin
--      to a shop worker is then not a bug to be caught in review — it is a
--      foreign key violation.
--
-- The test stays. A constraint the code cannot violate is better than a test,
-- and having both is better than either: the constraint stops it happening, the
-- test says why when someone tries to remove the constraint.

-- ---------------------------------------------------------------------------
-- 1. Which world each role belongs to
-- ---------------------------------------------------------------------------

alter table staff_roles add column world text not null default 'shop'
    check (world in ('shop', 'platform'));

update staff_roles set world = 'platform' where code in ('saas_admin', 'saas_support');

-- The composite key the assignment tables point at. A plain FK to code alone
-- cannot express "and it must be a shop role"; this one can.
alter table staff_roles add constraint staff_roles_code_world_key unique (code, world);

comment on column staff_roles.world is
    'Which world the role belongs to: shop roles operate a business, platform roles operate the SaaS. Referenced by the assignment tables so BR-ADM-14 is enforced by the database, not only by review.';

-- ---------------------------------------------------------------------------
-- 2. Vendor staff
-- ---------------------------------------------------------------------------

create table platform_users (
    id          uuid primary key default gen_random_uuid(),
    -- The same identities table as everyone else. Authentication is one
    -- mechanism; what differs is what the identity is a member OF (migration
    -- 00016). A platform user is a membership with no tenant.
    identity_id uuid not null unique references identities (id),
    status      text not null default 'active'
                check (status in ('active', 'suspended', 'disabled')),
    created_at  timestamptz not null default now(),
    created_by  uuid references platform_users (id),
    updated_at  timestamptz not null default now()
);

create index platform_users_active on platform_users (status) where status = 'active';
create index platform_users_created_by on platform_users (created_by) where created_by is not null;

comment on table platform_users is
    'Vendor staff who operate the SaaS. Deliberately NOT in `staff`: that table is tenant-scoped, so a vendor administrator there would hold a membership inside a client''s business (BR-ADM-14, docs/09 §6). See migration 00019.';

create table platform_role_assignments (
    platform_user_id uuid not null references platform_users (id) on delete cascade,
    role_code        text not null,
    world            text not null default 'platform' check (world = 'platform'),
    granted_at       timestamptz not null default now(),
    granted_by       uuid not null references platform_users (id),
    primary key (platform_user_id, role_code),
    constraint platform_role_assignments_role_is_a_platform_role
        foreign key (role_code, world) references staff_roles (code, world)
);

create index platform_role_assignments_role       on platform_role_assignments (role_code);
create index platform_role_assignments_granted_by on platform_role_assignments (granted_by);

-- ---------------------------------------------------------------------------
-- 3. Row-level security
--
-- Neither table is tenant-scoped, so neither carries a tenant policy — the same
-- reasoning as `identities` (migration 00016). What protects them is that no
-- shop role holds a platform action, so nothing a client's staff can do reaches
-- this data, and the vendor-side module is the only code that reads it.
--
-- RLS is left OFF rather than enabled with a permissive policy, because an
-- `using (true)` policy reads like protection and is not.
-- ---------------------------------------------------------------------------

-- ---------------------------------------------------------------------------
-- 4. Move the vendor staff that were seeded into a client's shop
--
-- Local development only in practice; written as a general statement so any
-- environment that made the same mistake is corrected rather than left in a
-- state the new constraint forbids.
-- ---------------------------------------------------------------------------

do $$
declare
    misplaced uuid;
    moved     uuid;
begin
    for misplaced in
        select distinct s.id
          from staff s
          join staff_role_assignments a on a.staff_id = s.id
         where a.role_code in ('saas_admin', 'saas_support')
    loop
        insert into platform_users (identity_id, status)
        select s.identity_id, s.status from staff s where s.id = misplaced
        on conflict (identity_id) do update set status = excluded.status
        returning id into moved;

        insert into platform_role_assignments (platform_user_id, role_code, granted_by)
        select moved, a.role_code, moved
          from staff_role_assignments a
         where a.staff_id = misplaced
        on conflict do nothing;

        delete from staff_role_assignments where staff_id = misplaced;
        delete from staff where id = misplaced;
    end loop;
end
$$;

-- ---------------------------------------------------------------------------
-- 5. Shop assignments may only carry shop roles
--
-- This comes AFTER the move above, and the order is load-bearing: the rows
-- being moved are shop assignments carrying a platform role, which is precisely
-- what this constraint forbids. Adding it first fails on the very data it
-- exists to prevent.
-- ---------------------------------------------------------------------------

-- A constant column, not a settable one: it exists to be half of the foreign
-- key. The check pins it so no future insert can supply something else.
alter table staff_role_assignments
    add column world text not null default 'shop' check (world = 'shop');

alter table staff_role_assignments
    add constraint staff_role_assignments_role_is_a_shop_role
    foreign key (role_code, world) references staff_roles (code, world);

grant select, insert, update, delete on platform_users, platform_role_assignments to steleios_app;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop table if exists platform_role_assignments;
drop table if exists platform_users;

alter table staff_role_assignments drop constraint if exists staff_role_assignments_role_is_a_shop_role;
alter table staff_role_assignments drop column if exists world;

alter table staff_roles drop constraint if exists staff_roles_code_world_key;
alter table staff_roles drop column if exists world;
-- +goose StatementEnd
