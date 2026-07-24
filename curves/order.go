package curves

import (
	"seedhammer.com/bezier"
	"seedhammer.com/svgpath"
)

// Order reorders a drawing's strokes to shorten the non-engraving travel
// between them, and picks each stroke's direction — a stroke engraves the
// same marks whichever way it is traced. Every stroke's geometry is
// preserved; only the sequence and per-stroke direction change, so the
// engraved plate is unchanged while the head travels a shorter path (and
// the planned engraving runs quicker). segs must begin with a MoveTo; the
// result does too.
//
// This is an emit-time optimization: the emitter has the whole scene and
// host RAM, and the firmware engraves payload order verbatim (retiring
// its on-device chaining). It must run only on non-secret drawings —
// stroke order would leak content timing otherwise — but curves payloads
// carry drawings, not seed material, so that holds by construction.
//
// The heuristic is greedy nearest-endpoint from the head's origin, with
// exit-aware direction choice. It is O(strokes^2), trivial at the 512
// stroke cap, and leaves the geometry bit-exact.
func Order(segs []svgpath.Segment) []svgpath.Segment {
	// A path not starting with a move is invalid; return it unchanged so the
	// encoder rejects it with its usual error instead of ordering silently
	// dropping the leading draw commands.
	if len(segs) == 0 || segs[0].Op != svgpath.MoveTo {
		return segs
	}
	strokes := splitStrokes(segs)
	if len(strokes) <= 1 {
		return segs
	}
	used := make([]bool, len(strokes))
	out := make([]svgpath.Segment, 0, len(segs))
	cur := bezier.Point{} // the planner clamps the head at the origin to start
	for range strokes {
		best, bestRev, bestD := -1, false, int64(-1)
		for i := range strokes {
			if used[i] {
				continue
			}
			if d := dist2(cur, strokes[i].entry); bestD < 0 || d < bestD {
				bestD, best, bestRev = d, i, false
			}
			if d := dist2(cur, strokes[i].exit); d < bestD {
				bestD, best, bestRev = d, i, true
			}
		}
		s := strokes[best]
		if bestRev {
			s = reverseStroke(s)
		}
		used[best] = true
		out = append(out, s.segs...)
		cur = s.exit
	}
	return out
}

// stroke is one contour: its leading MoveTo and draw commands, plus the
// on-curve points (entry, then each segment's endpoint) reversal needs.
type stroke struct {
	segs  []svgpath.Segment
	pts   []bezier.Point
	entry bezier.Point
	exit  bezier.Point
}

// splitStrokes cuts a segment list into strokes at each MoveTo.
func splitStrokes(segs []svgpath.Segment) []stroke {
	var strokes []stroke
	var cur stroke
	have := false
	flush := func() {
		if have {
			strokes = append(strokes, cur)
		}
	}
	for _, s := range segs {
		if s.Op == svgpath.MoveTo {
			flush()
			cur = stroke{
				segs:  []svgpath.Segment{s},
				pts:   []bezier.Point{s.Args[0]},
				entry: s.Args[0],
				exit:  s.Args[0],
			}
			have = true
			continue
		}
		if !have {
			// A draw before any move; Parse rejects this, skip defensively.
			continue
		}
		e := endpoint(s)
		cur.segs = append(cur.segs, s)
		cur.pts = append(cur.pts, e)
		cur.exit = e
	}
	flush()
	return strokes
}

// endpoint returns a segment's on-curve endpoint, its new pen position.
func endpoint(s svgpath.Segment) bezier.Point {
	switch s.Op {
	case svgpath.QuadTo:
		return s.Args[1]
	case svgpath.CubeTo:
		return s.Args[2]
	default: // MoveTo, LineTo
		return s.Args[0]
	}
}

// reverseStroke returns the stroke traced from its exit back to its
// entry: the same curve, opposite direction. Each command becomes one of
// the same type ending at the previous on-curve point, with a cubic's two
// control points swapped so the shape is preserved exactly.
func reverseStroke(s stroke) stroke {
	out := make([]svgpath.Segment, 0, len(s.segs))
	out = append(out, svgpath.Segment{Op: svgpath.MoveTo, Args: [4]bezier.Point{s.exit}})
	for i := len(s.segs) - 1; i >= 1; i-- {
		seg := s.segs[i]
		prev := s.pts[i-1] // the point this reversed command ends at
		var r svgpath.Segment
		switch seg.Op {
		case svgpath.QuadTo:
			r = svgpath.Segment{Op: svgpath.QuadTo, Args: [4]bezier.Point{seg.Args[0], prev}}
		case svgpath.CubeTo:
			r = svgpath.Segment{Op: svgpath.CubeTo, Args: [4]bezier.Point{seg.Args[1], seg.Args[0], prev}}
		default: // LineTo
			r = svgpath.Segment{Op: svgpath.LineTo, Args: [4]bezier.Point{prev}}
		}
		out = append(out, r)
	}
	rp := make([]bezier.Point, len(s.pts))
	for i, p := range s.pts {
		rp[len(s.pts)-1-i] = p
	}
	return stroke{segs: out, pts: rp, entry: s.exit, exit: s.entry}
}

// dist2 is the squared distance between two points; enough to compare
// travels without a square root, and int64 holds it at plate scale.
func dist2(a, b bezier.Point) int64 {
	dx, dy := int64(a.X-b.X), int64(a.Y-b.Y)
	return dx*dx + dy*dy
}
