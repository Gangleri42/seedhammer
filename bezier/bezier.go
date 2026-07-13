package bezier

import (
	"math"
)

type Cubic struct {
	C0, C1, C2, C3 Point
}

type cubic16 struct {
	C0, C1, C2, C3 point16
}

// Interpolator interpolates a [Cubic] curve.
type Interpolator struct {
	int fraction
	c   cubic16
	// off tracks the offset from the current
	// curve segment to c, which is offset to
	// fill out its limited precision.
	off Point

	// The remaining part of the segment.
	rem struct {
		c     Cubic
		steps uint
	}
}

const (
	logOne = 16
	// 1.0 in fixed-point for [Bezier.Interpolate16].
	one = 1 << logOne
)

// fraction implements integer interpolation of the interval ]0;1].
type fraction struct {
	// lim is the interval end.
	lim uint32
	// n is the number of ticks.
	n uint32
	// 2*d/m2 is the fractional step rate.
	d, m2 uint32
	// frac2 accumulates the fractional steps,
	// multiplied by 2.
	frac2 uint32
	// p1 is 1-p, where p is the interpolation parameter.
	p1 uint32
}

// Construct a new fraction that steps through ]0;n] in d steps.
func newFraction(n, d uint32) fraction {
	f := fraction{
		lim: n,
		n:   d,
		// Round to nearest step by initializing the fraction
		// to 1/2*steps (=> 2*frac = steps).
		frac2: d,
	}
	if f.n > 0 {
		f.p1 = n
		f.d = n / d
		f.m2 = (n % d) * 2
	}
	return f
}

func (f *fraction) Value() uint32 {
	return f.lim - f.p1
}

func (f *fraction) Step() bool {
	if f.p1 == 0 {
		return false
	}
	f.p1 -= f.d
	f.frac2 += f.m2
	if n2 := 2 * f.n; f.frac2 >= n2 {
		f.frac2 -= n2
		f.p1--
	}
	return true
}

func (in *Interpolator) Segment(c Cubic, steps uint) {
	// Track discontinuities.
	end := in.c.C3.P().Add(in.off)
	diff := c.C0.Sub(end)
	in.off = in.off.Add(diff)
	in.rem.c, in.rem.steps = c, steps
}

// Step the interpolator or return false if there are
// no more steps.
func (in *Interpolator) Step() bool {
	for !in.int.Step() {
		if in.rem.steps == 0 {
			return false
		}
		in.advance()
	}
	return true
}

// Position of the bézier interpolation.
func (in *Interpolator) Position() Point {
	p := in.int.Value()
	return in.c.Interpolate16(p).P().Add(in.off)
}

func (in *Interpolator) advance() {
	c, steps := in.rem.c, in.rem.steps
	if steps == 0 {
		panic("overstepping")
	}
	c1, c2, t1 := splitBezier16(c, steps)
	t2 := steps - t1
	in.rem.c = c2
	in.rem.steps = t2
	// off is the offset caused by centering of the
	// 16-bit segment c16.
	off := in.c.C3.P().Sub(c1.C0.P())
	in.off = in.off.Add(off)
	in.c = c1
	in.int = newFraction(one, uint32(t1))
}

// Split a part off c that fit in a [bezier16].
func splitBezier16(c Cubic, ticks uint) (cubic16, Cubic, uint) {
	// Firt, compute the split that makes the duration
	// fit 16 bits.
	splitDiv := (ticks + one - 1) / one
	// Then split until the bezier control points fit.
	for {
		d := uint32(splitDiv)
		p := (one + d - 1) / d
		c1, c2 := c.split16(p)
		// Center the bezier to maximize use of the available
		// precision.
		if c16, ok := clampBezier16(centerBezier(c1)); ok {
			return c16, c2, ticks / splitDiv
		}
		splitDiv *= 2
	}
}

func centerBezier(c Cubic) Cubic {
	b := Bounds{
		Min: Point{
			X: min(c.C0.X, c.C1.X, c.C2.X, c.C3.X),
			Y: min(c.C0.Y, c.C1.Y, c.C2.Y, c.C3.Y),
		},
		Max: Point{
			X: max(c.C0.X, c.C1.X, c.C2.X, c.C3.X),
			Y: max(c.C0.Y, c.C1.Y, c.C2.Y, c.C3.Y),
		},
	}
	center := b.Max.Add(b.Min).Div(2)
	return c.Sub(center)
}

func clampBezier16(c Cubic) (cubic16, bool) {
	c16 := cubic16{
		C0: clampP16(c.C0),
		C1: clampP16(c.C1),
		C2: clampP16(c.C2),
		C3: clampP16(c.C3),
	}
	if c16.B() != c {
		return cubic16{}, false
	}
	return c16, true
}

