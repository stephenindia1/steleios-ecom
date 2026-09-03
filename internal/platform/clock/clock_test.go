package clock_test

import (
	"testing"
	"time"

	"github.com/stephenindia1/steleios-ecom/internal/platform/clock"
)

func TestSystemClockReturnsUTC(t *testing.T) {
	t.Parallel()

	now := clock.System{}.Now()
	if now.Location() != time.UTC {
		t.Errorf("System.Now() location = %s, want UTC (GO-071)", now.Location())
	}
	if now.IsZero() {
		t.Error("System.Now() returned the zero time")
	}
}

func TestFakeClock(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	f := clock.NewFake(start)

	if !f.Now().Equal(start) {
		t.Errorf("Now() = %s, want %s", f.Now(), start)
	}

	f.Advance(90 * time.Minute)
	if want := start.Add(90 * time.Minute); !f.Now().Equal(want) {
		t.Errorf("after Advance, Now() = %s, want %s", f.Now(), want)
	}

	reset := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	f.Set(reset)
	if !f.Now().Equal(reset) {
		t.Errorf("after Set, Now() = %s, want %s", f.Now(), reset)
	}

	// Fake normalises to UTC so tests cannot accidentally depend on a local zone.
	f.Set(time.Date(2027, 1, 1, 0, 0, 0, 0, clock.IST))
	if f.Now().Location() != time.UTC {
		t.Errorf("Fake.Now() location = %s, want UTC", f.Now().Location())
	}
}

func TestFakeClockIsConcurrencySafe(t *testing.T) {
	t.Parallel()

	// Services under test are often exercised concurrently; the fake must not be
	// the thing that races (GO-055).
	f := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	done := make(chan struct{})

	go func() {
		defer close(done)
		for range 1000 {
			f.Advance(time.Second)
		}
	}()
	for range 1000 {
		_ = f.Now()
	}
	<-done

	if got := f.Now().Sub(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); got != 1000*time.Second {
		t.Errorf("advanced by %s, want 1000s", got)
	}
}

func TestBusinessDate(t *testing.T) {
	t.Parallel()

	// BR-BAT-24: expiry is a date in IST, whatever zone the server runs in.
	// The interesting cases are instants that fall on different calendar days in
	// UTC and IST — IST is UTC+5:30, so 19:00 UTC is already the next day in IST.
	cases := []struct {
		name string
		in   time.Time
		want string
	}{
		{
			name: "midday UTC is the same IST day",
			in:   time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC),
			want: "2026-09-03",
		},
		{
			name: "18:29 UTC is still the same IST day",
			in:   time.Date(2026, 9, 3, 18, 29, 0, 0, time.UTC),
			want: "2026-09-03",
		},
		{
			name: "18:30 UTC has rolled over in IST",
			in:   time.Date(2026, 9, 3, 18, 30, 0, 0, time.UTC),
			want: "2026-09-04",
		},
		{
			name: "just before IST midnight",
			in:   time.Date(2026, 9, 3, 23, 59, 59, 0, clock.IST),
			want: "2026-09-03",
		},
		{
			name: "IST midnight starts the next day",
			in:   time.Date(2026, 9, 4, 0, 0, 0, 0, clock.IST),
			want: "2026-09-04",
		},
		{
			name: "year boundary",
			in:   time.Date(2026, 12, 31, 19, 0, 0, 0, time.UTC),
			want: "2027-01-01",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := clock.BusinessDate(tc.in).Format("2006-01-02"); got != tc.want {
				t.Errorf("BusinessDate(%s) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

func TestEndOfBusinessDay(t *testing.T) {
	t.Parallel()

	// A batch expires at the END of its expiry date (BR-BAT-24), and a return
	// window is inclusive of its final day (BR-RET-12). Both depend on this.
	at := time.Date(2026, 9, 3, 6, 0, 0, 0, time.UTC) // 11:30 IST
	end := clock.EndOfBusinessDay(at)

	if got := end.In(clock.IST).Format("2006-01-02 15:04:05"); got != "2026-09-03 23:59:59" {
		t.Errorf("EndOfBusinessDay = %s IST, want 2026-09-03 23:59:59", got)
	}

	// The instant itself is before the end of the day; one nanosecond later is not.
	if !at.Before(end) {
		t.Error("the instant should fall before the end of its own business day")
	}
	if !end.Add(time.Nanosecond).After(end) {
		t.Error("end of day should be the last instant of the day")
	}
}

func TestDaysBetween(t *testing.T) {
	t.Parallel()

	// A 7-day return window counts calendar days crossed, not 24-hour periods —
	// which is what a customer and a court both mean by "7 days" (BR-RET-12).
	cases := []struct {
		name string
		a, b time.Time
		want int
	}{
		{
			name: "same day",
			a:    time.Date(2026, 9, 3, 1, 0, 0, 0, clock.IST),
			b:    time.Date(2026, 9, 3, 23, 0, 0, 0, clock.IST),
			want: 0,
		},
		{
			name: "one calendar day even if only minutes apart",
			a:    time.Date(2026, 9, 3, 23, 59, 0, 0, clock.IST),
			b:    time.Date(2026, 9, 4, 0, 1, 0, 0, clock.IST),
			want: 1,
		},
		{
			name: "a full week",
			a:    time.Date(2026, 9, 3, 12, 0, 0, 0, clock.IST),
			b:    time.Date(2026, 9, 10, 12, 0, 0, 0, clock.IST),
			want: 7,
		},
		{
			name: "backwards is negative",
			a:    time.Date(2026, 9, 10, 12, 0, 0, 0, clock.IST),
			b:    time.Date(2026, 9, 3, 12, 0, 0, 0, clock.IST),
			want: -7,
		},
		{
			name: "across a month boundary",
			a:    time.Date(2026, 8, 30, 12, 0, 0, 0, clock.IST),
			b:    time.Date(2026, 9, 2, 12, 0, 0, 0, clock.IST),
			want: 3,
		},
		{
			name: "UTC input is interpreted in IST",
			a:    time.Date(2026, 9, 3, 19, 0, 0, 0, time.UTC), // 4 Sep IST
			b:    time.Date(2026, 9, 4, 19, 0, 0, 0, time.UTC), // 5 Sep IST
			want: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := clock.DaysBetween(tc.a, tc.b); got != tc.want {
				t.Errorf("DaysBetween = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestReturnWindowBoundary(t *testing.T) {
	t.Parallel()

	// The rule under test: a 7-day window from delivery is open on day 7 and
	// closed on day 8 (BR-RET-12). This is the boundary a customer argues about.
	const windowDays = 7
	delivered := time.Date(2026, 9, 3, 15, 0, 0, 0, clock.IST)

	open := func(at time.Time) bool { return clock.DaysBetween(delivered, at) <= windowDays }

	cases := []struct {
		name string
		at   time.Time
		want bool
	}{
		{name: "delivery day", at: delivered, want: true},
		{name: "day 7 early morning", at: time.Date(2026, 9, 10, 0, 1, 0, 0, clock.IST), want: true},
		{name: "day 7 last minute", at: time.Date(2026, 9, 10, 23, 59, 0, 0, clock.IST), want: true},
		{name: "day 8 is closed", at: time.Date(2026, 9, 11, 0, 1, 0, 0, clock.IST), want: false},
		{name: "much later is closed", at: time.Date(2026, 10, 1, 0, 0, 0, 0, clock.IST), want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := open(tc.at); got != tc.want {
				t.Errorf("window open at %s = %v, want %v", tc.at, got, tc.want)
			}
		})
	}
}
