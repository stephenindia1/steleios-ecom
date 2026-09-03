package onboarding_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stephenindia1/steleios-ecom/internal/onboarding"
	"github.com/stephenindia1/steleios-ecom/internal/platform/audit"
	"github.com/stephenindia1/steleios-ecom/internal/platform/authz"
	"github.com/stephenindia1/steleios-ecom/internal/platform/clock"
	"github.com/stephenindia1/steleios-ecom/internal/platform/passwd"
	"github.com/stephenindia1/steleios-ecom/internal/platform/sms"
	"github.com/stephenindia1/steleios-ecom/internal/platform/tenant"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

type fakeRepo struct {
	clients map[uuid.UUID]onboarding.Client
	owners  map[uuid.UUID][]onboarding.Owner
	shops   map[uuid.UUID][]onboarding.Shop

	retired  bool
	logins   int
	lastHash string
	// lastTenant records which shop the owner login was created in, so a test
	// can assert it landed in the right one.
	lastTenant tenant.ID
}

func newRepo() *fakeRepo {
	return &fakeRepo{
		clients: map[uuid.UUID]onboarding.Client{},
		owners:  map[uuid.UUID][]onboarding.Owner{},
		shops:   map[uuid.UUID][]onboarding.Shop{},
	}
}

func (f *fakeRepo) Register(_ context.Context, in onboarding.RegisterInput, _ uuid.UUID) (onboarding.Client, error) {
	c := onboarding.Client{
		ID: uuid.New(), ClientCode: "STL-C-000001", LegalName: in.LegalName,
		ContactEmail: in.ContactEmail, ContactPhone: in.ContactPhone,
		Status: onboarding.StatusActive,
		GSTIN:  in.GSTIN, TIN: in.TIN, PAN: in.PAN, StateCode: in.StateCode,
	}
	f.clients[c.ID] = c
	return c, nil
}

func (f *fakeRepo) FindClient(_ context.Context, id uuid.UUID) (onboarding.Client, error) {
	c, ok := f.clients[id]
	if !ok {
		return onboarding.Client{}, onboarding.ErrNoSuchClient
	}
	return c, nil
}

func (f *fakeRepo) FindClientByCode(context.Context, string) (onboarding.Client, error) {
	return onboarding.Client{}, onboarding.ErrNoSuchClient
}

func (f *fakeRepo) ListClients(context.Context, int, string) ([]onboarding.Client, error) {
	return nil, nil
}

func (f *fakeRepo) AddOwner(_ context.Context, clientID uuid.UUID, in onboarding.OwnerInput) (onboarding.Owner, error) {
	o := onboarding.Owner{
		ID: uuid.New(), ClientID: clientID, FullName: in.FullName,
		AadhaarLast4: in.AadhaarLast4, IsPrimary: in.IsPrimary,
	}
	f.owners[clientID] = append(f.owners[clientID], o)
	return o, nil
}

func (f *fakeRepo) OwnersOf(_ context.Context, id uuid.UUID) ([]onboarding.Owner, error) {
	return f.owners[id], nil
}

func (f *fakeRepo) ProvisionShop(_ context.Context, clientID uuid.UUID, in onboarding.ShopInput) (onboarding.Shop, error) {
	s := onboarding.Shop{
		TenantID: tenant.ID(uuid.New()), ClientID: clientID,
		Slug: in.Slug, ShopCode: in.ShopCode, LegalName: in.LegalName,
		StateCode: in.StateCode, GSTIN: in.GSTIN, Status: onboarding.StatusActive,
	}
	f.shops[clientID] = append(f.shops[clientID], s)
	return s, nil
}

func (f *fakeRepo) ShopsOf(_ context.Context, id uuid.UUID) ([]onboarding.Shop, error) {
	return f.shops[id], nil
}

func (f *fakeRepo) CreateOwnerLogin(_ context.Context, in onboarding.OwnerLogin) (uuid.UUID, error) {
	f.logins++
	f.lastHash = in.PasswordHash
	f.lastTenant = in.TenantID
	return uuid.New(), nil
}

func (f *fakeRepo) IsRetiredContact(context.Context, string, string) (bool, error) {
	return f.retired, nil
}

func (f *fakeRepo) Confirm(_ context.Context, id, _ uuid.UUID, at time.Time) error {
	c, ok := f.clients[id]
	if !ok {
		return onboarding.ErrNoSuchClient
	}
	if c.ConfirmedAt != nil {
		return onboarding.ErrAlreadyConfirmed
	}
	c.ConfirmedAt = &at
	f.clients[id] = c
	return nil
}

