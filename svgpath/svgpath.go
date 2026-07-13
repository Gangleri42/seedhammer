// Package svgpath converts SVG path data into segments and further
// into the uniform B-spline representation used for engraving.
package svgpath

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"seedhammer.com/bezier"
	"seedhammer.com/font/vector"
)

type Segment struct {
	Op   SegmentOp
	Args [4]bezier.Point
}

type SegmentOp uint32

const (
	MoveTo SegmentOp = iota
	LineTo
	QuadTo
	CubeTo
)

// ParseData parses SVG path data, the "d" attribute of a <path>
// element, offset by (offx, offy). Coordinates are converted
// through scale.
func ParseData(d string, offx, offy int, scale func(float64) int) ([]Segment, error) {
	var segs []Segment
	it := NewIter(d, offx, offy, scale)
	for {
		s, ok := it.Next()
		if !ok {
			break
		}
		segs = append(segs, s)
	}
	return segs, it.Err()
}

// Iter parses SVG path data incrementally, one segment per call to
// Next. It supports the commands M, L, H, V, C, S, Q, Z and their
// relative forms.
type Iter struct {
	d          string
	offx, offy int
	scale      func(float64) int

	op        rune
	implicit  bool // a moveto's subsequent pairs are linetos.
	pen       bezier.Point
	initPoint bezier.Point
	ctrl2     bezier.Point
	err       error
}

func NewIter(d string, offx, offy int, scale func(float64) int) *Iter {
	pen := bezier.Pt(offx, offy)
	return &Iter{
		d:         strings.TrimSpace(d),
		offx:      offx,
		offy:      offy,
		scale:     scale,
		pen:       pen,
		initPoint: pen,
		ctrl2:     pen,
	}
}

// Err reports the first error encountered by Next.
func (it *Iter) Err() error {
	return it.err
}

// Next returns the next segment, if any.
func (it *Iter) Next() (Segment, bool) {
	for it.err == nil {
		it.d = strings.TrimLeft(it.d, " ,\t\n")
		if len(it.d) == 0 {
			return Segment{}, false
		}
		if c := rune(it.d[0]); !isNumberStart(byte(c)) {
			// A command letter.
			orig := it.d
			it.d = it.d[1:]
			switch c {
			case 'M', 'm', 'V', 'v', 'L', 'l', 'H', 'h', 'C', 'c', 'S', 's', 'Q', 'q':
				it.op = c
				it.implicit = false
				continue
			case 'Z', 'z':
				it.ctrl2 = it.initPoint
				if it.pen != it.initPoint {
					it.pen = it.initPoint
					return seg(LineTo, it.initPoint), true
				}
				continue
			default:
				it.err = fmt.Errorf("unknown <path> command %s in %q", string(c), orig)
				return Segment{}, false
			}
		}
		if it.op == 0 {
			it.err = fmt.Errorf("coordinates before command in <path> data: %q", it.d)
			return Segment{}, false
		}
		op := it.op
		if it.implicit {
			// Pairs following a moveto pair are implicit linetos.
			switch op {
			case 'M':
				op = 'L'
			case 'm':
				op = 'l'
			}
		}
		var coords [6]int
		n := numCoords(op)
		for i := 0; i < n; i++ {
			it.d = strings.TrimLeft(it.d, " ,\t\n")
			l, x, ok := parseFloat(it.d)
			if !ok {
				it.err = fmt.Errorf("odd number of coordinates in <path> data: %q", it.d)
				return Segment{}, false
			}
			it.d = it.d[l:]
			coords[i] = it.scale(x)
		}
		it.implicit = true
		rel := 'a' <= op && op <= 'z'
		var off bezier.Point
		if rel {
			off = it.pen
		} else {
			off = bezier.Pt(it.offx, it.offy)
		}
		var points [3]bezier.Point
		for i := 0; i < n/2; i++ {
			points[i] = bezier.Pt(coords[i*2], coords[i*2+1]).Add(off)
		}
		switch lower(op) {
		case 'h':
			p := bezier.Pt(coords[0], it.pen.Y)
			if rel {
				p.X += it.pen.X
			} else {
				p.X += it.offx
			}
			it.pen = p
			it.ctrl2 = p
			return seg(LineTo, p), true
		case 'v':
			p := bezier.Pt(it.pen.X, coords[0])
			if rel {
				p.Y += it.pen.Y
			} else {
				p.Y += it.offy
			}
			it.pen = p
			it.ctrl2 = p
			return seg(LineTo, p), true
		case 'm':
			it.pen = points[0]
			it.initPoint = points[0]
			it.ctrl2 = points[0]
			return seg(MoveTo, points[0]), true
		case 'l':
			it.pen = points[0]
			it.ctrl2 = points[0]
			return seg(LineTo, points[0]), true
		case 'c':
			it.pen = points[2]
			it.ctrl2 = points[1]
			return seg(CubeTo, points[0], points[1], points[2]), true
		case 's':
			// Compute p1 by reflecting p2 on to the line that contains pen and p2.
			p1 := it.pen.Mul(2).Sub(it.ctrl2)
			p2, p3 := points[0], points[1]
			it.pen = p3
			it.ctrl2 = p2
			return seg(CubeTo, p1, p2, p3), true
		case 'q':
			it.pen = points[1]
			it.ctrl2 = points[1]
			return seg(QuadTo, points[0], points[1]), true
		}
	}
	return Segment{}, false
}

