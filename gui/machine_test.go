package gui

import (
	"errors"
	"image"
	"testing"

	"seedhammer.com/bezier"
	"seedhammer.com/bip39"
	"seedhammer.com/bspline"
)

// TestPreviewAspect pins the preview raster's shape: one scale for
// both axes anchored at the shared plate width, so the square plate
// keeps its square raster and the small plate's carries the 85:55
// aspect.
func TestPreviewAspect(t *testing.T) {
	r := newSplineRasterizer(240, engraverParams, SquarePlate)
	if got, want := r.preview.Bounds().Size(), image.Pt(240, 240); got != want {
		t.Errorf("square raster %v, want %v", got, want)
	}
	r = newSplineRasterizer(240, engraverParams, SmallPlate)
	if got, want := r.preview.Bounds().Size(), image.Pt(240, 155); got != want {
		t.Errorf("small raster %v, want %v", got, want)
	}
	dims := image.Pt(480, 320)
	if small, square := previewSide(dims, SmallPlate), previewSide(dims, SquarePlate); small < square {
		t.Errorf("small preview width %d narrower than square %d", small, square)
	}
}

// TestSmallSeedGate pins which seed plates the small format takes:
// 12 words fit with the edge-rotated layout, while 24 words overflow
// the word column and must be rejected by the margin gate rather
// than lose words.
func TestSmallSeedGate(t *testing.T) {
	for _, test := range []struct {
		words int
		fits  bool
	}{
		{12, true},
		{24, false},
	} {
		plan, err := engraveSeed(engraverParams, SmallPlate, testMnemonic(t, test.words), "", "")
		if err != nil {
			t.Fatalf("%d words: %v", test.words, err)
		}
		if got := layoutFits(plan, engraverParams, SmallPlate); got != test.fits {
			t.Errorf("%d words on the small plate: fits=%v, want %v", test.words, got, test.fits)
		}
		if !test.fits {
			if _, err := toPlate(plan, engraverParams, SmallPlate); !errors.Is(err, ErrTooLarge) {
				t.Errorf("%d words: gate error %v, want ErrTooLarge", test.words, err)
			}
		}
	}
}

func testMnemonic(t *testing.T, words int) bip39.Mnemonic {
	t.Helper()
	m := make(bip39.Mnemonic, words)
	for j := range m {
		m[j] = bip39.Word(j)
	}
	return m.FixChecksum()
}

// TestMachinePlan pins the plate-to-machine contract: the machine
// origin is the square plate's top-left corner, so a plate's display
// spline stays in its own frame while the engraver's plan lives in
// the machine frame — and, the part a bounds check cannot see, the
// approach from the homing origin to the first engraved stroke must
// be budgeted travel: the stepper walks at most one step per tick and
// stamps the current segment's needle state on every tick, so a plan
// whose early ticks cannot cover the distance hammers the needle
// while still chasing the start position.
func TestMachinePlan(t *testing.T) {
	mm := engraverParams.Millimeter

	approach := func(spline bspline.Curve) (ticks uint, start bezier.Point, found bool) {
		var seg bspline.Segment
		for k := range spline {
			c, dt, engraved := seg.Knot(k)
			if engraved {
				return ticks, c.C0, true
			}
			ticks += dt
		}
		return ticks, bezier.Point{}, false
	}
	chebyshev := func(p bezier.Point) uint {
		dx, dy := p.X, p.Y
		if dx < 0 {
			dx = -dx
		}
		if dy < 0 {
			dy = -dy
		}
		return uint(max(dx, dy))
	}

	square, err := validateText(engraverParams, SquarePlate, "MACHINE FRAME")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := bspline.Measure(square.Machine).Bounds, bspline.Measure(square.Spline).Bounds; got != want {
		t.Errorf("square machine plan moved: %v, want %v", got, want)
	}

	small, err := validateText(engraverParams, SmallPlate, "MACHINE FRAME")
	if err != nil {
		t.Fatal(err)
	}
	local := bspline.Measure(small.Spline).Bounds
	machine := bspline.Measure(small.Machine).Bounds
	dy := (SquarePlate.Dims().Y - SmallPlate.Dims().Y) * mm
	if want := 30 * mm; dy != want {
		t.Fatalf("height difference %d units, want %d", dy, want)
	}
	if got, want := machine.Min.Y, local.Min.Y+dy; got != want {
		t.Errorf("machine min Y %d, want %d", got, want)
	}
	// The engraving must land on the small stock: below the removed
	// top 30mm, above the bottom edge, margins included.
	if got, want := machine.Min.Y, 33*mm; got < want {
		t.Errorf("machine min Y %d enters the removed top band (< %d)", got, want)
	}
	if got, want := machine.Max.Y, 82*mm; got > want {
		t.Errorf("machine max Y %d passes the safety margin (> %d)", got, want)
	}

	// The contract the needle depends on: enough ticks before the
	// first engraved stroke to cover the distance from the homing
	// origin at one step per tick.
	for _, plate := range []Plate{square, small} {
		ticks, start, found := approach(plate.Machine)
		if !found {
			t.Fatalf("%v machine plan engraves nothing", plate.Size)
		}
		if need := chebyshev(start); ticks < need {
			t.Errorf("%v: %d approach ticks cannot cover %d units to the first stroke; the needle would fire mid-travel",
				plate.Size, ticks, need)
		}
	}
}
