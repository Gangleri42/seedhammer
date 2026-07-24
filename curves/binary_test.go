package curves

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"seedhammer.com/bezier"
	"seedhammer.com/engrave"
	"seedhammer.com/svgpath"
)

// mkseg builds an absolute payload-unit segment for the encoder input.
func mkseg(op svgpath.SegmentOp, pts ...[2]int) svgpath.Segment {
	s := svgpath.Segment{Op: op}
	for i, p := range pts {
		s.Args[i] = bezier.Pt(p[0], p[1])
	}
	return s
}

// collect runs an engraving to completion and returns its command stream.
func collect(e engrave.Engraving) []engrave.Command {
	var out []engrave.Command
	for c := range e {
		out = append(out, c)
	}
	return out
}

// encodeV1 formats the same absolute payload-unit segments as a v1 ASCII
// path payload: the reference the v2 binary body must decode identically
// at the same units-per-mm.
func encodeV1(unitsPerMM, strokeWidth int, segs []svgpath.Segment) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "%d %s %d %d\n", Version, ModePath, unitsPerMM, strokeWidth)
	for _, s := range segs {
		switch s.Op {
		case svgpath.MoveTo:
			fmt.Fprintf(&b, "M%d %d ", s.Args[0].X, s.Args[0].Y)
		case svgpath.LineTo:
			fmt.Fprintf(&b, "L%d %d ", s.Args[0].X, s.Args[0].Y)
		case svgpath.QuadTo:
			fmt.Fprintf(&b, "Q%d %d %d %d ", s.Args[0].X, s.Args[0].Y, s.Args[1].X, s.Args[1].Y)
		case svgpath.CubeTo:
			fmt.Fprintf(&b, "C%d %d %d %d %d %d ",
				s.Args[0].X, s.Args[0].Y, s.Args[1].X, s.Args[1].Y, s.Args[2].X, s.Args[2].Y)
		}
	}
	return []byte(b.String())
}

// A representative drawing in payload units at 10/mm (1 unit = 0.1mm):
// a closed cubic contour, a travel to a second stroke (the M delta is
// the travel vector), and a dense zigzag run. Exercises M/L/Q/C,
// negative deltas (zigzag) and multi-byte varints.
func sampleSegs() []svgpath.Segment {
	segs := []svgpath.Segment{
		mkseg(svgpath.MoveTo, [2]int{100, 100}),
		mkseg(svgpath.LineTo, [2]int{300, 100}),
		mkseg(svgpath.QuadTo, [2]int{400, 200}, [2]int{300, 300}),
		mkseg(svgpath.CubeTo, [2]int{200, 350}, [2]int{120, 250}, [2]int{100, 100}),
		mkseg(svgpath.MoveTo, [2]int{500, 500}), // travel: +200,+400
	}
	// A run of line segments with 3-digit coordinates and small deltas —
	// the shape the varint+relative encoding compacts best.
	x, y := 500, 500
	for i := 0; i < 24; i++ {
		x += 40 - (i%3)*30 // +40, +10, -20, repeating: forward drift
		y += 25 - (i%2)*45 // +25, -20 zigzag
		segs = append(segs, mkseg(svgpath.LineTo, [2]int{x, y}))
	}
	return segs
}

