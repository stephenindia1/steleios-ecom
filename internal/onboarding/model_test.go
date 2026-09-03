package onboarding_test

import (
	"testing"

	"github.com/stephenindia1/steleios-ecom/internal/onboarding"
)

// The validation here is what stands between a typo and an error printed on
// every invoice a shop ever issues. A wrong GSTIN is not caught by the GST
// portal at entry — Steleios does not call it — so the cross-checks below are
// the only chance to notice before the client's accountant does.

func TestRegisterInputValidation(t *testing.T) {
	t.Parallel()

	// A complete, internally consistent registration. Each case below breaks
	// exactly one thing, so a failure names the rule that broke.
	base := func() onboarding.RegisterInput {
		in := onboarding.RegisterInput{
			LegalName:    "Kadambari Stores Pvt Ltd",
			ContactEmail: "owner@kadambari.example",
			ContactPhone: "+919876543210",
			GSTIN:        "29ABCDE1234F1Z5",
			PAN:          "ABCDE1234F",
			StateCode:    "29",
			BusinessType: "private_limited",
		}
		in.Normalise()
		return in
	}

	if f := base().Validate(); len(f) != 0 {
		t.Fatalf("the base case should be valid, got %v", f)
	}

	cases := []struct {
		name   string
		break_ func(*onboarding.RegisterInput)
		field  string
	}{
		{"no legal name", func(i *onboarding.RegisterInput) { i.LegalName = "" }, "legal_name"},
		{"no contact email", func(i *onboarding.RegisterInput) { i.ContactEmail = "" }, "contact_email"},
		{"email without an @", func(i *onboarding.RegisterInput) { i.ContactEmail = "not-an-address" }, "contact_email"},

		// The mobile is not optional, and the reason is worth a test of its
		// own: recovery is verified by SMS to it, so a business without one
		// cannot be recovered when its email is lost (BR-REC-14b).
		{"no mobile", func(i *onboarding.RegisterInput) { i.ContactPhone = "" }, "contact_phone"},
		{"mobile without a country code", func(i *onboarding.RegisterInput) { i.ContactPhone = "9876543210" }, "contact_phone"},

		{"malformed GSTIN", func(i *onboarding.RegisterInput) { i.GSTIN = "29ABCDE1234F1Z" }, "gstin"},
		{"malformed PAN", func(i *onboarding.RegisterInput) { i.PAN = "ABCDE12345" }, "pan"},
		{"malformed TIN", func(i *onboarding.RegisterInput) { i.TIN = "1234" }, "tin"},
		{"unknown business type", func(i *onboarding.RegisterInput) { i.BusinessType = "cooperative" }, "business_type"},

		// The cross-checks. Each of these is two fields contradicting each
		// other, which means one of them is a typo — and recording both as fact
		// puts the error into every invoice the shop issues.
		{"PAN contradicts the one inside the GSTIN",
			func(i *onboarding.RegisterInput) { i.PAN = "ZZZZZ9999Z" }, "pan"},
		{"state contradicts the one inside the GSTIN",
			func(i *onboarding.RegisterInput) { i.StateCode = "27" }, "state_code"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			in := base()
			tc.break_(&in)
			in.Normalise()

			f := in.Validate()
			if _, ok := f[tc.field]; !ok {
				t.Fatalf("expected a message on %q, got %v", tc.field, f)
			}
		})
	}
}

// TestGSTINIsAcceptedInLowerCase: a government identifier typed in lower case is
// a formatting slip, not an error. Normalising before validating is what makes
// the difference, and getting that order wrong would reject correct data.
func TestGSTINIsAcceptedInLowerCase(t *testing.T) {
	t.Parallel()

	in := onboarding.RegisterInput{
		LegalName: "X", ContactEmail: "a@b.example", ContactPhone: "+919876543210",
		GSTIN: "29abcde1234f1z5", PAN: "abcde1234f", StateCode: "29",
	}
	in.Normalise()

	if f := in.Validate(); len(f) != 0 {
		t.Fatalf("a lower-case GSTIN was rejected: %v", f)
	}
	if in.GSTIN != "29ABCDE1234F1Z5" {
		t.Errorf("GSTIN = %q, want it upper-cased", in.GSTIN)
	}
}

