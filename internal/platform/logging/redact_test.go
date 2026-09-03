package logging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stephenindia1/steleios-ecom/internal/platform/config"
	"github.com/stephenindia1/steleios-ecom/internal/platform/logging"
)

// testLogger builds a JSON logger writing into buf, so tests assert on what
// would actually be shipped rather than on an internal helper.
func testLogger(buf *bytes.Buffer) *slog.Logger {
	return logging.New(config.Config{
		Env:     config.EnvTest,
		Version: "test",
		Log:     config.Log{Level: "debug", Format: "json"},
	}, buf)
}

// lastRecord parses the most recent log line, so a test can assert on fields
// rather than on substrings where structure matters.
func lastRecord(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &m); err != nil {
		t.Fatalf("log line is not JSON: %v\n%s", err, buf.String())
	}
	return m
}

func TestLogOutputIsStructuredJSON(t *testing.T) {
	t.Parallel()

	// LOG-001/003: JSON, with data in fields rather than interpolated into the
	// message, so lines group.
	var buf bytes.Buffer
	testLogger(&buf).Info("reservation failed", "variant_id", "var-1", "requested", 3, "available", 1)

	rec := lastRecord(t, &buf)
	if rec["msg"] != "reservation failed" {
		t.Errorf("msg = %v, want a short static message", rec["msg"])
	}
	if rec["variant_id"] != "var-1" {
		t.Errorf("variant_id = %v, want it as a field", rec["variant_id"])
	}
	if rec["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", rec["level"])
	}
	for _, required := range []string{"time", "level", "msg", "service", "env"} {
		if _, ok := rec[required]; !ok {
			t.Errorf("every line must carry %q (LOG-002): %v", required, rec)
		}
	}
}

func TestRedactionOfSensitiveKeys(t *testing.T) {
	// BR-SEC-07, BR-NOT-06, BR-PAY-16: none of these values may ever reach a log.
	sensitive := []string{
		"password", "Password", "PASSWORD", "pass_word",
		"token", "access_token", "refreshToken",
		"api_key", "key_secret", "webhook_secret", "signature",
		"authorization", "cookie", "session_id", "csrf_token",
		"otp", "pin", "card_number", "cvv", "upi_id", "vpa",
		"account_number", "ifsc",
		"phone", "phone_number", "customer_phone", "mobile",
		"email", "customerEmail", "email_address",
		"address", "address_line1", "pincode",
		"name", "full_name", "first_name",
		"gstin", "aadhaar", "dob",
		"payload", "raw_body",
	}

	for _, key := range sensitive {
		t.Run(key, func(t *testing.T) {
			var buf bytes.Buffer
			log := logging.New(config.Config{
				Env: config.EnvTest, Log: config.Log{Level: "debug", Format: "json"},
			}, &buf)

			const secret = "SUPER-SECRET-VALUE-9999"
			log.Info("under test", key, secret)

			out := buf.String()
			if strings.Contains(out, secret) {
				t.Fatalf("key %q leaked its value into the log: %s", key, out)
			}
			if !strings.Contains(out, logging.Redacted) {
				t.Fatalf("key %q was not redacted: %s", key, out)
			}
		})
	}
}

func TestIdentifiersSurviveRedaction(t *testing.T) {
	// Correlation depends on identifiers being logged (OBS-002). Over-redacting
	// them would make the logs useless, so the allowlist is tested explicitly.
	identifiers := map[string]string{
		"request_id":      "req-123",
		"correlation_id":  "corr-456",
		"causation_id":    "cause-789",
		"trace_id":        "trace-abc",
		"actor_id":        "cust-001",
		"customer_id":     "cust-002",
		"order_id":        "ord-003",
		"cart_id":         "cart-004",
		"variant_id":      "var-005",
		"payment_id":      "pay-006",
		"batch_id":        "batch-007",
		"supplier_id":     "sup-008",
		"order_number":    "STL-26-7K3QP9",
		"sku":             "RICE-500G",
		"hsn_code":        "1006",
		"idempotency_key": "idem-009",
	}

	for key, value := range identifiers {
		t.Run(key, func(t *testing.T) {
			var buf bytes.Buffer
			log := logging.New(config.Config{
				Env: config.EnvTest, Log: config.Log{Level: "debug", Format: "json"},
			}, &buf)

			log.Info("under test", key, value)

			if !strings.Contains(buf.String(), value) {
				t.Errorf("identifier %q was redacted but must survive: %s", key, buf.String())
			}
		})
	}
}