func clampP16(p Point) point16 {
	return point16{
		X: clamp16(p.X),
		Y: clamp16(p.Y),
	}
}

func clamp16(v int) int16 {
	return int16(min(max(v, math.MinInt16), math.MaxInt16))
}

func (b *cubic16) B() Cubic {
	return Cubic{
		C0: b.C0.P(),
		C1: b.C1.P(),
		C2: b.C2.P(),
		C3: b.C3.P(),
	}
}

func (p point16) P() Point {
	return Point{
		X: int(p.X),
		Y: int(p.Y),
	}
}

// Sample the curve at p. p must be in the interval [0;one].
// The result is exact.
func (s *cubic16) Interpolate16(p16 uint32) point16 {
	// p fit in 16 bits, except for the extremes.
	switch {
	case p16 == 0:
		return s.C0
	case p16 == one:
		return s.C3
	}
	p := uint16(p16)
	p1 := uint16(one - p16)
	q0 := interpolate16(p, p1, s.C0, s.C1)
	q1 := interpolate16(p, p1, s.C1, s.C2)
	q2 := interpolate16(p, p1, s.C2, s.C3)
	r0 := interpolate32(p, p1, q0, q1)
	r1 := interpolate32(p, p1, q1, q2)
	x := interpolate64(p, p1, r0, r1)

	const (
		prec     = 3 * logOne
		rounding = 1 << (prec - 1)
	)
	return point16{
		X: int16((x.X + rounding) >> prec),
		Y: int16((x.Y + rounding) >> prec),
	}
}

// split16 splits a bezier at p16, in the iterval [0;one].
func (s *Cubic) split16(p16 uint32) (Cubic, Cubic) {
	switch {
	case p16 == 0:
		return Cubic{}, *s
	case p16 == one:
		return *s, Cubic{}
	}
	p := uint16(p16)
	p1 := uint16(one - p16)
	q0 := interpolate32(p, p1, s.C0, s.C1)
	q1 := interpolate32(p, p1, s.C1, s.C2)
	q2 := interpolate32(p, p1, s.C2, s.C3)
	r064 := interpolate64(p, p1, q0, q1)
	r164 := interpolate64(p, p1, q1, q2)
	// We're out of bits - round and shift down once.
	r0 := roundP64(r064)
	r1 := roundP64(r164)
	// Round twice.
	x64 := interpolate64(p, p1, r0, r1)
	x := Point{
		X: int((x64.X + 1<<(2*logOne-1)) >> (2 * logOne)),
		Y: int((x64.Y + 1<<(2*logOne-1)) >> (2 * logOne)),
	}
	c11 := roundP64(q0).Point()
	c12 := roundP64(r0).Point()
	c21 := roundP64(r1).Point()
	c22 := roundP64(q2).Point()
	return Cubic{s.C0, c11, c12, x},
		Cubic{x, c21, c22, s.C3}
}

// roundP64 performs a 16-bit rounding shifting.
func roundP64(p Point64) Point64 {
	return Point64{
		X: (p.X + 1<<(logOne-1)) >> logOne,
		Y: (p.Y + 1<<(logOne-1)) >> logOne,
	}
}

type Point struct {
	X, Y int
}

type point16 struct {
	X, Y int16
}

type Point64 struct {
	X, Y int64
}

func P64(p Point) Point64 {
	return Point64{
		X: int64(p.X),
		Y: int64(p.Y),
	}
}

func (p Point64) Mul(s int) Point64 {
	return Point64{
		X: p.X * int64(s),
		Y: p.Y * int64(s),
	}
}

func (p Point64) Div(s int) Point64 {
	return Point64{
		X: p.X / int64(s),
		Y: p.Y / int64(s),
	}
}

func (p Point64) Add(p2 Point64) Point64 {
	return Point64{
		X: p.X + p2.X,
		Y: p.Y + p2.Y,
	}
}

func (p Point64) Point() Point {
	return Point{
		X: int(p.X),
		Y: int(p.Y),
	}
}

func Pt(x, y int) Point {
	return Point{
		X: x,
		Y: y,
	}
}

func (p Point) Mul16(s uint16) Point {
	return Point{
		X: p.X * int(s),
		Y: p.Y * int(s),
	}
}

func (p Point) Mul(s int) Point {
	return Point{
		X: p.X * s,
		Y: p.Y * s,
	}
}

func (p Point) Div(s int) Point {
	return Point{
		X: p.X / s,
		Y: p.Y / s,
	}
}

func (p Point) Add(p2 Point) Point {
	return Point{
		X: p.X + p2.X,
		Y: p.Y + p2.Y,
	}
}

