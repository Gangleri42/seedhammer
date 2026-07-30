package curves

import (
	"testing"

	"seedhammer.com/bezier"
	"seedhammer.com/engrave"
	"seedhammer.com/svgpath"
)

// A stroke whose points all quantize onto its start must survive
// encoding with real geometry: dot glyphs sit below the payload grid
// at small text sizes, and a dropped stroke never reaches the needle.
func TestEncodePreservesPointStrokes(t *testing.T) {
	pt := bezier.Pt
	move := func(p bezier.Point) svgpath.Segment {
		s := svgpath.Segment{Op: svgpath.MoveTo}
		s.Args[0] = p
		return s
	}
	line := func(p bezier.Point) svgpath.Segment {
		s := svgpath.Segment{Op: svgpath.LineTo}
		s.Args[0] = p
		return s
	}
	cube := func(a, b, c bezier.Point) svgpath.Segment {
		s := svgpath.Segment{Op: svgpath.CubeTo}
		s.Args[0], s.Args[1], s.Args[2] = a, b, c
		return s
	}
	dot := pt(100, 200)
	segs := []svgpath.Segment{
		move(dot), line(dot), cube(dot, dot, dot), // the collapsed dot glyph
		move(pt(300, 300)), line(pt(400, 300)), // a normal stroke
		move(pt(500, 500)), // bare positioning move, not a dot
	}
	for _, enc := range []struct {
		name string
		data func() ([]byte, error)
	}{
		{"path", func() ([]byte, error) { return EncodePath(10, 3, segs) }},
		{"groups", func() ([]byte, error) {
			return EncodeGroups(10, 3, []Group{{At: pt(10, 10), Segs: segs}})
		}},
	} {
		data, err := enc.data()
		if err != nil {
			t.Fatalf("%s: %v", enc.name, err)
		}
		d, err := Parse(data, engrave.SH2Params)
		if err != nil {
			t.Fatalf("%s: parse: %v", enc.name, err)
		}
		if d.Strokes != 2 {
			t.Errorf("%s: strokes = %d, want 2 (dot preserved, bare move dropped)", enc.name, d.Strokes)
		}
	}
	// The rewrite must not disturb strokes that carry geometry.
	fixed := preservePointStrokes(segs[3:5])
	if len(fixed) != 2 || fixed[1].Args[0] != pt(400, 300) {
		t.Errorf("normal stroke rewritten: %v", fixed)
	}
	// The decoder contract stays pinned independently of the encoder
	// rewrite: raw wire whose only stroke is genuinely zero-length
	// must still refuse to parse.
	raw := []byte("2 path 10 3\n")
	var cur bezier.Point
	raw = appendSegs(raw, []svgpath.Segment{move(dot), line(dot)}, bezier.Point{}, &cur)
	if _, err := Parse(raw, engrave.SH2Params); err == nil {
		t.Error("raw zero-length wire parsed, want error")
	}
}
