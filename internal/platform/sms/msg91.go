package sms

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/stephenindia1/steleios-ecom/internal/platform/config"
)

// MSG91 sends through msg91.com.
//
// Chosen because it is an Indian provider with DLT registration built into its
// flow, which matters: under TRAI's DLT regime a transactional SMS must match a
// template registered in advance, sent under a registered sender ID, or the
// carrier drops it silently and bills for it. A provider that does not model
// templates makes that failure invisible.
type MSG91 struct {
	client   *http.Client
	authKey  string
	senderID string
	// templates maps our template names to MSG91 flow ids. A message whose
	// template has no id configured is refused rather than sent as free text:
	// free text is what gets dropped by the carrier.
	templates map[Template]string
	log       *slog.Logger
}

// msg91Endpoint is the flow API. Flows are MSG91's name for DLT templates.
const msg91Endpoint = "https://control.msg91.com/api/v5/flow/"

// NewMSG91 builds the provider client.
func NewMSG91(cfg config.SMS, log *slog.Logger) (*MSG91, error) {
	if cfg.AuthKey == "" {
		return nil, fmt.Errorf("sms: MSG91_AUTH_KEY is required when the provider is msg91")
	}
	if cfg.SenderID == "" {
		return nil, fmt.Errorf("sms: MSG91_SENDER_ID is required when the provider is msg91")
	}

	templates := map[Template]string{
		TemplateFirstLogin:     cfg.TemplateFirstLogin,
		TemplateOTP:            cfg.TemplateOTP,
		TemplateRecoveryIssued: cfg.TemplateRecoveryIssued,
		TemplateEmailChanged:   cfg.TemplateEmailChanged,
	}
	for name, id := range templates {
		if id == "" {
			return nil, fmt.Errorf("sms: no DLT template id configured for %q; "+
				"an unregistered message is dropped by the carrier and still billed", name)
		}
	}

	return &MSG91{
		// An explicit timeout, not http.DefaultClient: a provider that hangs
		// must not hold a request open. The default client has no timeout at
		// all, which is the classic way one slow dependency stalls a service.
		client:    &http.Client{Timeout: 10 * time.Second},
		authKey:   cfg.AuthKey,
		senderID:  cfg.SenderID,
		templates: templates,
		log:       log,
	}, nil
}

// msg91Request is the flow API's payload.
type msg91Request struct {
	TemplateID string                 `json:"template_id"`
	ShortURL   string                 `json:"short_url"`
	Recipients []map[string]string    `json:"recipients"`
	Extra      map[string]interface{} `json:"-"`
}

// msg91Response is what the API returns. It answers 200 for a refusal as well
// as a success, with the difference in the body, so the status code alone is
// not enough to know whether the message went.
type msg91Response struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// Send delivers a message through MSG91.
func (m *MSG91) Send(ctx context.Context, to string, msg Message) error {
	if err := validate(to, msg); err != nil {
		return err
	}

	templateID, ok := m.templates[msg.Template]
	if !ok {
		return fmt.Errorf("%w: unknown template %q", ErrRejected, msg.Template)
	}

	// MSG91 wants the number without a leading '+'.
	recipient := map[string]string{"mobiles": strings.TrimPrefix(to, "+")}
	for k, v := range msg.Params {
		recipient[k] = v
	}

	body, err := json.Marshal(msg91Request{
		TemplateID: templateID,
		// Off deliberately. Link shortening rewrites URLs through the
		// provider's domain, which trains people to trust a redirect in a
		// message about their own account — the exact shape of a phishing SMS.
		ShortURL:   "0",
		Recipients: []map[string]string{recipient},
	})
	if err != nil {
		return fmt.Errorf("%w: encode request: %w", ErrRejected, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, msg91Endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w: build request: %w", ErrUnavailable, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("authkey", m.authKey)

	resp, err := m.client.Do(req)
	if err != nil {
		// Note what is NOT in this message: the body. It holds the code.
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // response teardown

	// Bounded read: a provider returning an unexpected large body must not be
	// able to make us allocate it (DB-026 applied outbound).
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if err != nil {
		return fmt.Errorf("%w: read response: %w", ErrUnavailable, err)
	}

	if resp.StatusCode >= 500 {
		return fmt.Errorf("%w: provider returned %d", ErrUnavailable, resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%w: provider returned %d", ErrRejected, resp.StatusCode)
	}

	// A 200 with type "error" is a refusal. Trusting the status code alone here
	// would report every rejected message as delivered.
	var parsed msg91Response
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("%w: unreadable provider response", ErrUnavailable)
	}
	if !strings.EqualFold(parsed.Type, "success") {
		// parsed.Message is the provider's own explanation — "template not
		// found", "invalid number". It describes OUR configuration, not the
		// message content, so it is safe to log and useful to have.
		return fmt.Errorf("%w: %s", ErrRejected, parsed.Message)
	}

	m.log.InfoContext(ctx, "sms sent",
		"template", string(msg.Template), "to", Redact(to))
	return nil
}

// Log is the development sender.
//
// It writes that a message WOULD have been sent, with the template and a
// redacted number, and never the parameters — the same discipline as the real
// one, so a habit formed against the stub is the right habit. The code itself is
// available where a developer needs it: the API returns the generated password
// in the response to the vendor who asked for it.
type Log struct{ log *slog.Logger }

// NewLog returns the development sender.
func NewLog(log *slog.Logger) *Log { return &Log{log: log} }

// Send records the message without sending it.
func (l *Log) Send(ctx context.Context, to string, msg Message) error {
	if err := validate(to, msg); err != nil {
		return err
	}
	l.log.InfoContext(ctx, "sms not sent: no provider configured",
		"template", string(msg.Template), "to", Redact(to))
	return nil
}
