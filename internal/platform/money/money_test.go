package money_test

import (
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/stephenindia1/steleios-ecom/internal/platform/money"
)

// Every rule in this package guards BR-PRC-01, BR-PRC-03 and BR-DSC-13. Each
// test below carries both the passing and the failing case required by GO-091.

func TestParse(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		in      string
		want    money.Paise
		wantErr error
	}{
		{name: "whole rupees", in: "1234", want: 123400},
		{name: "two decimals", in: "1234.56", want: 123456},
		{name: "one decimal is tenths", in: "12.5", want: 1250},
		{name: "zero", in: "0", want: 0},
		{name: "zero with decimals", in: "0.00", want: 0},
		{name: "sub-rupee", in: "0.07", want: 7},
		{name: "negative", in: "-1234.56", want: -123456},
		{name: "negative sub-rupee", in: "-0.05", want: -5},
		{name: "explicit plus", in: "+10.00", want: 1000},
		{name: "leading whitespace", in: "  99.99  ", want: 9999},
		{name: "bare fraction", in: ".50", want: 50},

		// Failing cases: the point is that these are refused, not rounded.
		{name: "three decimals refused", in: "1.005", wantErr: money.ErrParse},
		{name: "empty", in: "", wantErr: money.ErrParse},
		{name: "whitespace only", in: "   ", wantErr: money.ErrParse},
		{name: "letters", in: "12a.00", wantErr: money.ErrParse},
		{name: "non-numeric fraction", in: "12.ab", wantErr: money.ErrParse},
		{name: "two dots", in: "1.2.3", wantErr: money.ErrParse},
		{name: "largest representable rupee amount", in: "92233720368547758", want: money.Paise(9223372036854775800)},
		{name: "rupees overflow", in: "92233720368547759", wantErr: money.ErrOverflow},
		{name: "overflow via the paise component", in: "92233720368547758.08", wantErr: money.ErrOverflow},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := money.Parse(tc.in)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Parse(%q) error = %v, want %v", tc.in, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q) unexpected error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("Parse(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestRoundHalfUp(t *testing.T) {
	t.Parallel()

	// BR-PRC-03: halves round away from zero, symmetrically. The boundary cases
	// are the whole point of having one rounding function.
	cases := []struct {
		name     string
		num, den int64
		want     int64
	}{
		{name: "exact", num: 100, den: 10, want: 10},
		{name: "below half rounds down", num: 104, den: 10, want: 10},
		{name: "exactly half rounds up", num: 105, den: 10, want: 11},
		{name: "above half rounds up", num: 106, den: 10, want: 11},
		{name: "negative below half", num: -104, den: 10, want: -10},
		{name: "negative exactly half rounds away from zero", num: -105, den: 10, want: -11},
		{name: "negative above half", num: -106, den: 10, want: -11},
		{name: "negative denominator normalises", num: 105, den: -10, want: -11},
		{name: "zero numerator", num: 0, den: 7, want: 0},
		{name: "denominator larger than numerator", num: 1, den: 3, want: 0},
		{name: "two thirds rounds up", num: 2, den: 3, want: 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := money.RoundHalfUp(tc.num, tc.den); got != tc.want {
				t.Errorf("RoundHalfUp(%d, %d) = %d, want %d", tc.num, tc.den, got, tc.want)
			}
		})
	}
}

func TestRoundHalfUp_ZeroDenominatorPanics(t *testing.T) {
	t.Parallel()

	// GO-027: a zero denominator is a programmer error, not a business
	// condition, so it panics rather than returning a silently wrong number.
	defer func() {
		if recover() == nil {
			t.Fatal("RoundHalfUp(1, 0) did not panic")
		}
	}()
	_ = money.RoundHalfUp(1, 0)
}

func TestApplyBps(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		amount  money.Paise
		bps     int
		want    money.Paise
		wantErr error
	}{
		{name: "18% GST on ₹100", amount: 10000, bps: 1800, want: 1800},
		{name: "5% GST on ₹100", amount: 10000, bps: 500, want: 500},
		{name: "12% on ₹1.05 rounds", amount: 105, bps: 1200, want: 13}, // 12.6 -> 13
		{name: "rounding lands exactly on half", amount: 250, bps: 200, want: 5},
		{name: "zero rate", amount: 10000, bps: 0, want: 0},
		{name: "zero amount", amount: 0, bps: 1800, want: 0},
		{name: "negative amount keeps sign", amount: -10000, bps: 1800, want: -1800},
		{name: "full 100%", amount: 12345, bps: 10000, want: 12345},

		{name: "negative rate refused", amount: 100, bps: -1, wantErr: money.ErrNegative},
		{name: "overflow refused", amount: money.Paise(math.MaxInt64), bps: 10000, wantErr: money.ErrOverflow},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := tc.amount.ApplyBps(tc.bps)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("ApplyBps error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("Paise(%d).ApplyBps(%d) = %d, want %d", tc.amount, tc.bps, got, tc.want)
			}
		})
	}
}

