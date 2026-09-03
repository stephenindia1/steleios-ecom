package passwd_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/stephenindia1/steleios-ecom/internal/platform/passwd"
)

// fast is a cheap hasher for tests. Argon2id at production cost is ~100ms per
// call by design, which would make this file take minutes. The COST is what is
// reduced, never the algorithm — the encoding, the salting and the comparison
// under test are identical.
func fast(t *testing.T) *passwd.Hasher {
	t.Helper()

	h, err := passwd.New(passwd.Params{
		Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
	})
	if err != nil {
		t.Fatalf("build hasher: %v", err)
	}
	return h
}

func TestHashAndVerify(t *testing.T) {
	t.Parallel()

	h := fast(t)
	const password = "correct horse battery staple"

	encoded, err := h.Hash(password)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}

	if err := h.Verify(encoded, password); err != nil {
		t.Errorf("the correct password did not verify: %v", err)
	}
	if err := h.Verify(encoded, "wrong password entirely"); !errors.Is(err, passwd.ErrMismatch) {
		t.Errorf("a wrong password returned %v, want ErrMismatch", err)
	}
	// Off by one character, which is the case a byte-by-byte comparison would
	// leak information about.
	if err := h.Verify(encoded, "correct horse battery stapl"); !errors.Is(err, passwd.ErrMismatch) {
		t.Errorf("a near-miss returned %v, want ErrMismatch", err)
	}
}

func TestHashNeverStoresThePassword(t *testing.T) {
	t.Parallel()

	h := fast(t)
	const password = "SuperSecretShopPassword"

	encoded, err := h.Hash(password)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}

	if strings.Contains(encoded, password) {
		t.Fatalf("the encoded hash contains the plaintext: %s", encoded)
	}
	if !strings.HasPrefix(encoded, "$argon2id$") {
		t.Errorf("encoded hash = %q, want the PHC argon2id form", encoded)
	}
}

func TestEveryHashIsSalted(t *testing.T) {
	t.Parallel()

	h := fast(t)
	const password = "the same password twice"

	first, err := h.Hash(password)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	second, err := h.Hash(password)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}

	// Identical passwords must produce different hashes, or a stolen table
	// reveals which accounts share a password — and one cracked password
	// unlocks all of them at once.
	if first == second {
		t.Fatal("hashing the same password twice produced identical output; the salt is not random")
	}
	if err := h.Verify(first, password); err != nil {
		t.Errorf("first hash does not verify: %v", err)
	}
	if err := h.Verify(second, password); err != nil {
		t.Errorf("second hash does not verify: %v", err)
	}
}

func TestVerifyRejectsMalformedHashes(t *testing.T) {
	t.Parallel()

	h := fast(t)

	cases := []struct {
		name    string
		encoded string
	}{
		{name: "empty", encoded: ""},
		{name: "not a hash at all", encoded: "hunter2"},
		{name: "wrong algorithm", encoded: "$bcrypt$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA"},
		{name: "too few fields", encoded: "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA"},
		{name: "unparseable parameters", encoded: "$argon2id$v=19$m=lots,t=3,p=2$c2FsdA$aGFzaA"},
		{name: "bad base64 salt", encoded: "$argon2id$v=19$m=65536,t=3,p=2$!!!!$aGFzaA"},
		{name: "bad base64 key", encoded: "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$!!!!"},
		{name: "empty salt", encoded: "$argon2id$v=19$m=65536,t=3,p=2$$aGFzaA"},
		{name: "unsupported version", encoded: "$argon2id$v=16$m=65536,t=3,p=2$c2FsdA$aGFzaA"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := h.Verify(tc.encoded, "anything")
			if !errors.Is(err, passwd.ErrMalformed) {
				t.Errorf("Verify(%q) = %v, want ErrMalformed", tc.encoded, err)
			}
			// It must never accidentally succeed. A malformed hash that
			// verified would be an authentication bypass.
			if err == nil {
				t.Fatal("a malformed hash verified successfully")
			}
		})
	}
}

func TestNeedsRehash(t *testing.T) {
	t.Parallel()

	weak, err := passwd.New(passwd.Params{
		Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
	})
	if err != nil {
		t.Fatalf("weak hasher: %v", err)
	}
	strong, err := passwd.New(passwd.Params{
		Memory: 32 * 1024, Iterations: 3, Parallelism: 1, SaltLength: 16, KeyLength: 32,
	})
	if err != nil {
		t.Fatalf("strong hasher: %v", err)
	}

	old, err := weak.Hash("a password from years ago")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}

	// The old hash still verifies — raising the cost must not lock anyone out.
	if err := strong.Verify(old, "a password from years ago"); err != nil {
		t.Errorf("an old hash stopped verifying after the cost was raised: %v", err)
	}
	if !strong.NeedsRehash(old) {
		t.Error("a weaker hash was not flagged for rehashing")
	}

	fresh, err := strong.Hash("a password from years ago")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if strong.NeedsRehash(fresh) {
		t.Error("a current-cost hash was flagged for rehashing")
	}
	// A weaker hasher must not ask to downgrade a stronger hash.
	if weak.NeedsRehash(fresh) {
		t.Error("a stronger hash was flagged for rehashing by a weaker hasher")
	}
}

