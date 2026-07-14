// Package engrave transforms shapes such as text and QR codes into
// line and move commands for use with an engraver.
package engrave

import (
	"errors"
	"fmt"
	"iter"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	qr "github.com/seedhammer/kortschak-qr"
	"seedhammer.com/bezier"
	"seedhammer.com/bspline"
	"seedhammer.com/font/vector"
)

// StepperConfig is a configuration for a [Stepper].
type StepperConfig struct {
	// Move speed in steps/second.
	Speed uint
	// EngraveSpeed in steps/second.
	EngravingSpeed uint
	// Acceleration (and deceleration) in steps/second².
	Acceleration uint
	// Jerk, the change in acceleration, in steps/second³.
	Jerk uint
	// Engraver ticks per second. A tick represents the duration
	// of a completed pio step.
	TicksPerSecond uint
}

// Params decribe the physical characteristics of an
// engraver.
type Params struct {
	// The StrokeWidth measured in machine units.
	StrokeWidth int
	// A Millimeter measured in machine units.
	Millimeter int
	StepperConfig
}

func (p Params) F(v float32) int {
	return int(math.Round(float64(v * float32(p.Millimeter))))
}

func (p Params) I(v int) int {
	return p.Millimeter * v
}

// Engraving is an iterator over the commands of an engraving.
type Engraving = iter.Seq[Command]

type Command struct {
	kind cmdKind
	args [3]uint
}

// splineKnot represents a control point in a uniform
// b-spline.
type splineKnot struct {
	Engrave      bool
	Periodic     bool
	Knot         bezier.Point
	Multiplicity int
}

type cmdKind uint8

const (
	moveCmd cmdKind = iota
	lineCmd
	linePeriodicCmd
	delayCmd
)

func (c Command) AsDelay() (denom, nom uint, ok bool) {
	switch c.kind {
	case delayCmd:
	default:
		return 0, 0, false
	}
	return uint(c.args[0]), uint(c.args[1]), true
}

func (c Command) AsKnot() (splineKnot, bool) {
	line, periodic := false, false
	switch c.kind {
	case moveCmd:
	case lineCmd:
		line = true
	case linePeriodicCmd:
		line, periodic = true, true
	default:
		return splineKnot{}, false
	}
	return splineKnot{
		Engrave:  line,
		Periodic: periodic,
		Knot: bezier.Point{
			X: int(c.args[0]),
			Y: int(c.args[1]),
		},
		Multiplicity: int(c.args[2]),
	}, true
}

type Transform struct {
	Yield func(Command) bool
	stack *transformStack
	slot  int
	id    int
}

func NewTransform(yield func(Command) bool) Transform {
	s := new(transformStack)
	s.yield = func(c Command) bool {
		if !s.done {
			switch c.kind {
			case moveCmd, lineCmd, linePeriodicCmd:
				p := bezier.Pt(int(c.args[0]), int(c.args[1]))
				coord := s.transform(p)
				c.args[0], c.args[1] = uint(coord.X), uint(coord.Y)
			}
			s.done = !yield(c)
		}
		return !s.done
	}
	s.stack = append(s.stack, transformSlot{t: offsetting(0, 0)})
	return Transform{stack: s, Yield: s.yield}
}

type transformStack struct {
	stack []transformSlot
	id    int
	yield func(Command) bool
	done  bool
}

type transformSlot struct {
	t  transform
	id int
}

func (s *transformStack) transform(p bezier.Point) bezier.Point {
	var t transform
	if n := len(s.stack); n > 0 {
		t = s.stack[n-1].t
	}
	return t.transform(p)
}

func (s *transformStack) push(id, slot int, t transform) Transform {
	if s.stack[slot].id != id {
		panic("transform was popped")
	}
	t0 := s.stack[slot].t
	e := transformSlot{
		id: s.id,
		t:  t0.Mul(t),
	}
	slot++
	s.stack = append(s.stack[:slot], e)
	s.id++
	return Transform{
		stack: s,
		slot:  slot,
		id:    e.id,
		Yield: s.yield,
	}
}

func (t Transform) Scale(sx, sy int) Transform {
	return t.push(scaling(sx, sy))
}

func (t Transform) Offset(x, y int) Transform {
	return t.push(offsetting(x, y))
}

func (t Transform) Rotate(radians float64) Transform {
	return t.push(rotating(radians))
}

func (t Transform) push(tr transform) Transform {
	return t.stack.push(t.id, t.slot, tr)
}

type transform [6]int

func (m transform) transform(p bezier.Point) bezier.Point {
	return bezier.Point{
		X: p.X*m[0] + p.Y*m[1] + m[2],
		Y: p.X*m[3] + p.Y*m[4] + m[5],
	}
}

func (m transform) Mul(m2 transform) transform {
	return transform{
		m[0]*m2[0] + m[1]*m2[3], m[0]*m2[1] + m[1]*m2[4], m[0]*m2[2] + m[1]*m2[5] + m[2],
		m[3]*m2[0] + m[4]*m2[3], m[3]*m2[1] + m[4]*m2[4], m[3]*m2[2] + m[4]*m2[5] + m[5],
	}
}

func rotating(radians float64) transform {
	sin, cos := math.Sincos(float64(radians))
	s, c := int(math.Round(sin)), int(math.Round(cos))
	return transform{
		c, -s, 0,
		s, c, 0,
	}
}

func offsetting(x, y int) transform {
	return transform{
		1, 0, x,
		0, 1, y,
	}
}

func scaling(sx, sy int) transform {
	return transform{
		sx, 0, 0,
		0, sy, 0,
	}
}

func DelayMove(yield func(Command) bool, conf StepperConfig, t uint, from, to bezier.Point) bool {
	dur := timeMove(conf, ManhattanDist(from, to))
	return yield(Delay(dur, t)) &&
		yield(Move(to))
}

func Delay(denom, nom uint) Command {
	c := Command{
		kind: delayCmd,
	}
	c.args[0] = uint(denom)
	c.args[1] = uint(nom)
	return c
}

func ControlPoint(engrave bool, ctrl bezier.Point) Command {
	c := Command{kind: moveCmd}
	if engrave {
		c.kind = lineCmd
	}
	c.args[0], c.args[1], c.args[2] = uint(ctrl.X), uint(ctrl.Y), 1
	return c
}

// PeriodicPoint is an engraved control point of a periodic contour: a
// closed smooth run the planner paces cyclically across its seam
// instead of against the clamp boundaries.
func PeriodicPoint(ctrl bezier.Point) Command {
	c := Command{kind: linePeriodicCmd}
	c.args[0], c.args[1], c.args[2] = uint(ctrl.X), uint(ctrl.Y), 1
	return c
}

func Move(p bezier.Point) Command {
	c := Command{
		kind: moveCmd,
	}
	c.args[0], c.args[1], c.args[2] = uint(p.X), uint(p.Y), 3
	return c
}

func Line(p bezier.Point) Command {
	c := Command{
		kind: lineCmd,
	}
	c.args[0], c.args[1], c.args[2] = uint(p.X), uint(p.Y), 3
	return c
}

func DryRun(s bspline.Curve) bspline.Curve {
	return func(yield func(bspline.Knot) bool) {
		for c := range s {
			c.Engrave = false
			if !yield(c) {
				return
			}
		}
	}
}

func QR(strokeWidth int, scale int, qr *qr.Code) Engraving {
	return func(yield func(Command) bool) {
		dim := qr.Size
		cont := true
		radius := strokeWidth / 2
		for y := range dim {
			for i := range scale {
				draw := false
				var firstx int
				line := y*scale + i
				// Swap direction every other line.
				rev := line%2 != 0
				off := radius
				if rev {
					off = -off
				}
				drawLine := func(endx int) {
					start := bezier.Pt(firstx*scale*strokeWidth+off, line*strokeWidth+radius)
					end := bezier.Pt(endx*scale*strokeWidth-off, line*strokeWidth+radius)
					cont = cont && yield(Move(start)) && yield(Line(end))
					draw = false
				}
				for x := -1; x <= dim; x++ {
					xl := x
					px := x
					if rev {
						xl = dim - 1 - x
						px = xl - 1
					}
					on := qr.Black(px, y)
					switch {
					case !draw && on:
						draw = true
						firstx = xl
					case draw && !on:
						drawLine(xl)
					}
				}
			}
		}
	}
}

// qrMovesPerModule is the exact number of qrMovesPerModule before engraving
// a constant time QR module.
const qrMovesPerModule = 4

// qrMove represent a move up to [qrMovesPerModule] far.
type qrMove struct {
	m uint8
}

func (m qrMove) Point() bezier.Point {
	return bezier.Point{
		X: int(m.m&0b1111) - qrMovesPerModule,
		Y: int(m.m>>4) - qrMovesPerModule,
	}
}

// constantQRMove computes a list of moves from the origin to target.
func constantQRMove(target bezier.Point) qrMove {
	m := qrMove{
		m: (uint8(target.X+qrMovesPerModule) & 0b1111) | uint8(target.Y+qrMovesPerModule)<<4,
	}
	if m.Point() != target {
		panic("move too far")
	}
	return m
}

// constantTimeQRModules returns the exact number of modules in a constant
// time QR code, given its dimension.
func constantTimeQRModules(dims int) int {
	// The numbers below are maximum numbers found through fuzzing.
	// Add a bit more to account for outliers not yet found.
	const extra = 5
	switch dims {
	case 21:
		return 166 + extra
	case 25:
		return 261 + extra
	case 29:
		return 386 + extra
	case 33:
		return 542 + extra
	}
	// Not supported, return a low number to force error.
	return 0
}

func constantTimeStartEnd(dim int) (start, end bezier.Point) {
	return bezier.Pt(8+qrMovesPerModule, dim-1-qrMovesPerModule), bezier.Pt(dim-1-3, 3)
}

func bitmapForQR(qr *qr.Code) bitmap {
	dim := qr.Size
	bm := newBitmap(dim, dim)
	for y := range dim {
		for x := range dim {
			if qr.Black(x, y) {
				bm.Set(bezier.Pt(x, y))
			}
		}
	}
	return bm
}

func bitmapForQRStatic(dim int) ([]bezier.Point, []bezier.Point) {
	// First 3 position markers.
	posMarkers := []bezier.Point{
		{},
		{X: dim - 7},
		{Y: dim - 7},
	}
	var alignMarkers []bezier.Point
	switch dim {
	case 21:
		// No marker.
	case 25, 29, 33:
		// Single marker.
		alignMarkers = append(alignMarkers, bezier.Pt(dim-9, dim-9))
	default:
		panic("unsupported qr code version")
	}
	return posMarkers, alignMarkers
}

