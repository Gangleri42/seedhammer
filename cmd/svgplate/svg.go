package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"seedhammer.com/svgpath"
)

// node is one element of a parsed SVG document. The tree is retained
// whole because <use> may reference an id the document only defines
// further down, which a single pass over the token stream cannot
// resolve.
type node struct {
	el       xml.StartElement
	children []*node
}

// nonRendering elements describe templates, not drawings: they are
// painted only where a <use> references them. Walking into one
// directly would engrave the template as well as every instance.
var nonRendering = map[string]bool{
	"defs":     true,
	"symbol":   true,
	"clipPath": true,
	"mask":     true,
	"marker":   true,
	"pattern":  true,
}

// maxUseDepth bounds <use> nesting, so a document whose references form
// a cycle fails with an error instead of expanding until it exhausts
// memory.
const maxUseDepth = 16

// shapeKey identifies an outline that two placements can share: the
// element it came from, under the same linear transform. Translation
// is excluded because that is precisely what a placement carries.
// Comparing the matrix exactly is right: two stamps of one definition
// arrive through the same arithmetic, and a pair that differs in the
// last bit only misses a dictionary entry, never draws wrongly.
type shapeKey struct {
	el  *node
	lin matrix
}

// extractSVG walks an SVG document and returns its visible geometry in
// millimetre-agnostic user coordinates, kept instanced: each outline
// appears once and every <use> that stamps it becomes a placement.
// Layout onto the plate is the caller's job. Invisible subtrees
// (display:none, visibility:hidden) and template subtrees are skipped.
func extractSVG(data []byte) (*drawing, error) {
	root, ids, err := parseSVG(data)
	if err != nil {
		return nil, err
	}
	b := &builder{d: &drawing{}, ids: ids, shapes: make(map[shapeKey]int)}
	for _, c := range root.children {
		if err := b.walk(c, identity(), 0, false); err != nil {
			return nil, err
		}
	}
	if len(b.d.groups) == 0 {
		return nil, fmt.Errorf("svg: no visible geometry found")
	}
	return b.d, nil
}

// builder accumulates a drawing while walking the document, reusing a
// shape slot whenever an outline it has already filed comes round
// again under the same linear transform.
type builder struct {
	d      *drawing
	ids    map[string]*node
	shapes map[shapeKey]int
}

// place files segs (in the frame lin puts them in) under key and stamps
// it at at.
//
// The outline is normalized to its own start point, with the
// difference moved into the placement. A <use> already separates shape
// from position, but most drawings repeat geometry by copying it, and
// a copied shape carries its position inside its own coordinates. Once
// normalized, those copies are byte-identical outlines and the
// dictionary collapses them too.
func (b *builder) place(key shapeKey, segs []fseg, at fpt) {
	if len(segs) == 0 {
		return
	}
	// Every instance under one key is built by the same arithmetic, so
	// they share an origin and the shape filed for the first is right
	// for the rest.
	origin := segs[0].p[0]
	shape, ok := b.shapes[key]
	if !ok {
		local := make([]fseg, len(segs))
		for i, s := range segs {
			local[i] = fseg{op: s.op}
			for j := 0; j < s.npts(); j++ {
				local[i].p[j] = fpt{X: s.p[j].X - origin.X, Y: s.p[j].Y - origin.Y}
			}
		}
		shape = b.d.addShape(local)
		b.shapes[key] = shape
	}
	b.d.place(shape, fpt{X: at.X + origin.X, Y: at.Y + origin.Y})
}

