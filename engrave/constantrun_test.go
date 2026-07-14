package engrave

import (
	"math"
	"math/rand"
	"testing"

	"seedhammer.com/bezier"
	"seedhammer.com/bspline"
)

// constantRunBuf builds a planEngraving-shaped run buffer: two start
// clamps, interior knots, and the tripled end knot.
func constantRunBuf(start bezier.Point, ds []bezier.Point, end bezier.Point) []bspline.Knot {
	knots := []bspline.Knot{{Ctrl: start, Engrave: true}, {Ctrl: start, Engrave: true}}
	for _, d := range ds {
		knots = append(knots, bspline.Knot{Ctrl: d, Engrave: true})
	}
	return append(knots,
		bspline.Knot{Ctrl: end, Engrave: true},
		bspline.Knot{Ctrl: end, Engrave: true},
		bspline.Knot{Ctrl: end, Engrave: true})
}

// TestConstantRunGate drives planConstantRun over adversarial run
// shapes and asserts its contract: clamp knots untouched, every span
// timed, the plan no slower than the uniform fallback plus its noise
// margin, and the traced curve no worse than the uniform fallback's
// traced curve with the nominal limits (and the blend-overshoot
// floor) as the baseline. The uniform fallback itself traces past the
// nominal limits at rest boundaries — the windowed model's
// bench-validated clamp deflation — so the bar is comparative, not
// absolute.
func TestConstantRunGate(t *testing.T) {
	tps := conf.TicksPerSecond
	check := func(t *testing.T, name string, buf []bspline.Knot) (applied bool) {
		t.Helper()
		n := len(buf)
		uni := append([]bspline.Knot(nil), buf...)
		for i := range uni[2 : n-2] {
			uni[i+2].T = 1
		}
		uv, ua, uj := bspline.ComputeKinematics(uni, 1)
		tscale := timeScale(conf, true, uv, ua, uj)
		for i := range uni[2 : n-2] {
			uni[i+2].T = tscale
		}
		applied = planConstantRun(buf, conf, true)
		for _, i := range []int{0, 1, n - 2, n - 1} {
			if buf[i].T != 0 {
				t.Errorf("%s: clamp knot %d T mutated to %d", name, i, buf[i].T)
			}
		}
		if !applied {
			return
		}
		var dur uint
		for k := 2; k <= n-3; k++ {
			if buf[k].T == 0 {
				t.Errorf("%s: span %d left at T=0", name, k)
			}
			dur += buf[k].T
		}
		uniform := uint(n-4) * tscale
		if dur > uniform+uniform/32 {
			t.Errorf("%s: applied plan slower than priced: %d vs uniform %d", name, dur, uniform)
		}
		vi, ai, ji, _, _, _ := tracedFlagMaxima(buf, tps, false)
		vb, ab, jb, _, _, _ := tracedFlagMaxima(uni, tps, false)
		const floor = 65. / 64
		const eps = 1.001
		if vi > max(float64(conf.EngravingSpeed)*floor, vb)*eps ||
			ai > max(float64(conf.Acceleration)*floor, ab)*eps ||
			ji > max(float64(conf.Jerk)*floor, jb)*eps {
			t.Errorf("%s: traced kinematics worse than the uniform fallback: v=%.0f/%.0f a=%.0f/%.0f j=%.0f/%.0f",
				name, vi, vb, ai, ab, ji, jb)
		}
		return
	}

	c := mm / 5
	p0 := bezier.Pt(10*mm, 10*mm)
	line := func(from bezier.Point, dir bezier.Point, n int) []bezier.Point {
		var ds []bezier.Point
		p := from
		for range n {
			p = p.Add(dir)
			ds = append(ds, p)
		}
		return ds
	}

	t.Run("straight-uniform", func(t *testing.T) {
		ds := line(p0, bezier.Pt(c, 0), 20)
		if !check(t, "straight", constantRunBuf(p0, ds, ds[len(ds)-1].Add(bezier.Pt(c, 0)))) {
			t.Errorf("constant pacing did not apply to a straight uniform run")
		}
	})
	t.Run("turns", func(t *testing.T) {
		for _, deg := range []float64{20, 45, 80} {
			dir := bezier.Pt(int(float64(c)*math.Cos(deg*math.Pi/180)), int(float64(c)*math.Sin(deg*math.Pi/180)))
			for mult := 1; mult <= 24; mult += 4 {
				// Turn after the first chord (head) and before the
				// last (tail), the clamp-adjacent windows the cruise
				// measurement excludes.
				head := append([]bezier.Point{p0.Add(bezier.Pt(mult*c/2, 0))}, line(p0.Add(bezier.Pt(mult*c/2, 0)), dir, 30)...)
				check(t, "head-turn", constantRunBuf(p0, head, head[len(head)-1].Add(dir)))
				tail := line(p0, bezier.Pt(c, 0), 30)
				tail = append(tail, tail[len(tail)-1].Add(bezier.Pt(int(float64(mult*c/2)*math.Cos(deg*math.Pi/180)), int(float64(mult*c/2)*math.Sin(deg*math.Pi/180)))))
				check(t, "tail-turn", constantRunBuf(p0, tail, tail[len(tail)-1].Add(dir)))
			}
		}
	})
	t.Run("monster-and-zero-chords", func(t *testing.T) {
		for _, pos := range []int{0, 1, 7, 13} {
			var ds []bezier.Point
			p := p0
			for i := 0; i < 14; i++ {
				d := c
				if i == pos {
					d = 20 * c
				}
				p = p.Add(bezier.Pt(d, 0))
				ds = append(ds, p)
				if i == pos+2 {
					ds = append(ds, p)
				}
			}
			check(t, "monster", constantRunBuf(p0, ds, p.Add(bezier.Pt(c, 0))))
		}
	})
	t.Run("random", func(t *testing.T) {
		applied := 0
		const cases = 2000
		for seed := range cases {
			rng := rand.New(rand.NewSource(int64(seed)))
			nd := 7 + rng.Intn(40)
			p := p0.Add(bezier.Pt(rng.Intn(mm), rng.Intn(mm)))
			start := p
			var ds []bezier.Point
			dir := float64(rng.Intn(360))
			for range nd {
				step := mm/10 + rng.Intn(mm/3)
				switch rng.Intn(12) {
				case 0:
					step *= 8
				case 1:
					step = 0
				}
				rad := dir * math.Pi / 180
				p = p.Add(bezier.Pt(int(float64(step)*math.Cos(rad)), int(float64(step)*math.Sin(rad))))
				dir += float64(rng.Intn(41) - 20)
				ds = append(ds, p)
			}
			if check(t, "random", constantRunBuf(start, ds, p.Add(bezier.Pt(mm/10, 0)))) {
				applied++
			}
		}
		t.Logf("random runs: %d/%d applied", applied, cases)
		if applied < cases/2 {
			t.Errorf("only %d/%d random runs applied", applied, cases)
		}
	})
}
