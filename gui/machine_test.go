package gui

import (
	"image"
	"testing"

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

// TestMachineSpline pins the plate-to-machine frame contract: the
// machine origin is the square plate's top-left corner, so a square
// plate hands its spline over untouched while a small plate shifts
// down by the height difference, landing inside the small stock's
// band of the machine frame.
func TestMachineSpline(t *testing.T) {
	mm := engraverParams.Millimeter

	square, err := validateText(engraverParams, SquarePlate, "MACHINE FRAME")
	if err != nil {
		t.Fatal(err)
	}
	if square.Size != SquarePlate {
		t.Errorf("square plate stamped %v", square.Size)
	}
	local := bspline.Measure(square.Spline)
	machine := bspline.Measure(machineSpline(engraverParams, square))
	if local.Bounds != machine.Bounds {
		t.Errorf("square plate moved: local %v, machine %v", local.Bounds, machine.Bounds)
	}

	small, err := validateText(engraverParams, SmallPlate, "MACHINE FRAME")
	if err != nil {
		t.Fatal(err)
	}
	if small.Size != SmallPlate {
		t.Errorf("small plate stamped %v", small.Size)
	}
	dy := (SquarePlate.Dims().Y - SmallPlate.Dims().Y) * mm
	if want := 30 * mm; dy != want {
		t.Fatalf("height difference %d units, want %d", dy, want)
	}
	local = bspline.Measure(small.Spline)
	machine = bspline.Measure(machineSpline(engraverParams, small))
	if got, want := machine.Bounds.Min.Y, local.Bounds.Min.Y+dy; got != want {
		t.Errorf("machine min Y %d, want %d", got, want)
	}
	if got, want := machine.Bounds.Max.Y, local.Bounds.Max.Y+dy; got != want {
		t.Errorf("machine max Y %d, want %d", got, want)
	}
	if machine.Bounds.Min.X != local.Bounds.Min.X || machine.Bounds.Max.X != local.Bounds.Max.X {
		t.Errorf("X moved: local %v, machine %v", local.Bounds, machine.Bounds)
	}
	// The engraving must land on the small stock: below the removed
	// top 30mm, above the bottom edge, margins included.
	if got, want := machine.Bounds.Min.Y, 33*mm; got < want {
		t.Errorf("machine min Y %d enters the removed top band (< %d)", got, want)
	}
	if got, want := machine.Bounds.Max.Y, 82*mm; got > want {
		t.Errorf("machine max Y %d passes the safety margin (> %d)", got, want)
	}
	if local.Duration != machine.Duration {
		t.Errorf("duration changed: %d to %d", local.Duration, machine.Duration)
	}
}
