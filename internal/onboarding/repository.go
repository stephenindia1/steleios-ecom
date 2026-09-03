package onboarding

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stephenindia1/steleios-ecom/internal/platform/postgres"
	"github.com/stephenindia1/steleios-ecom/internal/platform/tenant"
)

// Repository reads and writes the vendor's record of its clients.
//
// Every method but one runs on the PLATFORM path — postgres.DoPlatform or
// ReadPlatform. That is not a convenience: a vendor administrator has no tenant,
// so the tenant path would see nothing, and the system path would see nothing
// either. The platform flag is what makes these tables visible, and migration
// 00020 confines what it reveals to the tables naming which businesses exist.
//
// A consequence worth stating: `grep DoPlatform` over this repository is the
// complete list of queries in the system that can see across clients.
//
// The exception is CreateOwnerLogin, which writes into a client's own `staff`
// table and therefore runs scoped to that shop. See its comment — it is the
// boundary working, not an exception to it.
type Repository interface {
	Register(ctx context.Context, in RegisterInput, by uuid.UUID) (Client, error)
	FindClient(ctx context.Context, id uuid.UUID) (Client, error)
	FindClientByCode(ctx context.Context, code string) (Client, error)
	ListClients(ctx context.Context, limit int, after string) ([]Client, error)

	AddOwner(ctx context.Context, clientID uuid.UUID, in OwnerInput) (Owner, error)
	OwnersOf(ctx context.Context, clientID uuid.UUID) ([]Owner, error)

	ProvisionShop(ctx context.Context, clientID uuid.UUID, in ShopInput) (Shop, error)
	ShopsOf(ctx context.Context, clientID uuid.UUID) ([]Shop, error)

	// CreateOwnerLogin creates the identity, its staff row in the shop and its
	// owner role, in ONE transaction. Split across three it could leave an
	// identity with no membership, which is an account that can sign in and do
	// nothing and which nobody would think to clean up.
	CreateOwnerLogin(ctx context.Context, in OwnerLogin) (uuid.UUID, error)

	// IsRetiredContact reports whether an address or number was retired from a
	// previous account and may never be reused (migration 00010).
	IsRetiredContact(ctx context.Context, email, phone string) (bool, error)

	Confirm(ctx context.Context, clientID, by uuid.UUID, at time.Time) error
}

// OwnerLogin is what the repository needs to create a first user.
//
// Exported because Repository is: an exported interface whose method signatures
// name unexported types cannot be implemented from outside the package, which
// would make the service untestable against a fake and defeat the point of
// declaring the interface at all (OOP-05).
//
// PasswordHash, never a password: the plaintext does not cross this boundary.
type OwnerLogin struct {
	TenantID     tenant.ID
	Email        string
	Phone        string
	FullName     string
	PasswordHash string
	ExpiresAt    time.Time
}

type pgRepository struct {
	pool *postgres.Pool
	uow  postgres.UnitOfWork
}

// NewRepository returns the production repository.
func NewRepository(pool *postgres.Pool, uow postgres.UnitOfWork) Repository {
	return &pgRepository{pool: pool, uow: uow}
}

const clientColumns = `
  id, client_code, legal_name, contact_email, coalesce(contact_phone, ''), status,
  coalesce(gstin, ''), coalesce(tin, ''), coalesce(pan, ''), coalesce(cin, ''),
  coalesce(udyam_number, ''), coalesce(shop_licence_number, ''), gst_registered,
  coalesce(business_type, ''), coalesce(registered_address, ''), coalesce(state_code, ''),
  onboarded_at, confirmed_at, created_at`

func scanClient(row interface{ Scan(...any) error }, c *Client) error {
	return row.Scan(
		&c.ID, &c.ClientCode, &c.LegalName, &c.ContactEmail, &c.ContactPhone, &c.Status,
		&c.GSTIN, &c.TIN, &c.PAN, &c.CIN,
		&c.UdyamNumber, &c.ShopLicenceNumber, &c.GSTRegistered,
		&c.BusinessType, &c.RegisteredAddress, &c.StateCode,
		&c.OnboardedAt, &c.ConfirmedAt, &c.CreatedAt,
	)
}

const registerSQL = `
insert into clients (
  client_code, legal_name, contact_email, contact_phone,
  gstin, tin, pan, cin, udyam_number, shop_licence_number, gst_registered,
  business_type, registered_address, state_code,
  onboarded_at, onboarded_by, status
) values (
  next_client_code(), $1, $2, nullif($3, ''),
  nullif($4, ''), nullif($5, ''), nullif($6, ''), nullif($7, ''), nullif($8, ''), nullif($9, ''), $10,
  nullif($11, ''), nullif($12, ''), nullif($13, ''),
  $14, $15, 'active'
) returning ` + clientColumns

