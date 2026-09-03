package uom_test

import (
	"errors"
	"math"
	"testing"

	"github.com/stephenindia1/steleios-ecom/internal/platform/uom"
)

func TestDimensionValid(t *testing.T) {
	t.Parallel()

	for _, d := range []uom.Dimension{uom.Mass, uom.Volume, uom.Length, uom.Area, uom.Count} {
		if err := d.Valid(); err != nil {
			t.Errorf("%s should be valid: %v", d, err)
		}
	}
	for _, d := range []uom.Dimension{"", "weight", "MASS", "temperature"} {
		if err := d.Valid(); !errors.Is(err, uom.ErrUnknownDimension) {
			t.Errorf("Dimension(%q).Valid() = %v, want ErrUnknownDimension", d, err)
		}
	}
}

func TestFactorValidate(t *testing.T) {
	t.Parallel()

	// BR-UOM-03: a factor is a positive integer. Zero and negative factors would
	// make availability arithmetic divide by zero or invert.
	if err := uom.Factor(1).Validate(); err != nil {
		t.Errorf("factor 1 should be valid: %v", err)
	}
	if err := uom.Factor(1000).Validate(); err != nil {
		t.Errorf("factor 1000 should be valid: %v", err)
	}
	for _, f := range []uom.Factor{0, -1, math.MinInt64} {
		if err := uom.Factor(f).Validate(); !errors.Is(err, uom.ErrFactor) {
			t.Errorf("Factor(%d).Validate() = %v, want ErrFactor", f, err)
		}
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		base    int64
		dim     uom.Dimension
		wantErr error
	}{
		{name: "positive mass", base: 250_000, dim: uom.Mass},
		{name: "zero is valid", base: 0, dim: uom.Count},
		{name: "large value", base: math.MaxInt64, dim: uom.Volume},

		{name: "negative refused", base: -1, dim: uom.Mass, wantErr: uom.ErrNegative},
		{name: "unknown dimension refused", base: 1, dim: "weight", wantErr: uom.ErrUnknownDimension},
		{name: "empty dimension refused", base: 1, dim: "", wantErr: uom.ErrUnknownDimension},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			q, err := uom.New(tc.base, tc.dim)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("New error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if q.Base() != tc.base {
				t.Errorf("Base() = %d, want %d", q.Base(), tc.base)
			}
			if q.Dimension() != tc.dim {
				t.Errorf("Dimension() = %s, want %s", q.Dimension(), tc.dim)
			}
		})
	}
}

func TestToBase(t *testing.T) {
	t.Parallel()

	// The worked example from docs/02 §2B: three pack sizes of rice drawing on
	// one pool of grams.
	cases := []struct {
		name      string
		saleUnits int64
		factor    uom.Factor
		want      int64
		wantErr   error
	}{
		{name: "three 500g packs", saleUnits: 3, factor: 500, want: 1500},
		{name: "one 1kg pack", saleUnits: 1, factor: 1000, want: 1000},
		{name: "two 5kg sacks", saleUnits: 2, factor: 5000, want: 10000},
		{name: "eaches pass through", saleUnits: 7, factor: 1, want: 7},
		{name: "zero sale units", saleUnits: 0, factor: 500, want: 0},

		{name: "negative sale units refused", saleUnits: -1, factor: 500, wantErr: uom.ErrNegative},
		{name: "zero factor refused", saleUnits: 1, factor: 0, wantErr: uom.ErrFactor},
		{name: "negative factor refused", saleUnits: 1, factor: -500, wantErr: uom.ErrFactor},
		{name: "overflow refused", saleUnits: math.MaxInt64, factor: 2, wantErr: uom.ErrOverflow},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			q, err := uom.ToBase(tc.saleUnits, tc.factor, uom.Mass)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("ToBase error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if q.Base() != tc.want {
				t.Errorf("ToBase(%d, %d) = %d base units, want %d",
					tc.saleUnits, tc.factor, q.Base(), tc.want)
			}
		})
	}
}

func TestToSaleUnitsFloors(t *testing.T) {
	t.Parallel()

	// BR-UOM-08: availability floors, because a partial pack is not sellable.
	// 247,000 g of rice is 247 one-kilo packs and 494 half-kilo packs, and the
	// 500 g left over after 494 packs is not a 495th pack.
	cases := []struct {
		name   string
		base   int64
		factor uom.Factor
		want   int64
	}{
		{name: "exact multiple", base: 250_000, factor: 1000, want: 250},
		{name: "remainder is dropped", base: 247_300, factor: 1000, want: 247},
		{name: "half-kilo packs", base: 247_300, factor: 500, want: 494},
		{name: "5kg sacks", base: 247_300, factor: 5000, want: 49},
		{name: "less than one unit", base: 400, factor: 500, want: 0},
		{name: "exactly one unit", base: 500, factor: 500, want: 1},
		{name: "one short of a unit", base: 999, factor: 1000, want: 0},
		{name: "zero stock", base: 0, factor: 500, want: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			q := uom.MustNew(tc.base, uom.Mass)
			got, err := q.ToSaleUnits(tc.factor)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("%d base / factor %d = %d sale units, want %d",
					tc.base, tc.factor, got, tc.want)
			}
		})
	}

	if _, err := uom.MustNew(100, uom.Mass).ToSaleUnits(0); !errors.Is(err, uom.ErrFactor) {
		t.Errorf("ToSaleUnits(0) = %v, want ErrFactor", err)
	}
}

