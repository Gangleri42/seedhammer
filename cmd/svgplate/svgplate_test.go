package main

import (
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
