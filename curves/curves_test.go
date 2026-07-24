package curves

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"seedhammer.com/bezier"
	"seedhammer.com/bspline"
	"seedhammer.com/engrave"
)

// params with 400 machine units per mm so that a units-per-mm of 400
// makes payload units and machine units coincide.
var params = engrave.Params{
	StrokeWidth: 120,
	Millimeter:  400,
	StepperConfig: engrave.StepperConfig{
		Speed:          12000,
		EngravingSpeed: 3200,
		Acceleration:   100000,
		Jerk:           1000000,
		TicksPerSecond: 12000,
	},
}

func payload(path string) []byte {
	return []byte("1 path 400 120\n" + path)
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{"no header", "M 0 0 L 1 1"},
		{"short header", "1 path 400\nM 0 0 L 800 800"},
		{"unsupported version", "3 path 400 120\nM 0 0 L 800 800"},
		{"zero units", "1 path 0 120\nM 0 0 L 800 800"},
		{"negative units", "1 path -400 120\nM 0 0 L 800 800"},
		{"stroke too narrow", "1 path 400 60\nM 0 0 L 800 800"},
		{"stroke too wide", "1 path 400 240\nM 0 0 L 800 800"},
		{"relative command", "1 path 400 120\nM 0 0 l 800 800"},
		{"horizontal command", "1 path 400 120\nM 0 0 H 800"},
		{"smooth command", "1 path 400 120\nM 0 0 C 1 2 3 4 5 6 S 7 8 9 10"},
		{"arc command", "1 path 400 120\nM 0 0 A 1 1 0 0 0 2 2"},
		{"exponent", "1 path 400 120\nM 0 0 L 8e2 800"},
		{"starts with line", "1 path 400 120\nL 800 800"},
		{"coordinates only", "1 path 400 120\n800 800"},
		{"incomplete group", "1 path 400 120\nM 0 0 C 1 2 3 4"},
		{"empty path", "1 path 400 120\n"},
		{"move only", "1 path 400 120\nM 400 400"},
		{"zero-length drawing", "1 path 400 120\nM 400 400 L 400 400 C 400 400 400 400 400 400"},
	}
	for _, test := range tests {
		if _, err := Parse([]byte(test.data), params); err == nil {
			t.Errorf("%s: Parse succeeded, want error", test.name)
		}
	}
}

// TestParseHostile covers payloads crafted to crash the device: none
// may panic, and the degenerate ones must return an error.
func TestParseHostile(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		wantErr bool
	}{
		// A cubic whose control point escapes the degenerate filter
		// but whose whole arc rounds to a single sample point. Must
		// not reach the fitter with a one-point run.
		{"collapsing cubic", "1 path 400 120\nM 0 0 C 1 0 0 0 0 0", true},
		{"collapsing cubic mid", "1 path 400 120\nM 100 100 C 101 100 99 100 100 100", true},
		// Coordinates that scale past the fixed-point range would
		// overflow the sampler or divide by zero. Clamped to a safe
		// magnitude, so Parse succeeds but the plate bounds land far
		// outside the plate for the caller to reject.
		{"overflow coordinates", "1 path 3 1\nM 0 0 L 2000000 2000000 L 0 0 Z", false},
	}
	for _, test := range tests {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s: Parse panicked: %v", test.name, r)
				}
			}()
			d, err := Parse([]byte(test.data), params)
			switch {
			case test.wantErr && err == nil:
				t.Errorf("%s: Parse succeeded, want error", test.name)
			case !test.wantErr && err != nil:
				t.Errorf("%s: Parse failed: %v", test.name, err)
			case !test.wantErr && d.Bounds.Max.X <= 85*params.Millimeter:
				t.Errorf("%s: bounds %v not clamped out of the plate", test.name, d.Bounds)
			}
		}()
	}
}

