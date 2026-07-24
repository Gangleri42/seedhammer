package main

import (
	"fmt"
	"math"
	"strings"

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

// Payload units: 100 per millimeter, so the 0.3mm needle is 30 units.
// A coordinate is an integer count of these; the plate is 8500 wide.
const (
	payloadUnitsPerMM = 100
	payloadStroke     = 30
)

// emitPayload quantizes millimeter segments to payload units and
// writes the curves path payload. Segments must already be laid out on
// the plate with (0,0) at its top-left corner.
func emitPayload(segs []fseg) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d %s %d %d\n", curves.Version, curves.ModePath, payloadUnitsPerMM, payloadStroke)
	q := func(v float64) int { return int(math.Round(v * payloadUnitsPerMM)) }
	for _, s := range segs {
		switch s.op {
		case svgpath.MoveTo:
			fmt.Fprintf(&b, "M%d %d ", q(s.p[0].X), q(s.p[0].Y))
		case svgpath.LineTo:
			fmt.Fprintf(&b, "L%d %d ", q(s.p[0].X), q(s.p[0].Y))
		case svgpath.QuadTo:
			fmt.Fprintf(&b, "Q%d %d %d %d ", q(s.p[0].X), q(s.p[0].Y), q(s.p[1].X), q(s.p[1].Y))
		case svgpath.CubeTo:
			fmt.Fprintf(&b, "C%d %d %d %d %d %d ",
				q(s.p[0].X), q(s.p[0].Y), q(s.p[1].X), q(s.p[1].Y), q(s.p[2].X), q(s.p[2].Y))
		}
	}
	return strings.TrimRight(b.String(), " ")
}

// finish emits, parses and validates a laid-out drawing. It returns
// the payload bytes, the parsed drawing (for preview) and its gauge
// report. A parse or cap failure is returned as an error with the
// report still filled where possible.
func finish(segs []fseg) (payload string, d *curves.Drawing, r curves.Report, err error) {
	// Guard the payload choke point: a non-finite coordinate (from a
	// malformed source, a degenerate transform, or an arc edge case)
	// would quantize to garbage and desync curves.Parse.
	for _, s := range segs {
		for i := 0; i < s.npts(); i++ {
			if math.IsNaN(s.p[i].X) || math.IsInf(s.p[i].X, 0) || math.IsNaN(s.p[i].Y) || math.IsInf(s.p[i].Y, 0) {
				return "", nil, curves.Report{}, fmt.Errorf("curves: non-finite coordinate %v in geometry", s.p[i])
			}
		}
	}
	payload = emitPayload(segs)
	d, err = curves.Parse([]byte(payload), sh2)
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
