package svgpath

import (
	"fmt"
	"math"
	"testing"

	"seedhammer.com/bezier"
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
