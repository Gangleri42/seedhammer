package curves

import (
	"encoding/binary"
	"errors"
	"fmt"

	"seedhammer.com/bezier"
	"seedhammer.com/svgpath"
)

// The binary path payload format. A one-line ASCII header — "2 path
// units-per-mm stroke-width\n" — dispatches version and mode in one
// Atoi before any binary parse, and stays greppable. The body is a
// compact binary stream:
//
//   - One opcode byte per command, the v1 letters 'M' 'L' 'Q' 'C'.
//   - Then the command's coordinate pairs, each a signed zigzag-varint
//     (encoding/binary) DELTA from the running cursor — the previous
//     coordinate. M starts a stroke, so its delta from the last stroke's
//     exit IS the travel vector; the flying planner reads it off the
//     wire. M and L carry one pair, Q two (control, end), C three.
//
// The body may open with a shape dictionary, so a drawing full of
// repeated shapes — a plate of text in a font the firmware does not
// have — ships each shape's outline once:
//
//   - 'D', a uvarint shape count, then each shape as a uvarint byte
//     length followed by that many bytes of the shape's own opcode
//     stream: shape-local coordinates with the cursor starting at
//     (0, 0), the first command a move, placements not allowed.
//   - In the main stream that follows, 'G' places a shape: a uvarint
//     shape id, then the zigzag-varint delta from the cursor to the
//     placement base. The shape's coordinates are all offset by the
//     base, and the cursor continues from its placed exit point, so
//     the next delta stays travel-sized.
//
// Coordinates are payload units (units-per-mm from the header).
// Decoding is a stateful four-way switch that reads a fixed pair count
// and accumulates the cursor, point by point, nothing materialized; a
// placement redirects it into the shape's byte range and back, one
// level deep, without allocating.

// MaxDictShapes caps the dictionary of a path payload. It bounds
// the shape index a payload can make the device retain; no realistic
// drawing needs more distinct shapes (the full engravable charset is
// under a hundred), so the cap only denies a hostile payload a large
// allocation.
const MaxDictShapes = 512

// maxPayloadCoord bounds the accumulated cursor, in payload units, so a
// hostile run of large deltas clamps out of the plate instead of
// overflowing the 32-bit cursor on the device. Accumulation is done in
// int64 and clamped to this before it is stored, then the scale step
// clamps again to machine maxCoord.
const maxPayloadCoord = 1 << 24

var errTruncatedVarint = errors.New("truncated varint")

// opPairs maps an opcode byte to its segment op and coordinate-pair
// count. ok is false for any other byte.
func opPairs(op byte) (svgpath.SegmentOp, int, bool) {
	switch op {
	case 'M':
		return svgpath.MoveTo, 1, true
	case 'L':
		return svgpath.LineTo, 1, true
	case 'Q':
		return svgpath.QuadTo, 2, true
	case 'C':
		return svgpath.CubeTo, 3, true
	}
	return 0, 0, false
}

// opByte is opPairs' inverse for the encoder.
func opByte(op svgpath.SegmentOp) (byte, int) {
	switch op {
	case svgpath.MoveTo:
		return 'M', 1
	case svgpath.LineTo:
		return 'L', 1
	case svgpath.QuadTo:
		return 'Q', 2
	case svgpath.CubeTo:
		return 'C', 3
	}
	panic("curves: unknown segment op")
}

// EncodePath encodes absolute payload-unit segments as a binary path
// payload. Each segs[i].Args holds the command's points in payload
// units — integer coordinates at the given units-per-mm; the encoder
// differences them into the wire's chained deltas. The first segment
// must be a MoveTo, matching the decoder's requirement.
func EncodePath(unitsPerMM, strokeWidth int, segs []svgpath.Segment) ([]byte, error) {
	if len(segs) == 0 || segs[0].Op != svgpath.MoveTo {
		return nil, fmt.Errorf("curves: path must begin with a move")
	}
	// Bound the coordinate domain: past maxPayloadCoord the decoder's
	// cursor clamp would silently reshape the drawing.
	for _, s := range segs {
		_, n := opByte(s.Op)
		for i := 0; i < n; i++ {
			if p := s.Args[i]; abs(p.X) > maxPayloadCoord || abs(p.Y) > maxPayloadCoord {
				return nil, fmt.Errorf("curves: coordinate %v outside the payload range", p)
			}
		}
	}
	b := []byte(fmt.Sprintf("%d %s %d %d\n", Version, ModePath, unitsPerMM, strokeWidth))
	var cur bezier.Point
	return appendSegs(b, segs, bezier.Point{}, &cur), nil
}

