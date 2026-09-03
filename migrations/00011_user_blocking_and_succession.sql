-- +goose Up
-- +goose StatementBegin

-- Blocking a user and issuing its successor (docs/09 §4C).
--
-- The hierarchy, and what is permanent in it:
--
--   CLIENT   the business. Bound to its registration - GSTIN, documents,
--            legal name. Permanent. It is the thing the subscription and the
--            books belong to.
--     └── USER   a person's login. NOT permanent. A user can be blocked and
--                replaced by a new one, and the client carries on unchanged.
--
-- When an account is compromised the answer is NOT to swap the email on the
-- same user. It is to BLOCK that user and ISSUE A NEW ONE.
--
-- The reason is forensic. After a compromise nobody can say which of that
-- account's recent actions were the owner's and which were the attacker's.
-- Keeping the same user id bakes that ambiguity permanently into the audit
-- trail under one name: every past action, good and bad, attributed to an
-- identity that is still in use. Blocking and reissuing draws a hard line -
-- everything before it belongs to the possibly-compromised account, everything
-- after belongs to the new one, and no future reader has to guess which.
--
-- The old user's history is untouched and stays attributed to them. That is
-- the point, not a side effect (BR-APO-01).

-- ---------------------------------------------------------------------------
-- Blocking
-- ---------------------------------------------------------------------------
alter table identities drop constraint if exists identities_status_check;
alter table identities add constraint identities_status_check
    check (status in ('active','suspended','disabled','blocked'));

alter table identities add column blocked_at     timestamptz;
alter table identities add column blocked_reason text
    check (blocked_reason is null or blocked_reason in
           ('compromised','lost_access','left_the_business','policy','duplicate'));
alter table identities add column blocked_by     uuid references identities(id);

-- A blocked user is blocked forever. Reactivation is not an operation: if the
-- person needs access again they get a NEW user, which is the same procedure
-- and leaves the same clean line in the audit trail.
create or replace function identities_block_is_permanent() returns trigger
language plpgsql as $$
begin
    if old.status = 'blocked' and new.status <> 'blocked' then
        raise exception 'a blocked user cannot be reactivated; issue a new user instead';
    end if;
    if old.status = 'blocked' and new.blocked_at is distinct from old.blocked_at then
        raise exception 'blocked_at is immutable';
    end if;
    return new;
end;
$$;

create trigger identities_no_unblock before update on identities
    for each row execute function identities_block_is_permanent();

create index identities_blocked on identities (blocked_at desc) where status = 'blocked';

-- ---------------------------------------------------------------------------
-- Succession: which user replaced which
-- ---------------------------------------------------------------------------
create table identity_successions (
    id             uuid primary key default gen_random_uuid(),

    predecessor_id uuid not null references identities(id),
    successor_id   uuid not null references identities(id),
    client_id      uuid not null references clients(id),

    reason         text not null
                   check (reason in ('compromised','lost_access','left_the_business','policy')),

    -- Vendor-assisted succession needs two named staff and SMS confirmation to
    -- the registered mobile, which cannot change during it (BR-REC-03,
    -- BR-REC-26).
    method         text not null check (method in ('self_service','vendor_recovery')),
    verified_via   text,
    performed_by   uuid references identities(id),
    approved_by    uuid references identities(id),

    created_at     timestamptz not null default now(),
    notes          text,

    check (predecessor_id <> successor_id),
    check (
        method <> 'vendor_recovery'
        or (performed_by is not null and approved_by is not null and performed_by <> approved_by)
    )
);

-- A user is replaced once. A chain is fine - A replaced by B, later B replaced
-- by C - but B cannot have two predecessors or two successors, or "who is the
-- current account" stops having one answer.
create unique index identity_successions_predecessor on identity_successions (predecessor_id);
create unique index identity_successions_successor   on identity_successions (successor_id);
create index identity_successions_client on identity_successions (client_id, created_at desc);

-- Successions are facts (docs/02 §0).
create or replace function successions_append_only() returns trigger
language plpgsql as $$
begin
    raise exception 'identity_successions is append-only';
end;
$$;

create trigger identity_successions_no_update before update on identity_successions
    for each statement execute function successions_append_only();
create trigger identity_successions_no_delete before delete on identity_successions
    for each statement execute function successions_append_only();

-- A succession must actually block its predecessor. Recording one while the old
-- account still works would leave two live logins for one person, which is the
-- exact situation this exists to prevent.
create or replace function succession_requires_blocked_predecessor() returns trigger
language plpgsql as $$
declare
    predecessor_status text;
begin
    select status into predecessor_status from identities where id = new.predecessor_id;
    if predecessor_status <> 'blocked' then
        raise exception 'predecessor % must be blocked before a successor is recorded', new.predecessor_id;
    end if;
    return new;
end;
$$;

create trigger identity_successions_predecessor_blocked
    before insert on identity_successions
    for each row execute function succession_requires_blocked_predecessor();

grant select, insert on identity_successions to steleios_app;
revoke update, delete on identity_successions from steleios_app;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop trigger if exists identity_successions_predecessor_blocked on identity_successions;
drop function if exists succession_requires_blocked_predecessor();
drop trigger if exists identity_successions_no_delete on identity_successions;
drop trigger if exists identity_successions_no_update on identity_successions;
drop function if exists successions_append_only();
drop table if exists identity_successions;
drop index if exists identities_blocked;
drop trigger if exists identities_no_unblock on identities;
drop function if exists identities_block_is_permanent();
alter table identities drop column if exists blocked_by;
alter table identities drop column if exists blocked_reason;
alter table identities drop column if exists blocked_at;
alter table identities drop constraint if exists identities_status_check;
alter table identities add constraint identities_status_check
    check (status in ('active','suspended','disabled'));
-- +goose StatementEnd
