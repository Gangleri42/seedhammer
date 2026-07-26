package curves

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"seedhammer.com/bezier"
	"seedhammer.com/engrave"
	"seedhammer.com/svgpath"
)

// glyphShape is a small closed two-contour shape in local payload
// units, standing in for a glyph: an outer cubic loop and an inner
// line loop, entry at the local origin.
func glyphShape() []svgpath.Segment {
	return []svgpath.Segment{
		mkseg(svgpath.MoveTo, [2]int{0, 0}),
		mkseg(svgpath.CubeTo, [2]int{40, -20}, [2]int{80, 20}, [2]int{40, 40}),
		mkseg(svgpath.CubeTo, [2]int{20, 50}, [2]int{-10, 20}, [2]int{0, 0}),
		mkseg(svgpath.MoveTo, [2]int{20, 10}),
		mkseg(svgpath.LineTo, [2]int{30, 10}),
		mkseg(svgpath.LineTo, [2]int{25, 20}),
		mkseg(svgpath.LineTo, [2]int{20, 10}),
	}
}

// sampleGroups is a dictionary-friendly drawing: one shape placed
// four times along a row plus a unique zigzag, the mix a text plate
// produces.
func sampleGroups() []Group {
	var groups []Group
	for i := 0; i < 4; i++ {
		groups = append(groups, Group{At: bezier.Pt(100+90*i, 100), Segs: glyphShape()})
	}
	zig := []svgpath.Segment{mkseg(svgpath.MoveTo, [2]int{0, 0})}
	x, y := 0, 0
	for i := 0; i < 8; i++ {
		x += 30
		y += 25 - (i%2)*50
		zig = append(zig, mkseg(svgpath.LineTo, [2]int{x, y}))
	}
	groups = append(groups, Group{At: bezier.Pt(100, 300), Segs: zig})
	return groups
}

// flatten translates each group's local segments to absolute payload
// units: the drawing EncodePath would carry without the dictionary.
func flatten(groups []Group) []svgpath.Segment {
	var out []svgpath.Segment
	for _, g := range groups {
		for _, s := range g.Segs {
			f := svgpath.Segment{Op: s.Op}
			_, n := opByte(s.Op)
			for i := 0; i < n; i++ {
				f.Args[i] = s.Args[i].Add(g.At)
			}
			out = append(out, f)
		}
	}
	return out
}

// The dictionary payload must decode to the identical engraving the
// flattened drawing produces — dedup changes the wire, never the plate.
func TestEncodeGroupsRoundTrip(t *testing.T) {
	sh2 := engrave.SH2Params
	groups := sampleGroups()

	dict, err := EncodeGroups(10, 3, groups)
	if err != nil {
		t.Fatal(err)
	}
	flat, err := EncodePath(10, 3, flatten(groups))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(dict[:20], []byte("\nD")) {
		t.Fatalf("payload has no dictionary section: %q...", dict[:20])
	}
	dd, err := Parse(dict, sh2)
	if err != nil {
		t.Fatalf("dict parse: %v", err)
	}
	fd, err := Parse(flat, sh2)
	if err != nil {
		t.Fatalf("flat parse: %v", err)
	}
	cd, cf := collect(dd.Engraving()), collect(fd.Engraving())
	if !reflect.DeepEqual(cd, cf) {
		t.Fatalf("command streams differ: dict %d cmds, flat %d cmds", len(cd), len(cf))
	}
	if dd.Bounds != fd.Bounds || dd.Strokes != fd.Strokes || dd.Knots != fd.Knots {
		t.Errorf("summary differs: dict{strokes=%d knots=%d bounds=%v} flat{strokes=%d knots=%d bounds=%v}",
			dd.Strokes, dd.Knots, dd.Bounds, fd.Strokes, fd.Knots, fd.Bounds)
	}
	if len(dict) >= len(flat) {
		t.Errorf("dictionary not smaller: dict %d bytes, flat %d bytes", len(dict), len(flat))
	}
	t.Logf("size: flat %d bytes, dict %d bytes (%.2fx)",
		len(flat), len(dict), float64(len(flat))/float64(len(dict)))
}