// parseSVG reads the document into a tree under a synthetic root and
// indexes every id along the way. The first definition of a duplicate
// id wins, matching how a renderer resolves the reference.
func parseSVG(data []byte) (*node, map[string]*node, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	root := &node{}
	stack := []*node{root}
	ids := make(map[string]*node)
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("svg: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &node{el: t.Copy()}
			top := stack[len(stack)-1]
			top.children = append(top.children, n)
			stack = append(stack, n)
			if id := attr(t, "id"); id != "" {
				if _, dup := ids[id]; !dup {
					ids[id] = n
				}
			}
		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	return root, ids, nil
}

// walk records n's subtree under the accumulated transform m. Every
// shape element becomes its own placement, including inside a stamp,
// so travel ordering can still move each stroke independently. top
// lifts the template guard for an element a <use> named directly.
func (b *builder) walk(n *node, m matrix, depth int, top bool) error {
	if invisible(n.el) {
		return nil
	}
	// A transform that does not parse in full is an error, never a
	// prefix: applying the part that parsed would move the geometry
	// silently, which is what the strictness in parseTransform and
	// floats exists to prevent.
	local, err := parseTransform(attr(n.el, "transform"))
	if err != nil {
		return err
	}
	m = m.mul(local)
	if n.el.Name.Local == "use" {
		return b.expandUse(n, m, depth)
	}
	if !top && nonRendering[n.el.Name.Local] {
		return nil
	}
	segs, err := shapeSegments(n.el)
	if err != nil {
		return err
	}
	lin, at := m.split()
	b.place(shapeKey{el: n, lin: lin}, transformAll(segs, lin), at)
	for _, c := range n.children {
		if err := b.walk(c, m, depth, false); err != nil {
			return err
		}
	}
	return nil
}

// expandUse stamps the subtree a <use> references, offset by its x/y
// and drawn under its transform. Only the target's own transform
// applies: the clone is placed by the reference, not by wherever the
// definition happens to sit in the tree.
//
// Each shape inside the stamp becomes its own placement, keyed by the
// element it came from under the same linear transform. Two stamps of
// one definition therefore hit the same keys, so the outlines reach
// the payload once and repeat by reference, while travel ordering
// still sees the individual strokes. Making the whole stamp one
// placement would dedup marginally better and cost far more: a stamp
// whose strokes sit apart then has to be engraved as a unit, which
// measured 25-33% slower on drawings built to punish it.
//
// An unresolvable reference is an error. Ignoring it the way a renderer
// does would engrave a drawing quietly missing whatever the <use> was
// there to stamp, and the gauge report would still call it fine.
func (b *builder) expandUse(n *node, m matrix, depth int) error {
	if depth >= maxUseDepth {
		return fmt.Errorf("svg: <use> nested past %d levels, or referencing itself", maxUseDepth)
	}
	target, err := b.target(n)
	if err != nil {
		return err
	}
	off, err := nums(n.el, "x", "y")
	if err != nil {
		return err
	}
	m = m.mul(translateM(off[0], off[1]))
	return b.walk(target, m, depth+1, true)
}

// target resolves the element a <use> references.
func (b *builder) target(n *node) (*node, error) {
	// attr matches on the local name, so this finds both href and the
	// legacy xlink:href without resolving the namespace.
	ref := strings.TrimSpace(attr(n.el, "href"))
	id, ok := strings.CutPrefix(ref, "#")
	if !ok || id == "" {
		return nil, fmt.Errorf("svg: <use> href %q is not a reference to an id in this document", ref)
	}
	target := b.ids[id]
	if target == nil {
		return nil, fmt.Errorf("svg: <use> references unknown id %q", id)
	}
	return target, nil
}

func transformAll(segs []fseg, m matrix) []fseg {
	out := make([]fseg, len(segs))
	for i, s := range segs {
		out[i] = s.transform(m)
	}
	return out
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
		v, err := nums(e, "x", "y", "width", "height")
		if err != nil {
			return nil, err
		}
		x, y, w, h := v[0], v[1], v[2], v[3]
		if w <= 0 || h <= 0 {
			return nil, nil
		}
		rx, ry, err := rectRadii(attr(e, "rx"), attr(e, "ry"), w, h)
		if err != nil {
			return nil, err
		}
		if rx > 0 && ry > 0 {
			return roundRect(x, y, w, h, rx, ry), nil
		}
		return polygon([]fpt{{x, y}, {x + w, y}, {x + w, y + h}, {x, y + h}}, true), nil
	case "line":
		v, err := nums(e, "x1", "y1", "x2", "y2")
		if err != nil {
			return nil, err
		}
		return []fseg{
			{op: svgpath.MoveTo, p: [3]fpt{{v[0], v[1]}}},
			{op: svgpath.LineTo, p: [3]fpt{{v[2], v[3]}}},
		}, nil
	case "polyline":
		return polyPoints(attr(e, "points"), false)
	case "polygon":
		return polyPoints(attr(e, "points"), true)
	case "circle":
		v, err := nums(e, "cx", "cy", "r")
		if err != nil {
			return nil, err
		}
		return ellipse(v[0], v[1], v[2], v[2]), nil
	case "ellipse":
		v, err := nums(e, "cx", "cy", "rx", "ry")
		if err != nil {
			return nil, err
		}
		return ellipse(v[0], v[1], v[2], v[3]), nil
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
	vals, err := floats(s)
	if err != nil {
		return nil, err
	}
	if len(vals)%2 != 0 || len(vals) < 4 {
		return nil, fmt.Errorf("svg: bad points %q", s)
	}
	var pts []fpt
	for i := 0; i+1 < len(vals); i += 2 {
		pts = append(pts, fpt{vals[i], vals[i+1]})
	}
	return polygon(pts, closed), nil
}

// kappa is the cubic control offset, as a fraction of the radius, that
// approximates a quarter ellipse: 4/3*(sqrt(2)-1).
const kappa = 0.5522847498307936

// rectRadii resolves a rect's corner radii against its size. Either
// attribute alone sets both axes, and each clamps to half its side,
// per SVG's rect geometry rules. Absent, "auto" and negative all mean
// no radius on that axis.
func rectRadii(rxs, rys string, w, h float64) (float64, float64, error) {
	rx, hasX, err := radius(rxs)
	if err != nil {
		return 0, 0, err
	}
	ry, hasY, err := radius(rys)
	if err != nil {
		return 0, 0, err
	}
	switch {
	case !hasX && !hasY:
		return 0, 0, nil
	case !hasX:
		rx = ry
	case !hasY:
		ry = rx
	}
	return math.Min(rx, w/2), math.Min(ry, h/2), nil
}

func radius(s string) (float64, bool, error) {
	if s = strings.TrimSpace(s); s == "" || s == "auto" {
		return 0, false, nil
	}
	v, err := num(s)
	if err != nil {
		return 0, false, err
	}
	return v, v > 0, nil
}

// roundRect outlines a rect with elliptical corners, as four sides and
// four cubic quarter-arcs. It runs in polygon's direction so a rect
// keeps the same winding whether or not it has radii.
func roundRect(x, y, w, h, rx, ry float64) []fseg {
	ox, oy := rx*kappa, ry*kappa
	x1, y1 := x+w, y+h
	return []fseg{
		{op: svgpath.MoveTo, p: [3]fpt{{x + rx, y}}},
		{op: svgpath.LineTo, p: [3]fpt{{x1 - rx, y}}},
		{op: svgpath.CubeTo, p: [3]fpt{{x1 - rx + ox, y}, {x1, y + ry - oy}, {x1, y + ry}}},
		{op: svgpath.LineTo, p: [3]fpt{{x1, y1 - ry}}},
		{op: svgpath.CubeTo, p: [3]fpt{{x1, y1 - ry + oy}, {x1 - rx + ox, y1}, {x1 - rx, y1}}},
		{op: svgpath.LineTo, p: [3]fpt{{x + rx, y1}}},
		{op: svgpath.CubeTo, p: [3]fpt{{x + rx - ox, y1}, {x, y1 - ry + oy}, {x, y1 - ry}}},
		{op: svgpath.LineTo, p: [3]fpt{{x, y + ry}}},
		{op: svgpath.CubeTo, p: [3]fpt{{x, y + ry - oy}, {x + rx - ox, y}, {x + rx, y}}},
	}
}

// ellipse approximates an axis-aligned ellipse with four cubic beziers.
func ellipse(cx, cy, rx, ry float64) []fseg {
	if rx <= 0 || ry <= 0 {
		return nil
	}
	ox, oy := rx*kappa, ry*kappa
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
			// Trailing bytes that are not a function call. A browser
			// discards the whole attribute for this; keeping the
			// parsed prefix would silently move the geometry, so the
			// author gets an error instead.
			return m, fmt.Errorf("svg: trailing transform garbage %q", s)
		}
		name := strings.TrimSpace(s[:open])
		close := strings.IndexByte(s, ')')
		if close < open {
			// A ')' before or without its '(' is malformed; slicing
			// s[open+1:close] would panic.
			return m, fmt.Errorf("svg: malformed transform %q", s)
		}
		args, err := floats(s[open+1 : close])
		if err != nil {
			return m, err
		}
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

// num parses a lone float. An empty value (a missing attribute) is 0;
// anything else must parse fully, so a unit-suffixed length ("10mm",
// "10px") errors instead of silently becoming 0 and shrinking the
// shape to nothing. strconv.ParseFloat accepts "NaN" and "Inf", which
// would poison the geometry; they error too.
func num(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("svg: invalid length %q; unit suffixes and percentages are not supported, use unitless user units", s)
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("svg: non-finite length %q", s)
	}
	return f, nil
}

// nums parses the named attributes of e in order.
func nums(e xml.StartElement, names ...string) ([]float64, error) {
	out := make([]float64, len(names))
	for i, n := range names {
		v, err := num(attr(e, n))
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// floats scans every number out of a whitespace/comma separated list.
// The list has to be numbers to its end: a unit suffix ("10mm"), a
// stray token or anything else the scanner cannot read is an error,
// because stopping at the first bad token would keep the numbers
// before it and quietly reshape a transform or a polygon. A browser
// discards the whole attribute in that case; naming the token is
// more useful to the author than either.
func floats(s string) ([]float64, error) {
	sc := scanner{s: s}
	var out []float64
	for {
		v, ok := sc.number()
		if !ok {
			break
		}
		out = append(out, v)
	}
	sc.sep()
	if sc.i != len(sc.s) {
		return nil, fmt.Errorf("svg: unexpected %q in number list %q; unit suffixes are not supported, use unitless user units", sc.s[sc.i:], s)
	}
	return out, nil
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