func TestEncodePathRoundTrip(t *testing.T) {
	sh2 := engrave.SH2Params
	const unitsPerMM, strokeWidth = 10, 3 // 0.1mm units, 0.3mm needle
	segs := sampleSegs()

	v2, err := EncodePath(unitsPerMM, strokeWidth, segs)
	if err != nil {
		t.Fatal(err)
	}
	v1 := encodeV1(unitsPerMM, strokeWidth, segs)

	d1, err := Parse(v1, sh2)
	if err != nil {
		t.Fatalf("v1 parse: %v", err)
	}
	d2, err := Parse(v2, sh2)
	if err != nil {
		t.Fatalf("v2 parse: %v", err)
	}

	// Same points at the same quantization: the binary body must decode
	// to the identical engraving, command for command.
	c1, c2 := collect(d1.Engraving()), collect(d2.Engraving())
	if !reflect.DeepEqual(c1, c2) {
		t.Fatalf("command streams differ: v1 %d cmds, v2 %d cmds", len(c1), len(c2))
	}
	if d1.Bounds != d2.Bounds || d1.Strokes != d2.Strokes || d1.Knots != d2.Knots {
		t.Errorf("summary differs: v1{strokes=%d knots=%d bounds=%v} v2{strokes=%d knots=%d bounds=%v}",
			d1.Strokes, d1.Knots, d1.Bounds, d2.Strokes, d2.Knots, d2.Bounds)
	}

	if len(v2) >= len(v1) {
		t.Errorf("v2 not smaller: v1 %d bytes, v2 %d bytes", len(v1), len(v2))
	}
	t.Logf("size at equal quantization: v1 %d bytes, v2 %d bytes (%.2fx)",
		len(v1), len(v2), float64(len(v1))/float64(len(v2)))
}

// The engraving must be re-iterable: a v2 Drawing retains only the wire
// bytes and re-decodes on each replay, so two runs must be identical.
func TestEncodePathReplay(t *testing.T) {
	v2, err := EncodePath(10, 3, sampleSegs())
	if err != nil {
		t.Fatal(err)
	}
	d, err := Parse(v2, engrave.SH2Params)
	if err != nil {
		t.Fatal(err)
	}
	if a, b := collect(d.Engraving()), collect(d.Engraving()); !reflect.DeepEqual(a, b) {
		t.Errorf("replay differs: %d vs %d commands", len(a), len(b))
	}
}

// A hostile or truncated binary body must return an error, never panic.
func TestParseV2Hostile(t *testing.T) {
	const hdr = "2 path 10 3\n"
	tests := []struct {
		name string
		body []byte
	}{
		{"empty body", nil},
		{"leading line", []byte{'L', 0x02, 0x02}},
		{"bad opcode after move", []byte{'M', 0x02, 0x02, 'X'}},
		{"truncated first varint", []byte{'M', 0x80}},
		{"missing second coord", []byte{'M', 0x02}},
		{"opcode then eof", []byte{'M', 0x02, 0x02, 'L'}},
	}
	for _, tt := range tests {
		data := append([]byte(hdr), tt.body...)
		if _, err := Parse(data, engrave.SH2Params); err == nil {
			t.Errorf("%s: Parse succeeded, want error", tt.name)
		}
	}
}

// TestSizeVsProduction logs the real-world compaction: the same physical
// drawing at v1's production 100/mm ASCII vs v2's 10/mm binary. The
// coarser 10/mm quantization (0.1mm, below the 0.3mm needle) stacks on
// the varint+relative win; the geometry is equivalent, not identical, so
// this is a size measurement only.
func TestSizeVsProduction(t *testing.T) {
	segs10 := sampleSegs() // already payload units at 10/mm
	segs100 := make([]svgpath.Segment, len(segs10))
	for i, s := range segs10 {
		segs100[i] = s
		for j := range segs100[i].Args {
			segs100[i].Args[j] = bezier.Pt(s.Args[j].X*10, s.Args[j].Y*10)
		}
	}
	v1 := encodeV1(100, 30, segs100) // production quantization
	v2, err := EncodePath(10, 3, segs10)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("real-world: v1@100/mm %d bytes vs v2@10/mm %d bytes (%.2fx)",
		len(v1), len(v2), float64(len(v1))/float64(len(v2)))
}

// EncodePath rejects input that cannot form a valid payload.
func TestEncodePathRejects(t *testing.T) {
	if _, err := EncodePath(10, 3, nil); err == nil {
		t.Error("empty segments accepted")
	}
	if _, err := EncodePath(10, 3, []svgpath.Segment{mkseg(svgpath.LineTo, [2]int{1, 1})}); err == nil {
		t.Error("non-move first segment accepted")
	}
}
