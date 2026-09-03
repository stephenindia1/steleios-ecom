package identity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/stephenindia1/steleios-ecom/internal/platform/authz"
	"github.com/stephenindia1/steleios-ecom/internal/platform/postgres"
	"github.com/stephenindia1/steleios-ecom/internal/platform/tenant"
)

// Repository reads and writes identities and memberships.
//
// Declared as an interface by the service that uses it (OOP-05), so the service
// is testable against a fake without a database.
type Repository interface {
	FindByEmail(ctx context.Context, email string) (Identity, error)
	FindByID(ctx context.Context, id uuid.UUID) (Identity, error)
	MembershipsOf(ctx context.Context, id uuid.UUID) ([]Membership, error)
	MembershipIn(ctx context.Context, id uuid.UUID, t tenant.ID) (Membership, error)

	// PlatformRolesOf returns the vendor-side roles of an identity, or nothing
	// if it is not vendor staff. Separate from MembershipsOf because the two
	// worlds are disjoint (BR-ADM-14) and a single call returning both would be
	// the first step towards merging them.
	PlatformRolesOf(ctx context.Context, id uuid.UUID) ([]authz.Role, error)

	RecordFailedLogin(ctx context.Context, id uuid.UUID, lockUntil *time.Time) error
	RecordSuccessfulLogin(ctx context.Context, id uuid.UUID, at time.Time) error
	UpdatePassword(ctx context.Context, id uuid.UUID, hash string, at time.Time) error
}

// pgRepository is the PostgreSQL implementation.
type pgRepository struct {
	pool *postgres.Pool
	uow  postgres.UnitOfWork
}

// NewRepository returns the production repository.
func NewRepository(pool *postgres.Pool, uow postgres.UnitOfWork) Repository {
	return &pgRepository{pool: pool, uow: uow}
}

const identityColumns = `
  id, email, coalesce(phone, ''), full_name, status, password_hash,
  must_change_password, password_expires_at,
  failed_logins, locked_until, last_login_at, last_reauth_at`

// FindByEmail looks an identity up for sign-in.
//
// Runs on the SYSTEM path: authentication precedes tenancy, so there is no shop
// to scope to yet, and identities are deliberately not tenant-scoped
// (migration 00016, ADR 0007).
func (r *pgRepository) FindByEmail(ctx context.Context, email string) (Identity, error) {
	var i Identity
	err := r.pool.ReadSystem(ctx, func(rep postgres.Repos) error {
		return scanIdentity(rep.Querier().QueryRow(ctx,
			`select `+identityColumns+` from identities where email = $1`, email), &i)
	})
	if err != nil {
		if errors.Is(err, postgres.ErrNoRows) {
			return Identity{}, ErrNoSuchUser
		}
		return Identity{}, fmt.Errorf("identity: find by email: %w", err)
	}
	return i, nil
}

