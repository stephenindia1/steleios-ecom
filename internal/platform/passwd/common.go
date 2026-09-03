package passwd

import "strings"

// isCommon reports whether a password is one of the obvious ones.
//
// BR-IDN-01 calls for the top-10k list. That list is a data file to be added
// and embedded; this is the shortlist that catches what people actually type
// when a system tells them to invent a password on the spot, plus the ones a
// staff account gets given by a hurried manager.
//
// Matching is case-insensitive and ignores trailing digits, because Password1,
// password123 and PASSWORD are the same password wearing a hat.
func isCommon(password string) bool {
	normalised := strings.ToLower(strings.TrimSpace(password))
	normalised = strings.TrimRight(normalised, "0123456789!@#$")

	if _, found := commonPasswords[normalised]; found {
		return true
	}
	// A password that is only digits is guessable regardless of length: a
	// date of birth, a phone number, a shop's pincode repeated.
	if normalised == "" && strings.TrimLeft(password, "0123456789") == "" {
		return true
	}
	return false
}

// commonPasswords is the shortlist. Kept as a set rather than a slice so the
// check stays O(1) — it runs on every password change, and a linear scan over
// ten thousand entries on a hot path is exactly the kind of thing that gets
// noticed only under load (DB-001 reasoning, applied off the database).
var commonPasswords = map[string]struct{}{
	"password": {}, "passw0rd": {}, "pass": {}, "passcode": {},
	"welcome": {}, "letmein": {}, "admin": {}, "administrator": {},
	"qwerty": {}, "qwertyuiop": {}, "asdfgh": {}, "zxcvbn": {},
	"iloveyou": {}, "sunshine": {}, "princess": {}, "monkey": {},
	"football": {}, "cricket": {}, "dragon": {}, "master": {},
	"login": {}, "abc": {}, "abcd": {}, "abcdef": {}, "abcdefgh": {},
	"changeme": {}, "temporary": {}, "temp": {}, "default": {},
	"secret": {}, "trustno": {}, "whatever": {}, "freedom": {},

	// India-specific, and genuinely common in this market.
	"bharat": {}, "india": {}, "indian": {}, "namaste": {}, "ganesh": {},
	"krishna": {}, "shivam": {}, "sairam": {}, "jaishreeram": {},
	"mumbai": {}, "delhi": {}, "chennai": {}, "kolkata": {}, "bangalore": {},

	// Shaped like a shop or a product name, which is what an owner reaches for.
	"steleios": {}, "shop": {}, "store": {}, "billing": {}, "counter": {},
	"mystore": {}, "myshop": {}, "business": {},
}
