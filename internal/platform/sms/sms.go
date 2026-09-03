// Package sms is the sole implementation of outbound SMS (docs/03 §6.1).
//
// SMS matters more here than it would in most systems, and for one reason: it
// is the only channel Steleios treats as proof. Account recovery, contact
// changes and the first login all verify against the registered mobile, because
// the premise of every one of those flows is that the EMAIL may be lost,
// changed or in someone else's hands (BR-REC-11, BR-REC-14b). An email
// notification in those moments could land in an attacker's inbox.
//
// Two consequences run through this package:
//
//   - A message body is never logged. It carries one-time codes and generated
//     passwords, and a log line is read by more people, for longer, than the
//     message ever was (BR-REC-12, BR-SEC-07).
//
//   - A phone number is redacted in logs to its last four digits, which is
//     enough to answer "did it go to the right number" without recording the
//     number itself.
package sms

import (
	"context"
	"errors"
	"fmt"
)

// Errors this package returns.
var (
	// ErrUnavailable means the provider could not be reached or refused for a
	// reason that may pass. Callers decide whether that is fatal: for a login
	// code it is, for a notification alongside a completed action it is not.
	ErrUnavailable = errors.New("sms: provider unavailable")

	// ErrRejected means the provider refused the message permanently — an
	// invalid number, an unregistered template. Retrying will not help.
	ErrRejected = errors.New("sms: provider rejected the message")

	// ErrNoRecipient means no number was supplied.
	ErrNoRecipient = errors.New("sms: no recipient")
)

// Template names a DLT-registered message template.
//
// Indian telecom regulation (TRAI's DLT regime) requires every transactional
// SMS to match a template registered in advance with the operator, and to be
// sent under a registered sender ID. An unregistered body is dropped by the
// carrier, silently, and the sender is billed. So the CONTENT is not chosen
// here — only which registered template to use and what to fill into it.
type Template string

// The templates Steleios sends. Each needs a DLT registration whose id goes in
// configuration, and the parameter names below must match its variables.
const (
	// TemplateFirstLogin carries the generated password for a newly onboarded
	// owner. Params: client, code, hours.
	TemplateFirstLogin Template = "first_login"

	// TemplateOTP carries a six-digit code for verifying a contact change.
	// Params: code, minutes.
	TemplateOTP Template = "otp"

	// TemplateRecoveryIssued tells an owner their password was reset and by
	// whom, immediately. BR-REC-14 makes this one mandatory: an owner who did
	// not request it must be able to tell from the message alone that something
	// is wrong, without signing in to find out. Params: client, actor.
	TemplateRecoveryIssued Template = "recovery_issued"

	// TemplateEmailChanged names the new address, so a change nobody asked for
	// is visible in the message itself (BR-REC-14a). Params: client, email.
	TemplateEmailChanged Template = "email_changed"
)

// Message is what to send.
type Message struct {
	Template Template
	// Params fill the template's variables. Values may be secrets — a generated
	// password, a one-time code — so this map is never logged.
	Params map[string]string
}

// Sender delivers a message to a mobile number.
//
// Declared here and implemented by the MSG91 client and the development stub.
// Callers depend on this, never on a provider (OOP-05): the provider is a
// commercial decision that will change, and the recovery flow should not.
type Sender interface {
	// Send delivers msg to an E.164 number. It returns ErrRejected for a
	// permanent refusal and ErrUnavailable for one worth retrying.
	Send(ctx context.Context, to string, msg Message) error
}

// Redact returns a phone number with all but its last four digits masked.
//
// Used in every log line that mentions a number. Four digits is what a support
// conversation needs to confirm the right person, and is not enough to reach
// them (BR-SEC-07).
func Redact(phone string) string {
	const keep = 4
	if len(phone) <= keep {
		return "****"
	}
	return "****" + phone[len(phone)-keep:]
}

// validate reports whether a message can be sent at all.
func validate(to string, msg Message) error {
	if to == "" {
		return ErrNoRecipient
	}
	if msg.Template == "" {
		return fmt.Errorf("%w: no template named", ErrRejected)
	}
	return nil
}
