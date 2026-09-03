package logging

import (
	"log/slog"
	"strings"
)

// Redacted is the replacement written in place of any sensitive value.
const Redacted = "[REDACTED]"

// sensitiveKeys are attribute names whose values must never be written to a log,
// at any nesting depth (BR-SEC-07, BR-NOT-06, BR-PAY-16, LOG-008).
//
// Matching is on a normalised form of the key (lowercased, separators removed),
// so "customerPhone", "customer_phone" and "customer-phone" all match "phone".
// It is deliberately broad: a false redaction costs a debugging round trip, a
// missed one is a data breach.
var sensitiveKeys = map[string]struct{}{
	// Credentials and secrets
	"password": {}, "passwd": {}, "pass": {}, "secret": {}, "token": {},
	"accesstoken": {}, "refreshtoken": {}, "apikey": {}, "key": {},
	"keysecret": {}, "webhooksecret": {}, "signature": {}, "authorization": {},
	"cookie": {}, "setcookie": {}, "sessionid": {}, "sessiontoken": {},
	"csrf": {}, "csrftoken": {}, "otp": {}, "pin": {}, "salt": {}, "hash": {},
	"privatekey": {}, "credential": {}, "credentials": {}, "auth": {},

	// Payment instruments — never received or stored, but defence in depth
	// against a provider payload being logged wholesale (BR-PAY-11).
	"card": {}, "cardnumber": {}, "pan": {}, "cvv": {}, "cvc": {},
	"expiry": {}, "upiid": {}, "vpa": {}, "accountnumber": {}, "ifsc": {},
	"bankaccount": {},

	// Personal data (BR-DAT-06)
	"phone": {}, "phonenumber": {}, "mobile": {}, "email": {}, "emailaddress": {},
	"address": {}, "addressline1": {}, "addressline2": {}, "line1": {}, "line2": {},
	"pincode": {}, "postalcode": {}, "zip": {}, "name": {}, "fullname": {},
	"firstname": {}, "lastname": {}, "dob": {}, "dateofbirth": {}, "gstin": {},
	"pannumber": {}, "aadhaar": {}, "aadhar": {},

	// Raw provider payloads may contain any of the above.
	"payload": {}, "rawbody": {}, "body": {}, "request": {}, "response": {},
}

// allowedSuffixes are key endings that look sensitive but are safe identifiers.
//
// Redacting an order or customer ID would defeat the purpose of the logs —
// correlation depends on them (OBS-002). "customer_id" must survive even though
// it ends in a word we would otherwise treat carefully.
var allowedKeys = map[string]struct{}{
	"requestid": {}, "correlationid": {}, "causationid": {}, "traceid": {},
	"spanid": {}, "actorid": {}, "customerid": {}, "orderid": {}, "cartid": {},
	"variantid": {}, "productid": {}, "paymentid": {}, "batchid": {},
	"eventid": {}, "supplierid": {}, "reservationid": {}, "sessionfingerprint": {},
	"idempotencykey": {}, "ordernumber": {}, "sku": {}, "hsncode": {},
}

// redactAttr is installed as slog's ReplaceAttr. It runs for every attribute of
// every record, including nested groups.
func redactAttr(groups []string, a slog.Attr) slog.Attr {
	if isSensitive(a.Key) {
		return slog.String(a.Key, Redacted)
	}

	// Recurse into groups so a nested {"customer": {"phone": ...}} is caught.
	if a.Value.Kind() == slog.KindGroup {
		attrs := a.Value.Group()
		out := make([]slog.Attr, 0, len(attrs)) // DB-024
		for _, nested := range attrs {
			out = append(out, redactAttr(append(groups, a.Key), nested))
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	}

	return a
}

// isSensitive reports whether a key names a value that must not be logged.
func isSensitive(key string) bool {
	n := normalise(key)
	if n == "" {
		return false
	}
	if _, ok := allowedKeys[n]; ok {
		return false
	}
	if _, ok := sensitiveKeys[n]; ok {
		return true
	}
	// Suffix match catches qualified forms such as "customer_phone" and
	// "razorpay_key_secret" without listing every prefix.
	for k := range sensitiveKeys {
		if len(n) > len(k) && strings.HasSuffix(n, k) {
			return true
		}
	}
	return false
}

// normalise lowercases a key and removes the separators teams mix freely, so
// that one entry in the table covers every spelling.
func normalise(key string) string {
	var b strings.Builder
	b.Grow(len(key))
	for _, r := range key {
		switch {
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		case r == '_' || r == '-' || r == '.' || r == ' ':
			// separator: drop
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Redact returns the value that should be logged in place of s.
//
// Call sites that must log a partial value for support purposes — the last four
// digits of a phone number, say — use [Mask] instead of building their own.
func Redact(string) string { return Redacted }

// Mask keeps the last n characters of s and replaces the rest, for the cases
// where support genuinely needs a partial value (order tracking asks for the
// last four digits of the delivery phone, BR-FUL-04).
func Mask(s string, keep int) string {
	if keep <= 0 || len(s) <= keep {
		return Redacted
	}
	return strings.Repeat("*", len(s)-keep) + s[len(s)-keep:]
}
