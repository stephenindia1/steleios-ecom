-- +goose Up
-- +goose StatementBegin

-- Staff accounts and role assignment (docs/02 §15).
--
-- Customers are NOT in this table. A customer holds no roles and is authorised
-- by ownership alone (BR-ADM-13); mixing the two into one "users" table is how
-- a customer ends up one bad join away from a staff permission.

create table staff_roles (
    code        text primary key,
    description text not null,
    -- Roles are reference data. Adding one is a migration and a code change to
    -- the grant table in internal/platform/authz, never a runtime insert
    -- (SEC-10).
    created_at  timestamptz not null default now()
);

insert into staff_roles (code, description) values
    ('owner',         'Business owner: everything, including user and role management and GST rates'),
    ('admin',         'Platform administrator: identical grants to owner'),
    ('manager',       'Runs the store: orders, stock, catalog, pricing, refunds, purchasing, marketing'),
    ('counter_sales', 'Till operator: sell, choose a batch, take payment, award loyalty points'),
    ('data_entry',    'Data entry executive: product records only'),
    ('viewer',        'Read-only across orders, customers, catalog and reports'),
    ('support',       'Viewer plus order notes, address correction and cancellation'),
    ('ops',           'Support plus fulfilment, stock adjustment and shipments'),
    ('finance',       'Refunds, settlement import, invoices and credit notes'),
    ('catalog',       'Product, variant, price and media management'),
    ('marketing',     'Campaigns, loyalty configuration and contact exports'),
    ('purchasing',    'Suppliers, purchase orders, receipts and returns to vendor');

create table staff (
    id            uuid primary key default gen_random_uuid(),
    email         citext not null unique,       -- case-insensitive (BR-IDN-08)
    phone         text unique,                  -- E.164
    full_name     text not null,
    -- Argon2id encoded hash. Never a plaintext or reversible password
    -- (BR-IDN-01). The column is named for what it holds so nobody mistakes it
    -- for something readable.
    password_hash text not null,
    status        text not null default 'active'
                  check (status in ('active','suspended','disabled')),
    -- Re-authentication timestamp for high-consequence actions (BR-ADM-07).
    last_reauth_at timestamptz,
    last_login_at  timestamptz,
    failed_logins  int not null default 0 check (failed_logins >= 0),
    locked_until   timestamptz,                 -- temporary only (BR-IDN-11)
    created_at     timestamptz not null default now(),
    created_by     uuid references staff(id),
    updated_at     timestamptz not null default now()
);

create index staff_status on staff (status) where status = 'active';

-- Role assignment. A staff member may hold several roles and the grants are
-- their union; there is no runtime hierarchy (BR-ADM-12).
create table staff_role_assignments (
    staff_id    uuid not null references staff(id) on delete cascade,
    role_code   text not null references staff_roles(code),
    granted_at  timestamptz not null default now(),
    granted_by  uuid not null references staff(id),
    primary key (staff_id, role_code)
);

-- Every foreign key gets an index on the referencing side, or a parent delete
-- becomes a full scan of the child table (DB-004).
create index staff_role_assignments_role       on staff_role_assignments (role_code);
create index staff_role_assignments_granted_by on staff_role_assignments (granted_by);
create index staff_created_by                  on staff (created_by) where created_by is not null;

-- At least one owner must exist at all times. A platform whose only owner has
-- been deleted cannot grant anyone else access again, which is an outage with
-- no recovery path short of direct database surgery.
create or replace function assert_owner_remains() returns trigger
language plpgsql as $$
declare
    remaining int;
begin
    select count(*) into remaining
      from staff_role_assignments a
      join staff s on s.id = a.staff_id
     where a.role_code = 'owner'
       and s.status = 'active'
       and a.staff_id <> old.staff_id;

    if remaining = 0 then
        raise exception 'cannot remove the last active owner';
    end if;
    return old;
end;
$$;

create trigger staff_role_assignments_keep_an_owner
    before delete on staff_role_assignments
    for each row when (old.role_code = 'owner')
    execute function assert_owner_remains();

-- ---------------------------------------------------------------------------
-- Append-only enforcement for the audit log (BR-ADM-05)
--
-- Enforced by the database rather than by convention: the application cannot
-- rewrite history even if a bug or an attacker tries.
-- ---------------------------------------------------------------------------
create or replace function reject_mutation() returns trigger
language plpgsql as $$
begin
    raise exception '% is append-only', tg_table_name;
end;
$$;

create trigger audit_log_no_update before update on audit_log
    for each statement execute function reject_mutation();
create trigger audit_log_no_delete before delete on audit_log
    for each statement execute function reject_mutation();

-- Domain events are immutable facts too. The one permitted change is marking a
-- row published, so the outbox relay can drain it (EVT-002, EVT-008).
create or replace function reject_event_mutation() returns trigger
language plpgsql as $$
begin
    if new.id is distinct from old.id
       or new.name is distinct from old.name
       or new.occurred_at is distinct from old.occurred_at
       or new.aggregate_type is distinct from old.aggregate_type
       or new.aggregate_id is distinct from old.aggregate_id
       or new.payload is distinct from old.payload then
        raise exception 'domain_events is immutable except for published_at';
    end if;
    return new;
end;
$$;

create trigger domain_events_immutable before update on domain_events
    for each row execute function reject_event_mutation();
create trigger domain_events_no_delete before delete on domain_events
    for each statement execute function reject_mutation();

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop trigger if exists domain_events_no_delete on domain_events;
drop trigger if exists domain_events_immutable on domain_events;
drop trigger if exists audit_log_no_delete on audit_log;
drop trigger if exists audit_log_no_update on audit_log;
drop function if exists reject_event_mutation();
drop function if exists reject_mutation();
drop trigger if exists staff_role_assignments_keep_an_owner on staff_role_assignments;
drop function if exists assert_owner_remains();
drop table if exists staff_role_assignments;
drop table if exists staff;
drop table if exists staff_roles;
-- +goose StatementEnd
