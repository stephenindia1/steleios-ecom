package onboarding

import "errors"

// Errors this module returns.
//
// Unlike the identity module's, these are safe to distinguish to the caller:
// the caller is a vendor administrator acting on the vendor's own records, not
// an anonymous visitor who could use the difference as an oracle.
var (
	// ErrNoSuchClient means no client matched.
	ErrNoSuchClient = errors.New("onboarding: no such client")

	// ErrAlreadyConfirmed means the business identity is frozen. The database
	// enforces this too (migration 00012); the service checks first so the
	// answer is a sentence rather than a constraint violation.
	ErrAlreadyConfirmed = errors.New("onboarding: the business identity is confirmed and permanent")

	// ErrNoIdentifier means a client cannot be confirmed without a GSTIN or a
	// TIN. It is what the client is permanently bound to, so confirming without
	// one would freeze an identity that identifies nothing (migration 00013).
	ErrNoIdentifier = errors.New("onboarding: a client needs a GSTIN or a TIN before it can be confirmed")

	// ErrNoOwner means a client cannot be confirmed with no owner on record.
	// The business identity is bound to the natural persons behind it.
	ErrNoOwner = errors.New("onboarding: a client needs at least one owner before it can be confirmed")

	// ErrNoShop means a client cannot be confirmed with no shop. A business with
	// nowhere to trade has not finished onboarding.
	ErrNoShop = errors.New("onboarding: a client needs at least one shop before it can be confirmed")

	// ErrDuplicateIdentifier means a GSTIN, TIN, PAN-derived CIN or Udyam number
	// already belongs to another client. It is how the same business being
	// onboarded twice is caught.
	ErrDuplicateIdentifier = errors.New("onboarding: that identifier already belongs to another client")

	// ErrDuplicateShop means the slug or shop code is taken.
	ErrDuplicateShop = errors.New("onboarding: a shop with that slug or code already exists")

	// ErrEmailInUse means the address already has a login. One identity is bound
	// to one email (BR-IDN-08), so reusing it would mean two businesses sharing
	// a login.
	ErrEmailInUse = errors.New("onboarding: that email address already has a login")

	// ErrRetiredContact means the address or number was retired from a previous
	// account and may never be used again (migration 00010).
	ErrRetiredContact = errors.New("onboarding: that email or phone was retired and cannot be reused")

	// ErrShopNotThisClient means the named shop belongs to a different client.
	// Distinct from ErrNoShop, which means the client has none: conflating them
	// answers a cross-client attempt with "provision a shop first", which is
	// both confusing and wrong.
	ErrShopNotThisClient = errors.New("onboarding: that shop belongs to a different client")

	// ErrClientNotActive means the client is suspended or closed.
	ErrClientNotActive = errors.New("onboarding: the client is not active")
)
