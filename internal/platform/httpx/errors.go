package httpx

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/stephenindia1/steleios-ecom/internal/platform/authz"
	"github.com/stephenindia1/steleios-ecom/internal/platform/ratelimit"
)

// This file is the sole mapping from an error to an HTTP status and a client
// response (docs/03 §6.1, GO-025).
//
// Services never know about status codes. They return domain errors; this layer
// decides what the customer sees. What the customer sees is always generic —
// the detail is logged server-side (BR-SEC-09, GO-028).

// Code is a stable, machine-readable error identifier returned to clients.
//
// Codes are part of the API contract: a frontend switches on them, so they are
// versioned like any other contract element and never repurposed.
type Code string

// The error codes the API returns.
const (
	CodeBadRequest      Code = "bad_request"
	CodeValidation      Code = "validation_failed"
	CodeUnauthenticated Code = "unauthenticated"
	CodeForbidden       Code = "forbidden"
	CodeNotFound        Code = "not_found"
	CodeConflict        Code = "conflict"
	CodeIdempotency     Code = "idempotency_conflict"
	CodeRateLimited     Code = "rate_limited"
	CodeReauthRequired  Code = "reauth_required"
	CodePayloadTooLarge Code = "payload_too_large"
	CodeUnavailable     Code = "service_unavailable"
	CodeTimeout         Code = "timeout"
	CodeInternal        Code = "internal_error"
)

// Error is an error carrying the status and code the client should see.
//
// The Message field is customer-facing and must be safe to display: no
// identifiers of other people's data, no internal detail, no confirmation of
// whether a resource exists (SEC-12, BR-IDN-06). Anything diagnostic goes in
// the wrapped error, which is logged and never serialised.
type Error struct {
	Status  int
	Code    Code
	Message string
	// Fields carries per-field validation messages, so a form can highlight the
	// offending input. Field names are the client's own, never database columns.
	Fields map[string]string
	// RetryAfter is set on 429 and 503 so a client can back off correctly.
	RetryAfter int
	// wrapped is the diagnostic cause. It is logged, never returned.
	wrapped error
}

// Error implements error.
func (e *Error) Error() string {
	if e.wrapped != nil {
		return fmt.Sprintf("%s (%d): %v", e.Code, e.Status, e.wrapped)
	}
	return fmt.Sprintf("%s (%d): %s", e.Code, e.Status, e.Message)
}

// Unwrap exposes the diagnostic cause to errors.Is and errors.As.
func (e *Error) Unwrap() error { return e.wrapped }

// newError builds an Error wrapping cause.
func newError(status int, code Code, message string, cause error) *Error {
	return &Error{Status: status, Code: code, Message: message, wrapped: cause}
}

// BadRequest reports a malformed request. The cause is logged; the client is
// told only that the request was malformed.
func BadRequest(cause error) *Error {
	return newError(http.StatusBadRequest, CodeBadRequest, "The request could not be read.", cause)
}

// Validation reports field-level validation failures. Field messages are safe
// to display and are the one place detail crosses the boundary, because the
// customer needs to know what to fix (GO-028 permits this; the messages are
// authored, not derived from internal errors).
func Validation(fields map[string]string) *Error {
	return &Error{
		Status:  http.StatusUnprocessableEntity,
		Code:    CodeValidation,
		Message: "Some fields need attention.",
		Fields:  fields,
	}
}

// Unauthenticated reports that no valid session was present.
func Unauthenticated(cause error) *Error {
	return newError(http.StatusUnauthorized, CodeUnauthenticated, "Please sign in to continue.", cause)
}

// Forbidden reports that the actor may not perform the action.
func Forbidden(cause error) *Error {
	return newError(http.StatusForbidden, CodeForbidden, "You do not have access to this.", cause)
}