// FindByID looks an identity up by its permanent id.
func (r *pgRepository) FindByID(ctx context.Context, id uuid.UUID) (Identity, error) {
	var i Identity
	err := r.pool.ReadSystem(ctx, func(rep postgres.Repos) error {
		return scanIdentity(rep.Querier().QueryRow(ctx,
			`select `+identityColumns+` from identities where id = $1`, id), &i)
	})
	if err != nil {
		if errors.Is(err, postgres.ErrNoRows) {
			return Identity{}, ErrNoSuchUser
		}
		return Identity{}, fmt.Errorf("identity: find by id: %w", err)
	}
	return i, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanIdentity(row rowScanner, i *Identity) error {
	return row.Scan(
		&i.ID, &i.Email, &i.Phone, &i.FullName, &i.Status, &i.PasswordHash,
		&i.MustChangePassword, &i.PasswordExpiresAt,
		&i.FailedLogins, &i.LockedUntil, &i.LastLoginAt, &i.LastReauthAt,
	)
}

// membershipsSQL calls the one function permitted to answer this question.
//
// The join it replaces returned nothing, always: staff, tenants, clients and
// staff_role_assignments are all scoped by current_tenant_id(), which is NULL
// here because choosing a shop is what this query is FOR. Migration 00017
// explains the fix and why the bypass is a single narrow function rather than
// four widened policies.
const membershipsSQL = `select * from memberships_of_identity($1)`

// MembershipsOf returns every shop the identity belongs to.
//
// Also on the system path, and for the same reason: this is what the shop
// switcher is built from, so it must work before a shop is chosen. It reads
// only rows joined to this identity, so it cannot enumerate anything else.
func (r *pgRepository) MembershipsOf(ctx context.Context, id uuid.UUID) ([]Membership, error) {
	var out []Membership

	err := r.pool.ReadSystem(ctx, func(rep postgres.Repos) error {
		rows, err := rep.Querier().Query(ctx, membershipsSQL, id)
		if err != nil {
			return err
		}
		defer rows.Close() // DB-044

		for rows.Next() {
			var m Membership
			var tenantID uuid.UUID
			var roles []string

			if err := rows.Scan(&m.StaffID, &tenantID, &m.ShopCode, &m.ShopName,
				&m.ClientID, &m.ClientCode, &m.Status, &roles); err != nil {
				return err
			}
			m.TenantID = tenant.ID(tenantID)
			m.Roles = make([]authz.Role, 0, len(roles)) // DB-024
			for _, role := range roles {
				m.Roles = append(m.Roles, authz.Role(role))
			}
			out = append(out, m)
		}
		return rows.Err() // DB-044
	})
	if err != nil {
		return nil, fmt.Errorf("identity: memberships: %w", err)
	}
	return out, nil
}

// MembershipIn returns the identity's membership of one shop.
//
// The check behind shop selection: a person may only bind their session to a
// shop they actually belong to (BR-STO-39 reasoning applied to shops).
func (r *pgRepository) MembershipIn(ctx context.Context, id uuid.UUID, t tenant.ID) (Membership, error) {
	all, err := r.MembershipsOf(ctx, id)
	if err != nil {
		return Membership{}, err
	}
	for _, m := range all {
		if m.TenantID == t && m.IsActive() {
			return m, nil
		}
	}
	return Membership{}, ErrNotAMember
}

const platformRolesSQL = `
select coalesce(array_agg(a.role_code) filter (where a.role_code is not null), '{}')
  from platform_users p
  left join platform_role_assignments a on a.platform_user_id = p.id
 where p.identity_id = $1 and p.status = 'active'
 group by p.id`

// PlatformRolesOf returns the vendor-side roles of an identity.
//
// On the system path, and legitimately so: a platform user has no tenant at all,
// which is the whole point of migration 00019. platform_users carries no
// row-level security for the same reason, so unlike the membership lookup this
// needs no bypass — there is nothing to bypass.
//
// An identity that is not vendor staff returns no rows, which is not an error:
// almost every identity in the system is a shop worker.
func (r *pgRepository) PlatformRolesOf(ctx context.Context, id uuid.UUID) ([]authz.Role, error) {
	var codes []string

	err := r.pool.ReadSystem(ctx, func(rep postgres.Repos) error {
		return rep.Querier().QueryRow(ctx, platformRolesSQL, id).Scan(&codes)
	})
	if err != nil {
		if errors.Is(err, postgres.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("identity: platform roles: %w", err)
	}

	roles := make([]authz.Role, 0, len(codes)) // DB-024
	for _, c := range codes {
		roles = append(roles, authz.Role(c))
	}
	return roles, nil
}

// RecordFailedLogin increments the counter and applies a lockout.
//
// Lockout is temporary and time-bounded. A permanent one would be a denial of
// service anyone could trigger against anyone by guessing wrong on purpose
// (BR-IDN-11).
func (r *pgRepository) RecordFailedLogin(ctx context.Context, id uuid.UUID, lockUntil *time.Time) error {
	err := r.uow.DoSystem(ctx, func(rep postgres.Repos) error {
		_, err := rep.Querier().Exec(ctx,
			`update identities
			    set failed_logins = failed_logins + 1,
			        locked_until  = coalesce($2, locked_until)
			  where id = $1`, id, lockUntil)
		return err
	})
	if err != nil {
		return fmt.Errorf("identity: record failed login: %w", err)
	}
	return nil
}

// RecordSuccessfulLogin clears the failure counters.
func (r *pgRepository) RecordSuccessfulLogin(ctx context.Context, id uuid.UUID, at time.Time) error {
	err := r.uow.DoSystem(ctx, func(rep postgres.Repos) error {
		_, err := rep.Querier().Exec(ctx,
			`update identities
			    set failed_logins = 0, locked_until = null, last_login_at = $2
			  where id = $1`, id, at)
		return err
	})
	if err != nil {
		return fmt.Errorf("identity: record login: %w", err)
	}
	return nil
}

// UpdatePassword sets a new hash and clears the recovery lock.
//
// Clearing must_change_password here rather than in a separate statement is
// deliberate: the lock exists to force exactly this action, so completing the
// action and releasing the lock must be one atomic change. Two statements
// could leave an account locked with a password its owner already changed.
func (r *pgRepository) UpdatePassword(ctx context.Context, id uuid.UUID, hash string, at time.Time) error {
	err := r.uow.DoSystem(ctx, func(rep postgres.Repos) error {
		_, err := rep.Querier().Exec(ctx,
			`update identities
			    set password_hash        = $2,
			        password_set_at      = $3,
			        must_change_password = false,
			        password_expires_at  = null,
			        failed_logins        = 0,
			        locked_until         = null
			  where id = $1`, id, hash, at)
		return err
	})
	if err != nil {
		return fmt.Errorf("identity: update password: %w", err)
	}
	return nil
}
