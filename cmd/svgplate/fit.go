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
const fitBoxMM = curves.PlateMM - 2*curves.SafetyMarginMM

// layoutOnPlate rotates, scales and positions source segments onto the
// plate, returning segments in millimeters with (0,0) at the top-left
// corner. SVG's y-axis already points down like the plate's, so no
// flip is needed.
func layoutOnPlate(segs []fseg, pl placement) []fseg {
	if pl.rotate != 0 {
		r := rotateM(pl.rotate)
		rot := make([]fseg, len(segs))
		for i, s := range segs {
			rot[i] = s.transform(r)
		}
		segs = rot
	}
	b := segsBounds(segs)
	if b.empty {
		return segs
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
	m := translateM(tx, ty).mul(scaleM(s, s))
	out := make([]fseg, len(segs))
	for i, seg := range segs {
		out[i] = seg.transform(m)
	}
	return out
}
