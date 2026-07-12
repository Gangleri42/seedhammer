// Package svgpath converts SVG path data into segments and further
// into the uniform B-spline representation used for engraving.
package svgpath

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"

	"seedhammer.com/bezier"
	"seedhammer.com/bspline"
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
	encode := func(op SegmentOp, args ...bezier.Point) {
		seg := Segment{Op: op}
		if len(args) > len(seg.Args) {
			panic("too many arguments")
		}
		copy(seg.Args[:], args)
		segs = append(segs, seg)
	}
	cmds := strings.TrimSpace(d)
	pen := bezier.Pt(offx, offy)
	initPoint := pen
	ctrl2 := pen
	for {
		cmds = strings.TrimLeft(cmds, " ,\t\n")
		if len(cmds) == 0 {
			break
		}
		orig := cmds
		op := rune(cmds[0])
		cmds = cmds[1:]
		switch op {
		case 'M', 'm', 'V', 'v', 'L', 'l', 'H', 'h', 'C', 'c', 'S', 's':
		case 'Z', 'z':
			if pen != initPoint {
				encode(LineTo, initPoint)
				pen = initPoint
			}
			ctrl2 = initPoint
			continue
		default:
			return segs, fmt.Errorf("unknown <path> command %s in %q", string(op), orig)
		}
		var coords []int
		for {
			cmds = strings.TrimLeft(cmds, " ,\t\n")
			if len(cmds) == 0 {
				break
			}
			n, x, ok := parseFloat(cmds)
			if !ok {
				break
			}
			cmds = cmds[n:]
			coords = append(coords, scale(x))
		}
		rel := unicode.IsLower(op)
		newPen := pen
		switch unicode.ToLower(op) {
		case 'h':
			for _, x := range coords {
				p := bezier.Pt(x, pen.Y)
				if rel {
					p.X += pen.X
				} else {
					p.X += offx
				}
				encode(LineTo, p)
				newPen = p
			}
			pen = newPen
			ctrl2 = newPen
			continue
		case 'v':
			for _, y := range coords {
				p := bezier.Pt(pen.X, y)
				if rel {
					p.Y += pen.Y
				} else {
					p.Y += offy
				}
				encode(LineTo, p)
				newPen = p
			}
			pen = newPen
			ctrl2 = newPen
			continue
		}
		if len(coords)%2 != 0 {
			return segs, fmt.Errorf("odd number of coordinates in <path> data: %q", orig)
		}
		var off bezier.Point
		if rel {
			// Relative command.
			off = pen
		} else {
			off = bezier.Pt(offx, offy)
		}
		var points []bezier.Point
		for i := 0; i < len(coords); i += 2 {
			p := bezier.Pt(coords[i], coords[i+1])
			p = p.Add(off)
			points = append(points, p)
		}
		newCtrl2 := ctrl2
		switch op := unicode.ToLower(op); op {
		case 'm', 'l':
			sop := MoveTo
			if op == 'l' {
				sop = LineTo
			}
			for _, p := range points {
				encode(sop, p)
				newPen = p
			}
			if op == 'm' {
				initPoint = newPen
			}
		case 'c':
			for i := 0; i < len(points); i += 3 {
				p1, p2, p3 := points[i], points[i+1], points[i+2]
				encode(CubeTo, p1, p2, p3)
				newPen = p3
				newCtrl2 = p2
			}
		case 's':
			for i := 0; i < len(points); i += 2 {
				p2, p3 := points[i], points[i+1]
				// Compute p1 by reflecting p2 on to the line that contains pen and p2.
				p1 := pen.Mul(2).Sub(ctrl2)
				encode(CubeTo, p1, p2, p3)
				newPen = p3
				newCtrl2 = p2
			}
		}
		pen = newPen
		ctrl2 = newCtrl2
	}
	return segs, nil
}

func parseFloat(s string) (int, float64, bool) {
	n := 0
	if len(s) > 0 && s[0] == '-' {
		n++
	}
	for ; n < len(s); n++ {
		if !(unicode.IsDigit(rune(s[n])) || s[n] == '.') {
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
				p12 := s.Args[0]
				p3 := s.Args[1]
				// Expand to cubic.
				p1 := mix(p12, p0, 1.0/3.0)
				p2 := mix(p12, p3, 1.0/3.0)
				s.Op = CubeTo
				copy(s.Args[:], []bezier.Point{
					p1, p2, p3,
				})
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

// ToBSpline converts segments to a uniform B-spline, sampling curves
// with spacing prec. If splice is set, lines that are part of longer
// shapes are appended as (straight) curve segments.
func ToBSpline(segs []Segment, prec int, splice bool) (allSamples []bezier.Point, spline []vector.Knot, err error) {
	var samples []bezier.Point
	var interpolateErr error
	flushSamples := func(line bool) {
		if interpolateErr != nil || len(samples) == 0 {
			return
		}
		for i, s := range samples[:len(samples)-1] {
			s2 := samples[i+1]
			if s == s2 {
				interpolateErr = fmt.Errorf("overlapping sampling point %v", s)
				return
			}
		}

		uspline, err := bspline.InterpolatePoints(samples)
		allSamples = append(allSamples, samples...)
		samples = samples[:0]
		if err != nil {
			interpolateErr = err
			return
		}
		for _, k := range uspline[3:] {
			spline = append(spline, vector.Knot{
				Ctrl: k,
				Line: line,
			})
		}
	}
	appendBezier := func(c bezier.Cubic) {
		if len(samples) == 0 {
			samples = append(samples, c.C0)
		}
		samples = bezier.Sample(samples, c, prec)
	}
	p0 := bezier.Point{}
	for i, s := range segs {
		switch s.Op {
		case MoveTo:
			flushSamples(true)
			p1 := s.Args[0]
			if n := len(spline); n > 0 {
				spline[n-1].Line = false
			}
			k := vector.Knot{Ctrl: p1}
			spline = append(spline, k, k, k)
			p0 = p1
		case LineTo:
			p1 := s.Args[0]
			c := bezier.Cubic{
				C0: p0,
				C1: p0.Mul(2).Add(p1).Div(3),
				C2: p1.Mul(2).Add(p0).Div(3),
				C3: p1,
			}
			p0 = p1
			// If this line is part of a longer shape,
			// append it as a (straight) curve segment.
			if splice && (i >= 0 && segs[i-1].Op != MoveTo ||
				i < len(segs)-1 && segs[i+1].Op != MoveTo) {
				appendBezier(c)
				break
			}
			flushSamples(true)
			if n := len(spline); n > 0 {
				spline[n-1].Line = true
			}
			k := vector.Knot{Ctrl: p1, Line: true}
			spline = append(spline, k, k, k)
		case CubeTo:
			p1 := s.Args[0]
			p2 := s.Args[1]
			p3 := s.Args[2]
			c := bezier.Cubic{
				C0: p0, C1: p1, C2: p2, C3: p3,
			}
			p0 = p3
			appendBezier(c)
		default:
			panic("unknown segment type")
		}
	}
	flushSamples(true)
	return allSamples, spline, interpolateErr
}
