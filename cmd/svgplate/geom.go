package main

import (
	"math"

	"seedhammer.com/bezier"
	"seedhammer.com/svgpath"
)

// fpt is a floating-point plate coordinate, kept in millimeters
// through layout and quantized to payload units only at emit.
type fpt struct{ X, Y float64 }

// fseg is a path segment in float coordinates: a MoveTo or LineTo (one
// point), QuadTo (two) or CubeTo (three), the same op set the payload
// carries.
type fseg struct {
	op svgpath.SegmentOp
	p  [3]fpt
}

func (s fseg) npts() int {
	switch s.op {
	case svgpath.MoveTo, svgpath.LineTo:
		return 1
	case svgpath.QuadTo:
		return 2
	case svgpath.CubeTo:
		return 3
	}
	return 0
}

// end returns a segment's final point, its new pen position.
func (s fseg) end() fpt { return s.p[s.npts()-1] }

// matrix is a 2x3 affine transform [a b c d e f] acting on column
// vectors: x' = a*x + c*y + e, y' = b*x + d*y + f.
type matrix [6]float64

func identity() matrix { return matrix{1, 0, 0, 1, 0, 0} }

// mul returns m composed with n, applying n first: (m·n)·p = m·(n·p).
func (m matrix) mul(n matrix) matrix {
	return matrix{
		m[0]*n[0] + m[2]*n[1],
		m[1]*n[0] + m[3]*n[1],
		m[0]*n[2] + m[2]*n[3],
		m[1]*n[2] + m[3]*n[3],
		m[0]*n[4] + m[2]*n[5] + m[4],
		m[1]*n[4] + m[3]*n[5] + m[5],
	}
}

func (m matrix) apply(p fpt) fpt {
	return fpt{
		X: m[0]*p.X + m[2]*p.Y + m[4],
		Y: m[1]*p.X + m[3]*p.Y + m[5],
	}
}

// split separates m into its linear part and its translation, the two
// halves a placed shape needs: the linear part reshapes the outline,
// the translation moves the instance.
func (m matrix) split() (matrix, fpt) {
	return matrix{m[0], m[1], m[2], m[3], 0, 0}, fpt{m[4], m[5]}
}

func translateM(x, y float64) matrix { return matrix{1, 0, 0, 1, x, y} }
func scaleM(sx, sy float64) matrix   { return matrix{sx, 0, 0, sy, 0, 0} }

func rotateM(deg float64) matrix {
	r := deg * math.Pi / 180
	s, c := math.Sin(r), math.Cos(r)
	return matrix{c, s, -s, c, 0, 0}
}

func skewXM(deg float64) matrix { return matrix{1, 0, math.Tan(deg * math.Pi / 180), 1, 0, 0} }
func skewYM(deg float64) matrix { return matrix{1, math.Tan(deg * math.Pi / 180), 0, 1, 0, 0} }

// transform maps every point of a segment through m.
func (s fseg) transform(m matrix) fseg {
	out := fseg{op: s.op}
	for i := 0; i < s.npts(); i++ {
		out.p[i] = m.apply(s.p[i])
	}
	return out
}

// bounds is the axis-aligned hull of a segment list, from the control
// points. Curves stay within their control hull, so this bounds the
// geometry without flattening.
type bounds struct {
	min, max fpt
	empty    bool
}

func newBounds() bounds { return bounds{empty: true} }

func (b *bounds) add(p fpt) {
	if b.empty {
		b.min, b.max, b.empty = p, p, false
		return
	}
	b.min.X, b.min.Y = math.Min(b.min.X, p.X), math.Min(b.min.Y, p.Y)
	b.max.X, b.max.Y = math.Max(b.max.X, p.X), math.Max(b.max.Y, p.Y)
}

// drawing holds geometry in instanced form: every distinct outline
// once in shapes, and one entry in groups for each place it is
// stamped. Carrying the instancing through layout to the emitter is
// what lets a repeated shape reach the payload a single time.
type drawing struct {
	shapes [][]fseg
	groups []fgroup
}

// fgroup places shapes[shape] at at. A point of the shape lands at
// at + p, so a placement is a translation and nothing else.
type fgroup struct {
	shape int
	at    fpt
}

// addShape files an outline and returns its index.
func (d *drawing) addShape(segs []fseg) int {
	d.shapes = append(d.shapes, segs)
	return len(d.shapes) - 1
}

// place stamps a filed shape.
func (d *drawing) place(shape int, at fpt) {
	d.groups = append(d.groups, fgroup{shape: shape, at: at})
}

// transform maps a drawing through m without flattening it. The linear
// part reshapes each outline once and the full transform moves each
// placement, which lands the geometry exactly where transforming the
// flattened drawing would: M(at+p) = M(at) + L(p).
func (d *drawing) transform(m matrix) *drawing {
	lin, _ := m.split()
	out := &drawing{
		shapes: make([][]fseg, len(d.shapes)),
		groups: make([]fgroup, len(d.groups)),
	}
	for i, shape := range d.shapes {
		segs := make([]fseg, len(shape))
		for j, s := range shape {
			segs[j] = s.transform(lin)
		}
		out.shapes[i] = segs
	}
	for i, g := range d.groups {
		out.groups[i] = fgroup{shape: g.shape, at: m.apply(g.at)}
	}
	return out
}

// flatten returns every placement's geometry in absolute coordinates.
func (d *drawing) flatten() []fseg {
	var out []fseg
	for _, g := range d.groups {
		for _, s := range d.shapes[g.shape] {
			moved := fseg{op: s.op}
			for i := 0; i < s.npts(); i++ {
				moved.p[i] = fpt{X: s.p[i].X + g.at.X, Y: s.p[i].Y + g.at.Y}
			}
			out = append(out, moved)
		}
	}
	return out
}

func (d *drawing) bounds() bounds { return segsBounds(d.flatten()) }

func segsBounds(segs []fseg) bounds {
	b := newBounds()
	for _, s := range segs {
		for i := 0; i < s.npts(); i++ {
			b.add(s.p[i])
		}
	}
	return b
}

func (b bounds) width() float64  { return b.max.X - b.min.X }
func (b bounds) height() float64 { return b.max.Y - b.min.Y }

// bpt converts an integer font/payload point to float.
func bpt(p bezier.Point) fpt { return fpt{X: float64(p.X), Y: float64(p.Y)} }
