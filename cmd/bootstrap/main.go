// Command bootstrap creates the first vendor administrator.
//
// It exists because of a chicken-and-egg: every vendor-side route requires a
// platform role, platform roles are granted by a vendor administrator, and on a
// fresh installation there is none. Something outside HTTP has to make the
// first one.
//
// Deliberately a separate binary rather than a flag on the API, and deliberately
// not a migration:
//
//   - A flag on the API means the API can create administrators, which is a
//     capability it should not have at all. Anything reachable from the request
//     path is reachable by a bug in the request path.
//
//   - A migration means the credential is in version control, identical on every
//     installation, and applied by CI. That is a default password with extra
//     steps.
//
// Running it requires shell access to the deployment and the privileged database
// credential, which is the level of access creating the first administrator
// should need.
//
// It is idempotent in the safe direction: it refuses if a platform user already
// exists, so it cannot be used to quietly add a second administrator to a
// running installation. That path is the vendor console, where it is audited.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stephenindia1/steleios-ecom/internal/platform/config"
	"github.com/stephenindia1/steleios-ecom/internal/platform/passwd"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err) //nolint:forbidigo // GO-080: a CLI reporting to its operator
		os.Exit(1)
	}
}

func run() error {
	email := flag.String("email", "", "email address of the first vendor administrator")
	name := flag.String("name", "", "full name of the first vendor administrator")
	force := flag.Bool("force", false, "add an administrator even though one already exists")
	flag.Parse()

	*email = strings.ToLower(strings.TrimSpace(*email))
	*name = strings.TrimSpace(*name)

	if *email == "" || !strings.Contains(*email, "@") {
		return errors.New("-email is required and must be an email address")
	}
	if *name == "" {
		return errors.New("-name is required")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// The privileged connection, for the same reason migrations use it: this
	// writes rows the application role has no business writing, and running it
	// under the application role would mean the application role CAN create
	// administrators.
	if cfg.Postgres.AdminDSN == "" {
		return errors.New("POSTGRES_ADMIN_DSN is required: bootstrapping runs privileged")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, cfg.Postgres.AdminDSN)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = conn.Close(context.Background()) }() //nolint:errcheck // teardown

	var existing int
	if err := conn.QueryRow(ctx,
		`select count(*) from platform_users where status = 'active'`).Scan(&existing); err != nil {
		return fmt.Errorf("count platform users: %w", err)
	}
	if existing > 0 && !*force {
		return fmt.Errorf("this installation already has %d active vendor administrator(s); "+
			"add more through the vendor console, where it is audited, or pass -force if you are "+
			"recovering from losing them all", existing)
	}

	// Words, not symbols: this is read aloud or typed from a terminal, and it
	// is replaced at first sign-in anyway.
	password, err := passwd.Generate(5)
	if err != nil {
		return fmt.Errorf("generate password: %w", err)
	}
	hash, err := passwd.NewDefault().Hash(password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }() //nolint:errcheck // no-op after commit

	// must_change_password is set, so this account can do exactly one thing
	// until the password is replaced: change it. The identity module already
	// enforces that state by giving the actor no roles at all (BR-REC-20).
	//
	// No expiry, unlike an onboarding password: bootstrapping often happens
	// hours before anyone signs in, and an expired first administrator would
	// mean re-running this with -force, which is the flag that exists to be
	// used rarely.
	var identityID uuid.UUID
	if err := tx.QueryRow(ctx, `
		insert into identities (email, full_name, password_hash, status,
		                        must_change_password, password_set_at)
		values ($1, $2, $3, 'active', true, now())
		returning id`, *email, *name, hash).Scan(&identityID); err != nil {
		return fmt.Errorf("create identity (is the address already in use?): %w", err)
	}

	var userID uuid.UUID
	if err := tx.QueryRow(ctx, `
		insert into platform_users (identity_id, status) values ($1, 'active')
		returning id`, identityID).Scan(&userID); err != nil {
		return fmt.Errorf("create platform user: %w", err)
	}

	// granted_by is the administrator themselves. There is no earlier vendor
	// actor to attribute it to — that is what makes this the bootstrap.
	if _, err := tx.Exec(ctx, `
		insert into platform_role_assignments (platform_user_id, role_code, granted_by)
		values ($1, 'saas_admin', $1)`, userID); err != nil {
		return fmt.Errorf("grant saas_admin: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	// Printed to stdout, not logged. The application logger writes to the same
	// stream in production and its output is shipped and retained; a password
	// must not be (BR-REC-12). This is a one-shot CLI whose entire purpose is to
	// hand a credential to the operator standing in front of it.
	fmt.Printf(`
Vendor administrator created.

  email     %s
  name      %s
  password  %s

This password is shown once and is not stored in recoverable form.
Sign in and change it immediately: the account can do nothing else until
you do.

`, *email, *name, password) //nolint:forbidigo // the point of the command
	return nil
}
