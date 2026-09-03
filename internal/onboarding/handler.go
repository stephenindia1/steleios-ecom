package onboarding

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/stephenindia1/steleios-ecom/internal/platform/httpx"
)

// Handler is the vendor-facing HTTP surface.
type Handler struct {
	svc *Service
	log *slog.Logger
}

// NewHandler builds the handler.
func NewHandler(svc *Service, log *slog.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

// ---------------------------------------------------------------------------
// Representations
// ---------------------------------------------------------------------------

type clientView struct {
	ID         string `json:"id"`
	ClientCode string `json:"client_code"`
	LegalName  string `json:"legal_name"`

	ContactEmail string `json:"contact_email"`
	ContactPhone string `json:"contact_phone"`
	Status       string `json:"status"`

	GSTIN             string `json:"gstin,omitempty"`
	TIN               string `json:"tin,omitempty"`
	PAN               string `json:"pan,omitempty"`
	CIN               string `json:"cin,omitempty"`
	UdyamNumber       string `json:"udyam_number,omitempty"`
	ShopLicenceNumber string `json:"shop_licence_number,omitempty"`
	GSTRegistered     bool   `json:"gst_registered"`

	BusinessType      string `json:"business_type,omitempty"`
	RegisteredAddress string `json:"registered_address,omitempty"`
	StateCode         string `json:"state_code,omitempty"`

	OnboardedAt *time.Time `json:"onboarded_at,omitempty"`
	ConfirmedAt *time.Time `json:"confirmed_at,omitempty"`
	// Confirmed is the flag a console needs to decide whether to show an edit
	// button at all, rather than offering one that the database will refuse.
	Confirmed bool `json:"confirmed"`
}

func viewClient(c Client) clientView {
	return clientView{
		ID: c.ID.String(), ClientCode: c.ClientCode, LegalName: c.LegalName,
		ContactEmail: c.ContactEmail, ContactPhone: c.ContactPhone, Status: c.Status,
		GSTIN: c.GSTIN, TIN: c.TIN, PAN: c.PAN, CIN: c.CIN,
		UdyamNumber: c.UdyamNumber, ShopLicenceNumber: c.ShopLicenceNumber,
		GSTRegistered: c.GSTRegistered,
		BusinessType:  c.BusinessType, RegisteredAddress: c.RegisteredAddress,
		StateCode:   c.StateCode,
		OnboardedAt: c.OnboardedAt, ConfirmedAt: c.ConfirmedAt,
		Confirmed: c.IsConfirmed(),
	}
}

type ownerView struct {
	ID        string `json:"id"`
	FullName  string `json:"full_name"`
	PAN       string `json:"pan,omitempty"`
	City      string `json:"city"`
	StateCode string `json:"state_code"`
	Pincode   string `json:"pincode"`
	// AadhaarLast4 is the only part of an Aadhaar this system holds or returns.
	AadhaarLast4 string `json:"aadhaar_last4,omitempty"`
	Email        string `json:"email,omitempty"`
	Phone        string `json:"phone,omitempty"`
	IsPrimary    bool   `json:"is_primary"`
}

func viewOwner(o Owner) ownerView {
	return ownerView{
		ID: o.ID.String(), FullName: o.FullName, PAN: o.PAN,
		City: o.City, StateCode: o.StateCode, Pincode: o.Pincode,
		AadhaarLast4: o.AadhaarLast4, Email: o.Email, Phone: o.Phone,
		IsPrimary: o.IsPrimary,
	}
}

type shopView struct {
	TenantID  string `json:"tenant_id"`
	Slug      string `json:"slug"`
	ShopCode  string `json:"shop_code"`
	LegalName string `json:"legal_name"`
	StateCode string `json:"state_code,omitempty"`
	GSTIN     string `json:"gstin,omitempty"`
	Status    string `json:"status"`
}

func viewShop(s Shop) shopView {
	return shopView{
		TenantID: s.TenantID.String(), Slug: s.Slug, ShopCode: s.ShopCode,
		LegalName: s.LegalName, StateCode: s.StateCode, GSTIN: s.GSTIN, Status: s.Status,
	}
}

// ---------------------------------------------------------------------------
// Requests
// ---------------------------------------------------------------------------

type registerRequest struct {
	LegalName    string `json:"legal_name"`
	ContactEmail string `json:"contact_email"`
	ContactPhone string `json:"contact_phone"`

	GSTIN             string `json:"gstin"`
	TIN               string `json:"tin"`
	PAN               string `json:"pan"`
	CIN               string `json:"cin"`
	UdyamNumber       string `json:"udyam_number"`
	ShopLicenceNumber string `json:"shop_licence_number"`

	BusinessType      string `json:"business_type"`
	RegisteredAddress string `json:"registered_address"`
	StateCode         string `json:"state_code"`
}

func (r registerRequest) input() RegisterInput {
	in := RegisterInput{
		LegalName: r.LegalName, ContactEmail: r.ContactEmail, ContactPhone: r.ContactPhone,
		GSTIN: r.GSTIN, TIN: r.TIN, PAN: r.PAN, CIN: r.CIN,
		UdyamNumber: r.UdyamNumber, ShopLicenceNumber: r.ShopLicenceNumber,
		BusinessType: r.BusinessType, RegisteredAddress: r.RegisteredAddress,
		StateCode: r.StateCode,
	}
	in.Normalise()
	return in
}

// Validate runs the same rules the service will. Normalising first matters: a
// GSTIN typed in lower case is a formatting slip, not an error, and rejecting
// it before normalisation would report a problem that does not exist.
func (r registerRequest) Validate() map[string]string { return r.input().Validate() }

type ownerRequest struct {
	FullName     string `json:"full_name"`
	PAN          string `json:"pan"`
	AddressLine1 string `json:"address_line1"`
	AddressLine2 string `json:"address_line2"`
	City         string `json:"city"`
	StateCode    string `json:"state_code"`
	Pincode      string `json:"pincode"`
	AadhaarLast4 string `json:"aadhaar_last4"`
	Email        string `json:"email"`
	Phone        string `json:"phone"`
	IsPrimary    bool   `json:"is_primary"`
}

func (r ownerRequest) input() OwnerInput {
	in := OwnerInput{
		FullName: r.FullName, PAN: r.PAN,
		AddressL1: r.AddressLine1, AddressL2: r.AddressLine2,
		City: r.City, StateCode: r.StateCode, Pincode: r.Pincode,
		AadhaarLast4: r.AadhaarLast4, Email: r.Email, Phone: r.Phone,
		IsPrimary: r.IsPrimary,
	}
	in.Normalise()
	return in
}

func (r ownerRequest) Validate() map[string]string { return r.input().Validate() }

type shopRequest struct {
	Slug      string `json:"slug"`
	ShopCode  string `json:"shop_code"`
	LegalName string `json:"legal_name"`
	StateCode string `json:"state_code"`
	GSTIN     string `json:"gstin"`
	GroupID   string `json:"group_id"`
}

func (r shopRequest) input() (ShopInput, error) {
	in := ShopInput{
		Slug: r.Slug, ShopCode: r.ShopCode, LegalName: r.LegalName,
		StateCode: r.StateCode, GSTIN: r.GSTIN,
	}
	if r.GroupID != "" {
		id, err := uuid.Parse(r.GroupID)
		if err != nil {
			return ShopInput{}, err
		}
		in.GroupID = &id
	}
	in.Normalise()
	return in, nil
}

func (r shopRequest) Validate() map[string]string {
	in, err := r.input()
	if err != nil {
		return map[string]string{"group_id": "That is not a valid group id."}
	}
	// LegalName is optional here and inherited from the client when absent, so
	// the service's default is applied before the shape rule would reject it.
	if in.LegalName == "" {
		in.LegalName = "inherited"
	}
	return in.Validate()
}

type firstUserRequest struct {
	TenantID string `json:"tenant_id"`
	Email    string `json:"email"`
	Phone    string `json:"phone"`
	FullName string `json:"full_name"`
}

func (r firstUserRequest) Validate() map[string]string {
	f := map[string]string{}
	if r.TenantID == "" {
		f["tenant_id"] = "Name the shop this owner signs in to."
	}
	if r.Email == "" {
		f["email"] = "Enter the owner's email address."
	}
	if r.FullName == "" {
		f["full_name"] = "Enter the owner's name."
	}
	return f
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// Register creates a client.
func (h *Handler) Register(ctx context.Context, req *httpx.Request) (httpx.Response, error) {
	var body registerRequest
	if err := req.Decode(&body); err != nil {
		return httpx.Response{}, err
	}

	c, err := h.svc.Register(ctx, body.input(), req.Actor())
	if err != nil {
		return httpx.Response{}, mapError(err)
	}
	return httpx.Created("/api/v1/platform/clients/"+c.ID.String(), viewClient(c)), nil
}

// ListClients returns a page of clients.
func (h *Handler) ListClients(ctx context.Context, req *httpx.Request) (httpx.Response, error) {
	// Bounded, and the bound is not optional: an unbounded limit is how a
	// client asks for every row in the table (DB-020).
	limit, err := req.QueryInt("limit", 50, 1, 200)
	if err != nil {
		return httpx.Response{}, err
	}

	clients, err := h.svc.ListClients(ctx, limit, req.Query("after"))
	if err != nil {
		return httpx.Response{}, mapError(err)
	}

	views := make([]clientView, 0, len(clients)) // DB-024
	for _, c := range clients {
		views = append(views, viewClient(c))
	}

	// The cursor is the last code on the page. Absent when the page was not
	// full, which is how a caller knows to stop.
	next := ""
	if len(clients) == limit {
		next = clients[len(clients)-1].ClientCode
	}
	return httpx.OK(map[string]any{"clients": views, "next": next}), nil
}

type clientDetail struct {
	Client clientView  `json:"client"`
	Owners []ownerView `json:"owners"`
	Shops  []shopView  `json:"shops"`
	// ReadyToConfirm tells the console whether the button should be enabled,
	// and Blockers says what is missing when it is not — so the vendor is not
	// left pressing a button that refuses without saying why.
	ReadyToConfirm bool     `json:"ready_to_confirm"`
	Blockers       []string `json:"blockers,omitempty"`
}

// Client returns one client with its owners and shops.
func (h *Handler) Client(ctx context.Context, req *httpx.Request) (httpx.Response, error) {
	id, err := uuid.Parse(req.Param("id"))
	if err != nil {
		return httpx.Response{}, httpx.NotFound(err)
	}

	c, owners, shops, err := h.svc.Client(ctx, id)
	if err != nil {
		return httpx.Response{}, mapError(err)
	}

	ownerViews := make([]ownerView, 0, len(owners)) // DB-024
	for _, o := range owners {
		ownerViews = append(ownerViews, viewOwner(o))
	}
	shopViews := make([]shopView, 0, len(shops)) // DB-024
	for _, s := range shops {
		shopViews = append(shopViews, viewShop(s))
	}

	blockers := []string{}
	if !c.IsConfirmed() {
		if c.GSTIN == "" && c.TIN == "" {
			blockers = append(blockers, "no GSTIN or TIN on record")
		}
		if len(owners) == 0 {
			blockers = append(blockers, "no owner on record")
		}
		if len(shops) == 0 {
			blockers = append(blockers, "no shop provisioned")
		}
	}

	return httpx.OK(clientDetail{
		Client: viewClient(c), Owners: ownerViews, Shops: shopViews,
		ReadyToConfirm: !c.IsConfirmed() && len(blockers) == 0,
		Blockers:       blockers,
	}), nil
}

// AddOwner records a natural person behind the business.
func (h *Handler) AddOwner(ctx context.Context, req *httpx.Request) (httpx.Response, error) {
	id, err := uuid.Parse(req.Param("id"))
	if err != nil {
		return httpx.Response{}, httpx.NotFound(err)
	}

	var body ownerRequest
	if err := req.Decode(&body); err != nil {
		return httpx.Response{}, err
	}

	o, err := h.svc.AddOwner(ctx, id, body.input())
	if err != nil {
		return httpx.Response{}, mapError(err)
	}
	return httpx.Created("/api/v1/platform/clients/"+id.String(), viewOwner(o)), nil
}

// ProvisionShop creates a tenant for the client.
func (h *Handler) ProvisionShop(ctx context.Context, req *httpx.Request) (httpx.Response, error) {
	id, err := uuid.Parse(req.Param("id"))
	if err != nil {
		return httpx.Response{}, httpx.NotFound(err)
	}

	var body shopRequest
	if err := req.Decode(&body); err != nil {
		return httpx.Response{}, err
	}
	in, err := body.input()
	if err != nil {
		return httpx.Response{}, httpx.Validation(map[string]string{"group_id": "That is not a valid group id."})
	}

	shop, err := h.svc.ProvisionShop(ctx, id, in)
	if err != nil {
		return httpx.Response{}, mapError(err)
	}
	return httpx.Created("/api/v1/platform/clients/"+id.String(), viewShop(shop)), nil
}

type firstUserView struct {
	IdentityID string `json:"identity_id"`
	Email      string `json:"email"`
	// Password appears in this response and nowhere else, ever. It is not
	// stored in recoverable form, not logged, and not retrievable afterwards
	// (BR-REC-12). If it is lost before use, issue another.
	Password  string    `json:"password"`
	ExpiresAt time.Time `json:"expires_at"`
	Notice    string    `json:"notice"`
}

// IssueFirstUser creates the owner's login for one shop.
func (h *Handler) IssueFirstUser(ctx context.Context, req *httpx.Request) (httpx.Response, error) {
	clientID, err := uuid.Parse(req.Param("id"))
	if err != nil {
		return httpx.Response{}, httpx.NotFound(err)
	}

	var body firstUserRequest
	if err := req.Decode(&body); err != nil {
		return httpx.Response{}, err
	}

	tenantID, err := uuid.Parse(body.TenantID)
	if err != nil {
		return httpx.Response{}, httpx.Validation(map[string]string{"tenant_id": "That is not a valid shop id."})
	}

	// The shop must belong to THIS client. Without the check a vendor
	// administrator could create an owner login into any shop in the system by
	// naming its id, which is the cross-client hole the tenant model exists to
	// prevent (SEC-09: the service re-asserts what the route cannot know).
	shop, err := h.svc.ShopOf(ctx, clientID, tenantID)
	if err != nil {
		return httpx.Response{}, mapError(err)
	}

	user, err := h.svc.IssueFirstUser(ctx, clientID, shop, body.Email, body.Phone, body.FullName)
	if err != nil {
		return httpx.Response{}, mapError(err)
	}

	return httpx.Created("/api/v1/platform/clients/"+clientID.String(), firstUserView{
		IdentityID: user.IdentityID.String(),
		Email:      user.Email,
		Password:   user.Password,
		ExpiresAt:  user.ExpiresAt,
		Notice: "This password is shown once and cannot be retrieved. " +
			"It was also sent by SMS to the registered mobile. " +
			"The owner must change it at first sign-in and can do nothing else until they do.",
	}), nil
}

// Confirm freezes the client's business identity, permanently.
func (h *Handler) Confirm(ctx context.Context, req *httpx.Request) (httpx.Response, error) {
	id, err := uuid.Parse(req.Param("id"))
	if err != nil {
		return httpx.Response{}, httpx.NotFound(err)
	}

	c, err := h.svc.Confirm(ctx, id, req.Actor())
	if err != nil {
		return httpx.Response{}, mapError(err)
	}
	return httpx.OK(viewClient(c)), nil
}

// mapError turns a domain error into the response the vendor should see.
//
// Unlike the identity module's, these messages are specific. The caller is a
// vendor administrator acting on the vendor's own records, so there is no
// oracle to protect against — and a refusal they cannot understand is a support
// call.
func mapError(err error) error {
	switch {
	case errors.Is(err, ErrNoSuchClient):
		return httpx.NotFound(err)
	case errors.Is(err, ErrAlreadyConfirmed):
		return httpx.Conflict("The business identity is confirmed and cannot be changed. A different business is a different client.", err)
	case errors.Is(err, ErrNoIdentifier):
		return httpx.Conflict("Record a GSTIN or TIN before confirming: it is what the client is permanently bound to.", err)
	case errors.Is(err, ErrNoOwner):
		return httpx.Conflict("Record at least one owner before confirming.", err)
	case errors.Is(err, ErrNoShop):
		return httpx.Conflict("Provision at least one shop before confirming.", err)
	case errors.Is(err, ErrShopNotThisClient):
		return httpx.Conflict("That shop belongs to a different client.", err)
	case errors.Is(err, ErrDuplicateIdentifier):
		return httpx.Conflict("Another client already holds one of those identifiers. This business may already be onboarded.", err)
	case errors.Is(err, ErrDuplicateShop):
		return httpx.Conflict("A shop with that slug or code already exists.", err)
	case errors.Is(err, ErrEmailInUse):
		return httpx.Conflict("That email address already has a login.", err)
	case errors.Is(err, ErrRetiredContact):
		return httpx.Conflict("That email or mobile was retired from a blocked account and can never be reused.", err)
	case errors.Is(err, ErrClientNotActive):
		return httpx.Conflict("The client is suspended or closed.", err)
	default:
		return err
	}
}
