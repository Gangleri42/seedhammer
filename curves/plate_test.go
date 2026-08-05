package curves

import (
	"strings"
	"testing"

	"seedhammer.com/bezier"
	"seedhammer.com/engrave"
	"seedhammer.com/svgpath"
)

// TestPlateToken pins the optional trailing plate token: emitters
// stamp it with WithPlate, both modes carry it in the header, the
// decoder surfaces it, and Validate checks the named plate's own box.
func TestPlateToken(t *testing.T) {
	params := engrave.SH2Params
	seg := func(op svgpath.SegmentOp, x, y int) svgpath.Segment {
		return svgpath.Segment{Op: op, Args: [4]bezier.Point{bezier.Pt(x, y)}}
	}
	// A tall stroke: 10..60mm in payload units (10/mm), inside the
	// square plate, below the small plate's floor.
	tall := []svgpath.Segment{seg(svgpath.MoveTo, 100, 100), seg(svgpath.LineTo, 100, 600)}
	payload, err := EncodePath(10, 3, tall)
	if err != nil {
		t.Fatal(err)
	}

	if got := PlateToken(payload); got != "" {
		t.Errorf("legacy payload names plate %q", got)
	}
	d, err := Parse(payload, params)
	if err != nil {
		t.Fatal(err)
	}
	if d.Plate != "" {
		t.Errorf("legacy payload decodes plate %q", d.Plate)
	}
	if _, err := d.Validate(params); err != nil {
		t.Errorf("tall drawing must fit the square plate: %v", err)
	}

	small, err := WithPlate(payload, PlateSmall)
	if err != nil {
		t.Fatal(err)
	}
	if got := PlateToken(small); got != PlateSmall {
		t.Errorf("PlateToken = %q, want small", got)
	}
	d, err = Parse(small, params)
	if err != nil {
		t.Fatal(err)
	}
	if d.Plate != PlateSmall {
		t.Errorf("decoded plate %q, want small", d.Plate)
	}
	if _, err := d.Validate(params); err == nil || !strings.Contains(err.Error(), "85x55") {
		t.Errorf("tall drawing on the small plate: err = %v, want the 85x55 box named", err)
	}

	// Re-stamping replaces the token instead of stacking a second one.
	square, err := WithPlate(small, PlateSquare)
	if err != nil {
		t.Fatal(err)
	}
	if got := PlateToken(square); got != PlateSquare {
		t.Errorf("re-stamped token = %q, want square", got)
	}
	d, err = Parse(square, params)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Validate(params); err != nil {
		t.Errorf("square-stamped tall drawing must fit: %v", err)
	}

	// An unknown token is rejected, not ignored.
	bad := append([]byte("2 path 10 3 tiny\n"), payload[len("2 path 10 3\n"):]...)
	if _, err := Open(bad, params); err == nil {
		t.Error("unknown plate token accepted")
	}

	// Text mode carries the token in the same trailing position.
	text := []byte("2 text\nIN CASE OF FIRE")
	stamped, err := WithPlate(text, PlateSmall)
	if err != nil {
		t.Fatal(err)
	}
	if got := PlateToken(stamped); got != PlateSmall {
		t.Errorf("text token = %q, want small", got)
	}
	if body, err := Text(stamped); err != nil || body != "IN CASE OF FIRE" {
		t.Errorf("stamped text body = %q, %v", body, err)
	}
}
