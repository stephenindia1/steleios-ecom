package sms_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/stephenindia1/steleios-ecom/internal/platform/config"
	"github.com/stephenindia1/steleios-ecom/internal/platform/sms"
)

func TestRedactKeepsOnlyTheLastFourDigits(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in, want string
	}{
		{"+919876543210", "****3210"},
		{"+14155550123", "****0123"},
		{"3210", "****"},
		{"", "****"},
		{"21", "****"},
	}

	for _, tc := range cases {
		if got := sms.Redact(tc.in); got != tc.want {
			t.Errorf("Redact(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestRedactNeverLeavesEnoughToDialAgain is the property that matters, stated
// separately from the exact format so it survives a change to it.
//
// Four digits is what a support conversation needs to confirm the right person,
// and is not enough to reach them (BR-SEC-07).
func TestRedactNeverLeavesEnoughToDialAgain(t *testing.T) {
	t.Parallel()

	const number = "+919876543210"
	got := sms.Redact(number)

	if strings.Contains(got, "98765") {
		t.Fatalf("Redact(%q) = %q, which still carries the subscriber digits", number, got)
	}
	if len(strings.TrimLeft(got, "*")) > 4 {
		t.Fatalf("Redact left %d digits, want at most 4", len(strings.TrimLeft(got, "*")))
	}
}

func TestMSG91RefusesToStartWithoutCredentials(t *testing.T) {
	t.Parallel()

	log := slog.New(slog.NewJSONHandler(io.Discard, nil))

	complete := func() config.SMS {
		return config.SMS{
			Provider: "msg91", AuthKey: "key", SenderID: "STLEIO",
			TemplateFirstLogin:     "t1",
			TemplateOTP:            "t2",
			TemplateRecoveryIssued: "t3",
			TemplateEmailChanged:   "t4",
		}
	}

	if _, err := sms.NewMSG91(complete(), log); err != nil {
		t.Fatalf("a complete configuration was refused: %v", err)
	}

	cases := []struct {
		name   string
		break_ func(*config.SMS)
	}{
		{"no auth key", func(c *config.SMS) { c.AuthKey = "" }},
		{"no sender id", func(c *config.SMS) { c.SenderID = "" }},
		// A missing DLT template id is a startup failure and not a runtime one,
		// because an unregistered message is dropped by the carrier SILENTLY
		// and still billed. Nobody would notice at runtime.
		{"no first-login template", func(c *config.SMS) { c.TemplateFirstLogin = "" }},
		{"no OTP template", func(c *config.SMS) { c.TemplateOTP = "" }},
		{"no recovery template", func(c *config.SMS) { c.TemplateRecoveryIssued = "" }},
		{"no email-changed template", func(c *config.SMS) { c.TemplateEmailChanged = "" }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := complete()
			tc.break_(&cfg)
			if _, err := sms.NewMSG91(cfg, log); err == nil {
				t.Fatal("the provider started with an incomplete configuration")
			}
		})
	}
}

func TestSendersRejectAnEmptyRecipient(t *testing.T) {
	t.Parallel()

	sender := sms.NewLog(slog.New(slog.NewJSONHandler(io.Discard, nil)))

	err := sender.Send(context.Background(), "", sms.Message{Template: sms.TemplateOTP})
	if !errors.Is(err, sms.ErrNoRecipient) {
		t.Fatalf("Send with no recipient = %v, want ErrNoRecipient", err)
	}
}

func TestSendersRejectAMessageWithNoTemplate(t *testing.T) {
	t.Parallel()

	sender := sms.NewLog(slog.New(slog.NewJSONHandler(io.Discard, nil)))

	// Free text is what the carrier drops. A message with no template named is
	// refused here rather than sent and silently lost.
	err := sender.Send(context.Background(), "+919876543210", sms.Message{})
	if !errors.Is(err, sms.ErrRejected) {
		t.Fatalf("Send with no template = %v, want ErrRejected", err)
	}
}

// TestTheLogSenderNeverWritesTheMessageParameters.
//
// The stub exists for development, and a habit formed against it is the habit
// that ships. Its parameters carry one-time codes and generated passwords, and
// a log line is read by more people, for longer, than the message ever was
// (BR-REC-12, BR-SEC-07).
func TestTheLogSenderNeverWritesTheMessageParameters(t *testing.T) {
	t.Parallel()

	var buf strings.Builder
	sender := sms.NewLog(slog.New(slog.NewTextHandler(&buf, nil)))

	const secret = "marigold-thistle-copper-lantern"
	err := sender.Send(context.Background(), "+919876543210", sms.Message{
		Template: sms.TemplateFirstLogin,
		Params:   map[string]string{"code": secret, "client": "STL-C-000001"},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	out := buf.String()
	if strings.Contains(out, secret) {
		t.Fatalf("the generated password was written to the log:\n%s", out)
	}
	if strings.Contains(out, "9876543") {
		t.Fatalf("the full number was written to the log:\n%s", out)
	}
	// It must still say enough to be useful.
	if !strings.Contains(out, string(sms.TemplateFirstLogin)) {
		t.Errorf("the log does not name the template:\n%s", out)
	}
}