// ConstantQR is like QR that engraves the QR code in a pattern independent of content,
// except for the QR code version (size).
func ConstantQR(qrc *qr.Code) (*ConstantQRCmd, error) {
	dim := qrc.Size
	if dim > 33 {
		return nil, fmt.Errorf("engrave: constant QR size too large: %d", dim)
	}
	qr := bitmapForQR(qrc)
	engraved := newBitmap(dim, dim)
	posMarkers, alignMarkers := bitmapForQRStatic(dim)
	// No need to engrave static features of the QR code.
	for _, p := range posMarkers {
		fillMarker(engraved, p, positionMarker)
	}
	for _, p := range alignMarkers {
		fillMarker(engraved, p, alignmentMarker)
	}
	// Start in the lower-left corner.
	pos := bezier.Pt(0, dim-1)
	// Iterating forward.
	dir := 1
	start, end := constantTimeStartEnd(dim)
	needle := start
	nmod := constantTimeQRModules(dim)
	modules := make([]qrMove, 0, nmod)
	waste := 0
	engrave := func(p bezier.Point) {
		m := constantQRMove(p.Sub(needle))
		modules = append(modules, m)
		needle = p
		if engraved.Get(p) {
			waste++
		} else {
			engraved.Set(p)
		}
	}
	visited := newBitmap(dim, dim)
	// Find path to a module close enough to p.
	move := func(p bezier.Point) error {
		clear(visited.bits)
		visited.Set(needle)
		path, ok := findPath(nil, visited, qr, engraved, p, needle)
		if !ok {
			return errors.New("QR modules spaced too far for constant time engraving")
		}
		for _, m := range path {
			engrave(needle.Add(m.Point()))
		}
		return nil
	}
	for pos.Y >= 0 {
		if qr.Get(pos) && !engraved.Get(pos) {
			dist := ManhattanDist(pos, needle)
			if dist > qrMovesPerModule {
				if err := move(pos); err != nil {
					return nil, err
				}
			}
			engrave(pos)
		}
		// Advance to next module.
		if nextx := pos.X + dir; 0 <= nextx && nextx < dim {
			pos.X = nextx
			continue
		}
		// Row complete, advance to previous row.
		dir = -dir
		pos.Y--
	}
	if err := move(end); err != nil {
		return nil, err
	}
	if len(modules) > nmod {
		return nil, fmt.Errorf("too many dims %d QR modules for constant time engraving n: %d waste: %d",
			dim, len(modules), waste)
	}
	cmd := &ConstantQRCmd{
		Size: dim,
		plan: modules,
	}
	return cmd, nil
}

var alignmentMarker = []bezier.Point{
	{X: 0, Y: 0},
	{X: 1, Y: 0},
	{X: 2, Y: 0},
	{X: 3, Y: 0},
	{X: 4, Y: 0},

	{X: 4, Y: 1},
	{X: 4, Y: 2},
	{X: 4, Y: 3},

	{X: 4, Y: 4},
	{X: 3, Y: 4},
	{X: 2, Y: 4},
	{X: 1, Y: 4},
	{X: 0, Y: 4},

	{X: 0, Y: 3},
	{X: 0, Y: 2},
	{X: 0, Y: 1},

	{X: 2, Y: 2},
}

var positionMarker = []bezier.Point{
	{X: 0, Y: 0},
	{X: 1, Y: 0},
	{X: 2, Y: 0},
	{X: 3, Y: 0},
	{X: 4, Y: 0},
	{X: 5, Y: 0},
	{X: 6, Y: 0},

	{X: 6, Y: 1},
	{X: 6, Y: 2},
	{X: 6, Y: 3},
	{X: 6, Y: 4},
	{X: 6, Y: 5},

	{X: 6, Y: 6},
	{X: 5, Y: 6},
	{X: 4, Y: 6},
	{X: 3, Y: 6},
	{X: 2, Y: 6},
	{X: 1, Y: 6},
	{X: 0, Y: 6},

	{X: 0, Y: 5},
	{X: 0, Y: 4},
	{X: 0, Y: 3},
	{X: 0, Y: 2},
	{X: 0, Y: 1},

	{X: 2, Y: 2},
	{X: 3, Y: 2},
	{X: 4, Y: 2},
	{X: 2, Y: 3},
	{X: 3, Y: 3},
	{X: 4, Y: 3},
	{X: 2, Y: 4},
	{X: 3, Y: 4},
	{X: 4, Y: 4},
}

func fillMarker(engraved bitmap, off bezier.Point, points []bezier.Point) {
	for _, p := range points {
		p = p.Add(off)
		engraved.Set(p)
	}
}

func findPath(modules []qrMove, visited, qr, engraved bitmap, to, from bezier.Point) ([]qrMove, bool) {
	if ManhattanDist(from, to) <= qrMovesPerModule {
		return modules, true
	}
	// The maximum number of positions is the manhattan square reachable
	// from the starting point. Subtract 1 for the center which is always
	// marked visible.
	const nmoves = (2*qrMovesPerModule+1)*(2*qrMovesPerModule+1) - 1
	candidates := make([]qrMove, 0, nmoves)
	for y := -qrMovesPerModule; y <= qrMovesPerModule; y++ {
		for x := -qrMovesPerModule; x <= qrMovesPerModule; x++ {
			m := constantQRMove(bezier.Pt(x, y))
			p := from.Add(m.Point())
			if !qr.Get(p) || visited.Get(p) {
				continue
			}
			visited.Set(p)
			candidates = append(candidates, m)
		}
	}
	slices.SortFunc(candidates, func(mi, mj qrMove) int {
		pi := from.Add(mi.Point())
		pj := from.Add(mj.Point())
		di, dj := ManhattanDist(pi, to), ManhattanDist(pj, to)
		if di == dj {
			// Equal distance; prefer the un-engraved path.
			if engraved.Get(pj) {
				return -1
			} else {
				return 1
			}
		}
		return di - dj
	})
	for _, m := range candidates {
		p := from.Add(m.Point())
		path, ok := findPath(append(modules, m), visited, qr, engraved, to, p)
		if ok {
			return path, true
		}
	}
	return nil, false
}

// ConstantQRCmd represents the constant time plan for engraving a QR
// code.
type ConstantQRCmd struct {
	// The QR dimension.
	Size int
	// The list of moves.
	plan []qrMove
}

func centerOf(sw, scale int, p bezier.Point) bezier.Point {
	radius := sw / 2
	return p.Mul(scale).Add(bezier.Pt(1, 1)).Mul(sw).Add(bezier.Pt(radius, radius))
}

func (q ConstantQRCmd) Engrave(conf StepperConfig, strokeWidth, scale int) Engraving {
	return func(yield func(Command) bool) {
		cont := true
		posMarkers, alignMarkers := bitmapForQRStatic(q.Size)
		start, end := constantTimeStartEnd(q.Size)
		for _, off := range posMarkers {
			for _, m := range positionMarker {
				center := centerOf(strokeWidth, scale, m.Add(off))
				cont = cont && yield(Move(center)) &&
					engraveModule(yield, strokeWidth, scale, center)
			}
		}
		for _, off := range alignMarkers {
			for _, m := range alignmentMarker {
				center := centerOf(strokeWidth, scale, m.Add(off))
				cont = cont && yield(Move(center)) &&
					engraveModule(yield, strokeWidth, scale, center)
			}
		}
		needle := start
		cont = cont && yield(Move(centerOf(strokeWidth, scale, needle)))
		maxDur := timeMove(conf, qrMovesPerModule*strokeWidth*scale)
		nmod := constantTimeQRModules(q.Size)
		// len(q.plan) is generally less than nmod, the constant number of
		// modules to engrave. Accumulate fractions in units of 1/nmod where
		// each q.plan module contributes len(q.plan) fractions. Advance
		// the plan when the accumulated fraction is >= 1.
		accum := 0
		plan := q.plan
		advance := true
		for range nmod {
			var move bezier.Point
			if advance {
				move = plan[0].Point()
			}
			from := centerOf(strokeWidth, scale, needle)
			needle = needle.Add(move)
			to := centerOf(strokeWidth, scale, needle)
			cont = cont && DelayMove(yield, conf, maxDur, from, to) &&
				engraveModule(yield, strokeWidth, scale, to) &&
				yield(Line(to))
			accum += len(q.plan)
			advance = accum >= nmod
			if advance {
				accum -= nmod
				plan = plan[1:]
			}
		}
		// Move to end point.
		from := centerOf(strokeWidth, scale, needle)
		needle = end
		to := centerOf(strokeWidth, scale, needle)
		cont = cont && DelayMove(yield, conf, maxDur, from, to)
	}
}

func engraveModule(yield func(Command) bool, sw, scale int, center bezier.Point) bool {
	switch scale {
	case 3:
		return yield(Line(center.Add(bezier.Pt(sw, 0)))) &&
			yield(Line(center.Add(bezier.Pt(sw, sw)))) &&
			yield(Line(center.Add(bezier.Pt(-sw, sw)))) &&
			yield(Line(center.Add(bezier.Pt(-sw, -sw)))) &&
			yield(Line(center.Add(bezier.Pt(sw, -sw))))
	case 4:
		return yield(Line(center.Add(bezier.Pt(-sw, 0)))) &&
			yield(Line(center.Add(bezier.Pt(-sw, -sw)))) &&
			yield(Line(center.Add(bezier.Pt(2*sw, -sw)))) &&
			yield(Line(center.Add(bezier.Pt(2*sw, 2*sw)))) &&
			yield(Line(center.Add(bezier.Pt(-sw, 2*sw)))) &&
			yield(Line(center.Add(bezier.Pt(-sw, sw)))) &&
			yield(Line(center.Add(bezier.Pt(sw, sw)))) &&
			yield(Line(center.Add(bezier.Pt(sw, 0))))
	default:
		panic("unsupported module scale")
	}
}

func ManhattanDist(p1, p2 bezier.Point) int {
	return manhattanLen(p1.Sub(p2))
}

func manhattanLen(v bezier.Point) int {
	if v.X < 0 {
		v.X = -v.X
	}
	if v.Y < 0 {
		v.Y = -v.Y
	}
	return int(max(v.X, v.Y))
}

type bitmap struct {
	w    int
	bits []uint64
}

func newBitmap(w, h int) bitmap {
	if w > 64 {
		panic("bitset too wide")
	}
	return bitmap{
		w:    w,
		bits: make([]uint64, h),
	}
}

func (b bitmap) Set(p bezier.Point) {
	if p.X < 0 || p.Y < 0 || p.X >= b.w || int(p.Y) >= len(b.bits) {
		panic("out of range")
	}
	b.bits[p.Y] |= 1 << p.X
}