func TestNewRejectsUnsafeParameters(t *testing.T) {
	t.Parallel()

	base := passwd.DefaultParams()

	cases := []struct {
		name   string
		mutate func(*passwd.Params)
	}{
		{name: "memory far too low", mutate: func(p *passwd.Params) { p.Memory = 1024 }},
		{name: "no iterations", mutate: func(p *passwd.Params) { p.Iterations = 0 }},
		{name: "no parallelism", mutate: func(p *passwd.Params) { p.Parallelism = 0 }},
		{name: "salt too short", mutate: func(p *passwd.Params) { p.SaltLength = 8 }},
		{name: "key too short", mutate: func(p *passwd.Params) { p.KeyLength = 16 }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := base
			tc.mutate(&p)
			if _, err := passwd.New(p); err == nil {
				t.Errorf("New accepted unsafe parameters: %+v", p)
			}
		})
	}

	if _, err := passwd.New(base); err != nil {
		t.Errorf("New rejected the defaults: %v", err)
	}
}

func TestDefaultParamsAreNotWeak(t *testing.T) {
	t.Parallel()

	// Guards against somebody quietly lowering the cost to make tests or a
	// benchmark faster. The cost IS the security property.
	p := passwd.DefaultParams()

	if p.Memory < 19*1024 {
		t.Errorf("default memory %d KiB is below the OWASP minimum of 19456", p.Memory)
	}
	if p.Iterations < 2 {
		t.Errorf("default iterations = %d, want at least 2", p.Iterations)
	}
	if p.SaltLength < 16 {
		t.Errorf("default salt = %d bytes, want at least 16", p.SaltLength)
	}
	if p.KeyLength < 32 {
		t.Errorf("default key = %d bytes, want at least 32", p.KeyLength)
	}
}

func TestCheckPolicy(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		password string
		wantErr  bool
	}{
		{name: "a passphrase", password: "correct horse battery staple"},
		{name: "exactly the minimum", password: "abcdefghij"},
		{name: "long and unusual", password: "thequickbrownfoxjumpedoverit"},
		{name: "non-latin is fine", password: "मेरीदुकानकाखाता"},

		{name: "too short", password: "short", wantErr: true},
		{name: "one under the minimum", password: "abcdefghi", wantErr: true},
		{name: "empty", password: "", wantErr: true},
		{name: "only whitespace", password: "              ", wantErr: true},
		{name: "absurdly long", password: strings.Repeat("a", 300), wantErr: true},

		// The common-password cases, including the variants people reach for
		// when told to add a number.
		{name: "password", password: "password", wantErr: true},
		{name: "Password1", password: "Password1", wantErr: true},
		{name: "password123", password: "password123", wantErr: true},
		{name: "PASSWORD!", password: "PASSWORD!", wantErr: true},
		{name: "welcome123", password: "welcome123", wantErr: true},
		{name: "qwertyuiop", password: "qwertyuiop", wantErr: true},
		{name: "changeme123", password: "changeme123", wantErr: true},
		{name: "mystore123", password: "mystore123", wantErr: true},
		{name: "bangalore1", password: "bangalore1", wantErr: true},
		{name: "all digits", password: "9876543210", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := passwd.CheckPolicy(tc.password)
			if tc.wantErr {
				if !errors.Is(err, passwd.ErrPolicy) {
					t.Errorf("CheckPolicy(%q) = %v, want ErrPolicy", tc.password, err)
				}
				return
			}
			if err != nil {
				t.Errorf("CheckPolicy(%q) = %v, want nil", tc.password, err)
			}
		})
	}
}

func TestPolicyErrorsDoNotEchoThePassword(t *testing.T) {
	t.Parallel()

	// A policy error is shown to a user and may be logged. Echoing the rejected
	// password back would put it in both (BR-SEC-07).
	const attempted = "password123"

	err := passwd.CheckPolicy(attempted)
	if err == nil {
		t.Fatal("expected a policy error")
	}
	if strings.Contains(err.Error(), attempted) {
		t.Errorf("the policy error contains the attempted password: %v", err)
	}
}

func TestVerifyIsIndependentOfHasherCost(t *testing.T) {
	t.Parallel()

	// A hash carries its own parameters, so any hasher can verify any hash.
	// This is what allows the cost to be raised without a migration.
	weak := fast(t)
	strong, err := passwd.New(passwd.Params{
		Memory: 32 * 1024, Iterations: 2, Parallelism: 1, SaltLength: 16, KeyLength: 32,
	})
	if err != nil {
		t.Fatalf("strong hasher: %v", err)
	}

	const password = "a shared password across hashers"

	fromWeak, err := weak.Hash(password)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	fromStrong, err := strong.Hash(password)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}

	if err := strong.Verify(fromWeak, password); err != nil {
		t.Errorf("strong hasher could not verify a weak hash: %v", err)
	}
	if err := weak.Verify(fromStrong, password); err != nil {
		t.Errorf("weak hasher could not verify a strong hash: %v", err)
	}
}
