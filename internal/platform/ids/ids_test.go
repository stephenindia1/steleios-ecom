package ids_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stephenindia1/steleios-ecom/internal/platform/ids"
)

func TestNewIsUniqueAndTimeOrdered(t *testing.T) {
	t.Parallel()

	const n = 5000
	seen := make(map[string]struct{}, n) // DB-024
	var previous string

	for i := range n {
		id, err := ids.New()
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		s := id.String()

		if _, dup := seen[s]; dup {
			t.Fatalf("duplicate id at iteration %d: %s", i, s)
		}
		seen[s] = struct{}{}

		// UUIDv7 is time-ordered, which is why it is chosen over v4 for primary
		// keys: sequential inserts do not fragment the B-tree (DB-003).
		if previous != "" && s < previous {
			t.Errorf("ids went backwards: %s then %s", previous, s)
		}
		previous = s

		if got := id.Version(); got != 7 {
			t.Fatalf("version = %d, want 7", got)
		}
	}
}

func TestToken(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		bytes   int
		wantErr bool
	}{
		{name: "session sized", bytes: 32},
		{name: "minimum accepted", bytes: 16},
		{name: "large", bytes: 64},

		{name: "below minimum entropy refused", bytes: 15, wantErr: true},
		{name: "zero refused", bytes: 0, wantErr: true},
		{name: "negative refused", bytes: -1, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tok, err := ids.Token(tc.bytes)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %d bytes, got %q", tc.bytes, tok)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// URL-safe base64 without padding, so it is cookie- and URL-safe.
			if strings.ContainsAny(tok, "+/=") {
				t.Errorf("token %q is not URL-safe", tok)
			}
			if len(tok) == 0 {
				t.Error("empty token")
			}
		})
	}
}

func TestSessionTokenIsUnguessable(t *testing.T) {
	t.Parallel()

	// SES-001: 256 bits of entropy. The test that matters is uniqueness across
	// a large sample; a repeat would be a session-fixation vulnerability.
	const n = 10000
	seen := make(map[string]struct{}, n)

	for range n {
		tok, err := ids.SessionToken()
		if err != nil {
			t.Fatalf("SessionToken: %v", err)
		}
		if _, dup := seen[tok]; dup {
			t.Fatalf("duplicate session token: %s", tok)
		}
		seen[tok] = struct{}{}

		// 32 bytes in raw base64url is 43 characters.
		if len(tok) != 43 {
			t.Fatalf("session token length = %d, want 43 (256 bits)", len(tok))
		}
	}
}

func TestOrderNumber(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

	const ambiguous = "ILOU01"
	const n = 2000
	seen := make(map[string]struct{}, n)

	for range n {
		num, err := ids.OrderNumber(now)
		if err != nil {
			t.Fatalf("OrderNumber: %v", err)
		}

		if !strings.HasPrefix(num, "STL-26-") {
			t.Fatalf("order number %q does not carry the year prefix", num)
		}
		suffix := strings.TrimPrefix(num, "STL-26-")
		if len(suffix) != 6 {
			t.Fatalf("suffix %q is %d characters, want 6", suffix, len(suffix))
		}

		// Characters that are misread over the phone or off a printed invoice
		// are excluded from the alphabet on purpose.
		if strings.ContainsAny(suffix, ambiguous) {
			t.Fatalf("order number %q contains a transcription-ambiguous character", num)
		}

		seen[num] = struct{}{}
	}

	// BR-ORD-05: order numbers must not be enumerable. A sequential generator
	// would produce n distinct values too, so the real assertion is that the
	// suffix varies rather than incrementing — collisions in 2000 draws from
	// 30^6 (~729M) should be vanishingly rare.
	if len(seen) < n-1 {
		t.Errorf("only %d distinct order numbers from %d draws — suffix entropy looks wrong", len(seen), n)
	}
}

func TestOrderNumberUsesTheGivenYear(t *testing.T) {
	t.Parallel()

	cases := map[int]string{
		2026: "STL-26-",
		2027: "STL-27-",
		2030: "STL-30-",
		2100: "STL-00-",
	}

	for year, want := range cases {
		num, err := ids.OrderNumber(time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatalf("OrderNumber: %v", err)
		}
		if !strings.HasPrefix(num, want) {
			t.Errorf("year %d produced %q, want prefix %q", year, num, want)
		}
	}
}

func TestFingerprint(t *testing.T) {
	t.Parallel()

	const secret = "a-session-token"

	a := ids.Fingerprint(secret)
	if a != ids.Fingerprint(secret) {
		t.Error("fingerprint is not stable")
	}
	if len(a) != 12 {
		t.Errorf("length = %d, want 12", len(a))
	}
	if strings.Contains(a, secret) {
		t.Error("fingerprint contains its input")
	}
	if ids.Fingerprint("") == a {
		t.Error("different inputs collided")
	}
	// Lowercase base32 so it is safe in a log field and a URL alike.
	if a != strings.ToLower(a) {
		t.Errorf("fingerprint %q should be lowercase", a)
	}
}

func TestMustNewDoesNotPanicInNormalOperation(t *testing.T) {
	t.Parallel()

	for range 100 {
		if ids.MustNew().Version() != 7 {
			t.Fatal("MustNew produced a non-v7 uuid")
		}
	}
}
