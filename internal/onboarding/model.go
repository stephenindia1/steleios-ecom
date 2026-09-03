// Package onboarding is how a business becomes a client of Steleios.
//
// It is the vendor's side of the relationship and nothing else: which businesses
// exist, their shops, their government identifiers, their owners, and the first
// login handed to each. It holds no business data and cannot reach any — the
// tables it reads are the ones naming WHICH businesses exist, and row-level
// security refuses it the rest (BR-ADM-14, migration 00020).
//
// The order of operations is deliberate and enforced by the service:
//
//	register    a client exists, with its legal name and contact
//	owners      the natural persons behind it, with their KYC
//	shops       one tenant per shop; a two-shop owner gets two tenants
//	first user  the owner's login, with a generated password
//	confirm     the government identifiers become PERMANENT
//
// Confirm is the point of no return. After it the business identity cannot be
// edited by anyone, including the vendor: a different GSTIN is a different
// business and therefore a different client (migration 00012).
package onboarding

import (
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/stephenindia1/steleios-ecom/internal/platform/tenant"
)

// Client status values, matching the database's check constraint.
const (
	StatusActive    = "active"
	StatusSuspended = "suspended"
	StatusClosed    = "closed"
)

// The business types the schema accepts.
var businessTypes = map[string]bool{
	"proprietorship": true, "partnership": true, "llp": true,
	"private_limited": true, "public_limited": true, "huf": true,
	"trust": true, "society": true,
}

// Format rules, mirroring the database's check constraints.
//
// Duplicated here deliberately, and this is NOT the duplication rule 23
// forbids: the database constraint is the guarantee, and this is a validation
// that produces a message a person can act on. Without it a mistyped GSTIN
// surfaces as a 500 from a constraint violation instead of "that is not a valid
// GSTIN". The constraint is what makes it true; this is what makes it kind.
var (
	gstinFormat = regexp.MustCompile(`^[0-9]{2}[A-Z]{5}[0-9]{4}[A-Z]{1}[0-9A-Z]{1}[Z]{1}[0-9A-Z]{1}$`)
	panFormat   = regexp.MustCompile(`^[A-Z]{5}[0-9]{4}[A-Z]{1}$`)
	tinFormat   = regexp.MustCompile(`^[0-9]{11}$`)
	pincode     = regexp.MustCompile(`^[1-9][0-9]{5}$`)
	stateCode   = regexp.MustCompile(`^[0-9]{2}$`)
	// E.164 with an India-shaped default. Stored with the country code so a
	// number is unambiguous when an SMS is sent to it (BR-REC-11).
	phoneFormat = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)
	aadhaarLast = regexp.MustCompile(`^[0-9]{4}$`)
)

// Client is a business that uses Steleios.
type Client struct {
	ID         uuid.UUID
	ClientCode string
	LegalName  string

	ContactEmail string
	ContactPhone string
	Status       string

	// The government identifiers. At least one of GSTIN or TIN is required
	// before a client can be confirmed, because it is what the client is
	// permanently bound to (migration 00013).
	GSTIN             string
	TIN               string
	PAN               string
	CIN               string
	UdyamNumber       string
	ShopLicenceNumber string
	GSTRegistered     bool

	BusinessType      string
	RegisteredAddress string
	StateCode         string

	OnboardedAt *time.Time
	ConfirmedAt *time.Time
	CreatedAt   time.Time
}

// IsConfirmed reports whether the business identity is frozen.
func (c Client) IsConfirmed() bool { return c.ConfirmedAt != nil }

// Owner is a natural person behind the business.
//
// The KYC the law expects a platform to hold. Note what is NOT here: the full
// Aadhaar number. Only its last four digits are stored, cross-referenced against
// the GSTIN, because holding the full number creates an obligation and a breach
// exposure with no matching benefit — Steleios is not an authentication agency.
type Owner struct {
	ID        uuid.UUID
	ClientID  uuid.UUID
	FullName  string
	PAN       string
	AddressL1 string
	AddressL2 string
	City      string
	StateCode string
	Pincode   string
	// AadhaarLast4 is exactly four digits or empty. Never the full number.
	AadhaarLast4 string
	Email        string
	Phone        string
	IsPrimary    bool
	CreatedAt    time.Time
}

// Shop is one trading location, and one tenant.
//
// A business with two shops has two tenants, so its staff cannot reach across
// them and a shop worker sees exactly one shop's data.
type Shop struct {
	TenantID  tenant.ID
	ClientID  uuid.UUID
	Slug      string
	ShopCode  string
	LegalName string
	StateCode string
	GSTIN     string
	Status    string
	GroupID   *uuid.UUID
	CreatedAt time.Time
}

