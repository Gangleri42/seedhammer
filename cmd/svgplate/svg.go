package main

import (
	"encoding/xml"
	"fmt"
	"math"
	"strconv"
	"strings"

	"seedhammer.com/svgpath"
)

// extractSVG walks an SVG document and returns every visible shape as
// path segments in the document's user coordinate space, with element
// and ancestor transforms flattened in. Layout onto the plate is the
// caller's job. Invisible subtrees (display:none, visibility:hidden)
// and <defs> are skipped.
func extractSVG(data []byte) ([]fseg, error) {
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	type frame struct {
		m    matrix
		skip bool
	}
	stack := []frame{{m: identity()}}
	var out []fseg
	for {
		tok, err := dec.Token()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return nil, fmt.Errorf("svg: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			top := stack[len(stack)-1]
			local, _ := parseTransform(attr(t, "transform"))
			f := frame{m: top.m.mul(local), skip: top.skip || invisible(t) || t.Name.Local == "defs"}
			stack = append(stack, f)
			if f.skip {
				continue
			}
			segs, err := shapeSegments(t)
			if err != nil {
				return nil, err
			}
			for _, s := range segs {
				out = append(out, s.transform(f.m))
			}
		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("svg: no visible geometry found")
	}
	return out, nil
}

func attr(e xml.StartElement, name string) string {
	for _, a := range e.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// invisible reports whether an element hides itself and its subtree.
func invisible(e xml.StartElement) bool {
	if v := attr(e, "display"); v == "none" {
		return true
	}
	if v := attr(e, "visibility"); v == "hidden" || v == "collapse" {
		return true
	}
	style := attr(e, "style")
	return strings.Contains(style, "display:none") ||
		strings.Contains(style, "visibility:hidden")
}

// shapeSegments returns the outline of a single shape element in its
// own user units, or nil for a non-shape (group, metadata, ...).
func shapeSegments(e xml.StartElement) ([]fseg, error) {
	switch e.Name.Local {
	case "path":
		return parsePath(attr(e, "d"))
	case "rect":
		x, y := num(attr(e, "x")), num(attr(e, "y"))
		w, h := num(attr(e, "width")), num(attr(e, "height"))
		if w <= 0 || h <= 0 {
			return nil, nil
		}
		return polygon([]fpt{{x, y}, {x + w, y}, {x + w, y + h}, {x, y + h}}, true), nil
	case "line":
		return []fseg{
			{op: svgpath.MoveTo, p: [3]fpt{{num(attr(e, "x1")), num(attr(e, "y1"))}}},
			{op: svgpath.LineTo, p: [3]fpt{{num(attr(e, "x2")), num(attr(e, "y2"))}}},
		}, nil
	case "polyline":
		return polyPoints(attr(e, "points"), false)
	case "polygon":
		return polyPoints(attr(e, "points"), true)
	case "circle":
		return ellipse(num(attr(e, "cx")), num(attr(e, "cy")), num(attr(e, "r")), num(attr(e, "r"))), nil
	case "ellipse":
		return ellipse(num(attr(e, "cx")), num(attr(e, "cy")), num(attr(e, "rx")), num(attr(e, "ry"))), nil
	}
	return nil, nil
}

// polygon builds a closed (or open) run of line segments through pts.
func polygon(pts []fpt, closed bool) []fseg {
	if len(pts) == 0 {
		return nil
	}
	segs := []fseg{{op: svgpath.MoveTo, p: [3]fpt{pts[0]}}}
	for _, p := range pts[1:] {
		segs = append(segs, fseg{op: svgpath.LineTo, p: [3]fpt{p}})
	}
	if closed {
		segs = append(segs, fseg{op: svgpath.LineTo, p: [3]fpt{pts[0]}})
	}
	return segs
}

func polyPoints(s string, closed bool) ([]fseg, error) {
	vals := floats(s)
	if len(vals)%2 != 0 || len(vals) < 4 {
		return nil, fmt.Errorf("svg: bad points %q", s)
	}
	var pts []fpt
	for i := 0; i+1 < len(vals); i += 2 {
		pts = append(pts, fpt{vals[i], vals[i+1]})
	}
	return polygon(pts, closed), nil
}

// ellipse approximates an axis-aligned ellipse with four cubic beziers.
func ellipse(cx, cy, rx, ry float64) []fseg {
	if rx <= 0 || ry <= 0 {
		return nil
	}
	const k = 0.5522847498307936 // 4/3*(sqrt(2)-1)
	ox, oy := rx*k, ry*k
	return []fseg{
		{op: svgpath.MoveTo, p: [3]fpt{{cx + rx, cy}}},
		{op: svgpath.CubeTo, p: [3]fpt{{cx + rx, cy + oy}, {cx + ox, cy + ry}, {cx, cy + ry}}},
		{op: svgpath.CubeTo, p: [3]fpt{{cx - ox, cy + ry}, {cx - rx, cy + oy}, {cx - rx, cy}}},
		{op: svgpath.CubeTo, p: [3]fpt{{cx - rx, cy - oy}, {cx - ox, cy - ry}, {cx, cy - ry}}},
		{op: svgpath.CubeTo, p: [3]fpt{{cx + ox, cy - ry}, {cx + rx, cy - oy}, {cx + rx, cy}}},
	}
}

// parseTransform parses an SVG transform attribute into a single
// affine matrix. An empty or unparseable attribute yields identity.
func parseTransform(s string) (matrix, error) {
	m := identity()
	s = strings.TrimSpace(s)
	for s != "" {
		open := strings.IndexByte(s, '(')
		if open < 0 {
			break
		}
		name := strings.TrimSpace(s[:open])
		close := strings.IndexByte(s, ')')
		if close < open {
			// A ')' before or without its '(' is malformed; slicing
			// s[open+1:close] would panic.
			return m, fmt.Errorf("svg: malformed transform %q", s)
		}
		args := floats(s[open+1 : close])
		s = strings.TrimLeft(s[close+1:], " ,\t\n")
		var t matrix
		switch name {
		case "translate":
			tx := arg(args, 0, 0)
			t = translateM(tx, arg(args, 1, 0))
		case "scale":
			sx := arg(args, 0, 1)
			t = scaleM(sx, arg(args, 1, sx))
		case "rotate":
			if len(args) >= 3 {
				t = translateM(args[1], args[2]).mul(rotateM(args[0])).mul(translateM(-args[1], -args[2]))
			} else {
				t = rotateM(arg(args, 0, 0))
			}
		case "matrix":
			if len(args) != 6 {
				return m, fmt.Errorf("svg: matrix needs 6 args, got %d", len(args))
			}
			t = matrix{args[0], args[1], args[2], args[3], args[4], args[5]}
		case "skewX":
			t = skewXM(arg(args, 0, 0))
		case "skewY":
			t = skewYM(arg(args, 0, 0))
		default:
			return m, fmt.Errorf("svg: unknown transform %q", name)
		}
		m = m.mul(t)
	}
	return m, nil
}

func arg(a []float64, i int, def float64) float64 {
	if i < len(a) {
		return a[i]
	}
	return def
}

// num parses a lone float, returning 0 on failure (missing attribute)
// or a non-finite value. strconv.ParseFloat accepts "NaN" and "Inf",
// which would otherwise poison the geometry.
func num(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return f
}

// floats scans every number out of a whitespace/comma separated list.
func floats(s string) []float64 {
	sc := scanner{s: s}
	var out []float64
	for {
		v, ok := sc.number()
		if !ok {
			return out
		}
		out = append(out, v)
	}
}

// scanner reads SVG numbers and arc flags out of path data, coping
// with the compact forms where a sign or decimal point separates two
// numbers with no whitespace.
type scanner struct {
	s string
	i int
}

func (sc *scanner) sep() {
	for sc.i < len(sc.s) {
		switch sc.s[sc.i] {
		case ' ', ',', '\t', '\n', '\r':
			sc.i++
		default:
			return
		}
	}
}

func (sc *scanner) number() (float64, bool) {
	sc.sep()
	start := sc.i
	dot, exp, digit := false, false, false
	if sc.i < len(sc.s) && (sc.s[sc.i] == '+' || sc.s[sc.i] == '-') {
		sc.i++
	}
loop:
	for sc.i < len(sc.s) {
		c := sc.s[sc.i]
		switch {
		case c >= '0' && c <= '9':
			digit = true
			sc.i++
		case c == '.' && !dot && !exp:
			dot = true
			sc.i++
		case (c == 'e' || c == 'E') && !exp && digit:
			exp = true
			sc.i++
			if sc.i < len(sc.s) && (sc.s[sc.i] == '+' || sc.s[sc.i] == '-') {
				sc.i++
			}
		default:
			break loop
		}
	}
	if !digit {
		return 0, false
	}
	f, err := strconv.ParseFloat(sc.s[start:sc.i], 64)
	return f, err == nil
}

// flag reads a single 0/1 arc flag, which may abut the next number.
func (sc *scanner) flag() (int, bool) {
	sc.sep()
	if sc.i < len(sc.s) && (sc.s[sc.i] == '0' || sc.s[sc.i] == '1') {
		v := int(sc.s[sc.i] - '0')
		sc.i++
		return v, true
	}
	return 0, false
}

func (sc *scanner) command() (byte, bool) {
	sc.sep()
	if sc.i >= len(sc.s) {
		return 0, false
	}
	c := sc.s[sc.i]
	if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
		sc.i++
		return c, true
	}
	return 0, false
}

func upper(c byte) byte {
	if c >= 'a' && c <= 'z' {
		return c - 32
	}
	return c
}

// parsePath parses SVG path data into absolute float segments,
// supporting M L H V C S Q T A Z and their relative forms. Arcs are
// converted to cubic beziers here so the payload keeps the firmware's
// M/L/C/Q subset.
func parsePath(d string) ([]fseg, error) {
	sc := scanner{s: d}
	var segs []fseg
	var pen, start, ctrl fpt
	var cmd, prevCmd byte
	readPt := func(rel bool) (fpt, bool) {
		x, ok := sc.number()
		if !ok {
			return fpt{}, false
		}
		y, ok := sc.number()
		if !ok {
			return fpt{}, false
		}
		p := fpt{x, y}
		if rel {
			p.X += pen.X
			p.Y += pen.Y
		}
		return p, true
	}
	for {
		sc.sep()
		if sc.i >= len(d) {
			break
		}
		if c := d[sc.i]; (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
			cmd = c
			sc.i++
		} else if cmd == 0 {
			return nil, fmt.Errorf("svg: number before command in %q", d)
		} else if cmd == 'M' {
			cmd = 'L'
		} else if cmd == 'm' {
			cmd = 'l'
		}
		rel := cmd >= 'a' && cmd <= 'z'
		switch upper(cmd) {
		case 'M':
			p, ok := readPt(rel)
			if !ok {
				return nil, fmt.Errorf("svg: bad moveto in %q", d)
			}
			pen, start = p, p
			segs = append(segs, fseg{op: svgpath.MoveTo, p: [3]fpt{p}})
		case 'L':
			p, ok := readPt(rel)
			if !ok {
				return nil, fmt.Errorf("svg: bad lineto in %q", d)
			}
			pen = p
			segs = append(segs, fseg{op: svgpath.LineTo, p: [3]fpt{p}})
		case 'H':
			x, ok := sc.number()
			if !ok {
				return nil, fmt.Errorf("svg: bad H in %q", d)
			}
			if rel {
				x += pen.X
			}
			pen.X = x
			segs = append(segs, fseg{op: svgpath.LineTo, p: [3]fpt{pen}})
		case 'V':
			y, ok := sc.number()
			if !ok {
				return nil, fmt.Errorf("svg: bad V in %q", d)
			}
			if rel {
				y += pen.Y
			}
			pen.Y = y
			segs = append(segs, fseg{op: svgpath.LineTo, p: [3]fpt{pen}})
		case 'C':
			p1, ok1 := readPt(rel)
			p2, ok2 := readPt(rel)
			p3, ok3 := readPt(rel)
			if !ok1 || !ok2 || !ok3 {
				return nil, fmt.Errorf("svg: bad curveto in %q", d)
			}
			pen, ctrl = p3, p2
			segs = append(segs, fseg{op: svgpath.CubeTo, p: [3]fpt{p1, p2, p3}})
		case 'S':
			p2, ok2 := readPt(rel)
			p3, ok3 := readPt(rel)
			if !ok2 || !ok3 {
				return nil, fmt.Errorf("svg: bad smooth curveto in %q", d)
			}
			p1 := pen
			if pc := upper(prevCmd); pc == 'C' || pc == 'S' {
				p1 = fpt{2*pen.X - ctrl.X, 2*pen.Y - ctrl.Y}
			}
			pen, ctrl = p3, p2
			segs = append(segs, fseg{op: svgpath.CubeTo, p: [3]fpt{p1, p2, p3}})
		case 'Q':
			p1, ok1 := readPt(rel)
			p2, ok2 := readPt(rel)
			if !ok1 || !ok2 {
				return nil, fmt.Errorf("svg: bad quadto in %q", d)
			}
			pen, ctrl = p2, p1
			segs = append(segs, fseg{op: svgpath.QuadTo, p: [3]fpt{p1, p2}})
		case 'T':
			p2, ok := readPt(rel)
			if !ok {
				return nil, fmt.Errorf("svg: bad smooth quadto in %q", d)
			}
			p1 := pen
			if pc := upper(prevCmd); pc == 'Q' || pc == 'T' {
				p1 = fpt{2*pen.X - ctrl.X, 2*pen.Y - ctrl.Y}
			}
			pen, ctrl = p2, p1
			segs = append(segs, fseg{op: svgpath.QuadTo, p: [3]fpt{p1, p2}})
		case 'A':
			rx, ok1 := sc.number()
			ry, ok2 := sc.number()
			rot, ok3 := sc.number()
			large, ok4 := sc.flag()
			sweep, ok5 := sc.flag()
			p, ok6 := readPt(rel)
			if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || !ok6 {
				return nil, fmt.Errorf("svg: bad arc in %q", d)
			}
			segs = append(segs, arcToCubics(pen, rx, ry, rot, large == 1, sweep == 1, p)...)
			pen, ctrl = p, p
		case 'Z':
			if pen != start {
				segs = append(segs, fseg{op: svgpath.LineTo, p: [3]fpt{start}})
			}
			pen, ctrl = start, start
			// Z takes no coordinates and has no implicit repeat, so a
			// following number is an error, not another Z. Clearing cmd
			// makes the next number fail the "number before command"
			// check instead of re-entering this no-op case forever.
			cmd = 0
		default:
			return nil, fmt.Errorf("svg: unknown path command %q", string(cmd))
		}
		prevCmd = cmd
	}
	return segs, nil
}

// arcToCubics converts an SVG elliptical arc from p0 to p1 into a run
// of cubic bezier segments, following the endpoint-to-center
// conversion of SVG appendix F.6.
func arcToCubics(p0 fpt, rx, ry, phiDeg float64, large, sweep bool, p1 fpt) []fseg {
	if p0 == p1 {
		return nil
	}
	if rx == 0 || ry == 0 {
		return []fseg{{op: svgpath.LineTo, p: [3]fpt{p1}}}
	}
	rx, ry = math.Abs(rx), math.Abs(ry)
	phi := phiDeg * math.Pi / 180
	cosP, sinP := math.Cos(phi), math.Sin(phi)
	dx, dy := (p0.X-p1.X)/2, (p0.Y-p1.Y)/2
	x1p := cosP*dx + sinP*dy
	y1p := -sinP*dx + cosP*dy
	// Scale radii up if they are too small to span the endpoints.
	if l := x1p*x1p/(rx*rx) + y1p*y1p/(ry*ry); l > 1 {
		s := math.Sqrt(l)
		rx, ry = rx*s, ry*s
	}
	den := rx*rx*y1p*y1p + ry*ry*x1p*x1p
	num := rx*rx*ry*ry - den
	co := 0.0
	if num > 0 {
		co = math.Sqrt(num / den)
	}
	if large == sweep {
		co = -co
	}
	cxp := co * rx * y1p / ry
	cyp := -co * ry * x1p / rx
	cx := cosP*cxp - sinP*cyp + (p0.X+p1.X)/2
	cy := sinP*cxp + cosP*cyp + (p0.Y+p1.Y)/2
	ang := func(ux, uy, vx, vy float64) float64 {
		dot := ux*vx + uy*vy
		l := math.Hypot(ux, uy) * math.Hypot(vx, vy)
		a := math.Acos(math.Max(-1, math.Min(1, dot/l)))
		if ux*vy-uy*vx < 0 {
			return -a
		}
		return a
	}
	theta1 := ang(1, 0, (x1p-cxp)/rx, (y1p-cyp)/ry)
	dtheta := ang((x1p-cxp)/rx, (y1p-cyp)/ry, (-x1p-cxp)/rx, (-y1p-cyp)/ry)
	if !sweep && dtheta > 0 {
		dtheta -= 2 * math.Pi
	} else if sweep && dtheta < 0 {
		dtheta += 2 * math.Pi
	}
	n := int(math.Ceil(math.Abs(dtheta) / (math.Pi / 2)))
	if n == 0 {
		n = 1
	}
	delta := dtheta / float64(n)
	t := 4.0 / 3 * math.Tan(delta/4)
	var segs []fseg
	th := theta1
	for i := 0; i < n; i++ {
		th2 := th + delta
		cos1, sin1 := math.Cos(th), math.Sin(th)
		cos2, sin2 := math.Cos(th2), math.Sin(th2)
		e := func(c, s float64) fpt {
			return fpt{
				X: cx + cosP*rx*c - sinP*ry*s,
				Y: cy + sinP*rx*c + cosP*ry*s,
			}
		}
		p := e(cos1, sin1)
		q := e(cos2, sin2)
		c1 := fpt{
			X: p.X + cosP*rx*(-sin1)*t - sinP*ry*cos1*t,
			Y: p.Y + sinP*rx*(-sin1)*t + cosP*ry*cos1*t,
		}
		c2 := fpt{
			X: q.X - (cosP*rx*(-sin2)*t - sinP*ry*cos2*t),
			Y: q.Y - (sinP*rx*(-sin2)*t + cosP*ry*cos2*t),
		}
		segs = append(segs, fseg{op: svgpath.CubeTo, p: [3]fpt{c1, c2, q}})
		th = th2
	}
	return segs
}
