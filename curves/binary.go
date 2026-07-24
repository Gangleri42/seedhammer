package curves

import (
	"encoding/binary"
	"errors"
	"fmt"

	"seedhammer.com/bezier"
	"seedhammer.com/svgpath"
)

// Version2 is the binary path payload format. It keeps v1's one-line
// ASCII header — "2 path units-per-mm stroke-width\n" — so the leading
// token still dispatches version and mode in one Atoi before any binary
// parse. The body then replaces v1's ASCII SVG path with a compact
// binary stream:
//
//   - One opcode byte per command, the v1 letters 'M' 'L' 'Q' 'C'.
//   - Then the command's coordinate pairs, each a signed zigzag-varint
//     (encoding/binary) DELTA from the running cursor — the previous
//     coordinate. M starts a stroke, so its delta from the last stroke's
//     exit IS the travel vector; the flying planner reads it off the
//     wire. M and L carry one pair, Q two (control, end), C three.
//
// Coordinates are payload units (units-per-mm from the header), the same
// quantization v1 uses, so a v2 payload decodes to geometry identical to
// the v1 payload for the same points — only the encoding differs.
// Decoding is a stateful four-way switch that reads a fixed pair count
// and accumulates the cursor, point by point, nothing materialized.
const Version2 = 2

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

// EncodePath encodes absolute payload-unit segments as a Version2 binary
// path payload. Each segs[i].Args holds the command's points in payload
// units — integer coordinates at the given units-per-mm; the encoder
// differences them into the wire's chained deltas. The first segment
// must be a MoveTo, matching the decoder's requirement.
func EncodePath(unitsPerMM, strokeWidth int, segs []svgpath.Segment) ([]byte, error) {
	if len(segs) == 0 || segs[0].Op != svgpath.MoveTo {
		return nil, fmt.Errorf("curves: path must begin with a move")
	}
	b := []byte(fmt.Sprintf("%d %s %d %d\n", Version2, ModePath, unitsPerMM, strokeWidth))
	var cur bezier.Point
	for _, s := range segs {
		opc, n := opByte(s.Op)
		b = append(b, opc)
		for i := 0; i < n; i++ {
			p := s.Args[i]
			b = binary.AppendVarint(b, int64(p.X-cur.X))
			b = binary.AppendVarint(b, int64(p.Y-cur.Y))
			cur = p
		}
	}
	return b, nil
}

// segIter is the segment source run walks: v1's ASCII svgpath.Iter or
// v2's binaryIter, both yielding scaled machine-unit segments so the
// builder pipeline downstream is identical for either format.
type segIter interface {
	Next() (svgpath.Segment, bool)
	Err() error
}

// binaryIter walks a Version2 binary body, yielding scaled machine-unit
// segments — the same stream svgpath.NewIter yields for v1, so run's
// builder pipeline is identical for both formats. scale converts an
// accumulated payload-unit coordinate to machine units (and clamps to
// maxCoord), exactly as v1's NewIter scale does.
type binaryIter struct {
	body  []byte
	pos   int
	cur   bezier.Point // payload-unit cursor
	scale func(float64) int
	first bool
	err   error
}

func newBinaryIter(body []byte, scale func(float64) int) *binaryIter {
	return &binaryIter{body: body, scale: scale, first: true}
}

func (it *binaryIter) Err() error { return it.err }

func (it *binaryIter) Next() (svgpath.Segment, bool) {
	if it.err != nil || it.pos >= len(it.body) {
		return svgpath.Segment{}, false
	}
	opc := it.body[it.pos]
	it.pos++
	op, n, ok := opPairs(opc)
	if !ok {
		it.err = fmt.Errorf("curves: unexpected opcode %#x in path data", opc)
		return svgpath.Segment{}, false
	}
	if it.first && op != svgpath.MoveTo {
		it.err = fmt.Errorf("curves: path data must begin with a move")
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
		it.cur = bezier.Pt(
			clampPayload(int64(it.cur.X)+dx),
			clampPayload(int64(it.cur.Y)+dy),
		)
		s.Args[i] = bezier.Pt(it.scale(float64(it.cur.X)), it.scale(float64(it.cur.Y)))
	}
	return s, true
}

// readDelta reads one zigzag-varint from the body, advancing pos. It
// records a truncation error and returns ok=false at end of input.
func (it *binaryIter) readDelta() (int64, bool) {
	v, n := binary.Varint(it.body[it.pos:])
	if n <= 0 {
		it.err = fmt.Errorf("curves: %w in path data", errTruncatedVarint)
		return 0, false
	}
	it.pos += n
	return v, true
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
