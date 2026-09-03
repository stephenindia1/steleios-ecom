-- +goose Up
-- +goose StatementBegin

-- ===========================================================================
-- WHAT THE VENDOR CAN SEE
-- ===========================================================================
--
-- BR-ADM-15 says saas_admin creates and manages SaaS clients. It could not:
-- every client-side table is readable only through `t.id = current_tenant_id()`,
-- and a platform user has no tenant at all — that is the whole point of
-- migration 00019. So the vendor console would have listed zero clients, which
-- is the same defect as migrations 00016 and 00017, now for the third time.
--
-- Fixing it a third time with a third one-off SECURITY DEFINER function would
-- not scale: the vendor console reads clients, shops, groups, documents, owners
-- and acceptances, and will read subscriptions next.
--
-- So this migration answers it generally, and the boundary is the point:
--
--   * `app.platform` is a transaction-local flag, set only by
--     postgres.DoPlatform / ReadPlatform. Transaction-local for the same reason
--     the tenant is — a plain SET would persist on the pooled connection and
--     leak into the next request, which here would cross the CLIENT boundary
--     rather than move within it.
--
--   * The flag is added to the policies of the tables that describe WHICH
--     BUSINESSES EXIST. It is deliberately NOT added to any table holding a
--     business's own data.
--
-- That division is BR-ADM-14 expressed in row-level security rather than only in
-- the grant table. A vendor with the flag set and every platform role still sees
-- no order, no product, no stock movement, no customer and no invoice — not
-- because a permission stopped them, but because the rows are not visible to
-- them at all. Two independent layers, which is the standard this schema holds
-- everywhere else (migration 00002's append-only law is enforced the same way).
--
-- TestThePlatformFlagGrantsNothingOnBusinessTables is what keeps that true: it
-- sets the flag and asserts a business table still returns nothing.

-- ---------------------------------------------------------------------------
-- The predicate
--
-- Fails closed exactly as current_tenant_id() does: anything other than the
-- literal 'on' is false, so a malformed or absent setting denies rather than
-- grants. `current_setting(..., true)` returns NULL when unset rather than
-- raising, and NULL = 'on' is NULL, which is not true.
-- ---------------------------------------------------------------------------
create or replace function current_is_platform() returns boolean
language sql stable as $$
    select coalesce(current_setting('app.platform', true), '') = 'on';
$$;

comment on function current_is_platform() is
    'True inside a transaction opened by postgres.DoPlatform/ReadPlatform. Grants visibility ONLY on the tables naming which businesses exist, never on a business''s own data (BR-ADM-14). See migration 00020.';

-- ---------------------------------------------------------------------------
-- Clients, and the records that describe them
--
-- Each policy keeps its tenant clause unchanged — a shop still sees its own
-- client and no other — and gains the vendor as a second way in.
-- ---------------------------------------------------------------------------

drop policy if exists client_read_of_current_tenant on clients;
create policy client_read_of_current_tenant on clients
    for select using (
        current_is_platform()
        or exists (select 1 from tenants t
                    where t.client_id = clients.id
                      and t.id = current_tenant_id())
    );

drop policy if exists group_read_of_current_tenant on store_groups;
create policy group_read_of_current_tenant on store_groups
    for select using (
        current_is_platform()
        or exists (select 1 from tenants t
                    where t.group_id = store_groups.id
                      and t.id = current_tenant_id())
    );

drop policy if exists acceptance_read_of_current_tenant on client_acceptances;
create policy acceptance_read_of_current_tenant on client_acceptances
    for select using (
        current_is_platform()
        or exists (select 1 from tenants t
                    where t.client_id = client_acceptances.client_id
                      and t.id = current_tenant_id())
    );

drop policy if exists tenant_read_self on tenants;
create policy tenant_read_self on tenants
    for select using (current_is_platform() or id = current_tenant_id());

-- ---------------------------------------------------------------------------
-- The documents and owner records gathered at onboarding
--
-- These carry KYC: PAN, the last four of an Aadhaar, a registered address. The
-- vendor collects them because the law requires it of a platform onboarding a
-- business, and reads them for exactly that purpose. They are the most sensitive
-- rows the vendor can reach, and the reason every read of them is audited
-- (BR-ADM-06).
-- ---------------------------------------------------------------------------

do $$
declare
    t text;
begin
    foreach t in array array['client_documents', 'client_owners'] loop
        if to_regclass(t) is null then
            continue;
        end if;
        execute format('drop policy if exists %I on %I', t || '_read_of_current_tenant', t);
        execute format($f$
            create policy %I on %I for select using (
                current_is_platform()
                or exists (select 1 from tenants tt
                            where tt.client_id = %I.client_id
                              and tt.id = current_tenant_id())
            )$f$, t || '_read_of_current_tenant', t, t);
    end loop;
end
$$;

-- ---------------------------------------------------------------------------
-- Deliberately NOT extended
--
-- Every other table in the schema. When a business-data table is added, it gets
-- the plain `tenant_id = current_tenant_id()` policy from migration 00004 and
-- nothing else. Adding current_is_platform() to one would make the vendor a
-- participant in its customers' businesses, which docs/09 §6 rules out and
-- BR-ADM-14 forbids.
--
-- If a future requirement seems to need it, the answer is almost certainly a
-- client-initiated export (docs/09 §6) or a break-glass support session with the
-- client's consent (BR-SUP-12), not a widened policy.
-- ---------------------------------------------------------------------------

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop policy if exists client_read_of_current_tenant on clients;
create policy client_read_of_current_tenant on clients
    for select using (exists (select 1 from tenants t where t.client_id = clients.id and t.id = current_tenant_id()));

drop policy if exists group_read_of_current_tenant on store_groups;
create policy group_read_of_current_tenant on store_groups
    for select using (exists (select 1 from tenants t where t.group_id = store_groups.id and t.id = current_tenant_id()));

drop policy if exists acceptance_read_of_current_tenant on client_acceptances;
create policy acceptance_read_of_current_tenant on client_acceptances
    for select using (exists (select 1 from tenants t where t.client_id = client_acceptances.client_id and t.id = current_tenant_id()));

drop policy if exists tenant_read_self on tenants;
create policy tenant_read_self on tenants for select using (id = current_tenant_id());

drop function if exists current_is_platform();
-- +goose StatementEnd