func TestAddSubOverflow(t *testing.T) {
	t.Parallel()

	max := money.Paise(math.MaxInt64)
	min := money.Paise(math.MinInt64)

	if _, err := max.Add(1); !errors.Is(err, money.ErrOverflow) {
		t.Errorf("max+1 error = %v, want ErrOverflow", err)
	}
	if _, err := min.Add(-1); !errors.Is(err, money.ErrOverflow) {
		t.Errorf("min-1 error = %v, want ErrOverflow", err)
	}
	if got, err := money.Paise(500).Add(250); err != nil || got != 750 {
		t.Errorf("500+250 = %d, %v; want 750, nil", got, err)
	}
	if got, err := money.Paise(500).Sub(750); err != nil || got != -250 {
		t.Errorf("500-750 = %d, %v; want -250, nil", got, err)
	}
	// Adding opposite signs can never overflow.
	if _, err := max.Add(min); err != nil {
		t.Errorf("max+min unexpected error: %v", err)
	}
}

func TestMulQty(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		unit    money.Paise
		qty     int64
		want    money.Paise
		wantErr error
	}{
		{name: "line total", unit: 12550, qty: 3, want: 37650},
		{name: "zero quantity", unit: 12550, qty: 0, want: 0},
		{name: "zero price", unit: 0, qty: 99, want: 0},
		{name: "single unit", unit: 1, qty: 1, want: 1},

		{name: "negative quantity refused", unit: 100, qty: -1, wantErr: money.ErrNegative},
		{name: "overflow refused", unit: money.Paise(math.MaxInt64 / 2), qty: 4, wantErr: money.ErrOverflow},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := tc.unit.MulQty(tc.qty)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("MulQty error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("MulQty = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestAllocateProportionally(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		total   money.Paise
		weights []money.Paise
		want    []money.Paise
		wantErr bool
	}{
		{
			name:    "even split",
			total:   30000,
			weights: []money.Paise{10000, 10000, 10000},
			want:    []money.Paise{10000, 10000, 10000},
		},
		{
			name:    "remainder goes to the largest line",
			total:   100,
			weights: []money.Paise{100, 100, 101},
			// 100*100/301 = 33.22 -> 33 each for the first two; 100*101/301 = 33.55 -> 34.
			// Sum 100, no remainder to move.
			want: []money.Paise{33, 33, 34},
		},
		{
			name:    "indivisible remainder is not lost",
			total:   10,
			weights: []money.Paise{1, 1, 1},
			// 10/3 = 3.33 -> 3 each = 9; the missing 1 goes to index 0.
			want: []money.Paise{4, 3, 3},
		},
		{
			name:    "single line takes everything",
			total:   999,
			weights: []money.Paise{500},
			want:    []money.Paise{999},
		},
		{
			name:    "zero-weight line gets nothing",
			total:   100,
			weights: []money.Paise{0, 100},
			want:    []money.Paise{0, 100},
		},
		{
			name:    "zero total allocates zeros",
			total:   0,
			weights: []money.Paise{10, 20},
			want:    []money.Paise{0, 0},
		},
		{
			name:    "zero total and zero weights is fine",
			total:   0,
			weights: []money.Paise{0, 0},
			want:    []money.Paise{0, 0},
		},

		{name: "no weights refused", total: 100, weights: nil, wantErr: true},
		{name: "negative weight refused", total: 100, weights: []money.Paise{-1, 2}, wantErr: true},
		{name: "amount across zero weight refused", total: 100, weights: []money.Paise{0, 0}, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := money.AllocateProportionally(tc.total, tc.weights)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d parts, want %d", len(got), len(tc.want))
			}

			var sum money.Paise
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("part %d = %d, want %d", i, got[i], tc.want[i])
				}
				sum += got[i]
			}
			// BR-DSC-13: the invariant that actually matters — parts sum exactly
			// to the total, with no paise created or destroyed by rounding.
			if sum != tc.total {
				t.Errorf("parts sum to %d, want exactly %d", sum, tc.total)
			}
		})
	}
}