func (b bitmap) Get(p bezier.Point) bool {
	if p.X < 0 || p.Y < 0 || p.X >= b.w || int(p.Y) >= len(b.bits) {
		return false
	}
	return b.bits[p.Y]&(1<<p.X) != 0
}

type Rect bspline.Bounds

func (r Rect) Engrave(yield func(Command) bool) {
	_ = yield(Move(r.Min)) &&
		yield(Line(bezier.Point{X: r.Max.X, Y: r.Min.Y})) &&
		yield(Line(r.Max)) &&
		yield(Line(bezier.Point{X: r.Min.X, Y: r.Max.Y})) &&
		yield(Line(r.Min))
}

const constantAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"

// ConstantStringer can engrave text in a timing insensitive way.
type ConstantStringer struct {
	face *vector.Face
	// runeDuration is the duration of the longest rune.
	runeDuration uint
	// center is the starting position.
	center bezier.Point
	// startEndDist is the longest distance between a rune start and end
	// points.
	startEndDist int
	// advDist is the face advance in steps.
	advDist int
	// em is the font size.
	em       int
	alphabet []constantRune
	conf     StepperConfig
}

type constantRune struct {
	R    rune
	Info constantPlan
}

type constantPlan struct {
	Duration   uint
	Start, End bezier.Point
}

// An scurvePhase is a (duration, distance) tuple of a phase in an [scurve].
type scurvePhase struct {
	Duration uint
	Position uint
}

// computeSCurve computes the phases for traveling dist along a straight
// line. The waypoints respects machine limits and continuity up to and
// including acceleration.
// An S-curve is characterized by 7 phases:
//
//  1. Maximum jerk (j = jmax).
//  2. Zero jerk, constant acceleration (j = 0).
//  3. Minimum jerk, (j = -jmax).
//  4. Coasting at constant velocity (j=0).
//
// Phases 5, 6 and 7 are mirror images of phases 3, 2, 1, respectively.
func computeSCurve(smax, vmax, amax, jmax uint, tps uint) [7]scurvePhase {
	// Set the minimum number of ticks to avoid short segments. Short
	// segments lead to larger errors in kinetic value calculations.
	const minTicks = 50

	smaxf := float32(smax)
	vmaxf := float32(vmax)
	amaxf := float32(amax)
	jmaxf := float32(jmax)

	// The relation between position, velocity, acceleration and jerk is
	// given by
	//
	//  s(t) = s0 + v*t + a/2*t² + j/6*t³
	//
	// There are 3 phases with +jmax, 0, -jmax respectively. Denoting
	// the phase durations t1, t2, t3, the acceleration after phase 3 is
	//
	//  a_ph3 = jmax*t1-jmax*t3
	//
	// Since the acceleration must be 0 in the coasting phase,
	//
	//  a_ph3 = 0 => t3 = t1
	//
	// The duration of phase 1, t1, is limited by amax:
	//
	//  a_ph1 = jmax*t1, aph1 <= amax => t1 <= amax/jmax
	t1amax := amaxf / jmaxf

	// t1 is further limited by vmax. Assuming t2 is zero, the
	// velocity after phase 3 is
	//
	//  v_ph3 = jmax*t1² => t1 <= √(vmax/jmax)
	t1vmax := float32(math.Sqrt(float64(vmaxf / jmaxf)))

	// Finally, t1 is limited by half the distance, smax/2.
	// The displacement after phase 3 is given by
	//
	//  s_ph3 = jmax*t1³ => t1 <= ∛(1/2*smax/jmax)
	halfDist := 1. / 2 * smaxf
	t1smax := float32(math.Cbrt(float64(halfDist / jmaxf)))

	// t1f and t3 are now known, and respects machine limits.
	t1f := min(t1smax, t1vmax, t1amax)

	var t2vmax, t2smax float32
	if t1f != 0 {
		// The duration of phase 2, t2, is limited by vmax. The velocity after
		// phase 3 is given by
		//
		//  v_ph3 = jmax*t1² + jmax*t1*t2 => t2 <= (vmax - jmax*t1²)/(jmax*t1)
		t2vmax = (vmaxf - jmaxf*t1f*t1f) / (jmaxf * t1f)

		// t2 is also limited by smax/2:
		//
		//  s_ph3 = jmax*t1³ + 3/2*jmax*t1²*t2 + 1/2*jmax*t1*t2²
		//   => t2 <= (-3*jmax*t1² + √(jmax^2*t1^4 + 4*jmax*smax*t1))/(2*jmax*t1)
		//   => t2 <= -3/2*t1 + √(1/4*t1² + smax/(jmax*t1))
		if t1f != 0 {
			t2smax = -3./2*t1f + float32(math.Sqrt(float64(1./4*t1f*t1f+smaxf/(jmaxf*t1f))))
		}
	}

	// Clamp phase 2 duration to 0 to avoid round-off error.
	t2f := max(0, min(t2vmax, t2smax))

	// Knowing the jerk and duration of every phase,
	// compute the distance and velocities by integration.
	sph0 := physState{}
	sph1 := sph0.Simulate(t1f, +jmaxf)
	sph2 := sph1.Simulate(t2f, 0)
	sph3 := sph2.Simulate(t1f, -jmaxf)
	// Coasting phase 4 is the remaining distance.
	sph4 := max(0, smaxf-sph3.s*2)
	// Phase 4 velocity is vmax by construction (otherwise its distance is zero).
	t4f := sph4 / vmaxf
	type controlPoint struct {
		t float32
		s physState
	}
	tpsf := float32(tps)
	t1 := uint(t1f*tpsf + .5)
	t2 := uint(t2f*tpsf + .5)
	t4 := uint(t4f*tpsf + .5)
	ctrls := make([]controlPoint, 4)
	var nctrls int
	switch {
	case t4 > minTicks && t2 > minTicks:
		nctrls = copy(ctrls, []controlPoint{{t1f, sph0}, {t2f, sph1}, {t1f, sph2}, {t4f, sph3}})
	case t4 > minTicks:
		nctrls = copy(ctrls, []controlPoint{{t1f, sph0}, {t1f, sph2}, {t4f, sph3}})
	case t2 > minTicks:
		nctrls = copy(ctrls, []controlPoint{{t1f, sph0}, {t2f, sph1}, {t1f, sph2}, {t1f, sph3}})
	default:
		nctrls = copy(ctrls, []controlPoint{{t1f, sph0}, {t1f, sph2}, {t1f, sph3}})
	}
	ctrls = ctrls[:nctrls]
	spline := make([]uint, 4)[:nctrls-1]
	// Compute control points for phases 1-3.
	for i, s0 := range ctrls[:len(ctrls)-1] {
		s1 := ctrls[i+1]
		// Use polar coordinates to compute the knot control point
		// from the two middle control points of the Bézier segment
		// implied by the position and velocity of the two states
		// s0 and s1.
		p001 := s0.s.s + s0.s.v*s0.t/3
		p011 := s1.s.s - s1.s.v*s0.t/3
		p012 := (p011-p001)*s1.t/s0.t + p011
		spline[i] = uint(p012 + .5)
	}
	switch {
	case t4 > minTicks && t2 > minTicks:
		return [...]scurvePhase{
			{t1, spline[0]},
			{t2, spline[1]},
			{t1, spline[2]},
			{t4, smax - spline[2]},
			{t1, smax - spline[1]},
			{t2, smax - spline[0]},
			{t1, smax},
		}
	case t4 > minTicks:
		return [...]scurvePhase{
			{t1, spline[0]},
			{},
			{t1, spline[1]},
			{t4, smax - spline[1]},
			{t1, smax - spline[0]},
			{},
			{t1, smax},
		}
	case t2 > minTicks:
		return [...]scurvePhase{
			{t1, spline[0]},
			{t2, spline[1]},
			{t1, spline[2]},
			{},
			{t1, smax - spline[1]},
			{t2, smax - spline[0]},
			{t1, smax},
		}
	default:
		return [...]scurvePhase{
			{t1, spline[0]},
			{},
			{t1, spline[1]},
			{},
			{t1, smax - spline[0]},
			{},
			{t1, smax},
		}
	}
}

// physState models physical properties for simulating the
// movement of the engraving needle in one dimension.
type physState struct {
	// s is the position.
	s float32
	// v is the velocity.
	v float32
	// a is the acceleration.
	a float32
}

// Simulate advance time by t at jerk j.
func (p *physState) Simulate(t, j float32) physState {
	jt := j * t
	jtt := jt * t
	jttt := jtt * t
	at := p.a * t
	return physState{
		a: p.a + jt,
		v: p.v + at + jtt/2,
		s: p.s + p.v*t + at*t/2 + jttt/6,
	}
}

func PlanEngraving(conf StepperConfig, e Engraving) bspline.Curve {
	const maxSplineKnots = 100

	knotBuf := make([]bspline.Knot, 0, maxSplineKnots)
	return planEngraving(knotBuf, conf, e)
}

// planEngraving is like PlanEngraving but avoids garbage from the knot buffer on
// TinyGo.
func planEngraving(knotBuf []bspline.Knot, conf StepperConfig, e Engraving) bspline.Curve {
	return func(yield func(bspline.Knot) bool) {
		var ts timeScaler
		start := bspline.Knot{}
		spline := knotBuf[:0]
		// Initialize the spline with 2 clamping knots at (0, 0).
		spline = append(spline, start, start)
		periodic := false
		for c := range e {
			if k, ok := c.AsKnot(); ok {
				periodic = periodic || k.Periodic
				for range k.Multiplicity {
					spline = append(spline, bspline.Knot{Engrave: k.Engrave, Ctrl: k.Knot})
					if len(spline) < 5 {
						continue
					}
					n := len(spline)
					k0, k1, k2 := spline[n-3].Ctrl, spline[n-2].Ctrl, spline[n-1].Ctrl
					if clamped := k0 == k1 && k1 == k2; !clamped {
						continue
					}

					engrave := spline[2].Engrave
					// Line segments have a closed form solution for
					// time minimal traversal.
					if len(spline) == 5 {
						s, e := spline[1].Ctrl, spline[3].Ctrl
						spline = appendLine(spline[:0], conf, engrave, s, e)
					} else if !(periodic && planPeriodicRun(spline, conf, engrave)) &&
						!planConstantRun(spline, conf, engrave) {
						for i := range spline[2 : len(spline)-2] {
							spline[i+2].T = 1
						}
						maxv, maxa, maxj := bspline.ComputeKinematics(spline, 1)
						tscale := timeScale(conf, engrave, maxv, maxa, maxj)
						for i := range spline[2 : len(spline)-2] {
							spline[i+2].T = tscale
						}
					}
					periodic = false
					dur := uint(0)
					for _, k := range spline[2:] {
						dur += k.T
					}
					if ts.Done() {
						ts.Reset(dur, dur)
					}
					for _, k := range spline[2:] {
						k.T = ts.Scale(k.T)
						if !yield(k) {
							return
						}
					}
					// Duplicate the last clamping knot twice to maintain
					// clamping start knots.
					spline = append(spline[:0], spline[len(spline)-2:]...)
				}
			} else if d, n, ok := c.AsDelay(); ok {
				if len(spline) > 3 {
					panic("delay during spline")
				}
				ts.Reset(n, d)
			}
		}
	}
}

