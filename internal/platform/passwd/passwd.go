// Package passwd is the sole implementation of password hashing and
// verification (docs/03 §6.1, BR-IDN-01).
//
// Argon2id, with the parameters and the encoded form stored together so that a
// future parameter change does not invalidate existing hashes. Plaintext and
// reversibly-encrypted passwords are prohibited, and a password never appears
// in a log, an error message or an audit entry (BR-SEC-07).
package passwd

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Errors returned by this package.
var (
	// ErrMismatch means the password does not match the hash. It is
	// deliberately indistinguishable from any other verification failure at the
	// call site, so a caller cannot leak which part was wrong (BR-IDN-06).
	ErrMismatch = errors.New("passwd: password does not match")
	// ErrMalformed means the stored hash could not be parsed.
	ErrMalformed = errors.New("passwd: malformed encoded hash")
	// ErrPolicy means the proposed password does not meet policy.
	ErrPolicy = errors.New("passwd: password does not meet policy")
)

// Params are the Argon2id cost parameters.
//
// These are the values NEW hashes are made with. Old hashes carry their own
// parameters in their encoded form, so raising the cost later re-hashes people
// gradually on their next sign-in rather than locking everyone out (NeedsRehash).
type Params struct {
	Memory      uint32 // KiB
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// DefaultParams follows the OWASP guidance for Argon2id.
//
// 64 MiB with 3 iterations is comfortably above the minimum and costs roughly
// 100ms on server hardware. That cost is the point — it is what makes an
// offline attack on a stolen hash expensive.
//
// It is also a denial-of-service consideration: each concurrent verification
// holds 64 MiB. Login is rate limited per IP and per account for exactly this
// reason (BR-IDN-11), and parallelism is capped at the machine's cores.
func DefaultParams() Params {
	p := uint8(2) //nolint:mnd // documented below
	if runtime.NumCPU() < 2 {
		p = 1
	}
	return Params{
		Memory:      64 * 1024,
		Iterations:  3,
		Parallelism: p,
		SaltLength:  16,
		KeyLength:   32,
	}
}

// Hasher hashes and verifies passwords.
type Hasher struct {
	params Params
}

// New returns a hasher with the given parameters.
func New(p Params) (*Hasher, error) {
	switch {
	case p.Memory < 8*1024:
		return nil, fmt.Errorf("passwd: memory %d KiB is too low to be useful", p.Memory)
	case p.Iterations < 1:
		return nil, errors.New("passwd: iterations must be at least 1")
	case p.Parallelism < 1:
		return nil, errors.New("passwd: parallelism must be at least 1")
	case p.SaltLength < 16:
		return nil, errors.New("passwd: salt must be at least 16 bytes")
	case p.KeyLength < 32:
		return nil, errors.New("passwd: key must be at least 32 bytes")
	}
	return &Hasher{params: p}, nil
}

// NewDefault returns a hasher with the default parameters.
func NewDefault() *Hasher {
	h, err := New(DefaultParams())
	if err != nil {
		panic(err) // GO-027: the defaults are a compile-time fact
	}
	return h
}

// Hash returns the encoded hash of a password.
//
// The encoding is the standard PHC string form:
//
//	$argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash>
//
// Self-describing on purpose: the parameters travel with the hash, so a hash
// made years ago still verifies after the defaults change.
func (h *Hasher) Hash(password string) (string, error) {
	salt := make([]byte, h.params.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("passwd: read salt: %w", err)
	}

	key := argon2.IDKey([]byte(password), salt,
		h.params.Iterations, h.params.Memory, h.params.Parallelism, h.params.KeyLength)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, h.params.Memory, h.params.Iterations, h.params.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// Verify checks a password against an encoded hash.
//
// It returns ErrMismatch for a wrong password and ErrMalformed for a hash it
// cannot parse. Callers MUST treat both identically in what they tell the
// client: revealing that a stored hash is corrupt tells an attacker the account
// exists (BR-IDN-06).
func (h *Hasher) Verify(encoded, password string) error {
	params, salt, want, err := decode(encoded)
	if err != nil {
		return err
	}

	got := argon2.IDKey([]byte(password), salt,
		params.Iterations, params.Memory, params.Parallelism, uint32(len(want))) //nolint:gosec // length is bounded by decode

	// GO-077: constant time. A byte-by-byte comparison leaks how much of the
	// hash matched, which over many attempts is enough to reconstruct it.
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrMismatch
	}
	return nil
}

// NeedsRehash reports whether a stored hash was made with weaker parameters
// than the hasher now uses.
//
// Called after a successful sign-in: the plaintext is in hand exactly once, so
// that is the only moment a stronger hash can be made without asking the person
// to do anything. Raising the cost therefore upgrades the population gradually
// rather than all at once.
func (h *Hasher) NeedsRehash(encoded string) bool {
	params, _, key, err := decode(encoded)
	if err != nil {
		// An unparseable hash cannot verify anyone, so rehashing it is the
		// right answer if we ever get here with a valid password.
		return true
	}
	return params.Memory < h.params.Memory ||
		params.Iterations < h.params.Iterations ||
		uint32(len(key)) < h.params.KeyLength //nolint:gosec // length is bounded
}

// decode parses a PHC-encoded Argon2id hash.
func decode(encoded string) (Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return Params{}, nil, nil, ErrMalformed
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return Params{}, nil, nil, ErrMalformed
	}
	if version != argon2.Version {
		return Params{}, nil, nil, fmt.Errorf("%w: unsupported version %d", ErrMalformed, version)
	}

	var p Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Iterations, &p.Parallelism); err != nil {
		return Params{}, nil, nil, ErrMalformed
	}

	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil {
		return Params{}, nil, nil, ErrMalformed
	}
	key, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil {
		return Params{}, nil, nil, ErrMalformed
	}
	if len(salt) == 0 || len(key) == 0 {
		return Params{}, nil, nil, ErrMalformed
	}

	p.SaltLength = uint32(len(salt)) //nolint:gosec // bounded by the decoded value
	p.KeyLength = uint32(len(key))   //nolint:gosec // bounded by the decoded value
	return p, salt, key, nil
}