// recordingSender captures what would have been sent, so a test can assert the
// message went to the right number under the right template — and, crucially,
// that the password reached the phone at all.
type recordingSender struct {
	sent []struct {
		to  string
		msg sms.Message
	}
	err error
}

func (r *recordingSender) Send(_ context.Context, to string, msg sms.Message) error {
	if r.err != nil {
		return r.err
	}
	r.sent = append(r.sent, struct {
		to  string
		msg sms.Message
	}{to, msg})
	return nil
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

type fixture struct {
	svc   *onboarding.Service
	repo  *fakeRepo
	sms   *recordingSender
	audit *audit.Recording
	clk   *clock.Fake
	actor authz.Actor
}

func newFixture(t *testing.T) fixture {
	t.Helper()

	// Cheap hashing: what is under test is the onboarding sequence, not
	// Argon2id, which has its own tests.
	h, err := passwd.New(passwd.Params{
		Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
	})
	if err != nil {
		t.Fatalf("hasher: %v", err)
	}

	repo := newRepo()
	sender := &recordingSender{}
	rec := &audit.Recording{}
	clk := clock.NewFake(time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC))

	svc, err := onboarding.NewService(repo, h, sender, rec, clk,
		slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("service: %v", err)
	}

	return fixture{
		svc: svc, repo: repo, sms: sender, audit: rec, clk: clk,
		actor: authz.Actor{
			ID:    uuid.NewString(),
			Type:  authz.ActorAdmin,
			Roles: []authz.Role{authz.RoleSaaSAdmin},
		},
	}
}

func (f fixture) register(t *testing.T) onboarding.Client {
	t.Helper()

	c, err := f.svc.Register(t.Context(), onboarding.RegisterInput{
		LegalName:    "Kadambari Stores Pvt Ltd",
		ContactEmail: "owner@kadambari.example",
		ContactPhone: "+919876543210",
		GSTIN:        "29ABCDE1234F1Z5",
		PAN:          "ABCDE1234F",
		StateCode:    "29",
	}, f.actor)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	return c
}

func (f fixture) addOwner(t *testing.T, c onboarding.Client) {
	t.Helper()

	if _, err := f.svc.AddOwner(t.Context(), c.ID, onboarding.OwnerInput{
		FullName: "Ravi Kadambari", AddressL1: "12 MG Road",
		City: "Bengaluru", StateCode: "29", Pincode: "560001",
		AadhaarLast4: "7391", IsPrimary: true,
	}); err != nil {
		t.Fatalf("AddOwner: %v", err)
	}
}

func (f fixture) addShop(t *testing.T, c onboarding.Client) onboarding.Shop {
	t.Helper()

	s, err := f.svc.ProvisionShop(t.Context(), c.ID, onboarding.ShopInput{
		Slug: "kadambari-mg-road", ShopCode: "MGRD",
	})
	if err != nil {
		t.Fatalf("ProvisionShop: %v", err)
	}
	return s
}

// ---------------------------------------------------------------------------
// Confirmation: the point of no return
// ---------------------------------------------------------------------------

