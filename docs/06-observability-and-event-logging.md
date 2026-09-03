# Steleios — Observability and Event Logging

Engraved rules for logging, events, metrics, tracing and incident probing. **Normative: MUST / MUST NOT.**
Companions: [04-go-and-typescript-standards.md](04-go-and-typescript-standards.md) §9 · [05-data-access-and-performance.md](05-data-access-and-performance.md) · [02-features-and-business-rules.md](02-features-and-business-rules.md).

Status: draft · 3 September 2026

---

## 0. The mandate

> **Every meaningful event is recorded, correlated, and queryable. Given only a customer's order number or a timestamp, an engineer can reconstruct exactly what happened, in what order, caused by whom, and why it failed — without adding logging and redeploying.**

Observability is a build-time property. Instrumentation added while investigating an incident arrives too late for the incident that needed it.

---

## 1. Four streams, four purposes

They are not interchangeable, and a fact belonging in one MUST NOT be substituted by another.

| Stream | Question it answers | Store | Retention | Mutable |
|---|---|---|---|---|
| **Application log** | What did the code do, and why did it fail? | Log store | 30 days (90 for `error`) | No |
| **Domain event log** | What happened in the business, as a fact? | PostgreSQL `domain_events` | 2 years, then archived | Never |
| **Audit log** | Who did what, to which resource, from where? | PostgreSQL `audit_log`, append-only | 7 years (BR-DAT-01) | Never |
| **Metrics & traces** | How much, how fast, where is the time going? | Metrics backend / OTLP | 15 months / 14 days | N/A |

| ID | Rule |
|---|---|
| OBS-001 | A state change MUST emit a domain event. A state change performed by an actor MUST additionally write an audit entry. Neither may be replaced by a log line. |
| OBS-002 | The application log MUST NOT be the system of record for anything. If a fact matters after the retention window, it belongs in the event or audit log. |
| OBS-003 | Every stream carries the same correlation identifiers (§2), so a single query joins them. |

---

## 2. Correlation — the identifiers that make probing possible

| Field | Meaning | Origin |
|---|---|---|
| `request_id` | One inbound HTTP request | Generated in middleware step 1; returned in the `X-Request-Id` response header |
| `trace_id` / `span_id` | Distributed trace | OpenTelemetry, propagated via W3C `traceparent` |
| `correlation_id` | The whole customer journey | The checkout's `request_id`, inherited by every event, task and retry that follows |
| `causation_id` | The direct parent event or request | The `event_id` or `request_id` that caused this one |
| `actor_id`, `actor_type` | Who acted | Session, or a named system actor for workers (WRK-04) |
| `session_fingerprint` | Hashed session reference | Never the session ID itself (SES-010) |
| `order_id`, `payment_id`, `cart_id`, `variant_id` | Business anchors | Present wherever applicable |

| ID | Rule |
|---|---|
| OBS-010 | `request_id` is generated once per request and MUST propagate into every log line, database statement comment, event, audit row, queue payload, and outbound HTTP header. |
| OBS-011 | `correlation_id` and `causation_id` MUST survive queue hops and retries. A task that loses correlation is a defect (QUE-004). |
| OBS-012 | The `X-Request-Id` response header is returned on every response, including errors, so a customer-reported failure can be located from a screenshot. |
| OBS-013 | Queries carry `/* request_id=... module=... */` comments, so `pg_stat_activity` and slow-query logs tie back to application context. |

---

## 3. Domain events — the transactional outbox

Events are written **in the same PostgreSQL transaction as the state change**, then published. This is what makes "every event is logged" a guarantee rather than an aspiration: a crash between the commit and the publish cannot lose the event, and a rollback cannot leave a phantom one.

```sql
create table domain_events (
  id             uuid primary key,
  occurred_at    timestamptz not null default now(),
  name           text not null,            -- 'order.placed'
  version        int  not null default 1,
  aggregate_type text not null,            -- 'order'
  aggregate_id   text not null,
  actor_id       text,
  actor_type     text not null,            -- customer | admin | system | provider
  correlation_id text not null,
  causation_id   text,
  request_id     text,
  payload        jsonb not null,           -- redacted; identifiers and amounts, never PII
  published_at   timestamptz               -- null = pending publication
);
create index on domain_events (aggregate_type, aggregate_id, occurred_at);
create index on domain_events (correlation_id, occurred_at);
create index on domain_events (name, occurred_at desc);
create index on domain_events (occurred_at) where published_at is null;  -- the outbox drain
```

