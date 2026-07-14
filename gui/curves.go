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

// Limits on curves payloads. The knot caps bound the planner's
// per-stroke buffering and the time limit bounds unattended machine
// time; none of them are reachable by drawings of sane complexity.
const (
	curvesMaxStrokes     = 512
	curvesMaxKnots       = 16384
	curvesMaxStrokeKnots = 2048
	curvesMaxMinutes     = 45
)

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
	plate, err := validateCurves(cs, payload, params, ctx.Platform.DisplaySize())
	if err != nil {
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

// validateCurves parses and validates a curves payload and fills in
// the screen's preview. Unlike text plates, the payload dictates all
// geometry, so everything is checked up front: the confirm screen
// preview is the operator's only verification.
func validateCurves(cs *CurvesScreen, payload []byte, params engrave.Params, dims image.Point) (Plate, error) {
	drawing, err := curves.Parse(payload, params)
	if err != nil {
		return Plate{}, err
	}
	switch {
	case drawing.Strokes > curvesMaxStrokes:
		return Plate{}, fmt.Errorf("The drawing has %d strokes; at most %d are supported.", drawing.Strokes, curvesMaxStrokes)
	case drawing.Knots > curvesMaxKnots, drawing.MaxStrokeKnots > curvesMaxStrokeKnots:
		return Plate{}, fmt.Errorf("The drawing is too detailed to engrave.")
	}
	// The planner measures engraved segments only; bound every knot,
	// including travel, to keep the head on the plate.
	mm := params.Millimeter
	margin := bezier.Pt(safetyMargin*mm, safetyMargin*mm)
	sz := SquarePlate.Dims(mm)
	if !drawing.Bounds.In(bspline.Bounds{Min: margin, Max: sz.Sub(margin)}) {
		return Plate{}, ErrTooLarge
	}
	plate, err := toPlate(drawing.Engraving(), params)
	if err != nil {
		return Plate{}, err
	}
	tps := params.TicksPerSecond
	if plate.Duration > curvesMaxMinutes*60*tps {
		mins := (plate.Duration + 60*tps - 1) / (60 * tps)
		return Plate{}, fmt.Errorf("The engraving would run %d minutes; at most %d are allowed.", mins, curvesMaxMinutes)
	}
	cs.init(plate, drawing, params, dims)
	return plate, nil
}

type CurvesScreen struct {
	preview *curvesPreview
	info    string
}

func (s *CurvesScreen) init(plate Plate, drawing *curves.Drawing, params engrave.Params, dims image.Point) {
	side := previewSide(dims)
	s.preview = rasterizeSpline(plate.Spline, params, side)
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

func rasterizeSpline(spline bspline.Curve, params engrave.Params, side int) *curvesPreview {
	p := &curvesPreview{
		sz:   image.Pt(side, side),
		bits: make([]uint32, (side*side+31)/32),
	}
	plate := SquarePlate.Dims(params.Millimeter).X
	// Sample at a third of a pixel so plotted points form contiguous
	// strokes without a line rasterizer.
	spacing := max(1, plate/(side*3))
	var samples []bezier.Point
	var seg bspline.Segment
	for k := range spline {
		c, dt, engrave := seg.Knot(k)
		if dt == 0 || !engrave {
			continue
		}
		samples = append(samples[:0], c.C0)
		samples = bezier.Sample(samples, c, spacing)
		for _, pt := range samples {
			p.set(pt.X*side/plate, pt.Y*side/plate)
		}
	}
	return p
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
