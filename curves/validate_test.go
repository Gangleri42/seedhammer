package curves

import (
	"strings"
	"testing"

	"seedhammer.com/bezier"
	"seedhammer.com/bspline"
)

// TestValidateBoundsIncludeTravel guards that Validate rejects a
// drawing whose travel leaves the plate, exactly as the firmware's
// gui/curves.go does by checking the drawing's full knot hull. A
// trailing MoveTo has no engraved geometry after it, so the planned
// spline can drop it; the hull must still catch it. With this params'
// 400 units/mm, y=40000 payload units is 100mm, off the 85mm plate.
func TestValidateBoundsIncludeTravel(t *testing.T) {
	d, err := Parse(payload("M 2000 2000 L 5000 5000 M 2000 40000"), params)
	if err != nil {
		t.Fatal(err)
	}
	r, verr := d.Validate(params)
	if verr == nil {
		t.Fatalf("Validate accepted an off-plate travel move (bounds %+v)", r.Bounds)
	}
	if !strings.Contains(verr.Error(), "margin") {
		t.Errorf("want a plate/margin rejection, got %v", verr)
	}
	// The reported bounds must be the full hull the firmware checks.
	mm := params.Millimeter
	margin := bezier.Pt(SafetyMarginMM*mm, SafetyMarginMM*mm)
	plate := bezier.Pt(PlateMM*mm, PlateMM*mm)
	box := bspline.Bounds{Min: margin, Max: plate.Sub(margin)}
	if d.Bounds.In(box) {
		t.Errorf("test payload should have an off-plate hull, got %+v", d.Bounds)
	}
}
