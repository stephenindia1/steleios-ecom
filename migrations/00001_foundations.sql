-- +goose Up
-- +goose StatementBegin

-- Foundations: extensions, reference data, and the three append-only ledgers
-- every later migration depends on.
--
-- Migrations are forward-only and are never edited after merge (BR-VER-07).

create extension if not exists pgcrypto;   -- gen_random_uuid
create extension if not exists btree_gist; -- exclusion constraints on GST rates
create extension if not exists pg_trgm;    -- typo-tolerant catalog search (DB-009)
create extension if not exists citext;     -- case-insensitive email (BR-IDN-08)

-- ---------------------------------------------------------------------------
-- Units of measure (BR-UOM-19)
--
-- Reference data, seeded here and not editable at runtime. The base unit of a
-- dimension is chosen fine enough that every sale and purchase conversion is an
-- exact integer (BR-UOM-02).
-- ---------------------------------------------------------------------------
create table uoms (
    code       text primary key,
    dimension  text not null check (dimension in ('mass','volume','length','area','count')),
    uqc        text not null,   -- GST Unique Quantity Code, required on invoices (BR-UOM-11)
    symbol     text not null,
    is_base    boolean not null default false,
    created_at timestamptz not null default now()
);

-- Exactly one base unit per dimension, or conversion has no anchor.
create unique index uoms_one_base_per_dimension
    on uoms (dimension) where is_base;

insert into uoms (code, dimension, uqc, symbol, is_base) values
    ('GRAM',   'mass',   'GMS', 'g',   true),
    ('KG',     'mass',   'KGS', 'kg',  false),
    ('MG',     'mass',   'GMS', 'mg',  false),
    ('ML',     'volume', 'MLT', 'ml',  true),
    ('LTR',    'volume', 'LTR', 'L',   false),
    ('MM',     'length', 'MTR', 'mm',  true),
    ('CM',     'length', 'MTR', 'cm',  false),
    ('MTR',    'length', 'MTR', 'm',   false),
    ('SQMM',   'area',   'SQM', 'mm²', true),
    ('SQM',    'area',   'SQM', 'm²',  false),
    ('PCS',    'count',  'NOS', 'pc',  true),
    ('BOX',    'count',  'BOX', 'box', false),
    ('PKT',    'count',  'PAC', 'pkt', false),
    ('BAG',    'count',  'BAG', 'bag', false),
    ('DOZEN',  'count',  'NOS', 'dz',  false);

-- ---------------------------------------------------------------------------
-- Audit log (BR-ADM-05, BR-ADM-06)
--
-- Append-only: the application role is granted INSERT and SELECT only, in
-- 00002. Retention is 7 years and it is exempt from deletion requests
-- (BR-DAT-01, BR-DAT-03).
-- ---------------------------------------------------------------------------
create table audit_log (
    id             uuid primary key default gen_random_uuid(),
    at             timestamptz not null default now(),
    actor_id       text,
    actor_type     text not null,   -- customer | admin | system | provider
    action         text not null,   -- 'order.cancel', 'role.change'
    resource_type  text not null,
    resource_id    text,
    reason         text,
    before         jsonb,
    after          jsonb,
    request_id     text,
    correlation_id text,
    ip             text,
    user_agent     text
);

-- The two questions asked of the audit log: "what happened to this resource"
-- and "what did this person do". Both are index-backed (DB-001).
create index audit_log_resource on audit_log (resource_type, resource_id, at desc);
create index audit_log_actor    on audit_log (actor_id, at desc) where actor_id is not null;
create index audit_log_at       on audit_log (at desc);
create index audit_log_request  on audit_log (request_id) where request_id is not null;

-- ---------------------------------------------------------------------------
-- Domain events — the transactional outbox (EVT-001, EVT-002)
--
-- Written in the same transaction as the state change they describe, then
-- drained by a relay. That is what makes "every event is logged" a guarantee
-- rather than an aspiration.
-- ---------------------------------------------------------------------------
create table domain_events (
    id             uuid primary key default gen_random_uuid(),
    occurred_at    timestamptz not null default now(),
    name           text not null,             -- 'order.placed' — past tense (EVT-003)
    version        int  not null default 1,   -- shape version (EVT-004)
    aggregate_type text not null,
    aggregate_id   text not null,
    actor_id       text,
    actor_type     text not null,
    correlation_id text not null,
    causation_id   text,
    request_id     text,
    payload        jsonb not null default '{}'::jsonb,  -- redacted (EVT-005)
    published_at   timestamptz
);

create index domain_events_aggregate   on domain_events (aggregate_type, aggregate_id, occurred_at);
create index domain_events_correlation on domain_events (correlation_id, occurred_at);
create index domain_events_name        on domain_events (name, occurred_at desc);

-- The outbox drain. A partial index, so it stays small however large the table
-- grows: only unpublished rows are ever scanned (DB-005).
create index domain_events_outbox
    on domain_events (occurred_at) where published_at is null;

-- ---------------------------------------------------------------------------
-- Webhook events — the payment idempotency ledger (BR-PAY-07)
--
-- The primary key is the provider's own event id. INSERT ... ON CONFLICT DO
-- NOTHING is the whole deduplication mechanism: zero rows inserted means the
-- event was already handled.
-- ---------------------------------------------------------------------------
create table webhook_events (
    id           text primary key,           -- provider event id
    provider     text not null default 'razorpay',
    event        text not null,
    payload      jsonb not null,
    signature_ok boolean not null default true,
    received_at  timestamptz not null default now(),
    processed_at timestamptz,
    error        text
);

create index webhook_events_unprocessed
    on webhook_events (received_at) where processed_at is null;
create index webhook_events_event on webhook_events (event, received_at desc);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
drop table if exists webhook_events;
drop table if exists domain_events;
drop table if exists audit_log;
drop table if exists uoms;
-- +goose StatementEnd
