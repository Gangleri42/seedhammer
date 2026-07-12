package svgpath

import (
	"errors"
	"math"
	"testing"

	"seedhammer.com/bezier"
	"seedhammer.com/font/vector"
)

func TestParseData(t *testing.T) {
	identity := func(v float64) int {
		return int(math.Round(v))
	}
	tests := []struct {
		d          string
		offx, offy int
		scale      func(float64) int
		want       []Segment
	}{
		{
			d: "M 1 2 L 3 4",
			want: []Segment{
				seg(MoveTo, bezier.Pt(1, 2)),
				seg(LineTo, bezier.Pt(3, 4)),
			},
		},
		{
			d: "m 1 2 l 3 4",
			want: []Segment{
				seg(MoveTo, bezier.Pt(1, 2)),
				seg(LineTo, bezier.Pt(4, 6)),
			},
		},
		{
			d: "M 0 0 H 5 v 3 h -2 V 0",
			want: []Segment{
				seg(MoveTo, bezier.Pt(0, 0)),
				seg(LineTo, bezier.Pt(5, 0)),
				seg(LineTo, bezier.Pt(5, 3)),
				seg(LineTo, bezier.Pt(3, 3)),
				seg(LineTo, bezier.Pt(3, 0)),
			},
		},
		{
			d: "M 0 0 C 1 2 3 4 5 6",
			want: []Segment{
				seg(MoveTo, bezier.Pt(0, 0)),
				seg(CubeTo, bezier.Pt(1, 2), bezier.Pt(3, 4), bezier.Pt(5, 6)),
			},
		},
		{
			d: "M 10 10 c 1 2 3 4 5 6",
			want: []Segment{
				seg(MoveTo, bezier.Pt(10, 10)),
				seg(CubeTo, bezier.Pt(11, 12), bezier.Pt(13, 14), bezier.Pt(15, 16)),
			},
		},
		{
			// S reflects the previous cubic's second control point.
			d: "M 0 0 C 1 1 2 1 3 0 S 5 -1 6 0",
			want: []Segment{
				seg(MoveTo, bezier.Pt(0, 0)),
				seg(CubeTo, bezier.Pt(1, 1), bezier.Pt(2, 1), bezier.Pt(3, 0)),
				seg(CubeTo, bezier.Pt(4, -1), bezier.Pt(5, -1), bezier.Pt(6, 0)),
			},
		},
		{
			// Z closes back to the initial point of the subpath.
			d: "M 1 1 L 5 1 L 5 5 Z",
			want: []Segment{
				seg(MoveTo, bezier.Pt(1, 1)),
				seg(LineTo, bezier.Pt(5, 1)),
				seg(LineTo, bezier.Pt(5, 5)),
				seg(LineTo, bezier.Pt(1, 1)),
			},
		},
		{
			d: "M 0 0 Q 6 0 6 6",
			want: []Segment{
				seg(MoveTo, bezier.Pt(0, 0)),
				seg(QuadTo, bezier.Pt(6, 0), bezier.Pt(6, 6)),
			},
		},
		{
			d: "M 10 10 q 1 2 3 4",
			want: []Segment{
				seg(MoveTo, bezier.Pt(10, 10)),
				seg(QuadTo, bezier.Pt(11, 12), bezier.Pt(13, 14)),
			},
		},
		{
			// Pairs following a moveto pair are implicit linetos.
			d: "M 1 2 3 4 5 6",
			want: []Segment{
				seg(MoveTo, bezier.Pt(1, 2)),
				seg(LineTo, bezier.Pt(3, 4)),
				seg(LineTo, bezier.Pt(5, 6)),
			},
		},
		{
			d: "m 1 2 3 4 Z",
			want: []Segment{
				seg(MoveTo, bezier.Pt(1, 2)),
				seg(LineTo, bezier.Pt(4, 6)),
				seg(LineTo, bezier.Pt(1, 2)),
			},
		},
		{
			d:    "M 1 1 l 1 0",
			offx: 10, offy: 20,
			want: []Segment{
				seg(MoveTo, bezier.Pt(11, 21)),
				seg(LineTo, bezier.Pt(12, 21)),
			},
		},
		{
			d: "M 1.5 2",
			scale: func(v float64) int {
				return int(math.Round(v * 2))
			},
			want: []Segment{
				seg(MoveTo, bezier.Pt(3, 4)),
			},
		},
	}
	for _, test := range tests {
		scale := test.scale
		if scale == nil {
			scale = identity
		}
		got, err := ParseData(test.d, test.offx, test.offy, scale)
		if err != nil {
			t.Errorf("ParseData(%q): %v", test.d, err)
			continue
		}
		if !segsEqual(got, test.want) {
			t.Errorf("ParseData(%q) = %v, want %v", test.d, got, test.want)
		}
	}
	errTests := []string{
		// Arc commands are unsupported.
		"M 0 0 A 1 1 0 0 0 2 2",
		// Odd number of coordinates.
		"M 1",
		// Incomplete coordinate groups.
		"M 0 0 C 1 2 3 4",
		"M 0 0 Q 1 2 3",
		// Coordinates before any command.
		"1 2 M 0 0",
	}
	for _, d := range errTests {
		if _, err := ParseData(d, 0, 0, identity); err == nil {
			t.Errorf("ParseData(%q) succeeded, want error", d)
		}
	}
}

