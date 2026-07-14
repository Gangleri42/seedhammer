// Package glyph renders sh-font glyphs as path geometry. It is the
// single source of glyph outlines shared by the plate editor's font
// export (cmd/textplate, via glyphs.js) and the SVG/text engraving
// converter (cmd/svgplate), so a glyph laid out as curves matches the
// one the firmware engraves.
package glyph

import (
	"fmt"
	"strings"

	"seedhammer.com/bezier"
	"seedhammer.com/bspline"
	"seedhammer.com/font/sh"
	"seedhammer.com/svgpath"
)

// Segments returns a glyph's outline as absolute path segments in font
// units, with the baseline shifted to the cell top (y grows down from
// y=0), matching the coordinate frame the plate editor lays glyphs out
// in. The segments are pure font geometry: a MoveTo starting each
// contour and CubeTo for every stroke, without the planner's travel or
// timing. It also returns the glyph's advance width. ok is false for a
// rune the font cannot engrave.
func Segments(ch rune) (segs []svgpath.Segment, advance int, ok bool) {
	adv, spline, ok := sh.Font.Decode(ch)
	if !ok {
		return nil, adv, false
	}
	ascent := sh.Font.Metrics().Ascent
	var seg bspline.Segment
	var prev bspline.Knot
	first := true
	emit := func(k bspline.Knot) {
		c, dt, line := seg.Knot(k)
		if dt == 0 {
			return
		}
		if line {
			segs = append(segs, svgpath.Segment{
				Op: svgpath.CubeTo,
				Args: [4]bezier.Point{
					{X: c.C1.X, Y: c.C1.Y + ascent},
					{X: c.C2.X, Y: c.C2.Y + ascent},
					{X: c.C3.X, Y: c.C3.Y + ascent},
				},
			})
		} else {
			segs = append(segs, svgpath.Segment{
				Op:   svgpath.MoveTo,
				Args: [4]bezier.Point{{X: c.C3.X, Y: c.C3.Y + ascent}},
			})
		}
	}
	for {
		vk, ok := spline.Next()
		if !ok {
			break
		}
		k := bspline.Knot{Ctrl: vk.Ctrl, T: 1, Engrave: vk.Line}
		if !first && k.Ctrl == prev.Ctrl {
			k.T = 0
		}
		first = false
		prev = k
		emit(k)
	}
	return segs, adv, true
}

// Path formats a glyph's outline as SVG path data in font units,
// identical to the string the plate editor's font export emits.
func Path(ch rune) string {
	segs, _, ok := Segments(ch)
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, s := range segs {
		switch s.Op {
		case svgpath.MoveTo:
			fmt.Fprintf(&b, "M%d %d", s.Args[0].X, s.Args[0].Y)
		case svgpath.CubeTo:
			fmt.Fprintf(&b, "C%d %d %d %d %d %d",
				s.Args[0].X, s.Args[0].Y, s.Args[1].X, s.Args[1].Y, s.Args[2].X, s.Args[2].Y)
		}
	}
	return b.String()
}

// Metrics reports the sh-font vertical metrics: the ascent (baseline
// offset from the cell top) and the cell height, in font units.
func Metrics() (ascent, height int) {
	m := sh.Font.Metrics()
	return m.Ascent, m.Height
}
