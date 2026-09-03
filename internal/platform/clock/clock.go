// Package clock is the sole source of time in Steleios.
//
// Domain code MUST NOT call time.Now (GO-072). Time is injected so that expiry,
// reservation windows, effective-dated rates and loyalty accrual are testable
// without sleeping (GO-056) and without waiting for a real date to arrive.
//
// The lint configuration forbids time.Now outside this package.
package clock

import (
	"sync"
	"time"
)

// IST is the business timezone. Storage is always UTC (GO-071); IST is used only
// where a business rule is expressed in local calendar terms — batch expiry
// (BR-BAT-24), return windows (BR-RET-12) and marketing send windows
// (BR-CMP-12).
var IST = time.FixedZone("IST", 5*60*60+30*60)

// Clock reports the current time.
//
// It is an interface with one method so that every consumer can accept the
// narrowest possible dependency (OOP-06).
type Clock interface {
	Now() time.Time
}

// System is the production clock. It returns UTC.
type System struct{}

// Now returns the current UTC time.
//
//nolint:forbidigo // GO-072: this is the one permitted call to time.Now.
func (System) Now() time.Time { return time.Now().UTC() }

// Fake is a controllable clock for tests. The zero value is not usable; build it
// with [NewFake]. It is safe for concurrent use.
type Fake struct {
	mu  sync.RWMutex
	now time.Time
}

// NewFake returns a clock fixed at t.
func NewFake(t time.Time) *Fake { return &Fake{now: t.UTC()} }

// Now returns the fake's current time.
func (f *Fake) Now() time.Time {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.now
}

// Advance moves the fake forward by d.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

// Set moves the fake to t.
func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = t.UTC()
}

// BusinessDate returns the calendar date of t in IST.
//
// Expiry dates, return windows and campaign schedules are dates, not instants: a
// batch expires at the end of its expiry date in IST, wherever the server runs
// (BR-BAT-24).
func BusinessDate(t time.Time) time.Time {
	ist := t.In(IST)
	return time.Date(ist.Year(), ist.Month(), ist.Day(), 0, 0, 0, 0, IST)
}

// EndOfBusinessDay returns the last instant of t's IST calendar date.
//
// Windows expressed in days are inclusive of their final day (BR-RET-12), so a
// comparison against this value is the correct expiry test.
func EndOfBusinessDay(t time.Time) time.Time {
	return BusinessDate(t).AddDate(0, 0, 1).Add(-time.Nanosecond)
}

// DaysBetween returns the number of whole IST calendar days from a to b.
//
// It counts date boundaries crossed, not 24-hour periods, which is what a
// "7 day return window" means to a customer and to a court.
func DaysBetween(a, b time.Time) int {
	return int(BusinessDate(b).Sub(BusinessDate(a)).Hours() / 24)
}
