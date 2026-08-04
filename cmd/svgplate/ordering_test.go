package main

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"seedhammer.com/bspline"
	"seedhammer.com/curves"
	"seedhammer.com/engrave"
)

// Travel ordering works on placements, so how a stamp is cut into
// placements decides what the orderer is allowed to move. Giving each
// shape inside a stamp its own placement keeps that freedom; making
// the whole stamp one placement would dedup slightly better and force
// every stamp to engrave as a unit. On the split cases below that cost
// 25-33% engrave time and roughly tripled travel, so these tests hold
// the instanced payload to the flat encoder's ordering.

// planCost returns the engrave duration and the pen-up travel of a
// payload, both from the planner the firmware runs.
func planCost(t *testing.T, payload []byte) (secs int, travelMM float64) {
	t.Helper()
	d, err := curves.Parse(payload, sh2)
	if err != nil {
		t.Fatal(err)
	}
	r, err := d.Validate(sh2)
	if err != nil {
		t.Fatal(err)
	}
	mm := float64(sh2.Millimeter)
	var seg bspline.Segment
	for k := range engrave.PlanEngraving(sh2.StepperConfig, d.Engraving()) {
		c, dt, engraved := seg.Knot(k)
		if dt == 0 || engraved {
			continue
		}
		travelMM += math.Hypot(float64(c.C3.X-c.C0.X), float64(c.C3.Y-c.C0.Y)) / mm
	}
	return r.Seconds, travelMM
}

// stampGrid builds n stamps of one target on a grid. target is the
// defs body; the caller decides how far apart the strokes inside a
// stamp sit, which is the whole variable under test.
func stampGrid(target string, n, cols, pitch int) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 800 800"><defs><g id="s">%s</g></defs>`, target)
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, `<use href="#s" x="%d" y="%d"/>`,
			60+(i%cols)*pitch, 60+(i/cols)*pitch)
	}
	b.WriteString(`</svg>`)
	return b.String()
}

func TestPlacementOrderingMatchesFlat(t *testing.T) {
	cases := []struct {
		name string
		doc  string
	}{
		{
			// The shape a real stamped logo has: strokes nested at the
			// same spot, so keeping them together is what you want.
			"concentric",
			stampGrid(`<rect x="-25" y="-35" width="50" height="70" rx="22"/>`+
				`<rect x="-8" y="-20" width="16" height="40" rx="8"/>`, 12, 4, 170),
		},
		{
			// Adversarial: one stamp's two strokes sit far apart, so
			// every stamp forces a long pen-up inside itself and its
			// far stroke sits next to a different stamp's.
			"split-wide",
			stampGrid(`<circle cx="0" cy="0" r="18"/>`+
				`<circle cx="300" cy="0" r="18"/>`, 10, 2, 140),
		},
		{
			// Worse: four strokes per stamp, scattered across the
			// stamp's own bounding box.
			"split-four",
			stampGrid(`<circle cx="0" cy="0" r="14"/>`+
				`<circle cx="260" cy="0" r="14"/>`+
				`<circle cx="0" cy="260" r="14"/>`+
				`<circle cx="260" cy="260" r="14"/>`, 8, 2, 120),
		},
		{
			// Many single-stroke stamps: grouping and flat order the
			// same units, so this is the control.
			"single-stroke",
			stampGrid(`<circle cx="0" cy="0" r="16"/>`, 36, 6, 110),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := extractSVG([]byte(tc.doc))
			if err != nil {
				t.Fatal(err)
			}
			d := layoutOnPlate(raw, placement{posX: math.NaN(), posY: math.NaN()}, 85, 85)

			grouped, _, gr, err := finishDrawing(d, true)
			if err != nil {
				t.Fatal(err)
			}
			flat, err := flatEncode(d.flatten(), true)
			if err != nil {
				t.Fatal(err)
			}
			unordered, err := flatEncode(d.flatten(), false)
			if err != nil {
				t.Fatal(err)
			}

			gs, gt := planCost(t, grouped)
			fs, ft := planCost(t, flat)
			us, ut := planCost(t, unordered)

			t.Logf("%-13s shapes=%d placements=%d strokes=%d", tc.name,
				len(d.shapes), len(d.groups), gr.Strokes)
			t.Logf("  placed    %4ds  travel %6.1fmm  %5d bytes", gs, gt, len(grouped))
			t.Logf("  flat      %4ds  travel %6.1fmm  %5d bytes", fs, ft, len(flat))
			t.Logf("  unordered %4ds  travel %6.1fmm  %5d bytes", us, ut, len(unordered))
			t.Logf("  placed vs flat: %+.1f%% time, %+.1f%% travel, %.2fx bytes",
				100*float64(gs-fs)/float64(fs),
				100*(gt-ft)/ft,
				float64(len(flat))/float64(len(grouped)))

			// Instancing must not cost engrave time. A second of slack
			// covers tie-breaking between the two orderers.
			if slack := max(1, fs/50); gs > fs+slack {
				t.Errorf("placed %ds against flat %ds: instancing cost ordering freedom", gs, fs)
			}
			if ft > 0 && gt > ft*1.05 {
				t.Errorf("placed travel %.1fmm against flat %.1fmm", gt, ft)
			}
			// Ordering must still be doing something, or the comparison
			// above proves nothing.
			if us < fs {
				t.Errorf("unordered %ds beat ordered %ds: the case is not exercising ordering", us, fs)
			}
			if len(grouped) >= len(flat) {
				t.Errorf("instanced payload %d not under flat %d", len(grouped), len(flat))
			}
		})
	}
}

func TestAutoFitClearsMarginAfterQuantization(t *testing.T) {
	// A placed shape rounds twice on its way to the wire, so a fit that
	// lands the drawing exactly on the safety margin tips outside it as
	// payload. The auto-fit holds a unit back for that; without the
	// slack this document converts to a drawing the validator rejects.
	for _, n := range []int{4, 12, 24} {
		raw, err := extractSVG([]byte(stampGrid(
			`<rect x="-25" y="-35" width="50" height="70" rx="22"/>`+
				`<rect x="-8" y="-20" width="16" height="40" rx="8"/>`, n, 4, 170)))
		if err != nil {
			t.Fatal(err)
		}
		d := layoutOnPlate(raw, placement{posX: math.NaN(), posY: math.NaN()}, 85, 85)
		if _, _, r, err := finishDrawing(d, true); err != nil {
			t.Errorf("%d stamps: %v (size %v)", n, err, r.Bounds)
		}
	}
}
