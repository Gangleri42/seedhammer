package svgpath

import (
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
		// Quadratic and arc commands are unsupported.
		"M 0 0 Q 1 2 3 4",
		"M 0 0 A 1 1 0 0 0 2 2",
		// Odd number of coordinates.
		"M 1",
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

func seg(op SegmentOp, args ...bezier.Point) Segment {
	s := Segment{Op: op}
	copy(s.Args[:], args)
	return s
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