// Register creates a client.
//
// onboarded_at is set here rather than later: the date the vendor took the
// business on is the start of the record the client's accountant will one day
// reconcile against, and a client that exists with no onboarding date is a gap
// in that record (BR-ACP-05).
func (r *pgRepository) Register(ctx context.Context, in RegisterInput, by uuid.UUID) (Client, error) {
	var c Client

	err := r.uow.DoPlatform(ctx, func(rep postgres.Repos) error {
		return scanClient(rep.Querier().QueryRow(ctx, registerSQL,
			in.LegalName, in.ContactEmail, in.ContactPhone,
			in.GSTIN, in.TIN, in.PAN, in.CIN, in.UdyamNumber, in.ShopLicenceNumber,
			in.GSTIN != "",
			in.BusinessType, in.RegisteredAddress, in.StateCode,
			time.Now(), by.String(),
		), &c)
	})
	if err != nil {
		if isUniqueViolation(err) {
			return Client{}, ErrDuplicateIdentifier
		}
		return Client{}, fmt.Errorf("onboarding: register client: %w", err)
	}
	return c, nil
}

// FindClient reads one client.
func (r *pgRepository) FindClient(ctx context.Context, id uuid.UUID) (Client, error) {
	return r.findClient(ctx, `select `+clientColumns+` from clients where id = $1`, id)
}

// FindClientByCode reads one client by its STL-C-nnnnnn code, which is what
// appears on correspondence and is what support is given over the phone.
func (r *pgRepository) FindClientByCode(ctx context.Context, code string) (Client, error) {
	return r.findClient(ctx, `select `+clientColumns+` from clients where client_code = $1`, code)
}

func (r *pgRepository) findClient(ctx context.Context, sql string, arg any) (Client, error) {
	var c Client
	err := r.pool.ReadPlatform(ctx, func(rep postgres.Repos) error {
		return scanClient(rep.Querier().QueryRow(ctx, sql, arg), &c)
	})
	if err != nil {
		if errors.Is(err, postgres.ErrNoRows) {
			return Client{}, ErrNoSuchClient
		}
		return Client{}, fmt.Errorf("onboarding: find client: %w", err)
	}
	return c, nil
}

const listClientsSQL = `
select ` + clientColumns + `
  from clients
 where client_code > $1
 order by client_code
 limit $2`

