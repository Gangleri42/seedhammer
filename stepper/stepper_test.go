package stepper

import (
	"iter"
	"slices"
	"testing"

	qr "github.com/seedhammer/kortschak-qr"
	"seedhammer.com/bezier"
	"seedhammer.com/bip39"
	"seedhammer.com/bspline"
	"seedhammer.com/engrave"
	"seedhammer.com/font/constant"
	"seedhammer.com/slip39"
)

func TestPath(t *testing.T) {
	cmds := []struct {
		Engrave bool
		P       bezier.Point
	}{
		{false, bezier.Pt(0, 0)},
		{false, bezier.Pt(1, 0)},
		{true, bezier.Pt(100*mm, 10*mm)},
		{false, bezier.Pt(10*mm, 30*mm)},
		{true, bezier.Pt(60*mm, 30*mm)},
		{true, bezier.Pt(50*mm, 10*mm)},
		{false, bezier.Pt(0, 0)},
	}
	plan := func(yield func(engrave.Command) bool) {
		for _, cmd := range cmds {
			c := engrave.Move(cmd.P)
			if cmd.Engrave {
				c = engrave.Line(cmd.P)
			}
			if !yield(c) {
				return
			}
		}
	}
	pen := bezier.Point{}
	for {
		if pen != cmds[0].P {
			break
		}
		cmds = cmds[1:]
	}
	spline := engrave.PlanEngraving(params.StepperConfig, plan)
	for s := range runEngraving(t, spline) {
		dx, dy := (s>>pinDirX)&0b1, (s>>pinDirY)&0b1
		sx, sy := (s>>pinStepX)&0b1, (s>>pinStepY)&0b1
		needle := s>>pinNeedle&0b1 == 0b1
		pen.X += int(sx) * (1 - int(dx)*2)
		pen.Y += int(sy) * (1 - int(dy)*2)
		for len(cmds) > 0 {
			if pen != cmds[0].P || needle != cmds[0].Engrave {
				break
			}
			cmds = cmds[1:]
		}
	}
	if len(cmds) > 0 {
		t.Errorf("engraving didn't visit the points %v", cmds)
	}
}

type device struct {
	steps     []uint32
	progress  int
	reportErr error
}

func (d *device) Read(steps []uint32) int {
	n := copy(steps, d.steps)
	d.progress += n
	rem := copy(d.steps, d.steps[n:])
	d.steps = d.steps[:rem]
	return n
}

func (d *device) Write(steps []uint32) (int, error) {
	p := d.progress
	d.progress = 0
	if err := d.reportErr; err != nil {
		return p, err
	}
	d.steps = append(d.steps, steps...)
	return p, nil
}

func runEngraving(t *testing.T, spline bspline.Curve) iter.Seq[uint8] {
	dev := new(device)
	drv := NewDriver(dev, Scale{}, Scale{})
	for k := range spline {
		if _, err := drv.Knot(k); err != nil {
			t.Fatal(err)
		}
	}
	if err := drv.Flush(); err != nil {
		t.Fatal(err)
	}
	return func(yield func(step uint8) bool) {
		for _, w := range dev.steps {
			for j := range stepsPerWord {
				s := uint8((w >> (j * pinBits)) & (0b1<<pinBits - 1))
				if s == 0 || !yield(s) {
					return
				}
			}
		}
	}
}

func TestScaleSteps(t *testing.T) {
	for _, v := range []int{-1_280_000, -100_000, -1, 0, 1, 99_999, 544_000} {
		var zero Scale
		if got := zero.steps(v); got != v {
			t.Errorf("zero Scale: steps(%d) = %d", v, got)
		}
		if got := ScaleQ24(1 << 24).steps(v); got != v {
			t.Errorf("unit Scale: steps(%d) = %d", v, got)
		}
	}
	half := ScaleQ24(1 << 23)
	for _, c := range []struct{ v, want int }{
		{0, 0}, {1, 1}, {2, 1}, {3, 2}, {-1, 0}, {-2, -1}, {-3, -1},
	} {
		if got := half.steps(c.v); got != c.want {
			t.Errorf("half Scale: steps(%d) = %d, want %d", c.v, got, c.want)
		}
	}
	// The shape of a 58.2 mm/rev axis against 6400 units/mm: positions
	// must be monotonic and advance by at most one microstep per unit.
	awk := ScaleQ24(2_306_167)
	prev := awk.steps(-5000)
	for v := -4999; v <= 5000; v++ {
		cur := awk.steps(v)
		if cur < prev || cur > prev+1 {
			t.Fatalf("steps(%d) = %d after steps(%d) = %d", v, cur, v-1, prev)
		}
		prev = cur
	}
}

type tickTrace struct {
	x, y   int
	needle bool
}