func numCoords(op rune) int {
	switch lower(op) {
	case 'h', 'v':
		return 1
	case 'm', 'l':
		return 2
	case 's', 'q':
		return 4
	case 'c':
		return 6
	}
	return 0
}

func lower(op rune) rune {
	return op | 0x20
}

func isNumberStart(c byte) bool {
	return '0' <= c && c <= '9' || c == '-' || c == '.'
}

func seg(op SegmentOp, args ...bezier.Point) Segment {
	s := Segment{Op: op}
	copy(s.Args[:], args)
	return s
}

func parseFloat(s string) (int, float64, bool) {
	n := 0
	if len(s) > 0 && s[0] == '-' {
		n++
	}
	for ; n < len(s); n++ {
		if !('0' <= s[n] && s[n] <= '9' || s[n] == '.') {
			break
		}
	}
	f, err := strconv.ParseFloat(s[:n], 64)
	return n, f, err == nil
}

// Optimize drops empty segments, merges colinear runs, expands
// quadratic segments to cubic and converts degenerate cubic
// segments to lines.
func Optimize(segs []Segment) []Segment {
	var opt []Segment
	var p0, pMinusOne bezier.Point
skip:
	for _, s := range segs {
		var pNext bezier.Point
		for {
			switch s.Op {
			case MoveTo, LineTo:
				p1 := s.Args[0]
				if p1 == p0 {
					continue skip
				}
				pNext = p1
				// Merge colinear segments of the same type.
				if len(opt) > 0 {
					if prevSeg := opt[len(opt)-1]; prevSeg.Op == s.Op {
						if onSegment(p0, pMinusOne, p1) {
							opt[len(opt)-1].Args[0] = p1
							continue skip
						}
					}
				}
			case QuadTo:
				s = quadToCube(p0, s)
				continue
			case CubeTo:
				p1 := s.Args[0]
				p2 := s.Args[1]
				p3 := s.Args[2]
				// Check whether the segment degenerates into
				// a line, which is equivalent to checking whether
				// the two inner control points lie on the line segment
				// of the endpoints.
				if onSegment(p1, p0, p3) && onSegment(p2, p0, p3) {
					s.Op = LineTo
					s.Args[0] = p3
					continue
				}
				pNext = p3
			}
			break
		}
		opt = append(opt, s)
		p0, pMinusOne = pNext, p0
	}
	return opt
}

// quadToCube expands a quadratic segment starting at p0 to a cubic.
func quadToCube(p0 bezier.Point, s Segment) Segment {
	p12 := s.Args[0]
	p3 := s.Args[1]
	p1 := mix(p12, p0, 1.0/3.0)
	p2 := mix(p12, p3, 1.0/3.0)
	s.Op = CubeTo
	copy(s.Args[:], []bezier.Point{
		p1, p2, p3,
	})
	return s
}

// onSegment checks if point p lies on the segment between a and b.
func onSegment(p, a, b bezier.Point) bool {
	// Check collinearity using the cross product.
	if cross := (p.Y-a.Y)*(b.X-a.X) - (p.X-a.X)*(b.Y-a.Y); cross != 0 {
		return false
	}

	// p must also lie in the bounding box with a and b as corners.
	return p.X >= min(a.X, b.X) && p.X <= max(a.X, b.X) &&
		p.Y >= min(a.Y, b.Y) && p.Y <= max(a.Y, b.Y)
}

func mix(p1, p2 bezier.Point, a float64) bezier.Point {
	return bezier.Point{
		X: int(math.Round(float64(p1.X)*(1.-a) + float64(p2.X)*a)),
		Y: int(math.Round(float64(p1.Y)*(1.-a) + float64(p2.Y)*a)),
	}
}

// A Fitter fits a run of at least 2 sample points to the control
// points of a clamped uniform B-spline. The returned control points
// begin and end with their clamps: 3 repetitions of the first and
// last sample.
type Fitter func(samples []bezier.Point) ([]bezier.Point, error)

