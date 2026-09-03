-- +goose Up
-- +goose StatementBegin

-- Onboarding dates for clients and shops.
--
-- created_at is when the record was made; onboarded_at is when the business
-- actually went live. They are usually different — a client is created during
-- a sales conversation and goes live weeks later — and conflating them makes
-- every tenure figure wrong: how long they have been a customer, how quickly
-- they got going, whether a churned client ever really started.
--
-- Nothing here is a substitute for the event and audit history. Those record
-- what happened; these are the two dates the business asks for constantly.

alter table clients add column onboarded_at  timestamptz;
alter table clients add column onboarded_by  text;          -- vendor staff, from the billing side
alter table clients add column trial_ends_at timestamptz;
alter table clients add column churned_at    timestamptz;
alter table clients add column churn_reason  text;

-- A client is live from onboarding to churn, and cannot churn before it starts.
alter table clients add constraint clients_churn_after_onboarding
    check (churned_at is null or onboarded_at is null or churned_at >= onboarded_at);

-- The two questions asked of this table: who is live, and who onboarded when.
create index clients_onboarded on clients (onboarded_at) where onboarded_at is not null;
create index clients_live      on clients (onboarded_at)
    where onboarded_at is not null and churned_at is null;

-- Existing rows predate this column. Backfilling them with created_at would
-- assert a go-live date nobody recorded, so they stay null and read as "not
-- onboarded" — which is true (BR-VER-09: never invent history).

-- A shop opens on its own date. A client's second branch onboards long after
-- the client did, and reporting that both started on the client's date would
-- understate how long it took to open the second one.
alter table tenants add column opened_at timestamptz;
alter table tenants add column closed_at timestamptz;

alter table tenants add constraint tenants_closed_after_opened
    check (closed_at is null or opened_at is null or closed_at >= opened_at);

create index tenants_open on tenants (client_id, opened_at)
    where opened_at is not null and closed_at is null;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop index if exists tenants_open;
alter table tenants drop constraint if exists tenants_closed_after_opened;
alter table tenants drop column if exists closed_at;
alter table tenants drop column if exists opened_at;
drop index if exists clients_live;
drop index if exists clients_onboarded;
alter table clients drop constraint if exists clients_churn_after_onboarding;
alter table clients drop column if exists churn_reason;
alter table clients drop column if exists churned_at;
alter table clients drop column if exists trial_ends_at;
alter table clients drop column if exists onboarded_by;
alter table clients drop column if exists onboarded_at;
-- +goose StatementEnd
