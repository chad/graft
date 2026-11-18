// Package bench is graft's measurement harness (SPEC.md §10): one bench,
// three arms (graft / baseline / intent), every rate with its 95% Wilson
// interval, append-only results, and failures attributed to a layer.
//
// The discipline is Sedum's, by way of Phoenix's bench: a rate is never
// printed without its interval; overlapping intervals license only "these
// runs do not distinguish these rates", never a verdict; smoke, dirty and
// unstated runs stay visible and are counted in nothing.
package bench

import (
	"fmt"
	"math"
)

// z is the 95% two-sided normal quantile, the only level the harness offers.
const z = 1.959963984540054

// Interval is k successes of n with its 95% Wilson score interval.
// A rate over nothing is absent, not zero: n = 0 has HasRate false and [0, 1].
type Interval struct {
	K, N    int
	HasRate bool
	Rate    float64
	Lo, Hi  float64
}

// Wilson returns the 95% Wilson score interval for k of n.
func Wilson(k, n int) (Interval, error) {
	if k < 0 || n < 0 || k > n {
		return Interval{}, fmt.Errorf("bench: invalid counts k=%d n=%d", k, n)
	}
	if n == 0 {
		return Interval{Lo: 0, Hi: 1}, nil
	}
	p := float64(k) / float64(n)
	nn := float64(n)
	z2 := z * z
	denom := 1 + z2/nn
	centre := (p + z2/(2*nn)) / denom
	half := z * math.Sqrt(p*(1-p)/nn+z2/(4*nn*nn)) / denom
	iv := Interval{K: k, N: n, HasRate: true, Rate: p, Lo: math.Max(0, centre-half), Hi: math.Min(1, centre+half)}
	// Boundaries are exact facts, not floating-point dust.
	if k == 0 {
		iv.Lo = 0
	}
	if k == n {
		iv.Hi = 1
	}
	return iv, nil
}

// String is "34/50 [0.54, 0.79]": the fraction and the interval, always together.
func (iv Interval) String() string {
	if iv.N == 0 {
		return "—/0 (no samples)"
	}
	return fmt.Sprintf("%d/%d [%.2f, %.2f]", iv.K, iv.N, iv.Lo, iv.Hi)
}

// Overlaps is the ONLY comparison the harness makes. Nothing is distinguished
// by nothing, so an empty interval overlaps everything.
func Overlaps(a, b Interval) bool {
	if a.N == 0 || b.N == 0 {
		return true
	}
	return a.Lo <= b.Hi && b.Lo <= a.Hi
}

// Compare is the sentence a comparison is allowed to print.
func Compare(la string, a Interval, lb string, b Interval) string {
	switch {
	case a.N == 0 || b.N == 0:
		return fmt.Sprintf("%s and %s cannot be compared: one has no eligible samples.", la, lb)
	case Overlaps(a, b):
		return fmt.Sprintf("These runs do NOT distinguish %s %s from %s %s.", la, a, lb, b)
	default:
		return fmt.Sprintf("These runs distinguish %s %s from %s %s (intervals disjoint).", la, a, lb, b)
	}
}
