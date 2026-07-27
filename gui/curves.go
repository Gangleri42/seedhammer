package gui

import (
	"errors"
	"fmt"
	"image"
	"image/color"

	"seedhammer.com/bezier"
	"seedhammer.com/bspline"
	"seedhammer.com/curves"
	"seedhammer.com/engrave"
	"seedhammer.com/gui/op"
	"seedhammer.com/gui/widget"
)

// curvesPayload is the body of a scanned seedhammer.com:curves
// record, dispatched by curvesFlow on its mode.
type curvesPayload []byte

var errCurvesText = errors.New("The text cannot be engraved.")

// The one limit on curves payloads: the time cap bounds unattended
// machine time. The curves package is its single source; hostile
// structural blowups are already rejected by curves.Parse's own
// bounds, and the planner streams, so complexity costs time, not
// memory.
const curvesMaxMinutes = curves.MaxMinutes

// curvesFlow dispatches a seedhammer.com:curves record on its mode:
// text is laid out and rendered from the firmware font like a text
// plate, path is engraved as geometry. Both share the engrave path.
func curvesFlow(ctx *Context, th *Colors, payload curvesPayload) {
	mode, err := curves.Mode(payload)
	if err != nil {
		showError(ctx, th, err, blankScreen)
		return
	}
	switch mode {
	case curves.ModeText:
		text, _ := curves.Text(payload)
		t, ok := parsePlainText([]byte(text))
		if !ok {
			showError(ctx, th, errCurvesText, blankScreen)
			return
		}
		textFlow(ctx, th, t)
	case curves.ModePath:
		curvesPathFlow(ctx, th, payload)
	}
}

func blankScreen(ctx *Context, th *Colors, dims image.Point) op.Op {
	return op.Color(&ctx.B, th.Background)
}

func curvesPathFlow(ctx *Context, th *Colors, payload curvesPayload) {
	params := ctx.Platform.EngraverParams()
	cs := &CurvesScreen{}
	plate, err := scanCurves(ctx, th, cs, payload, params)
	if err != nil {
		if errors.Is(err, errPlanCanceled) {
			return
		}
		cs.info = ""
		showError(ctx, th, err, cs.Draw)
		return
	}
	for {
		plate, ok := cs.Confirm(ctx, th, plate)
		if !ok {
			return
		}
		completed := NewEngraveScreen(ctx, plate).Engrave(ctx, &engraveTheme)
		if completed {
			return
		}
	}
}

// scanCurves validates the payload behind the progress screen: the
// preview raster fills stroke by stroke as the walk streams and the
// info line carries the walked fraction; the back button abandons
// the scan.
func scanCurves(ctx *Context, th *Colors, cs *CurvesScreen, payload []byte, params engrave.Params) (Plate, error) {
	dims := ctx.Platform.DisplaySize()
	return runJob(ctx, th, func(pump func(done, total int) bool) (Plate, error) {
		return validateCurves(cs, payload, params, dims, pump)
	}, func(pct int) op.Op {
		cs.info = fmt.Sprintf("Preparing %d%%", pct)
		return cs.Draw(ctx, th, dims)
	})
}

// validateCurves parses and validates a curves payload and fills in
// the screen's preview, walking the drawing once: the payload's
// commands stream through the planner while the duration and the
// preview raster accumulate from the planned knots. The payload
// dictates all geometry, so everything is checked up front: the
// confirm screen preview is the operator's only verification. pump,
// if not nil, observes the walk and cancels it by returning false.
func validateCurves(cs *CurvesScreen, payload []byte, params engrave.Params, dims image.Point, pump func(done, total int) bool) (Plate, error) {
	drawing, err := curves.Open(payload, params)
	if err != nil {
		return Plate{}, err
	}
	r := newSplineRasterizer(previewSide(dims), params)
	// Shared with the pumping frame loop as it fills; the cooperative
	// scheduler serializes the access.
	cs.preview = r.preview
	var walkErr error
	var duration uint
	spline := engrave.PlanEngraving(params.StepperConfig, func(yield func(engrave.Command) bool) {
		walkErr = drawing.Walk(pump, yield)
	})
	for k := range spline {
		duration += k.T
		r.knot(k)
	}
	if walkErr != nil {
		return Plate{}, walkErr
	}
	// The planner measures engraved segments only; bound every knot,
	// including travel, to keep the head on the plate.
	mm := params.Millimeter
	margin := bezier.Pt(safetyMargin*mm, safetyMargin*mm)
	sz := SquarePlate.Dims(mm)
	if !drawing.Bounds.In(bspline.Bounds{Min: margin, Max: sz.Sub(margin)}) {
		return Plate{}, ErrTooLarge
	}
	tps := params.TicksPerSecond
	if duration > curvesMaxMinutes*60*tps {
		mins := (duration + 60*tps - 1) / (60 * tps)
		return Plate{}, fmt.Errorf("The engraving would run %d minutes; at most %d are allowed.", mins, curvesMaxMinutes)
	}
	plate := Plate{
		Duration: duration,
		// A fresh plan for the engraver's own re-iterations; the walk
		// above already proved it.
		Spline: engrave.PlanEngraving(params.StepperConfig, drawing.Engraving()),
	}
	cs.init(plate, drawing, params)
	return plate, nil
}

