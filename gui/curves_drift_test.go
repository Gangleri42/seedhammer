package gui

import (
	"testing"

	"seedhammer.com/curves"
)

// TestCurvesGeometryNoDrift guards the plate geometry the curves
// package mirrors for the host converter against the firmware's own
// constants. If the plate or margin ever change here, curves.PlateMM
// and curves.SafetyMarginMM must change with them.
func TestCurvesGeometryNoDrift(t *testing.T) {
	const mm = 1000
	if got := plateDims(SquarePlate, mm).X; got != curves.PlateMM*mm {
		t.Errorf("plate side %d, curves.PlateMM implies %d", got, curves.PlateMM*mm)
	}
	// The small plate shares the square plate's width, so the
	// converter's outer bound stays valid for both.
	if got := plateDims(SmallPlate, mm).X; got != curves.PlateMM*mm {
		t.Errorf("small plate width %d, curves.PlateMM implies %d", got, curves.PlateMM*mm)
	}
	// A payload can name the small plate, so the wire package carries
	// its height too.
	if got := plateDims(SmallPlate, mm).Y; got != curves.SmallPlateHMM*mm {
		t.Errorf("small plate height %d, curves.SmallPlateHMM implies %d", got, curves.SmallPlateHMM*mm)
	}
	if safetyMargin != curves.SafetyMarginMM {
		t.Errorf("safetyMargin %d, curves.SafetyMarginMM %d", safetyMargin, curves.SafetyMarginMM)
	}
}
