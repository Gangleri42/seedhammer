package engrave

import (
	"math"
	"slices"
	"testing"

	"seedhammer.com/bspline"
	"seedhammer.com/font/sh"
)

// The hammer solenoid counts a free-running 25ms period, started in
// the same register write as the step stream, so period 0 begins at
// plan tick 0. Each period ends with an activation window (4ms at 28V
// supply to 5ms at 20V) during which the stream's needle bit passes
// through to the solenoid: a dot lands where a window opens with the
// needle down.
const (
	hammerPeriodTicks = speed * 25 / 1000
	// Activation window at the 20V worst case. The window offset
	// shifts every dot by a constant along-path distance; pitch is
	// unaffected, absolute dot positions assume 20V.
	hammerActTicks = speed * 5 / 1000
)

// dot is one hammer strike: the commanded needle position, in machine
// units, at the tick its activation window opened.
type dot struct {
	x, y   float64
	stroke int
}

// pitchTrace walks a planned spline on the global tick grid, the way
// the stepper and the free-running solenoid clock execute it, keeping
// travel and dwell ticks that traceEngraving discards.
type pitchTrace struct {
	dots    []dot
	strokes int
	// partial counts needle flips inside an open activation window:
	// strikes with less than the full window's energy.
	partial      int
	ticks        uint64
	engraveTicks uint64
}

func tracePitch(spline bspline.Curve) pitchTrace {
	var tr pitchTrace
	var seg bspline.Segment
	engraving := false
	for k := range spline {
		c, dt, engrave := seg.Knot(k)
		if engrave != engraving {
			if tr.ticks%hammerPeriodTicks > hammerPeriodTicks-hammerActTicks {
				tr.partial++
			}
			if engrave {
				tr.strokes++
			}
			engraving = engrave
		}
		if dt == 0 {
			continue
		}
		if engrave {
			tr.engraveTicks += uint64(dt)
			// Window-open ticks w in [ticks, ticks+dt) with
			// w % period == period - act.
			const open = hammerPeriodTicks - hammerActTicks
			w := tr.ticks + (open+hammerPeriodTicks-tr.ticks%hammerPeriodTicks)%hammerPeriodTicks
			for ; w < tr.ticks+uint64(dt); w += hammerPeriodTicks {
				u := float64(w-tr.ticks) / float64(dt)
				mu := 1 - u
				b := func(p0, p1, p2, p3 int) float64 {
					return float64(p0)*mu*mu*mu + 3*float64(p1)*mu*mu*u + 3*float64(p2)*mu*u*u + float64(p3)*u*u*u
				}
				tr.dots = append(tr.dots, dot{
					x:      b(c.C0.X, c.C1.X, c.C2.X, c.C3.X),
					y:      b(c.C0.Y, c.C1.Y, c.C2.Y, c.C3.Y),
					stroke: tr.strokes,
				})
			}
		}
		tr.ticks += uint64(dt)
	}
	return tr
}

// charsetPlate is the worst-case 44x26 charset plate at 3.0mm as one
// continuous command stream, serpentine rows like the text-plate
// assembler. Planning it per row would drop the inter-row travel
// ticks and scramble the free-running hammer phase.
func charsetPlate() Engraving {
	charset := ""
	for r := rune('!'); r < 127; r++ {
		if _, _, ok := sh.Font.Decode(r); ok {
			charset += string(r)
		}
	}
	row := ""
	for len(row) < 44 {
		row += charset
	}
	row = row[:44]
	const em = 3 * mm
	lineHeight := em * 12 / 10
	return func(yield func(Command) bool) {
		for i := range 26 {
			tr := NewTransform(yield)
			tr = tr.Offset(0, i*lineHeight)
			str := String(sh.Font, em, row)
			if i%2 == 1 {
				str.Reversed()
			}
			if !str.Engrave(tr.Yield) {
				return
			}
		}
	}
}

// TestDotPitchBaseline measures the inter-dot spacing of the perfect
// plate: the worst-case charset plate through today's rest-to-rest
// planner at jerk 2600. The constant-velocity phases are judged
// against this histogram, pitch uniformity first, minutes second.
func TestDotPitchBaseline(t *testing.T) {
	plate := charsetPlate()
	tr := tracePitch(PlanEngraving(conf, plate))

	if dur := bspline.Measure(PlanEngraving(conf, plate)).Duration; tr.ticks != uint64(dur) {
		t.Errorf("traced %d ticks, plan measures %d: trace dropped time", tr.ticks, dur)
	}
	if len(tr.dots) == 0 {
		t.Fatal("no dots traced")
	}

	// Pitch: distance between consecutive dots of one stroke. A
	// stroke change means the needle lifted in between: a travel
	// crossing, not a pitch.
	var pitches []float64
	dotStrokes := 1
	for i := 1; i < len(tr.dots); i++ {
		a, b := tr.dots[i-1], tr.dots[i]
		if a.stroke != b.stroke {
			dotStrokes++
			continue
		}
		pitches = append(pitches, math.Hypot(b.x-a.x, b.y-a.y)/mm)
	}
	slices.Sort(pitches)
	quantile := func(q float64) float64 {
		return pitches[int(q*float64(len(pitches)-1))]
	}

	var sum, sumsq float64
	for _, p := range pitches {
		sum += p
		sumsq += p * p
	}
	mean := sum / float64(len(pitches))
	cv := math.Sqrt(sumsq/float64(len(pitches))-mean*mean) / mean

	// The planner caps per-axis speed (Chebyshev), so path speed
	// reaches engravingSpeed*sqrt(2) on diagonals and pitch tops out
	// at sqrt(2) times the nominal speed*period.
	const nominal = float64(engravingSpeed) / mm * 0.025
	if maxPitch := pitches[len(pitches)-1]; maxPitch > nominal*math.Sqrt2*1.02 {
		t.Errorf("max pitch %.4fmm exceeds the diagonal engraving speed cap", maxPitch)
	}

	inBand := 0
	for _, p := range pitches {
		if math.Abs(p-nominal) <= nominal/10 {
			inBand++
		}
	}

	tps := float64(conf.TicksPerSecond)
	t.Logf("plate: %.2f min total, %.2f min needle-down, %d strokes",
		float64(tr.ticks)/tps/60, float64(tr.engraveTicks)/tps/60, tr.strokes)
	t.Logf("dots: %d on %d strokes, %d zero-dot strokes, %d partial-energy windows",
		len(tr.dots), dotStrokes, tr.strokes-dotStrokes, tr.partial)
	t.Logf("pitch: n=%d mean=%.4fmm cv=%.3f", len(pitches), mean, cv)
	t.Logf("pitch quantiles mm: min=%.4f p1=%.4f p10=%.4f p50=%.4f p90=%.4f p99=%.4f max=%.4f",
		pitches[0], quantile(0.01), quantile(0.10), quantile(0.50),
		quantile(0.90), quantile(0.99), pitches[len(pitches)-1])
	t.Logf("within ±10%% of the nominal %.3fmm: %.1f%%",
		nominal, 100*float64(inBand)/float64(len(pitches)))
	const bin = 0.02
	capPitch := nominal * math.Sqrt2
	hist := make([]int, int(capPitch/bin)+2)
	for _, p := range pitches {
		hist[min(int(p/bin), len(hist)-1)]++
	}
	for i, n := range hist {
		if n == 0 {
			continue
		}
		t.Logf("%.2f-%.2fmm: %7d (%5.1f%%)",
			float64(i)*bin, float64(i+1)*bin, n, 100*float64(n)/float64(len(pitches)))
	}
}