func (p Point) Sub(p2 Point) Point {
	return Point{
		X: p.X - p2.X,
		Y: p.Y - p2.Y,
	}
}

func interpolate16(p, p1 uint16, a, b point16) Point {
	b1 := b.P().Mul16(p)
	a1 := a.P().Mul16(p1)
	s := b1.Add(a1)
	return s
}

func interpolate32(p, p1 uint16, a, b Point) Point64 {
	b1 := P64(b).Mul(int(p))
	a1 := P64(a).Mul(int(p1))
	s := b1.Add(a1)
	return s
}

func interpolate64(p, p1 uint16, a, b Point64) Point64 {
	b1 := b.Mul(int(p))
	a1 := a.Mul(int(p1))
	s := b1.Add(a1)
	return s
}

func (s *Cubic) Add(off Point) Cubic {
	return Cubic{
		s.C0.Add(off),
		s.C1.Add(off),
		s.C2.Add(off),
		s.C3.Add(off),
	}
}

func (s *Cubic) Sub(off Point) Cubic {
	return Cubic{
		s.C0.Sub(off),
		s.C1.Sub(off),
		s.C2.Sub(off),
		s.C3.Sub(off),
	}
}

// Bounds is like [image.Rectangle] with its upper
// bound inclusive.
type Bounds struct {
	Min, Max Point
}

func (b Bounds) In(b2 Bounds) bool {
	return inBounds(b.Min, b2) && inBounds(b.Max, b2)
}

func (b Bounds) Union(b2 Bounds) Bounds {
	return Bounds{
		Min: Point{
			X: min(b.Min.X, b2.Min.X),
			Y: min(b.Min.Y, b2.Min.Y),
		},
		Max: Point{
			X: max(b.Max.X, b2.Max.X),
			Y: max(b.Max.Y, b2.Max.Y),
		},
	}
}

func (b Bounds) Empty() bool {
	return b.Max.X < b.Min.X || b.Max.Y < b.Min.X
}

func (b Bounds) Dx() int {
	return int(b.Max.X - b.Min.X)
}

func (b Bounds) Dy() int {
	return int(b.Max.Y - b.Min.Y)
}

func inBounds(p Point, b Bounds) bool {
	return b.Min.X <= p.X && p.X <= b.Max.X &&
		b.Min.Y <= p.Y && p.Y <= b.Max.Y
}

// Sample samples enough points on b that chords between samples
// are close to spacing apart. The samples are appended to points.
func Sample(points []Point, b Cubic, spacing int) []Point {
	// Estimate the curve length by many small samples.
	const samplingRate = 200
	var totalDist int
	var first Point
	if len(points) > 0 {
		first = points[len(points)-1]
	}
	prev := first
	in := new(Interpolator)
	in.Segment(b, samplingRate)
	for in.Step() {
		s := in.Position()
		totalDist += dist(prev, s)
		prev = s
	}
	// Given the total distance, compute the number of samples.
	nsamples := (totalDist + spacing - 1) / spacing
	// Sample short segments in the middle.
	nsamples = max(nsamples, 2)
	// Cap the samples per curve. The length-estimate Interpolator only
	// advances samplingRate steps, so the walk below can visit at most
	// that many distinct grid positions. Beyond it a curve degrades to a
	// coarser polyline rather than growing the sample buffer without
	// bound (matching maxSamplesPerCurve in SampleSym). Interpolating to
	// the exact spacing, unlike the old grid snapping, would otherwise
	// emit an unbounded run of sub-grid points for a very long stroke.
	nsamples = min(nsamples, samplingRate)
	adjSpacing := (totalDist + nsamples - 1) / nsamples
	prev = first
	from := first
	seg := 0
	var d int
	// Sample inner points. Spacings finer than the estimate
	// partition degenerate into repeated positions; skip them
	// rather than emit duplicate samples.
	in = new(Interpolator)
	in.Segment(b, samplingRate)
	for range nsamples - 1 {
		for d < adjSpacing && in.Step() {
			from = prev
			s := in.Position()
			seg = dist(prev, s)
			d += seg
			prev = s
		}
		// Place the sample at exactly adjSpacing along the last grid
		// step [from, prev] instead of snapping to the grid point prev.
		// The length-estimate Interpolator walks a coarse samplingRate
		// grid, so when adjSpacing sits just above one grid step every
		// chord snaps to 1x or 2x the step. That periodic doubling makes
		// the worst chord pace the whole engrave (see the planner's
		// uniform time scale). Interpolating removes the doubling with no
		// extra steps and no float. int64 avoids overflow of the 32-bit
		// device int when a grid step spans a large drawing.
		emit := prev
		if over := d - adjSpacing; over > 0 && seg > 0 {
			emit = prev.Add(P64(from.Sub(prev)).Mul(over).Div(seg).Point())
		}
		if len(points) == 0 || points[len(points)-1] != emit {
			points = append(points, emit)
		}
		d -= adjSpacing
	}
	// Force endpoint to align with curve segment end.
	if len(points) == 0 || points[len(points)-1] != b.C3 {
		points = append(points, b.C3)
	}
	return points
}