// ListClients returns a page of clients, ordered and keyset-paginated by the
// client code.
//
// Keyset rather than OFFSET (DB-020, rule 25): the client list only grows, and
// OFFSET makes page 400 read 400 pages' worth of rows to discard them. The code
// is sequential and unique, so it is a natural cursor.
func (r *pgRepository) ListClients(ctx context.Context, limit int, after string) ([]Client, error) {
	out := make([]Client, 0, limit) // DB-024

	err := r.pool.ReadPlatform(ctx, func(rep postgres.Repos) error {
		rows, err := rep.Querier().Query(ctx, listClientsSQL, after, limit)
		if err != nil {
			return err
		}
		defer rows.Close() // DB-044

		for rows.Next() {
			var c Client
			if err := scanClient(rows, &c); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err() // DB-044
	})
	if err != nil {
		return nil, fmt.Errorf("onboarding: list clients: %w", err)
	}
	return out, nil
}

const ownerColumns = `
  id, client_id, full_name, coalesce(pan, ''), address_line1, coalesce(address_line2, ''),
  city, state_code, pincode, coalesce(aadhaar_last4, ''),
  coalesce(email, ''), coalesce(phone, ''), is_primary, created_at`

func scanOwner(row interface{ Scan(...any) error }, o *Owner) error {
	return row.Scan(
		&o.ID, &o.ClientID, &o.FullName, &o.PAN, &o.AddressL1, &o.AddressL2,
		&o.City, &o.StateCode, &o.Pincode, &o.AadhaarLast4,
		&o.Email, &o.Phone, &o.IsPrimary, &o.CreatedAt,
	)
}

const addOwnerSQL = `
insert into client_owners (
  client_id, full_name, pan, address_line1, address_line2,
  city, state_code, pincode, aadhaar_last4, email, phone, is_primary
) values ($1, $2, nullif($3, ''), $4, nullif($5, ''),
          $6, $7, $8, nullif($9, ''), nullif($10, ''), nullif($11, ''), $12)
returning ` + ownerColumns

// AddOwner records one natural person behind the business.
func (r *pgRepository) AddOwner(ctx context.Context, clientID uuid.UUID, in OwnerInput) (Owner, error) {
	var o Owner

	err := r.uow.DoPlatform(ctx, func(rep postgres.Repos) error {
		return scanOwner(rep.Querier().QueryRow(ctx, addOwnerSQL,
			clientID, in.FullName, in.PAN, in.AddressL1, in.AddressL2,
			in.City, in.StateCode, in.Pincode, in.AadhaarLast4,
			in.Email, in.Phone, in.IsPrimary,
		), &o)
	})
	if err != nil {
		return Owner{}, fmt.Errorf("onboarding: add owner: %w", err)
	}
	return o, nil
}

// OwnersOf lists the people behind a business.
func (r *pgRepository) OwnersOf(ctx context.Context, clientID uuid.UUID) ([]Owner, error) {
	var out []Owner

	err := r.pool.ReadPlatform(ctx, func(rep postgres.Repos) error {
		rows, err := rep.Querier().Query(ctx,
			`select `+ownerColumns+` from client_owners where client_id = $1 order by is_primary desc, created_at`,
			clientID)
		if err != nil {
			return err
		}
		defer rows.Close() // DB-044

		for rows.Next() {
			var o Owner
			if err := scanOwner(rows, &o); err != nil {
				return err
			}
			out = append(out, o)
		}
		return rows.Err() // DB-044
	})
	if err != nil {
		return nil, fmt.Errorf("onboarding: owners: %w", err)
	}
	return out, nil
}

const shopColumns = `
  id, client_id, slug, coalesce(shop_code, ''), legal_name,
  coalesce(state_code, ''), coalesce(gstin, ''), status, group_id, created_at`

func scanShop(row interface{ Scan(...any) error }, s *Shop) error {
	var id uuid.UUID
	if err := row.Scan(&id, &s.ClientID, &s.Slug, &s.ShopCode, &s.LegalName,
		&s.StateCode, &s.GSTIN, &s.Status, &s.GroupID, &s.CreatedAt); err != nil {
		return err
	}
	s.TenantID = tenant.ID(id)
	return nil
}

const provisionShopSQL = `
insert into tenants (slug, shop_code, legal_name, client_id, state_code, gstin, group_id, status)
values ($1, $2, $3, $4, nullif($5, ''), nullif($6, ''), $7, 'active')
returning ` + shopColumns

// ProvisionShop creates one tenant.
//
// One shop is one tenant, so a business with two shops gets two — which is what
// stops its staff reaching across them, and is the reason a shop worker's
// row-level security scope is a single value rather than a list.
func (r *pgRepository) ProvisionShop(ctx context.Context, clientID uuid.UUID, in ShopInput) (Shop, error) {
	var s Shop

	err := r.uow.DoPlatform(ctx, func(rep postgres.Repos) error {
		return scanShop(rep.Querier().QueryRow(ctx, provisionShopSQL,
			in.Slug, in.ShopCode, in.LegalName, clientID, in.StateCode, in.GSTIN, in.GroupID), &s)
	})
	if err != nil {
		if isUniqueViolation(err) {
			return Shop{}, ErrDuplicateShop
		}
		return Shop{}, fmt.Errorf("onboarding: provision shop: %w", err)
	}
	return s, nil
}

// ShopsOf lists a client's shops.
func (r *pgRepository) ShopsOf(ctx context.Context, clientID uuid.UUID) ([]Shop, error) {
	var out []Shop

	err := r.pool.ReadPlatform(ctx, func(rep postgres.Repos) error {
		rows, err := rep.Querier().Query(ctx,
			`select `+shopColumns+` from tenants where client_id = $1 order by slug`, clientID)
		if err != nil {
			return err
		}
		defer rows.Close() // DB-044

		for rows.Next() {
			var s Shop
			if err := scanShop(rows, &s); err != nil {
				return err
			}
			out = append(out, s)
		}
		return rows.Err() // DB-044
	})
	if err != nil {
		return nil, fmt.Errorf("onboarding: shops: %w", err)
	}
	return out, nil
}

// CreateOwnerLogin creates the identity, the staff row and the owner role
// atomically.
//
// It runs on the TENANT path, scoped to the shop being provisioned — NOT on the
// platform path like everything else in this repository. That is deliberate and
// it is the boundary working rather than an exception to it:
//
// `staff` is a client's business data. The platform flag deliberately grants
// nothing on it (migration 00020), so a vendor transaction cannot write there,
// and the first attempt at this failed with "new row violates row-level
// security policy for table staff" — correctly. The vendor does not get blanket
// access to every client's employees; instead this one insert is performed IN
// THE SCOPE OF the shop it belongs to, which is what it actually is.
//
// The shop id comes from the caller, which has already verified the shop belongs
// to this client (Handler.IssueFirstUser). Row-level security then confines the
// write to that shop, so even a wrong id could only ever create a staff row in
// the shop it names.
//
// must_change_password is set: the vendor generated this password and has seen
// it, so it must be replaced before the account can do anything else. The locked
// state is the one the identity module already implements — the actor carries no
// roles at all until the password is changed (BR-REC-20).
func (r *pgRepository) CreateOwnerLogin(ctx context.Context, in OwnerLogin) (uuid.UUID, error) {
	var identityID uuid.UUID

	// One transaction for all three writes. Split apart, a failure on the staff
	// insert would leave an identity that can sign in and belongs to nothing —
	// an account nobody would think to clean up.
	ctx = tenant.WithTenant(ctx, in.TenantID)

	err := r.uow.Do(ctx, func(rep postgres.Repos) error {
		q := rep.Querier()

		if err := q.QueryRow(ctx, `
			insert into identities (email, phone, full_name, password_hash, status,
			                        must_change_password, password_expires_at, password_set_at)
			values ($1, nullif($2, ''), $3, $4, 'active', true, $5, now())
			returning id`,
			in.Email, in.Phone, in.FullName, in.PasswordHash, in.ExpiresAt,
		).Scan(&identityID); err != nil {
			return err
		}

		var staffID uuid.UUID
		if err := q.QueryRow(ctx, `
			insert into staff (tenant_id, identity_id, status)
			values ($1, $2, 'active') returning id`,
			in.TenantID.UUID(), identityID,
		).Scan(&staffID); err != nil {
			return err
		}

		// granted_by is the new owner themselves. There is no earlier shop
		// actor to attribute it to — this is the first — and attributing it to
		// the vendor would put a vendor identity in a shop's role history, which
		// is exactly the boundary migration 00019 draws.
		_, err := q.Exec(ctx, `
			insert into staff_role_assignments (staff_id, role_code, granted_by, tenant_id)
			values ($1, 'owner', $1, $2)`, staffID, in.TenantID.UUID())
		return err
	})
	if err != nil {
		if isUniqueViolation(err) {
			return uuid.Nil, ErrEmailInUse
		}
		return uuid.Nil, fmt.Errorf("onboarding: create owner login: %w", err)
	}
	return identityID, nil
}

// IsRetiredContact reports whether an address or number is permanently barred.
//
// A contact retired when an account was blocked may never be used again, by
// anybody (migration 00010). Checked here so the answer is a sentence rather
// than a constraint violation, but the database is what guarantees it.
func (r *pgRepository) IsRetiredContact(ctx context.Context, email, phone string) (bool, error) {
	var n int

	err := r.pool.ReadPlatform(ctx, func(rep postgres.Repos) error {
		return rep.Querier().QueryRow(ctx, `
			select count(*) from retired_contacts
			 where (field = 'email' and value = $1)
			    or ($2 <> '' and field = 'phone' and value = $2)`,
			email, phone).Scan(&n)
	})
	if err != nil {
		return false, fmt.Errorf("onboarding: check retired contacts: %w", err)
	}
	return n > 0, nil
}

// Confirm freezes the business identity.
//
// After this the database trigger refuses every change to the legal name, the
// address, the state, and every government identifier — for anyone, vendor
// included (migration 00012). A different business is a different client.
func (r *pgRepository) Confirm(ctx context.Context, clientID, by uuid.UUID, at time.Time) error {
	err := r.uow.DoPlatform(ctx, func(rep postgres.Repos) error {
		tag, err := rep.Querier().Exec(ctx, `
			update clients set confirmed_at = $2, confirmed_by = $3, updated_at = $2
			 where id = $1 and confirmed_at is null`, clientID, at, by)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			// Either it does not exist or it was already confirmed. The service
			// has read the row first, so it knows which; this is the guard
			// against two administrators confirming at once.
			return ErrAlreadyConfirmed
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrAlreadyConfirmed) {
			return err
		}
		return fmt.Errorf("onboarding: confirm client: %w", err)
	}
	return nil
}

// isUniqueViolation reports whether err is a PostgreSQL unique constraint
// violation.
//
// Matched on the SQLSTATE rather than the message text (GO-024): the message is
// localised and version-dependent, the code is neither.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