| ID | Rule |
|---|---|
| EVT-001 | Domain events are written inside the same transaction as the state change, through `UnitOfWork` (doc 03 §2.5). An event emitted after commit is prohibited where the event matters. |
| EVT-002 | A relay drains `published_at is null` in `occurred_at` order and enqueues the corresponding asynq tasks, marking rows published. It is idempotent and safe to run on multiple instances (QUE-008). |
| EVT-003 | Events are **immutable facts in the past tense**: `order.placed`, `payment.captured`, `stock.reserved`. Never imperative (`create_order`), never present tense. |
| EVT-004 | Naming is `<aggregate>.<event>`, lowercase, dot-separated. The name MUST NOT change once emitted; a changed shape gets `version + 1`. |
| EVT-005 | Payloads carry identifiers, amounts in paise, status values and reason codes. They MUST NOT carry PII, secrets, card data or raw provider payloads (BR-SEC-07). |
| EVT-006 | Every event payload has a Go struct and a registered JSON schema. An event without a defined shape MUST NOT be emitted. |
| EVT-007 | Consumers MUST tolerate duplicates and out-of-order delivery. Ordering is guaranteed only per aggregate. |
| EVT-008 | Events are never deleted or edited. A mistaken event is corrected by a compensating event. |

### The event catalogue

Every module MUST declare its events in `internal/<module>/events.go` with a doc comment naming the `BR-*` rule each one evidences. The launch catalogue:

| Aggregate | Events |
|---|---|
| `cart` | `created`, `line_added`, `line_updated`, `line_removed`, `merged`, `abandoned` |
| `checkout` | `started`, `repriced`, `blocked` |
| `inventory` | `reserved`, `reservation_expired`, `released`, `committed`, `adjusted`, `low_stock_reached` |
| `pricing` | `quoted`, `coupon_applied`, `coupon_rejected` |
| `order` | `placed`, `payment_failed`, `paid`, `confirmed`, `cancelled`, `expired`, `packed`, `shipped`, `delivered`, `returned`, `refunded` |
| `payment` | `provider_order_created`, `callback_verified`, `callback_rejected`, `webhook_received`, `webhook_rejected`, `webhook_duplicate`, `captured`, `failed`, `amount_mismatch`, `refund_requested`, `refund_processed`, `settlement_imported` |
| `identity` | `registered`, `login_succeeded`, `login_failed`, `otp_sent`, `otp_verified`, `otp_failed`, `password_changed`, `sessions_revoked` |
| `catalog` | `published`, `price_changed`, `archived` |
| `shipping` | `shipment_created`, `dispatched`, `tracking_updated`, `delivery_failed`, `rto_initiated` |
| `admin` | `role_changed`, `refund_approved`, `stock_adjusted`, `price_overridden`, `export_generated` |

| ID | Rule |
|---|---|
| EVT-010 | Every rejection path emits an event, not only success paths. `webhook_rejected`, `coupon_rejected`, `checkout_blocked` and `login_failed` are what make failures probeable. |
| EVT-011 | Every `[MONEY]` and `[SEC]` rule in doc 02 MUST have an event that fires when it is enforced or violated. A rule that silently prevents something cannot be observed. |

---

## 4. Application logging