func TestOptimize(t *testing.T) {
	tests := []struct {
		name       string
		segs, want []Segment
	}{
		{
			name: "zero-length line dropped",
			segs: []Segment{
				seg(MoveTo, bezier.Pt(1, 1)),
				seg(LineTo, bezier.Pt(1, 1)),
				seg(LineTo, bezier.Pt(2, 1)),
			},
			want: []Segment{
				seg(MoveTo, bezier.Pt(1, 1)),
				seg(LineTo, bezier.Pt(2, 1)),
			},
		},
		{
			// A move to the initial pen position (0, 0) is also
			// a zero-length segment.
			name: "zero-length move dropped",
			segs: []Segment{
				seg(MoveTo, bezier.Pt(0, 0)),
				seg(LineTo, bezier.Pt(2, 0)),
			},
			want: []Segment{
				seg(LineTo, bezier.Pt(2, 0)),
			},
		},
		{
			name: "colinear lines merged",
			segs: []Segment{
				seg(MoveTo, bezier.Pt(0, 1)),
				seg(LineTo, bezier.Pt(1, 1)),
				seg(LineTo, bezier.Pt(2, 1)),
				seg(LineTo, bezier.Pt(2, 5)),
			},
			want: []Segment{
				seg(MoveTo, bezier.Pt(0, 1)),
				seg(LineTo, bezier.Pt(2, 1)),
				seg(LineTo, bezier.Pt(2, 5)),
			},
		},
		{
			name: "quad expanded to cubic",
			segs: []Segment{
				seg(MoveTo, bezier.Pt(6, 6)),
				seg(QuadTo, bezier.Pt(3, 3), bezier.Pt(6, 0)),
			},
			want: []Segment{
				seg(MoveTo, bezier.Pt(6, 6)),
				seg(CubeTo, bezier.Pt(4, 4), bezier.Pt(4, 2), bezier.Pt(6, 0)),
			},
		},
		{
			name: "degenerate cubic converted to line",
			segs: []Segment{
				seg(MoveTo, bezier.Pt(0, 3)),
				seg(CubeTo, bezier.Pt(1, 3), bezier.Pt(2, 3), bezier.Pt(3, 3)),
			},
			want: []Segment{
				seg(MoveTo, bezier.Pt(0, 3)),
				seg(LineTo, bezier.Pt(3, 3)),
			},
		},
	}
	for _, test := range tests {
		if got := Optimize(test.segs); !segsEqual(got, test.want) {
			t.Errorf("%s: Optimize = %v, want %v", test.name, got, test.want)
		}
	}
}

