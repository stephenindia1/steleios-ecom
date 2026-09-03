// Package logging builds the one structured logger used by Steleios and owns
// redaction of secrets and PII (docs/03 §6.1, LOG-008).
//
// log/slog with the JSON handler is the only logging mechanism. fmt.Print*,
// log.Print* and println are forbidden by the lint configuration (GO-080).
//
// Redaction is applied by the handler, not by call sites, so a new call site
// cannot forget it. A field named in [sensitiveKeys] is replaced wherever it
// appears, at any nesting depth.
package logging

import (
	"context"
	"io"
	"log/slog"
	"strings"

	"github.com/stephenindia1/steleios-ecom/internal/platform/config"
	"github.com/stephenindia1/steleios-ecom/internal/platform/ids"
)

// Context keys for correlation values carried through a request (OBS-010).
// The key type is unexported so no other package can collide with it (GO-034).
type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota
	ctxKeyCorrelationID
	ctxKeyActorID
	ctxKeyActorType
)

// Field names used across every stream, so a log line, an event and an audit row
// can be joined on the same identifiers (OBS-003).
const (
	FieldRequestID     = "request_id"
	FieldCorrelationID = "correlation_id"
	FieldActorID       = "actor_id"
	FieldActorType     = "actor_type"
	FieldModule        = "module"
	FieldReason        = "reason"
	FieldRoute         = "route"
	FieldPolicy        = "policy"
)

// New builds the application logger.
func New(cfg config.Config, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level:       parseLevel(cfg.Log.Level),
		AddSource:   false,
		ReplaceAttr: redactAttr,
	}

	var h slog.Handler
	if cfg.Log.Format == "text" && !cfg.Env.IsProduction() {
		h = slog.NewTextHandler(w, opts)
	} else {
		h = slog.NewJSONHandler(w, opts)
	}

	return slog.New(h).With(
		slog.String("service", "steleios"),
		slog.String("env", string(cfg.Env)),
		slog.String("version", cfg.Version),
	)
}

// WithRequest returns a context carrying the correlation identifiers, and a
// logger already bound to them so that every subsequent line includes them
// without the call site repeating itself (LOG-002).
func WithRequest(ctx context.Context, log *slog.Logger, requestID, correlationID string) (context.Context, *slog.Logger) {
	ctx = context.WithValue(ctx, ctxKeyRequestID, requestID)
	ctx = context.WithValue(ctx, ctxKeyCorrelationID, correlationID)
	return ctx, log.With(
		slog.String(FieldRequestID, requestID),
		slog.String(FieldCorrelationID, correlationID),
	)
}

// WithActor records who is acting, once authentication has resolved them.
func WithActor(ctx context.Context, actorID, actorType string) context.Context {
	ctx = context.WithValue(ctx, ctxKeyActorID, actorID)
	return context.WithValue(ctx, ctxKeyActorType, actorType)
}

// RequestID returns the request identifier carried by ctx, or "".
func RequestID(ctx context.Context) string { return stringValue(ctx, ctxKeyRequestID) }

// CorrelationID returns the journey identifier carried by ctx, or "".
func CorrelationID(ctx context.Context) string { return stringValue(ctx, ctxKeyCorrelationID) }

// ActorID returns the acting principal's identifier, or "".
func ActorID(ctx context.Context) string { return stringValue(ctx, ctxKeyActorID) }

// ActorType returns the acting principal's type, or "".
func ActorType(ctx context.Context) string { return stringValue(ctx, ctxKeyActorType) }

func stringValue(ctx context.Context, k ctxKey) string {
	v, _ := ctx.Value(k).(string)
	return v
}

// FromContext returns log enriched with whatever correlation values ctx carries.
//
// Worker tasks use this to re-attach correlation after a queue hop (OBS-011).
func FromContext(ctx context.Context, log *slog.Logger) *slog.Logger {
	attrs := make([]any, 0, 4) // DB-024
	if v := RequestID(ctx); v != "" {
		attrs = append(attrs, slog.String(FieldRequestID, v))
	}
	if v := CorrelationID(ctx); v != "" {
		attrs = append(attrs, slog.String(FieldCorrelationID, v))
	}
	if v := ActorID(ctx); v != "" {
		attrs = append(attrs, slog.String(FieldActorID, v))
	}
	if v := ActorType(ctx); v != "" {
		attrs = append(attrs, slog.String(FieldActorType, v))
	}
	if len(attrs) == 0 {
		return log
	}
	return log.With(attrs...)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Fingerprint returns a short non-reversible reference to a secret, for
// correlating a session across lines without ever writing the value
// (SES-010).
func Fingerprint(secret string) string { return ids.Fingerprint(secret) }
