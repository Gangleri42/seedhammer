package stepper

import (
	"seedhammer.com/bezier"
	"seedhammer.com/bspline"
)

// Driver is an engraving driver suitable for
// stepping through a [bspline.Curve] using DMA.
type Driver struct {
	seg     bspline.Segment
	stepper bezier.Interpolator
	needle  bool
	// pos is in physical microsteps. Interpolator positions are in
	// planning units and converted per axis by the scales.
	pos            bezier.Point
	xscale, yscale Scale

	w     Writer
	buf   []uint32
	steps int
}

// Scale converts absolute positions in planning units to the physical
// microsteps of one axis. The zero value is the identity, for an axis
// whose microsteps per millimeter equal the planning unit scale.
type Scale struct {
	// ratio in Q24 fixed point, or 0 for identity.
	ratioQ24 uint32
}

// ScaleQ24 returns the scale for a Q24 fixed-point ratio of physical
// microsteps to planning units. Zero (and the zero Scale) is the
// identity. Ratios must not exceed 1<<24: a physically finer axis
// breaks the planner's 1 step per tick budget and loses steps at full
// speed.
func ScaleQ24(ratio uint32) Scale {
	if ratio == 1<<24 {
		ratio = 0
	}
	return Scale{ratioQ24: ratio}
}

// steps converts an absolute planning-unit coordinate to physical
// microsteps, rounded to nearest. Scaling absolute positions rather
// than deltas bounds the error to half a microstep with no drift.
func (s Scale) steps(v int) int {
	if s.ratioQ24 == 0 {
		return v
	}
	return int((int64(v)*int64(s.ratioQ24) + 1<<23) >> 24)
}

type Writer interface {
	Write(steps []uint32) (completed int, err error)
}

const (
	pinBits = 5
	// stepsPerWord is the number of pio steps that
	// fit into a 32-bit pio FIFO entry.
	stepsPerWord = 32 / pinBits
)

const (
	// Pin offsets from base pin.
	pinDirY = iota
	pinDirX
	pinNeedle
	pinStepY
	pinStepX
)

func (d *Driver) fill() {
	n := len(d.buf) * stepsPerWord
	for d.steps < n && d.stepper.Step() {
		// The 5-bit pins. Note that the all-zero
		// value means halt, so the code below is
		// careful to set at least one direction pin.
		var pins uint8
		pos := d.stepper.Position()
		pos = bezier.Point{
			X: d.xscale.steps(pos.X),
			Y: d.yscale.steps(pos.Y),
		}
		// Clamp to 1 step per tick.
		step := pos.Sub(d.pos)
		step.X = max(min(step.X, 1), -1)
		step.Y = max(min(step.Y, 1), -1)
		d.pos = d.pos.Add(step)
		if step.X != 0 {
			pins |= 0b1 << pinStepX
		}
		if step.X == -1 || step.X == 0 {
			pins |= 0b1 << pinDirX
		}
		if step.Y != 0 {
			pins |= 0b1 << pinStepY
		}
		if step.Y == -1 || step.Y == 0 {
			pins |= 0b1 << pinDirY
		}
		if d.needle {
			pins |= 0b1 << pinNeedle
		}
		word := d.steps / stepsPerWord
		stepIdx := d.steps % stepsPerWord
		w := uint32(0)
		if stepIdx != 0 {
			w = d.buf[word]
		}
		w |= uint32(pins) << (stepIdx * pinBits)
		d.buf[word] = w
		d.steps++
	}
}

func NewDriver(w Writer, xscale, yscale Scale) *Driver {
	return &Driver{
		w:      w,
		xscale: xscale,
		yscale: yscale,
		buf:    make([]uint32, 128),
	}
}

func (d *Driver) Knot(k bspline.Knot) (completed uint, err error) {
	c, ticks, needle := d.seg.Knot(k)
	d.needle = needle
	d.stepper.Segment(c, ticks)
	for {
		before := d.steps
		d.fill()
		if d.steps == before {
			return completed, nil
		}
		n, err := d.flush()
		completed += n
		if err != nil {
			return completed, err
		}
	}
}

func (d *Driver) Flush() error {
	// Ensure partially filled words are written,
	// by rounding up the step count.
	if rem := d.steps % stepsPerWord; rem > 0 {
		d.steps += stepsPerWord - rem
	}
	_, err := d.flush()
	return err
}

func (d *Driver) flush() (completed uint, err error) {
	// Write whole words.
	n := d.steps / stepsPerWord
	d.steps -= n * stepsPerWord
	buf := d.buf[:n]
	var nwords int
	nwords, err = d.w.Write(buf)
	copy(d.buf, d.buf[n:])
	return uint(nwords) * stepsPerWord, err
}
