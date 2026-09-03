-- +goose Up
-- +goose StatementBegin

-- Retired email addresses and phone numbers (docs/09 §4C).
--
-- An identity has exactly one active email and one active phone. When either
-- changes, the previous value is RETIRED — it can no longer be used to sign in,
-- and no other identity may claim it.
--
-- A retired value is dead. Not "dead unless the original owner asks" — dead.
-- Once a new address has been issued, the old one is never honoured again, by
-- anyone, including the identity it belonged to.
--
-- Why absolute rather than conditional. The reason an address is replaced is
-- usually that it was compromised, and a mailbox that was hacked once is not
-- trustworthy afterwards: the attacker may still hold it, or may have left a
-- forwarding rule behind. But the stronger argument is that a conditional rule
-- is one somebody can be talked into applying wrongly. "Blocked, except for
-- the original owner" invites the exact conversation an attacker wants to
-- have with a support desk. "Retired addresses are never usable again" is a
-- sentence with no negotiable part in it.
--
-- The cost is small and the failure mode is benign: an owner who recovers an
-- old mailbox simply keeps using their current address, or moves to another.

create table retired_contacts (
    id           uuid primary key default gen_random_uuid(),

    field        text not null check (field in ('email','phone')),
    -- citext so that Owner@Shop.com and owner@shop.com are the same retired
    -- address. Case-sensitivity here would be a trivial bypass.
    value        citext not null,

    -- The identity it belonged to. Recorded for the audit trail — "whose
    -- address was this" — not as a permission. Nobody reclaims it, including
    -- them.
    identity_id  uuid not null references identities(id),

    retired_at   timestamptz not null default now(),
    reason       text not null
                 check (reason in ('changed','compromised','vendor_recovery','closed')),
    -- The change that retired it, so the two records join.
    change_id    uuid references identity_contact_changes(id)
);

-- One retirement per value. A second attempt to retire the same address is a
-- bug or a race, not a new fact.
create unique index retired_contacts_value on retired_contacts (field, value);
create index retired_contacts_identity on retired_contacts (identity_id, retired_at desc);
create index retired_contacts_change on retired_contacts (change_id) where change_id is not null;

-- ---------------------------------------------------------------------------
-- Enforcement: a retired value is never usable again, by anyone.
--
-- No owner exception, so the check is a plain existence test. That the rule
-- fits in four lines is the point — there is nothing here to reason about
-- under pressure, and nothing to make an exception to.
-- ---------------------------------------------------------------------------
create or replace function reject_retired_contact() returns trigger
language plpgsql as $$
begin
    if new.email is not null
       and exists (select 1 from retired_contacts where field = 'email' and value = new.email) then
        raise exception 'email % is retired and can never be used again', new.email;
    end if;

    if new.phone is not null
       and exists (select 1 from retired_contacts where field = 'phone' and value = new.phone) then
        raise exception 'phone % is retired and can never be used again', new.phone;
    end if;

    return new;
end;
$$;

create trigger identities_reject_retired_contacts
    before insert or update of email, phone on identities
    for each row execute function reject_retired_contact();

-- Retirements are facts: they happened, and they are not edited away
-- (docs/02 §0, BR-APO-01). Releasing a value is itself a deliberate act with
-- its own record, not a DELETE.
create or replace function retired_contacts_append_only() returns trigger
language plpgsql as $$
begin
    raise exception 'retired_contacts is append-only';
end;
$$;

create trigger retired_contacts_no_update before update on retired_contacts
    for each statement execute function retired_contacts_append_only();
create trigger retired_contacts_no_delete before delete on retired_contacts
    for each statement execute function retired_contacts_append_only();

grant select, insert on retired_contacts to steleios_app;
revoke update, delete on retired_contacts from steleios_app;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop trigger if exists retired_contacts_no_delete on retired_contacts;
drop trigger if exists retired_contacts_no_update on retired_contacts;
drop function if exists retired_contacts_append_only();
drop trigger if exists identities_reject_retired_contacts on identities;
drop function if exists reject_retired_contact();
drop table if exists retired_contacts;
-- +goose StatementEnd
