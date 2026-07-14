package engrave

import (
	"math"
	"testing"

	"seedhammer.com/bezier"
	"seedhammer.com/bspline"
	"seedhammer.com/font/vector"
	"seedhammer.com/svgpath"
)

// periodicTestCircle is a 70mm circle of four cubic arcs centered on
// an 85mm plate, in machine units: the bench payload of the curves
// engrave-speed work.
func periodicTestCircle() []svgpath.Segment {
	pt := func(x, y int) bezier.Point { return bezier.Pt(x*64, y*64) }
	return []svgpath.Segment{
		{Op: svgpath.MoveTo, Args: [4]bezier.Point{pt(4250, 750)}},
		{Op: svgpath.CubeTo, Args: [4]bezier.Point{pt(6183, 750), pt(7750, 2317), pt(7750, 4250)}},
		{Op: svgpath.CubeTo, Args: [4]bezier.Point{pt(7750, 6183), pt(6183, 7750), pt(4250, 7750)}},
		{Op: svgpath.CubeTo, Args: [4]bezier.Point{pt(2317, 7750), pt(750, 6183), pt(750, 4250)}},
		{Op: svgpath.CubeTo, Args: [4]bezier.Point{pt(750, 2317), pt(2317, 750), pt(4250, 750)}},
	}
}

