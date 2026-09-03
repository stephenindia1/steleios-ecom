-- +goose Up
-- +goose StatementBegin

-- Sessions (docs/05 §5.1, ADR 0005).
--
-- Steleios runs PostgreSQL only, so sessions live here rather than in Redis.
-- For one shop with a handful of tills the load is trivial, and one datastore
-- means one backup to get right.
--
-- ===========================================================================
-- THE TOKEN IS NOT STORED. ONLY ITS HASH.
-- ===========================================================================
--
-- A session token is a bearer credential: whoever holds it IS that person for
-- as long as it lives. Storing tokens in plaintext would mean a single SELECT
-- on this table — a read-only database leak, a stray backup, an over-broad
-- support query — hands over every live session in the system, and the holder
-- could act as any signed-in user without a password.
--
-- Storing only sha256(token) makes the table useless to a reader: the hash
-- cannot be presented as a cookie. The cost is nothing, because lookup is by
-- exact hash rather than by comparison.
--
-- SHA-256 rather than Argon2id here on purpose. A session token is 256 bits of
-- crypto/rand (SES-001), so there is no dictionary to attack and no work
-- factor needed — and a slow hash on every single request would be a
-- self-inflicted performance problem. Passwords are the opposite case and use
-- Argon2id (BR-IDN-01).

create table sessions (
    -- sha256(token), hex. The raw token exists only in the client's cookie.
    token_sha256   text primary key,

    identity_id    uuid not null references identities(id),

    -- The shop this session is currently acting in. Null until one is chosen:
    -- an owner with several shops signs in first and picks afterwards
    -- (migration 00005). Row-level security reads this via app.tenant_id,
    -- which the middleware sets from here (ADR 0007).
    tenant_id      uuid references tenants(id),

    actor_type     text not null default 'admin'
                   check (actor_type in ('customer','admin','guest')),

    -- The CSRF secret for double-submit. Bound to the session so a token from
    -- one session cannot be replayed into another.
    csrf_secret    text not null,

    created_at     timestamptz not null default now(),
    -- Slid at most once a minute, not on every request: a write per request
    -- would make every read a write (SES-004).
    last_seen_at   timestamptz not null default now(),
    expires_at     timestamptz not null,

    -- Recorded for the audit trail and for showing someone their own sessions.
    -- The user agent is stored as given; it is not trusted for anything.
    ip             text,
    user_agent     text,

    revoked_at     timestamptz,
    revoked_reason text
                   check (revoked_reason is null or revoked_reason in
                          ('signed_out','signed_out_everywhere','password_changed',
                           'role_changed','user_blocked','recovery','expired','admin')),

    check (expires_at > created_at)
);

-- "Sign out everywhere" and "block this user" are both one indexed lookup
-- rather than a scan, which is what SES-005 asks for.
create index sessions_identity on sessions (identity_id) where revoked_at is null;
create index sessions_expiry   on sessions (expires_at)  where revoked_at is null;
create index sessions_tenant   on sessions (tenant_id)   where tenant_id is not null and revoked_at is null;

-- ---------------------------------------------------------------------------
-- Sessions are deliberately NOT tenant-scoped by row-level security.
--
-- Authentication necessarily precedes tenancy: the system cannot know which
-- shop a request belongs to until it has resolved the session that says so.
-- A tenant-scoped policy here would be circular.
--
-- Access is instead confined to the identity module, which reaches them
-- through the explicitly-named ReadSystem/DoSystem path (ADR 0007). The table
-- holds no business data, and its primary key is the hash of a 256-bit random
-- token, so it cannot be usefully enumerated.
-- ---------------------------------------------------------------------------

-- A revoked session stays revoked. Un-revoking would silently resurrect a
-- credential somebody deliberately killed — after a password change, a role
-- change, or a compromise.
create or replace function sessions_revocation_is_final() returns trigger
language plpgsql as $$
begin
    if old.revoked_at is not null and new.revoked_at is null then
        raise exception 'a revoked session cannot be restored';
    end if;
    if old.revoked_at is not null and new.expires_at is distinct from old.expires_at then
        raise exception 'a revoked session cannot be extended';
    end if;
    if new.identity_id is distinct from old.identity_id then
        raise exception 'a session cannot be reassigned to another identity';
    end if;
    if new.token_sha256 is distinct from old.token_sha256 then
        raise exception 'a session token cannot be changed in place';
    end if;
    return new;
end;
$$;

create trigger sessions_no_resurrection before update on sessions
    for each row execute function sessions_revocation_is_final();

-- Expired and revoked sessions are swept rather than kept forever; the audit
-- log is where the history of who signed in lives (BR-ADM-06), not here.
comment on table sessions is
    'Server-side sessions. Stores sha256(token), never the token itself. Swept after expiry; sign-in history lives in the audit log.';

grant select, insert, update, delete on sessions to steleios_app;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop trigger if exists sessions_no_resurrection on sessions;
drop function if exists sessions_revocation_is_final();
drop table if exists sessions;
-- +goose StatementEnd
