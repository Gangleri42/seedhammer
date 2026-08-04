// Package curves implements the seedhammer.com:curves payload:
// free-form vector engravings received over NFC.
//
// Every payload opens with a one-line ASCII header, the fields
// separated by spaces and terminated by a newline:
//
//	version mode units-per-mm stroke-width
//
// The leading token dispatches the version and mode before any body
// parse. Version 2 is the only format: text mode carries plain UTF-8
// plate text, and path mode a compact binary stream of M/L/Q/C
// commands with relative zigzag-varint coordinates that may open with
// a dictionary of repeated shapes placements stamp by reference. See
// Version, EncodePath and EncodeGroups. (Version 1's ASCII path body
// retired in the coordinated firmware+Studio cutover; no v1 payloads
// remain in the wild.)
//
// Coordinates are payload units, converted to machine units through
// units-per-mm; stroke-width is the width the source device assumed, in
// payload units, and must match the machine's needle.
//
// The source device is responsible for all layout: coordinates are
// plate-absolute, with (0, 0) the top left corner of the plate. The
// device re-fits the geometry to its own spline representation and
// plans velocities itself, so incoming geometry carries no timing.
package curves

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"seedhammer.com/bezier"
	"seedhammer.com/bspline"
	"seedhammer.com/engrave"
	"seedhammer.com/font/vector"
	"seedhammer.com/svgpath"
)

// RecordType is the NDEF external record type carrying a curves
// payload.
const RecordType = "seedhammer.com:curves"

// Version is the payload format version this package implements: the
// binary path body and plain text mode. The version 1 ASCII path
// decoder is gone; the parser accepts version 2 only.
const Version = 2

// Engraving limits a curves drawing must satisfy to be engraved,
// shared by the firmware (gui) and the host converter (cmd/svgplate).
// The duration cap bounds unattended machine time — an operator
// policy, and since 2026-07-26 the only complexity gate: the planner
// streams every drawing through a fixed 100-knot window, so the
// structural stroke and knot caps of the original curves flow
// (a28b918: 512 strokes, 16384 knots, 2048 knots per stroke) guarded
// no real resource and were retired once the payload dictionary made
// dense text plates cheap enough to reach them. The plate geometry
// keeps the head on the plate.
const (
	MaxMinutes = 60
	// PlateMM is the plate width every format shares (and the square
	// plate's side) and SafetyMarginMM its engravable keepout, both
	// in millimeters. Payload coordinates are bounded by the square
	// plate; which physical plate they land on is decided on the
	// device. A gui test asserts these match the firmware's own
	// plate geometry.
	PlateMM        = 85
	SafetyMarginMM = 3
)

// The two payload modes a curves record can carry, named in the
// second header field. Text is a plate of engravable text the
// firmware lays out and renders from its own font; path is SVG path
// geometry the firmware engraves directly.
const (
	ModeText = "text"
	ModePath = "path"
)

// Mode reports a payload's mode from its header line, which is
// "version mode ..." for both kinds.
func Mode(data []byte) (string, error) {
	// Read only the header: a v2 body is binary and may hold 0x0a bytes,
	// so never stringify past the first newline.
	var header string
	if nl := bytes.IndexByte(data, '\n'); nl < 0 {
		header = string(data)
	} else {
		header = string(data[:nl])
	}
	fields := strings.Fields(header)
	if len(fields) < 2 {
		return "", fmt.Errorf("curves: malformed header %q", header)
	}
	if v, err := strconv.Atoi(fields[0]); err != nil || v != Version {
		return "", fmt.Errorf("curves: unsupported version %q", fields[0])
	}
	switch m := fields[1]; m {
	case ModeText, ModePath:
		return m, nil
	default:
		return "", fmt.Errorf("curves: unknown mode %q", m)
	}
}

// Text returns the plate text of a text-mode payload.
func Text(data []byte) (string, error) {
	mode, err := Mode(data)
	if err != nil {
		return "", err
	}
	if mode != ModeText {
		return "", fmt.Errorf("curves: not a text payload")
	}
	_, body, _ := strings.Cut(string(data), "\n")
	return body, nil
}