// curveCommands runs segments through the device fitting pipeline and
// collects the resulting engraver commands.
func curveCommands(t *testing.T, segs []svgpath.Segment, periodic bool) Engraving {
	t.Helper()
	var cmds []Command
	b := svgpath.NewBuilder(strokeWidth, true, svgpath.ControlFit(), func(k vector.Knot) bool {
		if k.Periodic {
			cmds = append(cmds, PeriodicPoint(k.Ctrl))
		} else {
			cmds = append(cmds, ControlPoint(k.Line, k.Ctrl))
		}
		return true
	})
	if periodic {
		b.Periodic()
	}
	for _, s := range segs {
		if !b.Add(s) {
			break
		}
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	return func(yield func(Command) bool) {
		for _, c := range cmds {
			if !yield(c) {
				return
			}
		}
	}
}

// tracedSegment is a Bézier extracted from a planned spline with its
// tick duration.
type tracedSegment struct {
	c bezier.Cubic
	t uint
}

// traceEngraving extracts the engraved Béziers of a planned spline
// the way the stepper does.
func traceEngraving(spline bspline.Curve) []tracedSegment {
	var seg bspline.Segment
	var out []tracedSegment
	for k := range spline {
		c, t, engrave := seg.Knot(k)
		if t > 0 && engrave {
			out = append(out, tracedSegment{c, t})
		}
	}
	return out
}

// traceKinematics measures the true per-axis kinematic maxima of
// traced segments, in steps/s^k, along with the total duration in
// ticks. Unlike the windowed spline estimate, the values derive from
// the Bézier polynomials the stepper interpolates.
func traceKinematics(segs []tracedSegment, tps uint) (v, a, j float64, dur uint) {
	for _, s := range segs {
		dur += s.t
		T := float64(s.t) / float64(tps)
		d1 := [3]bezier.Point{
			s.c.C1.Sub(s.c.C0).Mul(3),
			s.c.C2.Sub(s.c.C1).Mul(3),
			s.c.C3.Sub(s.c.C2).Mul(3),
		}
		for i := 0; i <= 8; i++ {
			u := float64(i) / 8
			mu := 1 - u
			vx := (float64(d1[0].X)*mu*mu + 2*float64(d1[1].X)*mu*u + float64(d1[2].X)*u*u) / T
			vy := (float64(d1[0].Y)*mu*mu + 2*float64(d1[1].Y)*mu*u + float64(d1[2].Y)*u*u) / T
			v = max(v, math.Abs(vx), math.Abs(vy))
			ax := 2 * (float64(d1[1].X-d1[0].X)*mu + float64(d1[2].X-d1[1].X)*u) / (T * T)
			ay := 2 * (float64(d1[1].Y-d1[0].Y)*mu + float64(d1[2].Y-d1[1].Y)*u) / (T * T)
			a = max(a, math.Abs(ax), math.Abs(ay))
		}
		jx := 6 * float64(s.c.C3.X-3*s.c.C2.X+3*s.c.C1.X-s.c.C0.X) / (T * T * T)
		jy := 6 * float64(s.c.C3.Y-3*s.c.C2.Y+3*s.c.C1.Y-s.c.C0.Y) / (T * T * T)
		j = max(j, math.Abs(jx), math.Abs(jy))
	}
	return v, a, j, dur
}

// maxRadialDeviation measures how far traced segments stray from a
// circle, in machine units.
func maxRadialDeviation(segs []tracedSegment, center bezier.Point, r float64) float64 {
	var dev float64
	for _, s := range segs {
		for i := 0; i <= 8; i++ {
			u := float64(i) / 8
			mu := 1 - u
			b := func(p0, p1, p2, p3 int) float64 {
				return float64(p0)*mu*mu*mu + 3*float64(p1)*mu*mu*u + 3*float64(p2)*mu*u*u + float64(p3)*u*u*u
			}
			x := b(s.c.C0.X, s.c.C1.X, s.c.C2.X, s.c.C3.X) - float64(center.X)
			y := b(s.c.C0.Y, s.c.C1.Y, s.c.C2.Y, s.c.C3.Y) - float64(center.Y)
			dev = max(dev, math.Abs(math.Hypot(x, y)-r))
		}
	}
	return dev
}

// TestPeriodicPlan checks that a closed contour planned periodically
// engraves faster than its clamped plan, stays a circle, respects the
// machine limits in the traced Béziers (which today's clamped seam
// overshoots) and enters and leaves the loop at rest.
func TestPeriodicPlan(t *testing.T) {
	segs := periodicTestCircle()
	clamped := PlanEngraving(conf, curveCommands(t, segs, false))
	periodic := verifiedEngraving(t, conf, PlanEngraving(conf, curveCommands(t, segs, true)))

	_, _, _, clampedDur := traceKinematics(traceEngraving(clamped), conf.TicksPerSecond)
	trace := traceEngraving(periodic)
	v, a, j, dur := traceKinematics(trace, conf.TicksPerSecond)

	t.Logf("engrave duration: clamped %.2fs, periodic %.2fs", secs(clampedDur), secs(dur))
	t.Logf("traced kinematics: v=%.2f a=%.0f j=%.0f (limits %d %d %d, mm/s^k)",
		v/mm, a/mm, j/mm, conf.EngravingSpeed/uint(mm), conf.Acceleration/uint(mm), conf.Jerk/uint(mm))
	if dur >= clampedDur*92/100 {
		t.Errorf("periodic plan not faster: %.2fs vs clamped %.2fs", secs(dur), secs(clampedDur))
	}
	const slack = 1.01
	if v > float64(conf.EngravingSpeed)*slack || a > float64(conf.Acceleration)*slack || j > float64(conf.Jerk)*slack {
		t.Errorf("traced kinematics exceed limits: v=%.2f a=%.0f j=%.0f mm/s^k", v/mm, a/mm, j/mm)
	}

	center := bezier.Pt(4250*64, 4250*64)
	if dev := maxRadialDeviation(trace, center, 3500*64); dev > 0.03*mm {
		t.Errorf("loop deviates %.4fmm from the circle", dev/mm)
	}

	if first := trace[0]; first.c.C0 != first.c.C1 {
		t.Errorf("loop does not start at rest: C0=%v C1=%v", first.c.C0, first.c.C1)
	}
	if last := trace[len(trace)-1]; last.c.C2 != last.c.C3 {
		t.Errorf("loop does not end at rest: C2=%v C3=%v", last.c.C2, last.c.C3)
	}
}

// TestPeriodicPlanFallback checks that loops too small for cyclic
// pacing still plan safely as clamped runs.
func TestPeriodicPlanFallback(t *testing.T) {
	// A 1mm circle samples to a handful of knots: below the periodic
	// span minimum.
	pt := func(x, y float64) bezier.Point {
		return bezier.Pt(int(math.Round(x*mm)), int(math.Round(y*mm)))
	}
	const k = 0.5523
	segs := []svgpath.Segment{
		{Op: svgpath.MoveTo, Args: [4]bezier.Point{pt(10, 9.5)}},
		{Op: svgpath.CubeTo, Args: [4]bezier.Point{pt(10+k*.5, 9.5), pt(10.5, 10-k*.5), pt(10.5, 10)}},
		{Op: svgpath.CubeTo, Args: [4]bezier.Point{pt(10.5, 10+k*.5), pt(10+k*.5, 10.5), pt(10, 10.5)}},
		{Op: svgpath.CubeTo, Args: [4]bezier.Point{pt(10-k*.5, 10.5), pt(9.5, 10+k*.5), pt(9.5, 10)}},
		{Op: svgpath.CubeTo, Args: [4]bezier.Point{pt(9.5, 10-k*.5), pt(10-k*.5, 9.5), pt(10, 9.5)}},
	}
	spline := verifiedEngraving(t, conf, PlanEngraving(conf, curveCommands(t, segs, true)))
	if _, _, _, dur := traceKinematics(traceEngraving(spline), conf.TicksPerSecond); dur == 0 {
		t.Error("small loop planned to nothing")
	}
}

// TestPeriodicOpenRunUnchanged checks that an open stroke plans
// identically with periodic contours enabled.
func TestPeriodicOpenRunUnchanged(t *testing.T) {
	segs := periodicTestCircle()[:3] // half the circle: an open arc
	want := planKnots(PlanEngraving(conf, curveCommands(t, segs, false)))
	got := planKnots(PlanEngraving(conf, curveCommands(t, segs, true)))
	if len(want) != len(got) {
		t.Fatalf("open run changed: %d knots vs %d", len(got), len(want))
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("open run knot %d changed: %v vs %v", i, got[i], want[i])
		}
	}
}

func planKnots(s bspline.Curve) []bspline.Knot {
	var out []bspline.Knot
	for k := range s {
		out = append(out, k)
	}
	return out
}

func secs(ticks uint) float64 {
	return float64(ticks) / float64(conf.TicksPerSecond)
}