// timeScaler precisely scales spline segment durations by
// a rational fraction.
type timeScaler struct {
	// nom/denom is the scaling fraction.
	nom, denom uint
	// frac2 accumulates twice the fractional ticks.
	frac2 uint
	// rem is the remaining ticks at nom/denom speed.
	rem uint
}

func (s *timeScaler) Reset(n, d uint) {
	if n < d {
		panic("invalid scale")
	}
	if s.rem > 0 {
		panic("scale already in effect")
	}
	s.rem = n
	s.nom, s.denom = n, d
	// Round to nearest tick by adding denom/2 ticks.
	s.frac2 = d
}

func (s *timeScaler) Scale(t uint) uint {
	var scaled uint
	if s.denom != 0 {
		frac2_64 := uint64(s.frac2) + uint64(2*s.nom)*uint64(t)
		d2 := uint64(2 * s.denom)
		d, r := frac2_64/d2, frac2_64%d2
		scaled = uint(d)
		s.frac2 = uint(r)
	} else {
		// Special case: a 0-length spline.
		scaled = s.rem
	}

	if scaled > s.rem {
		panic("unaligned delay")
	}
	s.rem -= scaled
	return scaled
}

func (s *timeScaler) Done() bool {
	return s.rem == 0
}

func appendLine(spline []bspline.Knot, conf StepperConfig, engrave bool, s, e bezier.Point) []bspline.Knot {
	tps := conf.TicksPerSecond
	vlim := conf.Speed
	if engrave {
		vlim = conf.EngravingSpeed
	}
	jlim := conf.Jerk
	alim := conf.Acceleration
	dist := uint(ManhattanDist(s, e))
	sc := computeSCurve(dist, vlim, alim, jlim, tps)
	knots := make([]bspline.Knot, len(sc))
	// Starting knot.
	nknots := 0
	for _, p := range sc {
		if p.Duration == 0 {
			continue
		}
		// Interpolate between endpoints.
		s64, e64 := bezier.P64(s), bezier.P64(e)
		ip := e64.Mul(int(p.Position)).Add(s64.Mul(int(dist - p.Position))).Div(int(dist)).Point()
		knots[nknots] = bspline.Knot{
			Ctrl:    ip,
			T:       p.Duration,
			Engrave: engrave,
		}
		nknots++
	}
	knots = knots[:nknots]
	start := bspline.Knot{Ctrl: s, Engrave: engrave}
	end := bspline.Knot{Ctrl: e, Engrave: engrave}
	spline = append(spline, start, start)
	spline = append(spline, knots...)
	if len(knots) == 0 {
		spline = append(spline, start)
	}
	spline = append(spline, end, end)
	return spline
}

// planPeriodicRun times a buffered periodic contour run, reporting
// whether it applied. The buffer holds the seam-clamped insertion
// polygon of a closed loop,
//
//	K, K, B2, d1, …, dn-1, B1, K, K, K
//
// whose interior traces the loop's periodic polygon exactly. The
// cruise pace comes from the loop's cyclic kinematics: measured
// across the seam wrap, the clamp boundary contributes no phantom
// rest-to-cruise spike, so the loop cruises as fast as its interior
// allows. The needle still enters and leaves the loop at rest at the
// seam clamp; the head and tail spans stretch along a jerk-limited
// s-curve so the boundary velocities ramp within the machine limits.
// Every stretched span is slower than its cruise pace and the
// interior stays uniform, keeping the windowed kinematics model
// valid; a final gate re-measures the timed run and rejects it,
// falling back to clamped pacing, if it exceeds the limits.
//
// The ramps are also priced against the clamped fallback: a loop
// enters and leaves at rest either way, and on short tight loops
// (text-size glyph bowls) the rest-to-rest ramps cost more time than
// the seam they remove, while the smooth insertion polygon already
// de-spikes the clamped measurement. Cyclic pacing applies only when
// its cost over the clamped plan is noise; past ~3% the clamped
// plan, whose seam behaves like every clamped stroke the machine has
// always engraved, wins.
func planPeriodicRun(spline []bspline.Knot, conf StepperConfig, engrave bool) bool {
	// Below minSpans the ramps cannot spread and cyclic pacing gains
	// nothing over the clamped run.
	const minSpans = 16
	lo, hi := 2, len(spline)-3
	nspans := hi - lo + 1
	if nspans < minSpans {
		return false
	}
	// The clamped fallback's duration, for pricing the ramps.
	for i := range spline[2 : len(spline)-2] {
		spline[i+2].T = 1
	}
	cv, ca, cj := bspline.ComputeKinematics(spline, 1)
	clamped := uint(nspans) * timeScale(conf, engrave, cv, ca, cj)
	maxv, maxa, maxj := cyclicKinematics(spline[2 : len(spline)-3])
	tc := timeScale(conf, engrave, maxv, maxa, maxj)
	if tc == 0 {
		return false
	}
	chord := func(k int) uint {
		return uint(ManhattanDist(spline[k].Ctrl, spline[k-1].Ctrl))
	}
	var length uint
	for k := lo; k <= hi; k++ {
		length += chord(k)
	}
	if length == 0 {
		return false
	}
	tps := conf.TicksPerSecond
	// The coasting speed implied by the cruise pace, in steps/s.
	vc := float32(length) / float32(nspans) * float32(tps) / float32(tc)
	prof, ok := newRampProfile(float32(length), vc, float32(conf.Acceleration), float32(conf.Jerk))
	if !ok {
		return false
	}
	// Stretch the head and tail spans along the ramp profile; the
	// profile is symmetric, so distances from the loop end map to the
	// same times. The ramp needs at most half the loop by
	// construction, so the passes meet without overlapping. No span
	// runs shorter than the cruise duration: an interval below tc on
	// a short chord is slower in speed, but the windowed kinematics
	// model reads mixed windows as if their pace applied to full
	// chords; keeping every interval at or above tc caps every window
	// read at the pace the cyclic measurement approved.
	ramp := func(from, to, dir int) int {
		var cum uint
		var prev float32
		k := from
		for ; k != to+dir; k += dir {
			cum += chord(k)
			t := prof.timeAt(float32(cum))
			dt := max(t-prev, 0)
			spline[k].T = max(uint(float64(dt)*float64(tps)+0.5), tc)
			prev = t
			if float32(cum) >= prof.sAcc {
				break
			}
		}
		return k
	}
	first := ramp(lo, hi, +1) + 1
	last := hi
	if first <= hi {
		last = ramp(hi, first, -1) - 1
	}
	for k := first; k <= last; k++ {
		spline[k].T = tc
	}
	// Price the ramps against the clamped fallback.
	var dur uint
	for k := lo; k <= hi; k++ {
		dur += spline[k].T
	}
	if dur > clamped+clamped/32 {
		return false
	}
	// Gate: the construction keeps every span at or below its cruise
	// pace, so exceeding a limit means degenerate geometry.
	v, a, j := bspline.ComputeKinematics(spline, tps)
	limv := conf.Speed
	if engrave {
		limv = conf.EngravingSpeed
	}
	return v <= limv && a <= conf.Acceleration && j <= conf.Jerk
}