func runScaled(t *testing.T, spline bspline.Curve, xscale, yscale Scale) (words []uint32, ticks []tickTrace) {
	t.Helper()
	dev := new(device)
	drv := NewDriver(dev, xscale, yscale)
	for k := range spline {
		if _, err := drv.Knot(k); err != nil {
			t.Fatal(err)
		}
	}
	if err := drv.Flush(); err != nil {
		t.Fatal(err)
	}
	words = slices.Clone(dev.steps)
	var pen bezier.Point
	for _, w := range words {
		for j := range stepsPerWord {
			s := uint8((w >> (j * pinBits)) & (0b1<<pinBits - 1))
			if s == 0 {
				return words, ticks
			}
			dx, dy := (s>>pinDirX)&0b1, (s>>pinDirY)&0b1
			sx, sy := (s>>pinStepX)&0b1, (s>>pinStepY)&0b1
			pen.X += int(sx) * (1 - int(dx)*2)
			pen.Y += int(sy) * (1 - int(dy)*2)
			ticks = append(ticks, tickTrace{pen.X, pen.Y, s>>pinNeedle&0b1 == 0b1})
		}
	}
	return words, ticks
}

func TestScale(t *testing.T) {
	plan := func(yield func(engrave.Command) bool) {
		_ = yield(engrave.Move(bezier.Pt(2*mm, 1*mm))) &&
			yield(engrave.Line(bezier.Pt(30*mm, 25*mm))) &&
			yield(engrave.Line(bezier.Pt(5*mm, 24*mm))) &&
			yield(engrave.Line(bezier.Pt(33*mm, 7*mm)))
	}
	spline := engrave.PlanEngraving(params.StepperConfig, plan)
	baseWords, base := runScaled(t, spline, Scale{}, Scale{})

	// The explicit unit ratio is the identity, bit for bit.
	identWords, _ := runScaled(t, spline, ScaleQ24(1<<24), ScaleQ24(1<<24))
	if !slices.Equal(baseWords, identWords) {
		t.Errorf("unit Scale changed the step words")
	}

	// Scaling X leaves the Y and needle traces untouched, keeps X
	// within a microstep of the scaled plan, and lands exactly on the
	// scaled endpoint.
	xs := ScaleQ24(1 << 24 * 62 / 100)
	_, scaled := runScaled(t, spline, xs, Scale{})
	if len(scaled) != len(base) {
		t.Fatalf("tick count changed: %d != %d", len(scaled), len(base))
	}
	for i := range base {
		b, s := base[i], scaled[i]
		if s.y != b.y {
			t.Fatalf("tick %d: Y diverged: %d != %d", i, s.y, b.y)
		}
		if s.needle != b.needle {
			t.Fatalf("tick %d: needle diverged", i)
		}
		if d := s.x - xs.steps(b.x); d < -1 || 1 < d {
			t.Fatalf("tick %d: X = %d, want %d within 1", i, s.x, xs.steps(b.x))
		}
	}
	lastB, lastS := base[len(base)-1], scaled[len(scaled)-1]
	if lastS.x != xs.steps(lastB.x) {
		t.Errorf("terminal X = %d, want exactly %d", lastS.x, xs.steps(lastB.x))
	}
}

const (
	mm             = 6400
	speed          = 40 * mm
	engravingSpeed = 8 * mm
	accel          = 100 * mm
	jerk           = 2400 * mm
)

var (
	params = engrave.Params{
		Millimeter:  mm,
		StrokeWidth: mm / 3,
		StepperConfig: engrave.StepperConfig{
			Speed:          speed,
			EngravingSpeed: engravingSpeed,
			Acceleration:   accel,
			Jerk:           jerk,
			TicksPerSecond: speed,
		},
	}
)

func TestTiming(t *testing.T) {
	f := constant.Font
	const em = 300
	stringer := engrave.NewConstantStringer(f, params, em)
	const seedqr = "011513251154012711900771041507421289190620080870026613431420201617920614089619290300152408010643"
	qrc, err := qr.Encode(seedqr, qr.L)
	if err != nil {
		t.Fatal(err)
	}
	qr, err := engrave.ConstantQR(qrc)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		plan engrave.Engraving
	}{
		{
			"String",
			func(yield func(engrave.Command) bool) {
				engrave.String(f, em, "HELLO WORLD").Engrave(yield)
			},
		},
		{
			"BIP39",
			func(yield func(engrave.Command) bool) {
				stringer.PaddedString(yield, "ZOO", bip39.ShortestWord, bip39.LongestWord)
			},
		},
		{
			"SLIP39",
			func(yield func(engrave.Command) bool) {
				stringer.PaddedString(yield, "ACADEMIC", slip39.ShortestWord, slip39.LongestWord)
			},
		},
		{
			"SeedQR",
			qr.Engrave(params.StepperConfig, params.StrokeWidth, 3),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			spline := engrave.PlanEngraving(params.StepperConfig, test.plan)
			want := engrave.ProfileSpline(spline).Pattern
			got := profileSpline(t, spline)
			if !slices.Equal(got, want) {
				t.Errorf("got profile\n%+v\nwant\n%+v", got, want)
			}
		})
	}
}

// profileSpline is like [engrave.ProfileSpline] using a stepper to
// produce the pattern.
func profileSpline(t *testing.T, spline bspline.Curve) []uint {
	t.Helper()
	var time uint
	wasNeedle := false
	var pattern []uint
	var lastt uint
	for s := range runEngraving(t, spline) {
		needle := (s>>pinNeedle)&0b1 != 0
		if wasNeedle != needle {
			wasNeedle = needle
			pattern = append(pattern, time)
			lastt = time
		}
		time++
	}
	if time != lastt {
		pattern = append(pattern, time)
	}
	return pattern
}
