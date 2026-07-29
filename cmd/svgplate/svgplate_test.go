package main

import (
	"encoding/xml"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"seedhammer.com/bezier"
	"seedhammer.com/curves"
	"seedhammer.com/richtext"
	"seedhammer.com/svgpath"
)

// endpoints returns the on-curve points of a segment list: the start
// of the first and the last point of each segment.
func endpoints(segs []fseg) []fpt {
	var pts []fpt
	for _, s := range segs {
		pts = append(pts, s.end())
	}
	return pts
}

func approx(a, b, eps float64) bool { return math.Abs(a-b) <= eps }

// flatEncode is the baseline the shape dictionary has to beat: the
// same quantization with every instance inlined and no dictionary.
// Tests keep their own copy so the comparison stays independent of
// whatever the emitter does.
func flatEncode(segs []fseg, order bool) ([]byte, error) {
	q := func(v float64) int { return int(math.Round(v * payloadUnitsPerMM)) }
	out := make([]svgpath.Segment, len(segs))
	for i, s := range segs {
		out[i].Op = s.op
		for j := 0; j < s.npts(); j++ {
			out[i].Args[j] = bezier.Pt(q(s.p[j].X), q(s.p[j].Y))
		}
	}
	if order {
		out = curves.Order(out)
	}
	return curves.EncodePath(payloadUnitsPerMM, payloadStroke, out)
}

// flatDrawing wraps raw segments as a single unrepeated placement, for
// tests that build geometry directly rather than from a document.
func flatDrawing(segs []fseg) *drawing {
	d := &drawing{}
	d.place(d.addShape(segs), fpt{})
	return d
}

// startEl builds an element from name and alternating attribute
// name/value pairs, for the shape helpers that take one directly.
func startEl(name string, kv ...string) xml.StartElement {
	e := xml.StartElement{Name: xml.Name{Local: name}}
	for i := 0; i+1 < len(kv); i += 2 {
		e.Attr = append(e.Attr, xml.Attr{Name: xml.Name{Local: kv[i]}, Value: kv[i+1]})
	}
	return e
}

func TestParsePathRelativeMatchesAbsolute(t *testing.T) {
	abs, err := parsePath("M10 10 L30 10 C30 30 50 30 50 50")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := parsePath("m10 10 l20 0 c0 20 20 20 20 40")
	if err != nil {
		t.Fatal(err)
	}
	ap, rp := endpoints(abs), endpoints(rel)
	if len(ap) != len(rp) {
		t.Fatalf("segment count %d vs %d", len(ap), len(rp))
	}
	for i := range ap {
		if !approx(ap[i].X, rp[i].X, 1e-9) || !approx(ap[i].Y, rp[i].Y, 1e-9) {
			t.Errorf("point %d: abs %v rel %v", i, ap[i], rp[i])
		}
	}
}

func TestParsePathSmoothReflection(t *testing.T) {
	// S reflects the previous cubic's second control across the pen.
	segs, err := parsePath("M0 0 C0 10 10 10 10 0 S20 -10 20 0")
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 3 {
		t.Fatalf("want 3 segments, got %d", len(segs))
	}
	// After the first cubic ends at (10,0) with ctrl2 (10,10), the
	// smooth cubic's first control is the reflection: (10, -10).
	if got := segs[2].p[0]; !approx(got.X, 10, 1e-9) || !approx(got.Y, -10, 1e-9) {
		t.Errorf("reflected control = %v, want (10,-10)", got)
	}
}

func TestParsePathClose(t *testing.T) {
	segs, err := parsePath("M0 0 L10 0 L10 10 Z")
	if err != nil {
		t.Fatal(err)
	}
	last := segs[len(segs)-1]
	if last.op != svgpath.LineTo || last.p[0] != (fpt{0, 0}) {
		t.Errorf("Z should close to start, got %v", last)
	}
}

func TestArcSemicircleOnCircle(t *testing.T) {
	// A 180-degree arc from (0,0) to (20,0), radius 10, centers at
	// (10,0). Every on-curve point must lie on that circle.
	segs := arcToCubics(fpt{0, 0}, 10, 10, 0, false, true, fpt{20, 0})
	if len(segs) < 2 {
		t.Fatalf("want at least 2 cubics, got %d", len(segs))
	}
	center := fpt{10, 0}
	for i, p := range endpoints(segs) {
		r := math.Hypot(p.X-center.X, p.Y-center.Y)
		if !approx(r, 10, 0.05) {
			t.Errorf("on-curve point %d at radius %.4f, want 10 (%v)", i, r, p)
		}
	}
}