// planConstantRun times a buffered smooth stroke at a constant cruise
// pace, reporting whether it applied. The buffer holds an open
// rest-to-rest run,
//
//	K, K, d1, …, dn, K, K, K
//
// which today is paced uniformly by its worst derivative window: the
// rest clamps' boundary spike reads as if it applied to the whole
// stroke, so every smooth stroke crawls and dot pitch wanders with
// the geometry. Instead, the cruise pace comes from the interior
// windows alone — on the sampler's uniform chords, the engraving
// speed limit unless curvature demands less — and the head and tail
// spans stretch along the same jerk-limited ramp profile the periodic
// loops use, so the boundary velocities ramp within the machine
// limits instead of pacing the interior.
//
// Every stretched span is slower than the cruise pace and the
// interior stays uniform, keeping the windowed kinematics model valid
// (the planPeriodicRun argument); a final gate re-measures the timed
// run and yields to the uniform fallback when it exceeds the limits.
// The ramps are also priced against that fallback: on short strokes
// they cost more than the boundary spike they remove, and past ~3%
// the fallback wins.
func planConstantRun(spline []bspline.Knot, conf StepperConfig, engrave bool) bool {
	// Below minSpans there is no interior to measure and the ramps
	// cannot spread.
	const minSpans = 8
	lo, hi := 2, len(spline)-3
	nspans := hi - lo + 1
	if nspans < minSpans {
		return false
	}
	n := len(spline)
	// The uniform fallback's duration, for pricing.
	for i := range spline[2 : n-2] {
		spline[i+2].T = 1
	}
	uv, ua, uj := bspline.ComputeKinematics(spline, 1)
	tscaleU := timeScale(conf, engrave, uv, ua, uj)
	uniform := uint(nspans) * tscaleU

	// Measure the interior windows at unit pace. The boundary windows
	// (the first 5, straddling the start clamps and warmup, and the
	// last 2) read the rest-clamp spike; the ramps govern those spans
	// instead. Only ink windows set the cruise: windows touching
	// needle-up knots trace the teardrop bridges of flying
	// transitions, which the windowed model over-reads; their safety
	// is the emitter's curvature-envelope construction (rminFly), not
	// this measurement.
	var kin bspline.Kinematics
	var iv, ia, ij uint
	var up [4]bool
	widx := 0
	for _, k := range spline {
		kin.Knot(k.T, k.Ctrl, 1)
		kv, ka, kj := kin.Max()
		copy(up[:3], up[1:])
		up[3] = !k.Engrave
		widx++
		if widx > 5 && widx <= n-2 && !(up[0] || up[1] || up[2] || up[3]) {
			iv, ia, ij = max(iv, kv), max(ia, ka), max(ij, kj)
		}
	}
	tc := timeScale(conf, engrave, iv, ia, ij)
	if tc == 0 {
		return false
	}
	chord := func(k int) uint {
		return uint(ManhattanDist(spline[k].Ctrl, spline[k-1].Ctrl))
	}
	var length uint
	for k := lo; k <= hi; k++ {
		length += chord(k)
	}
	if length == 0 {
		return false
	}
	tps := conf.TicksPerSecond
	// The coasting speed implied by the cruise pace, in steps/s.
	vc := float32(length) / float32(nspans) * float32(tps) / float32(tc)
	prof, ok := newRampProfile(float32(length), vc, float32(conf.Acceleration), float32(conf.Jerk))
	if !ok {
		return false
	}
	// Stretch the head and tail spans along the ramp profile; the
	// profile is symmetric, so distances from the stroke end map to
	// the same times. No span runs shorter than the cruise duration
	// (the planPeriodicRun argument), and needle-up dips keep their
	// slower local pace.
	ramp := func(from, to, dir int) int {
		var cum uint
		var prev float32
		k := from
		for ; k != to+dir; k += dir {
			cum += chord(k)
			t := prof.timeAt(float32(cum))
			dt := max(t-prev, 0)
			spline[k].T = max(uint(float64(dt)*float64(tps)+0.5), spline[k].T, tc)
			prev = t
			if float32(cum) >= prof.sAcc {
				break
			}
		}
		return k
	}
	first := ramp(lo, hi, +1) + 1
	last := hi
	if first <= hi {
		last = ramp(hi, first, -1) - 1
	}
	for k := first; k <= last; k++ {
		spline[k].T = max(spline[k].T, tc)
	}
	// The profile maps to whole spans, and at glyph scale the ramp
	// distance is shorter than a chord or two: the mapped ramp ends in
	// a cliff from a slow span straight to cruise, a real jerk
	// violation in the blended spline. Taper the cliff by bounding
	// adjacent span-time ratios, spreading the ramp inward; the taper
	// only ever slows spans down.
	for k := lo + 1; k <= hi; k++ {
		if spline[k].T*5 < spline[k-1].T*4 {
			spline[k].T = spline[k-1].T * 4 / 5
		}
	}
	for k := hi - 1; k >= lo; k-- {
		if spline[k].T*5 < spline[k+1].T*4 {
			spline[k].T = spline[k+1].T * 4 / 5
		}
	}
	// The blend at a needle flip crosses between stroke and bridge
	// geometry over a full window of knots; ease the spans around
	// each flip so the junction trace stays within the stroke's own
	// envelope.
	ease := func() {
		for k := lo + 1; k <= hi; k++ {
			if spline[k].Engrave != spline[k-1].Engrave {
				for e := max(k-2, lo); e <= min(k+1, hi); e++ {
					spline[e].T = max(spline[e].T, tc+tc/8)
				}
			}
		}
	}
	ease()
	mixed := false
	for k := lo; k <= hi; k++ {
		mixed = mixed || !spline[k].Engrave
	}
	// Needle-up spans are held to the absolute limits, and the
	// blended polygon of a teardrop traces harder than the arc it
	// samples. Slow the bridges by the measured excess (free of dot
	// pitch) and re-ease; the final gate re-judges the result. Pure
	// ink runs skip the measurement entirely.
	if mixed {
		_, _, _, vup, aup, jup := tracedFlagMaxima(spline, tps, false)
		const uslack = 63. / 64
		f := max(1,
			float64(vup)/(float64(conf.Speed)*uslack),
			math.Sqrt(float64(aup)/(float64(conf.Acceleration)*uslack)),
			math.Cbrt(float64(jup)/(float64(conf.Jerk)*uslack)))
		if f > 1 {
			for k := lo; k <= hi; k++ {
				if !spline[k].Engrave {
					spline[k].T = uint(float64(spline[k].T)*f + 1)
				}
			}
			ease()
		}
	}
	// Price against the uniform fallback. Mixed runs have no
	// meaningful uniform price: their baseline paces the whole run by
	// bridge windows the model over-reads, and the emitter already
	// priced the flight against its stop-and-go alternative, so only
	// the gate judges them.
	if !mixed {
		var dur uint
		for k := lo; k <= hi; k++ {
			dur += spline[k].T
		}
		if dur > uniform+uniform/32 {
			return false
		}
	}
	// Gate: the windowed model chose the paces, the traced curve
	// decides them. Non-uniform spans (ramps, tapers, bridges) blend
	// into real polynomials the windowed divided differences can
	// under-read. The bar is the uniform fallback: the machine has
	// always engraved uniform-paced runs whose boundary windows trace
	// past the nominal limits (the windowed model's deliberate
	// clamp-artifact deflation, bench-validated), so an applied plan
	// must trace no worse than the uniform plan of the same run, with
	// the nominal limits as the floor. Needle-up bridges are new
	// geometry with no such precedent and stay absolutely limited.
	limv := float32(conf.Speed)
	if engrave {
		limv = float32(conf.EngravingSpeed)
	}
	lima, limj := float32(conf.Acceleration), float32(conf.Jerk)
	// The blend between unequal spans overshoots the per-span rates
	// by a fraction of a percent; a 65/64 floor is noise against the
	// boundary envelope the uniform baseline already carries.
	const slack = 65. / 64
	for range 3 {
		vi, ai, ji, vu, au, ju := tracedFlagMaxima(spline, conf.TicksPerSecond, false)
		v1, a1, j1, _, _, _ := tracedFlagMaxima(spline, conf.TicksPerSecond, true)
		sc := float32(conf.TicksPerSecond) / float32(tscaleU)
		f := max(
			vi/max(limv*slack, v1*sc),
			float32(math.Sqrt(float64(ai/max(lima*slack, a1*sc*sc)))),
			float32(math.Cbrt(float64(ji/max(limj*slack, j1*sc*sc*sc)))),
			vu/(float32(conf.Speed)*slack),
			float32(math.Sqrt(float64(au/(lima*slack)))),
			float32(math.Cbrt(float64(ju/(limj*slack)))))
		if f <= 1 {
			return true
		}
		if !mixed {
			return false
		}
		// A mixed run has no safe fallback: the windowed model cannot
		// see its bridges, so uniform re-pacing may command teardrops
		// far past the limits. Scale the whole run by the measured
		// excess instead; a uniform time scale shrinks the traced
		// curve exactly, so one pass converges.
		for k := lo; k <= hi; k++ {
			spline[k].T = uint(float32(spline[k].T)*f + 1)
		}
	}
	// Unreachable while the scaling law holds; take the safe
	// direction regardless.
	for k := lo; k <= hi; k++ {
		spline[k].T *= 2
	}
	return true
}

// tracedFlagMaxima measures a timed run's traced per-axis kinematic
// maxima on the polynomials the stepper interpolates, ink and
// needle-up segments separately. With unit set, the run's geometry is
// measured at one tick per span instead of its pacing: scaling those
// maxima by (tps/tscale)^k gives the exact traced kinematics of the
// uniform fallback, since every knot interval scales together.
// Single precision throughout: the device has no double-precision
// FPU, and the gates carry percent-scale slack.
func tracedFlagMaxima(spline []bspline.Knot, tps uint, unit bool) (vi, ai, ji, vu, au, ju float32) {
	abs := func(x float32) float32 {
		if x < 0 {
			return -x
		}
		return x
	}
	var seg bspline.Segment
	for _, k := range spline {
		if unit && k.T > 0 {
			k.T = 1
		}
		c, dt, engrave := seg.Knot(k)
		if dt == 0 {
			continue
		}
		T := float32(dt)
		if !unit {
			T /= float32(tps)
		}
		d1 := [3]bezier.Point{
			c.C1.Sub(c.C0).Mul(3),
			c.C2.Sub(c.C1).Mul(3),
			c.C3.Sub(c.C2).Mul(3),
		}
		// Per axis, velocity is quadratic in u (exact extremum in
		// closed form) and acceleration is linear (extremes at the
		// endpoints); no sampling needed.
		var v, a float32
		for axis := 0; axis < 2; axis++ {
			A := float32(d1[0].X)
			B := float32(d1[1].X)
			C := float32(d1[2].X)
			if axis == 1 {
				A, B, C = float32(d1[0].Y), float32(d1[1].Y), float32(d1[2].Y)
			}
			v = max(v, abs(A), abs(C))
			if den := A - 2*B + C; den != 0 {
				if u := (A - B) / den; u > 0 && u < 1 {
					mu := 1 - u
					v = max(v, abs(A*mu*mu+2*B*mu*u+C*u*u))
				}
			}
			a = max(a, 2*abs(B-A), 2*abs(C-B))
		}
		v /= T
		a /= T * T
		j := max(
			abs(6*float32(c.C3.X-3*c.C2.X+3*c.C1.X-c.C0.X)/(T*T*T)),
			abs(6*float32(c.C3.Y-3*c.C2.Y+3*c.C1.Y-c.C0.Y)/(T*T*T)))
		if engrave {
			vi, ai, ji = max(vi, v), max(ai, a), max(ji, j)
		} else {
			vu, au, ju = max(vu, v), max(au, a), max(ju, j)
		}
	}
	return
}

// cyclicKinematics measures the uniform-pace kinematic maxima of a
// closed control polygon, the derivative windows wrapping across the
// seam. The polygon must have at least 7 knots.
func cyclicKinematics(loop []bspline.Knot) (v, a, j uint) {
	var kin bspline.Kinematics
	n := len(loop)
	// Warm the derivative chain over the wrap-around tail, then
	// measure one full cycle.
	for i := n - 6; i < n; i++ {
		kin.Knot(1, loop[i].Ctrl, 1)
	}
	for _, k := range loop {
		kin.Knot(1, k.Ctrl, 1)
		kv, ka, kj := kin.Max()
		v, a, j = max(v, kv), max(a, ka), max(j, kj)
	}
	return v, a, j
}

// rampProfile is the jerk-limited velocity profile a periodic run
// follows along its arc length: accelerate from rest through
// constant-jerk and constant-acceleration phases, coast, and mirror
// down to rest, like the s-curve of a straight line.
type rampProfile struct {
	// t1, t2 are the constant-jerk and constant-acceleration phase
	// durations; tAcc, sAcc the full ramp duration and distance.
	t1, t2, tAcc, sAcc float32
	// ph and pht are the start state and time of the three
	// acceleration phases.
	ph   [3]physState
	pht  [3]float32
	jmax float32
	// vc is the coasting speed; tTot, sTot the loop totals.
	vc, tTot, sTot float32
}

