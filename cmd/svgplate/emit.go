package main

import (
	"fmt"
	"math"

	"seedhammer.com/bezier"
	"seedhammer.com/curves"
	"seedhammer.com/engrave"
	"seedhammer.com/nfc/type4"
	"seedhammer.com/richtext"
	"seedhammer.com/svgpath"
)

// payloadByteCap is the largest curves payload the tag can hold: the
// NDEF file size less record framing headroom, matching cmd/textplate.
const payloadByteCap = type4.NDEFFileSize - 64

// sh2 is the shared SeedHammer II engraver profile (engrave.SH2Params). A
// payload is validated against it so the converter accepts exactly what the
// device engraves.
var sh2 = engrave.SH2Params

// Payload units: 10 per millimeter, so a coordinate is a count of 0.1mm
// steps and the 0.3mm needle is 3 units. The 0.1mm quantization is below
// the needle and the planner's sampling, so it engraves identically to a
// finer grid while keeping the v2 relative deltas small. The plate is 850
// units wide.
const (
	payloadUnitsPerMM = 10
	payloadStroke     = 3
)

// orderMaxStrokes bounds the O(strokes^2) travel ordering; far above
// any drawing the duration cap admits.
const orderMaxStrokes = 4096

// emitGroups encodes placed millimeter groups with the shape
// dictionary: richtext.Groups quantizes local frame and placement
// separately (the contract that keeps glyph instances byte-identical),
// then the ordered groups encode.
func emitGroups(groups []richtext.Group, order bool) ([]byte, error) {
	out := richtext.Groups(groups, payloadUnitsPerMM)
	strokes := 0
	for _, g := range out {
		for _, s := range g.Segs {
			if s.Op == svgpath.MoveTo {
				strokes++
			}
		}
	}
	// Ordering is quadratic in strokes; skip it for inputs no sane
	// drawing reaches.
	if order && strokes <= orderMaxStrokes {
		out = curves.OrderGroups(out)
	}
	return curves.EncodeGroups(payloadUnitsPerMM, payloadStroke, out)
}

// emitDrawing quantizes an instanced drawing and encodes it through
// the shape dictionary. Each outline quantizes once in its own frame
// and every placement quantizes on its own, which is the contract
// EncodeGroups needs: rounding a shape afresh at each position leaves
// the instances differing by a unit here and there, and nothing to
// deduplicate.
func emitDrawing(d *drawing, order bool) ([]byte, error) {
	q := func(v float64) int { return int(math.Round(v * payloadUnitsPerMM)) }
	shapes := make([][]svgpath.Segment, len(d.shapes))
	strokes := make([]int, len(d.shapes))
	for i, shape := range d.shapes {
		segs := make([]svgpath.Segment, len(shape))
		for j, s := range shape {
			segs[j].Op = s.op
			for k := 0; k < s.npts(); k++ {
				segs[j].Args[k] = bezier.Pt(q(s.p[k].X), q(s.p[k].Y))
			}
			if s.op == svgpath.MoveTo {
				strokes[i]++
			}
		}
		shapes[i] = segs
	}
	groups := make([]curves.Group, len(d.groups))
	total := 0
	for i, g := range d.groups {
		groups[i] = curves.Group{At: bezier.Pt(q(g.at.X), q(g.at.Y)), Segs: shapes[g.shape]}
		total += strokes[g.shape]
	}
	// Resequencing shortens head travel; a drawing is non-secret art,
	// so it only saves time and never leaks content. Ordering is
	// quadratic in strokes, so it is skipped past a count no sane
	// drawing reaches: a pathological SVG must not grind for minutes
	// before validation rejects it on duration.
	if order && total <= orderMaxStrokes {
		groups = curves.OrderGroups(groups)
	}
	return curves.EncodeGroups(payloadUnitsPerMM, payloadStroke, groups)
}

// finishDrawing emits, parses and validates a laid-out drawing.
// It returns the payload bytes, the parsed drawing (for preview)
// and its gauge report. A parse or cap failure is returned as an
// error with the report still filled where possible.
//
// The non-finite guard covers the payload choke point: a coordinate
// from a malformed source, a degenerate transform or an arc edge
// case would quantize to garbage and desync curves.Parse.
func finishDrawing(d *drawing, order bool) ([]byte, *curves.Drawing, curves.Report, error) {
	for _, g := range d.groups {
		if !finite(g.at.X, g.at.Y) {
			return nil, nil, curves.Report{}, fmt.Errorf("curves: non-finite coordinate %v in geometry", g.at)
		}
	}
	for _, shape := range d.shapes {
		for _, s := range shape {
			for i := 0; i < s.npts(); i++ {
				if !finite(s.p[i].X, s.p[i].Y) {
					return nil, nil, curves.Report{}, fmt.Errorf("curves: non-finite coordinate %v in geometry", s.p[i])
				}
			}
		}
	}
	payload, err := emitDrawing(d, order)
	return validatePayload(payload, err)
}

// finishGroups is finishDrawing for the rich-text front-end.
func finishGroups(groups []richtext.Group, order bool) ([]byte, *curves.Drawing, curves.Report, error) {
	for _, g := range groups {
		if !finite(g.At.X, g.At.Y) {
			return nil, nil, curves.Report{}, fmt.Errorf("curves: non-finite coordinate %v in geometry", g.At)
		}
		for _, s := range g.Segs {
			for i := 0; i < s.NPts(); i++ {
				if !finite(s.P[i].X, s.P[i].Y) {
					return nil, nil, curves.Report{}, fmt.Errorf("curves: non-finite coordinate %v in geometry", s.P[i])
				}
			}
		}
	}
	payload, err := emitGroups(groups, order)
	return validatePayload(payload, err)
}

func finite(x, y float64) bool {
	return !math.IsNaN(x) && !math.IsInf(x, 0) && !math.IsNaN(y) && !math.IsInf(y, 0)
}

// validatePayload parses an emitted payload back and validates it
// against the shared caps: the converter accepts exactly what the
// device engraves.
func validatePayload(payload []byte, err error) ([]byte, *curves.Drawing, curves.Report, error) {
	if err != nil {
		return payload, nil, curves.Report{Bytes: len(payload)}, err
	}
	d, err := curves.Parse(payload, sh2)
	if err != nil {
		return payload, nil, curves.Report{Bytes: len(payload)}, err
	}
	r, err := d.Validate(sh2)
	// The NDEF file cap is independent of the drawing caps: a payload
	// can fit every knot limit yet be too large to write to the tag.
	if err == nil && len(payload) > payloadByteCap {
		err = fmt.Errorf("curves: payload is %d bytes, over the %d byte NDEF cap", len(payload), payloadByteCap)
	}
	return payload, d, r, err
}
