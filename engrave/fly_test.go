package engrave

import (
	"math"
	"math/rand"
	"testing"

	"seedhammer.com/bezier"
	"seedhammer.com/bspline"
	"seedhammer.com/font/sh"
)

// TestDubinsClosure drives the bounded-curvature bridge over random
// junction configurations: every accepted path must close on its
// endpoint and heading (the sampler's self-check) and keep its chords
// near the requested spacing. The formulas' failure mode is refusing
// to fly, never bad geometry.
func TestDubinsClosure(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	const r, spacing = 4000.0, 2000.0
	accepted := 0
	const tries = 2000
	for i := 0; i < tries; i++ {
		exit := bezier.Pt(rng.Intn(20000)-10000, rng.Intn(20000)-10000)
		entry := bezier.Pt(rng.Intn(20000)-10000, rng.Intn(20000)-10000)
		out := dubinsBridge(nil, exit, rng.Float64()*2*math.Pi, entry, rng.Float64()*2*math.Pi, r, spacing)
		if out == nil {
			continue
		}
		accepted++
		prev := exit
		for _, p := range out {
			d := math.Hypot(float64(p.X-prev.X), float64(p.Y-prev.Y))
			if d > spacing*1.2+1 {
				t.Fatalf("case %d: chord %.0f exceeds spacing %g", i, d, spacing)
			}
			prev = p
		}
	}
	if accepted < tries*95/100 {
		t.Errorf("only %d/%d junctions closed", accepted, tries)
	}
}

// TestFlyingGlyphLimits plans glyphs with flying transitions enabled
// and holds the result to the planner gate's contract: ink no worse
// than the same text planned without flying (whose rest-boundary
// envelope the bench validated), needle-up bridges within the travel
// limits absolutely, both with the blend-overshoot floor.
func TestFlyingGlyphLimits(t *testing.T) {
	defer func(v bool) { FlyingTransitions = v }(FlyingTransitions)
	const floor = 65. / 64
	const eps = 1.001
	for _, txt := range []string{"t", "x", "F", "H", "+", "#", "*", "j", "$", "E!\"1245:;=?@ABDGIKMNPRTWXYZ[]abdef"} {
		plan := func() []bspline.Knot {
			var knots []bspline.Knot
			for k := range PlanEngraving(conf, func(yield func(Command) bool) {
				String(sh.Font, 3*mm, txt).Engrave(yield)
			}) {
				knots = append(knots, k)
			}
			return knots
		}
		FlyingTransitions = false
		vb, ab, jb, _, _, _ := tracedFlagMaxima(plan(), conf.TicksPerSecond, false)
		FlyingTransitions = true
		vi, ai, ji, vu, au, ju := tracedFlagMaxima(plan(), conf.TicksPerSecond, false)
		if vi > max(float64(conf.EngravingSpeed)*floor, vb)*eps ||
			ai > max(float64(conf.Acceleration)*floor, ab)*eps ||
			ji > max(float64(conf.Jerk)*floor, jb)*eps {
			t.Errorf("%q: flying ink trace worse than rest-to-rest: v=%.0f/%.0f a=%.0f/%.0f j=%.0f/%.0f",
				txt, vi, vb, ai, ab, ji, jb)
		}
		if vu > float64(conf.Speed)*floor || au > float64(conf.Acceleration)*floor || ju > float64(conf.Jerk)*floor {
			t.Errorf("%q: needle-up trace exceeds the travel limits: v=%.2f a=%.0f j=%.0f mm/s^k",
				txt, vu/mm, au/mm, ju/mm)
		}
	}
}