func TestRedactionRecursesIntoGroups(t *testing.T) {
	t.Parallel()

	// A nested payload is exactly how a provider response leaks PII wholesale,
	// so redaction must recurse rather than only inspect top-level keys.
	var buf bytes.Buffer
	log := testLogger(&buf)

	log.With("provider", "razorpay").Info("webhook received",
		slog.Group("customer",
			"phone", "+919876543210",
			"email", "buyer@example.com",
			"customer_id", "cust-1",
		),
		slog.Group("payment",
			slog.Group("method",
				"vpa", "buyer@upi",
				"payment_id", "pay-1",
			),
		),
	)

	out := buf.String()
	for _, secret := range []string{"+919876543210", "buyer@example.com", "buyer@upi"} {
		if strings.Contains(out, secret) {
			t.Errorf("nested value %q leaked: %s", secret, out)
		}
	}
	for _, keep := range []string{"cust-1", "pay-1", "razorpay"} {
		if !strings.Contains(out, keep) {
			t.Errorf("nested identifier %q should survive: %s", keep, out)
		}
	}
}

func TestMask(t *testing.T) {
	t.Parallel()

	// BR-FUL-04: tracking asks for the last four digits of the delivery phone,
	// so a partial reveal is a real requirement — but only through this helper.
	cases := []struct {
		name string
		in   string
		keep int
		want string
	}{
		{name: "last four of a phone", in: "9876543210", keep: 4, want: "******3210"},
		{name: "keep one", in: "abcd", keep: 1, want: "***d"},
		{name: "keep zero redacts entirely", in: "abcd", keep: 0, want: logging.Redacted},
		{name: "negative keep redacts entirely", in: "abcd", keep: -1, want: logging.Redacted},
		{name: "keep equals length redacts entirely", in: "abcd", keep: 4, want: logging.Redacted},
		{name: "keep exceeds length redacts entirely", in: "abc", keep: 10, want: logging.Redacted},
		{name: "empty input", in: "", keep: 4, want: logging.Redacted},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := logging.Mask(tc.in, tc.keep); got != tc.want {
				t.Errorf("Mask(%q, %d) = %q, want %q", tc.in, tc.keep, got, tc.want)
			}
		})
	}
}

func TestCorrelationPropagation(t *testing.T) {
	t.Parallel()

	// OBS-010/011: identifiers must survive being put into and taken out of a
	// context, which is how they cross a queue hop.
	ctx := context.Background()
	var buf bytes.Buffer
	base := logging.New(config.Config{
		Env: config.EnvTest, Log: config.Log{Level: "debug", Format: "json"},
	}, &buf)

	ctx, log := logging.WithRequest(ctx, base, "req-1", "corr-1")
	ctx = logging.WithActor(ctx, "cust-1", "customer")

	if got := logging.RequestID(ctx); got != "req-1" {
		t.Errorf("RequestID = %q, want req-1", got)
	}
	if got := logging.CorrelationID(ctx); got != "corr-1" {
		t.Errorf("CorrelationID = %q, want corr-1", got)
	}
	if got := logging.ActorID(ctx); got != "cust-1" {
		t.Errorf("ActorID = %q, want cust-1", got)
	}
	if got := logging.ActorType(ctx); got != "customer" {
		t.Errorf("ActorType = %q, want customer", got)
	}

	log.Info("bound at request start")
	if out := buf.String(); !strings.Contains(out, "req-1") || !strings.Contains(out, "corr-1") {
		t.Errorf("request logger did not carry correlation: %s", out)
	}

	// FromContext re-attaches everything, including the actor resolved later.
	buf.Reset()
	logging.FromContext(ctx, base).Info("re-attached after a queue hop")
	out := buf.String()
	for _, want := range []string{"req-1", "corr-1", "cust-1", "customer"} {
		if !strings.Contains(out, want) {
			t.Errorf("FromContext dropped %q: %s", want, out)
		}
	}
}

func TestFromContextWithNoValuesReturnsBaseLogger(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	base := logging.New(config.Config{
		Env: config.EnvTest, Log: config.Log{Level: "debug", Format: "json"},
	}, &buf)

	logging.FromContext(context.Background(), base).Info("no correlation available")

	if strings.Contains(buf.String(), `"request_id"`) {
		t.Errorf("empty correlation should not emit empty fields: %s", buf.String())
	}
}

func TestFingerprintIsStableAndNonReversible(t *testing.T) {
	t.Parallel()

	const secret = "session-token-value"
	a := logging.Fingerprint(secret)
	b := logging.Fingerprint(secret)

	if a != b {
		t.Errorf("fingerprint is not stable: %q vs %q", a, b)
	}
	if a == "" {
		t.Fatal("fingerprint is empty")
	}
	if strings.Contains(a, secret) {
		t.Errorf("fingerprint contains the secret: %q", a)
	}
	if logging.Fingerprint("other") == a {
		t.Error("different secrets produced the same fingerprint")
	}
	if len(a) != 12 {
		t.Errorf("fingerprint length = %d, want 12", len(a))
	}
}
