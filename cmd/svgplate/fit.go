package main

import (
	"math"

	"seedhammer.com/curves"
)

// placement describes how source geometry lands on the plate.
type placement struct {
	heightMM float64 // target height; 0 fits the larger side to the margin box.
	rotate   float64 // whole-drawing rotation in degrees.
	posX     float64 // top-left of the placed bounds in mm; NaN centers.
	posY     float64
}

// fitBoxMM is the square inside the safety margin the auto-fit targets.
//
// It holds back one payload unit on each side. A placed shape rounds
// twice on its way to the wire, once for its outline in its own frame
// and once for its placement, so a point can land a whole unit outside
// where the float fit put it. Without the slack an auto-fit lands the
// drawing exactly on the safety margin and that rounding tips it over,
// failing a drawing that fits.
const fitSlackMM = 1.0 / payloadUnitsPerMM
const fitBoxMM = curves.PlateMM - 2*curves.SafetyMarginMM - 2*fitSlackMM

// layoutOnPlate rotates, scales and positions a source drawing onto
// the plate, returning millimeters with (0,0) at the top-left corner.
// SVG's y-axis already points down like the plate's, so no flip is
// needed. The drawing stays instanced throughout: the fit is one
// affine transform, and an affine transform of a placed shape is the
// same shape placed somewhere else.
func layoutOnPlate(d *drawing, pl placement) *drawing {
	if pl.rotate != 0 {
		d = d.transform(rotateM(pl.rotate))
	}
	b := d.bounds()
	if b.empty {
		return d
	}
	w, h := b.width(), b.height()
	var s float64
	switch {
	case pl.heightMM > 0 && h > 0:
		s = pl.heightMM / h
	case math.Max(w, h) > 0:
		s = fitBoxMM / math.Max(w, h)
	default:
		s = 1
	}
	sw, sh := w*s, h*s
	var tx, ty float64
	if math.IsNaN(pl.posX) {
		tx = (curves.PlateMM-sw)/2 - b.min.X*s
		ty = (curves.PlateMM-sh)/2 - b.min.Y*s
	} else {
		tx = pl.posX - b.min.X*s
		ty = pl.posY - b.min.Y*s
	}
	return d.transform(translateM(tx, ty).mul(scaleM(s, s)))
}