// Without a paying repeat the dictionary must stay off the wire, and
// the payload must be the byte-exact EncodePath encoding.
func TestEncodeGroupsNoRepeats(t *testing.T) {
	groups := sampleGroups()[3:] // one shape instance + the zigzag
	dict, err := EncodeGroups(10, 3, groups)
	if err != nil {
		t.Fatal(err)
	}
	flat, err := EncodePath(10, 3, flatten(groups))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(dict, flat) {
		t.Errorf("repeat-free groups should encode byte-identically to the flat path: %d vs %d bytes", len(dict), len(flat))
	}
}

// A tiny shape must not be dictionaried: the placement overhead would
// exceed re-shipping it.
func TestEncodeGroupsTinyShapeInlined(t *testing.T) {
	dot := []svgpath.Segment{
		mkseg(svgpath.MoveTo, [2]int{0, 0}),
		mkseg(svgpath.LineTo, [2]int{2, 0}),
	}
	var groups []Group
	for i := 0; i < 3; i++ {
		groups = append(groups, Group{At: bezier.Pt(100+20*i, 100), Segs: dot})
	}
	got, err := EncodeGroups(10, 3, groups)
	if err != nil {
		t.Fatal(err)
	}
	flat, err := EncodePath(10, 3, flatten(groups))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, flat) {
		t.Errorf("tiny repeated shape should inline: got %d bytes, flat %d", len(got), len(flat))
	}
}

// A dictionary drawing must replay identically: the Drawing retains
// only the wire bytes and the shape index, and re-decodes each run.
func TestEncodeGroupsReplay(t *testing.T) {
	dict, err := EncodeGroups(10, 3, sampleGroups())
	if err != nil {
		t.Fatal(err)
	}
	d, err := Parse(dict, engrave.SH2Params)
	if err != nil {
		t.Fatal(err)
	}
	if a, b := collect(d.Engraving()), collect(d.Engraving()); !reflect.DeepEqual(a, b) {
		t.Errorf("replay differs: %d vs %d commands", len(a), len(b))
	}
}

func TestEncodeGroupsRejects(t *testing.T) {
	if _, err := EncodeGroups(10, 3, nil); err == nil {
		t.Error("empty group list accepted")
	}
	if _, err := EncodeGroups(10, 3, []Group{{}}); err == nil {
		t.Error("empty group accepted")
	}
	bad := []Group{{Segs: []svgpath.Segment{mkseg(svgpath.LineTo, [2]int{1, 1})}}}
	if _, err := EncodeGroups(10, 3, bad); err == nil {
		t.Error("non-move leading segment accepted")
	}
}

// OrderGroups must keep every group intact — same instances, same
// geometry — and not lengthen the head travel of an adversarial
// arrangement.
func TestOrderGroups(t *testing.T) {
	groups := sampleGroups()
	// Scatter: interleave far and near placements.
	scattered := []Group{groups[3], groups[0], groups[4], groups[2], groups[1]}
	ordered := OrderGroups(scattered)
	if len(ordered) != len(scattered) {
		t.Fatalf("group count changed: %d vs %d", len(ordered), len(scattered))
	}
	key := func(g Group) string {
		var cur bezier.Point
		return fmt.Sprintf("%v %s", g.At, appendSegs(nil, g.Segs, bezier.Point{}, &cur))
	}
	want := map[string]int{}
	for _, g := range scattered {
		want[key(g)]++
	}
	for _, g := range ordered {
		want[key(g)]--
	}
	for k, n := range want {
		if n != 0 {
			t.Errorf("group multiset changed: %q off by %d", k, n)
		}
	}
	if a, b := groupsTravel(ordered), groupsTravel(scattered); a > b {
		t.Errorf("ordering lengthened travel: %d > %d", a, b)
	}
}