// ControlFit is a Fitter that uses the samples directly as control
// points. The resulting spline stays within the convex hull of the
// samples, approximating the sampled curve without the cost of
// [bspline.InterpolatePoints].
func ControlFit() Fitter {
	var spline []bezier.Point
	return func(samples []bezier.Point) ([]bezier.Point, error) {
		s, e := samples[0], samples[len(samples)-1]
		spline = spline[:0]
		spline = append(spline, s, s, s)
		spline = append(spline, samples[1:len(samples)-1]...)
		spline = append(spline, e, e, e)
		return spline, nil
	}
}

// InterpolateFit is a Fitter whose clamped uniform cubic B-spline
// passes through the sample points. It solves the [1 4 1]
// tridiagonal system for the interior control points, so the spline
// value at each interior knot, (d[i-1]+4·d[i]+d[i+1])/6, equals the
// corresponding sample; the clamped ends fix d[0] and d[n].
//
// Being a fixed linear solve it is symmetric: a symmetric run of
// samples yields a symmetric spline. That is the difference from
// [bspline.InterpolatePoints], which minimises a kinematic cost and
// slides control points off their faithful positions asymmetrically,
// and from ControlFit, which stays inside the sample hull and so
// undershoots. It needs no solver dependency and runs in O(n) by the
// Thomas algorithm.
func InterpolateFit() Fitter {
	return func(samples []bezier.Point) ([]bezier.Point, error) {
		n := len(samples) - 1
		first, last := samples[0], samples[n]
		out := make([]bezier.Point, 0, n+5)
		out = append(out, first, first, first)
		if n < 3 {
			// Too few intervals to interpolate; use the samples.
			out = append(out, samples[1:n]...)
			return append(out, last, last, last), nil
		}
		// Thomas forward sweep over interior points d[1..n-1]. The
		// clamped endpoints move to the right-hand side of the first
		// and last equations.
		m := n - 1
		c := make([]float64, m)
		x := make([]float64, m)
		y := make([]float64, m)
		c[0] = 1.0 / 4
		x[0] = (6*float64(samples[1].X) - float64(first.X)) / 4
		y[0] = (6*float64(samples[1].Y) - float64(first.Y)) / 4
		for i := 1; i < m; i++ {
			rx := 6 * float64(samples[i+1].X)
			ry := 6 * float64(samples[i+1].Y)
			if i == m-1 {
				rx -= float64(last.X)
				ry -= float64(last.Y)
			}
			den := 4 - c[i-1]
			c[i] = 1 / den
			x[i] = (rx - x[i-1]) / den
			y[i] = (ry - y[i-1]) / den
		}
		// Back substitution in place, rounding to machine units.
		d := make([]bezier.Point, m)
		d[m-1] = bezier.Pt(iround(x[m-1]), iround(y[m-1]))
		for i := m - 2; i >= 0; i-- {
			x[i] -= c[i] * x[i+1]
			y[i] -= c[i] * y[i+1]
			d[i] = bezier.Pt(iround(x[i]), iround(y[i]))
		}
		out = append(out, d...)
		return append(out, last, last, last), nil
	}
}

func iround(v float64) int {
	return int(math.Round(v))
}

// Builder converts a stream of segments into uniform B-spline knots,
// sampling curves with spacing prec and fitting each run of samples
// with fit. Knots are passed to yield as they complete; a stroke is
// clamped by tripling its boundary knots. A zero-length line is
// treated as a clamp, splitting the stroke at that point.
//
// If splice is set, lines that are part of longer shapes are
// appended as (straight) curve segments.
type Builder struct {
	prec    int
	splice  bool
	fit     Fitter
	sample  func([]bezier.Point, bezier.Cubic, int) []bezier.Point
	yield   func(vector.Knot) bool
	maxRun  int
	tooLong error

	// onSamples reports every fitted sample run, for debugging.
	onSamples func([]bezier.Point)

	samples []bezier.Point
	pending vector.Knot
	hasPend bool
	prev    Segment
	hasPrev bool
	prevOp  SegmentOp
	p0      bezier.Point
	stopped bool
	err     error
}

func NewBuilder(prec int, splice bool, fit Fitter, yield func(vector.Knot) bool) *Builder {
	return &Builder{
		prec:   prec,
		splice: splice,
		fit:    fit,
		sample: bezier.Sample,
		yield:  yield,
		prevOp: MoveTo,
	}
}

// LimitRun caps the number of sample points a single stroke may
// accumulate before fitting, so a pathological unbroken stroke fails
// with err instead of growing the sample buffer without bound. A
// non-positive max disables the cap.
func (b *Builder) LimitRun(max int, err error) {
	b.maxRun = max
	b.tooLong = err
}

// Add processes the next segment. It reports whether the builder
// accepts more segments.
func (b *Builder) Add(s Segment) bool {
	if b.stopped || b.err != nil {
		return false
	}
	if b.hasPrev {
		b.process(b.prev, s.Op, true)
	}
	b.prev = s
	b.hasPrev = true
	return !b.stopped && b.err == nil
}

