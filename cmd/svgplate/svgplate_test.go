package main

import (
	"encoding/xml"
	"math"
	"os"
	"strings"
	"testing"
	"time"

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
	segs := layoutOnPlate(raw, placement{posX: math.NaN(), posY: math.NaN()})
	_, _, r, err := finish(segs, true)
	if err != nil {
		t.Fatalf("finish: %v", err)
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
	_, _, r, err := finish(segs, true)
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
	for _, s := range raw {
		for i := 0; i < s.npts(); i++ {
			if math.IsNaN(s.p[i].X) || math.IsNaN(s.p[i].Y) {
				t.Fatalf("NaN reached geometry: %v", s.p[i])
			}
		}
	}
	// The guard itself rejects a hand-built non-finite segment.
	bad := []fseg{{op: svgpath.MoveTo, p: [3]fpt{{math.Inf(1), 0}}}}
	if _, _, _, err := finish(bad, true); err == nil {
		t.Error("finish accepted a non-finite coordinate")
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
	got := moves(segs)
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
	got := moves(segs)
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
	if got := moves(segs); len(got) != 1 || !approx(got[0].X, 20, 1e-9) || !approx(got[0].Y, 30, 1e-9) {
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
	if got := moves(segs); len(got) != 1 || !approx(got[0].X, 40, 1e-9) {
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
		segs := layoutOnPlate(raw, placement{posX: math.NaN(), posY: math.NaN()})
		if _, _, _, err := finish(segs, true); err != nil {
			t.Errorf("%s: finish: %v", f, err)
		}
	}
}
