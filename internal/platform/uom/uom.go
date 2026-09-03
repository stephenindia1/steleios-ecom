// Package uom is the sole implementation of unit-of-measure conversion
// (docs/03 §6.1, BR-UOM-20).
//
// Every quantity in Steleios is an integer in a product's base unit
// (BR-UOM-01). Stock is held in base units; customers buy in sale units; a
// supplier ships in purchase units. The conversion between them is an integer
// factor, and a conversion that would not be exact is a configuration error
// rather than a rounding (BR-UOM-03).
//
// Inline multiplication by a conversion factor elsewhere in the codebase is
// prohibited — it is how dimension and rounding errors enter.
package uom

import (
	"errors"
	"fmt"
)

// Dimension is the physical quantity a unit measures. Conversion between
// different dimensions is impossible: a mass is not a volume (BR-UOM-04).
type Dimension string

// The dimensions Steleios supports.
const (
	Mass   Dimension = "mass"
	Volume Dimension = "volume"
	Length Dimension = "length"
	Area   Dimension = "area"
	Count  Dimension = "count"
)

// Valid reports whether d is a known dimension.
func (d Dimension) Valid() error {
	switch d {
	case Mass, Volume, Length, Area, Count:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrUnknownDimension, string(d))
	}
}

// Errors returned by this package.
var (
	ErrDimensionMismatch = errors.New("uom: dimension mismatch")
	ErrUnknownDimension  = errors.New("uom: unknown dimension")
	ErrNegative          = errors.New("uom: negative quantity")
	ErrFactor            = errors.New("uom: invalid conversion factor")
	ErrOverflow          = errors.New("uom: quantity overflows the representable range")
)

// Unit is a unit of measure. Units are reference data, seeded by migration and
// not editable at runtime (BR-UOM-19).
type Unit struct {
	// Code identifies the unit, e.g. "GRAM", "KG", "PCS".
	Code string
	// Dimension is what the unit measures.
	Dimension Dimension
	// UQC is the GST Unique Quantity Code that must appear on an invoice line
	// (BR-UOM-11).
	UQC string
	// Symbol is the display form, e.g. "g", "kg", "pc".
	Symbol string
}

// Factor is the number of base units in one sale or purchase unit. It is always
// a positive integer (BR-UOM-03).
type Factor int64

// Validate reports whether f is usable as a conversion factor.
func (f Factor) Validate() error {
	if f <= 0 {
		return fmt.Errorf("%w: factor %d must be positive", ErrFactor, int64(f))
	}
	return nil
}

// Quantity is an amount of something, held as an integer count of base units
// together with the dimension it measures.
//
// The dimension travels with the value so that adding a mass to a volume is
// caught. Note that dimension checking is a runtime error rather than a compile
// error: a product's base unit is reference data read from the database, so its
// dimension is not known when the code is compiled. Configuration-time
// validation (a sale unit's dimension must equal its product's base unit
// dimension) is the primary defence; this type is the second (BR-UOM-04).
//
// The zero Quantity is zero of no dimension and combines with anything.
type Quantity struct {
	base int64
	dim  Dimension
}

// New builds a quantity of base units. Negative quantities are rejected: stock
// and order quantities are never negative, and a decrement is expressed as an
// operation, not as a negative amount.
func New(baseUnits int64, dim Dimension) (Quantity, error) {
	if baseUnits < 0 {
		return Quantity{}, fmt.Errorf("%w: %d", ErrNegative, baseUnits)
	}
	if err := dim.Valid(); err != nil {
		return Quantity{}, err
	}
	return Quantity{base: baseUnits, dim: dim}, nil
}

// MustNew is New for reference data and tests, where the inputs are known good.
// It panics on invalid input, which is a programmer error (GO-027).
func MustNew(baseUnits int64, dim Dimension) Quantity {
	q, err := New(baseUnits, dim)
	if err != nil {
		panic(err)
	}
	return q
}

// Base returns the quantity as an integer count of base units. This is the value
// that is stored and that all stock arithmetic uses (BR-UOM-07).
func (q Quantity) Base() int64 { return q.base }

// Dimension returns what the quantity measures.
func (q Quantity) Dimension() Dimension { return q.dim }

// IsZero reports whether the quantity is zero.
func (q Quantity) IsZero() bool { return q.base == 0 }

// ToBase converts a quantity expressed in sale (or purchase) units into base
// units (BR-UOM-07).
//
// Reserving a cart line converts to base BEFORE the atomic reservation update,
// so that every pack size of a product draws on one pool of base units.
func ToBase(saleUnits int64, factor Factor, dim Dimension) (Quantity, error) {
	if saleUnits < 0 {
		return Quantity{}, fmt.Errorf("%w: %d sale units", ErrNegative, saleUnits)
	}
	if err := factor.Validate(); err != nil {
		return Quantity{}, err
	}
	if saleUnits != 0 {
		product := saleUnits * int64(factor)
		if product/saleUnits != int64(factor) {
			return Quantity{}, ErrOverflow
		}
		return New(product, dim)
	}
	return New(0, dim)
}

// ToSaleUnits converts base units back to whole sale units, rounding down.
//
// The floor is deliberate: a partial pack is not sellable, so availability shown
// to a customer is always the number of complete units that can be fulfilled
// (BR-UOM-08).
func (q Quantity) ToSaleUnits(factor Factor) (int64, error) {
	if err := factor.Validate(); err != nil {
		return 0, err
	}
	return q.base / int64(factor), nil
}

// Add returns q+o, rejecting a dimension mismatch.
func (q Quantity) Add(o Quantity) (Quantity, error) {
	dim, err := q.combine(o)
	if err != nil {
		return Quantity{}, err
	}
	sum := q.base + o.base
	if sum < q.base {
		return Quantity{}, ErrOverflow
	}
	return Quantity{base: sum, dim: dim}, nil
}

// Sub returns q-o, rejecting a dimension mismatch and a negative result.
func (q Quantity) Sub(o Quantity) (Quantity, error) {
	dim, err := q.combine(o)
	if err != nil {
		return Quantity{}, err
	}
	if o.base > q.base {
		return Quantity{}, fmt.Errorf("%w: %d - %d", ErrNegative, q.base, o.base)
	}
	return Quantity{base: q.base - o.base, dim: dim}, nil
}

// LessThan reports whether q is smaller than o. A dimension mismatch is an
// error, not a false comparison.
func (q Quantity) LessThan(o Quantity) (bool, error) {
	if _, err := q.combine(o); err != nil {
		return false, err
	}
	return q.base < o.base, nil
}

// combine validates that two quantities may be compared or combined and returns
// the resulting dimension. The zero Quantity has no dimension and adopts the
// other operand's, so that a running total can start from the zero value.
func (q Quantity) combine(o Quantity) (Dimension, error) {
	switch {
	case q.dim == o.dim:
		return q.dim, nil
	case q.dim == "":
		return o.dim, nil
	case o.dim == "":
		return q.dim, nil
	default:
		return "", fmt.Errorf("%w: %s and %s", ErrDimensionMismatch, q.dim, o.dim)
	}
}

// String renders the quantity for logs and errors.
func (q Quantity) String() string {
	if q.dim == "" {
		return fmt.Sprintf("%d", q.base)
	}
	return fmt.Sprintf("%d %s", q.base, q.dim)
}