func TestAllocateProportionally_AlwaysSumsExactly(t *testing.T) {
	t.Parallel()

	// A broader sweep of the BR-DSC-13 invariant across awkward ratios, because
	// the remainder handling is the part most likely to regress.
	totals := []money.Paise{1, 7, 99, 100, 1234, 99999}
	weightSets := [][]money.Paise{
		{1, 1, 1},
		{1, 2, 3},
		{7, 11, 13, 17},
		{1, 999999},
		{333, 333, 334},
		{5},
	}

	for _, total := range totals {
		for _, weights := range weightSets {
			parts, err := money.AllocateProportionally(total, weights)
			if err != nil {
				t.Fatalf("total %d weights %v: %v", total, weights, err)
			}
			var sum money.Paise
			for _, p := range parts {
				if p < 0 {
					t.Errorf("total %d weights %v: negative part %d", total, weights, p)
				}
				sum += p
			}
			if sum != total {
				t.Errorf("total %d weights %v: parts sum to %d", total, weights, sum)
			}
		}
	}
}

func TestSum(t *testing.T) {
	t.Parallel()

	if got, err := money.Sum(); err != nil || got != 0 {
		t.Errorf("Sum() = %d, %v; want 0, nil", got, err)
	}
	if got, err := money.Sum(100, 250, -50); err != nil || got != 300 {
		t.Errorf("Sum(100,250,-50) = %d, %v; want 300, nil", got, err)
	}
	if _, err := money.Sum(money.Paise(math.MaxInt64), 1); !errors.Is(err, money.ErrOverflow) {
		t.Errorf("Sum overflow error = %v, want ErrOverflow", err)
	}
}

func TestFromRupeePaise(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		rupees, paise int64
		want          money.Paise
		wantErr       bool
	}{
		{name: "positive", rupees: 12, paise: 34, want: 1234},
		{name: "zero paise", rupees: 12, paise: 0, want: 1200},
		{name: "negative rupees subtracts paise", rupees: -12, paise: 34, want: -1234},
		{name: "upper boundary", rupees: 1, paise: 99, want: 199},
		{name: "lower boundary", rupees: 1, paise: 0, want: 100},

		{name: "paise above range", rupees: 1, paise: 100, wantErr: true},
		{name: "negative paise", rupees: 1, paise: -1, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := money.FromRupeePaise(tc.rupees, tc.paise)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("= %d, want %d", got, tc.want)
			}
		})
	}
}

func TestComponentsAndString(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in           money.Paise
		rupees, frac int64
		str          string
	}{
		{in: 123456, rupees: 1234, frac: 56, str: "1234.56"},
		{in: 7, rupees: 0, frac: 7, str: "0.07"},
		{in: 0, rupees: 0, frac: 0, str: "0.00"},
		{in: -123456, rupees: -1234, frac: 56, str: "-1234.56"},
		{in: -5, rupees: 0, frac: 5, str: "-0.05"},
		{in: 100, rupees: 1, frac: 0, str: "1.00"},
	}

	for _, tc := range cases {
		t.Run(tc.str, func(t *testing.T) {
			t.Parallel()
			if got := tc.in.Rupees(); got != tc.rupees {
				t.Errorf("Rupees() = %d, want %d", got, tc.rupees)
			}
			if got := tc.in.Fraction(); got != tc.frac {
				t.Errorf("Fraction() = %d, want %d (must always be 0..99)", got, tc.frac)
			}
			if got := tc.in.String(); got != tc.str {
				t.Errorf("String() = %q, want %q", got, tc.str)
			}
		})
	}
}

func TestJSONRoundTrip(t *testing.T) {
	t.Parallel()

	// ADR 0001: money crosses the wire as an integer number of paise, never a
	// float, so a JavaScript client cannot introduce a rounding error.
	type payload struct {
		Total money.Paise `json:"total"`
	}

	b, err := json.Marshal(payload{Total: 123456})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != `{"total":123456}` {
		t.Fatalf("marshalled %s, want an integer paise value", b)
	}

	var back payload
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Total != 123456 {
		t.Errorf("round trip = %d, want 123456", back.Total)
	}

	// A float on the wire is a client bug and must be rejected, not truncated.
	if err := json.Unmarshal([]byte(`{"total":123.45}`), &back); err == nil {
		t.Error("unmarshalling a float into Paise should fail")
	}
}
