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
	if got := SquarePlate.Dims(mm).X; got != curves.PlateMM*mm {
		t.Errorf("plate side %d, curves.PlateMM implies %d", got, curves.PlateMM*mm)
	}
	if safetyMargin != curves.SafetyMarginMM {
		t.Errorf("safetyMargin %d, curves.SafetyMarginMM %d", safetyMargin, curves.SafetyMarginMM)
	}
}