// maxCoord bounds a scaled coordinate, in machine units. It sits far
// above the plate (~164mm at 6400 units/mm) but well inside the
// fixed-point headroom of the bezier sampler, so a hostile payload
// with absurd coordinates is clamped out of the plate bounds instead
// of overflowing arithmetic or dividing by zero.
const maxCoord = 1 << 20

// maxRun caps the sample points a single stroke may accumulate. A
// stroke this long already exceeds the knot cap; the limit only keeps
// a pathological unbroken stroke from growing the sample buffer to
// exhaustion before the count is checked.
const maxRun = 4096

var errStrokeTooLong = errors.New("stroke too long")

// Hard parse bounds. The dictionary lets a 32 KB payload expand to
// millions of knots, so the counting walk stops once a drawing cannot
// possibly fit the duration cap; a full hour of engraving plans to
// well under 100k knots, so these sit far above any honest drawing —
// Validate's duration check owns the real rejections, with its gauge
// report intact — and only expansion bombs reach them.
//
// The segment bound backs the knot bound: degenerate zero-length
// segments decode without yielding a knot, so a knot count alone would
// let a placement bomb of degenerate filler walk millions of segments
// on the device before erroring. Real drawings yield at least as many
// knots as segments (the fitter samples at stroke width).
const (
	parseMaxStrokes = 16384
	parseMaxKnots   = 1 << 18
	parseMaxSegs    = 1 << 18
)

// Drawing is a validated curves payload, ready for engraving.
type Drawing struct {
	// Strokes counts the engraved strokes.
	Strokes int
	// Knots counts the spline knots of the converted drawing.
	Knots int
	// MaxStrokeKnots is the largest number of knots in a single
	// stroke.
	MaxStrokeKnots int
	// Bounds is the hull of the converted spline knots, in machine
	// units.
	Bounds bspline.Bounds

	// binary aliases the payload's body, so a Drawing retains only the
	// wire bytes, not a materialized geometry; dict holds the byte
	// ranges of the dictionary shapes and mainOff the offset where the
	// main stream starts — the one small index a dictionary payload
	// retains beyond the wire bytes.
	binary  []byte
	dict    [][2]int32
	mainOff int
	// wireBytes is the full payload length (header included), the figure
	// Report.Bytes gauges against the NDEF cap for either format.
	wireBytes int
	scale     float64
	prec      int
}

// Parse validates a path-mode curves payload against the engraver
// parameters. Text-mode payloads are the caller's concern; see Mode
// and Text. Parse is Open followed by an unobserved Walk; callers
// that consume the walk — the firmware feeds it straight into the
// planner — use the two halves themselves.
func Parse(data []byte, params engrave.Params) (*Drawing, error) {
	d, err := Open(data, params)
	if err != nil {
		return nil, err
	}
	if err := d.Walk(nil, nil); err != nil {
		return nil, err
	}
	return d, nil
}

