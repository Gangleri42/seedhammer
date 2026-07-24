package curves

import (
	"math"
	"testing"

	"seedhammer.com/engrave"
	"seedhammer.com/svgpath"
)

// scatteredStrokes lays 12 open diagonal strokes on a grid but lists them
// in a travel-heavy scrambled order, so a good ordering (and per-stroke
// direction choice, since they are open) has clearly shorter travel.
func scatteredStrokes() []svgpath.Segment {
	grid := [][2]int{
		{0, 0}, {3, 0}, {1, 2}, {2, 1}, {0, 2}, {3, 2},
		{1, 0}, {2, 2}, {0, 1}, {3, 1}, {2, 0}, {1, 1},
	}
	var segs []svgpath.Segment
	for _, g := range grid {
		x, y := 60+g[0]*180, 60+g[1]*180
		segs = append(segs,
			mkseg(svgpath.MoveTo, [2]int{x, y}),
			mkseg(svgpath.LineTo, [2]int{x + 120, y + 60}),
		)
	}
	return segs
}

// travel sums the Euclidean gaps the head jumps between strokes, from the
// origin the planner starts at through each stroke's entry.
func travel(segs []svgpath.Segment) float64 {
	var sum float64
	var curx, cury int
	for _, s := range splitStrokes(segs) {
		dx, dy := float64(s.entry.X-curx), float64(s.entry.Y-cury)
		sum += math.Hypot(dx, dy)
		curx, cury = s.exit.X, s.exit.Y
	}
	return sum
}

func mustEncode(t *testing.T, segs []svgpath.Segment) []byte {
	t.Helper()
	b, err := EncodePath(10, 3, segs)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return b
}

func TestOrderReducesTravel(t *testing.T) {
	segs := scatteredStrokes()
	before, after := travel(segs), travel(Order(segs))
	if after >= before {
		t.Fatalf("travel not reduced: before %.0f after %.0f", before, after)
	}
	t.Logf("travel %.0f -> %.0f (%.0f%% less)", before, after, 100*(before-after)/before)

	// Geometry is preserved: reordering and reversing straight strokes
	// leaves the same strokes and the same hull, only a shorter path and
	// so a shorter planned engraving.
	sh2 := engrave.SH2Params
	d0, err := Parse(mustEncode(t, segs), sh2)
	if err != nil {
		t.Fatal(err)
	}
	d1, err := Parse(mustEncode(t, Order(segs)), sh2)
	if err != nil {
		t.Fatal(err)
	}
	if d0.Strokes != d1.Strokes {
		t.Errorf("stroke count changed: %d -> %d", d0.Strokes, d1.Strokes)
	}
	if d0.Bounds != d1.Bounds {
		t.Errorf("bounds changed: %v -> %v", d0.Bounds, d1.Bounds)
	}
	r0, _ := d0.Validate(sh2)
	r1, _ := d1.Validate(sh2)
	if r1.DurationTicks >= r0.DurationTicks {
		t.Errorf("planned duration not reduced: %d -> %d ticks", r0.DurationTicks, r1.DurationTicks)
	}
}

func TestReverseStroke(t *testing.T) {
	segs := []svgpath.Segment{
		mkseg(svgpath.MoveTo, [2]int{10, 10}),
		mkseg(svgpath.LineTo, [2]int{20, 10}),
		mkseg(svgpath.QuadTo, [2]int{30, 20}, [2]int{20, 30}),
		mkseg(svgpath.CubeTo, [2]int{10, 30}, [2]int{5, 20}, [2]int{10, 10}),
	}
	s := splitStrokes(segs)[0]
	r := reverseStroke(s)

	if r.entry != s.exit || r.exit != s.entry {
		t.Errorf("endpoints not swapped: entry %v exit %v", r.entry, r.exit)
	}
	// The on-curve point sequence is reversed.
	for i := range s.pts {
		if r.pts[i] != s.pts[len(s.pts)-1-i] {
			t.Errorf("pts[%d] = %v, want %v", i, r.pts[i], s.pts[len(s.pts)-1-i])
		}
	}
	// The reversed commands retrace the same geometry: the cubic's two
	// control points swap so the shape is identical.
	want := []svgpath.Segment{
		mkseg(svgpath.MoveTo, [2]int{10, 10}), // was the exit
		mkseg(svgpath.CubeTo, [2]int{5, 20}, [2]int{10, 30}, [2]int{20, 30}),
		mkseg(svgpath.QuadTo, [2]int{30, 20}, [2]int{20, 10}),
		mkseg(svgpath.LineTo, [2]int{10, 10}),
	}
	for i := range want {
		if r.segs[i] != want[i] {
			t.Errorf("segs[%d] = %+v, want %+v", i, r.segs[i], want[i])
		}
	}
}

func TestOrderKeepsSingleStroke(t *testing.T) {
	segs := []svgpath.Segment{
		mkseg(svgpath.MoveTo, [2]int{10, 10}),
		mkseg(svgpath.LineTo, [2]int{20, 20}),
	}
	got := Order(segs)
	if len(got) != len(segs) {
		t.Fatalf("single stroke changed length: %d -> %d", len(segs), len(got))
	}
	for i := range segs {
		if got[i] != segs[i] {
			t.Errorf("segs[%d] changed", i)
		}
	}
}