func TestArithmeticRejectsDimensionMismatch(t *testing.T) {
	t.Parallel()

	// BR-UOM-04: a mass is not a volume. This is the value-time half of the
	// enforcement; the configuration-time half lives in the catalog service.
	mass := uom.MustNew(1000, uom.Mass)
	volume := uom.MustNew(1000, uom.Volume)

	if _, err := mass.Add(volume); !errors.Is(err, uom.ErrDimensionMismatch) {
		t.Errorf("mass.Add(volume) = %v, want ErrDimensionMismatch", err)
	}
	if _, err := mass.Sub(volume); !errors.Is(err, uom.ErrDimensionMismatch) {
		t.Errorf("mass.Sub(volume) = %v, want ErrDimensionMismatch", err)
	}
	if _, err := mass.LessThan(volume); !errors.Is(err, uom.ErrDimensionMismatch) {
		t.Errorf("mass.LessThan(volume) = %v, want ErrDimensionMismatch", err)
	}
}

func TestArithmetic(t *testing.T) {
	t.Parallel()

	a := uom.MustNew(1500, uom.Mass)
	b := uom.MustNew(500, uom.Mass)

	sum, err := a.Add(b)
	if err != nil || sum.Base() != 2000 {
		t.Errorf("Add = %v, %v; want 2000 mass", sum, err)
	}

	diff, err := a.Sub(b)
	if err != nil || diff.Base() != 1000 {
		t.Errorf("Sub = %v, %v; want 1000 mass", diff, err)
	}

	// Stock can never go negative; a decrement past zero is an error, not a
	// negative quantity (BR-BAT-06).
	if _, err := b.Sub(a); !errors.Is(err, uom.ErrNegative) {
		t.Errorf("500 - 1500 = %v, want ErrNegative", err)
	}

	if _, err := uom.MustNew(math.MaxInt64, uom.Mass).Add(uom.MustNew(1, uom.Mass)); !errors.Is(err, uom.ErrOverflow) {
		t.Errorf("overflow not detected")
	}

	less, err := b.LessThan(a)
	if err != nil || !less {
		t.Errorf("500 < 1500 = %v, %v; want true", less, err)
	}
}

func TestZeroValueCombinesWithAnyDimension(t *testing.T) {
	t.Parallel()

	// A running total must be able to start from the zero value without the
	// caller knowing the dimension in advance.
	var total uom.Quantity
	if !total.IsZero() {
		t.Fatal("zero value should be zero")
	}

	for _, q := range []uom.Quantity{
		uom.MustNew(500, uom.Mass),
		uom.MustNew(1000, uom.Mass),
	} {
		var err error
		if total, err = total.Add(q); err != nil {
			t.Fatalf("accumulate: %v", err)
		}
	}

	if total.Base() != 1500 {
		t.Errorf("total = %d, want 1500", total.Base())
	}
	if total.Dimension() != uom.Mass {
		t.Errorf("total dimension = %s, want mass (adopted from the first operand)", total.Dimension())
	}

	// But once it has a dimension, the mismatch rule applies again.
	if _, err := total.Add(uom.MustNew(1, uom.Volume)); !errors.Is(err, uom.ErrDimensionMismatch) {
		t.Errorf("accumulated total should reject a different dimension, got %v", err)
	}
}

func TestMustNewPanicsOnInvalidInput(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("MustNew with a negative quantity did not panic")
		}
	}()
	_ = uom.MustNew(-1, uom.Mass)
}

func TestRoundTripSaleToBaseAndBack(t *testing.T) {
	t.Parallel()

	// Converting to base and back is lossless for whole sale units — this is
	// the property that lets an order line be reconciled against stock.
	for _, factor := range []uom.Factor{1, 500, 1000, 5000, 999} {
		for _, saleUnits := range []int64{0, 1, 3, 17, 1000} {
			q, err := uom.ToBase(saleUnits, factor, uom.Mass)
			if err != nil {
				t.Fatalf("ToBase(%d, %d): %v", saleUnits, factor, err)
			}
			back, err := q.ToSaleUnits(factor)
			if err != nil {
				t.Fatalf("ToSaleUnits: %v", err)
			}
			if back != saleUnits {
				t.Errorf("factor %d: %d sale units round-tripped to %d", factor, saleUnits, back)
			}
		}
	}
}