type CurvesScreen struct {
	preview *curvesPreview
	info    string
}

func (s *CurvesScreen) init(plate Plate, drawing *curves.Drawing, params engrave.Params) {
	mm := params.Millimeter
	w := (drawing.Bounds.Dx() + mm/2) / mm
	h := (drawing.Bounds.Dy() + mm/2) / mm
	secs := (plate.Duration + params.TicksPerSecond - 1) / params.TicksPerSecond
	s.info = fmt.Sprintf("%d x %d mm   %d:%.2d", w, h, secs/60, secs%60)
}

func (s *CurvesScreen) Confirm(ctx *Context, th *Colors, plate Plate) (Plate, bool) {
	return confirmScreen(ctx, th, s.Draw, func() (Plate, bool, error) {
		return plate, true, nil
	})
}

// previewSide is the pixel size of the square plate preview.
func previewSide(dims image.Point) int {
	const infoSpace = 32
	return min(dims.Y-leadingSize-infoSpace, dims.X/2)
}

func (s *CurvesScreen) Draw(ctx *Context, th *Colors, dims image.Point) op.Op {
	title, _ := layoutTitle(ctx, dims.X, th.Text, "Engrave Curves")
	content := op.Layer(
		title,
		op.Color(&ctx.B, th.Background),
	)
	if s.preview == nil {
		return content
	}
	side := s.preview.sz.X
	pos := image.Pt((dims.X-side)/2, leadingSize+4)
	plate := image.Rectangle{Max: s.preview.sz}
	// The plate outline, with its 3mm corner radius to scale.
	outline := op.Compose(
		op.Color(&ctx.B, th.Primary),
		op.RoundedOutline2(&ctx.B, plate, 3*side/85, 1).Offset(pos),
	)
	drawing := op.Compose(
		op.Color(&ctx.B, th.Text),
		op.Mask(&ctx.B, s.preview).Offset(pos),
	)
	info, infosz := widget.Label(&ctx.B, ctx.Styles.subtitle, th.Text, s.info)
	info = info.Offset(image.Pt((dims.X-infosz.X)/2, pos.Y+side+(dims.Y-pos.Y-side-infosz.Y)/2))
	return op.Layer(
		drawing,
		outline,
		info,
		content,
	)
}

// curvesPreview is a 1-bit raster of the engraved strokes of a
// planned spline, scaled to the display.
type curvesPreview struct {
	sz   image.Point
	bits []uint32
}

// splineRasterizer accumulates the preview from planned spline knots
// as they stream by, so the raster costs no walk of its own.
type splineRasterizer struct {
	preview *curvesPreview
	seg     bspline.Segment
	samples []bezier.Point
	plate   int
	spacing int
}

func newSplineRasterizer(side int, params engrave.Params) *splineRasterizer {
	plate := SquarePlate.Dims(params.Millimeter).X
	return &splineRasterizer{
		preview: &curvesPreview{
			sz:   image.Pt(side, side),
			bits: make([]uint32, (side*side+31)/32),
		},
		plate: plate,
		// Sample at a third of a pixel so plotted points form
		// contiguous strokes without a line rasterizer.
		spacing: max(1, plate/(side*3)),
	}
}

func (r *splineRasterizer) knot(k bspline.Knot) {
	c, dt, engrave := r.seg.Knot(k)
	if dt == 0 || !engrave {
		return
	}
	r.samples = append(r.samples[:0], c.C0)
	r.samples = bezier.Sample(r.samples, c, r.spacing)
	side := r.preview.sz.X
	for _, pt := range r.samples {
		r.preview.set(pt.X*side/r.plate, pt.Y*side/r.plate)
	}
}

func (p *curvesPreview) set(x, y int) {
	if x < 0 || y < 0 || x >= p.sz.X || y >= p.sz.Y {
		return
	}
	i := y*p.sz.X + x
	p.bits[i/32] |= 1 << (i % 32)
}

func (p *curvesPreview) alpha(x, y int) uint8 {
	if x < 0 || y < 0 || x >= p.sz.X || y >= p.sz.Y {
		return 0
	}
	i := y*p.sz.X + x
	if p.bits[i/32]&(1<<(i%32)) != 0 {
		return 0xff
	}
	return 0
}

func (p *curvesPreview) ColorModel() color.Model {
	return color.AlphaModel
}

func (p *curvesPreview) Bounds() image.Rectangle {
	return image.Rectangle{Max: p.sz}
}

func (p *curvesPreview) At(x, y int) color.Color {
	return color.Alpha{A: p.alpha(x, y)}
}

func (p *curvesPreview) RGBA64At(x, y int) color.RGBA64 {
	a := p.alpha(x, y)
	return color.RGBA64{A: uint16(a)<<8 | uint16(a)}
}