func newRampProfile(length, vc, amax, jmax float32) (rampProfile, bool) {
	t1 := min(
		amax/jmax,
		float32(math.Sqrt(float64(vc/jmax))),
		float32(math.Cbrt(float64(length/2/jmax))),
	)
	var t2 float32
	if t1 > 0 {
		t2v := (vc - jmax*t1*t1) / (jmax * t1)
		t2s := -3./2*t1 + float32(math.Sqrt(float64(1./4*t1*t1+length/(jmax*t1))))
		t2 = max(0, min(t2v, t2s))
	}
	var p rampProfile
	p.t1, p.t2, p.jmax = t1, t2, jmax
	p.pht[1] = t1
	p.ph[1] = p.ph[0].Simulate(t1, jmax)
	p.pht[2] = t1 + t2
	p.ph[2] = p.ph[1].Simulate(t2, 0)
	end := p.ph[2].Simulate(t1, -jmax)
	p.tAcc, p.sAcc, p.vc = 2*t1+t2, end.s, end.v
	if !(end.v > 0) {
		return p, false
	}
	p.tTot = 2*p.tAcc + max(0, length-2*end.s)/end.v
	p.sTot = length
	return p, true
}

// timeAt inverts the profile: the time at which arc distance s is
// reached.
func (p *rampProfile) timeAt(s float32) float32 {
	if s <= 0 {
		return 0
	}
	if s >= p.sTot {
		return p.tTot
	}
	if s > p.sTot/2 {
		return p.tTot - p.timeAt(p.sTot-s)
	}
	if s >= p.sAcc {
		return p.tAcc + (s-p.sAcc)/p.vc
	}
	// Bisect within the acceleration phase containing s: position
	// grows monotonically with time.
	i, dur, j := 0, p.t1, p.jmax
	switch {
	case s >= p.ph[2].s:
		i, dur, j = 2, p.t1, -p.jmax
	case s >= p.ph[1].s:
		i, dur, j = 1, p.t2, 0
	}
	st := p.ph[i]
	lo, hi := float32(0), dur
	for range 24 {
		mid := (lo + hi) / 2
		if st.s+st.v*mid+st.a*mid*mid/2+j*mid*mid*mid/6 < s {
			lo = mid
		} else {
			hi = mid
		}
	}
	return p.pht[i] + (lo+hi)/2
}

// timeScale computes the minimum time in ticks to traverse c given
// limits.
func timeScale(c StepperConfig, engrave bool, v, a, j uint) uint {
	limv := c.Speed
	if engrave {
		limv = c.EngravingSpeed
	}
	lima, limj := c.Acceleration, c.Jerk
	// Compute the scale required by the velocity limit.
	// Velocity is propertional to the scale.
	tv := float32(v) / float32(limv)
	// Acceleration is proportional to the square of the scale.
	ta := float32(math.Sqrt(float64(float32(a) / float32(lima))))
	// Jerk by the cube.
	tj := float32(math.Cbrt(float64(float32(j) / float32(limj))))
	tps := float32(c.TicksPerSecond)
	scale := float32(math.Ceil(float64(max(0, tv, ta, tj) * tps)))
	return uint(scale)
}

// timeConstantPath computes the engraving time in ticks along
// with the start and end points.
func timeConstantPath(s bspline.Curve) constantPlan {
	engraving := false
	var inf constantPlan
	var seg bspline.Segment
	for k := range s {
		c, ticks, engrave := seg.Knot(k)
		switch {
		case !engraving && engrave:
			inf.Start = inf.End
			engraving = true
		case engraving && !engrave:
			panic("broken path")
		}
		if engrave {
			inf.Duration += ticks
		}
		inf.End = c.C3
	}
	return inf
}

func TimePlan(conf StepperConfig, p Engraving) time.Duration {
	ticks := uint(0)
	for k := range PlanEngraving(conf, p) {
		ticks += k.T
	}
	s := (ticks + conf.TicksPerSecond - 1) / conf.TicksPerSecond
	return time.Duration(s) * time.Second
}

func timeMove(conf StepperConfig, dist int) uint {
	sc := computeSCurve(uint(dist), conf.Speed, conf.Acceleration, conf.Jerk, conf.TicksPerSecond)
	t := uint(0)
	for _, s := range sc {
		t += s.Duration
	}
	return t
}

func NewConstantStringer(face *vector.Face, params Params, em int) *ConstantStringer {
	var bounds bspline.Bounds
	var adv int
	var maxDur uint
	m := face.Metrics()
	fh := m.Height
	conf := params.StepperConfig
	runes := make([]constantRune, 0, len(constantAlphabet))
	var lastr rune
	const maxSplineKnots = 100

	knotBuf := make([]bspline.Knot, 0, maxSplineKnots)
	// Compute engraving durations for the alphabet.
	for i, r := range constantAlphabet {
		if r < lastr {
			panic("unsorted alphabet")
		}
		lastr = r
		a, spline, found := face.Decode(r)
		if !found {
			panic(fmt.Errorf("unsupported rune: %s", string(r)))
		}
		if i > 0 && adv != a {
			panic("variable width font")
		}
		adv = a
		inf := timeConstantPath(planEngraving(knotBuf, conf, func(yield func(c Command) bool) {
			engraveSpline(yield, bezier.Point{}, em, fh, spline)
		}))
		bounds.Min.X = min(bounds.Min.X, inf.Start.X, inf.End.X)
		bounds.Min.Y = min(bounds.Min.Y, inf.Start.Y, inf.End.Y)
		bounds.Max.X = max(bounds.Max.X, inf.Start.X, inf.End.X)
		bounds.Max.Y = max(bounds.Max.Y, inf.Start.Y, inf.End.Y)
		runes = append(runes, constantRune{
			R:    r,
			Info: inf,
		})
		maxDur = max(inf.Duration, maxDur)
	}
	startEndDist := ManhattanDist(bounds.Min, bounds.Max)
	center := bounds.Max.Add(bounds.Min).Div(2)

	return &ConstantStringer{
		face:         face,
		runeDuration: maxDur,
		alphabet:     runes,
		em:           em,
		center:       center,
		startEndDist: startEndDist,
		conf:         params.StepperConfig,
		advDist:      adv * em / fh,
	}
}

func (c *ConstantStringer) String(yield func(Command) bool, txt string) bool {
	n := strlen(txt)
	return c.PaddedString(yield, txt, n, n)
}

func (c *ConstantStringer) PaddedString(yield func(Command) bool, txt string, shortest, longest int) bool {
	if n := strlen(txt); n < shortest || longest < n {
		panic("string length out of bounds")
	}
	return c.paddedString(yield, txt, shortest, longest)
}

func (c *ConstantStringer) paddedString(yield func(Command) bool, txt string, shortest, longest int) bool {
	f := c.face
	m := f.Metrics()
	fh := m.Height
	baseline := (m.Ascent*c.em + fh - 1) / fh
	dot := bezier.Pt(0, baseline)
	// Move to the data-independent start position.
	pen := dot.Add(c.center)
	cont := yield(Move(pen))
	// Compute worst case movement durations.
	padDur := timeMove(c.conf, ((longest-shortest)*c.advDist+1)/2+c.startEndDist)
	advDur := timeMove(c.conf, c.advDist+c.startEndDist)
	centerDur := timeMove(c.conf, (c.startEndDist+1)/2)
	totalDur := centerDur
	// accum accumulates the fraction each rune in txt
	// contributes towards engraving the total number of runes
	// (longest). This is to spread out the repeat runes.
	accum := 0
	ridx := 0
	for range longest {
		r, n := utf8.DecodeRuneInString(txt[ridx:])
		idx, found := sort.Find(len(c.alphabet), func(i int) int {
			return int(r - c.alphabet[i].R)
		})
		if !found {
			panic(fmt.Errorf("unsupported rune: %s", string(r)))
		}
		_, spline, found := f.Decode(r)
		if !found {
			// Unreachable by construction, since c.alphabet contains
			// only runes from f.
			panic("unreachable")
		}
		inf := c.alphabet[idx].Info
		// Skip starting move segment.
		if inf.Start != (bezier.Point{}) {
			for range 3 {
				if _, ok := spline.Next(); !ok {
					panic("unclamped spline")
				}
			}
		}
		start := dot.Add(inf.Start)
		cont = cont && DelayMove(yield, c.conf, totalDur, pen, start) &&
			yield(Delay(inf.Duration, c.runeDuration)) &&
			engraveSpline(yield, dot, c.em, fh, spline)
		pen = dot.Add(inf.End)
		totalDur = advDur
		accum += len(txt)
		if accum >= longest {
			accum -= longest
			ridx += n
			dot.X += c.advDist
		}
	}
	// Move to end, the midpoint between shortest and longest.
	mid2 := longest + shortest - 1
	dot = bezier.Pt(mid2*c.advDist/2, baseline)
	end := dot.Add(c.center)
	cont = cont && DelayMove(yield, c.conf, padDur, pen, end)
	return cont
}

func String(face *vector.Face, em int, txt string) *StringCmd {
	return &StringCmd{
		LineHeight: 1,
		face:       face,
		em:         em,
		txt:        txt,
	}
}

type StringCmd struct {
	LineHeight int

	face *vector.Face
	em   int
	txt  string
	// Reused glyph decode buffers for the flying re-emission.
	raw       []glyphKnot
	nodes     []glyphNode
	fly       []bool
	spans     []glyphSpan
	scratch   []glyphKnot
	keepOrder bool
	reversed  bool
	rbuf      []rune
	xbuf      []int
}

// SourceOrder disables stroke reordering and flying transitions for
// this text: seed plates keep their baked emission untouched.
func (s *StringCmd) SourceOrder() *StringCmd {
	s.keepOrder = true
	return s
}

// Reversed keeps the layout unchanged but engraves the glyphs of each
// line right to left: a serpentine text block enters every row where
// the previous row ended instead of paying a full-width return
// travel.
func (s *StringCmd) Reversed() *StringCmd {
	s.reversed = true
	return s
}

func (s *StringCmd) Engrave(yield func(Command) bool) bool {
	if s.reversed {
		return s.engraveReversed(yield)
	}
	_, ok := s.engrave(yield)
	return ok
}

// engraveReversed emits each line's glyphs at their forward layout
// positions, in reverse order.
func (s *StringCmd) engraveReversed(yield func(Command) bool) bool {
	m := s.face.Metrics()
	mh := m.Height
	y := (m.Ascent*s.em + mh - 1) / mh
	lheight := s.em * s.LineHeight
	txt := s.txt
	for {
		seg := txt
		if i := strings.IndexByte(txt, '\n'); i >= 0 {
			seg, txt = txt[:i], txt[i+1:]
		} else {
			txt = ""
		}
		s.rbuf, s.xbuf = s.rbuf[:0], s.xbuf[:0]
		x := 0
		for _, r := range seg {
			adv, _, found := s.face.Decode(r)
			if !found {
				panic(fmt.Errorf("unsupported rune: %s", string(r)))
			}
			s.rbuf = append(s.rbuf, r)
			s.xbuf = append(s.xbuf, x)
			x += adv * s.em / mh
		}
		for i := len(s.rbuf) - 1; i >= 0; i-- {
			_, spline, _ := s.face.Decode(s.rbuf[i])
			if !s.engraveGlyph(yield, bezier.Pt(s.xbuf[i], y), mh, spline) {
				return false
			}
		}
		if txt == "" {
			return true
		}
		y += lheight
	}
}