// Close processes the final segment and flushes the remaining knots.
func (b *Builder) Close() error {
	if b.hasPrev && !b.stopped && b.err == nil {
		b.process(b.prev, 0, false)
		b.hasPrev = false
	}
	if !b.stopped && b.err == nil {
		b.flush(true)
		if b.hasPend {
			b.emit(b.pending)
			b.hasPend = false
		}
	}
	return b.err
}

func (b *Builder) process(s Segment, next SegmentOp, hasNext bool) {
	switch s.Op {
	case MoveTo:
		p1 := s.Args[0]
		b.flush(true)
		b.pending.Line = false
		k := vector.Knot{Ctrl: p1}
		b.emit3(k)
		b.p0 = p1
	case LineTo:
		p1 := s.Args[0]
		if p1 == b.p0 {
			// A zero-length line clamps the stroke, forcing the
			// spline through the point: the flushed run already
			// ends with a clamp triple there.
			b.flush(true)
			b.pending.Line = true
			break
		}
		c := bezier.Cubic{
			C0: b.p0,
			C1: b.p0.Mul(2).Add(p1).Div(3),
			C2: p1.Mul(2).Add(b.p0).Div(3),
			C3: p1,
		}
		b.p0 = p1
		// If this line is part of a longer shape,
		// append it as a (straight) curve segment.
		if b.splice && (b.prevOp != MoveTo || hasNext && next != MoveTo) {
			b.appendBezier(c)
			break
		}
		b.flush(true)
		b.pending.Line = true
		b.emit3(vector.Knot{Ctrl: p1, Line: true})
	case QuadTo:
		s = quadToCube(b.p0, s)
		fallthrough
	case CubeTo:
		p1 := s.Args[0]
		p2 := s.Args[1]
		p3 := s.Args[2]
		c := bezier.Cubic{
			C0: b.p0, C1: p1, C2: p2, C3: p3,
		}
		b.p0 = p3
		b.appendBezier(c)
	default:
		panic("unknown segment type")
	}
	b.prevOp = s.Op
}

func (b *Builder) appendBezier(c bezier.Cubic) {
	if len(b.samples) == 0 {
		b.samples = append(b.samples, c.C0)
	}
	b.samples = b.sample(b.samples, c, b.prec)
	if b.maxRun > 0 && len(b.samples) > b.maxRun {
		b.err = b.tooLong
	}
}

func (b *Builder) flush(line bool) {
	if b.err != nil || b.stopped {
		return
	}
	if len(b.samples) < 2 {
		// A run that sampled to a single point is a curve smaller
		// than the sampling resolution: it carries no geometry, and
		// its start point was already emitted as a clamp. Drop it
		// rather than hand a degenerate run to the fitter.
		b.samples = b.samples[:0]
		return
	}
	for i, s := range b.samples[:len(b.samples)-1] {
		s2 := b.samples[i+1]
		if s == s2 {
			b.err = fmt.Errorf("overlapping sampling point %v", s)
			return
		}
	}
	if b.onSamples != nil {
		b.onSamples(b.samples)
	}
	uspline, err := b.fit(b.samples)
	b.samples = b.samples[:0]
	if err != nil {
		b.err = err
		return
	}
	for _, k := range uspline[3:] {
		b.emit(vector.Knot{
			Ctrl: k,
			Line: line,
		})
	}
}

func (b *Builder) emit(k vector.Knot) {
	if b.stopped {
		return
	}
	if b.hasPend {
		if !b.yield(b.pending) {
			b.stopped = true
			b.hasPend = false
			return
		}
	}
	b.pending = k
	b.hasPend = true
}

func (b *Builder) emit3(k vector.Knot) {
	b.emit(k)
	b.emit(k)
	b.emit(k)
}

// ToBSpline converts segments to a uniform B-spline, sampling curves
// with spacing prec. If splice is set, lines that are part of longer
// shapes are appended as (straight) curve segments.
func ToBSpline(segs []Segment, prec int, splice bool) (allSamples []bezier.Point, spline []vector.Knot, err error) {
	b := NewBuilder(prec, splice, InterpolateFit(), func(k vector.Knot) bool {
		spline = append(spline, k)
		return true
	})
	// Font generation runs off-device, so it samples with the symmetric
	// arc-length sampler: a symmetric outline then yields a symmetric
	// spline, which the integer Sample's one-directional walk would
	// break. The device curves path keeps the cheaper Sample.
	b.sample = bezier.SampleSym
	b.onSamples = func(s []bezier.Point) {
		allSamples = append(allSamples, s...)
	}
	for _, s := range segs {
		if !b.Add(s) {
			break
		}
	}
	err = b.Close()
	return allSamples, spline, err
}