| ID | Rule |
|---|---|
| LOG-001 | `log/slog` JSON only (GO-080). One logger, built in `platform/logging`, derived per request with correlation fields already bound. |
| LOG-002 | Mandatory fields on every line: `time` (ISO-8601 UTC), `level`, `msg`, `module`, `request_id`, plus `actor_id` where one exists. |
| LOG-003 | Messages are short, static, lowercase strings. Data goes in fields (GO-082), so lines are groupable. `slog.Info("reservation failed", "variant_id", id, "requested", n, "available", a)`. |
| LOG-004 | Log at the boundary that handles the error, once (GO-026). A logged-and-returned error produces the same incident three times at three layers. |
| LOG-005 | Levels: `error` = a human must look; `warn` = unexpected but handled; `info` = state change or key decision; `debug` = development only, disabled in production. An expected business rejection (out of stock, invalid coupon) is `info`, never `error`. |
| LOG-006 | Every request produces exactly one access line at completion with: method, route pattern (never the raw path with IDs), status, duration, bytes, policy name, actor. |
| LOG-007 | Route patterns, not raw paths — `/orders/{id}`, so lines aggregate. |
| LOG-008 | Redaction is centralised in `platform/logging.Redact` and applied by the handler, not by call sites. Phone, email, address, name, card, token, OTP, session ID and raw provider payloads are redacted (BR-SEC-07, BR-NOT-06, BR-PAY-16). |
| LOG-009 | High-volume `debug` and `info` lines are sampled with a stated rate; `warn` and `error` are never sampled. |
| LOG-010 | Log output is stdout, structured, unbuffered on `error`. The application MUST NOT write log files or rotate them itself. |
| LOG-011 | Every security-relevant decision logs at `warn` or above with its reason code: signature rejection, authorization denial, rate limit breach, CSRF failure, idempotency conflict. |

---

## 5. Metrics

RED for every endpoint, USE for every resource, plus business KPIs — because a failure that does not change technical metrics still stops revenue.

| Family | Metrics |
|---|---|
| HTTP | `http_requests_total{route,method,status,policy}`, `http_request_duration_seconds` (histogram), `http_in_flight` |
| Database | `db_query_duration_seconds{module,query}`, `db_rows_returned`, `db_pool_in_use`, `db_pool_waiters` |
| Redis | `redis_command_duration_seconds{cmd}`, `cache_hits_total` / `cache_misses_total{key_class}` |
| Queue | `queue_depth{queue}`, `queue_oldest_pending_seconds{queue}`, `task_duration_seconds{type}`, `task_retries_total{type}`, `task_dead_total{type}` |
| Outbox | `outbox_pending`, `outbox_lag_seconds` |
| Payments | `payment_attempts_total{method,status}`, `webhook_received_total{event,outcome}`, `webhook_signature_failures_total`, `payment_amount_mismatch_total` |
| Business | `checkout_started_total`, `orders_placed_total{method}`, `checkout_failures_total{reason}`, `cart_abandoned_total`, `oversell_attempts_total`, `refunds_total` |
| Security | `auth_failures_total{reason}`, `authz_denials_total{policy}`, `rate_limit_breaches_total{policy}` |

| ID | Rule |
|---|---|
| MET-001 | Metric labels MUST be bounded-cardinality. Order ID, customer ID, SKU, IP and raw path MUST NOT be labels. |
| MET-002 | Latency is a histogram, never an average. p50/p95/p99 are the reported figures. |
| MET-003 | Every failure metric carries a `reason` label drawn from a closed enum, so a spike is diagnosable from the metric alone. |
| MET-004 | Instrumentation lives in middleware and decorators, never inside business methods (GO-086). |

### Alerts

| Alert | Condition | Severity |
|---|---|---|
| Payment failure rate | `payment_attempts{status=failed}` > 20% over 10 min | Page |
| Checkout failure rate | `checkout_failures_total` > 5% of started over 10 min | Page |
| Webhook signature failures | any sustained rate > 0 | Page (security) |
| Payment amount mismatch | any occurrence (BR-PAY-09) | Page |
| Outbox lag | `outbox_lag_seconds` > 60 | Page |
| Queue oldest pending | `critical` queue > 120 s | Page |
| Dead-letter growth | any task type > 0 over 15 min | Ticket |
| Oversell attempts | any occurrence | Ticket |
| Admin auth failures | 5 in 10 min (BR-ADM-08) | Ticket (security) |
| p95 budget breach | any budgeted path (doc 05 §7) for 15 min | Ticket |

| ID | Rule |
|---|---|
| MET-010 | Every alert MUST name a runbook section and a first diagnostic query. An alert without a documented response is noise and gets deleted. |
| MET-011 | Alerts fire on symptoms customers feel (checkout failing), not on causes that may be benign (CPU high). |