// TestParseUnboundedStroke bounds the memory a single pathological
// stroke can consume: one smooth stroke of many gentle curves must
// fail cheaply, not exhaust memory sampling it.
func TestParseUnboundedStroke(t *testing.T) {
	const (
		l  = 4000
		dy = 200
		n  = 250
	)
	var b strings.Builder
	b.WriteString("1 path 400 120\nM 0 17000")
	for i := 0; i < n; i++ {
		x0 := i * l
		// Each cubic bows the same way, so consecutive tangents turn
		// well under 45 degrees and the run is never split by a corner
		// clamp; the whole path is one continuous stroke.
		fmt.Fprintf(&b, " C %d %d %d %d %d %d",
			x0+l/3, 17000+dy, x0+2*l/3, 17000+dy, x0+l, 17000)
	}
	if _, err := Parse([]byte(b.String()), params); err == nil {
		t.Error("Parse accepted a pathologically long stroke")
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		strokes int
		bounds  [4]int
	}{
		{"line", "M 400 400 L 1200 400", 1, [4]int{400, 400, 1200, 400}},
		{"square", "M 400 400 L 1200 400 L 1200 1200 L 400 1200 Z", 1, [4]int{400, 400, 1200, 1200}},
		{"two strokes", "M 400 400 L 1200 400 M 400 800 L 1200 800", 2, [4]int{400, 400, 1200, 800}},
		{"cubic", "M 400 400 C 400 1200 1200 1200 1200 400", 1, [4]int{400, 400, 1200, 1000}},
		{"quadratic", "M 400 400 Q 800 1200 1200 400", 1, [4]int{400, 400, 1200, 800}},
		{"implicit lineto", "M 400 400 1200 400 1200 1200", 1, [4]int{400, 400, 1200, 1200}},
	}
	for _, test := range tests {
		d, err := Parse(payload(test.path), params)
		if err != nil {
			t.Errorf("%s: Parse: %v", test.name, err)
			continue
		}
		if d.Strokes != test.strokes {
			t.Errorf("%s: %d strokes, want %d", test.name, d.Strokes, test.strokes)
		}
		if d.Knots == 0 || d.MaxStrokeKnots == 0 || d.MaxStrokeKnots > d.Knots {
			t.Errorf("%s: inconsistent knot counts: %d total, %d max per stroke", test.name, d.Knots, d.MaxStrokeKnots)
		}
		got := [4]int{d.Bounds.Min.X, d.Bounds.Min.Y, d.Bounds.Max.X, d.Bounds.Max.Y}
		if got != test.bounds {
			t.Errorf("%s: bounds %v, want %v", test.name, got, test.bounds)
		}
	}
}

func TestUnitsPerMM(t *testing.T) {
	// The same drawing at 100 units per mm spans 4 times the machine
	// units, with a proportionally scaled stroke width.
	d, err := Parse([]byte("1 path 100 30\nM 100 100 L 300 100"), params)
	if err != nil {
		t.Fatal(err)
	}
	if want := bezier.Pt(1200, 400); d.Bounds.Max != want {
		t.Errorf("bounds max %v, want %v", d.Bounds.Max, want)
	}
}

func TestEngravingReplay(t *testing.T) {
	d, err := Parse(payload("M 400 400 C 400 1200 1200 1200 1200 400 L 2000 400 Z"), params)
	if err != nil {
		t.Fatal(err)
	}
	collect := func() []engrave.Command {
		var cmds []engrave.Command
		for cmd := range d.Engraving() {
			cmds = append(cmds, cmd)
		}
		return cmds
	}
	first, second := collect(), collect()
	if !slices.Equal(first, second) {
		t.Errorf("replay differs: %d vs %d commands", len(first), len(second))
	}
	if len(first) != d.Knots {
		t.Errorf("engraving has %d commands, Parse counted %d knots", len(first), d.Knots)
	}
	// Early termination must not disturb subsequent replays.
	for range d.Engraving() {
		break
	}
	if !slices.Equal(collect(), first) {
		t.Error("replay differs after early termination")
	}
}

func TestCornerClamps(t *testing.T) {
	// A 90° corner inside a polyline is clamped: the knot stream
	// passes exactly through the corner as a tripled knot.
	d, err := Parse(payload("M 400 400 L 1200 400 L 1200 1200"), params)
	if err != nil {
		t.Fatal(err)
	}
	corner := bezier.Pt(1200, 400)
	triples := 0
	var run []bezier.Point
	for cmd := range d.Engraving() {
		k, ok := cmd.AsKnot()
		if !ok {
			t.Fatal("non-knot command")
		}
		run = append(run, k.Knot)
		if n := len(run); n >= 3 && run[n-1] == corner && run[n-2] == corner && run[n-3] == corner {
			triples++
		}
	}
	if triples != 1 {
		t.Errorf("corner clamped %d times, want 1", triples)
	}
}

func TestSmoothJoinNotClamped(t *testing.T) {
	// Two cubics meeting with a straight tangent are one smooth
	// stroke: no interior clamp at the join.
	d, err := Parse(payload("M 400 400 C 400 800 800 800 1200 800 C 1600 800 2000 800 2000 400"), params)
	if err != nil {
		t.Fatal(err)
	}
	join := bezier.Pt(1200, 800)
	var prev bezier.Point
	eq := 0
	for cmd := range d.Engraving() {
		k, _ := cmd.AsKnot()
		if k.Knot == join && k.Knot == prev {
			eq++
		}
		prev = k.Knot
	}
	if eq > 0 {
		t.Errorf("smooth join emitted %d repeated knots, want 0", eq)
	}
}

func TestStrokeWidthTolerance(t *testing.T) {
	// Within an eighth of the machine stroke width passes.
	for _, w := range []string{"110", "120", "130"} {
		data := []byte("1 path 400 " + w + "\nM 400 400 L 1200 400")
		if _, err := Parse(data, params); err != nil {
			t.Errorf("stroke width %s: %v", w, err)
		}
	}
}