// TestOnlyTheLastFourAadhaarDigitsAreAccepted.
//
// Steleios holds four digits, cross-referenced against the GSTIN, and never the
// full number. A caller sending a whole Aadhaar is REFUSED rather than silently
// truncated: truncating would mean the full number was transmitted to us and
// appeared in a request body, which is the exposure the rule exists to avoid
// (2018 Supreme Court judgment on private-sector Aadhaar collection).
func TestOnlyTheLastFourAadhaarDigitsAreAccepted(t *testing.T) {
	t.Parallel()

	base := func() onboarding.OwnerInput {
		return onboarding.OwnerInput{
			FullName: "Ravi Kadambari", AddressL1: "12 MG Road",
			City: "Bengaluru", StateCode: "29", Pincode: "560001",
		}
	}

	cases := []struct {
		name    string
		aadhaar string
		wantErr bool
	}{
		{"four digits", "7391", false},
		{"absent", "", false},
		{"a full Aadhaar number", "123456789012", true},
		{"three digits", "739", true},
		{"five digits", "73915", true},
		{"letters", "abcd", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			in := base()
			in.AadhaarLast4 = tc.aadhaar
			in.Normalise()

			_, got := in.Validate()["aadhaar_last4"]
			if got != tc.wantErr {
				t.Fatalf("rejected = %v, want %v", got, tc.wantErr)
			}
		})
	}
}

func TestOwnerInputValidation(t *testing.T) {
	t.Parallel()

	base := func() onboarding.OwnerInput {
		return onboarding.OwnerInput{
			FullName: "Ravi Kadambari", AddressL1: "12 MG Road",
			City: "Bengaluru", StateCode: "29", Pincode: "560001",
			PAN: "ABCDE1234F", Phone: "+919876543210",
		}
	}

	in := base()
	in.Normalise()
	if f := in.Validate(); len(f) != 0 {
		t.Fatalf("the base case should be valid, got %v", f)
	}

	cases := []struct {
		name   string
		break_ func(*onboarding.OwnerInput)
		field  string
	}{
		{"no name", func(i *onboarding.OwnerInput) { i.FullName = "" }, "full_name"},
		{"no address", func(i *onboarding.OwnerInput) { i.AddressL1 = "" }, "address_line1"},
		{"no city", func(i *onboarding.OwnerInput) { i.City = "" }, "city"},
		{"no state", func(i *onboarding.OwnerInput) { i.StateCode = "" }, "state_code"},
		{"one-digit state", func(i *onboarding.OwnerInput) { i.StateCode = "9" }, "state_code"},
		{"no pincode", func(i *onboarding.OwnerInput) { i.Pincode = "" }, "pincode"},
		{"pincode starting with zero", func(i *onboarding.OwnerInput) { i.Pincode = "060001" }, "pincode"},
		{"five-digit pincode", func(i *onboarding.OwnerInput) { i.Pincode = "56000" }, "pincode"},
		{"malformed PAN", func(i *onboarding.OwnerInput) { i.PAN = "ABCD1234F" }, "pan"},
		{"mobile without a country code", func(i *onboarding.OwnerInput) { i.Phone = "9876543210" }, "phone"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			in := base()
			tc.break_(&in)
			in.Normalise()

			if _, ok := in.Validate()[tc.field]; !ok {
				t.Fatalf("expected a message on %q, got %v", tc.field, in.Validate())
			}
		})
	}
}

func TestShopInputValidation(t *testing.T) {
	t.Parallel()

	base := func() onboarding.ShopInput {
		return onboarding.ShopInput{
			Slug: "kadambari-mg-road", ShopCode: "MGRD",
			LegalName: "Kadambari Stores", StateCode: "29",
		}
	}

	in := base()
	in.Normalise()
	if f := in.Validate(); len(f) != 0 {
		t.Fatalf("the base case should be valid, got %v", f)
	}

	cases := []struct {
		name   string
		break_ func(*onboarding.ShopInput)
		field  string
	}{
		{"no slug", func(i *onboarding.ShopInput) { i.Slug = "" }, "slug"},
		{"slug too short", func(i *onboarding.ShopInput) { i.Slug = "ab" }, "slug"},
		{"slug with a space", func(i *onboarding.ShopInput) { i.Slug = "mg road" }, "slug"},
		{"slug ending in a hyphen", func(i *onboarding.ShopInput) { i.Slug = "mg-road-" }, "slug"},
		{"no shop code", func(i *onboarding.ShopInput) { i.ShopCode = "" }, "shop_code"},
		{"no name", func(i *onboarding.ShopInput) { i.LegalName = "" }, "legal_name"},

		// A shop's GSTIN is its own: GST registration is per state, so a
		// business trading in two states holds two, and the place of supply on
		// every invoice depends on which one issued it.
		{"shop GSTIN contradicts the shop's state",
			func(i *onboarding.ShopInput) { i.GSTIN = "27ABCDE1234F1Z5" }, "state_code"},
		{"malformed shop GSTIN",
			func(i *onboarding.ShopInput) { i.GSTIN = "29ABC" }, "gstin"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			in := base()
			tc.break_(&in)
			in.Normalise()

			if _, ok := in.Validate()[tc.field]; !ok {
				t.Fatalf("expected a message on %q, got %v", tc.field, in.Validate())
			}
		})
	}
}