---

## 6. Tracing

| ID | Rule |
|---|---|
| TRC-001 | OpenTelemetry, W3C context propagation. A trace spans the HTTP request, every database and Redis call, every outbound provider call, and continues across the queue into the worker. |
| TRC-002 | Span names are low-cardinality: `GET /orders/{id}`, `pg.order.get_by_id`, `razorpay.orders.create`, `task.order.expire_pending`. |
| TRC-003 | Spans carry the correlation identifiers of §2 as attributes. Attributes MUST NOT carry PII or secrets. |
| TRC-004 | Errors are recorded on the span with the reason code, and the span status is set. A failed request MUST be findable by status alone. |
| TRC-005 | Sampling: 100% of errors and of every payment or checkout path; head-based sampling elsewhere with a documented rate. |
| TRC-006 | External calls to Razorpay and couriers are always their own span with the provider's own request identifier as an attribute, so a provider support ticket can be raised from the trace. |

---

## 7. Probing a problem — the standard path

Every incident starts from one of three inputs. All three converge within a couple of queries.

**From a customer's order number**

```sql
-- 1. What the business believes happened, in order.
select occurred_at, name, actor_type, actor_id, causation_id, payload
  from domain_events
 where aggregate_type = 'order' and aggregate_id = $1
 order by occurred_at;

-- 2. The whole journey, including cart, payment and notification events.
select occurred_at, aggregate_type, aggregate_id, name, payload
  from domain_events
 where correlation_id = (select correlation_id from domain_events
                          where aggregate_type='order' and aggregate_id=$1 limit 1)
 order by occurred_at;

-- 3. Who touched it.
select at, actor_id, action, before, after, reason, ip
  from audit_log where resource_type='order' and resource_id=$1 order by at;
```

Then `request_id` from any of those rows retrieves the application logs and the distributed trace.

**From an `X-Request-Id`** — the customer has a screenshot or an email. Logs, trace and events all filter on it directly.

**From a metric spike** — the `reason` label names the failure class; the exemplar on the histogram bucket links to a trace; the trace yields a `request_id`; the `request_id` yields the events.

| ID | Rule |
|---|---|
| PRB-001 | Every one of those queries MUST be index-backed (see the `domain_events` indexes above) and MUST work at production data volumes. |
| PRB-002 | A saved query set for these three paths lives in `docs/runbooks/` and is verified in a quarterly game day. |
| PRB-003 | Any incident that required adding instrumentation to diagnose MUST result in that instrumentation being committed, with a note in the postmortem. |
| PRB-004 | The admin UI surfaces the per-order event and audit timeline directly (doc 01, Phase 5), so support can answer routine questions without an engineer. |

---

## 8. Health, readiness and startup

| ID | Rule |
|---|---|
| HLT-001 | `/healthz` is liveness: the process is running. It MUST NOT check dependencies. |
| HLT-002 | `/readyz` is readiness: PostgreSQL, Redis and the queue are reachable. It MUST fail when a dependency is down so traffic is withdrawn. |
| HLT-003 | Each module implements `Health(ctx)` (doc 03 §2.1); readiness aggregates them and reports per-module status. |
| HLT-004 | Startup logs one structured line with: version, git SHA, build time, environment, effective configuration with secrets redacted, and the route/policy table (doc 03, SEC-03). Enough to answer "what exactly is running" without shell access. |
| HLT-005 | Configuration is validated at startup and the process exits non-zero on invalid config. Starting degraded is prohibited. |

---

## 9. Review checklist — observability

A pull request is rejected if any of these is true.

1. A state change was added with no domain event.
2. An actor-initiated state change was added with no audit entry.
3. A new event has no struct, no registered schema, or is not in the catalogue.
4. An event is emitted outside the transaction that made the change it describes.
5. A failure or rejection path emits nothing.
6. A log line lacks `request_id`, or interpolates data into the message.
7. PII or a secret can reach a log, event payload, metric label or span attribute.
8. A new metric has unbounded label cardinality.
9. A new alert has no runbook entry.
10. Correlation is lost across a queue hop or a retry.