// Open validates a path-mode payload's header and dictionary framing
// against the engraver parameters, leaving the geometry unwalked:
// the returned Drawing's stats stay zero until Walk fills them.
func Open(data []byte, params engrave.Params) (*Drawing, error) {
	// The header is ASCII up to the first newline; a v2 body is binary
	// and may hold 0x0a, so split on the byte, never stringify past it.
	nl := bytes.IndexByte(data, '\n')
	if nl < 0 {
		return nil, fmt.Errorf("curves: missing header")
	}
	header, body := string(data[:nl]), data[nl+1:]
	// version path units-per-mm stroke-width
	fields := strings.Fields(header)
	if len(fields) != 4 || fields[1] != ModePath {
		return nil, fmt.Errorf("curves: malformed path header %q", header)
	}
	var vals [3]int
	for i, f := range []string{fields[0], fields[2], fields[3]} {
		v, err := strconv.Atoi(f)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("curves: malformed header %q", header)
		}
		vals[i] = v
	}
	version, unitsPerMM, strokeWidth := vals[0], vals[1], vals[2]
	scale := float64(params.Millimeter) / float64(unitsPerMM)
	if w := int(math.Round(float64(strokeWidth) * scale)); 8*abs(w-params.StrokeWidth) > params.StrokeWidth {
		return nil, fmt.Errorf("curves: stroke width %d units differs from the %d machine units engraved", w, params.StrokeWidth)
	}
	d := &Drawing{
		wireBytes: len(data),
		scale:     scale,
		prec:      max(1, params.StrokeWidth),
	}
	if version != Version {
		return nil, fmt.Errorf("curves: unsupported version %d", version)
	}
	// An optional shape dictionary opens the body; its framing is
	// validated here so run can jump between the shape byte ranges
	// and the main stream. The streams themselves need only the
	// leading-move guarantee up front, the rest is validated as run
	// walks them (bad opcodes, truncated varints, bad placements).
	dict, main, derr := scanDict(body)
	if derr != nil {
		return nil, fmt.Errorf("curves: %w", derr)
	}
	if main >= len(body) {
		return nil, fmt.Errorf("curves: path data must begin with a move")
	}
	if b := body[main]; b != 'M' && (b != 'G' || dict == nil) {
		return nil, fmt.Errorf("curves: path data must begin with a move")
	}
	d.binary = body
	d.dict = dict
	d.mainOff = main
	return d, nil
}

// ErrCanceled reports a walk stopped by its progress callback.
var ErrCanceled = errors.New("curves: canceled")

// Walk validates an opened drawing in one walk of its geometry,
// filling in the stats and enforcing the expansion bounds. The
// converted commands stream to yield — nil to validate without a
// consumer — so a caller can feed the planner and measure, preview
// and gauge the drawing during the same walk. progress, if not nil,
// is called as the walk advances with the consumed and total byte
// counts of the payload's main stream; returning false stops the
// walk with ErrCanceled.
func (d *Drawing) Walk(progress func(done, total int) bool, yield func(engrave.Command) bool) error {
	d.Strokes, d.Knots, d.MaxStrokeKnots, d.Bounds = 0, 0, 0, bspline.Bounds{}
	var (
		first    = true
		engraved = false
		last     bezier.Point
		eq       int
		run      int
		bomb     error
	)
	err := d.run(progress, func(cmd engrave.Command) bool {
		k, ok := cmd.AsKnot()
		if !ok {
			return yield == nil || yield(cmd)
		}
		if first {
			d.Bounds = bspline.Bounds{Min: k.Knot, Max: k.Knot}
			first = false
		} else {
			d.Bounds = d.Bounds.Union(bspline.Bounds{Min: k.Knot, Max: k.Knot})
		}
		d.Knots++
		if k.Engrave {
			if !engraved {
				d.Strokes++
			}
			engraved = true
		} else {
			engraved = false
		}
		if d.Strokes > parseMaxStrokes || d.Knots > parseMaxKnots {
			bomb = fmt.Errorf("the drawing expands past %d strokes or %d knots", parseMaxStrokes, parseMaxKnots)
			return false
		}
		if k.Knot == last && d.Knots > 1 {
			eq++
		} else {
			eq = 1
			last = k.Knot
		}
		run++
		if eq == 3 {
			// A tripled knot clamps the spline, bounding the
			// stroke buffered by the planner.
			d.MaxStrokeKnots = max(d.MaxStrokeKnots, run)
			run = 0
		}
		return yield == nil || yield(cmd)
	})
	if bomb != nil {
		err = bomb
	}
	if err != nil {
		if errors.Is(err, ErrCanceled) {
			return err
		}
		return fmt.Errorf("curves: %w", err)
	}
	d.MaxStrokeKnots = max(d.MaxStrokeKnots, run)
	if d.Strokes == 0 {
		return fmt.Errorf("curves: empty drawing")
	}
	return nil
}

