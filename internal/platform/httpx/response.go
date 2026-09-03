package httpx

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// Response is what a handler returns. It carries the status, the body and any
// headers, so a handler never touches the ResponseWriter directly and cannot
// write a partial response before an error occurs.
type Response struct {
	Status  int
	Body    any
	Headers map[string]string
	// Cookies are set on the response. Built with SessionCookie and friends so
	// that the security attributes are decided in one place rather than at each
	// call site (BR-IDN-02).
	Cookies []*http.Cookie
}

// WithCookies returns the response with cookies attached.
func (r Response) WithCookies(cookies ...*http.Cookie) Response {
	r.Cookies = append(r.Cookies, cookies...)
	return r
}

// OK returns 200 with a JSON body.
func OK(body any) Response { return Response{Status: http.StatusOK, Body: body} }

// Created returns 201 with a JSON body and a Location header.
func Created(location string, body any) Response {
	return Response{
		Status:  http.StatusCreated,
		Body:    body,
		Headers: map[string]string{"Location": location},
	}
}

// Accepted returns 202 for work that has been queued rather than completed.
func Accepted(body any) Response { return Response{Status: http.StatusAccepted, Body: body} }

// NoContent returns 204.
func NoContent() Response { return Response{Status: http.StatusNoContent} }

// envelope is the shape of every error response.
//
// Success responses are the resource itself, not wrapped: wrapping every success
// in {"data": ...} costs the client a level of indirection on every call and
// buys nothing. Errors are wrapped, because they need a code and fields.
type envelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code      Code              `json:"code"`
	Message   string            `json:"message"`
	Fields    map[string]string `json:"fields,omitempty"`
	RequestID string            `json:"request_id"`
}

// write serialises a Response.
//
// It writes the body first into a buffer so that a marshalling failure becomes a
// 500 rather than a truncated 200 with a broken body.
func write(w http.ResponseWriter, resp Response) error {
	for k, v := range resp.Headers {
		w.Header().Set(k, v)
	}
	for _, c := range resp.Cookies {
		http.SetCookie(w, c)
	}

	if resp.Body == nil || resp.Status == http.StatusNoContent {
		w.WriteHeader(resp.Status)
		return nil
	}

	buf, err := json.Marshal(resp.Body)
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(buf)))
	w.WriteHeader(resp.Status)
	_, err = w.Write(buf)
	return err
}

// writeError serialises an error response.
//
// The request id is always included, so a customer reporting a failure from a
// screenshot gives support everything needed to find the logs, the trace and
// the events (OBS-012).
func writeError(w http.ResponseWriter, requestID string, e *Error) {
	if e.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(e.RetryAfter))
	}

	body := envelope{Error: errorBody{
		Code:      e.Code,
		Message:   e.Message,
		Fields:    e.Fields,
		RequestID: requestID,
	}}

	buf, err := json.Marshal(body)
	if err != nil {
		// Nothing left to do but emit a bare status. This is unreachable in
		// practice: the envelope contains only strings.
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(buf)))
	w.WriteHeader(e.Status)
	_, _ = w.Write(buf) //nolint:errcheck // GO-021: the response is already committed; a write failure here is the client hanging up and is captured by the access log.
}
