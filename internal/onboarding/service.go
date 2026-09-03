package onboarding

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/stephenindia1/steleios-ecom/internal/platform/audit"
	"github.com/stephenindia1/steleios-ecom/internal/platform/authz"
	"github.com/stephenindia1/steleios-ecom/internal/platform/clock"
	"github.com/stephenindia1/steleios-ecom/internal/platform/ids"
	"github.com/stephenindia1/steleios-ecom/internal/platform/passwd"
	"github.com/stephenindia1/steleios-ecom/internal/platform/sms"
)

// generatedPasswordTTL bounds how long an issued password stays usable.
//
// Short by design (BR-REC-10). A generated password has been seen by a vendor
// administrator and travelled over SMS, so it is the weakest credential in the
// system and its value is that it expires. An owner who does not use it within
// the window asks for another, which costs a phone call and is the correct
// trade.
const generatedPasswordTTL = time.Hour

// generatedPasswordWords is the length of the issued passphrase in words.
//
// Words rather than characters because it is read aloud over a phone or typed
// from an SMS, and "correct-horse-battery-staple" survives that where
// "xK9$mQ2!" does not. Four words from a large list is comfortably beyond
// online guessing, and the password lives for an hour.
const generatedPasswordWords = 4

// Service is the onboarding module's behaviour.
type Service struct {
	repo   Repository
	hasher *passwd.Hasher
	sms    sms.Sender
	audit  audit.Recorder
	clock  clock.Clock
	log    *slog.Logger
}

// NewService builds the service.
func NewService(repo Repository, hasher *passwd.Hasher, sender sms.Sender,
	aud audit.Recorder, clk clock.Clock, log *slog.Logger) (*Service, error) {

	if repo == nil || hasher == nil || sender == nil || aud == nil || clk == nil || log == nil {
		return nil, errors.New("onboarding: nil dependency")
	}
	return &Service{repo: repo, hasher: hasher, sms: sender,
		audit: aud, clock: clk, log: log}, nil
}

// Register creates a client.
//
// The vendor supplies what the business told them. Steleios does NOT verify a
// GSTIN against the GST portal, and this is a deliberate scope boundary rather
// than a shortcut: the client is responsible for the accuracy of its own tax
// registration and prices, the vendor provides the fields (BR-ACP-02, docs/09
// §6). What is checked is internal consistency — a GSTIN whose embedded PAN
// contradicts the PAN field is a typo in one of the two, and recording both as
// fact would put the error into every invoice the shop ever issues.
func (s *Service) Register(ctx context.Context, in RegisterInput, by authz.Actor) (Client, error) {
	in.Normalise()

	byID, err := uuid.Parse(by.ID)
	if err != nil {
		return Client{}, fmt.Errorf("onboarding: actor id: %w", err)
	}

	c, err := s.repo.Register(ctx, in, byID)
	if err != nil {
		return Client{}, err
	}

	_ = s.audit.Record(ctx, audit.Entry{ //nolint:errcheck // logged inside
		Action:       "client.registered",
		ResourceType: "client",
		ResourceID:   c.ID.String(),
	})
	s.log.InfoContext(ctx, "client registered",
		"client_id", c.ID.String(), "client_code", c.ClientCode)
	return c, nil
}

// Client returns one client with its owners and shops.
func (s *Service) Client(ctx context.Context, id uuid.UUID) (Client, []Owner, []Shop, error) {
	c, err := s.repo.FindClient(ctx, id)
	if err != nil {
		return Client{}, nil, nil, err
	}
	owners, err := s.repo.OwnersOf(ctx, id)
	if err != nil {
		return Client{}, nil, nil, err
	}
	shops, err := s.repo.ShopsOf(ctx, id)
	if err != nil {
		return Client{}, nil, nil, err
	}
	return c, owners, shops, nil
}

// ListClients returns a page of clients.
func (s *Service) ListClients(ctx context.Context, limit int, after string) ([]Client, error) {
	return s.repo.ListClients(ctx, limit, after)
}

