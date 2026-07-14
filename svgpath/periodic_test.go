package svgpath

import (
	"fmt"
	"math"
	"testing"

	"seedhammer.com/bezier"
	"seedhammer.com/font/vector"
)

// peak3rd returns the peak and mean magnitude of the 3rd difference of
// a control polygon (the quantity the engrave planner paces a run off).
func peak3rd(pts []bezier.Point) (peak, mean float64) {
	var sum float64
	n := 0
	for i := 3; i < len(pts); i++ {
		dx := float64(-pts[i-3].X + 3*pts[i-2].X - 3*pts[i-1].X + pts[i].X)
		dy := float64(-pts[i-3].Y + 3*pts[i-2].Y - 3*pts[i-1].Y + pts[i].Y)
		m := math.Hypot(dx, dy)
		if m > peak {
			peak = m
		}
		sum += m
		n++
	}
	if n > 0 {
		mean = sum / float64(n)
	}
	return
}

// buildKnots runs segments through a periodic builder, returning the
// emitted knots and the sample runs seen by the fitter.
func buildKnots(t *testing.T, segs []Segment, prec int) (knots []vector.Knot, runs [][]bezier.Point) {
	t.Helper()
	b := NewBuilder(prec, true, ControlFit(), func(k vector.Knot) bool {
		knots = append(knots, k)
		return true
	})
	b.Periodic()
	b.onSamples = func(s []bezier.Point) {
		runs = append(runs, append([]bezier.Point{}, s...))
	}
	for _, s := range segs {
		if !b.Add(s) {
			break
		}
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	return knots, runs
}

// circleSegments is a closed circle of four cubic arcs.
func circleSegments(cx, cy, r int) []Segment {
	k := int(math.Round(0.5523 * float64(r)))
	return []Segment{
		{Op: MoveTo, Args: [4]bezier.Point{bezier.Pt(cx+r, cy)}},
		{Op: CubeTo, Args: [4]bezier.Point{bezier.Pt(cx+r, cy+k), bezier.Pt(cx+k, cy+r), bezier.Pt(cx, cy+r)}},
		{Op: CubeTo, Args: [4]bezier.Point{bezier.Pt(cx-k, cy+r), bezier.Pt(cx-r, cy+k), bezier.Pt(cx-r, cy)}},
		{Op: CubeTo, Args: [4]bezier.Point{bezier.Pt(cx-r, cy-k), bezier.Pt(cx-k, cy-r), bezier.Pt(cx, cy-r)}},
		{Op: CubeTo, Args: [4]bezier.Point{bezier.Pt(cx+k, cy-r), bezier.Pt(cx+r, cy-k), bezier.Pt(cx+r, cy)}},
	}
}

// TestBuilderPeriodic checks the emission of a closed smooth contour:
// a stroke-start clamp retargeted to the seam knot value, the
// insertion polygon flagged periodic, and a trailing clamp at the
// seam.
func TestBuilderPeriodic(t *testing.T) {
	knots, runs := buildKnots(t, circleSegments(5000, 5000, 4000), 300)
	if len(runs) != 1 {
		t.Fatalf("expected one sample run, got %d", len(runs))
	}
	samples := runs[0]
	if samples[0] != samples[len(samples)-1] {
		t.Fatal("sample run is not closed")
	}
	d := samples[:len(samples)-1]
	n := len(d)
	dl, d0, d1 := d[n-1], d[0], d[1]
	b1 := insetThird(dl, d0)
	b2 := insetThird(d1, d0)
	k0 := bezier.Pt(divRound(dl.X+4*d0.X+d1.X, 6), divRound(dl.Y+4*d0.Y+d1.Y, 6))

	want := []vector.Knot{{Ctrl: k0}, {Ctrl: k0}, {Ctrl: k0}}
	want = append(want, vector.Knot{Ctrl: b2, Line: true, Periodic: true})
	for _, p := range d[1:] {
		want = append(want, vector.Knot{Ctrl: p, Line: true, Periodic: true})
	}
	want = append(want, vector.Knot{Ctrl: b1, Line: true, Periodic: true})
	want = append(want,
		vector.Knot{Ctrl: k0, Line: true},
		vector.Knot{Ctrl: k0, Line: true},
		vector.Knot{Ctrl: k0, Line: true})
	if len(knots) != len(want) {
		t.Fatalf("emitted %d knots, want %d", len(knots), len(want))
	}
	for i := range want {
		if knots[i] != want[i] {
			t.Fatalf("knot %d: got %v, want %v", i, knots[i], want[i])
		}
	}
}

// TestBuilderPeriodicSharpSeam checks that a closed contour whose
// seam turns sharper than 45° keeps the clamped representation.
func TestBuilderPeriodicSharpSeam(t *testing.T) {
	// A teardrop: leaves (0,0) heading +x, returns heading -y.
	segs := []Segment{
		{Op: MoveTo, Args: [4]bezier.Point{bezier.Pt(0, 0)}},
		{Op: CubeTo, Args: [4]bezier.Point{bezier.Pt(6000, 0), bezier.Pt(6000, 6000), bezier.Pt(3000, 6000)}},
		{Op: CubeTo, Args: [4]bezier.Point{bezier.Pt(0, 6000), bezier.Pt(0, 4000), bezier.Pt(0, 0)}},
	}
	knots, _ := buildKnots(t, segs, 300)
	for i, k := range knots {
		if k.Periodic {
			t.Fatalf("knot %d flagged periodic across a sharp seam", i)
		}
	}
	if len(knots) == 0 || knots[0].Ctrl != bezier.Pt(0, 0) {
		t.Fatal("stroke-start clamp not at the move target")
	}
}

// TestBuilderPeriodicSplit checks that a deliberate clamp inside a
// closed contour disables the periodic representation.
func TestBuilderPeriodicSplit(t *testing.T) {
	segs := circleSegments(5000, 5000, 4000)
	// A zero-length line after the second arc clamps the stroke.
	clamp := Segment{Op: LineTo, Args: [4]bezier.Point{bezier.Pt(1000, 5000)}}
	segs = append(segs[:3], append([]Segment{clamp}, segs[3:]...)...)
	knots, _ := buildKnots(t, segs, 300)
	for i, k := range knots {
		if k.Periodic {
			t.Fatalf("knot %d flagged periodic across a deliberate clamp", i)
		}
	}
}

// TestBuilderPeriodicTooSmall checks that a loop below the sample
// minimum keeps the clamped representation.
func TestBuilderPeriodicTooSmall(t *testing.T) {
	// A two-arc circle smaller than the sampling spacing: too few
	// samples for a periodic loop.
	const cx, cy, r = 5000, 5000, 100
	segs := []Segment{
		{Op: MoveTo, Args: [4]bezier.Point{bezier.Pt(cx+r, cy)}},
		{Op: CubeTo, Args: [4]bezier.Point{bezier.Pt(cx+r, cy+4*r/3), bezier.Pt(cx-r, cy+4*r/3), bezier.Pt(cx-r, cy)}},
		{Op: CubeTo, Args: [4]bezier.Point{bezier.Pt(cx-r, cy-4*r/3), bezier.Pt(cx+r, cy-4*r/3), bezier.Pt(cx+r, cy)}},
	}
	knots, _ := buildKnots(t, segs, 300)
	for i, k := range knots {
		if k.Periodic {
			t.Fatalf("knot %d of a tiny loop flagged periodic", i)
		}
	}
}

// TestPeriodicPolygonRuntSeam checks that the seam smoothness policy
// judges the polygon adjacency that is actually emitted: a runt
// closing sample must not pass the 45° check on behalf of the sharp
// corner its drop exposes.
func TestPeriodicPolygonRuntSeam(t *testing.T) {
	// A smooth open arc of 9 samples 300 apart, closed by a hairpin: a
	// runt sample 100 units from the start, aligned with the start
	// tangent, while the leg behind it doubles back.
	samples := []bezier.Point{
		{X: 0, Y: 0},
		{X: 300, Y: 0},
		{X: 600, Y: 30},
		{X: 900, Y: 90},
		{X: 1200, Y: 180},
		{X: 900, Y: 400},
		{X: 600, Y: 430},
		{X: 300, Y: 400},
		// The runt: within prec/2 of the start, its chord within 45°
		// of the start tangent, but arrived at from behind the seam.
		{X: -100, Y: 20},
		{X: 0, Y: 0},
	}
	b := &Builder{prec: 300, periodic: true, samples: samples}
	if d := b.periodicPolygon(); d != nil {
		t.Errorf("hairpin seam accepted as periodic: %v", d)
	}

	// A loop with a smooth approach keeps the periodic form, and the
	// runt is absorbed.
	b.samples = []bezier.Point{
		{X: 0, Y: 0},
		{X: 300, Y: -100},
		{X: 600, Y: 0},
		{X: 800, Y: 200},
		{X: 800, Y: 500},
		{X: 500, Y: 700},
		{X: 100, Y: 600},
		{X: -250, Y: 150},
		{X: 30, Y: 20},
		{X: 0, Y: 0},
	}
	d := b.periodicPolygon()
	if d == nil {
		t.Fatal("smooth runt seam rejected")
	}
	if len(d) != 8 || d[len(d)-1] != bezier.Pt(-250, 150) {
		t.Errorf("runt closing sample not absorbed: %v", d)
	}
}

// TestPeriodicKillsSeamSpike is the concept gate for periodic contours:
// a closed circle fitted the current CLAMPED way spikes the control-
// polygon 3rd difference at the coincident-triple seam, while the same
// samples wrapped PERIODICALLY are smooth (constant by circle symmetry).
func TestPeriodicKillsSeamSpike(t *testing.T) {
	// Evenly spaced samples on a circle, closed (last == first).
	const n = 48
	var s []bezier.Point
	for i := 0; i < n; i++ {
		a := float64(i) / n * 2 * math.Pi
		s = append(s, bezier.Pt(int(math.Round(math.Cos(a)*10000)), int(math.Round(math.Sin(a)*10000))))
	}

	// Current clamped representation: leading + trailing coincident
	// triples, as emitted by MoveTo's emit3 and the fitter's last×3.
	clamped := make([]bezier.Point, 0, n+7)
	clamped = append(clamped, s[0], s[0], s[0])
	clamped = append(clamped, s...)
	clamped = append(clamped, s[0], s[0], s[0]) // close back to the seam

	// Periodic representation: no clamps, wrap the first three control
	// points so the uniform b-spline traces a smooth closed loop.
	periodic := make([]bezier.Point, 0, n+3)
	periodic = append(periodic, s...)
	periodic = append(periodic, s[0], s[1], s[2])

	cp, cm := peak3rd(clamped)
	pp, pm := peak3rd(periodic)
	fmt.Printf("clamped : peak=%.0f mean=%.0f  peak/mean=%.1f\n", cp, cm, cp/cm)
	fmt.Printf("periodic: peak=%.0f mean=%.0f  peak/mean=%.1f\n", pp, pm, pp/pm)

	if pp/pm > 2 {
		t.Errorf("periodic still spikes: peak/mean=%.1f", pp/pm)
	}
	if cp/cm < 5 {
		t.Errorf("clamped expected to spike, got peak/mean=%.1f", cp/cm)
	}
}

// TestInterpolateFitPeriodic checks the cyclic fit interpolates its
// samples and preserves mirror symmetry: the failure mode that egged
// round glyphs in asymmetric fitters.
func TestInterpolateFitPeriodic(t *testing.T) {
	const n = 24
	var s []bezier.Point
	for i := 0; i < n; i++ {
		a := 2 * math.Pi * float64(i) / n
		s = append(s, bezier.Pt(
			int(math.Round(10000*math.Cos(a))),
			int(math.Round(6000*math.Sin(a))),
		))
	}
	d, err := InterpolateFitPeriodic()(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(d) != n {
		t.Fatalf("fit returned %d controls for %d samples", len(d), n)
	}
	for i := range s {
		prev, next := d[(i+n-1)%n], d[(i+1)%n]
		kx := math.Round(float64(prev.X+4*d[i].X+next.X) / 6)
		ky := math.Round(float64(prev.Y+4*d[i].Y+next.Y) / 6)
		if dx, dy := kx-float64(s[i].X), ky-float64(s[i].Y); math.Abs(dx) > 1 || math.Abs(dy) > 1 {
			t.Errorf("knot %d misses its sample by (%g, %g)", i, dx, dy)
		}
	}
	// Mirror symmetry about the X axis: sample i mirrors sample n-i.
	for i := 1; i < n; i++ {
		m := d[n-i]
		if d[i].X != m.X || d[i].Y != -m.Y {
			t.Errorf("controls %d/%d not mirrored: %v vs %v", i, n-i, d[i], m)
		}
	}
}
