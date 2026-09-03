package redis

import (
	"fmt"
	"time"
)

// Keys are constructed here and nowhere else (RD-003, docs/03 §6.1).
//
// One namespace per concern, each with a stated TTL. A key built inline at a
// call site is how two features end up sharing a namespace and evicting each
// other's data.

// TTLs. Each one is a decision, not a default.
const (
	// TTLSession matches the customer session lifetime (SES-004).
	TTLSession = 30 * 24 * time.Hour
	// TTLAdminSession is deliberately shorter (BR-ADM-07).
	TTLAdminSession = 12 * time.Hour
	// TTLCart keeps a guest cart for the same window as a session (BR-CRT-02).
	TTLCart = 30 * 24 * time.Hour
	// TTLIdempotency retains a response for replay (BR-CHK-03).
	TTLIdempotency = 24 * time.Hour
	// TTLCatalog is a backstop only. Catalog cache is invalidated explicitly on
	// write; it must never be left to expire (BR-CAT-13, RD-007).
	TTLCatalog = time.Hour
	// TTLLock bounds a distributed lock so a crashed holder cannot block work
	// forever (GO-057).
	TTLLock = 5 * time.Minute
)

// Session returns the key holding a session record.
func Session(id string) string { return "sess:" + id }

// SessionIndex returns the set of a customer's session ids, so "sign out
// everywhere" is one lookup rather than a scan of the keyspace (SES-005).
func SessionIndex(customerID string) string { return "sess:user:" + customerID }

// Cart returns the key holding a cart's hot state.
func Cart(cartID string) string { return "cart:" + cartID }

// RateLimit returns the key counting one rule for one subject.
//
// The window start is part of the key, which is what makes the counter a fixed
// window that expires on its own rather than a structure needing pruning.
func RateLimit(policy, scope, subject string, windowStart int64) string {
	return fmt.Sprintf("rl:%s:%s:%s:%d", policy, scope, subject, windowStart)
}

// Idempotency returns the key holding a stored response (BR-CHK-02).
//
// The actor is part of the key so one customer's key cannot collide with, or be
// used to read, another's response.
func Idempotency(actorID, key string) string {
	if actorID == "" {
		actorID = "guest"
	}
	return "idem:" + actorID + ":" + key
}

// CatalogProduct returns the key holding a rendered product payload.
func CatalogProduct(slug string) string { return "cat:product:" + slug }

// CatalogCategory returns the key holding a rendered category listing page.
func CatalogCategory(path string, page int) string {
	return fmt.Sprintf("cat:category:%s:%d", path, page)
}

// Lock returns the key for a named distributed lock (GO-057).
func Lock(name string) string { return "lock:" + name }

// OTP returns the key holding a pending one-time password (BR-IDN-04).
//
// The value is never logged and the key contains the subject, so it must not be
// logged either (SES-010).
func OTP(subject string) string { return "otp:" + subject }