// AddOwner records a natural person behind the business.
//
// Refused once the client is confirmed. The owners are part of the identity
// that confirmation freezes: adding one afterwards would change who the business
// belongs to without changing the record that says who it belonged to.
func (s *Service) AddOwner(ctx context.Context, clientID uuid.UUID, in OwnerInput) (Owner, error) {
	in.Normalise()

	c, err := s.repo.FindClient(ctx, clientID)
	if err != nil {
		return Owner{}, err
	}
	if c.IsConfirmed() {
		return Owner{}, ErrAlreadyConfirmed
	}

	o, err := s.repo.AddOwner(ctx, clientID, in)
	if err != nil {
		return Owner{}, err
	}

	// The audit entry names the owner and the client, never the Aadhaar digits
	// or the PAN. An audit log is read by more people than the record it
	// describes (BR-SEC-07).
	_ = s.audit.Record(ctx, audit.Entry{ //nolint:errcheck // logged inside
		Action:       "client.owner_added",
		ResourceType: "client",
		ResourceID:   clientID.String(),
	})
	return o, nil
}

// ProvisionShop creates a tenant for the client.
//
// Allowed after confirmation, unlike the business identity: a business opening
// a second branch is the same business, and refusing it would mean re-onboarding
// a client to let them grow.
func (s *Service) ProvisionShop(ctx context.Context, clientID uuid.UUID, in ShopInput) (Shop, error) {
	in.Normalise()

	c, err := s.repo.FindClient(ctx, clientID)
	if err != nil {
		return Shop{}, err
	}
	if c.Status != StatusActive {
		return Shop{}, ErrClientNotActive
	}

	// A shop inherits the client's state and GSTIN when it does not name its
	// own. Most businesses trade in one state with one registration, and making
	// them retype it is how the two drift apart.
	if in.StateCode == "" {
		in.StateCode = c.StateCode
	}
	if in.GSTIN == "" {
		in.GSTIN = c.GSTIN
	}
	if in.LegalName == "" {
		in.LegalName = c.LegalName
	}

	shop, err := s.repo.ProvisionShop(ctx, clientID, in)
	if err != nil {
		return Shop{}, err
	}

	_ = s.audit.Record(ctx, audit.Entry{ //nolint:errcheck // logged inside
		Action:       "client.shop_provisioned",
		ResourceType: "tenant",
		ResourceID:   shop.TenantID.String(),
	})
	s.log.InfoContext(ctx, "shop provisioned",
		"client_code", c.ClientCode, "tenant_id", shop.TenantID.String(), "slug", shop.Slug)
	return shop, nil
}

// IssueFirstUser creates the owner's login for a shop.
//
// The password is generated here, returned exactly once, and sent by SMS to the
// number on file. It is never stored in recoverable form and never logged
// (BR-REC-10, BR-REC-12).
//
// SMS rather than email, and the reason is the same one that governs recovery:
// the email address is what the vendor was just told, over the phone or in a
// form, and has not been proven to belong to the person. The mobile is on the
// registration record. Sending credentials to an unverified address would mean
// the first thing the system does with a new business is post its keys to
// somewhere nobody has checked (BR-REC-11).
func (s *Service) IssueFirstUser(ctx context.Context, clientID uuid.UUID, shop Shop,
	email, phone, fullName string) (FirstUser, error) {

	c, err := s.repo.FindClient(ctx, clientID)
	if err != nil {
		return FirstUser{}, err
	}
	if c.Status != StatusActive {
		return FirstUser{}, ErrClientNotActive
	}

	// A contact retired when an account was blocked may never be used again, by
	// anybody (migration 00010). Checked before anything is created so the
	// refusal costs nothing.
	retired, err := s.repo.IsRetiredContact(ctx, email, phone)
	if err != nil {
		return FirstUser{}, err
	}
	if retired {
		return FirstUser{}, ErrRetiredContact
	}

	if phone == "" {
		phone = c.ContactPhone
	}

	password, err := passwd.Generate(generatedPasswordWords)
	if err != nil {
		return FirstUser{}, fmt.Errorf("onboarding: generate password: %w", err)
	}
	hash, err := s.hasher.Hash(password)
	if err != nil {
		return FirstUser{}, fmt.Errorf("onboarding: hash password: %w", err)
	}

	expires := s.clock.Now().Add(generatedPasswordTTL)
	identityID, err := s.repo.CreateOwnerLogin(ctx, OwnerLogin{
		TenantID:     shop.TenantID,
		Email:        email,
		Phone:        phone,
		FullName:     fullName,
		PasswordHash: hash,
		ExpiresAt:    expires,
	})
	if err != nil {
		return FirstUser{}, err
	}

	// The SMS is sent AFTER the account exists. The other order would mean a
	// person holding a password for an account that failed to be created.
	//
	// A failed send does not fail the call: the account is real, the vendor has
	// the password in the response in front of them, and they can read it out.
	// Rolling back a created owner because an SMS gateway was slow would be a
	// worse outcome than a warning.
	if err := s.sms.Send(ctx, phone, sms.Message{
		Template: sms.TemplateFirstLogin,
		Params: map[string]string{
			"client": c.ClientCode,
			"code":   password,
			"hours":  "1",
		},
	}); err != nil {
		s.log.ErrorContext(ctx, "could not send the first-login SMS; the vendor must read the password out",
			"client_id", clientID.String(), "identity_id", identityID.String(), "error", err.Error())
	}

	_ = s.audit.Record(ctx, audit.Entry{ //nolint:errcheck // logged inside
		Action:       "client.first_user_issued",
		ResourceType: "identity",
		ResourceID:   identityID.String(),
	})
	// Logged without the password, deliberately and permanently.
	s.log.InfoContext(ctx, "first user issued",
		"client_code", c.ClientCode, "identity_id", identityID.String(),
		"tenant_id", shop.TenantID.String())

	return FirstUser{
		IdentityID: identityID,
		Email:      email,
		Password:   password,
		ExpiresAt:  expires,
	}, nil
}