// Engraving returns the drawing as engraver commands. The returned
// engraving is re-iterable and deterministic.
func (d *Drawing) Engraving() engrave.Engraving {
	return func(yield func(engrave.Command) bool) {
		// The payload was fully validated by a Walk; run cannot fail.
		d.run(nil, yield)
	}
}

// run converts the path data to engraver commands: parse segments,
// clamp sharp corners, sample and fit each smooth run to spline
// knots. Closed smooth contours become periodic loops, paced by the
// planner across their seam instead of against a clamp. progress, if
// not nil, observes the decoder's main-stream position per segment
// and stops the walk with ErrCanceled by returning false.
func (d *Drawing) run(progress func(done, total int) bool, yield func(engrave.Command) bool) error {
	b := svgpath.NewBuilder(d.prec, true, svgpath.ControlFit(), func(k vector.Knot) bool {
		if k.Periodic {
			return yield(engrave.PeriodicPoint(k.Ctrl))
		}
		return yield(engrave.ControlPoint(k.Line, k.Ctrl))
	})
	b.LimitRun(maxRun, errStrokeTooLong)
	b.Periodic()
	scale := func(v float64) int {
		v = math.Round(v * d.scale)
		return int(min(max(v, -maxCoord), maxCoord))
	}
	it := newBinaryIter(d.binary, d.dict, d.mainOff, scale)
	var (
		pen   bezier.Point
		out   bezier.Point
		drawn bool
		segs  int
	)
	for {
		s, ok := it.Next()
		if !ok {
			break
		}
		if segs++; segs > parseMaxSegs {
			return fmt.Errorf("the drawing expands past %d segments", parseMaxSegs)
		}
		if progress != nil && !progress(it.mainPos()-d.mainOff, len(d.binary)-d.mainOff) {
			return ErrCanceled
		}
		if s.Op != svgpath.MoveTo && degenerate(s, pen) {
			// Zero-length drawing segments carry no geometry, but
			// would clamp or fail sampling. A deliberate clamp is
			// meaningful only after drawing.
			if s.Op == svgpath.LineTo && drawn {
				if !b.Add(s) {
					break
				}
				out = bezier.Point{}
				drawn = false
			}
			continue
		}
		if in, ok := inTangent(s, pen); ok && drawn && sharp(out, in) {
			if !b.Add(svgpath.Segment{Op: svgpath.LineTo, Args: [4]bezier.Point{pen}}) {
				break
			}
		}
		if !b.Add(s) {
			break
		}
		pen, out = advance(s, pen)
		drawn = s.Op != svgpath.MoveTo
	}
	if err := it.Err(); err != nil {
		return err
	}
	return b.Close()
}

// degenerate reports whether every point of a drawing segment
// coincides with the pen position.
func degenerate(s svgpath.Segment, pen bezier.Point) bool {
	n := 1
	switch s.Op {
	case svgpath.QuadTo:
		n = 2
	case svgpath.CubeTo:
		n = 3
	}
	for _, p := range s.Args[:n] {
		if p != pen {
			return false
		}
	}
	return true
}

// advance returns the pen position after s and the tangent leaving
// its endpoint. Moves have no tangent.
func advance(s svgpath.Segment, pen bezier.Point) (bezier.Point, bezier.Point) {
	switch s.Op {
	case svgpath.MoveTo:
		return s.Args[0], bezier.Point{}
	case svgpath.LineTo:
		return s.Args[0], s.Args[0].Sub(pen)
	case svgpath.QuadTo:
		p12, p3 := s.Args[0], s.Args[1]
		return p3, firstNonZero(p3.Sub(p12), p3.Sub(pen))
	case svgpath.CubeTo:
		p1, p2, p3 := s.Args[0], s.Args[1], s.Args[2]
		return p3, firstNonZero(p3.Sub(p2), p3.Sub(p1), p3.Sub(pen))
	}
	panic("unknown segment type")
}