// FirstUser is the login handed to a new owner.
//
// The password is returned exactly once, at creation, and is never stored in
// recoverable form or written to a log (BR-REC-12). If it is lost before use,
// the answer is to issue another, not to look this one up.
type FirstUser struct {
	IdentityID uuid.UUID
	Email      string
	// Password is plaintext and lives only in this value, in memory, for the
	// length of one response.
	Password  string
	ExpiresAt time.Time
}

// RegisterInput is what the vendor supplies to create a client.
type RegisterInput struct {
	LegalName    string
	ContactEmail string
	ContactPhone string

	GSTIN             string
	TIN               string
	PAN               string
	CIN               string
	UdyamNumber       string
	ShopLicenceNumber string

	BusinessType      string
	RegisteredAddress string
	StateCode         string
}

// Normalise trims and upper-cases the identifiers.
//
// Government identifiers are upper-case by definition, and someone typing a
// GSTIN in lower case has made a formatting slip rather than an error. Email is
// lower-cased; the column is citext, so this is cosmetic rather than load
// bearing.
func (in *RegisterInput) Normalise() {
	in.LegalName = strings.TrimSpace(in.LegalName)
	in.ContactEmail = strings.ToLower(strings.TrimSpace(in.ContactEmail))
	in.ContactPhone = strings.TrimSpace(in.ContactPhone)

	in.GSTIN = strings.ToUpper(strings.TrimSpace(in.GSTIN))
	in.TIN = strings.TrimSpace(in.TIN)
	in.PAN = strings.ToUpper(strings.TrimSpace(in.PAN))
	in.CIN = strings.ToUpper(strings.TrimSpace(in.CIN))
	in.UdyamNumber = strings.ToUpper(strings.TrimSpace(in.UdyamNumber))
	in.ShopLicenceNumber = strings.TrimSpace(in.ShopLicenceNumber)

	in.BusinessType = strings.ToLower(strings.TrimSpace(in.BusinessType))
	in.RegisteredAddress = strings.TrimSpace(in.RegisteredAddress)
	in.StateCode = strings.TrimSpace(in.StateCode)
}

// Validate reports field-level problems, keyed by the client's own field names.
//
// It checks shape and internal consistency. It does not check whether a GSTIN
// is REAL — Steleios does not call the GST portal, and the client is responsible
// for the accuracy of what they supply (BR-ACP-02, docs/09 §6). What is checked
// is that the value could be a GSTIN at all, and that the pieces agree with each
// other, because a GSTIN whose embedded PAN contradicts the PAN field is a typo
// on one of the two and both would otherwise be recorded as fact.
func (in RegisterInput) Validate() map[string]string {
	f := map[string]string{}

	if in.LegalName == "" {
		f["legal_name"] = "Enter the registered name of the business."
	}
	if in.ContactEmail == "" {
		f["contact_email"] = "Enter a contact email address."
	} else if !strings.Contains(in.ContactEmail, "@") {
		f["contact_email"] = "That does not look like an email address."
	}
	// The phone is not optional, and this is the reason: recovery and every
	// contact change is verified by SMS to it, because the email is precisely
	// what those flows assume may be lost (BR-REC-11, BR-REC-14b).
	if in.ContactPhone == "" {
		f["contact_phone"] = "Enter a mobile number. Account recovery is verified by SMS, so a business without one cannot be recovered."
	} else if !phoneFormat.MatchString(in.ContactPhone) {
		f["contact_phone"] = "Enter the number with its country code, for example +919876543210."
	}

	if in.GSTIN != "" && !gstinFormat.MatchString(in.GSTIN) {
		f["gstin"] = "A GSTIN is 15 characters: two state digits, a PAN, and three more."
	}
	if in.TIN != "" && !tinFormat.MatchString(in.TIN) {
		f["tin"] = "A TIN is 11 digits."
	}
	if in.PAN != "" && !panFormat.MatchString(in.PAN) {
		f["pan"] = "A PAN is five letters, four digits and a letter, for example ABCDE1234F."
	}
	if in.StateCode != "" && !stateCode.MatchString(in.StateCode) {
		f["state_code"] = "A state code is two digits."
	}
	if in.BusinessType != "" && !businessTypes[in.BusinessType] {
		f["business_type"] = "Choose one of the recognised business types."
	}

	// The cross-checks. Each mirrors a database constraint, and each exists
	// because the two fields contradicting one another means one of them is
	// wrong — and neither the client nor their accountant would find out until
	// a return was filed.
	if in.GSTIN != "" && in.PAN != "" && len(in.GSTIN) >= 12 && in.GSTIN[2:12] != in.PAN {
		f["pan"] = "The PAN inside the GSTIN does not match this PAN. One of the two has a typo."
	}
	if in.GSTIN != "" && in.StateCode != "" && in.GSTIN[:2] != in.StateCode {
		f["state_code"] = "The state code inside the GSTIN does not match this state code."
	}

	return f
}

