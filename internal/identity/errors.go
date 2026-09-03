package identity

import "errors"

// Errors this module returns.
//
// A note on how these reach a client, because it is the whole point of having
// several of them: the HANDLER collapses every authentication failure into one
// generic message (BR-IDN-06). ErrNoSuchUser, ErrBadPassword and ErrNotActive
// must be indistinguishable from outside, or the login form becomes an oracle
// for which email addresses have accounts.
//
// They are distinct HERE so that the service can log the real reason, count the
// right thing, and decide whether to increment a lockout counter — none of
// which is possible if the only signal is "login failed".
var (
	// ErrNoSuchUser means no identity matched the email.
	ErrNoSuchUser = errors.New("identity: no such user")
	// ErrBadPassword means the identity exists and the password is wrong.
	ErrBadPassword = errors.New("identity: password does not match")
	// ErrNotActive means the identity is suspended or disabled.
	ErrNotActive = errors.New("identity: not active")
	// ErrBlocked means the identity is permanently blocked. It is never
	// reactivated; the person gets a new user (migration 00011).
	ErrBlocked = errors.New("identity: blocked")
	// ErrLockedOut means too many failed attempts. Temporary by design —
	// permanent lockout would be a denial-of-service anyone could trigger
	// against anyone (BR-IDN-11).
	ErrLockedOut = errors.New("identity: temporarily locked out")

	// ErrMustChangePassword means the account is in the locked state that
	// follows a vendor-issued password. It may change its password and nothing
	// else (BR-REC-20).
	ErrMustChangePassword = errors.New("identity: password must be changed before continuing")
	// ErrPasswordExpired means a generated password was not used in time
	// (BR-REC-24).
	ErrPasswordExpired = errors.New("identity: generated password has expired")

	// ErrNoMembership means the identity belongs to no active shop. A person
	// with a valid password and no membership can authenticate but can do
	// nothing, which is correct: membership is what grants access.
	ErrNoMembership = errors.New("identity: no active shop membership")
	// ErrNotAMember means the identity asked for a shop it does not belong to.
	ErrNotAMember = errors.New("identity: not a member of that shop")

	// ErrSamePassword means a new password equals the current one. Relevant
	// during recovery, where the generated password must actually be replaced
	// (BR-REC-23).
	ErrSamePassword = errors.New("identity: the new password must be different")
)

// IsAuthFailure reports whether an error is one of the several ways a sign-in
// can fail that MUST look identical to the client.
//
// Having this as a function rather than a comment means the handler cannot
// accidentally let one of them through with a distinguishing message — the
// list is in one place and adding a new failure mode is a change here.
func IsAuthFailure(err error) bool {
	return errors.Is(err, ErrNoSuchUser) ||
		errors.Is(err, ErrBadPassword) ||
		errors.Is(err, ErrNotActive) ||
		errors.Is(err, ErrBlocked) ||
		errors.Is(err, ErrLockedOut)
}