func TestScannerPackedNumbers(t *testing.T) {
	cases := map[string][]float64{
		"1.5.5":     {1.5, 0.5},
		"1-2":       {1, -2},
		"1e3 -2E-1": {1000, -0.2},
		".5.5":      {0.5, 0.5},
	}
	for in, want := range cases {
		got := floats(in)
		if len(got) != len(want) {
			t.Errorf("%q: got %v, want %v", in, got, want)
			continue
		}
		for i := range want {
			if !approx(got[i], want[i], 1e-9) {
				t.Errorf("%q: got %v, want %v", in, got, want)
				break
			}
		}
	}
}

func TestTransformNesting(t *testing.T) {
	// translate(10,10) then rotate(90): (1,0) -> rotate -> (0,1) ->
	// translate -> (10,11).
	m, err := parseTransform("translate(10,10) rotate(90)")
	if err != nil {
		t.Fatal(err)
	}
	p := m.apply(fpt{1, 0})
	if !approx(p.X, 10, 1e-9) || !approx(p.Y, 11, 1e-9) {
		t.Errorf("got %v, want (10,11)", p)
	}
}

func TestSVGRoundTrip(t *testing.T) {
	const doc = `<svg viewBox="0 0 100 100">
	  <g transform="translate(5,5)">
	    <circle cx="20" cy="20" r="15"/>
	    <rect x="40" y="40" width="30" height="20"/>
	  </g>
	  <path d="M0 90 L90 90" display="none"/>
	</svg>`
	raw, err := extractSVG([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	d := layoutOnPlate(raw, placement{posX: math.NaN(), posY: math.NaN()})
	_, _, r, err := finishDrawing(d, true)
	if err != nil {
		t.Fatalf("finishDrawing: %v", err)
	}
	if r.Strokes != 2 {
		t.Errorf("want 2 strokes (circle + rect; hidden path skipped), got %d", r.Strokes)
	}
}

func TestRichTextValid(t *testing.T) {
	const md = "# Title\n\nKeep *safe*.\n"
	groups, err := richtext.Render(md, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := finishGroups(groups, true); err != nil {
		t.Fatalf("finishGroups: %v", err)
	}
}

// TestDictionaryCompaction logs the dictionary's win on a realistic
// text plate: the same rendered groups encoded flat (every glyph
// instance shipped) versus through the shape dictionary. Repeated
// glyphs are the entire size problem for text-as-curves, so the dict
// payload must come in well under the flat one and under the NDEF cap.
func TestDictionaryCompaction(t *testing.T) {
	const md = `# RECOVERY

Wallet: *family vault* 2-of-3
Verify addresses on two devices.

| Slot | Device |
| --- | --- |
| 1 | SeedHammer |
| 2 | Coldcard |
| 3 | Passport |

Check the plates yearly.
Keep away from the seed plates.
`
	groups, err := richtext.Render(md, 4)
	if err != nil {
		t.Fatal(err)
	}
	dict, _, r, err := finishGroups(groups, true)
	if err != nil {
		t.Fatalf("finishGroups: %v", err)
	}
	// The flat baseline: identical quantized geometry, no dictionary.
	var flatSegs []svgpath.Segment
	for _, g := range richtext.Groups(groups, payloadUnitsPerMM) {
		for _, s := range g.Segs {
			out := svgpath.Segment{Op: s.Op}
			for i := range s.Args {
				out.Args[i] = s.Args[i].Add(g.At)
			}
			flatSegs = append(flatSegs, out)
		}
	}
	flat, err := curves.EncodePath(payloadUnitsPerMM, payloadStroke, flatSegs)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("text plate: flat v2 %d bytes, dict v2 %d bytes (%.2fx), strokes=%d knots=%d",
		len(flat), len(dict), float64(len(flat))/float64(len(dict)), r.Strokes, r.Knots)
	if len(dict) >= len(flat) {
		t.Errorf("dictionary payload (%d) not smaller than flat (%d)", len(dict), len(flat))
	}
}

func TestByteCapRejected(t *testing.T) {
	// v2's binary encoding makes bytes cheap, so overrunning the NDEF byte
	// cap takes a lot of geometry. Many short zigzag strokes push the
	// payload over the cap while staying under the stroke, knot and time
	// caps, so the byte check is the one that fires.
	var segs []fseg
	const strokes, cubics, step = 80, 80, 0.5
	for s := 0; s < strokes; s++ {
		y := 6.0 + float64(s%60)*1.2 // rows within the plate margin
		x := 6.0
		segs = append(segs, fseg{op: svgpath.MoveTo, p: [3]fpt{{x, y}}})
		for i := 0; i < cubics; i++ {
			// A straight run of short collinear cubics: knots track path
			// length (the fitter samples by arc length), so short cubics
			// keep knots low while every cubic is a wire record, pushing
			// bytes over the cap first.
			segs = append(segs, fseg{op: svgpath.CubeTo, p: [3]fpt{
				{x + step/3, y}, {x + 2*step/3, y}, {x + step, y},
			}})
			x += step
		}
	}
	_, _, r, err := finishDrawing(flatDrawing(segs), true)
	t.Logf("gauges: bytes=%d strokes=%d knots=%d knots/stroke=%d secs=%d",
		r.Bytes, r.Strokes, r.Knots, r.MaxStrokeKnots, r.Seconds)
	if err == nil || !strings.Contains(err.Error(), "NDEF cap") {
		t.Fatalf("want NDEF cap rejection, got %v", err)
	}
}

func TestParsePathCoordAfterZErrors(t *testing.T) {
	// A number after Z has no command; it must error, not loop forever.
	done := make(chan error, 1)
	go func() {
		_, err := parsePath("M0 0 L10 0 Z 5 5")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("want an error for a coordinate after Z, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("parsePath hung on a coordinate after Z")
	}
}

func TestParseTransformMalformed(t *testing.T) {
	// A ')' before '(' must not panic on a bad slice.
	if _, err := parseTransform(")x("); err == nil {
		t.Error("want an error for malformed transform, got nil")
	}
}

func TestNonFiniteRejected(t *testing.T) {
	// A NaN attribute is sanitized to 0 by num, so it never reaches
	// the payload; and finish's guard catches any non-finite that
	// slips through by another path.
	const doc = `<svg viewBox="0 0 100 100"><rect x="NaN" y="0" width="50" height="50"/></svg>`
	raw, err := extractSVG([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range raw.flatten() {
		for i := 0; i < s.npts(); i++ {
			if math.IsNaN(s.p[i].X) || math.IsNaN(s.p[i].Y) {
				t.Fatalf("NaN reached geometry: %v", s.p[i])
			}
		}
	}
	// The guard itself rejects a hand-built non-finite segment.
	bad := []fseg{{op: svgpath.MoveTo, p: [3]fpt{{math.Inf(1), 0}}}}
	if _, _, _, err := finishDrawing(flatDrawing(bad), true); err == nil {
		t.Error("finishDrawing accepted a non-finite coordinate")
	}
}

// moves returns the start point of every stroke in a segment list.
func moves(segs []fseg) []fpt {
	var pts []fpt
	for _, s := range segs {
		if s.op == svgpath.MoveTo {
			pts = append(pts, s.p[0])
		}
	}
	return pts
}

func TestUseStampsTheReferencedShape(t *testing.T) {
	// One definition, two stamps, and the target is declared after both
	// references: a document is free to define an id below its use.
	const doc = `<svg viewBox="0 0 100 100">
	  <use href="#pill" x="10" y="10"/>
	  <use href="#pill" x="60" y="20"/>
	  <defs><rect id="pill" width="20" height="30"/></defs>
	</svg>`
	segs, err := extractSVG([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	got := moves(segs.flatten())
	want := []fpt{{10, 10}, {60, 20}}
	if len(got) != len(want) {
		t.Fatalf("want %d stamps, got %d", len(want), len(got))
	}
	for i := range want {
		if !approx(got[i].X, want[i].X, 1e-9) || !approx(got[i].Y, want[i].Y, 1e-9) {
			t.Errorf("stamp %d at %v, want %v", i, got[i], want[i])
		}
	}
}

func TestUseComposesTransformWithOffset(t *testing.T) {
	// The use's own transform applies, then its x/y, then the target's:
	// scale(2) doubles the offset, and the target's translate rides
	// along scaled.
	const doc = `<svg viewBox="0 0 100 100">
	  <use href="#dot" transform="scale(2)" x="5" y="5"/>
	  <defs><rect id="dot" transform="translate(1,1)" width="4" height="4"/></defs>
	</svg>`
	segs, err := extractSVG([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	got := moves(segs.flatten())
	if len(got) != 1 {
		t.Fatalf("want 1 stamp, got %d", len(got))
	}
	if !approx(got[0].X, 12, 1e-9) || !approx(got[0].Y, 12, 1e-9) {
		t.Errorf("stamp at %v, want (12,12)", got[0])
	}
}

func TestUseThroughLegacyXlinkHref(t *testing.T) {
	// Inkscape and Illustrator still write xlink:href.
	const doc = `<svg viewBox="0 0 100 100" xmlns:xlink="http://www.w3.org/1999/xlink">
	  <defs><rect id="box" width="10" height="10"/></defs>
	  <use xlink:href="#box" x="20" y="30"/>
	</svg>`
	segs, err := extractSVG([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if got := moves(segs.flatten()); len(got) != 1 || !approx(got[0].X, 20, 1e-9) || !approx(got[0].Y, 30, 1e-9) {
		t.Errorf("got %v, want one stamp at (20,30)", got)
	}
}

func TestUseOfSymbolRenders(t *testing.T) {
	// A <symbol> draws nothing on its own but stamps through a <use>,
	// so the document below engraves exactly one box.
	const doc = `<svg viewBox="0 0 100 100">
	  <symbol id="s"><rect width="10" height="10"/></symbol>
	  <use href="#s" x="40" y="40"/>
	</svg>`
	segs, err := extractSVG([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if got := moves(segs.flatten()); len(got) != 1 || !approx(got[0].X, 40, 1e-9) {
		t.Errorf("got %v, want one stamp at (40,40)", got)
	}
}

func TestUseUnresolvedIsAnError(t *testing.T) {
	// Silently dropping the reference would engrave a drawing missing
	// whatever it stamped, and still report every cap as clear.
	for _, doc := range []string{
		`<svg><use href="#missing" x="1" y="1"/><rect width="5" height="5"/></svg>`,
		`<svg><use href="other.svg#box"/><rect width="5" height="5"/></svg>`,
	} {
		if _, err := extractSVG([]byte(doc)); err == nil {
			t.Errorf("%s: want an error, got none", doc)
		}
	}
}

func TestUseCycleTerminates(t *testing.T) {
	const doc = `<svg><g id="loop"><use href="#loop"/></g></svg>`
	done := make(chan error, 1)
	go func() {
		_, err := extractSVG([]byte(doc))
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("want an error on a self-referencing use, got none")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("extractSVG did not terminate on a use cycle")
	}
}

func TestRectRadii(t *testing.T) {
	for _, tc := range []struct {
		rx, ry       string
		w, h         float64
		wantX, wantY float64
		name         string
	}{
		{"", "", 20, 10, 0, 0, "no radii"},
		{"4", "", 20, 10, 4, 4, "rx alone sets both"},
		{"", "3", 20, 10, 3, 3, "ry alone sets both"},
		{"4", "2", 20, 10, 4, 2, "both given"},
		{"100", "100", 20, 10, 10, 5, "clamped to half the side"},
		{"auto", "auto", 20, 10, 0, 0, "auto is no radius"},
		{"-5", "", 20, 10, 0, 0, "negative is no radius"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x, y := rectRadii(tc.rx, tc.ry, tc.w, tc.h)
			if !approx(x, tc.wantX, 1e-9) || !approx(y, tc.wantY, 1e-9) {
				t.Errorf("got (%g,%g), want (%g,%g)", x, y, tc.wantX, tc.wantY)
			}
		})
	}
}

func TestRoundedRectCorners(t *testing.T) {
	// A rounded rect is four sides and four cubic quarter-arcs, opening
	// one radius along the top edge.
	segs, err := shapeSegments(startEl("rect", "x", "10", "y", "20", "width", "40", "height", "30", "rx", "5"))
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 9 {
		t.Fatalf("want 9 segments, got %d", len(segs))
	}
	if got := segs[0].p[0]; !approx(got.X, 15, 1e-9) || !approx(got.Y, 20, 1e-9) {
		t.Errorf("start at %v, want (15,20)", got)
	}
	cubics := 0
	for _, s := range segs {
		if s.op == svgpath.CubeTo {
			cubics++
		}
	}
	if cubics != 4 {
		t.Errorf("want 4 corner cubics, got %d", cubics)
	}
	// The outline closes back on its start.
	if got := segs[len(segs)-1].end(); !approx(got.X, 15, 1e-9) || !approx(got.Y, 20, 1e-9) {
		t.Errorf("end at %v, want (15,20)", got)
	}
	// A radius-free rect stays a four-line polygon.
	sharp, err := shapeSegments(startEl("rect", "width", "40", "height", "30"))
	if err != nil {
		t.Fatal(err)
	}
	if len(sharp) != 5 {
		t.Errorf("want 5 segments for a sharp rect, got %d", len(sharp))
	}
}

// stampedDoc builds a document that defines one rounded pill and
// stamps it n times on a grid, the shape a logo built from repeated
// glyphs has.
func stampedDoc(n int) string {
	var b strings.Builder
	b.WriteString(`<svg viewBox="0 0 400 400">` +
		`<defs><rect id="p" x="-10" y="-15" width="20" height="30" rx="8"/></defs>`)
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, `<use href="#p" x="%d" y="%d"/>`, 40+(i%6)*60, 40+(i/6)*90)
	}
	b.WriteString(`</svg>`)
	return b.String()
}

func TestUseKeepsOneShapePerDefinition(t *testing.T) {
	d, err := extractSVG([]byte(stampedDoc(24)))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.shapes) != 1 {
		t.Errorf("want 1 shape for 24 stamps of one definition, got %d", len(d.shapes))
	}
	if len(d.groups) != 24 {
		t.Errorf("want 24 placements, got %d", len(d.groups))
	}
}

func TestUseUnderDifferentScalesAreDifferentShapes(t *testing.T) {
	// Only translation is a placement. A stamp under its own scale is
	// a different outline and must not share a dictionary entry.
	const doc = `<svg viewBox="0 0 100 100">
	  <defs><rect id="p" width="10" height="10"/></defs>
	  <use href="#p" x="10" y="10"/>
	  <use href="#p" x="40" y="10"/>
	  <use href="#p" transform="scale(2)" x="10" y="30"/>
	</svg>`
	d, err := extractSVG([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.shapes) != 2 {
		t.Errorf("want 2 shapes (one per scale), got %d", len(d.shapes))
	}
	if len(d.groups) != 3 {
		t.Errorf("want 3 placements, got %d", len(d.groups))
	}
}

func TestCopiedShapesShareADictionaryEntry(t *testing.T) {
	// Most drawings repeat geometry by copying it, not with <use>: the
	// copies carry their position inside their own coordinates. They
	// still describe one outline, so the payload must ship one.
	var b strings.Builder
	b.WriteString(`<svg viewBox="0 0 400 400">`)
	for i := 0; i < 16; i++ {
		fmt.Fprintf(&b, `<rect x="%d" y="%d" width="20" height="30" rx="8"/>`,
			30+(i%4)*80, 30+(i/4)*80)
	}
	b.WriteString(`</svg>`)
	raw, err := extractSVG([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	d := layoutOnPlate(raw, placement{posX: math.NaN(), posY: math.NaN()})
	dict, _, r, err := finishDrawing(d, true)
	if err != nil {
		t.Fatal(err)
	}
	flat, err := flatEncode(d.flatten(), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("16 copies: flat %d bytes, dict %d bytes (%.2fx)",
		len(flat), len(dict), float64(len(flat))/float64(len(dict)))
	if r.Strokes != 16 {
		t.Errorf("want 16 strokes, got %d", r.Strokes)
	}
	if len(dict)*2 > len(flat) {
		t.Errorf("copied shapes did not collapse: dict %d against flat %d", len(dict), len(flat))
	}
}

// TestSVGDictionaryCompaction is the SVG front-end's version of
// TestDictionaryCompaction: a stamped drawing must ship its shape
// once. Quantizing each stamp at its own absolute position instead
// would leave the instances differing by a unit here and there, and
// the dictionary with nothing to match, so this guards the
// quantize-once contract rather than the encoder.
func TestSVGDictionaryCompaction(t *testing.T) {
	raw, err := extractSVG([]byte(stampedDoc(24)))
	if err != nil {
		t.Fatal(err)
	}
	d := layoutOnPlate(raw, placement{posX: math.NaN(), posY: math.NaN()})
	dict, _, r, err := finishDrawing(d, true)
	if err != nil {
		t.Fatalf("finishDrawing: %v", err)
	}
	// The flat baseline: the same geometry with every stamp inlined.
	flat, err := flatEncode(d.flatten(), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("24 stamps: flat %d bytes, dict %d bytes (%.2fx), strokes=%d",
		len(flat), len(dict), float64(len(flat))/float64(len(dict)), r.Strokes)
	if r.Strokes != 24 {
		t.Errorf("want 24 strokes engraved, got %d", r.Strokes)
	}
	// One shape and 24 placements against 24 inlined outlines: the win
	// is structural, so anything short of half is a broken contract.
	if len(dict)*2 > len(flat) {
		t.Errorf("dictionary payload %d not meaningfully under flat %d", len(dict), len(flat))
	}
}

func TestPlacedGeometryMatchesFlat(t *testing.T) {
	// Quantizing the outline and the placement separately costs at
	// most one unit on each axis over rounding the absolute point, so
	// the placed drawing must land within 0.1mm of the flat one.
	for _, pl := range []placement{
		{posX: math.NaN(), posY: math.NaN()},
		{rotate: 30, posX: math.NaN(), posY: math.NaN()},
		{heightMM: 40, rotate: -17, posX: 5, posY: 8},
	} {
		t.Run(fmt.Sprintf("rotate%g", pl.rotate), func(t *testing.T) {
			placedMatchesFlat(t, pl)
		})
	}
}

func placedMatchesFlat(t *testing.T, pl placement) {
	t.Helper()
	raw, err := extractSVG([]byte(stampedDoc(12)))
	if err != nil {
		t.Fatal(err)
	}
	d := layoutOnPlate(raw, pl)
	dict, parsed, _, err := finishDrawing(d, false)
	if err != nil {
		t.Fatal(err)
	}
	flatPayload, err := flatEncode(d.flatten(), false)
	if err != nil {
		t.Fatal(err)
	}
	flat, err := curves.Parse(flatPayload, sh2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := flat.Validate(sh2); err != nil {
		t.Fatal(err)
	}
	if _, err := parsed.Validate(sh2); err != nil {
		t.Fatal(err)
	}
	const tolMM = 0.1
	tol := int(tolMM * float64(sh2.Millimeter))
	fb, db := flat.Bounds, parsed.Bounds
	for _, c := range []struct {
		name      string
		got, want int
	}{
		{"min x", db.Min.X, fb.Min.X}, {"min y", db.Min.Y, fb.Min.Y},
		{"max x", db.Max.X, fb.Max.X}, {"max y", db.Max.Y, fb.Max.Y},
	} {
		if diff := c.got - c.want; diff > tol || diff < -tol {
			t.Errorf("%s off by %d machine units, over the %.1fmm quantization tolerance", c.name, diff, tolMM)
		}
	}
	t.Logf("dict %d bytes vs flat %d bytes", len(dict), len(flatPayload))
}

func TestRealLogos(t *testing.T) {
	for _, f := range []string{"~/Downloads/oshw-logo.svg", "~/Downloads/Bitcoin_logo.svg"} {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Skipf("%s not present", f)
		}
		raw, err := extractSVG(data)
		if err != nil {
			t.Errorf("%s: extract: %v", f, err)
			continue
		}
		d := layoutOnPlate(raw, placement{posX: math.NaN(), posY: math.NaN()})
		if _, _, _, err := finishDrawing(d, true); err != nil {
			t.Errorf("%s: finishDrawing: %v", f, err)
		}
	}
}