func TestPlanEngraving(t *testing.T) {
	// The converted drawing must survive the machine planner.
	d, err := Parse(payload("M 400 400 L 1200 400 L 1200 1200 C 1200 2000 400 2000 400 1200 Z"), params)
	if err != nil {
		t.Fatal(err)
	}
	spline := engrave.PlanEngraving(params.StepperConfig, d.Engraving())
	knots, engraved := 0, 0
	for k := range spline {
		knots++
		if k.Engrave {
			engraved++
		}
	}
	if knots == 0 || engraved == 0 {
		t.Fatalf("planned spline has %d knots, %d engraved", knots, engraved)
	}
}

func TestRecordType(t *testing.T) {
	if !strings.HasPrefix(RecordType, "seedhammer.com:") {
		t.Errorf("RecordType %q is not in the seedhammer.com namespace", RecordType)
	}
}

func TestMode(t *testing.T) {
	tests := []struct {
		data string
		mode string
		err  bool
	}{
		{"1 text\nHELLO", ModeText, false},
		{"1 path 400 120\nM 0 0 L 1 1", ModePath, false},
		{"1 text", ModeText, false},
		{"", "", true},
		{"1", "", true},
		{"2 text\nHELLO", ModeText, false},
		{"3 text\nHELLO", "", true},
		{"1 draw\nx", "", true},
	}
	for _, test := range tests {
		mode, err := Mode([]byte(test.data))
		if (err != nil) != test.err {
			t.Errorf("Mode(%q) err = %v, want err=%v", test.data, err, test.err)
		}
		if mode != test.mode {
			t.Errorf("Mode(%q) = %q, want %q", test.data, mode, test.mode)
		}
	}
}

func TestText(t *testing.T) {
	got, err := Text([]byte("1 text\nIN CASE OF FIRE\nBREAK GLASS"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "IN CASE OF FIRE\nBREAK GLASS"; got != want {
		t.Errorf("Text = %q, want %q", got, want)
	}
	if _, err := Text([]byte("1 path 400 120\nM 0 0 L 1 1")); err == nil {
		t.Error("Text accepted a path payload")
	}
}

func TestParseRejectsTextMode(t *testing.T) {
	// Parse is the path decoder; a text payload must not slip through
	// it and be read as geometry.
	if _, err := Parse([]byte("1 text\nHELLO"), params); err == nil {
		t.Error("Parse accepted a text-mode payload")
	}
}

// TestPeriodicCircle runs the 70mm bench circle through the full
// device pipeline at SH2-realistic parameters: a closed smooth
// contour must engrave as one periodic loop near the velocity-limit
// floor, well under its clamped-era pace.
func TestPeriodicCircle(t *testing.T) {
	const mm = 6400
	sh2 := engrave.Params{
		Millimeter:  mm,
		StrokeWidth: 0.3 * mm,
		StepperConfig: engrave.StepperConfig{
			Speed:          30 * mm,
			EngravingSpeed: 8 * mm,
			Acceleration:   250 * mm,
			Jerk:           3900 * mm,
			TicksPerSecond: 30 * mm,
		},
	}
	circle := []byte("1 path 100 30\n" +
		"M4250 750 C6183 750 7750 2317 7750 4250 " +
		"C7750 6183 6183 7750 4250 7750 " +
		"C2317 7750 750 6183 750 4250 " +
		"C750 2317 2317 750 4250 750")
	d, err := Parse(circle, sh2)
	if err != nil {
		t.Fatal(err)
	}
	if d.Strokes != 1 {
		t.Errorf("circle parsed to %d strokes", d.Strokes)
	}
	periodic := false
	for c := range d.Engraving() {
		if k, ok := c.AsKnot(); ok && k.Periodic {
			periodic = true
			break
		}
	}
	if !periodic {
		t.Error("closed circle did not convert to a periodic contour")
	}
	var engraveDur, totalDur uint
	var seg bspline.Segment
	for k := range engrave.PlanEngraving(sh2.StepperConfig, d.Engraving()) {
		_, dt, eng := seg.Knot(k)
		totalDur += dt
		if eng {
			engraveDur += dt
		}
	}
	// The 219.4mm circle floors at 27.4s against the 8mm/s limit; the
	// clamped plan took 31.2s.
	tps := sh2.TicksPerSecond
	if lo, hi := 27*tps, 29*tps; engraveDur < lo || engraveDur > hi {
		t.Errorf("periodic circle engraves in %.2fs, want %d-%ds",
			float64(engraveDur)/float64(tps), lo/tps, hi/tps)
	}
	if totalDur < engraveDur {
		t.Error("total duration below engrave duration")
	}
}