func TestToBSpline(t *testing.T) {
	segs := []Segment{
		seg(MoveTo, bezier.Pt(0, 0)),
		seg(CubeTo, bezier.Pt(0, 100), bezier.Pt(100, 100), bezier.Pt(100, 0)),
	}
	_, spline, err := ToBSpline(segs, 50, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []vector.Knot{
		{Ctrl: bezier.Pt(0, 0)},
		{Ctrl: bezier.Pt(0, 0)},
		{Ctrl: bezier.Pt(0, 0)},
		{Ctrl: bezier.Pt(3, 23), Line: true},
		{Ctrl: bezier.Pt(22, 68), Line: true},
		{Ctrl: bezier.Pt(78, 68), Line: true},
		{Ctrl: bezier.Pt(97, 23), Line: true},
		{Ctrl: bezier.Pt(100, 0), Line: true},
		{Ctrl: bezier.Pt(100, 0), Line: true},
		{Ctrl: bezier.Pt(100, 0), Line: true},
	}
	if len(spline) != len(want) {
		t.Fatalf("ToBSpline returned %d knots, want %d: %v", len(spline), len(want), spline)
	}
	for i, k := range spline {
		if k != want[i] {
			t.Errorf("knot %d: got %v, want %v", i, k, want[i])
		}
	}
}

func TestBuilderControlFit(t *testing.T) {
	// A cubic bows away from the chord; ControlFit keeps the samples
	// as control points between the clamps.
	segs := []Segment{
		seg(MoveTo, bezier.Pt(0, 0)),
		seg(CubeTo, bezier.Pt(0, 100), bezier.Pt(100, 100), bezier.Pt(100, 0)),
	}
	var spline []vector.Knot
	b := NewBuilder(50, false, ControlFit(), func(k vector.Knot) bool {
		spline = append(spline, k)
		return true
	})
	for _, s := range segs {
		if !b.Add(s) {
			t.Fatal("Builder rejected segment")
		}
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	n := len(spline)
	if n < 7 {
		t.Fatalf("Builder emitted %d knots, want at least 7", n)
	}
	start, end := spline[0], spline[n-1]
	for i := 0; i < 3; i++ {
		if spline[i] != start || spline[n-1-i] != end {
			t.Errorf("boundary knots are not tripled: %v", spline)
		}
	}
	if want := bezier.Pt(0, 0); start.Ctrl != want {
		t.Errorf("spline starts at %v, want %v", start.Ctrl, want)
	}
	if want := bezier.Pt(100, 0); end.Ctrl != want {
		t.Errorf("spline ends at %v, want %v", end.Ctrl, want)
	}
	for _, k := range spline[3 : n-3] {
		if !k.Line {
			t.Errorf("interior knot %v is not engraved", k)
		}
		if k.Ctrl.Y <= 0 {
			t.Errorf("interior knot %v does not follow the curve", k)
		}
	}
}

func TestBuilderDegenerateRun(t *testing.T) {
	// A cubic whose samples all collapse onto its start point must
	// not reach the fitter (which slices samples[1:len-1]).
	segs := []Segment{
		seg(MoveTo, bezier.Pt(0, 0)),
		seg(CubeTo, bezier.Pt(1, 0), bezier.Pt(0, 0), bezier.Pt(0, 0)),
	}
	var spline []vector.Knot
	b := NewBuilder(120, true, ControlFit(), func(k vector.Knot) bool {
		spline = append(spline, k)
		return true
	})
	for _, s := range segs {
		b.Add(s)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// The collapsed run yields no engraved knots; only the move clamp.
	for _, k := range spline {
		if k.Line {
			t.Errorf("degenerate run emitted an engraved knot: %v", spline)
			break
		}
	}
}

func TestBuilderLimitRun(t *testing.T) {
	limit := errors.New("too long")
	var knots int
	b := NewBuilder(1, false, ControlFit(), func(vector.Knot) bool {
		knots++
		return true
	})
	b.LimitRun(64, limit)
	b.Add(seg(MoveTo, bezier.Pt(0, 0)))
	// A long curve samples to far more than the cap.
	b.Add(seg(CubeTo, bezier.Pt(0, 4000), bezier.Pt(8000, 4000), bezier.Pt(8000, 0)))
	if err := b.Close(); err != limit {
		t.Fatalf("Close = %v, want %v", err, limit)
	}
}

func TestBuilderZeroLengthLineClamps(t *testing.T) {
	// A zero-length line splits the stroke with a clamp, without
	// lifting the needle.
	segs := []Segment{
		seg(MoveTo, bezier.Pt(0, 0)),
		seg(CubeTo, bezier.Pt(0, 50), bezier.Pt(50, 100), bezier.Pt(100, 100)),
		seg(LineTo, bezier.Pt(100, 100)),
		seg(CubeTo, bezier.Pt(150, 100), bezier.Pt(200, 50), bezier.Pt(200, 0)),
	}
	var spline []vector.Knot
	b := NewBuilder(50, true, ControlFit(), func(k vector.Knot) bool {
		spline = append(spline, k)
		return true
	})
	for _, s := range segs {
		if !b.Add(s) {
			t.Fatal("Builder rejected segment")
		}
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	clamp := vector.Knot{Ctrl: bezier.Pt(100, 100), Line: true}
	triples := 0
	for i := 0; i+2 < len(spline); i++ {
		if spline[i] == clamp && spline[i+1] == clamp && spline[i+2] == clamp {
			triples++
		}
	}
	if triples != 1 {
		t.Errorf("found %d corner clamp triples, want 1: %v", triples, spline)
	}
}

// segsEqual compares only the arguments significant to each
// segment's op; converted segments may leave stale trailing
// arguments behind.
func segsEqual(a, b []Segment) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Op != b[i].Op {
			return false
		}
		n := 1
		switch a[i].Op {
		case QuadTo:
			n = 2
		case CubeTo:
			n = 3
		}
		for j := range n {
			if a[i].Args[j] != b[i].Args[j] {
				return false
			}
		}
	}
	return true
}