// groupsTravel sums the squared travel hops between consecutive
// groups from the origin — a coarse but monotone travel figure.
func groupsTravel(groups []Group) int64 {
	var cur bezier.Point
	var sum int64
	for _, g := range groups {
		entry := g.At.Add(g.Segs[0].Args[0])
		sum += dist2(cur, entry)
		cur = g.At.Add(groupExit(g.Segs))
	}
	return sum
}

// Hand-built hostile dictionary bodies must error, never panic or hang.
func TestParseDictHostile(t *testing.T) {
	const hdr = "2 path 10 3\n"
	// A minimal valid shape: M 0,0 then L +10,+0.
	shape := []byte{'M', 0x00, 0x00, 'L', 0x14, 0x00}
	dict := func(shapes ...[]byte) []byte {
		b := []byte{'D'}
		b = binary.AppendUvarint(b, uint64(len(shapes)))
		for _, s := range shapes {
			b = binary.AppendUvarint(b, uint64(len(s)))
			b = append(b, s...)
		}
		return b
	}
	place := func(id uint64, dx, dy int64) []byte {
		b := []byte{'G'}
		b = binary.AppendUvarint(b, id)
		b = binary.AppendVarint(b, dx)
		b = binary.AppendVarint(b, dy)
		return b
	}
	cat := func(parts ...[]byte) []byte {
		var b []byte
		for _, p := range parts {
			b = append(b, p...)
		}
		return b
	}
	tests := []struct {
		name string
		body []byte
	}{
		{"bare D", []byte{'D'}},
		{"zero shapes", cat([]byte{'D', 0x00}, []byte{'M', 0x02, 0x02})},
		{"count over cap", []byte{'D', 0xff, 0xff, 0x7f}},
		{"truncated length", []byte{'D', 0x01}},
		{"length past payload", cat([]byte{'D', 0x01, 0x7f}, shape)},
		{"zero-length shape", cat([]byte{'D', 0x01, 0x00}, []byte{'M', 0x02, 0x02})},
		{"shape without move", cat(dict([]byte{'L', 0x02, 0x02}), place(0, 100, 100))},
		{"dictionary only", dict(shape)},
		{"placement without dictionary", place(0, 100, 100)},
		{"placement out of range", cat(dict(shape), place(1, 100, 100))},
		{"truncated placement id", cat(dict(shape), []byte{'G'})},
		{"truncated placement delta", cat(dict(shape), []byte{'G', 0x00, 0x14})},
		{"nested placement", cat(dict(cat(shape, place(0, 10, 10))), place(0, 100, 100))},
		{"D in main stream", cat(dict(shape), place(0, 100, 100), []byte{'D'})},
		// A shape length cutting a command short must not decode the
		// following bytes — the next shape, or the main stream — as
		// coordinates.
		{"shape cut before deltas", cat(dict([]byte{'M', 0x00, 0x00, 'L'}), place(0, 1000, 1000))},
		{"shape cut mid-varint", cat(dict([]byte{'M', 0x02}, shape), place(0, 1000, 1000))},
	}
	for _, tt := range tests {
		data := append([]byte(hdr), tt.body...)
		if _, err := Parse(data, engrave.SH2Params); err == nil {
			t.Errorf("%s: Parse succeeded, want error", tt.name)
		}
	}
}

