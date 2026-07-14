package engrave

import (
	"encoding/binary"
	"math"
	"math/rand"
	"testing"

	"seedhammer.com/bezier"
	"seedhammer.com/bspline"
	"seedhammer.com/font/sh"
	"seedhammer.com/font/vector"
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

// TestFlyingGlyphLimits holds flying emission to the planner gate's
// contract on real glyphs: with the flag on, traced ink and traced
// needle-up kinematics must stay no worse than the same text planned
// without flying (whose rest-boundary envelope the bench validated),
// floored by the nominal limits.
func TestFlyingGlyphLimits(t *testing.T) {
	defer func(v bool) { FlyingTransitions = v }(FlyingTransitions)
	const floor = 65. / 64
	const eps = 1.001
	for _, txt := range []string{"t", "x", "F", "H", "+", "#", "*", "j", "$", "mnru", "E!\"1245:;=?@ABDGIKMNPRTWXYZ[]abdef"} {
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
		vb, ab, jb, vub, aub, jub := testTracedMaxima(plan(), conf.TicksPerSecond)
		FlyingTransitions = true
		vi, ai, ji, vu, au, ju := testTracedMaxima(plan(), conf.TicksPerSecond)
		if vi > max(float64(conf.EngravingSpeed)*floor, vb)*eps ||
			ai > max(float64(conf.Acceleration)*floor, ab)*eps ||
			ji > max(float64(conf.Jerk)*floor, jb)*eps {
			t.Errorf("%q: flying ink trace worse than rest-to-rest: v=%.0f/%.0f a=%.0f/%.0f j=%.0f/%.0f",
				txt, vi, vb, ai, ab, ji, jb)
		}
		if vu > max(float64(conf.Speed)*floor, vub)*eps ||
			au > max(float64(conf.Acceleration)*floor, aub)*eps ||
			ju > max(float64(conf.Jerk)*floor, jub)*eps {
			t.Errorf("%q: flying needle-up trace worse than rest-to-rest: v=%.0f/%.0f a=%.0f/%.0f j=%.0f/%.0f",
				txt, vu, vub, au, aub, ju, jub)
		}
	}
}

// testFlyFace bakes a two-stroke glyph 'A' with collinear tangents
// and a short hop: a junction that must fly, independent of font and
// pricing drift.
func testFlyFace() *vector.Face {
	type knot struct {
		line bool
		x, y int16
	}
	var knots []knot
	add := func(x, y int16, flags ...bool) {
		for _, l := range flags {
			knots = append(knots, knot{l, x, y})
		}
	}
	M, L := false, true
	add(0, 0, M, M, L)
	for x := int16(100); x <= 2000; x += 100 {
		add(x, 0, L)
	}
	add(2000, 0, L, L, M)
	add(2400, 0, M, M, L)
	for x := int16(2500); x <= 4400; x += 100 {
		add(x, 0, L)
	}
	add(4400, 0, L, L)
	const indexLen = 127
	offSplines := 4 + indexLen*vector.IndexElemSize
	data := make([]byte, offSplines+len(knots)*5)
	bo := binary.LittleEndian
	bo.PutUint16(data[0:], 0)
	bo.PutUint16(data[2:], 1000)
	gdata := data[4+'A'*vector.IndexElemSize:]
	bo.PutUint16(gdata[0:], 4600)
	bo.PutUint16(gdata[2:], uint16(offSplines))
	bo.PutUint16(gdata[4:], uint16(offSplines+len(knots)*5))
	for i, k := range knots {
		off := offSplines + i*5
		if k.line {
			data[off] = 1
		}
		bo.PutUint16(data[off+1:], uint16(k.x))
		bo.PutUint16(data[off+3:], uint16(k.y))
	}
	return vector.NewFace(data)
}

// TestFlyingJunction plans the guaranteed-flyable synthetic glyph and
// binds the flying path end to end: the junction flies, the fused
// plan stays within the machine envelope, and the needle-up hop is
// never mislabeled as ink.
func TestFlyingJunction(t *testing.T) {
	defer func(v bool) { FlyingTransitions = v }(FlyingTransitions)
	FlyingTransitions = true
	face := testFlyFace()
	const em = 4 * mm
	cmd := String(face, em, "A")
	var knots []bspline.Knot
	for k := range PlanEngraving(conf, func(yield func(Command) bool) {
		cmd.Engrave(yield)
	}) {
		knots = append(knots, k)
	}
	flew := 0
	for _, b := range cmd.fly {
		if b {
			flew++
		}
	}
	if flew == 0 {
		t.Fatal("the tangent junction did not fly")
	}
	vi, ai, ji, vu, au, ju := testTracedMaxima(knots, conf.TicksPerSecond)
	const floor = 65. / 64
	if vi > float64(conf.EngravingSpeed)*floor || ai > float64(conf.Acceleration)*floor || ji > float64(conf.Jerk)*floor {
		t.Errorf("fused ink trace exceeds limits: v=%.0f a=%.0f j=%.0f", vi, ai, ji)
	}
	if vu > float64(conf.EngravingSpeed)*2 || au > float64(conf.Acceleration)*floor || ju > float64(conf.Jerk)*floor {
		t.Errorf("bridge trace exceeds limits: v=%.0f a=%.0f j=%.0f", vu, au, ju)
	}
	// Ink length must be the two strokes and their junction blends,
	// nothing more: a mislabeled hop shows up as extra ink.
	var seg bspline.Segment
	ink := 0.0
	for _, k := range knots {
		c, dt, eng := seg.Knot(k)
		if dt > 0 && eng {
			ink += math.Hypot(float64(c.C3.X-c.C0.X), float64(c.C3.Y-c.C0.Y))
		}
	}
	strokes := 2 * 2000.0 * em / 1000
	if ink > strokes*1.06 {
		t.Errorf("ink length %.0f exceeds the strokes' %.0f: hop mislabeled as ink", ink, strokes)
	}
}

// TestGlyphPeriodicEmission pins the baked Periodic flag through the
// glyph re-emission: bowls must reach the planner as periodic points
// or cyclic pacing silently dies for all font text.
func TestGlyphPeriodicEmission(t *testing.T) {
	for _, txt := range []string{"O", "0", "8", "o"} {
		periodic := 0
		String(sh.Font, 3*mm, txt).Engrave(func(c Command) bool {
			if k, ok := c.AsKnot(); ok && k.Periodic {
				periodic++
			}
			return true
		})
		if periodic == 0 {
			t.Errorf("%q: no periodic knots emitted", txt)
		}
	}
}