func (s *StringCmd) Measure() (int, int) {
	b, _ := s.engrave(nil)
	return int(b.X), int(b.Y)
}

func (s *StringCmd) engrave(yield func(Command) bool) (bezier.Point, bool) {
	m := s.face.Metrics()
	mh := m.Height
	dot := bezier.Pt(0, (m.Ascent*s.em+mh-1)/mh)
	lheight := s.em * s.LineHeight
	cont := true
	for _, r := range s.txt {
		if r == '\n' {
			dot.X = 0
			dot.Y += lheight
			continue
		}
		adv, spline, found := s.face.Decode(r)
		if !found {
			panic(fmt.Errorf("unsupported rune: %s", string(r)))
		}
		if yield != nil {
			cont = cont && s.engraveGlyph(yield, dot, mh, spline)
		}
		dot.X += adv * s.em / mh
	}
	return bezier.Point{X: dot.X, Y: lheight}, cont
}

// glyphKnot is a decoded, machine-scaled glyph control point.
type glyphKnot struct {
	p        bezier.Point
	line     bool
	periodic bool
}

// glyphNode is a maximal group of coincident glyph knots: baked
// clamp anchors have count 3, polyline points count 1.
type glyphNode struct {
	p      bezier.Point
	i0, n  int
	anchor bool
}

// glyphSpan is one stroke of a glyph: a raw knot range with its
// engraving endpoints, possibly reversed by the ordering pass.
type glyphSpan struct {
	i0, i1   int
	from, to bezier.Point
	rev      bool
}

// Flying-transition envelope, in machine units of the SH2 (6400
// steps/mm). A needle-up bridge whose curvature radius stays above
// rminFly sustains the 8mm/s engraving speed within the 250mm/s²
// acceleration and 2600mm/s³ jerk limits (sqrt(v³/j) = 0.44mm,
// v²/a = 0.26mm), so a fused glyph run keeps one uniform cruise pace
// through its junctions.
const (
	rminFly = 3600
	// Pricing constants for a junction: the bridge crosses at the
	// engraving speed; the stop-and-go alternative pays two
	// stroke-end ramp pairs plus a rest-to-rest travel. A junction
	// flies only when it does not lose time, so teardrop reversals
	// (whose curvature-capped crossing is slower than stopping) keep
	// the rest stop and its resume anchor.
	flyEngraveSpeed = 8 * 6400
	flyTravelSpeed  = 30 * 6400
	// Two jerk-limited rest<->v_e stroke ramps (2·2·sqrt(v/j)) plus
	// the travel s-curve's own ramp phases, in seconds.
	flyStopOverhead = 0.42
)

// FlyingTransitions enables the experimental needle-up bridges across
// glyph-internal travels. The construction and its safety gates are
// in place, but the fly/stop decision currently lives in the emitter,
// which cannot see the planner's pricing: a fused run that fails the
// kinematics gate falls back to worst-window uniform pacing and
// engraves far slower than its stop-and-go equivalent. Off until the
// decision moves into the planner, where fused and stop-and-go plans
// can be priced against each other directly.
var FlyingTransitions = false

// engraveGlyph re-emits a glyph with needle-up bridges across its
// internal travels: instead of ramping to rest at a stroke end,
// traveling and ramping up again, the head leaves the stroke along
// its exit tangent, crosses needle-up, and arrives on the next
// stroke's start along its entry tangent at speed, the planner
// pacing the fused run as one stroke. Junctions whose bridge would
// bend below rminFly (sharp turns, reversals) and periodic glyphs
// keep the baked rest-to-rest emission; a glyph with no flyable
// junction replays its baked knots exactly.
func (s *StringCmd) engraveGlyph(yield func(Command) bool, pos bezier.Point, height int, spline vector.UniformBSpline) bool {
	s.raw = s.raw[:0]
	periodic := false
	for {
		k, ok := spline.Next()
		if !ok {
			break
		}
		periodic = periodic || k.Periodic
		s.raw = append(s.raw, glyphKnot{addScale(pos, k.Ctrl, s.em, height), k.Line, k.Periodic})
	}
	replay := func(from, to int) bool {
		for _, k := range s.raw[from:to] {
			cmd := ControlPoint(k.line, k.p)
			if k.periodic {
				cmd = PeriodicPoint(k.p)
			}
			if !yield(cmd) {
				return false
			}
		}
		return true
	}
	if periodic || s.keepOrder {
		// Periodic contours pace cyclically, and source-ordered text
		// (seed plates) keeps the baked emission; leave them alone.
		return replay(0, len(s.raw))
	}
	s.orderStrokes()
	if !FlyingTransitions {
		return replay(0, len(s.raw))
	}
	// Collapse coincident knots into nodes.
	s.nodes = s.nodes[:0]
	for i := 0; i < len(s.raw); {
		j := i
		for j < len(s.raw) && s.raw[j].p == s.raw[i].p {
			j++
		}
		s.nodes = append(s.nodes, glyphNode{s.raw[i].p, i, j - i, j-i >= 3})
		i = j
	}
	// A travel junction is a pair of adjacent anchors; it can fly
	// when a bridge from the exit tangent to the entry tangent stays
	// within the curvature envelope. fly[i] refers to the junction
	// between nodes[i] and nodes[i+1].
	s.fly = s.fly[:0]
	for range max(len(s.nodes)-1, 0) {
		s.fly = append(s.fly, false)
	}
	fly := s.fly
	cref := s.chordRef()
	var bridge []bezier.Point
	anyFly := false
	for i := 0; i+1 < len(s.nodes); i++ {
		a, b := s.nodes[i], s.nodes[i+1]
		if !a.anchor || !b.anchor || a.p == b.p ||
			i == 0 || i+2 == len(s.nodes) {
			continue
		}
		// Adjacent anchors also encode straight strokes ([P×3][Q×3]
		// with the line drawn between them); only a move run may fly.
		if s.raw[a.i0+a.n-1].line || s.raw[b.i0].line {
			continue
		}
		// Both anchors must belong to real ink: an all-move phantom
		// anchor (a rest point inside a travel) has no stroke to fly
		// from, and flying both of its junctions would mislabel the
		// hop as ink. Real anchors touch ink on their stroke side
		// (smooth entries are baked all-move but ink follows); one
		// flight per anchor.
		aInk := s.raw[a.i0].line || (a.i0 > 0 && s.raw[a.i0-1].line)
		bInk := s.raw[b.i0+b.n-1].line || (b.i0+b.n < len(s.raw) && s.raw[b.i0+b.n].line)
		if !aInk || !bInk || fly[i-1] {
			continue
		}
		// Both neighboring strokes must be long enough to absorb the
		// junction blend and its easing; a dot or serif is not.
		strokeLen := func(from, dir int) int {
			sum, k := 0, from
			for k+dir >= 0 && k+dir < len(s.nodes) && sum < 6*cref {
				sum += ManhattanDist(s.nodes[k].p, s.nodes[k+dir].p)
				k += dir
				if s.nodes[k].anchor {
					break
				}
			}
			return sum
		}
		if strokeLen(i, -1) < 6*cref || strokeLen(i+1, +1) < 6*cref {
			continue
		}
		if bridge = flyBridge(bridge[:0], s.nodes[i-1].p, a.p, b.p, s.nodes[i+2].p, cref); bridge == nil {
			continue
		}
		length, prev := 0.0, a.p
		for _, p := range bridge {
			length += math.Hypot(float64(p.X-prev.X), float64(p.Y-prev.Y))
			prev = p
		}
		length += math.Hypot(float64(b.p.X-prev.X), float64(b.p.Y-prev.Y))
		hop := math.Hypot(float64(b.p.X-a.p.X), float64(b.p.Y-a.p.Y))
		if length/flyEngraveSpeed <= flyStopOverhead+hop/flyTravelSpeed {
			fly[i] = true
			anyFly = true
		}
	}
	if !anyFly {
		return replay(0, len(s.raw))
	}
	// Emit node by node. Polyline spans in fused runs are subdivided
	// so no chord dwarfs the sampler's and the whole fused run keeps
	// a uniform chord scale.
	cmax := cref
	fused := false // the current run contains a flown junction
	// restScan reports whether the run starting at rest anchor `from`
	// contains a flown junction: the run continues through flying
	// pairs and ends at the first anchor that neither starts nor
	// arrives from a flight.
	restScan := func(from int) bool {
		for i := from + 1; i < len(s.nodes); i++ {
			if !s.nodes[i].anchor {
				continue
			}
			if i < len(fly) && fly[i] {
				return true
			}
			if !fly[i-1] {
				return false
			}
		}
		return false
	}
	last := s.nodes[0].p
	for i, n := range s.nodes {
		// Subdivide any long span of a fused run so no chord dwarfs
		// the cruise pace's chord scale: polyline spans, and straight
		// strokes encoded as bare anchor pairs. The span's ink state
		// is its destination's first baked flag; arrival hops are
		// covered by the bridge instead.
		if fused && i > 0 && !fly[i-1] {
			if d := ManhattanDist(last, n.p); d > cmax {
				line := s.raw[n.i0].line
				steps := (d + cmax - 1) / cmax
				for k := 1; k < steps; k++ {
					ip := bezier.Point{
						X: last.X + (n.p.X-last.X)*k/steps,
						Y: last.Y + (n.p.Y-last.Y)*k/steps,
					}
					if !yield(ControlPoint(line, ip)) {
						return false
					}
				}
			}
		}
		switch {
		case i > 0 && fly[i-1]:
			// Arrival: the entry point once, engrave on; the needle
			// drops during the arrival blend.
			if !yield(ControlPoint(true, n.p)) {
				return false
			}
		case n.anchor:
			// Rest anchor (glyph start/end, corner, or a travel that
			// does not fly): baked knots verbatim.
			if i+1 < len(s.nodes) && fly[i] {
				// Departure: the exit point once, engrave on, then
				// the needle-up bridge.
				if !yield(ControlPoint(true, n.p)) {
					return false
				}
				bridge = flyBridge(bridge[:0], s.nodes[i-1].p, n.p, s.nodes[i+1].p, s.nodes[i+2].p, cref)
				for _, p := range bridge {
					if !yield(ControlPoint(false, p)) {
						return false
					}
				}
			} else {
				if !replay(n.i0, n.i0+n.n) {
					return false
				}
			}
			fused = restScan(i)
		default:
			// Polyline point.
			if !replay(n.i0, n.i0+n.n) {
				return false
			}
		}
		last = n.p
	}
	return true
}

