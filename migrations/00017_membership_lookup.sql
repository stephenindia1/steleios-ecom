-- +goose Up
-- +goose StatementBegin

-- ===========================================================================
-- THE BUG THIS FIXES
-- ===========================================================================
--
-- Nobody could reach a shop. Sign-in succeeded and returned an EMPTY shop
-- list, every time, for every user. Found by signing in against the live
-- database rather than a fake.
--
-- Why: the shop switcher is built by reading a person's staff memberships, and
-- that read necessarily happens BEFORE a shop is chosen — choosing one is what
-- it is for. So it runs on the system path (ADR 0007), where app.tenant_id is
-- unset and current_tenant_id() returns NULL. Every table the query touches is
-- scoped by that function:
--
--     staff                    tenant_id = current_tenant_id()
--     staff_role_assignments   tenant_id = current_tenant_id()
--     tenants                  id        = current_tenant_id()
--     clients                  exists (... t.id = current_tenant_id())
--
-- `x = NULL` is NULL, which is not true, so the join produced nothing. The
-- fail-closed design worked exactly as intended (migration 00004); the query
-- was asking a question the policies cannot answer.
--
-- This is the same shape of defect as migration 00016 — a tenant-scoped rule
-- applied to a step that precedes tenancy — on a different table, and it went
-- unnoticed for the same reason: the repository was faked in the service
-- tests, so nothing exercised the actual policies until now.
--
-- ===========================================================================
-- THE FIX, AND WHY IT IS THIS ONE
-- ===========================================================================
--
-- The rejected fix was to widen the four policies with an OR clause keyed on a
-- second setting, app.identity_id. It would work, but it would mean that ANY
-- query on staff on the system path returns that identity's rows across every
-- tenant — a permanent, ambient widening of four tables to serve one lookup,
-- where a later careless query inherits the exposure.
--
-- Instead the bypass is confined to ONE function with ONE shape of answer:
-- the memberships of the identity you name, and nothing else. It cannot be
-- asked a different question. Reviewing this exposure means reading the
-- twenty lines below rather than reasoning about every future query.
--
-- Three things make it safe:
--
--   1. It takes an identity id and filters on it. There is no parameter that
--      widens the result, and no way to ask it for "all staff".
--   2. search_path is pinned. Without that, a caller who could create a schema
--      on their own search path could shadow `staff` and have this function
--      read their table with the definer's privileges. This is THE classic
--      SECURITY DEFINER vulnerability and the pin is not optional.
--   3. The definer is a role that exists only to own it: no login, and it owns
--      nothing else. Compromising the function grants the ability to list one
--      identity's shop memberships, which is what the function is for.
--
-- Note that ownership alone would NOT have been enough. Every one of these
-- tables is FORCE row level security, which subjects even the table owner to
-- its policies (migration 00004) — deliberately, because the owner is who runs
-- migrations in production. So the definer needs BYPASSRLS, which is why the
-- role below is created rather than reusing an existing one.

-- The definer role. NOLOGIN: nothing may connect as it, so its only reachable
-- surface is the function it owns.
do $$
begin
    if not exists (select 1 from pg_roles where rolname = 'steleios_membership') then
        create role steleios_membership nologin bypassrls;
    end if;
end
$$;

-- BYPASSRLS exempts the role from POLICIES, not from GRANTS: without these the
-- function fails with "permission denied for table staff". Granted table by
-- table, and read-only, so the definer's reach is exactly the four relations
-- its body names and nothing that is added to the schema later.
grant select on staff, tenants, clients, staff_role_assignments to steleios_membership;

create or replace function memberships_of_identity(p_identity_id uuid)
returns table (
    staff_id    uuid,
    tenant_id   uuid,
    shop_code   text,
    shop_name   text,
    client_id   uuid,
    client_code text,
    status      text,
    roles       text[]
)
language sql
stable
security definer
set search_path = public, pg_temp
as $$
    select s.id, s.tenant_id, coalesce(t.shop_code, ''), t.legal_name,
           t.client_id, c.client_code, s.status,
           coalesce(array_agg(a.role_code) filter (where a.role_code is not null), '{}')
      from staff s
      join tenants t on t.id = s.tenant_id
      join clients c on c.id = t.client_id
      left join staff_role_assignments a on a.staff_id = s.id
     where s.identity_id = p_identity_id
     group by s.id, s.tenant_id, t.shop_code, t.legal_name, t.client_id,
              c.client_code, s.status;
$$;

alter function memberships_of_identity(uuid) owner to steleios_membership;

-- PUBLIC holds EXECUTE on new functions by default, which would make this
-- reachable by any role that ever gains a connection. Revoke first, then grant
-- to the one role that needs it.
revoke all on function memberships_of_identity(uuid) from public;
grant execute on function memberships_of_identity(uuid) to steleios_app;

comment on function memberships_of_identity(uuid) is
    'The shop switcher. SECURITY DEFINER because the lookup precedes tenancy and every table it reads is tenant-scoped; confined to one identity''s own memberships. See migration 00017.';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop function if exists memberships_of_identity(uuid);
revoke select on staff, tenants, clients, staff_role_assignments from steleios_membership;
-- The role is left in place: dropping it would fail if anything else came to
-- depend on it, and an unprivileged NOLOGIN role costs nothing.
-- +goose StatementEnd
