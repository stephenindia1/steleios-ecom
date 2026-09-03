-- +goose Up
-- +goose StatementBegin

-- Identity permanence and contact history (docs/09 §4C, §6).
--
-- The IDENTITY ID is permanent. Email and phone are attributes of it, and both
-- can change — an owner whose email is hacked gets a new address on the SAME
-- identity, so every membership, every role, every audit entry and every
-- document they ever touched still points at them.
--
-- Two things follow, and this migration does both.

-- ---------------------------------------------------------------------------
-- 1. Remove the duplicated contact details from staff.
--
-- staff carried its own copy of email and full_name. That was a latent defect:
-- the moment an identity's email changes, the copy is stale, and there are then
-- two answers to "what is this person's email" with nothing saying which wins.
--
-- The append-only law distinguishes facts from state (docs/02 §0). A staff
-- membership is CURRENT STATE, not a historical record, so duplicating a
-- mutable attribute into it is not the deliberate snapshotting that order lines
-- do — it is drift waiting to happen. The identity is the single source.
-- ---------------------------------------------------------------------------
drop index if exists staff_email_per_tenant;

alter table staff drop column if exists email;
alter table staff drop column if exists full_name;

-- One membership per identity per shop remains the real constraint, and it was
-- already expressed on identity_id (migration 00005).

-- ---------------------------------------------------------------------------
-- 2. Contact changes are FACTS, so they are recorded append-only.
--
-- "This address was replaced, then, by that person, verified that way" is
-- something that happened. It answers the question asked after an account
-- takeover — when did the address change, and who authorised it — which is
-- unanswerable if the old value was simply overwritten (BR-APO-01).
-- ---------------------------------------------------------------------------
create table identity_contact_changes (
    id           uuid primary key default gen_random_uuid(),
    identity_id  uuid not null references identities(id),

    field        text not null check (field in ('email','phone')),
    old_value    text,                       -- null on first set
    new_value    text not null,

    -- How the change was authorised. Self-service is OTP to the channel the
    -- owner still holds; vendor_recovery is §4C and requires SMS confirmation
    -- to the registered mobile, which cannot itself be changed during recovery
    -- (BR-REC-26).
    method       text not null
                 check (method in ('self_service_otp','vendor_recovery','initial_registration')),
    verified_via text,                       -- 'sms:+91XXXXXXXX' (masked), 'email:o***@x.com'

    changed_at   timestamptz not null default now(),
    -- Who performed it. For vendor_recovery this is the vendor staff member,
    -- and a second approver is required (BR-REC-03).
    changed_by   uuid references identities(id),
    approved_by  uuid references identities(id),
    ip           text,
    user_agent   text,
    reason       text
);

create index identity_contact_changes_identity
    on identity_contact_changes (identity_id, changed_at desc);
create index identity_contact_changes_field
    on identity_contact_changes (identity_id, field, changed_at desc);
create index identity_contact_changes_changed_by
    on identity_contact_changes (changed_by) where changed_by is not null;

-- A vendor recovery needs two people, and they must be different (BR-REC-03).
alter table identity_contact_changes add constraint contact_change_two_approvers
    check (
        method <> 'vendor_recovery'
        or (changed_by is not null and approved_by is not null and changed_by <> approved_by)
    );

create or replace function contact_changes_no_update() returns trigger
language plpgsql as $$
begin
    raise exception 'identity_contact_changes is append-only';
end;
$$;

create trigger identity_contact_changes_no_update before update on identity_contact_changes
    for each statement execute function contact_changes_no_update();
create trigger identity_contact_changes_no_delete before delete on identity_contact_changes
    for each statement execute function contact_changes_no_update();

-- ---------------------------------------------------------------------------
-- 3. The locked state after a vendor-issued password (BR-REC-20).
--
-- While set, the identity may do exactly one thing: change its own password.
-- No sale, no order, no export, no configuration. Enforced in the
-- authorization layer; the column is what that layer reads.
-- ---------------------------------------------------------------------------
alter table identities add column must_change_password  boolean not null default false;
alter table identities add column password_set_at       timestamptz;
alter table identities add column password_expires_at   timestamptz;  -- generated passwords expire (BR-REC-10)
alter table identities add column recovery_initiated_at timestamptz;

create index identities_must_change on identities (id) where must_change_password;

grant select, insert on identity_contact_changes to steleios_app;
revoke update, delete on identity_contact_changes from steleios_app;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop index if exists identities_must_change;
alter table identities drop column if exists recovery_initiated_at;
alter table identities drop column if exists password_expires_at;
alter table identities drop column if exists password_set_at;
alter table identities drop column if exists must_change_password;
drop trigger if exists identity_contact_changes_no_delete on identity_contact_changes;
drop trigger if exists identity_contact_changes_no_update on identity_contact_changes;
drop function if exists contact_changes_no_update();
drop table if exists identity_contact_changes;
alter table staff add column email citext;
alter table staff add column full_name text;
create unique index if not exists staff_email_per_tenant on staff (tenant_id, email);
-- +goose StatementEnd
