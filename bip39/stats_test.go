package bip39

import (
	"math"
	"testing"
)

// Self-contained statistics for the distribution soak. The repo carries
// a no-new-dependencies rule, and the one numerics dependency it already
// has (gonum, used by bspline/optimize.go) is not something this package
// should reach for, so the incomplete gamma function lives here.

// chiSquareSF returns P(X > x) for a chi-square variate with df degrees
// of freedom: the p-value of a goodness-of-fit statistic.
func chiSquareSF(x float64, df int) float64 {
	if df <= 0 {
		panic("chiSquareSF: degrees of freedom must be positive")
	}
	if x <= 0 {
		return 1
	}
	return gammaQ(float64(df)/2, x/2)
}

// gammaQ is the regularized upper incomplete gamma function
// Q(a,x) = Γ(a,x)/Γ(a), by series expansion below the transition point
// and by Lentz's continued fraction above it.
func gammaQ(a, x float64) float64 {
	switch {
	case x < 0 || a <= 0:
		panic("gammaQ: domain error")
	case x == 0:
		return 1
	case x < a+1:
		return 1 - gammaSeries(a, x)
	default:
		return gammaContinuedFraction(a, x)
	}
}

// gammaSeries evaluates P(a,x) = 1-Q(a,x) as the series
// e^-x x^a / Γ(a) * Σ x^n Γ(a) / Γ(a+1+n).
func gammaSeries(a, x float64) float64 {
	lg, _ := math.Lgamma(a)
	ap := a
	del := 1 / a
	sum := del
	for range 10000 {
		ap++
		del *= x / ap
		sum += del
		if math.Abs(del) < math.Abs(sum)*1e-16 {
			break
		}
	}
	return sum * math.Exp(-x+a*math.Log(x)-lg)
}

// gammaContinuedFraction evaluates Q(a,x) by the modified Lentz
// algorithm on the continued fraction for Γ(a,x).
func gammaContinuedFraction(a, x float64) float64 {
	const tiny = 1e-300
	lg, _ := math.Lgamma(a)
	b := x + 1 - a
	c := 1 / tiny
	d := 1 / b
	h := d
	for i := 1; i < 10000; i++ {
		an := -float64(i) * (float64(i) - a)
		b += 2
		d = an*d + b
		if math.Abs(d) < tiny {
			d = tiny
		}
		c = b + an/c
		if math.Abs(c) < tiny {
			c = tiny
		}
		d = 1 / d
		del := d * c
		h *= del
		if math.Abs(del-1) < 1e-16 {
			break
		}
	}
	return math.Exp(-x+a*math.Log(x)-lg) * h
}

// normalSF returns P(Z > z) for a standard normal variate.
func normalSF(z float64) float64 { return 0.5 * math.Erfc(z/math.Sqrt2) }

// twoSidedNormalP returns P(|Z| > |z|).
func twoSidedNormalP(z float64) float64 { return math.Erfc(math.Abs(z) / math.Sqrt2) }

// chiSquare returns the goodness-of-fit statistic of counts against a
// uniform expectation, along with the degrees of freedom and the
// smallest expected bin count (the validity condition for the
// chi-square approximation).
func chiSquare(counts []uint64) (stat float64, df int, expected float64) {
	var n uint64
	for _, c := range counts {
		n += c
	}
	expected = float64(n) / float64(len(counts))
	for _, c := range counts {
		d := float64(c) - expected
		stat += d * d / expected
	}
	return stat, len(counts) - 1, expected
}

// mcvMinEntropy is the NIST SP 800-90B most-common-value estimate: the
// min-entropy per symbol implied by a 99% upper confidence bound on the
// most frequent symbol's probability.
func mcvMinEntropy(counts []uint64) (bits float64, pUpper float64) {
	var n, most uint64
	for _, c := range counts {
		n += c
		most = max(most, c)
	}
	if n == 0 {
		return 0, 1
	}
	p := float64(most) / float64(n)
	pUpper = min(1, p+2.576*math.Sqrt(p*(1-p)/float64(n-1)))
	return -math.Log2(pUpper), pUpper
}

// mcvTolerance is how far below the ideal the MCV estimate is expected
// to sit for a *perfect* source, in bits. The estimator takes the
// largest of bins noisy counts and then inflates it to a 99% bound, so
// it always underestimates; the shortfall is the expected maximum of
// the multinomial counts, m + sqrt(2 ln bins) standard deviations, plus
// the confidence margin. Doubling that gives a threshold that shrinks
// with the sample size instead of a magic constant.
func mcvTolerance(bins int, n uint64) float64 {
	b := float64(bins)
	margin := (math.Sqrt(2*math.Log(b)) + 2.576) * math.Sqrt((b-1)/float64(n))
	return math.Log2(1 + 2*margin)
}