// A dictionary expansion bomb — a shape placed thousands of times in a
// few KB — must be rejected by the parse bounds, not walked in full.
// The placements stamp in place (travel back after each stroke), the
// worst case: marching off the plate would defuse itself against the
// coordinate clamp instead.
func TestParseExpansionBomb(t *testing.T) {
	b := []byte("2 path 10 3\n")
	shape := []byte{'M', 0x00, 0x00, 'L', 0x14, 0x00} // M 0,0 L +10,0
	b = append(b, 'D', 0x01)
	b = binary.AppendUvarint(b, uint64(len(shape)))
	b = append(b, shape...)
	for i := 0; i < 3*parseMaxStrokes; i++ {
		dx := int64(-10) // back to the same base, stroke after stroke
		if i == 0 {
			dx = 100
		}
		b = append(b, 'G', 0x00)
		b = binary.AppendVarint(b, dx)
		b = binary.AppendVarint(b, 0)
	}
	_, err := Parse(b, engrave.SH2Params)
	if err == nil || !strings.Contains(err.Error(), "expands past") {
		t.Fatalf("want parse-bound rejection, got %v", err)
	}
}

// A knot-free bomb — a shape padded with degenerate zero-length lines,
// placed over and over — yields no knots for the knot bound to count,
// so the segment bound must stop the walk instead.
func TestParseKnotFreeBomb(t *testing.T) {
	b := []byte("2 path 10 3\n")
	shape := []byte{'M', 0x02, 0x02}
	for i := 0; i < 1000; i++ {
		shape = append(shape, 'L', 0x00, 0x00)
	}
	b = append(b, 'D', 0x01)
	b = binary.AppendUvarint(b, uint64(len(shape)))
	b = append(b, shape...)
	for i := 0; i < 100; i++ {
		b = append(b, 'G', 0x00, 0x02, 0x02)
	}
	_, err := Parse(b, engrave.SH2Params)
	if err == nil || !strings.Contains(err.Error(), "segments") {
		t.Fatalf("want segment-bound rejection, got %v", err)
	}
}

// A drawing honestly over an engraving cap but inside the parse
// headroom must still Parse — Validate owns the rejection, with the
// full gauge report intact.
func TestParseHeadroomKeepsGauges(t *testing.T) {
	var groups []Group
	for i := 0; i < 600; i++ { // over MaxStrokes 512, under the 4x parse bound
		groups = append(groups, Group{At: bezier.Pt(100+(i%40)*15, 100+(i/40)*15), Segs: []svgpath.Segment{
			mkseg(svgpath.MoveTo, [2]int{0, 0}),
			mkseg(svgpath.LineTo, [2]int{8, 0}),
		}})
	}
	payload, err := EncodeGroups(10, 3, groups)
	if err != nil {
		t.Fatal(err)
	}
	d, err := Parse(payload, engrave.SH2Params)
	if err != nil {
		t.Fatalf("Parse rejected an in-headroom drawing: %v", err)
	}
	if d.Strokes != 600 {
		t.Errorf("strokes = %d, want 600", d.Strokes)
	}
	r, err := d.Validate(engrave.SH2Params)
	if err == nil || !strings.Contains(err.Error(), "strokes") {
		t.Fatalf("want stroke-cap rejection from Validate, got %v", err)
	}
	if r.Strokes != 600 {
		t.Errorf("report strokes = %d, want 600", r.Strokes)
	}
}

// A handful of identical table rules must already pay for the
// dictionary: the payoff rule prices travel on both sides, so it
// cancels instead of penalizing the placement.
func TestRulesDedupEarly(t *testing.T) {
	rule := []svgpath.Segment{
		mkseg(svgpath.MoveTo, [2]int{0, 0}),
		mkseg(svgpath.LineTo, [2]int{300, 0}),
	}
	var groups []Group
	for i := 0; i < 4; i++ {
		groups = append(groups, Group{At: bezier.Pt(100, 100+40*i), Segs: rule})
	}
	dict, err := EncodeGroups(10, 3, groups)
	if err != nil {
		t.Fatal(err)
	}
	flat, err := EncodePath(10, 3, flatten(groups))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(dict[:16], []byte("\nD")) {
		t.Fatalf("four identical rules should dictionary: %q", dict)
	}
	if len(dict) > len(flat) {
		t.Errorf("rule dictionary larger than flat: %d vs %d bytes", len(dict), len(flat))
	}
}