// orderStrokes reorders and orients a glyph's strokes in place to
// shorten its travels: a greedy chain from the baked entry stroke,
// picking the unvisited stroke whose nearer endpoint is closest and
// engraving it backwards when its far end is the nearer one. The
// baked order is kept unless the chain strictly shortens the total
// travel, and the first stroke stays fixed and forward so the glyph's
// entry point (and the travel from the previous glyph) is unchanged.
//
// A reversed stroke emits its knots backwards with each flag taken
// from its forward successor: a knot's flag describes the span
// arriving at it, and the arriving span of the reversal is the
// departing span of the original. The rule also mirrors the boundary
// anchors' baked roles ([M,M,L] entry for [L,L,M] exit and back).
func (s *StringCmd) orderStrokes() {
	if len(s.raw) == 0 {
		return
	}
	// Stroke spans: raw index ranges split where a move run leaves
	// one coincident anchor for another.
	s.spans = s.spans[:0]
	start := 0
	for i := 1; i < len(s.raw); i++ {
		// A travel boundary: previous knot is a move away from its
		// coincident group and this knot starts a new position.
		if !s.raw[i-1].line && !s.raw[i].line && s.raw[i-1].p != s.raw[i].p {
			s.spans = append(s.spans, glyphSpan{start, i - 1, s.raw[start].p, s.raw[i-1].p, false})
			start = i
		}
	}
	s.spans = append(s.spans, glyphSpan{start, len(s.raw) - 1, s.raw[start].p, s.raw[len(s.raw)-1].p, false})
	n := len(s.spans)
	if n < 2 {
		return
	}
	strokes, order := s.spans[:n], s.spans[n:]
	baked := 0
	for i := 1; i < n; i++ {
		baked += ManhattanDist(strokes[i-1].to, strokes[i].from)
	}
	order = append(order, strokes[0])
	strokes[0].rev = true // marks visited below
	for i := 1; i < n; i++ {
		strokes[i].rev = false
	}
	cur := strokes[0].to
	total := 0
	for range n - 1 {
		bi, brev, bd := -1, false, 0
		for i := 1; i < n; i++ {
			if strokes[i].rev {
				continue
			}
			if d := ManhattanDist(cur, strokes[i].from); bi < 0 || d < bd {
				bi, brev, bd = i, false, d
			}
			if d := ManhattanDist(cur, strokes[i].to); d < bd {
				bi, brev, bd = i, true, d
			}
		}
		st := strokes[bi]
		strokes[bi].rev = true
		st.rev = brev
		if brev {
			st.from, st.to = st.to, st.from
		}
		order = append(order, st)
		cur = st.to
		total += bd
	}
	if total >= baked {
		return
	}
	s.scratch = s.scratch[:0]
	for _, st := range order {
		if !st.rev {
			s.scratch = append(s.scratch, s.raw[st.i0:st.i1+1]...)
			continue
		}
		for i := st.i1; i >= st.i0; i-- {
			line := false
			if i+1 <= st.i1 {
				line = s.raw[i+1].line
			}
			s.scratch = append(s.scratch, glyphKnot{s.raw[i].p, line, false})
		}
	}
	s.raw = append(s.raw[:0], s.scratch...)
}

// chordRef is the glyph's reference chord: the average polyline span,
// clamped to a sane band of the em.
func (s *StringCmd) chordRef() int {
	var sum, n int
	for i := 1; i < len(s.nodes); i++ {
		if s.nodes[i].anchor || s.nodes[i-1].anchor {
			continue
		}
		sum += ManhattanDist(s.nodes[i-1].p, s.nodes[i].p)
		n++
	}
	c := s.em / 8
	if n > 0 {
		c = sum / n
	}
	return min(max(c, s.em/24), s.em/8)
}

// flyBridge builds the needle-up polygon of a flying junction from
// the exit point (tangent given by prev->exit) to the entry point
// (tangent given by entry->next), sampled at the reference chord. It
// returns nil when the bridge would bend below rminFly.
func flyBridge(dst []bezier.Point, prev, exit, entry, next bezier.Point, cref int) []bezier.Point {
	dout := exit.Sub(prev)
	din := next.Sub(entry)
	lout := math.Hypot(float64(dout.X), float64(dout.Y))
	lin := math.Hypot(float64(din.X), float64(din.Y))
	if lout == 0 || lin == 0 {
		return nil
	}
	c := float64(cref)
	b1 := bezier.Point{
		X: exit.X + int(float64(dout.X)/lout*c),
		Y: exit.Y + int(float64(dout.Y)/lout*c),
	}
	b2 := bezier.Point{
		X: entry.X - int(float64(din.X)/lin*c),
		Y: entry.Y - int(float64(din.Y)/lin*c),
	}
	span := b2.Sub(b1)
	h := max(math.Hypot(float64(span.X), float64(span.Y))/3, c)
	cubic := bezier.Cubic{
		C0: b1,
		C1: bezier.Point{X: b1.X + int(float64(dout.X)/lout*h), Y: b1.Y + int(float64(dout.Y)/lout*h)},
		C2: bezier.Point{X: b2.X - int(float64(din.X)/lin*h), Y: b2.Y - int(float64(din.Y)/lin*h)},
		C3: b2,
	}
	dst = append(dst, b1)
	dst = bezier.Sample(dst, cubic, min(cref, rminFly/2))
	// The full flown polygon must respect the curvature envelope.
	if minCircumradius(prev, exit, dst, entry, next) >= rminFly {
		return dst
	}
	// Hairpins and reversals: a bounded-curvature teardrop instead,
	// with a touch of margin over the envelope floor.
	dst = dubinsBridge(dst[:0], exit, math.Atan2(float64(dout.Y), float64(dout.X)),
		entry, math.Atan2(float64(din.Y), float64(din.X)),
		float64(rminFly)*10/9, float64(min(cref, rminFly/2)))
	if dst == nil {
		return nil
	}
	// The sampler snaps its tail onto the entry point; the arrival
	// knot follows separately, so drop the duplicate.
	if n := len(dst); n > 0 && dst[n-1] == entry {
		dst = dst[:n-1]
	}
	if len(dst) == 0 {
		return nil
	}
	if minCircumradius(prev, exit, dst, entry, next) < rminFly {
		return nil
	}
	return dst
}

// minCircumradius measures the tightest three-point bend of the
// polygon prev, exit, bridge..., entry, next.
func minCircumradius(prev, exit bezier.Point, bridge []bezier.Point, entry, next bezier.Point) float64 {
	r := math.Inf(1)
	pts := [4]bezier.Point{prev, exit, entry, next}
	get := func(i int) bezier.Point {
		switch {
		case i < 2:
			return pts[i]
		case i < 2+len(bridge):
			return bridge[i-2]
		default:
			return pts[i-len(bridge)]
		}
	}
	n := 4 + len(bridge)
	for i := 2; i < n; i++ {
		a, b, c := get(i-2), get(i-1), get(i)
		abx, aby := float64(b.X-a.X), float64(b.Y-a.Y)
		bcx, bcy := float64(c.X-b.X), float64(c.Y-b.Y)
		cax, cay := float64(a.X-c.X), float64(a.Y-c.Y)
		area2 := math.Abs(abx*bcy - aby*bcx)
		if area2 == 0 {
			continue
		}
		lab := math.Hypot(abx, aby)
		lbc := math.Hypot(bcx, bcy)
		lca := math.Hypot(cax, cay)
		r = min(r, lab*lbc*lca/(2*area2))
	}
	return r
}

func addScale(p1, p2 bezier.Point, em, height int) bezier.Point {
	return p2.Mul(em).Div(height).Add(p1)
}

func engraveSpline(yield func(Command) bool, pos bezier.Point, em, height int, spline vector.UniformBSpline) bool {
	for {
		k, ok := spline.Next()
		if !ok {
			break
		}
		c := addScale(pos, k.Ctrl, em, height)
		cmd := ControlPoint(k.Line, c)
		if k.Periodic {
			cmd = PeriodicPoint(c)
		}
		if !yield(cmd) {
			return false
		}
	}
	return true
}

// Profile describes the engraving timing as well as the
// start and end points of a [Engraving].
type Profile struct {
	// Pattern is the times, in ticks, where the plan
	// switches from moves to engraves or back.
	Pattern []uint
	// Start and End points of the plan.
	Start, End bezier.Point
}

func ProfileSpline(s bspline.Curve) Profile {
	engraving := false
	firstPoint := true
	var prof Profile
	var t uint
	var seg bspline.Segment
	for k := range s {
		c, ticks, _ := seg.Knot(k)
		prof.End = c.C3
		if !k.Engrave && firstPoint {
			prof.Start = prof.End
			firstPoint = false
		}
		if k.Engrave != engraving {
			engraving = k.Engrave
			prof.Pattern = append(prof.Pattern, t)
		}
		t += ticks
	}
	if t > 0 {
		prof.Pattern = append(prof.Pattern, t)
	}
	return prof
}

func (p Profile) Equal(p2 Profile) bool {
	if p.Start != p2.Start || p.End != p2.End || len(p.Pattern) != len(p2.Pattern) {
		return false
	}
	for i, t := range p.Pattern {
		if p2.Pattern[i] != t {
			return false
		}
	}
	return true
}

func strlen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

// SafePointer tracks the most recent safe point of a B-Spline.
// A safe point is where velocity and acceleration are zero.
type SafePointer struct {
	safePoint bezier.Point
	// history contains the knots after SafePoint.
	history   []bspline.Knot
	progress  uint
	completed int
}

func (s *SafePointer) Resume(conf StepperConfig) []bspline.Knot {
	move := make([]bspline.Knot, 0, len(s.history)+10)
	// Move to the safe point.
	move = appendLine(move, conf, false, bezier.Point{}, s.safePoint)
	move = append(move, s.history...)
	return move
}

func (s *SafePointer) Progress(p uint) {
	s.progress += p
	// Advance s.completed.
	for len(s.history) > s.completed {
		k := s.history[s.completed]
		// Stop when an engraving knot later than progress
		// is reached.
		if s.progress < k.T {
			break
		}
		s.progress -= k.T
		s.completed++
		// Advance safe point.
		his := s.history[:s.completed]
		n := len(his)
		if n < 3 {
			continue
		}
		k0, k1, k2 := his[n-3], his[n-2], his[n-1]
		if clamped := k0.Ctrl == k1.Ctrl && k1 == k2; !clamped {
			continue
		}
		rem := copy(s.history, s.history[n:])
		s.history = s.history[:rem]
		s.completed = 0
		s.safePoint = k0.Ctrl
	}
}

func (s *SafePointer) Knot(k bspline.Knot) {
	s.history = append(s.history, k)
}