// NotFound reports a missing resource.
//
// It is also what an ownership denial becomes for resources whose existence is
// itself sensitive: telling an attacker "that order exists but is not yours"
// confirms the order number is valid (SEC-12).
func NotFound(cause error) *Error {
	return newError(http.StatusNotFound, CodeNotFound, "Not found.", cause)
}

// Conflict reports that the request contradicts current state — a state machine
// transition that is not allowed, or a uniqueness violation.
func Conflict(message string, cause error) *Error {
	return newError(http.StatusConflict, CodeConflict, message, cause)
}

// ReauthRequired reports that the action needs the password re-entered.
//
// A DISTINCT code, not a plain 403, and that distinction is the point: the
// client must know to prompt for a password rather than show "you do not have
// access", which would be both wrong and a dead end for someone who does have
// access and simply signed in an hour ago (BR-ADM-07).
func ReauthRequired(window time.Duration) *Error {
	return &Error{
		Status:  http.StatusForbidden,
		Code:    CodeReauthRequired,
		Message: fmt.Sprintf("Please confirm your password to continue. This is asked again after %s of inactivity for actions of this kind.", window),
	}
}

// RateLimited reports that a throttle was hit. It names no limit and no
// remaining budget beyond Retry-After, so the limits are not enumerable.
func RateLimited(retryAfterSeconds int, cause error) *Error {
	e := newError(http.StatusTooManyRequests, CodeRateLimited,
		"Too many requests. Please wait a moment and try again.", cause)
	e.RetryAfter = retryAfterSeconds
	return e
}

// PayloadTooLarge reports a body over the configured ceiling (DB-026).
func PayloadTooLarge(cause error) *Error {
	return newError(http.StatusRequestEntityTooLarge, CodePayloadTooLarge,
		"That request was too large.", cause)
}

// Unavailable reports that a dependency could not answer. This is what fail-
// closed looks like from the outside: a refusal, never a silent allow
// (BR-SEC-11).
func Unavailable(cause error) *Error {
	return newError(http.StatusServiceUnavailable, CodeUnavailable,
		"The service is temporarily unavailable. Please try again shortly.", cause)
}

// Timeout reports that the request exceeded its deadline.
func Timeout(cause error) *Error {
	return newError(http.StatusGatewayTimeout, CodeTimeout,
		"That took too long. Please try again.", cause)
}

// Internal reports an unexpected failure. The cause is logged in full; the
// client is told nothing about it (BR-SEC-09).
func Internal(cause error) *Error {
	return newError(http.StatusInternalServerError, CodeInternal,
		"Something went wrong on our side.", cause)
}

// FromError maps any error to the response the client should receive.
//
// This is the only place that decision is made. A handler returns a domain
// error and this function decides the status, so no handler has to know one.
func FromError(err error) *Error {
	if err == nil {
		return nil
	}

	// An error already carrying a status passes through unchanged.
	var httpErr *Error
	if errors.As(err, &httpErr) {
		return httpErr
	}

	switch {
	case errors.Is(err, authz.ErrUnauthenticated):
		return Unauthenticated(err)

	case errors.Is(err, authz.ErrDenied):
		// Denial is rendered as 404 rather than 403 for owned resources,
		// because a 403 confirms the resource exists (SEC-12). The route's
		// policy decides which; the default is the safer one.
		return NotFound(err)

	case errors.Is(err, authz.ErrUnavailable),
		errors.Is(err, ratelimit.ErrUnavailable):
		return Unavailable(err)

	case errors.Is(err, ratelimit.ErrLimited):
		return RateLimited(0, err)

	case errors.Is(err, ErrNotFound):
		return NotFound(err)

	case errors.Is(err, ErrConflict):
		return Conflict("That conflicts with the current state.", err)

	default:
		return Internal(err)
	}
}

// Sentinel errors that domain packages return and this layer maps. They live
// here so a service can signal "not found" without importing net/http.
var (
	// ErrNotFound means the addressed resource does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict means the request contradicts current state.
	ErrConflict = errors.New("conflict")
)
