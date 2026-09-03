-- +goose Up
-- +goose StatementBegin

-- Client acceptance of the terms (docs/09 §6).
--
-- The vendor provides a platform. The client owns and operates the business
-- on it: what to sell, at what price, under which tax treatment, to whom, and
-- what to file. This table is the record that they accepted that, and it is
-- worth having precisely because the moment it matters is years later.
--
-- An acceptance nobody can produce is not an acceptance, so this records the
-- terms version, its hash, who accepted, when and from where — and never
-- deletes a superseded one (BR-ACP-02, BR-ACP-03).

create table client_acceptances (
    id             uuid primary key default gen_random_uuid(),
    client_id      uuid not null references clients(id),

    -- What was accepted. The hash pins the exact text: a version label alone
    -- proves nothing if the document behind it was edited afterwards.
    document       text not null
                   check (document in ('terms_of_service','tax_and_pricing_responsibility',
                                       'privacy_policy','data_processing')),
    version        text not null,
    document_sha256 text not null,

    -- Who accepted. The owner binds the business; a member of staff clicking
    -- through does not (BR-ACP-05).
    accepted_by    uuid not null references identities(id),
    accepted_at    timestamptz not null default now(),
    ip             text,
    user_agent     text,

    -- Set when a later acceptance replaces this one. The row itself is never
    -- deleted or edited (BR-ACP-03).
    superseded_at  timestamptz,
    superseded_by  uuid references client_acceptances(id)
);

-- The question asked of this table: what is this client currently bound by,
-- and what were they bound by on a given date.
create index client_acceptances_current on client_acceptances (client_id, document)
    where superseded_at is null;
create index client_acceptances_history on client_acceptances (client_id, document, accepted_at desc);
create index client_acceptances_superseded_by on client_acceptances (superseded_by)
    where superseded_by is not null;

-- One current acceptance per document per client.
create unique index client_acceptances_one_current
    on client_acceptances (client_id, document) where superseded_at is null;

-- Append-only, enforced by the database rather than by convention. An
-- acceptance that could be edited later is not evidence of anything
-- (BR-ADM-05 pattern).
create or replace function client_acceptances_immutable() returns trigger
language plpgsql as $$
begin
    if new.client_id is distinct from old.client_id
       or new.document is distinct from old.document
       or new.version is distinct from old.version
       or new.document_sha256 is distinct from old.document_sha256
       or new.accepted_by is distinct from old.accepted_by
       or new.accepted_at is distinct from old.accepted_at then
        raise exception 'client_acceptances is immutable except for supersession';
    end if;
    return new;
end;
$$;

create trigger client_acceptances_no_rewrite before update on client_acceptances
    for each row execute function client_acceptances_immutable();

create trigger client_acceptances_no_delete before delete on client_acceptances
    for each statement execute function reject_mutation();

-- Visible to the shops of the client it belongs to, like the client record
-- itself (migration 00005). A client can always see what it agreed to.
alter table client_acceptances enable row level security;
alter table client_acceptances force  row level security;

create policy acceptance_of_current_tenant on client_acceptances
    using (
        exists (
            select 1 from tenants t
             where t.client_id = client_acceptances.client_id
               and t.id = current_tenant_id()
        )
    );

grant select, insert on client_acceptances to steleios_app;
revoke update, delete on client_acceptances from steleios_app;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop policy if exists acceptance_of_current_tenant on client_acceptances;
alter table client_acceptances no force row level security;
alter table client_acceptances disable row level security;
drop trigger if exists client_acceptances_no_delete on client_acceptances;
drop trigger if exists client_acceptances_no_rewrite on client_acceptances;
drop function if exists client_acceptances_immutable();
drop table if exists client_acceptances;
-- +goose StatementEnd