// TestConfirmRequiresACompleteRecord.
//
// These preconditions are not bureaucracy. After confirmation the identifiers
// cannot be corrected by anyone — the database refuses it (migration 00012) — so
// confirming an incomplete client freezes a record that identifies nothing and
// can never be fixed. The only remedy would be a second client, and then the
// business has two records and its accountant has two sets of books.
func TestConfirmRequiresACompleteRecord(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		prepare func(t *testing.T, f fixture) onboarding.Client
		want    error
	}{
		{
			name: "no government identifier",
			prepare: func(t *testing.T, f fixture) onboarding.Client {
				c, err := f.svc.Register(t.Context(), onboarding.RegisterInput{
					LegalName: "No Identifier Ltd", ContactEmail: "a@b.example",
					ContactPhone: "+919876543210",
				}, f.actor)
				if err != nil {
					t.Fatalf("Register: %v", err)
				}
				f.addOwner(t, c)
				f.addShop(t, c)
				return c
			},
			want: onboarding.ErrNoIdentifier,
		},
		{
			name: "no owner",
			prepare: func(t *testing.T, f fixture) onboarding.Client {
				c := f.register(t)
				f.addShop(t, c)
				return c
			},
			want: onboarding.ErrNoOwner,
		},
		{
			name: "no shop",
			prepare: func(t *testing.T, f fixture) onboarding.Client {
				c := f.register(t)
				f.addOwner(t, c)
				return c
			},
			want: onboarding.ErrNoShop,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			c := tc.prepare(t, f)

			_, err := f.svc.Confirm(t.Context(), c.ID, f.actor)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Confirm = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestConfirmSucceedsOnACompleteRecord(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	c := f.register(t)
	f.addOwner(t, c)
	f.addShop(t, c)

	got, err := f.svc.Confirm(t.Context(), c.ID, f.actor)
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if !got.IsConfirmed() {
		t.Fatal("the client is not marked confirmed")
	}
}

// TestConfirmingTwiceIsRefused: confirmation is the point of no return, and a
// second one would look like it changed something.
func TestConfirmingTwiceIsRefused(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	c := f.register(t)
	f.addOwner(t, c)
	f.addShop(t, c)

	if _, err := f.svc.Confirm(t.Context(), c.ID, f.actor); err != nil {
		t.Fatalf("first Confirm: %v", err)
	}
	if _, err := f.svc.Confirm(t.Context(), c.ID, f.actor); !errors.Is(err, onboarding.ErrAlreadyConfirmed) {
		t.Fatalf("second Confirm = %v, want ErrAlreadyConfirmed", err)
	}
}

// TestOwnersCannotBeAddedAfterConfirmation.
//
// The owners are part of the identity that confirmation freezes. Adding one
// afterwards would change who the business belongs to without changing the
// record that says who it belonged to.
func TestOwnersCannotBeAddedAfterConfirmation(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	c := f.register(t)
	f.addOwner(t, c)
	f.addShop(t, c)

	if _, err := f.svc.Confirm(t.Context(), c.ID, f.actor); err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	_, err := f.svc.AddOwner(t.Context(), c.ID, onboarding.OwnerInput{
		FullName: "Someone Else", AddressL1: "x", City: "y",
		StateCode: "29", Pincode: "560001",
	})
	if !errors.Is(err, onboarding.ErrAlreadyConfirmed) {
		t.Fatalf("AddOwner after confirm = %v, want ErrAlreadyConfirmed", err)
	}
}

// TestShopsCanStillBeAddedAfterConfirmation is the deliberate asymmetry.
//
// A business opening a second branch is the same business. Refusing it would
// mean re-onboarding a client in order to let them grow, and would leave the
// second branch's trading outside the record of the first.
func TestShopsCanStillBeAddedAfterConfirmation(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	c := f.register(t)
	f.addOwner(t, c)
	f.addShop(t, c)

	if _, err := f.svc.Confirm(t.Context(), c.ID, f.actor); err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	if _, err := f.svc.ProvisionShop(t.Context(), c.ID, onboarding.ShopInput{
		Slug: "kadambari-jayanagar", ShopCode: "JAYA",
	}); err != nil {
		t.Fatalf("a confirmed client could not open a second shop: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Shops
// ---------------------------------------------------------------------------

// TestAShopInheritsTheClientsRegistration: most businesses trade in one state
// with one GST registration, and making them retype it is how the two drift
// apart — which would then put the wrong place of supply on invoices.
func TestAShopInheritsTheClientsRegistration(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	c := f.register(t)
	shop := f.addShop(t, c)

	if shop.StateCode != "29" {
		t.Errorf("state = %q, want the client's 29", shop.StateCode)
	}
	if shop.GSTIN != "29ABCDE1234F1Z5" {
		t.Errorf("gstin = %q, want the client's", shop.GSTIN)
	}
	if shop.LegalName != c.LegalName {
		t.Errorf("legal name = %q, want the client's %q", shop.LegalName, c.LegalName)
	}
}

// TestAShopMayHaveItsOwnRegistration: GST registration is per state, so a
// business trading in two states holds two, and each shop's invoices depend on
// which one issued them.
func TestAShopMayHaveItsOwnRegistration(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	c := f.register(t)

	shop, err := f.svc.ProvisionShop(t.Context(), c.ID, onboarding.ShopInput{
		Slug: "kadambari-pune", ShopCode: "PUNE",
		StateCode: "27", GSTIN: "27ABCDE1234F1Z5",
	})
	if err != nil {
		t.Fatalf("ProvisionShop: %v", err)
	}
	if shop.StateCode != "27" || shop.GSTIN != "27ABCDE1234F1Z5" {
		t.Fatalf("the shop did not keep its own registration: %+v", shop)
	}
}

func TestShopOfRefusesAnotherClientsShop(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	mine := f.register(t)
	f.addShop(t, mine)

	theirs, err := f.svc.Register(t.Context(), onboarding.RegisterInput{
		LegalName: "Other Business", ContactEmail: "other@x.example",
		ContactPhone: "+919000000000", GSTIN: "27ZZZZZ9999Z1Z5",
		PAN: "ZZZZZ9999Z", StateCode: "27",
	}, f.actor)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	theirShop := f.addShop(t, theirs)

	// The check that stops a vendor administrator creating an owner login into
	// any shop in the system by naming its id.
	_, err = f.svc.ShopOf(t.Context(), mine.ID, theirShop.TenantID.UUID())
	if !errors.Is(err, onboarding.ErrShopNotThisClient) {
		t.Fatalf("ShopOf = %v, want ErrShopNotThisClient", err)
	}
}

// ---------------------------------------------------------------------------
// The first login
// ---------------------------------------------------------------------------

func TestTheFirstLoginPasswordGoesToThePhone(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	c := f.register(t)
	shop := f.addShop(t, c)

	user, err := f.svc.IssueFirstUser(t.Context(), c.ID, shop,
		"ravi@kadambari.example", "", "Ravi Kadambari")
	if err != nil {
		t.Fatalf("IssueFirstUser: %v", err)
	}

	if user.Password == "" {
		t.Fatal("no password was returned")
	}
	// It expires, and quickly. A generated password has been seen by a vendor
	// administrator and travelled over SMS, so it is the weakest credential in
	// the system and its value is that it does not last (BR-REC-10).
	if !user.ExpiresAt.After(f.clk.Now()) {
		t.Error("the issued password does not expire in the future")
	}
	if user.ExpiresAt.Sub(f.clk.Now()) > 2*time.Hour {
		t.Errorf("the issued password lasts %s; it should be short-lived", user.ExpiresAt.Sub(f.clk.Now()))
	}

	if len(f.sms.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(f.sms.sent))
	}
	sent := f.sms.sent[0]

	// SMS to the number on the registration record, NOT to the email address —
	// which the vendor was simply told and has not been proven to belong to
	// anyone (BR-REC-11).
	if sent.to != c.ContactPhone {
		t.Errorf("sent to %q, want the registered mobile %q", sent.to, c.ContactPhone)
	}
	if sent.msg.Template != sms.TemplateFirstLogin {
		t.Errorf("template = %q, want %q", sent.msg.Template, sms.TemplateFirstLogin)
	}
	if sent.msg.Params["code"] != user.Password {
		t.Error("the message did not carry the password that was issued")
	}
}

// TestAFailedSMSStillCreatesTheAccount.
//
// The account is real and the vendor has the password in front of them — they
// can read it out. Rolling back a created owner because a gateway was slow
// would be a worse outcome than a warning in the log.
func TestAFailedSMSStillCreatesTheAccount(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.sms.err = sms.ErrUnavailable

	c := f.register(t)
	shop := f.addShop(t, c)

	user, err := f.svc.IssueFirstUser(t.Context(), c.ID, shop,
		"ravi@kadambari.example", "", "Ravi Kadambari")
	if err != nil {
		t.Fatalf("IssueFirstUser failed because the SMS did: %v", err)
	}
	if user.Password == "" {
		t.Fatal("no password was returned, so the vendor cannot read it out either")
	}
}

// TestARetiredContactCannotBeReused: a contact retired when an account was
// blocked may never be used again, by anybody (migration 00010).
func TestARetiredContactCannotBeReused(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.repo.retired = true

	c := f.register(t)
	shop := f.addShop(t, c)

	_, err := f.svc.IssueFirstUser(t.Context(), c.ID, shop,
		"blocked@kadambari.example", "", "Someone")
	if !errors.Is(err, onboarding.ErrRetiredContact) {
		t.Fatalf("IssueFirstUser = %v, want ErrRetiredContact", err)
	}
	if len(f.sms.sent) != 0 {
		t.Error("a message was sent for a refused account")
	}
}

// TestTheIssuedPasswordIsNeverTheSameTwice.
//
// Obvious, and worth pinning: a generator that returned a constant would pass
// every other test in this file.
func TestTheIssuedPasswordIsNeverTheSameTwice(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	c := f.register(t)
	shop := f.addShop(t, c)

	seen := map[string]bool{}
	for i := range 20 {
		user, err := f.svc.IssueFirstUser(t.Context(), c.ID, shop,
			"owner@kadambari.example", "", "Ravi")
		if err != nil {
			t.Fatalf("issue %d: %v", i, err)
		}
		if seen[user.Password] {
			t.Fatalf("the generator repeated %q", user.Password)
		}
		seen[user.Password] = true
	}
}

func TestNewServiceValidatesDependencies(t *testing.T) {
	t.Parallel()

	if _, err := onboarding.NewService(nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("NewService accepted nil dependencies")
	}
}
