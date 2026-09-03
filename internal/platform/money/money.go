// Package money is the sole representation of currency in Steleios.
//
// All amounts are integer paise (1/100 of an Indian rupee) held in an int64.
// Floating point MUST NOT appear in any pricing, tax, discount, refund, loyalty
// or settlement path (CLAUDE.md rule 10, GO-070, BR-PRC-01).
//
// Rounding happens in exactly one place — [RoundHalfUp] — and is applied per
// line before summing, never to an already-summed total (BR-PRC-03).
//
// This package is the sole implementation of money arithmetic (docs/03 §6.1).
package money

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Paise is an amount in Indian paise. The zero value is zero rupees.
//
// Paise is a defined type rather than a bare int64 so that a raw integer or a
// float cannot be passed where an amount is expected (GO-044).
type Paise int64

// Currency is fixed for launch. It is carried explicitly rather than assumed so
// that a future multi-currency decision (docs/01 §8) is a change of type, not a
// hunt for implicit assumptions.
const Currency = "INR"

// Common amounts.
const (
	Zero Paise = 0
	// MaxPaise bounds a single amount at ₹1,00,00,00,000 (one thousand crore).
	// Anything larger is a data error, not a legitimate order.
	MaxPaise Paise = 100_000_000_000_00
)

// Errors returned by this package.
var (
	ErrNegative = errors.New("money: amount is negative")
	ErrOverflow = errors.New("money: amount overflows the representable range")
	ErrParse    = errors.New("money: cannot parse amount")
)

// FromRupees converts whole rupees to Paise.
func FromRupees(rupees int64) Paise { return Paise(rupees) * 100 }

// FromRupeePaise builds an amount from whole rupees and paise. Paise must be in
// [0,99]; the sign is taken from rupees.
func FromRupeePaise(rupees, paise int64) (Paise, error) {
	if paise < 0 || paise > 99 {
		return 0, fmt.Errorf("%w: paise component %d out of range", ErrParse, paise)
	}
	if rupees < 0 {
		return Paise(rupees*100 - paise), nil
	}
	return Paise(rupees*100 + paise), nil
}

// Parse reads a decimal rupee string such as "1234.50" or "-99" into Paise.
//
// It accepts at most two decimal places and rejects anything else rather than
// rounding: a third decimal place in an input means the caller's data is wrong,
// and silently discarding it is how money goes missing (BR-UOM-03 reasoning).
func Parse(s string) (Paise, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("%w: empty string", ErrParse)
	}

	neg := false
	switch s[0] {
	case '-':
		neg, s = true, s[1:]
	case '+':
		s = s[1:]
	}

	whole, frac, hasFrac := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}

	rupees, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q", ErrParse, s)
	}

	var paise int64
	if hasFrac {
		switch len(frac) {
		case 1:
			frac += "0"
		case 2:
		default:
			return 0, fmt.Errorf("%w: %q has more than two decimal places", ErrParse, s)
		}
		if paise, err = strconv.ParseInt(frac, 10, 64); err != nil {
			return 0, fmt.Errorf("%w: %q", ErrParse, s)
		}
	}

	if rupees > math.MaxInt64/100 {
		return 0, ErrOverflow
	}
	wholePaise := rupees * 100
	if wholePaise > math.MaxInt64-paise {
		return 0, ErrOverflow
	}
	total := Paise(wholePaise + paise)
	if neg {
		total = -total
	}
	return total, nil
}

// Add returns p+q, reporting overflow rather than wrapping.
func (p Paise) Add(q Paise) (Paise, error) {
	sum := p + q
	// Overflow occurred if the operands share a sign that the result does not.
	if (p > 0 && q > 0 && sum < 0) || (p < 0 && q < 0 && sum > 0) {
		return 0, ErrOverflow
	}
	return sum, nil
}

// Sub returns p-q, reporting overflow rather than wrapping.
func (p Paise) Sub(q Paise) (Paise, error) { return p.Add(-q) }

// MulQty multiplies an amount by a whole quantity, as a line total is computed.
//
// Quantity is an int64 because every quantity in Steleios is an integer, in
// base units of measure (BR-UOM-01).
func (p Paise) MulQty(qty int64) (Paise, error) {
	if qty < 0 {
		return 0, fmt.Errorf("%w: quantity %d", ErrNegative, qty)
	}
	if qty == 0 || p == 0 {
		return 0, nil
	}
	product := p * Paise(qty)
	if product/Paise(qty) != p {
		return 0, ErrOverflow
	}
	return product, nil
}

