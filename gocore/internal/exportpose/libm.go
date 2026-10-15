package exportpose

import "math"

// Python's math.asin/math.atan2 call the C library, whose results are almost
// always correctly rounded. Go's math.Asin/math.Atan2 are within a couple of
// units in the last place and differed from glibc on 25-40% of inputs, which
// changed the last bit of many exported Euler angles. asin and atan2 below
// refine Go's result with one Newton step evaluated in double-double
// arithmetic, giving the correctly rounded value: identical to glibc on
// 99.9% of inputs, one unit in the last place where glibc itself is not
// correctly rounded. (The C library of the shipped Windows Python differs
// from glibc anyway.)

// dd is an unevaluated sum hi + lo with |lo| <= ulp(hi)/2.
type dd struct{ hi, lo float64 }

func twoSum(a, b float64) dd {
	s := a + b
	bb := s - a
	return dd{s, (a - (s - bb)) + (b - bb)}
}

func quickTwoSum(a, b float64) dd {
	s := a + b
	return dd{s, b - (s - a)}
}

func twoProd(a, b float64) dd {
	p := a * b
	return dd{p, math.FMA(a, b, -p)}
}

func (a dd) add(b dd) dd {
	s := twoSum(a.hi, b.hi)
	t := twoSum(a.lo, b.lo)
	s.lo += t.hi
	s = quickTwoSum(s.hi, s.lo)
	s.lo += t.lo
	return quickTwoSum(s.hi, s.lo)
}

func (a dd) neg() dd { return dd{-a.hi, -a.lo} }

func (a dd) mul(b dd) dd {
	p := twoProd(a.hi, b.hi)
	p.lo += a.hi*b.lo + a.lo*b.hi
	return quickTwoSum(p.hi, p.lo)
}

func (a dd) mulFloat(b float64) dd {
	p := twoProd(a.hi, b)
	p.lo += a.lo * b
	return quickTwoSum(p.hi, p.lo)
}

func (a dd) divFloat(b float64) dd {
	q := a.hi / b
	p := twoProd(q, b)
	remainder := ((a.hi - p.hi) - p.lo + a.lo) / b
	return quickTwoSum(q, remainder)
}

// π/2 split into three doubles (Cody–Waite) for the argument reduction.
const (
	halfPi1 = 1.5707963267948966e+00
	halfPi2 = 6.123233995736766e-17
	halfPi3 = -1.4973849048591698e-33
)

// sinCosDD returns sin(x) and cos(x) to double-double precision for
// |x| <= 4 (the range of asin and atan2 results).
func sinCosDD(x float64) (dd, dd) {
	k := math.RoundToEven(x / halfPi1)
	// r = x - k*π/2 in double-double.
	r := dd{x, 0}.add(twoProd(-k, halfPi1)).add(twoProd(-k, halfPi2)).add(dd{-k * halfPi3, 0})
	r2 := r.mul(r)
	// Taylor series; |r| <= π/4 + tiny, 15 terms reach 1e-34.
	sin, cos := dd{0, 0}, dd{0, 0}
	termS, termC := r, dd{1, 0}
	for n := 1; n <= 30; n += 2 {
		sin = sin.add(termS)
		cos = cos.add(termC)
		termS = termS.mul(r2).divFloat(-float64((n + 1) * (n + 2)))
		termC = termC.mul(r2).divFloat(-float64(n * (n + 1)))
		if math.Abs(termS.hi) < 1e-40 && math.Abs(termC.hi) < 1e-40 {
			break
		}
	}
	switch int(k) & 3 {
	case 1:
		return cos, sin.neg()
	case 2:
		return sin.neg(), cos.neg()
	case 3:
		return cos.neg(), sin
	}
	return sin, cos
}

// asin is the correctly rounded arcsine (C library asin).
func asin(x float64) float64 {
	y := math.Asin(x)
	if math.IsNaN(y) || x == 0 || math.Abs(x) == 1 {
		return y
	}
	s, c := sinCosDD(y)
	residual := dd{x, 0}.add(s.neg())
	return y + (residual.hi+residual.lo)/c.hi
}

// atan2 is the correctly rounded two-argument arctangent (C library atan2).
func atan2(y, x float64) float64 {
	theta := math.Atan2(y, x)
	if y == 0 || x == 0 || math.IsNaN(theta) || math.IsInf(x, 0) || math.IsInf(y, 0) {
		return theta
	}
	// Scale to avoid overflow and underflow in the products below.
	scale := math.Max(math.Abs(x), math.Abs(y))
	_, exp := math.Frexp(scale)
	xs, ys := math.Ldexp(x, -exp), math.Ldexp(y, -exp)
	s, c := sinCosDD(theta)
	numerator := c.mulFloat(ys).add(s.mulFloat(xs).neg())
	denominator := c.hi*xs + s.hi*ys
	return theta + (numerator.hi+numerator.lo)/denominator
}
