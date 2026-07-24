package main

import (
	"fmt"
	"math"

	"seedhammer.com/bezier"
	"seedhammer.com/curves"
	"seedhammer.com/engrave"
	"seedhammer.com/nfc/type4"
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

// emitPayload quantizes millimeter segments to payload units and encodes
// them as a Version2 binary curves path payload. Segments must already be
// laid out on the plate with (0,0) at its top-left corner. When order is
// set the strokes are resequenced to shorten head travel; the drawing is
// non-secret art, so this only saves time, never leaks content.
func emitPayload(segs []fseg, order bool) ([]byte, error) {
	q := func(v float64) int { return int(math.Round(v * payloadUnitsPerMM)) }
	out := make([]svgpath.Segment, len(segs))
	for i, s := range segs {
		out[i].Op = s.op
		for j := 0; j < s.npts(); j++ {
			out[i].Args[j] = bezier.Pt(q(s.p[j].X), q(s.p[j].Y))
		}
	}
	if order {
		// Ordering is quadratic in strokes. Skip it for inputs already far
		// past the stroke cap: validation rejects them right after, and a
		// pathological SVG must not grind for minutes first.
		strokes := 0
		for _, s := range out {
			if s.Op == svgpath.MoveTo {
				strokes++
			}
		}
		if strokes <= 2*curves.MaxStrokes {
			out = curves.Order(out)
		}
	}
	return curves.EncodePath(payloadUnitsPerMM, payloadStroke, out)
}

// finish emits, parses and validates a laid-out drawing. It returns
// the payload bytes, the parsed drawing (for preview) and its gauge
// report. A parse or cap failure is returned as an error with the
// report still filled where possible.
func finish(segs []fseg, order bool) (payload []byte, d *curves.Drawing, r curves.Report, err error) {
	// Guard the payload choke point: a non-finite coordinate (from a
	// malformed source, a degenerate transform, or an arc edge case)
	// would quantize to garbage and desync curves.Parse.
	for _, s := range segs {
		for i := 0; i < s.npts(); i++ {
			if math.IsNaN(s.p[i].X) || math.IsInf(s.p[i].X, 0) || math.IsNaN(s.p[i].Y) || math.IsInf(s.p[i].Y, 0) {
				return nil, nil, curves.Report{}, fmt.Errorf("curves: non-finite coordinate %v in geometry", s.p[i])
			}
		}
	}
	payload, err = emitPayload(segs, order)
	if err != nil {
		return payload, nil, curves.Report{Bytes: len(payload)}, err
	}
	d, err = curves.Parse(payload, sh2)
	if err != nil {
		return payload, nil, curves.Report{Bytes: len(payload)}, err
	}
	r, err = d.Validate(sh2)
	// The NDEF file cap is independent of the drawing caps: a payload
	// can fit every knot limit yet be too large to write to the tag.
	if err == nil && len(payload) > payloadByteCap {
		err = fmt.Errorf("curves: payload is %d bytes, over the %d byte NDEF cap", len(payload), payloadByteCap)
	}
	return payload, d, r, err
}