// MinLength is the shortest permitted password (BR-IDN-01).
//
// Length is the only composition rule. Requiring a symbol and a digit produces
// Password1! across an entire staff list, which is worse than a longer
// passphrase the person can actually remember.
const MinLength = 10

// MaxLength bounds the input so that a very long string cannot be used to make
// hashing arbitrarily expensive.
const MaxLength = 256

// CheckPolicy reports whether a proposed password is acceptable.
//
// Each reason is phrased to complete the sentence "Your password …", because
// these are the one class of error message that is meant to reach the person
// typing. PolicyReason is what renders them; nothing else may show an error
// string to a client (BR-SEC-09).
func CheckPolicy(password string) error {
	n := utf8.RuneCountInString(password)
	switch {
	case n < MinLength:
		return fmt.Errorf("%w: needs at least %d characters", ErrPolicy, MinLength)
	case n > MaxLength:
		return fmt.Errorf("%w: must be at most %d characters", ErrPolicy, MaxLength)
	case strings.TrimSpace(password) == "":
		return fmt.Errorf("%w: cannot be only spaces", ErrPolicy)
	}

	if isCommon(password) {
		// Deliberately does not say which list it is on. That would tell an
		// attacker their guess was close.
		return fmt.Errorf("%w: is too common to be safe", ErrPolicy)
	}
	return nil
}

// PolicyReason renders a policy failure as a sentence for the person who typed
// the password.
//
// It returns "" for any other error. That is the point of the function: a
// handler cannot use it to put an arbitrary internal message on the wire, only
// one of the authored reasons above (GO-028).
func PolicyReason(err error) string {
	if !errors.Is(err, ErrPolicy) {
		return ""
	}
	_, reason, ok := strings.Cut(err.Error(), ErrPolicy.Error()+": ")
	if !ok || reason == "" {
		return "Choose a different password."
	}
	return "Your password " + reason + "."
}

