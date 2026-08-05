package gui

import (
	"errors"
	"fmt"
	"image"

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
		textFlow(ctx, th, t, curves.PlateToken(payload))
	case curves.ModePath:
		curvesPathFlow(ctx, th, payload)
	}
}

func blankScreen(ctx *Context, th *Colors, dims image.Point) op.Op {
	return op.Color(&ctx.B, th.Background)
}

func curvesPathFlow(ctx *Context, th *Colors, payload curvesPayload) {
	params := ctx.Platform.EngraverParams()
	cs := &CurvesScreen{title: "Engrave Curves"}
	var plateSize PlateSize
	switch curves.PlateToken(payload) {
	case curves.PlateSmall:
		// The emitter laid the drawing out for the small plate: no
		// question; the scan validates against that frame.
		plateSize = SmallPlate
	case curves.PlateSquare:
		plateSize = SquarePlate
	default:
		// No named plate: measure the drawing (Open leaves the stats
		// zero until a walk, so this runs the geometry walk behind
		// the progress screen) and offer every plate it fits. A bad
		// payload falls through to the scan, whose error screen it is.
		fitsSmall := false
		if d, err := runJob(ctx, th, func(pump func(done, total int) bool) (*curves.Drawing, error) {
			return curves.Parse(payload, params)
		}, planFrame(ctx, th, cs.Draw)); err == nil {
			m := bezier.Pt(safetyMargin*params.Millimeter, safetyMargin*params.Millimeter)
			fitsSmall = d.Bounds.In(bspline.Bounds{Min: m, Max: plateDims(SmallPlate, params.Millimeter).Sub(m)})
		} else if errors.Is(err, errPlanCanceled) {
			return
		}
		var ok bool
		plateSize, ok = askPlateSize(ctx, th, fitsSmall)
		if !ok {
			return
		}
	}
	plate, err := scanCurves(ctx, th, cs, payload, params, plateSize)
	if err != nil {
		if errors.Is(err, errPlanCanceled) {
			return
		}
		showError(ctx, th, err, cs.Draw)
		return
	}
	NewEngraveScreen(ctx, plate, cs).Engrave(ctx, &engraveTheme)
}

// scanCurves validates the payload behind the progress screen: the
// preview raster fills stroke by stroke as the walk streams under
// the walked-percentage label; the back button abandons the scan.
func scanCurves(ctx *Context, th *Colors, cs *CurvesScreen, payload []byte, params engrave.Params, plateSize PlateSize) (Plate, error) {
	dims := ctx.Platform.DisplaySize()
	return runJob(ctx, th, func(pump func(done, total int) bool) (Plate, error) {
		return validateCurves(cs, payload, params, plateSize, dims, pump)
	}, planFrame(ctx, th, cs.Draw))
}

// validateCurves parses and validates a curves payload and fills in
// the screen's preview, walking the drawing once: the payload's
// commands stream through the planner while the duration and the
// preview raster accumulate from the planned knots. The payload
// dictates all geometry, so everything is checked up front: the
// confirm screen preview is the operator's only verification. pump,
// if not nil, observes the walk and cancels it by returning false.
func validateCurves(cs *CurvesScreen, payload []byte, params engrave.Params, plateSize PlateSize, dims image.Point, pump func(done, total int) bool) (Plate, error) {
	drawing, err := curves.Open(payload, params)
	if err != nil {
		return Plate{}, err
	}
	r := newSplineRasterizer(previewSide(dims, plateSize), params, plateSize)
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
	sz := plateDims(plateSize, mm)
	if !drawing.Bounds.In(bspline.Bounds{Min: margin, Max: sz.Sub(margin)}) {
		return Plate{}, ErrTooLarge
	}
	tps := params.TicksPerSecond
	if duration > curvesMaxMinutes*60*tps {
		mins := (duration + 60*tps - 1) / (60 * tps)
		return Plate{}, fmt.Errorf("The engraving would run %d minutes; at most %d are allowed.", mins, curvesMaxMinutes)
	}
	plate := Plate{
		Size:     plateSize,
		Duration: duration,
		// A fresh plan for the engraver's own re-iterations; the walk
		// above already proved it.
		Spline: engrave.PlanEngraving(params.StepperConfig, drawing.Engraving()),
	}
	cs.init(plate, drawing, params)
	return plate, nil
}

type CurvesScreen struct {
	preview *op.BitMask
	info    string
	title   string
	// notice warns about content that resembles a corrupted structured
	// backup (the text flow; see textNotice).
	notice string

	// hidden withholds what the plate says while keeping that it is a
	// plate: the operator's toggle for one they are about to leave
	// engraving unattended. Coarsening the raster into blocks was
	// tried on the bench and rejected — quantizing a 1-bit mask only
	// ever adds ink, so a hidden plate read as a white slab.
	hidden bool
}

// mask is the raster to draw, or nil while the content is hidden:
// then the plate keeps its outline and its figures, and shows
// nothing engraved inside them.
func (s *CurvesScreen) mask() *op.BitMask {
	if s.hidden {
		return nil
	}
	return s.preview
}

