-- +goose Up
-- +goose StatementBegin

-- Owner identity and KYC (docs/09 §6).
--
-- A business is registered by a person, and the government identifies that
-- person too. This records who owns the client: name, PAN, address, and the
-- documents evidencing them.
--
-- ===========================================================================
-- AADHAAR: THE FULL NUMBER IS DELIBERATELY NOT STORED
-- ===========================================================================
--
-- Since the 2018 Supreme Court judgment striking down section 57 of the
-- Aadhaar Act, a private company generally MAY NOT store Aadhaar numbers
-- without specific statutory authorisation. Steleios has none, and there are
-- penalties for holding them anyway.
--
-- The practical argument is as strong as the legal one. A SaaS holding the
-- Aadhaar number of every shop owner in its customer base is a target of a
-- completely different order from one holding names and PANs, and the breach
-- would be unrecoverable for the people in it.
--
-- So this schema makes storing one structurally impossible: the column accepts
-- exactly four digits. What is kept instead:
--
--   aadhaar_last4        the last four digits, which is what a human uses to
--                        confirm "yes, that is my card" - and is not an
--                        identifier on its own
--   aadhaar_verified_ref a reference from an offline verification (the UIDAI
--                        XML/QR flow), which proves the check happened without
--                        the number ever being held
--
-- PAN carries no such restriction and is the primary owner identifier. It is
-- also already the business tax identity, so it is the one that actually
-- matters here.

create table client_owners (
    id            uuid primary key default gen_random_uuid(),
    client_id     uuid not null references clients(id),

    full_name     text not null,
    -- The owner's own PAN. For a proprietorship this is usually the same PAN
    -- as the business (migration 00012), which is correct rather than
    -- duplicated: for a proprietor the person and the business are one.
    pan           text,
    date_of_birth date,

    -- Address as it appears on the proof document.
    address_line1 text not null,
    address_line2 text,
    city          text not null,
    state_code    text not null,
    pincode       text not null,

    -- See the note above. Four digits, never twelve.
    aadhaar_last4       text,
    aadhaar_verified_at timestamptz,
    aadhaar_verified_ref text,

    email         citext,
    phone         text,           -- E.164
    is_primary    boolean not null default false,
    share_percent numeric(5,2) check (share_percent is null or (share_percent > 0 and share_percent <= 100)),

    verified_at   timestamptz,
    verified_by   uuid references identities(id),

    created_at    timestamptz not null default now(),
    updated_at    timestamptz not null default now(),

    constraint client_owners_pan_format
        check (pan is null or pan ~ '^[A-Z]{5}[0-9]{4}[A-Z]{1}$'),
    constraint client_owners_pincode_format
        check (pincode ~ '^[0-9]{6}$'),

    -- The structural guard. Exactly four digits fit; a full Aadhaar cannot be
    -- written to this column even by a mistake or a careless import.
    constraint client_owners_aadhaar_is_masked
        check (aadhaar_last4 is null or aadhaar_last4 ~ '^[0-9]{4}$')
);

create index client_owners_client on client_owners (client_id);
create unique index client_owners_primary on client_owners (client_id) where is_primary;
create index client_owners_pan on client_owners (pan) where pan is not null;

-- Owner details are frozen once the client is confirmed, like the rest of the
-- business identity (migration 00012). A change of ownership is a matter for a
-- new registration and a recorded succession, not an edit to a field.
create or replace function client_owners_frozen() returns trigger
language plpgsql as $$
declare
    confirmed timestamptz;
    target_client uuid;
begin
    target_client := coalesce(new.client_id, old.client_id);
    select confirmed_at into confirmed from clients where id = target_client;

    if confirmed is not null then
        raise exception 'owner details of a confirmed client are permanent';
    end if;
    return coalesce(new, old);
end;
$$;

create trigger client_owners_freeze_on_confirm
    before update or delete on client_owners
    for each row execute function client_owners_frozen();

-- Owner records are personal data about a named individual, so the DPDP
-- obligations apply: purpose limitation, retention, and access control
-- (BR-DAT-06). They are held to evidence the business registration and for
-- nothing else.
comment on table client_owners is
    'KYC for the people who own a client. Personal data: collected to evidence business registration, never used for marketing or analytics. Aadhaar numbers are NOT stored - only the last four digits and an offline verification reference (see migration header).';

grant select, insert, update on client_owners to steleios_app;
revoke delete on client_owners from steleios_app;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop trigger if exists client_owners_freeze_on_confirm on client_owners;
drop function if exists client_owners_frozen();
drop table if exists client_owners;
-- +goose StatementEnd
