package curves

import (
	"encoding/binary"
	"fmt"
	"sort"

	"seedhammer.com/bezier"
	"seedhammer.com/svgpath"
)

// A Group is one placed shape of a drawing: segments in shape-local
// payload units, and the absolute payload-unit position they are
// placed at. Groups whose local segments are identical repeat one
// shape, which EncodeGroups then ships once through the dictionary.
// The emitter must therefore build the local segments identically for
// every instance — quantize the shape once in its own frame, then
// place it — or per-position rounding leaves nothing to deduplicate.
type Group struct {
	At   bezier.Point
	Segs []svgpath.Segment
}

// leadMoveLen is the byte length of a canonical key's leading move
// command: the opcode and its two delta varints. The key is encoder-
// built, so the varints are well-formed.
func leadMoveLen(k string) int {
	pos := 1
	for i := 0; i < 2; i++ {
		for k[pos] >= 0x80 {
			pos++
		}
		pos++
	}
	return pos
}

// EncodeGroups encodes placed shape groups as a binary path
// payload, carrying each repeated shape once in the dictionary section
// and stamping it by placement. A shape enters the dictionary when
// that undercuts inlining every instance; a drawing without such
// repeats encodes byte-identically to EncodePath of the flattened
// groups. Group order is engrave order. Each group's first segment
// must be a MoveTo, matching the decoder's requirement for its shapes.
func EncodeGroups(unitsPerMM, strokeWidth int, groups []Group) ([]byte, error) {
	if len(groups) == 0 {
		return nil, fmt.Errorf("curves: path must begin with a move")
	}
	// A group's canonical wire bytes — the dictionary entry it would
	// become — double as its dedup key. The absolute points must stay
	// inside the payload coordinate range: past it the decoder's
	// cursor clamp kicks in, and the flat and dictionary decodes of
	// one drawing clamp differently.
	keys := make([]string, len(groups))
	fixed := make([]Group, len(groups))
	for i, g := range groups {
		if len(g.Segs) == 0 || g.Segs[0].Op != svgpath.MoveTo {
			return nil, fmt.Errorf("curves: group %d must begin with a move", i)
		}
		g.Segs = preservePointStrokes(g.Segs)
		fixed[i] = g
		for _, s := range g.Segs {
			_, n := opByte(s.Op)
			for j := 0; j < n; j++ {
				if p := s.Args[j].Add(g.At); abs(p.X) > maxPayloadCoord || abs(p.Y) > maxPayloadCoord {
					return nil, fmt.Errorf("curves: coordinate %v outside the payload range", p)
				}
			}
		}
		var cur bezier.Point
		keys[i] = string(appendSegs(nil, g.Segs, bezier.Point{}, &cur))
	}
	groups = fixed
	counts := make(map[string]int, len(groups))
	firstSeen := make(map[string]int, len(groups))
	for i, k := range keys {
		if counts[k] == 0 {
			firstSeen[k] = i
		}
		counts[k]++
	}
	// The payoff accounting: inlining an instance costs the key bytes
	// with the leading move's deltas swapped for travel deltas, while a
	// placement costs 'G', a short id and the same travel — the travel
	// cancels, so each instance saves the shape's post-move bytes less
	// one ('G'+id against the bare move opcode). The dictionary costs
	// the entry and its length prefix once. (The id may widen past one
	// byte for cold shapes, and travels to a base differ slightly from
	// travels to a first point; both misjudge only shapes within a
	// byte or two of break-even.)
	var shapes []string
	for k, c := range counts {
		rest := len(k) - leadMoveLen(k)
		if c >= 2 && c*(rest-1) > uvarintLen(uint64(len(k)))+len(k) {
			shapes = append(shapes, k)
		}
	}
	// Most-placed shapes first, so the hottest ids are the shortest
	// uvarints; firstSeen breaks ties to keep the payload deterministic.
	sort.Slice(shapes, func(i, j int) bool {
		if ci, cj := counts[shapes[i]], counts[shapes[j]]; ci != cj {
			return ci > cj
		}
		return firstSeen[shapes[i]] < firstSeen[shapes[j]]
	})
	if len(shapes) > MaxDictShapes {
		shapes = shapes[:MaxDictShapes]
	}
	ids := make(map[string]int, len(shapes))
	for i, k := range shapes {
		ids[k] = i
	}

	b := []byte(fmt.Sprintf("%d %s %d %d\n", Version, ModePath, unitsPerMM, strokeWidth))
	if len(shapes) > 0 {
		b = append(b, 'D')
		b = binary.AppendUvarint(b, uint64(len(shapes)))
		for _, k := range shapes {
			b = binary.AppendUvarint(b, uint64(len(k)))
			b = append(b, k...)
		}
	}
	var cur bezier.Point
	for i, g := range groups {
		if id, ok := ids[keys[i]]; ok {
			b = append(b, 'G')
			b = binary.AppendUvarint(b, uint64(id))
			b = binary.AppendVarint(b, int64(g.At.X-cur.X))
			b = binary.AppendVarint(b, int64(g.At.Y-cur.Y))
			// The decoder's cursor continues from the placed exit.
			cur = g.At.Add(groupExit(g.Segs))
		} else {
			b = appendSegs(b, g.Segs, g.At, &cur)
		}
	}
	return b, nil
}

// groupExit is the local exit point of a group's shape: its last
// segment's on-curve endpoint.
func groupExit(segs []svgpath.Segment) bezier.Point {
	return endpoint(segs[len(segs)-1])
}

func uvarintLen(v uint64) int {
	n := 1
	for v >= 0x80 {
		v >>= 7
		n++
	}
	return n
}