// wordlist is the source for generated passphrases.
//
// Short, common, unambiguous English words: no homophones a person could
// mis-hear over a phone (no "right"/"write"), nothing that could be mistaken for
// another word in the list, and nothing that spells something unfortunate when
// four are put together. 256 entries, so each word contributes exactly 8 bits
// and the modulo below introduces no bias.
var wordlist = [256]string{
	"amber", "anchor", "apple", "arrow", "autumn", "bacon", "badge", "bagel",
	"balloon", "bamboo", "banjo", "barley", "basket", "beacon", "beetle", "bells",
	"birch", "biscuit", "bison", "blanket", "blossom", "bobbin", "bonfire", "bottle",
	"boulder", "bracket", "branch", "brandy", "bridge", "bronze", "bubble", "bucket",
	"buffalo", "bugle", "bundle", "burrow", "butter", "cabin", "cactus", "camel",
	"candle", "canvas", "canyon", "carbon", "cargo", "carpet", "carrot", "castle",
	"cedar", "cello", "cement", "chalk", "cherry", "chimney", "cinder", "circus",
	"clover", "cobalt", "cocoa", "coffee", "collar", "comet", "compass", "copper",
	"coral", "cotton", "cricket", "crimson", "crystal", "cymbal", "daisy", "damson",
	"dawn", "delta", "denim", "diamond", "dolphin", "domino", "donkey", "dragon",
	"drum", "dune", "eagle", "ember", "emerald", "engine", "fabric", "falcon",
	"fennel", "fern", "fiddle", "flint", "flute", "forest", "fossil", "fountain",
	"foxglove", "frost", "galaxy", "garden", "garlic", "gazelle", "ginger", "glacier",
	"glider", "granite", "gravel", "grotto", "guitar", "hammer", "harbour", "harvest",
	"hazel", "heather", "hedge", "helmet", "hickory", "hollow", "honey", "hornet",
	"iceberg", "indigo", "ingot", "island", "ivory", "jacket", "jaguar", "jasmine",
	"jigsaw", "juniper", "kettle", "keystone", "kitten", "koala", "lagoon", "lantern",
	"lattice", "lavender", "ledger", "lemon", "lentil", "leopard", "lilac", "linen",
	"lobster", "locket", "lotus", "lupin", "magnet", "magnolia", "mahogany", "mallet",
	"mango", "maple", "marble", "marigold", "meadow", "melon", "mercury", "meteor",
	"mimosa", "mineral", "mint", "mirror", "mitten", "monsoon", "mosaic", "mulberry",
	"mustard", "nectar", "needle", "nettle", "nickel", "nutmeg", "oatmeal", "obsidian",
	"ocean", "octopus", "olive", "onyx", "opal", "orbit", "orchid", "otter",
	"oyster", "paddle", "palm", "papaya", "paprika", "parchment", "parsley", "pastel",
	"peacock", "pebble", "pelican", "pepper", "petal", "pewter", "pigment", "pillow",
	"pistachio", "planet", "platinum", "plum", "pollen", "pomelo", "poppy", "portal",
	"pottery", "prairie", "pumpkin", "quartz", "quiver", "radish", "rainbow", "ranger",
	"raven", "ribbon", "rocket", "rosemary", "rubble", "ruby", "saffron", "sandal",
	"sapphire", "satchel", "scarlet", "seagull", "sequoia", "shadow", "shovel", "silver",
	"sonnet", "spinach", "spruce", "squirrel", "stallion", "sterling", "stucco", "sugar",
	"summit", "sunset", "syrup", "tangerine", "tapestry", "teapot", "temple", "thicket",
	"thimble", "thistle", "thunder", "timber", "topaz", "tortoise", "trellis", "tulip",
}

// Generate returns a random passphrase of n words joined by hyphens.
//
// It exists for one purpose: the password the vendor issues when an owner has
// lost everything, or when a business is first onboarded (BR-REC-10). It is read
// aloud over a phone and typed from an SMS, which is why it is words rather than
// symbols — "marigold-thistle-copper-lantern" survives that journey and
// "xK9$mQ2!" does not.
//
// Entropy: the list holds exactly 256 words, so each contributes 8 bits. Four
// words is 32 bits, which would be weak for a lasting password and is ample for
// one that expires within the hour and is rate-limited on every attempt. Callers
// wanting more ask for more words.
//
// The list length being a power of two is what makes the modulo unbiased. If a
// word is ever added or removed, this becomes biased — hence the fixed-size
// array, which makes the count a compile-time fact rather than a convention.
func Generate(words int) (string, error) {
	const minWords = 3
	if words < minWords {
		return "", fmt.Errorf("passwd: a generated password needs at least %d words, got %d", minWords, words)
	}

	b := make([]byte, words)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("passwd: read entropy: %w", err)
	}

	var sb strings.Builder
	sb.Grow(words * 10)
	for i, v := range b {
		if i > 0 {
			sb.WriteByte('-')
		}
		sb.WriteString(wordlist[v])
	}
	return sb.String(), nil
}
