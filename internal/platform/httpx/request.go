package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/stephenindia1/steleios-ecom/internal/platform/authz"
)

// Request is the handler-facing view of an inbound request.
//
// Handlers receive this rather than *http.Request so that decoding, parameter
// access and actor resolution all go through one validated path (SEC-13).
type Request struct {
	r    *http.Request
	w    http.ResponseWriter
	body []byte // populated only for RawBody policies (BR-PAY-05)
}

// Actor returns the principal established by the session middleware.
//
// It is always present: an unauthenticated request carries a guest actor, not a
// zero value, so a handler cannot mistake "no actor resolved yet" for "guest".
func (req *Request) Actor() authz.Actor {
	if a, ok := req.r.Context().Value(ctxKeyActor).(authz.Actor); ok {
		return a
	}
	return authz.Actor{Type: authz.ActorGuest}
}

// Context returns the request context.
func (req *Request) Context() context.Context { return req.r.Context() }

// Param returns a path parameter, or "" if absent.
func (req *Request) Param(name string) string { return chi.URLParam(req.r, name) }

// Header returns a request header value.
func (req *Request) Header(name string) string { return req.r.Header.Get(name) }

// Method returns the HTTP method.
func (req *Request) Method() string { return req.r.Method }

// RoutePattern returns the matched route pattern, e.g. "/orders/{id}".
//
// Logs and metrics use the pattern, never the raw path, so lines group and
// label cardinality stays bounded (LOG-007, MET-001).
func (req *Request) RoutePattern() string {
	if rc := chi.RouteContext(req.r.Context()); rc != nil {
		return rc.RoutePattern()
	}
	return "unknown"
}

// RawBody returns the buffered body for signature verification.
//
// It is populated only for policies declaring RawBody, and is the exact bytes
// received — reading it after any decoding would defeat HMAC verification
// (BR-PAY-05).
func (req *Request) RawBody() ([]byte, error) {
	if req.body == nil {
		return nil, errors.New("httpx: raw body is not buffered for this route")
	}
	return req.body, nil
}

// Underlying exposes the standard request for the few pieces of middleware and
// infrastructure that genuinely need it. Handlers MUST NOT use it.
func (req *Request) Underlying() *http.Request { return req.r }

// Decode reads and validates a JSON request body into dst.
//
// dst MUST be an explicit request struct enumerating the permitted fields.
// Binding a body to a domain model or a database row is prohibited, because it
// lets a client set fields the endpoint never intended to expose
// (BR-SEC-06, SEC-14).
//
// Unknown fields are rejected rather than ignored: a client sending a field the
// server does not understand is either out of date or probing, and silently
// dropping it hides both.
func (req *Request) Decode(dst any) error {
	if req.r.Body == nil {
		return BadRequest(errors.New("empty body"))
	}

	if ct := req.r.Header.Get("Content-Type"); ct != "" {
		media := strings.TrimSpace(strings.Split(ct, ";")[0])
		if !strings.EqualFold(media, "application/json") {
			return BadRequest(fmt.Errorf("unsupported content type %q", media))
		}
	}

	dec := json.NewDecoder(req.r.Body)
	dec.DisallowUnknownFields() // BR-SEC-06

	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return PayloadTooLarge(err)
		}
		var syntaxErr *json.SyntaxError
		var typeErr *json.UnmarshalTypeError
		switch {
		case errors.As(err, &syntaxErr):
			return BadRequest(fmt.Errorf("malformed JSON at byte %d: %w", syntaxErr.Offset, err))
		case errors.As(err, &typeErr):
			return Validation(map[string]string{
				typeErr.Field: fmt.Sprintf("expected %s", typeErr.Type),
			})
		case errors.Is(err, io.EOF):
			return BadRequest(errors.New("empty body"))
		default:
			return BadRequest(err)
		}
	}

	// Exactly one JSON value per body. Trailing content means the client sent
	// something we do not understand.
	if err := dec.Decode(new(struct{})); !errors.Is(err, io.EOF) {
		return BadRequest(errors.New("body contains more than one JSON value"))
	}

	// If the request type validates itself, run it. This is where business
	// validation lives for shape-level rules; semantic rules belong in the
	// service, which re-validates at the state change (CLAUDE.md rule 5).
	if v, ok := dst.(Validatable); ok {
		if fields := v.Validate(); len(fields) > 0 {
			return Validation(fields)
		}
	}
	return nil
}

// Validatable is implemented by request types that carry their own field-level
// validation. Returning a non-empty map produces a 422 naming each field.
type Validatable interface {
	Validate() map[string]string
}

// Query returns a query-string value.
func (req *Request) Query(name string) string { return req.r.URL.Query().Get(name) }

// QueryInt reads a bounded integer query parameter.
//
// Bounds are mandatory: an unbounded numeric parameter is how a client asks for
// a million rows (DB-020).
func (req *Request) QueryInt(name string, def, minValue, maxValue int) (int, error) {
	raw := req.Query(name)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, Validation(map[string]string{name: "must be a whole number"})
	}
	if n < minValue || n > maxValue {
		return 0, Validation(map[string]string{
			name: fmt.Sprintf("must be between %d and %d", minValue, maxValue),
		})
	}
	return n, nil
}

// QueryEnum reads a query parameter constrained to an allowlist.
//
// Sort fields and filter names are matched against an allowlist rather than
// interpolated, because a client-supplied column name reaching SQL is an
// injection vector (DB-041).
func (req *Request) QueryEnum(name, def string, allowed ...string) (string, error) {
	raw := req.Query(name)
	if raw == "" {
		return def, nil
	}
	for _, a := range allowed {
		if raw == a {
			return raw, nil
		}
	}
	return "", Validation(map[string]string{
		name: "must be one of: " + strings.Join(allowed, ", "),
	})
}