// Confirm freezes the client's business identity, permanently.
//
// The preconditions are not bureaucracy. After this the identifiers cannot be
// corrected by anyone — the database refuses it (migration 00012) — so
// confirming a client that is missing one of them would freeze a record that
// identifies nothing and could never be fixed. The only remedy would be a second
// client, and then the business has two records and its accountant has two sets
// of books.
func (s *Service) Confirm(ctx context.Context, clientID uuid.UUID, by authz.Actor) (Client, error) {
	c, err := s.repo.FindClient(ctx, clientID)
	if err != nil {
		return Client{}, err
	}
	if c.IsConfirmed() {
		return Client{}, ErrAlreadyConfirmed
	}
	// BR-IDN: the client is permanently bound to a government identifier, and
	// GSTIN or TIN is the one that binds it.
	if c.GSTIN == "" && c.TIN == "" {
		return Client{}, ErrNoIdentifier
	}

	owners, err := s.repo.OwnersOf(ctx, clientID)
	if err != nil {
		return Client{}, err
	}
	if len(owners) == 0 {
		return Client{}, ErrNoOwner
	}

	shops, err := s.repo.ShopsOf(ctx, clientID)
	if err != nil {
		return Client{}, err
	}
	if len(shops) == 0 {
		return Client{}, ErrNoShop
	}

	byID, err := uuid.Parse(by.ID)
	if err != nil {
		return Client{}, fmt.Errorf("onboarding: actor id: %w", err)
	}

	if err := s.repo.Confirm(ctx, clientID, byID, s.clock.Now()); err != nil {
		return Client{}, err
	}

	_ = s.audit.Record(ctx, audit.Entry{ //nolint:errcheck // logged inside
		Action:       "client.confirmed",
		ResourceType: "client",
		ResourceID:   clientID.String(),
	})
	s.log.InfoContext(ctx, "client confirmed; business identity is now permanent",
		"client_id", clientID.String(), "client_code", c.ClientCode)

	return s.repo.FindClient(ctx, clientID)
}

// ShopOf returns one of a client's shops by tenant id.
func (s *Service) ShopOf(ctx context.Context, clientID, tenantID uuid.UUID) (Shop, error) {
	shops, err := s.repo.ShopsOf(ctx, clientID)
	if err != nil {
		return Shop{}, err
	}
	for _, sh := range shops {
		if sh.TenantID.UUID() == tenantID {
			return sh, nil
		}
	}
	// The shop may well exist — just not for this client. Saying so plainly is
	// safe here because the caller is a vendor administrator who can list every
	// client anyway; there is no oracle to protect against.
	return Shop{}, ErrShopNotThisClient
}

// unusedGuard keeps the ids import meaningful if the generator moves.
var _ = ids.Fingerprint