func (s *CurvesScreen) init(plate Plate, drawing *curves.Drawing, params engrave.Params) {
	mm := params.Millimeter
	w := (drawing.Bounds.Dx() + mm/2) / mm
	h := (drawing.Bounds.Dy() + mm/2) / mm
	secs := (plate.Duration + params.TicksPerSecond - 1) / params.TicksPerSecond
	s.info = fmt.Sprintf("%d x %d mm   %d:%.2d", w, h, secs/60, secs%60)
}

// initText fills the info line from the planned plate and the
// rasterizer's engraved hull — the text flow's stand-in for a curves
// drawing's measured bounds. The hull comes from the raster's samples,
// spaced at a third of a preview pixel, well inside the line's
// whole-millimeter rounding.
func (s *CurvesScreen) initText(plate Plate, r *splineRasterizer, params engrave.Params) {
	mm := params.Millimeter
	w, h := 0, 0
	if r.any {
		w = (r.max.X - r.min.X + mm/2) / mm
		h = (r.max.Y - r.min.Y + mm/2) / mm
	}
	secs := (plate.Duration + params.TicksPerSecond - 1) / params.TicksPerSecond
	s.info = fmt.Sprintf("%d x %d mm   %d:%.2d", w, h, secs/60, secs%60)
}

// previewSide is the pixel width of the plate preview: the width
// that fills the display's height budget at the plate's aspect,
// capped at half the display width. For the square plate the two
// budgets coincide; wider-than-tall plates hit the width cap.
func previewSide(dims image.Point, plateSize PlateSize) int {
	const infoSpace = 32
	d := plateSize.Dims()
	budget := dims.Y - leadingSize - infoSpace
	return min(budget*d.X/d.Y, dims.X/2)
}

func (s *CurvesScreen) Draw(ctx *Context, th *Colors, dims image.Point) op.Op {
	title, _ := layoutTitle(ctx, dims.X, th.Text, s.title)
	content := op.Layer(
		title,
		op.Color(&ctx.B, th.Background),
	)
	if s.preview == nil {
		return content
	}
	return op.Layer(
		s.plateOp(ctx, th, dims, s.info),
		content,
	)
}

// plateOp renders the preview raster in its plate outline with a
// strip line centered beneath — the plate rendering shared by the
// plan progress screen and the engrave screen, which swaps the strip
// per engrave state. The preview must be non-nil.
func (s *CurvesScreen) plateOp(ctx *Context, th *Colors, dims image.Point, strip string) op.Op {
	side := s.preview.Bounds().Dx()
	height := s.preview.Bounds().Dy()
	pos := image.Pt((dims.X-side)/2, leadingSize+4)
	plate := s.preview.Bounds()
	// The plate outline, with its 3mm corner radius to scale. The
	// radius keys off the width because every plate format shares it.
	outline := op.Compose(
		op.Color(&ctx.B, th.Primary),
		op.RoundedOutline2(&ctx.B, plate, 3*side/curves.PlateMM, 1).Offset(pos),
	)
	info, infosz := widget.Label(&ctx.B, ctx.Styles.subtitle, th.Text, strip)
	space := dims.Y - pos.Y - height
	info = info.Offset(image.Pt((dims.X-infosz.X)/2, pos.Y+height+(space-infosz.Y)/2))
	m := s.mask()
	if m == nil {
		return op.Layer(outline, info)
	}
	drawing := op.Compose(
		op.Color(&ctx.B, th.Text),
		op.Mask(&ctx.B, m).Offset(pos),
	)
	return op.Layer(
		drawing,
		outline,
		info,
	)
}

// splineRasterizer accumulates the preview from planned spline knots
// as they stream by, so the raster costs no walk of its own.
type splineRasterizer struct {
	preview *op.BitMask
	seg     bspline.Segment
	samples []bezier.Point
	plate   int
	spacing int

	// The engraved-sample hull, for an info line when no measured
	// drawing bounds exist (the text flow).
	min, max bezier.Point
	any      bool
}

func newSplineRasterizer(side int, params engrave.Params, plateSize PlateSize) *splineRasterizer {
	d := plateDims(plateSize, params.Millimeter)
	// One scale for both axes, anchored to the width every plate
	// format shares; the raster's height carries the aspect.
	h := (side*d.Y + d.X/2) / d.X
	return &splineRasterizer{
		preview: op.NewBitMask(image.Pt(side, h)),
		plate:   d.X,
		// Sample at a third of a pixel so plotted points form
		// contiguous strokes without a line rasterizer.
		spacing: max(1, d.X/(side*3)),
	}
}

func (r *splineRasterizer) knot(k bspline.Knot) {
	c, dt, engrave := r.seg.Knot(k)
	if dt == 0 || !engrave {
		return
	}
	r.samples = append(r.samples[:0], c.C0)
	r.samples = bezier.Sample(r.samples, c, r.spacing)
	side := r.preview.Bounds().Dx()
	for _, pt := range r.samples {
		r.preview.Set(pt.X*side/r.plate, pt.Y*side/r.plate)
		if !r.any {
			r.min, r.max, r.any = pt, pt, true
			continue
		}
		r.min.X = min(r.min.X, pt.X)
		r.min.Y = min(r.min.Y, pt.Y)
		r.max.X = max(r.max.X, pt.X)
		r.max.Y = max(r.max.Y, pt.Y)
	}
}