// inTangent returns the tangent entering s from the pen position.
func inTangent(s svgpath.Segment, pen bezier.Point) (bezier.Point, bool) {
	var t bezier.Point
	switch s.Op {
	case svgpath.MoveTo:
		return bezier.Point{}, false
	case svgpath.LineTo:
		t = s.Args[0].Sub(pen)
	case svgpath.QuadTo:
		t = firstNonZero(s.Args[0].Sub(pen), s.Args[1].Sub(pen))
	case svgpath.CubeTo:
		t = firstNonZero(s.Args[0].Sub(pen), s.Args[1].Sub(pen), s.Args[2].Sub(pen))
	}
	return t, t != bezier.Point{}
}

func firstNonZero(ps ...bezier.Point) bezier.Point {
	for _, p := range ps {
		if (p != bezier.Point{}) {
			return p
		}
	}
	return bezier.Point{}
}

// sharp reports whether the turn from tangent a to tangent b exceeds
// 45°. Such corners are clamped instead of smoothed over.
func sharp(a, b bezier.Point) bool {
	if (a == bezier.Point{}) {
		return false
	}
	for abs(a.X) >= 1<<12 || abs(a.Y) >= 1<<12 {
		a.X >>= 1
		a.Y >>= 1
	}
	for abs(b.X) >= 1<<12 || abs(b.Y) >= 1<<12 {
		b.X >>= 1
		b.Y >>= 1
	}
	dot := int64(a.X)*int64(b.X) + int64(a.Y)*int64(b.Y)
	if dot <= 0 {
		return true
	}
	a2 := int64(a.X)*int64(a.X) + int64(a.Y)*int64(a.Y)
	b2 := int64(b.X)*int64(b.X) + int64(b.Y)*int64(b.Y)
	return 2*dot*dot < a2*b2
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// Report summarizes a drawing's cost against the engraving caps. All
// dimensions are in machine units; Seconds is the planned engraving
// time rounded up.
type Report struct {
	Bytes          int
	Strokes        int
	Knots          int
	MaxStrokeKnots int
	Bounds         bspline.Bounds
	DurationTicks  uint
	Seconds        int
}

// Validate reports the first engraving cap a drawing violates, or nil
// if it fits. It is the shared gate for the firmware and the host
// converter, so both reject the same payloads for the same reasons.
// The returned Report is filled whether or not the drawing fits, so a
// caller can show every gauge next to its cap. Duration comes from the
// same PlanEngraving the firmware's toPlate uses; Bounds is the
// drawing's own knot hull, the field the firmware checks against the
// plate, so it includes travel moves the planned spline may drop.
func (d *Drawing) Validate(params engrave.Params) (Report, error) {
	spline := engrave.PlanEngraving(params.StepperConfig, d.Engraving())
	attrs := bspline.Measure(spline)
	secs := 0
	if tps := params.TicksPerSecond; tps > 0 {
		secs = int((attrs.Duration + tps - 1) / tps)
	}
	r := Report{
		Bytes:          d.wireBytes,
		Strokes:        d.Strokes,
		Knots:          d.Knots,
		MaxStrokeKnots: d.MaxStrokeKnots,
		Bounds:         d.Bounds,
		DurationTicks:  attrs.Duration,
		Seconds:        secs,
	}
	mm := params.Millimeter
	margin := bezier.Pt(SafetyMarginMM*mm, SafetyMarginMM*mm)
	plate := bezier.Pt(PlateMM*mm, PlateMM*mm)
	if !r.Bounds.In(bspline.Bounds{Min: margin, Max: plate.Sub(margin)}) {
		return r, fmt.Errorf("curves: the drawing runs outside the %dmm plate's %dmm margin", PlateMM, SafetyMarginMM)
	}
	if r.Seconds > MaxMinutes*60 {
		return r, fmt.Errorf("curves: the engraving would run %d:%02d, over the %d minute cap", r.Seconds/60, r.Seconds%60, MaxMinutes)
	}
	return r, nil
}