// ApplyBps returns the portion of p described by bps basis points, rounded
// half-up (BR-PRC-03). 1800 bps is 18%.
//
// This is how every GST, cess, discount-percentage and loyalty-earn figure is
// derived. It exists once so those four cannot round differently.
func (p Paise) ApplyBps(bps int) (Paise, error) {
	if bps < 0 {
		return 0, fmt.Errorf("%w: basis points %d", ErrNegative, bps)
	}
	if bps == 0 || p == 0 {
		return 0, nil
	}
	scaled := p * Paise(bps)
	if scaled/Paise(bps) != p {
		return 0, ErrOverflow
	}
	return Paise(RoundHalfUp(int64(scaled), 10_000)), nil
}

// Sum adds amounts left to right, reporting overflow.
//
// Totals are produced by summing already-rounded line amounts; the sum itself is
// never rounded again (BR-PRC-03).
func Sum(amounts ...Paise) (Paise, error) {
	var total Paise
	for i, a := range amounts {
		var err error
		if total, err = total.Add(a); err != nil {
			return 0, fmt.Errorf("summing amount %d: %w", i, err)
		}
	}
	return total, nil
}

// AllocateProportionally splits total across the given weights so that the parts
// sum exactly to total, with the rounding remainder assigned to the largest
// weight (BR-DSC-13).
//
// This is the sole implementation of discount and tax allocation across order
// lines. Allocating independently per line and summing would leave the total
// short or over by a few paise, which is a reconciliation defect.
func AllocateProportionally(total Paise, weights []Paise) ([]Paise, error) {
	if len(weights) == 0 {
		return nil, errors.New("money: no weights to allocate across")
	}

	var weightTotal Paise
	for i, w := range weights {
		if w < 0 {
			return nil, fmt.Errorf("%w: weight %d", ErrNegative, i)
		}
		var err error
		if weightTotal, err = weightTotal.Add(w); err != nil {
			return nil, err
		}
	}

	parts := make([]Paise, len(weights)) // DB-024: size is known
	if weightTotal == 0 {
		// Allocating a non-zero amount across lines that are all worth nothing
		// has no correct answer, and returning zeros would silently lose the
		// amount. Refuse rather than absorb it.
		if total != 0 {
			return nil, fmt.Errorf("money: cannot allocate %s across zero total weight", total)
		}
		return parts, nil
	}

	var allocated Paise
	largest := 0
	for i, w := range weights {
		scaled := int64(total) * int64(w)
		if w != 0 && scaled/int64(w) != int64(total) {
			return nil, ErrOverflow
		}
		parts[i] = Paise(RoundHalfUp(scaled, int64(weightTotal)))
		var err error
		if allocated, err = allocated.Add(parts[i]); err != nil {
			return nil, err
		}
		if w > weights[largest] {
			largest = i
		}
	}

	// The remainder is whatever rounding left over. It goes to the largest line
	// so the parts sum exactly to total.
	parts[largest] += total - allocated
	return parts, nil
}

// RoundHalfUp divides num by den, rounding halves away from zero.
//
// This is the ONLY rounding function in Steleios (docs/03 §6.1, BR-PRC-03).
// Half-up rather than banker's rounding because it is what Indian invoicing
// convention and every finance spreadsheet expects; consistency with the
// customer's arithmetic matters more than statistical neutrality here.
func RoundHalfUp(num, den int64) int64 {
	if den == 0 {
		panic("money: division by zero denominator") // GO-027: programmer error
	}
	if den < 0 {
		num, den = -num, -den
	}
	if num >= 0 {
		return (num + den/2) / den
	}
	return -((-num + den/2) / den)
}

// IsNegative reports whether the amount is below zero.
func (p Paise) IsNegative() bool { return p < 0 }

// Rupees returns the whole-rupee component, truncated toward zero.
func (p Paise) Rupees() int64 { return int64(p) / 100 }

// Fraction returns the paise component, always in [0,99].
func (p Paise) Fraction() int64 {
	f := int64(p) % 100
	if f < 0 {
		f = -f
	}
	return f
}

// String renders the amount as a plain decimal, e.g. "-1234.50".
//
// It is deliberately not localised: this form is for logs, APIs and tests.
// Customer-facing formatting with the ₹ symbol and Indian digit grouping
// belongs in the presentation layer.
func (p Paise) String() string {
	sign := ""
	if p < 0 {
		sign = "-"
	}
	return fmt.Sprintf("%s%d.%02d", sign, abs(p.Rupees()), p.Fraction())
}

// MarshalJSON emits the amount as an integer number of paise.
//
// Amounts cross the wire as integers, never as decimal strings or floats, so a
// JavaScript client cannot introduce a floating-point error (TS-009).
func (p Paise) MarshalJSON() ([]byte, error) {
	return strconv.AppendInt(nil, int64(p), 10), nil
}

// UnmarshalJSON reads an integer number of paise.
func (p *Paise) UnmarshalJSON(b []byte) error {
	v, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrParse, b)
	}
	*p = Paise(v)
	return nil
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