// appendSegs appends segs to b in the wire's opcode + chained-delta
// encoding, offsetting every point by off and differencing from *cur,
// which it advances to the final offset point.
func appendSegs(b []byte, segs []svgpath.Segment, off bezier.Point, cur *bezier.Point) []byte {
	for _, s := range segs {
		opc, n := opByte(s.Op)
		b = append(b, opc)
		for i := 0; i < n; i++ {
			p := s.Args[i].Add(off)
			b = binary.AppendVarint(b, int64(p.X-cur.X))
			b = binary.AppendVarint(b, int64(p.Y-cur.Y))
			*cur = p
		}
	}
	return b
}

// scanDict reads the optional shape dictionary opening a path
// body and validates its framing, so the decoder can jump into shape
// byte ranges without bounds checks of its own. It returns each
// shape's byte range and the offset of the main stream; a body not
// opening with 'D' has no dictionary. The shape streams themselves
// are validated as the decoder walks them.
func scanDict(body []byte) (shapes [][2]int32, main int, err error) {
	if len(body) == 0 || body[0] != 'D' {
		return nil, 0, nil
	}
	pos := 1
	count, n := binary.Uvarint(body[pos:])
	if n <= 0 {
		return nil, 0, fmt.Errorf("%w in dictionary", errTruncatedVarint)
	}
	pos += n
	if count == 0 || count > MaxDictShapes {
		return nil, 0, fmt.Errorf("dictionary of %d shapes, at most %d supported", count, MaxDictShapes)
	}
	shapes = make([][2]int32, 0, count)
	for i := 0; i < int(count); i++ {
		sz, n := binary.Uvarint(body[pos:])
		if n <= 0 {
			return nil, 0, fmt.Errorf("%w in dictionary", errTruncatedVarint)
		}
		pos += n
		if sz == 0 || sz > uint64(len(body)-pos) {
			return nil, 0, fmt.Errorf("dictionary shape %d runs past the payload", i)
		}
		if body[pos] != 'M' {
			return nil, 0, fmt.Errorf("dictionary shape %d must begin with a move", i)
		}
		shapes = append(shapes, [2]int32{int32(pos), int32(pos + int(sz))})
		pos += int(sz)
	}
	return shapes, pos, nil
}

// binaryIter walks a binary path body, yielding scaled machine-unit
// segments for run's builder pipeline. scale converts an accumulated
// payload-unit coordinate to machine units, clamping to maxCoord.
//
// A placement redirects the iterator into its shape's byte range with
// the placement base added to every shape-local coordinate, then back
// to the main stream — one level (scanDict guarantees a shape starts
// with a move, and place rejects nesting), with no allocation, so
// each replay of the engraving stays cheap.
type binaryIter struct {
	body  []byte
	dict  [][2]int32
	pos   int
	cur   bezier.Point // payload-unit cursor
	scale func(float64) int
	first bool
	err   error

	// Placement expansion state; inShape guards the single level.
	inShape bool
	sEnd    int          // end of the current shape's byte range
	ret     int          // main-stream position to resume at
	base    bezier.Point // placement base offsetting the shape
	local   bezier.Point // shape-local cursor
}

func newBinaryIter(body []byte, dict [][2]int32, main int, scale func(float64) int) *binaryIter {
	return &binaryIter{body: body, dict: dict, pos: main, scale: scale, first: true}
}

func (it *binaryIter) Err() error { return it.err }

// mainPos is the walk's position in the main stream, for progress
// reporting. It is monotonic across placement expansions: while a
// shape plays, the position holds at the main stream's resume point.
func (it *binaryIter) mainPos() int {
	if it.inShape {
		return it.ret
	}
	return it.pos
}

