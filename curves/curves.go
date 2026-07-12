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

// Parse validates a curves payload against the engraver parameters.
func Parse(data []byte, params engrave.Params) (*Drawing, error) {
	header, path, ok := strings.Cut(string(data), "\n")
	if !ok {
		return nil, fmt.Errorf("curves: missing header")
	}
	fields := strings.Fields(header)
	if len(fields) != 3 {
		return nil, fmt.Errorf("curves: malformed header %q", header)
	}
	var vals [3]int
	for i, f := range fields {
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
// knots.
func (d *Drawing) run(yield func(engrave.Command) bool) error {
	b := svgpath.NewBuilder(d.prec, true, svgpath.ControlFit(), func(k vector.Knot) bool {
		return yield(engrave.ControlPoint(k.Line, k.Ctrl))
	})
	b.LimitRun(maxRun, errStrokeTooLong)
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