func TestMCVTolerance(t *testing.T) {
	// It must shrink towards zero as the sample grows, and stay above
	// the shortfall a uniform sample actually produces.
	prev := math.Inf(1)
	for _, n := range []uint64{1e5, 1e6, 1e7, 1e8} {
		got := mcvTolerance(128, n)
		if got >= prev {
			t.Errorf("mcvTolerance(128, %d) = %.5f, not below %.5f", n, got, prev)
		}
		prev = got
	}
	if got := mcvTolerance(128, 1e7); got < 0.02 || got > 0.12 {
		t.Errorf("mcvTolerance(128, 1e7) = %.5f bits, outside the expected range", got)
	}
}

func TestChiSquareSF(t *testing.T) {
	// The small-df rows are the textbook 5% critical points. The large-df
	// rows, which are the ones this package actually uses, come from an
	// independent Simpson quadrature of the chi-square density (see
	// testdata/chisq_quadrature.py) rather than from this code.
	tests := []struct {
		x    float64
		df   int
		want float64
		tol  float64
	}{
		{3.841458820694124, 1, 0.05, 1e-9},
		{5.991464547107979, 2, 0.05, 1e-9},
		{18.307038053275146, 10, 0.05, 1e-9},
		{14.067140449340167, 7, 0.05, 1e-9},
		{154.30152420585646, 128, 0.0565865675595, 1e-9},
		{100.0, 128, 0.968156558249, 1e-9},
		{128.0, 128, 0.483376012496, 1e-9},
		{2145.965833413664, 2047, 0.0626563090986, 1e-9},
		{2047.0, 2047, 0.495843313798, 1e-9},
		{2200.0, 2047, 0.00952735593308, 1e-9},
		// Q(1,x) = e^-x exactly, so df=2 has a closed form.
		{2, 2, math.Exp(-1), 1e-12},
		{20, 2, math.Exp(-10), 1e-12},
		// The median of a chi-square is near df-2/3.
		{0, 5, 1, 0},
		{1e-9, 1, 0.9999747, 1e-5},
	}
	for _, tc := range tests {
		got := chiSquareSF(tc.x, tc.df)
		if math.Abs(got-tc.want) > tc.tol {
			t.Errorf("chiSquareSF(%g, %d) = %.12g, want %.12g", tc.x, tc.df, got, tc.want)
		}
	}
	// P and Q must sum to one across the range.
	for _, df := range []int{1, 3, 128, 2047} {
		for _, x := range []float64{0.5, 5, 50, 200, 2000, 5000} {
			q := chiSquareSF(x, df)
			p := 1 - gammaQ(float64(df)/2, x/2)
			if math.Abs(q+p-1) > 1e-12 {
				t.Errorf("df=%d x=%g: P+Q = %.15g", df, x, p+q)
			}
		}
	}
	// A large-df statistic equal to its mean sits at the median, just
	// under p=0.5 (Wilson-Hilferty cross-check).
	for _, df := range []int{100, 1000, 2047} {
		p := chiSquareSF(float64(df), df)
		wh := normalSF((math.Pow(1, 1.0/3) - (1 - 2.0/(9*float64(df)))) / math.Sqrt(2.0/(9*float64(df))))
		if math.Abs(p-wh) > 0.01 {
			t.Errorf("df=%d: chiSquareSF=%.6f, Wilson-Hilferty=%.6f", df, p, wh)
		}
	}
}

func TestNormalSF(t *testing.T) {
	tests := []struct{ z, want float64 }{
		{0, 0.5},
		{1, 0.15865525393145707},
		{1.959963984540054, 0.025},
		{2.5758293035489004, 0.005},
		{-1, 0.8413447460685429},
	}
	for _, tc := range tests {
		if got := normalSF(tc.z); math.Abs(got-tc.want) > 1e-12 {
			t.Errorf("normalSF(%g) = %.15g, want %.15g", tc.z, got, tc.want)
		}
	}
}

func TestChiSquareUniform(t *testing.T) {
	counts := make([]uint64, 8)
	for i := range counts {
		counts[i] = 1000
	}
	stat, df, exp := chiSquare(counts)
	if stat != 0 || df != 7 || exp != 1000 {
		t.Errorf("chiSquare(uniform) = %g, %d, %g", stat, df, exp)
	}
	// One bin 10% heavy, one 10% light: the statistic is the sum of two
	// (100)^2/1000 terms.
	counts[0], counts[1] = 1100, 900
	stat, _, _ = chiSquare(counts)
	if want := 20.0; math.Abs(stat-want) > 1e-9 {
		t.Errorf("chiSquare(skewed) = %g, want %g", stat, want)
	}
}

func TestMCVMinEntropy(t *testing.T) {
	// A perfectly flat 128-bin tally over a large sample must land just
	// under log2(128) = 7 bits.
	counts := make([]uint64, 128)
	for i := range counts {
		counts[i] = 1 << 20
	}
	bits, _ := mcvMinEntropy(counts)
	if bits > 7 || bits < 6.99 {
		t.Errorf("mcvMinEntropy(flat) = %.4f bits, want just under 7", bits)
	}
	// A degenerate source has zero min-entropy.
	deg := make([]uint64, 128)
	deg[3] = 1000
	if bits, _ := mcvMinEntropy(deg); bits != 0 {
		t.Errorf("mcvMinEntropy(constant) = %.4f bits, want 0", bits)
	}
}