func (it *binaryIter) Next() (svgpath.Segment, bool) {
	for {
		if it.err != nil {
			return svgpath.Segment{}, false
		}
		if it.inShape && it.pos >= it.sEnd {
			// The shape is exhausted: the cursor continues from its
			// placed exit and the main stream resumes.
			it.inShape = false
			it.cur = addClamped(it.base, it.local)
			it.pos = it.ret
		}
		if !it.inShape && it.pos >= len(it.body) {
			return svgpath.Segment{}, false
		}
		opc := it.body[it.pos]
		it.pos++
		if opc == 'G' {
			if it.err = it.place(); it.err != nil {
				return svgpath.Segment{}, false
			}
			continue
		}
		op, n, ok := opPairs(opc)
		if !ok {
			it.err = fmt.Errorf("unexpected opcode %#x in path data", opc)
			return svgpath.Segment{}, false
		}
		if it.first && op != svgpath.MoveTo {
			it.err = fmt.Errorf("path data must begin with a move")
			return svgpath.Segment{}, false
		}
		it.first = false
		s := svgpath.Segment{Op: op}
		for i := 0; i < n; i++ {
			dx, ok := it.readDelta()
			if !ok {
				return svgpath.Segment{}, false
			}
			dy, ok := it.readDelta()
			if !ok {
				return svgpath.Segment{}, false
			}
			abs := &it.cur
			if it.inShape {
				abs = &it.local
			}
			*abs = bezier.Pt(
				clampPayload(int64(abs.X)+dx),
				clampPayload(int64(abs.Y)+dy),
			)
			p := *abs
			if it.inShape {
				p = addClamped(it.base, it.local)
			}
			s.Args[i] = bezier.Pt(it.scale(float64(p.X)), it.scale(float64(p.Y)))
		}
		return s, true
	}
}

// end bounds the current read region: the shape's declared byte range
// while expanding a placement, the whole body otherwise. Varints must
// not read past it — a shape whose length cuts a command short must
// error, not borrow the following bytes as coordinates.
func (it *binaryIter) end() int {
	if it.inShape {
		return it.sEnd
	}
	return len(it.body)
}

// place enters a 'G' placement: shape id, then the travel delta to the
// placement base. The iterator continues inside the shape's byte range.
func (it *binaryIter) place() error {
	if it.inShape {
		return fmt.Errorf("nested placement in a dictionary shape")
	}
	if len(it.dict) == 0 {
		return fmt.Errorf("placement without a dictionary")
	}
	id, n := binary.Uvarint(it.body[it.pos:])
	if n <= 0 {
		return fmt.Errorf("%w in path data", errTruncatedVarint)
	}
	it.pos += n
	if id >= uint64(len(it.dict)) {
		return fmt.Errorf("placement of shape %d outside the %d-shape dictionary", id, len(it.dict))
	}
	dx, ok := it.readDelta()
	if !ok {
		return it.err
	}
	dy, ok := it.readDelta()
	if !ok {
		return it.err
	}
	it.base = bezier.Pt(
		clampPayload(int64(it.cur.X)+dx),
		clampPayload(int64(it.cur.Y)+dy),
	)
	it.local = bezier.Point{}
	it.ret = it.pos
	sh := it.dict[id]
	it.pos, it.sEnd = int(sh[0]), int(sh[1])
	it.inShape = true
	return nil
}

// readDelta reads one zigzag-varint from the current read region,
// advancing pos. It records a truncation error and returns ok=false at
// the region's end.
func (it *binaryIter) readDelta() (int64, bool) {
	v, n := binary.Varint(it.body[it.pos:it.end()])
	if n <= 0 {
		it.err = fmt.Errorf("%w in path data", errTruncatedVarint)
		return 0, false
	}
	it.pos += n
	return v, true
}

// addClamped is the payload-unit point sum, clamped like any
// accumulated cursor.
func addClamped(a, b bezier.Point) bezier.Point {
	return bezier.Pt(
		clampPayload(int64(a.X)+int64(b.X)),
		clampPayload(int64(a.Y)+int64(b.Y)),
	)
}

func clampPayload(v int64) int {
	if v > maxPayloadCoord {
		return maxPayloadCoord
	}
	if v < -maxPayloadCoord {
		return -maxPayloadCoord
	}
	return int(v)
}
