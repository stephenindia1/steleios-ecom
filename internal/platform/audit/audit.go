// Package audit is the sole implementation of audit writing (docs/03 §6.1).
//
// The audit log answers "who did what, to which resource, when, from where"
// (BR-ADM-06). It is append-only — no UPDATE, no DELETE — enforced by database
// permissions on the application role, not by convention (BR-ADM-05).
//
// It is a separate stream from the application log and from domain events, and
// none of the three substitutes for another (OBS-001).
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/stephenindia1/steleios-ecom/internal/platform/clock"
	"github.com/stephenindia1/steleios-ecom/internal/platform/logging"
	"github.com/stephenindia1/steleios-ecom/internal/platform/postgres"
)

// Entry is one audited action.
//
// Before and After hold the changed fields only, already redacted. They are
// `any` because they describe arbitrary domain state; the recorder marshals
// them and rejects anything that will not encode, rather than writing a broken
// row (GO-048 justification).
type Entry struct {
	Action       string
	ResourceType string
	ResourceID   string
	Reason       string
	Before       any
	After        any
}

// Recorder writes audit entries.
//
// It takes a context rather than an actor argument because the actor, request
// id and IP are already on the context by the time any service runs; making the
// caller pass them again invites them being passed wrongly.
type Recorder interface {
	Record(ctx context.Context, e Entry) error
	// RecordTx writes inside an existing transaction, so a state change and its
	// audit row commit together or not at all.
	RecordTx(ctx context.Context, q postgres.Querier, e Entry) error
}

// Writer is the PostgreSQL-backed recorder.
type Writer struct {
	pool  *postgres.Pool
	clock clock.Clock
}

// NewWriter returns the production audit recorder.
func NewWriter(pool *postgres.Pool, clk clock.Clock) (*Writer, error) {
	if pool == nil || clk == nil {
		return nil, errors.New("audit: nil dependency")
	}
	return &Writer{pool: pool, clock: clk}, nil
}

const insertSQL = `
insert into audit_log (
  id, at, actor_id, actor_type, action,
  resource_type, resource_id, reason,
  before, after, request_id, correlation_id, ip, user_agent
) values (
  gen_random_uuid(), $1, $2, $3, $4,
  $5, $6, $7,
  $8, $9, $10, $11, $12, $13
)`

// Record writes an entry outside a transaction.
func (w *Writer) Record(ctx context.Context, e Entry) error {
	return w.pool.Read(ctx, func(r postgres.Repos) error {
		return w.RecordTx(ctx, r.Querier(), e)
	})
}

// RecordTx writes an entry using the given handle.
//
// Services call this inside UnitOfWork.Do so that the audit row and the change
// it describes share a transaction. An audit entry that survives a rolled-back
// change is a lie about what happened.
func (w *Writer) RecordTx(ctx context.Context, q postgres.Querier, e Entry) error {
	if e.Action == "" || e.ResourceType == "" {
		return fmt.Errorf("audit: entry needs an action and a resource type, got %q/%q",
			e.Action, e.ResourceType)
	}

	before, err := encode(e.Before)
	if err != nil {
		return fmt.Errorf("audit: encode before: %w", err)
	}
	after, err := encode(e.After)
	if err != nil {
		return fmt.Errorf("audit: encode after: %w", err)
	}

	_, err = q.Exec(ctx, insertSQL,
		w.clock.Now(),
		nullable(logging.ActorID(ctx)),
		defaulted(logging.ActorType(ctx), "unknown"),
		e.Action,
		e.ResourceType,
		nullable(e.ResourceID),
		nullable(e.Reason),
		before,
		after,
		nullable(logging.RequestID(ctx)),
		nullable(logging.CorrelationID(ctx)),
		nullable(IPFrom(ctx)),
		nullable(UserAgentFrom(ctx)),
	)
	if err != nil {
		return fmt.Errorf("audit: write %s on %s: %w", e.Action, e.ResourceType, err)
	}
	return nil
}

// encode marshals a state snapshot, treating nil as SQL null.
func encode(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func defaulted(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// Request metadata carried on the context by the HTTP layer.
type ctxKey int

const (
	ctxKeyIP ctxKey = iota
	ctxKeyUserAgent
)

// WithRequestMetadata records the caller's address and user agent for the audit
// row. "From where" is part of the required record (BR-ADM-06).
func WithRequestMetadata(ctx context.Context, ip, userAgent string) context.Context {
	ctx = context.WithValue(ctx, ctxKeyIP, ip)
	return context.WithValue(ctx, ctxKeyUserAgent, userAgent)
}

// IPFrom returns the caller's address, or "".
func IPFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyIP).(string)
	return v
}

// UserAgentFrom returns the caller's user agent, or "".
func UserAgentFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyUserAgent).(string)
	return v
}

// Recording is a test double that captures entries in memory.
//
// It lives here rather than in a test file so that every module's tests can
// assert "this action was audited" without each writing its own fake (DRY).
type Recording struct {
	Entries []Entry
	Err     error
}

// Record captures an entry.
func (r *Recording) Record(_ context.Context, e Entry) error {
	if r.Err != nil {
		return r.Err
	}
	r.Entries = append(r.Entries, e)
	return nil
}

// RecordTx captures an entry, ignoring the handle.
func (r *Recording) RecordTx(_ context.Context, _ postgres.Querier, e Entry) error {
	if r.Err != nil {
		return r.Err
	}
	r.Entries = append(r.Entries, e)
	return nil
}

// Has reports whether an action was recorded against a resource.
func (r *Recording) Has(action, resourceID string) bool {
	for _, e := range r.Entries {
		if e.Action == action && e.ResourceID == resourceID {
			return true
		}
	}
	return false
}

// Retention is how long audit entries are kept (BR-DAT-01). It is a constant
// rather than configuration because it is a legal obligation, not a preference.
const Retention = 7 * 365 * 24 * time.Hour
