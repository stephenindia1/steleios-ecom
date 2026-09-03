// Package ids is the sole source of identifiers in Steleios (docs/03 §6.1).
//
// Internal identifiers are UUIDv7: unguessable, but time-ordered, so they index
// well and do not fragment B-trees the way UUIDv4 primary keys do (DB-003).
// Customer-facing order numbers are separately generated and must not be
// enumerable (BR-ORD-05).
//
// Randomness always comes from crypto/rand (GO-076).
package ids

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// New returns a time-ordered UUIDv7.
func New() (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("ids: generate uuidv7: %w", err)
	}
	return id, nil
}

// MustNew is New for call sites where failure is a programmer or platform error
// that cannot be handled meaningfully.
func MustNew() uuid.UUID {
	id, err := New()
	if err != nil {
		panic(err) // GO-027: the entropy source failing is not a business condition
	}
	return id
}

// Token returns a URL-safe random token of n bytes of entropy.
//
// Used for session identifiers (SES-001), CSRF secrets, password-reset tokens
// and idempotency keys.
func Token(nBytes int) (string, error) {
	if nBytes < 16 {
		return "", fmt.Errorf("ids: token needs at least 16 bytes of entropy, got %d", nBytes)
	}
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("ids: read entropy: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// SessionToken returns a 256-bit session identifier (SES-001).
func SessionToken() (string, error) { return Token(32) }

// orderNumberAlphabet excludes I, L, O, U and 0, 1 so a number read over the
// phone or copied from a printed invoice cannot be transcribed wrongly.
const orderNumberAlphabet = "23456789ABCDEFGHJKMNPQRSTVWXYZ"

// OrderNumber returns a customer-facing order number of the form
// STL-26-7K3QP9.
//
// The suffix is random rather than sequential: a sequential number leaks order
// volume and invites enumeration of other customers' orders (BR-ORD-05). Note
// that invoice numbers are the opposite — they must be gapless and sequential
// per financial year (BR-ORD-10) — and are generated elsewhere, serialized in
// the database.
func OrderNumber(now time.Time) (string, error) {
	const suffixLen = 6

	b := make([]byte, suffixLen)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("ids: read entropy: %w", err)
	}

	var sb strings.Builder
	sb.Grow(len("STL-26-") + suffixLen) // DB-024: the size is known
	fmt.Fprintf(&sb, "STL-%02d-", now.Year()%100)
	for _, v := range b {
		// The alphabet length (30) does not divide 256 evenly, so this introduces
		// a slight bias. That is acceptable here: the suffix is an
		// unguessability measure backed by a uniqueness constraint, not a
		// cryptographic key. Uniqueness is enforced by the database, and a
		// collision is retried.
		sb.WriteByte(orderNumberAlphabet[int(v)%len(orderNumberAlphabet)])
	}
	return sb.String(), nil
}

// Fingerprint returns a short, stable, non-reversible reference to a secret.
//
// It exists so that a session or token can be correlated across log lines and
// audit rows without the value itself ever being written (SES-010, BR-SEC-07).
// Twelve base32 characters is 60 bits — ample to distinguish sessions, far too
// little to brute-force back to a 256-bit token.
func Fingerprint(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).
		EncodeToString(sum[:])[:12])
}
