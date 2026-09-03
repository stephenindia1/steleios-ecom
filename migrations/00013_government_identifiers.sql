-- +goose Up
-- +goose StatementBegin

-- Government-issued business identifiers (docs/09 §6).
--
-- The client is bound to the business as the GOVERNMENT identifies it. In
-- India that is normally the GSTIN, but not always, and the exceptions matter
-- for exactly the shops this product is for:
--
--   GSTIN   registered under GST. The usual case, and the one that lets a shop
--           charge GST and issue tax invoices.
--   TIN     the pre-GST state VAT number. Legacy, still quoted by older
--           businesses and on historical records.
--   PAN     every business has one. The fallback identity for a shop below the
--           GST registration threshold.
--   Udyam / Shop & Establishment  registration numbers a small shop may hold
--           when it holds nothing else.
--
-- A shop under the turnover threshold is NOT required to register for GST, and
-- there are a great many of them. Requiring a GSTIN would exclude precisely the
-- small retailers this product targets, so the binding is to "a government
-- identifier", with GSTIN preferred where it exists.

alter table clients add column tin           text;
alter table clients add column udyam_number  text;
alter table clients add column shop_licence_number text;

-- Whether the business is registered under GST. Derived at confirmation from
-- whether a GSTIN was supplied, and stored because it changes what the shop is
-- allowed to issue.
alter table clients add column gst_registered boolean not null default false;

create unique index clients_tin on clients (tin) where tin is not null;
create unique index clients_udyam on clients (udyam_number) where udyam_number is not null;

-- TIN was an 11-digit state number.
alter table clients add constraint clients_tin_format
    check (tin is null or tin ~ '^[0-9]{11}$');

-- The PRIMARY binding is the GSTIN, or the TIN for a business still identified
-- by its pre-GST state number. PAN, Udyam and the shop licence are recorded
-- alongside as supporting identity, but they do not on their own bind a client
-- to a business: PAN identifies a taxpayer, whereas GSTIN and TIN identify a
-- registered TRADING entity, which is what a shop is.
--
-- Consequence, stated so it is a decision rather than a discovery: a business
-- below the GST threshold and holding no TIN cannot be onboarded. That is a
-- real segment of small retail. Relaxing it later means accepting PAN as the
-- binding and issuing bills of supply only — the machinery is already here
-- (gst_registered, BR-DOC-12), so it is a constraint change rather than a
-- redesign.
alter table clients add constraint clients_confirmed_needs_identifier
    check (
        confirmed_at is null
        or gstin is not null
        or tin is not null
    );

-- gst_registered must agree with reality: it is true if and only if a GSTIN is
-- held. Letting them disagree would mean a shop charging GST without a
-- registration, or holding one and issuing bills of supply.
--
-- Backfill BEFORE constraining. The column defaults to false, so any existing
-- client that already holds a GSTIN would violate the constraint the moment it
-- is added — the ordinary trap of adding an invariant to a live table
-- (DB-037).
update clients set gst_registered = (gstin is not null);

alter table clients add constraint clients_gst_registered_matches_gstin
    check (gst_registered = (gstin is not null));

-- Freeze the new identifiers on confirmation, like the rest of the business
-- identity (migration 00012).
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
       or new.tin is distinct from old.tin
       or new.udyam_number is distinct from old.udyam_number
       or new.shop_licence_number is distinct from old.shop_licence_number
       or new.gst_registered is distinct from old.gst_registered
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

-- A shop that is not GST registered cannot charge GST. It issues bills of
-- supply, not tax invoices (BR-DOC-12), and that follows from the registration
-- rather than from a setting somebody can toggle.
comment on column clients.gst_registered is
    'True only when a GSTIN is held. An unregistered business issues bills of supply and charges no GST (BR-DOC-12).';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
alter table clients drop constraint if exists clients_gst_registered_matches_gstin;
alter table clients drop constraint if exists clients_confirmed_needs_identifier;
alter table clients drop constraint if exists clients_tin_format;
drop index if exists clients_udyam;
drop index if exists clients_tin;
alter table clients drop column if exists gst_registered;
alter table clients drop column if exists shop_licence_number;
alter table clients drop column if exists udyam_number;
alter table clients drop column if exists tin;
-- +goose StatementEnd
