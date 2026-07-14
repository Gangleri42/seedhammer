// Package curves implements the seedhammer.com:curves payload:
// free-form vector engravings received over NFC.
//
// A payload is ASCII text: a header line of three positive decimal
// integers
//
//	version units-per-mm stroke-width
//
// followed by SVG path data restricted to the absolute commands M,
// L, C, Q and Z. Coordinates are in payload units, converted through
// units-per-mm; stroke-width is the width the source device assumed,
// in payload units, and must match the machine's needle.
//
// The source device is responsible for all layout: coordinates are
// plate-absolute, with (0, 0) the top left corner of the plate. The
// device re-fits the geometry to its own spline representation and
// plans velocities itself, so incoming geometry carries no timing.
package curves

import (
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

// Version is the payload format version this package implements.
const Version = 1

// Engraving caps a curves drawing must satisfy to be engraved. The
// knot caps bound the planner's per-stroke buffering, the time cap
// bounds unattended machine time, and the plate geometry keeps the
// head on the plate. They are the single source of these limits,
// shared by the firmware (gui) and the host converter (cmd/svgplate);
// none are reachable by drawings of sane complexity.
const (
	MaxStrokes     = 512
	MaxKnots       = 16384
	MaxStrokeKnots = 2048
	MaxMinutes     = 45
	// PlateMM is the square plate side and SafetyMarginMM its
	// engravable keepout, both in millimeters. A gui test asserts
	// these match the firmware's own plate geometry.
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
	header, _, _ := strings.Cut(string(data), "\n")
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

	path  string
	scale float64
	prec  int
}

// Parse validates a path-mode curves payload against the engraver
// parameters. Text-mode payloads are the caller's concern; see Mode
// and Text.
func Parse(data []byte, params engrave.Params) (*Drawing, error) {
	header, path, ok := strings.Cut(string(data), "\n")
	if !ok {
		return nil, fmt.Errorf("curves: missing header")
	}
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
	if version != Version {
		return nil, fmt.Errorf("curves: unsupported version %d", version)
	}
	scale := float64(params.Millimeter) / float64(unitsPerMM)
	if w := int(math.Round(float64(strokeWidth) * scale)); 8*abs(w-params.StrokeWidth) > params.StrokeWidth {
		return nil, fmt.Errorf("curves: stroke width %d units differs from the %d machine units engraved", w, params.StrokeWidth)
	}
	for i := 0; i < len(path); i++ {
		switch c := path[i]; {
		case c == 'M' || c == 'L' || c == 'C' || c == 'Q' || c == 'Z':
		case '0' <= c && c <= '9' || c == '.' || c == '-':
		case c == ' ' || c == ',' || c == '\t' || c == '\n':
		default:
			return nil, fmt.Errorf("curves: unsupported byte %q in path data", c)
		}
	}
	if p := strings.TrimLeft(path, " ,\t\n"); p == "" || p[0] != 'M' {
		return nil, fmt.Errorf("curves: path data must begin with M")
	}
	d := &Drawing{
		path:  path,
		scale: scale,
		prec:  max(1, params.StrokeWidth),
	}
	var (
		first    = true
		engraved = false
		last     bezier.Point
		eq       int
		run      int
	)
	err := d.run(func(cmd engrave.Command) bool {
		k, ok := cmd.AsKnot()
		if !ok {
			return true
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
		return true
	})
	if err != nil {
		return nil, fmt.Errorf("curves: %w", err)
	}
	d.MaxStrokeKnots = max(d.MaxStrokeKnots, run)
	if d.Strokes == 0 {
		return nil, fmt.Errorf("curves: empty drawing")
	}
	return d, nil
}

// Engraving returns the drawing as engraver commands. The returned
// engraving is re-iterable and deterministic.
func (d *Drawing) Engraving() engrave.Engraving {
	return func(yield func(engrave.Command) bool) {
		// The payload was fully validated by Parse; run cannot fail.
		d.run(yield)
	}
}

// run converts the path data to engraver commands: parse segments,
// clamp sharp corners, sample and fit each smooth run to spline
// knots. Closed smooth contours become periodic loops, paced by the
// planner across their seam instead of against a clamp.
func (d *Drawing) run(yield func(engrave.Command) bool) error {
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
	it := svgpath.NewIter(d.path, 0, 0, scale)
	var (
		pen   bezier.Point
		out   bezier.Point
		drawn bool
	)
	for {
		s, ok := it.Next()
		if !ok {
			break
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
		Bytes:          len(d.path),
		Strokes:        d.Strokes,
		Knots:          d.Knots,
		MaxStrokeKnots: d.MaxStrokeKnots,
		Bounds:         d.Bounds,
		DurationTicks:  attrs.Duration,
		Seconds:        secs,
	}
	switch {
	case d.Strokes > MaxStrokes:
		return r, fmt.Errorf("curves: %d strokes exceeds the %d supported", d.Strokes, MaxStrokes)
	case d.Knots > MaxKnots:
		return r, fmt.Errorf("curves: %d knots exceeds the %d supported", d.Knots, MaxKnots)
	case d.MaxStrokeKnots > MaxStrokeKnots:
		return r, fmt.Errorf("curves: a stroke of %d knots exceeds the %d supported", d.MaxStrokeKnots, MaxStrokeKnots)
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