// dist is the Euclidean distance between a and b, rounded to the
// nearest integer. It is computed in integer arithmetic: Sample calls
// it a few hundred times per cubic, and the RP2350 has no double
// hardware, so a float64 Hypot here is software-emulated on the device
// curve-decode path.
func dist(a, b Point) int {
	d := b.Sub(a)
	return isqrt(d.X*d.X + d.Y*d.Y)
}

// isqrt returns sqrt(n) rounded to the nearest integer, for n >= 0.
func isqrt(n int) int {
	if n < 2 {
		return n
	}
	// Newton's method settles on floor(sqrt(n)).
	x := n
	for {
		y := (x + n/x) / 2
		if y >= x {
			break
		}
		x = y
	}
	// Round up past the midpoint x*x + x.
	if n-x*x > x {
		x++
	}
	return x
}

// sampleRefine is the number of uniform-t subdivisions used to build
// the arc-length table in SampleSym. It only needs to be fine enough
// that linear interpolation between table entries is accurate to a
// fraction of a machine unit for glyph-scale curves.
const sampleRefine = 1024

// maxSamplesPerCurve caps the samples one call to SampleSym emits. It
// mirrors the implicit cap in Sample, whose fixed-point Interpolator
// only advances samplingRate steps and so can never emit more than
// that many distinct points from a single curve. Downstream run-length
// guards (e.g. curves.maxRun) rely on a bounded per-curve count so a
// hostile curve with absurd arc length degrades to a coarse polyline
// instead of exhausting the sample buffer.
const maxSamplesPerCurve = 200

// SampleSym is a symmetric arc-length sampler. Like Sample it appends
// points spaced roughly spacing apart in chord length, forcing the
// final point to b.C3, but it computes cumulative arc length in
// floating point and places each interior sample at an exact target
// arc length. Because the cubic is evaluated with an exact symmetric
// polynomial and the targets are placed symmetrically, sampling a
// curve and its mirror yields mirrored points, unlike Sample whose
// integer fixed-point Interpolator and one-directional remainder walk
// drift asymmetrically.
func SampleSym(points []Point, b Cubic, spacing int) []Point {
	c0x, c0y := float64(b.C0.X), float64(b.C0.Y)
	c1x, c1y := float64(b.C1.X), float64(b.C1.Y)
	c2x, c2y := float64(b.C2.X), float64(b.C2.Y)
	c3x, c3y := float64(b.C3.X), float64(b.C3.Y)
	eval := func(t float64) (float64, float64) {
		mt := 1 - t
		a := mt * mt * mt
		bb := 3 * mt * mt * t
		cc := 3 * mt * t * t
		d := t * t * t
		return a*c0x + bb*c1x + cc*c2x + d*c3x,
			a*c0y + bb*c1y + cc*c2y + d*c3y
	}
	const n = sampleRefine
	var xs, ys, cum [n + 1]float64
	xs[0], ys[0] = c0x, c0y
	for i := 1; i <= n; i++ {
		xs[i], ys[i] = eval(float64(i) / n)
		cum[i] = cum[i-1] + math.Hypot(xs[i]-xs[i-1], ys[i]-ys[i-1])
	}
	total := cum[n]
	nsamples := int(math.Ceil(total/float64(spacing) - 1e-9))
	nsamples = max(nsamples, 2)
	nsamples = min(nsamples, maxSamplesPerCurve)
	adj := total / float64(nsamples)
	seg := 0
	for i := 1; i < nsamples; i++ {
		target := float64(i) * adj
		for seg < n && cum[seg+1] < target {
			seg++
		}
		l0, l1 := cum[seg], cum[seg+1]
		var f float64
		if l1 > l0 {
			f = (target - l0) / (l1 - l0)
		}
		p := Point{
			X: int(math.Round(xs[seg] + f*(xs[seg+1]-xs[seg]))),
			Y: int(math.Round(ys[seg] + f*(ys[seg+1]-ys[seg]))),
		}
		if len(points) == 0 || points[len(points)-1] != p {
			points = append(points, p)
		}
	}
	if len(points) == 0 || points[len(points)-1] != b.C3 {
		points = append(points, b.C3)
	}
	return points
}