// OwnerInput is the KYC for one natural person behind the business.
type OwnerInput struct {
	FullName     string
	PAN          string
	AddressL1    string
	AddressL2    string
	City         string
	StateCode    string
	Pincode      string
	AadhaarLast4 string
	Email        string
	Phone        string
	IsPrimary    bool
}

// Normalise trims and cases the fields.
func (in *OwnerInput) Normalise() {
	in.FullName = strings.TrimSpace(in.FullName)
	in.PAN = strings.ToUpper(strings.TrimSpace(in.PAN))
	in.AddressL1 = strings.TrimSpace(in.AddressL1)
	in.AddressL2 = strings.TrimSpace(in.AddressL2)
	in.City = strings.TrimSpace(in.City)
	in.StateCode = strings.TrimSpace(in.StateCode)
	in.Pincode = strings.TrimSpace(in.Pincode)
	in.AadhaarLast4 = strings.TrimSpace(in.AadhaarLast4)
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	in.Phone = strings.TrimSpace(in.Phone)
}

// Validate reports field-level problems.
func (in OwnerInput) Validate() map[string]string {
	f := map[string]string{}

	if in.FullName == "" {
		f["full_name"] = "Enter the owner's full name as it appears on their PAN."
	}
	if in.AddressL1 == "" {
		f["address_line1"] = "Enter the first line of the address."
	}
	if in.City == "" {
		f["city"] = "Enter the city."
	}
	if in.StateCode == "" || !stateCode.MatchString(in.StateCode) {
		f["state_code"] = "A state code is two digits."
	}
	if in.Pincode == "" || !pincode.MatchString(in.Pincode) {
		f["pincode"] = "A PIN code is six digits and does not start with zero."
	}
	if in.PAN != "" && !panFormat.MatchString(in.PAN) {
		f["pan"] = "A PAN is five letters, four digits and a letter, for example ABCDE1234F."
	}
	if in.Phone != "" && !phoneFormat.MatchString(in.Phone) {
		f["phone"] = "Enter the number with its country code, for example +919876543210."
	}
	if in.Email != "" && !strings.Contains(in.Email, "@") {
		f["email"] = "That does not look like an email address."
	}

	// The full Aadhaar number is deliberately not accepted. Anything longer
	// than four digits here means the caller sent one, and the right answer is
	// to refuse it rather than to truncate it and record that we received it
	// (2018 Supreme Court judgment on private-sector Aadhaar collection).
	if in.AadhaarLast4 != "" && !aadhaarLast.MatchString(in.AadhaarLast4) {
		f["aadhaar_last4"] = "Enter only the last four digits of the Aadhaar. Steleios does not accept the full number."
	}

	return f
}

// ShopInput provisions one trading location.
type ShopInput struct {
	Slug      string
	ShopCode  string
	LegalName string
	StateCode string
	GSTIN     string
	GroupID   *uuid.UUID
}

// Normalise trims and cases the fields.
func (in *ShopInput) Normalise() {
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	in.ShopCode = strings.ToUpper(strings.TrimSpace(in.ShopCode))
	in.LegalName = strings.TrimSpace(in.LegalName)
	in.StateCode = strings.TrimSpace(in.StateCode)
	in.GSTIN = strings.ToUpper(strings.TrimSpace(in.GSTIN))
}

var slugFormat = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{1,48}[a-z0-9])$`)

// Validate reports field-level problems.
func (in ShopInput) Validate() map[string]string {
	f := map[string]string{}

	if !slugFormat.MatchString(in.Slug) {
		f["slug"] = "A slug is 3 to 50 characters: lower-case letters, digits and hyphens, starting and ending with a letter or digit."
	}
	if in.ShopCode == "" {
		f["shop_code"] = "Enter a short code for the shop, for example MAIN."
	}
	if in.LegalName == "" {
		f["legal_name"] = "Enter the name of this shop."
	}
	if in.StateCode != "" && !stateCode.MatchString(in.StateCode) {
		f["state_code"] = "A state code is two digits."
	}
	// A shop's own GSTIN, where it has one: GST registration is per state, so a
	// business trading in two states holds two, and the place of supply on every
	// invoice depends on which one issued it.
	if in.GSTIN != "" {
		if !gstinFormat.MatchString(in.GSTIN) {
			f["gstin"] = "A GSTIN is 15 characters: two state digits, a PAN, and three more."
		} else if in.StateCode != "" && in.GSTIN[:2] != in.StateCode {
			f["state_code"] = "The state code inside this shop's GSTIN does not match its state code."
		}
	}

	return f
}
