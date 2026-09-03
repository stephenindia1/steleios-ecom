-- +goose Up
-- +goose StatementBegin

-- Business identity and registration documents (docs/09 §6).
--
--   CLIENT ──permanently bound──► the BUSINESS, as the government identifies it
--     │                            GSTIN, PAN, and the documents proving them
--     └── users, who come and go (migration 00011)
--
-- Users are replaceable; the business is not. A client IS its business: the
-- subscription, the books, the invoices and the seven-year retention all belong
-- to that legal entity, not to whoever currently logs in.
--
-- So once a client is CONFIRMED, its business identity and its documents are
-- frozen. A different business is a different client, not an edit.

-- ---------------------------------------------------------------------------
-- The government identifiers
-- ---------------------------------------------------------------------------
alter table clients add column pan            text;
alter table clients add column cin            text;   -- companies only
alter table clients add column business_type  text
    check (business_type is null or business_type in
           ('proprietorship','partnership','llp','private_limited','public_limited','huf','trust','society'));
alter table clients add column registered_address text;
alter table clients add column state_code     text;   -- place of supply for the seller

-- Confirmation is the moment the binding becomes permanent.
alter table clients add column confirmed_at   timestamptz;
alter table clients add column confirmed_by   uuid references identities(id);

-- One business, one client. Registering the same GSTIN twice would split one
-- business's books across two subscriptions, and neither would be complete.
create unique index clients_gstin on clients (gstin) where gstin is not null;
create unique index clients_cin   on clients (cin)   where cin is not null;
create index clients_confirmed on clients (confirmed_at) where confirmed_at is not null;

-- A GSTIN embeds its holder's PAN at characters 3-12 and its state at 1-2.
-- Checking that they agree costs nothing and catches a transposed digit at
-- registration rather than on a return (BR-ACP-16).
alter table clients add constraint clients_gstin_format
    check (gstin is null or gstin ~ '^[0-9]{2}[A-Z]{5}[0-9]{4}[A-Z]{1}[0-9A-Z]{1}[Z]{1}[0-9A-Z]{1}$');
alter table clients add constraint clients_pan_format
    check (pan is null or pan ~ '^[A-Z]{5}[0-9]{4}[A-Z]{1}$');
alter table clients add constraint clients_gstin_contains_pan
    check (gstin is null or pan is null or substring(gstin from 3 for 10) = pan);
alter table clients add constraint clients_gstin_state_matches
    check (gstin is null or state_code is null or substring(gstin from 1 for 2) = state_code);

-- ---------------------------------------------------------------------------
-- Registration documents
-- ---------------------------------------------------------------------------
create table client_documents (
    id            uuid primary key default gen_random_uuid(),
    client_id     uuid not null references clients(id),

    kind          text not null
                  check (kind in ('gst_certificate','pan_card','incorporation','partnership_deed',
                                  'address_proof','bank_proof','fssai_licence','shop_establishment','other')),
    -- The file lives in object storage; the row holds its key and its hash.
    -- The hash is what makes the document tamper-evident: a replaced file no
    -- longer matches what was verified (BR-MED-03).
    storage_key   text not null,
    sha256        text not null,
    content_type  text not null,
    size_bytes    bigint not null check (size_bytes > 0),
    original_name text,

    uploaded_at   timestamptz not null default now(),
    uploaded_by   uuid references identities(id),

    -- Vendor verification during onboarding.
    verified_at   timestamptz,
    verified_by   uuid references identities(id),
    rejected_at   timestamptz,
    rejection_reason text,

    -- Superseded rather than deleted: a document replaced during onboarding is
    -- still part of the record of what was submitted and when.
    superseded_at timestamptz,
    superseded_by uuid references client_documents(id),

    check (not (verified_at is not null and rejected_at is not null))
);

create index client_documents_client on client_documents (client_id, kind);
create unique index client_documents_current on client_documents (client_id, kind)
    where superseded_at is null and rejected_at is null;
create index client_documents_sha on client_documents (sha256);
create index client_documents_superseded_by on client_documents (superseded_by)
    where superseded_by is not null;

-- ---------------------------------------------------------------------------
-- Freezing on confirmation
--
-- Before confirmation, onboarding is a working process: details get corrected
-- and documents get replaced. After it, the binding is permanent - a different
-- business is a different client, never an edit to this one.
-- ---------------------------------------------------------------------------
create or replace function clients_business_identity_frozen() returns trigger
language plpgsql as $$
begin
    if old.confirmed_at is null then
        return new;   -- still onboarding
    end if;

    if new.confirmed_at is distinct from old.confirmed_at
       or new.gstin is distinct from old.gstin
       or new.pan is distinct from old.pan
       or new.cin is distinct from old.cin
       or new.business_type is distinct from old.business_type
       or new.legal_name is distinct from old.legal_name
       or new.registered_address is distinct from old.registered_address
       or new.state_code is distinct from old.state_code then
        raise exception
            'the business identity of a confirmed client is permanent; a different business is a different client';
    end if;

    return new;
end;
$$;

create trigger clients_freeze_business_identity before update on clients
    for each row execute function clients_business_identity_frozen();

-- Documents of a confirmed client are permanent: not edited, not replaced, not
-- deleted. They are the evidence the binding rests on.
create or replace function client_documents_frozen() returns trigger
language plpgsql as $$
declare
    confirmed timestamptz;
    target_client uuid;
begin
    target_client := coalesce(new.client_id, old.client_id);
    select confirmed_at into confirmed from clients where id = target_client;

    if confirmed is not null then
        raise exception 'documents of a confirmed client are permanent and cannot be changed';
    end if;
    return coalesce(new, old);
end;
$$;

create trigger client_documents_freeze_on_confirm
    before update or delete on client_documents
    for each row execute function client_documents_frozen();

grant select, insert, update on client_documents to steleios_app;
revoke delete on client_documents from steleios_app;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop trigger if exists client_documents_freeze_on_confirm on client_documents;
drop function if exists client_documents_frozen();
drop trigger if exists clients_freeze_business_identity on clients;
drop function if exists clients_business_identity_frozen();
drop table if exists client_documents;
drop index if exists clients_confirmed;
drop index if exists clients_cin;
drop index if exists clients_gstin;
alter table clients drop constraint if exists clients_gstin_state_matches;
alter table clients drop constraint if exists clients_gstin_contains_pan;
alter table clients drop constraint if exists clients_pan_format;
alter table clients drop constraint if exists clients_gstin_format;
alter table clients drop column if exists confirmed_by;
alter table clients drop column if exists confirmed_at;
alter table clients drop column if exists state_code;
alter table clients drop column if exists registered_address;
alter table clients drop column if exists business_type;
alter table clients drop column if exists cin;
alter table clients drop column if exists pan;
-- +goose StatementEnd
