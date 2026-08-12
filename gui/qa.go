package gui

import (
	"math"

	"seedhammer.com/bezier"
	"seedhammer.com/bspline"
	"seedhammer.com/engrave"
	"seedhammer.com/gui/layout"
	"seedhammer.com/gui/op"
	"seedhammer.com/gui/widget"
)

func qaEngraveFlow(ctx *Context) {
	p := ctx.Platform
	params := p.EngraverParams()
	plan := engrave.PlanEngraving(params.StepperConfig,
		qaPlan(params.Millimeter))
	e := newEngraverJob(p, plan, suppressStalls)
	e.Start()
	defer e.Stop()
	var eerr string
	var xLoadVals, yLoadVals maxValue
	var maxXLoad, maxYLoad int
	for !ctx.Done {
		lastSt := e.Status()
		stats := e.Stats()
		xload := stats.XLoad
		yload := stats.YLoad
		if stats.XSpeed < stats.StallSpeed {
			xload = 0
		}
		if stats.YSpeed < stats.StallSpeed {
			yload = 0
		}
		maxXLoad = xLoadVals.Put(xload)
		maxYLoad = yLoadVals.Put(yload)
		if err := stats.Error; eerr == "" && err != nil {
			eerr = err.Error()
		}
		if eerr == "" {
			eerr = lastSt.Error
		}
		qa := drawQA(ctx, stats, maxXLoad, maxYLoad, eerr)
		p.Wakeup()
		ctx.Frame(qa)
	}
}

func drawQA(ctx *Context, st EngraverStats, maxXLoad, maxYLoad int, eerr string) op.Op {
	dims := ctx.Platform.DisplaySize()
	th := &descriptorTheme
	title, txtsz := layoutTitle(ctx, dims.X, th.Text, "FOREVER, LAURA!")
	r := layout.Rectangle{Max: dims}
	r = r.Shrink(txtsz.Max.Y, 8, 0, 8)
	body, _ := widget.Labelwf(&ctx.B, ctx.Styles.body, r.Dx(), th.Text,
		"X Speed: %dmm/s\nY Speed: %dmm/s\nX Load: %d (now: %d)\nY Load: %d (now: %d)\nX Stalls: %d\nY Stalls: %d\nError: %s",
		st.XSpeed, st.YSpeed, maxXLoad, st.XLoad, maxYLoad, st.YLoad, st.XStalls, st.YStalls, eerr)
	return op.Layer(
		title,
		body.Offset(r.Min),
		op.Color(&ctx.B, th.Background),
	)
}

func qaPlan(mm int) engrave.Engraving {
	return func(yield func(engrave.Command) bool) {
		// The QA pattern exercises the prototype's physical travel
		// envelope (95x85 mm from the bumpers): an 81 mm square in the
		// plate frame, which sits 5.0/0 mm off the bumpers, so a 2 mm
		// plate margin clears both ends of each axis.
		const side = 81
		xMin, yMin := 2*mm, 2*mm
		xMax, yMax := xMin+side*mm, yMin+side*mm
		mp := bezier.Pt(xMin, yMin)
		if !yield(engrave.Move(mp)) {
			return
		}
		const (
			repeats = 10
		)
		center := bezier.Pt((xMin+xMax)/2, (yMin+yMax)/2)
		topRight := bezier.Pt(xMax, yMin)
		bottomLeft := bezier.Pt(xMin, yMax)
		bottomRight := bezier.Pt(xMax, yMax)
		rect := []bezier.Point{
			mp,
			topRight,
			bottomRight,
			bottomLeft,
		}
		diag := []bezier.Point{
			bottomLeft, topRight,
		}
		// This is the inlined result of
		//
		//	const segments = 50
		//  radius := int(40.5 * float64(mm)) // the 81 mm envelope circle
		//  circle := circleBSpline(segments, bezier.Point{}, radius)
		circle := []bezier.Point{
			{X: 259200, Y: 0},
			{X: 259200, Y: 0},
			{X: 259200, Y: 0},
			{X: 258369, Y: 13159},
			{X: 256941, Y: 36067},
			{X: 251496, Y: 65310},
			{X: 240407, Y: 98696},
			{X: 223261, Y: 132813},
			{X: 199516, Y: 166326},
			{X: 170823, Y: 195821},
			{X: 140600, Y: 218558},
			{X: 112074, Y: 234339},
			{X: 81828, Y: 246568},
			{X: 50277, Y: 254871},
			{X: 17937, Y: 259166},
			{X: -14686, Y: 259370},
			{X: -47078, Y: 255484},
			{X: -78729, Y: 247567},
			{X: -109131, Y: 235754},
			{X: -137833, Y: 220198},
			{X: -164294, Y: 201253},
			{X: -188411, Y: 178831},
			{X: -208632, Y: 154713},
			{X: -227019, Y: 126378},
			{X: -240886, Y: 97239},
			{X: -251265, Y: 66002},
			{X: -256957, Y: 36078},
			{X: -258832, Y: 10878},
			{X: -258728, Y: -13008},
			{X: -256415, Y: -38993},
			{X: -250627, Y: -67462},
			{X: -241864, Y: -95003},
			{X: -227905, Y: -125028},
			{X: -209328, Y: -154126},
			{X: -183714, Y: -183766},
			{X: -152104, Y: -210537},
			{X: -117919, Y: -231433},
			{X: -83766, Y: -245968},
			{X: -49581, Y: -255068},
			{X: -18783, Y: -258977},
			{X: 12047, Y: -259254},
			{X: 39490, Y: -256135},
			{X: 62611, Y: -251281},
			{X: 84829, Y: -244987},
			{X: 109562, Y: -235613},
			{X: 139090, Y: -219748},
			{X: 170161, Y: -196481},
			{X: 199357, Y: -166552},
			{X: 223258, Y: -132823},
			{X: 240402, Y: -98706},
			{X: 251494, Y: -65318},
			{X: 256940, Y: -36071},
			{X: 258369, Y: -13161},
			{X: 259200, Y: 0},
			{X: 259200, Y: 0},
			{X: 259200, Y: 0},
		}
		for {
			for range repeats {
				for _, c := range rect {
					if !yield(engrave.Move(c)) {
						return
					}
				}
			}
			for range repeats {
				for _, c := range diag {
					if !yield(engrave.Move(c)) {
						return
					}
				}
			}
			for range repeats {
				if !yield(engrave.Move(circle[0].Add(center))) {
					return
				}
				for _, c := range circle {
					c = c.Add(center)
					if !yield(engrave.ControlPoint(false, c)) {
						return
					}
				}
			}
		}
	}
}

type maxValue struct {
	values [50]int
	index  int
}

func (m *maxValue) Put(v int) int {
	m.values[m.index] = v
	m.index = (m.index + 1) % len(m.values)
	mval := 0
	for _, v := range m.values {
		mval = max(v, mval)
	}
	return mval
}

func circleBSpline(segments int, center bezier.Point, radius int) []bezier.Point {
	var ctrls []bezier.Point
	for i := range segments + 1 {
		angle := 2 * math.Pi * float64(i) / float64(segments)
		p := center.Add(bezier.Pt(
			int(float64(radius)*math.Cos(angle)),
			int(float64(radius)*math.Sin(angle)),
		))
		ctrls = append(ctrls, p)
	}
	knots, err := bspline.InterpolatePoints(ctrls)
	if err != nil {
		panic(err)
	}
	return knots
}
