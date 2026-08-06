// package gui implements the SeedHammer controller user interface.
package gui

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"io"
	"log"
	"math"
	"runtime"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/btcsuite/btcd/btcutil/v2/hdkeychain"
	"github.com/btcsuite/btcd/chaincfg/v2"
	qr "github.com/seedhammer/kortschak-qr"
	"seedhammer.com/backup"
	"seedhammer.com/bc/ur"
	"seedhammer.com/bc/urtypes"
	"seedhammer.com/bezier"
	"seedhammer.com/bip32"
	"seedhammer.com/bip380"
	"seedhammer.com/bip39"
	"seedhammer.com/bspline"
	"seedhammer.com/codex32"
	"seedhammer.com/curves"
	"seedhammer.com/engrave"
	"seedhammer.com/font/constant"
	"seedhammer.com/font/sh"
	"seedhammer.com/gui/assets"
	"seedhammer.com/gui/layout"
	"seedhammer.com/gui/op"
	"seedhammer.com/gui/saver"
	"seedhammer.com/gui/text"
	"seedhammer.com/gui/widget"
	"seedhammer.com/nip19"
	"seedhammer.com/nonstandard"
	"seedhammer.com/seedqr"
	slip39words "seedhammer.com/slip39"
)

var ErrTooLarge = errors.New("backup: data does not fit plate")

// safetyMargin is the distance in mm that must be kept free of
// engraving.
const safetyMargin = 3

const (
	cornerRadius    = 5
	buttonPadX      = 6
	buttonPadY      = 1
	keyCornerRadius = 3
	keyLineWidth    = 1
	keyPadX         = 3
	keyPadY         = 4
)

type Context struct {
	Platform      Platform
	Styles        Styles
	Wakeup        time.Time
	Done          bool
	FrameCallback func(op.Op)
	B             op.Buffer

	Router EventRouter
}

func (c *Context) Frame(op op.Op) {
	if f := c.FrameCallback; f != nil {
		f(op)
	}
	c.B.Reset()
}

func NewContext(pl Platform) *Context {
	c := &Context{
		Platform: pl,
		Styles:   NewStyles(),
	}
	return c
}

func (c *Context) WakeupAt(t time.Time) {
	if c.Wakeup.IsZero() || t.Before(c.Wakeup) {
		c.Wakeup = t
	}
}

func (c *Context) Reset() {
	c.Wakeup = time.Time{}
	// Immediately wake up to process remaining events.
	if c.Router.Reset() {
		c.Wakeup = time.Now()
	}
}

type InputTracker struct {
	Pressed [MaxButton]bool
	clicked [MaxButton]bool
	repeats [MaxButton]time.Time
}

func (t *InputTracker) Next(c *Context, filters ...Filter) (Event, bool) {
	now := time.Now()
	for _, btn := range []Button{Up, Down, Right, Left} {
		if !t.Pressed[btn] {
			t.repeats[btn] = time.Time{}
			continue
		}
		wakeup := t.repeats[btn]
		if wakeup.IsZero() {
			wakeup = now.Add(repeatStartDelay)
		}
		repeat := !now.Before(wakeup)
		if repeat {
			wakeup = now.Add(repeatDelay)
		}
		t.repeats[btn] = wakeup
		c.WakeupAt(wakeup)
		if repeat {
			return ButtonEvent{Button: btn, Pressed: true}.Event(), true
		}
	}

	e, ok := c.Router.Next(filters...)
	if !ok {
		return Event{}, false
	}
	if e, ok := e.AsButton(); ok {
		if int(e.Button) < len(t.clicked) {
			t.clicked[e.Button] = !e.Pressed && t.Pressed[e.Button]
			t.Pressed[e.Button] = e.Pressed
		}
	}
	return e, true
}

const widestWord = "TOMORROW"

type program int

const (
	backupWallet program = iota
	qaProgram
)

type richText struct {
	Content op.Op
	Y       int
}

func (r *richText) Add(b *op.Buffer, style text.Style, width int, col color.RGBA, str string) {
	r.Addf(b, style, width, col, "%s", str)
}

func (r *richText) Addf(b *op.Buffer, style text.Style, width int, col color.RGBA, format string, args ...any) {
	m := style.Face.Metrics()
	offy := r.Y + m.Ascent.Ceil()
	lheight := style.LineHeight()
	l := &text.Layout{
		MaxWidth: width,
		Style:    style,
	}
	for {
		g, ok := l.Next(format, args...)
		if !ok {
			break
		}
		if g.Rune == '\n' {
			offy += lheight
			continue
		}
		off := image.Pt(g.Dot.Round(), offy)
		r.Content = op.Layer(
			r.Content,
			op.Compose(
				op.Color(b, col),
				op.Glyph(b, style.Face, g.Rune),
			).Offset(off),
		)
	}
	r.Y = offy + m.Descent.Ceil()
}

func deriveMasterKey(m bip39.Mnemonic, passphrase string, net *chaincfg.Params) (*hdkeychain.ExtendedKey, bool) {
	seed := bip39.MnemonicSeed(m, passphrase)
	mk, err := hdkeychain.NewMaster(seed, net)
	// Err is only non-nil if the seed generates an invalid key, or we made a mistake.
	// According to [0] the odds of encountering a seed that generates
	// an invalid key by chance is 1 in 2^127.
	//
	// [0] https://bitcoin.stackexchange.com/questions/53180/bip-32-seed-resulting-in-an-invalid-private-key
	return mk, err == nil
}

type ErrorScreen struct {
	Title string
	Body  string
	w     Warning
	ok    Clickable
}

func (s *ErrorScreen) Layout(ctx *Context, th *Colors, dims image.Point) (op.Op, bool) {
	s.ok.Button = Button3
	if s.ok.Clicked(ctx) {
		return op.Op{}, true
	}
	nav, _ := layoutNavigation(&ctx.B, th, dims, NavButton{Clickable: &s.ok, Style: StylePrimary, Icon: assets.IconCheckmark})
	content := s.w.Layout(ctx, th, dims, s.Title, s.Body)
	return op.Layer(nav, content), false
}

type ConfirmWarningScreen struct {
	Title string
	Body  string
	Icon  image.RGBA64Image

	cancelBtn  Clickable
	confirmBtn Clickable
	pressed    bool
	warning    Warning
	confirm    ConfirmDelay
}

type Warning struct {
	scroll  int
	txtclip int
	inp     InputTracker
}

type ConfirmResult int

const (
	ConfirmNone ConfirmResult = iota
	ConfirmNo
	ConfirmYes
)

type ConfirmDelay struct {
	timeout time.Time
}

func (c *ConfirmDelay) Start(ctx *Context, delay time.Duration) {
	c.timeout = time.Now().Add(delay)
}

func (c *ConfirmDelay) Progress(ctx *Context) float32 {
	if c.timeout.IsZero() {
		return 0.
	}
	now := time.Now()
	d := c.timeout.Sub(now)
	if d <= 0 {
		return 1.
	}
	ctx.Platform.Wakeup()
	return 1. - float32(d.Seconds()/confirmDelay.Seconds())
}

const confirmDelay = 1 * time.Second

func (w *Warning) Layout(ctx *Context, th *Colors, dims image.Point, title, txt string) op.Op {
	for {
		e, ok := w.inp.Next(ctx, ButtonFilter(Up), ButtonFilter(Down))
		if !ok {
			break
		}
		if e, ok := e.AsButton(); ok {
			switch e.Button {
			case Up:
				if e.Pressed {
					w.scroll -= w.txtclip / 2
				}
			case Down:
				if e.Pressed {
					w.scroll += w.txtclip / 2
				}
			}
		}
	}
	const btnMargin = 4
	const boxMargin = 6

	btnOff := assets.NavBtnPrimary.Bounds().Dx() + btnMargin
	bodyClip := image.Rectangle{
		Min: image.Pt(boxMargin, leadingSize),
		Max: image.Pt(dims.X-btnOff, dims.Y-boxMargin),
	}
	body, bodysz := widget.Labelw(&ctx.B, ctx.Styles.body, bodyClip.Dx(), th.Text, txt)
	w.txtclip = bodyClip.Dy()
	maxScroll := bodysz.Y - (bodyClip.Dy() - 2*scrollFadeDist)
	if w.scroll > maxScroll {
		w.scroll = maxScroll
	}
	if w.scroll < 0 {
		w.scroll = 0
	}
	body = body.Offset(image.Pt(bodyClip.Min.X, bodyClip.Min.Y+scrollFadeDist-w.scroll))
	body = fadeClip(&ctx.B, body, image.Rectangle(bodyClip))

	titleOp, _ := layoutTitle(ctx, dims.X, th.Text, title)
	return op.Layer(
		body,
		titleOp,
		op.Color(&ctx.B, th.Background),
	)
}

// noticeScreen explains something and takes a plain yes or no. It is
// ConfirmWarningScreen without the hold, because a hold earns its place
// when an action cannot be undone, and what this gates can be redrawn or
// backed out of.
type noticeScreen struct {
	Title string
	Body  string
	Icon  image.RGBA64Image

	cancelBtn  Clickable
	confirmBtn Clickable
	warning    Warning
}

func (s *noticeScreen) Layout(ctx *Context, th *Colors, dims image.Point) (op.Op, ConfirmResult) {
	cancelBtn := s.cancelBtn.For(Button1)
	confirmBtn := s.confirmBtn.For(Button3, Center)
	if cancelBtn.Clicked(ctx) {
		return op.Op{}, ConfirmNo
	}
	if confirmBtn.Clicked(ctx) {
		return op.Op{}, ConfirmYes
	}
	nav, _ := layoutNavigation(&ctx.B, th, dims, []NavButton{
		{Clickable: cancelBtn, Style: StyleSecondary, Icon: assets.IconBack},
		{Clickable: confirmBtn, Style: StylePrimary, Icon: s.Icon},
	}...)
	return op.Layer(nav, s.warning.Layout(ctx, th, dims, s.Title, s.Body)), ConfirmNone
}

func (s *ConfirmWarningScreen) Layout(ctx *Context, th *Colors, dims image.Point) (op.Op, ConfirmResult) {
	cancelBtn := s.cancelBtn.For(Button1)
	confirmBtn := s.confirmBtn.For(Button3, Center)
	if cancelBtn.Clicked(ctx) {
		return op.Op{}, ConfirmNo
	}
	for {
		if _, ok := confirmBtn.Next(ctx); !ok {
			break
		}
		if confirmBtn.Pressed != s.pressed {
			s.pressed = confirmBtn.Pressed
			if s.pressed {
				s.confirm.Start(ctx, confirmDelay)
			} else {
				s.confirm = ConfirmDelay{}
			}
		}
	}
	progress := s.confirm.Progress(ctx)
	if progress == 1 {
		return op.Op{}, ConfirmYes
	}
	nav, _ := layoutNavigation(&ctx.B, th, dims, []NavButton{
		{Clickable: cancelBtn, Style: StyleSecondary, Icon: assets.IconBack},
		{Clickable: confirmBtn, Style: StylePrimary, Icon: s.Icon, Progress: progress},
	}...)
	content := s.warning.Layout(ctx, th, dims, s.Title, s.Body)
	return op.Layer(nav, content), ConfirmNone
}

type ProgressImage struct {
	Progress float32
	Src      image.RGBA64Image
}

func (p *ProgressImage) Op(buf *op.Buffer) op.MaskOp {
	return op.ParamImageMask(buf, progressImageGen, []any{p.Src}, []uint32{math.Float32bits(p.Progress)})
}

func (p *ProgressImage) At(x, y int) color.Color {
	return p.RGBA64At(x, y)
}

func (p *ProgressImage) RGBA64At(x, y int) color.RGBA64 {
	b := p.Bounds()
	c := b.Max.Add(b.Min).Div(2)
	d := image.Pt(x, y).Sub(c)
	angle := float32(math.Atan2(float64(d.X), float64(d.Y)))
	angle = math.Pi - angle
	if angle > 2*math.Pi*p.Progress {
		return color.RGBA64{}
	}
	return p.Src.RGBA64At(x, y)
}

func (p *ProgressImage) ColorModel() color.Model {
	return p.Src.ColorModel()
}

func (p *ProgressImage) Bounds() image.Rectangle {
	return p.Src.Bounds()
}

var progressImageGen = op.RegisterParameterizedImage(func() op.ParameterizedImage {
	img := new(ProgressImage)
	return func(args []uint32, refs []any) image.Image {
		img.Src = refs[0].(image.RGBA64Image)
		img.Progress = math.Float32frombits(args[0])
		return img
	}
})

func NewErrorScreen(err error) *ErrorScreen {
	switch {
	case errors.Is(err, ErrTooLarge):
		return &ErrorScreen{
			Title: "Too Large",
			Body:  "The engraving cannot fit any plate size.",
		}
	default:
		return &ErrorScreen{
			Title: "Error",
			Body:  err.Error(),
		}
	}
}

// fitDescriptor selects the descriptor engravings that fit the
// plate, without planning any of them: each candidate layout is
// walked for the hull of its engraved marks, the figure the
// planner's measure would report, and rejected on the plate margin
// alone. The brute-force ladder used to plan every failing attempt,
// which on a dense multisig descriptor churned megabytes of planner
// state through the device heap before the choice screen could
// appear. The chosen variant is planned after the operator picks it;
// see planDescriptorPlate. The QR variants carry an all-dark
// stand-in of the code's exact size: a real code's finder patterns
// pin its hull to the full square, so the stand-in measures
// identically, and the fit skips the encoder's transient churn,
// which exhausted the device heap before the first progress frame
// (the stand-in costs a bitmap; encoding churns megabytes to build
// one). qrText is the string the stand-in sized, for the real
// encode after the choice. pump, if not nil, observes the ladder
// attempts and cancels by returning false.
func fitDescriptor(params engrave.Params, plateSize PlateSize, desc *bip380.Descriptor, pump func(done, total int) bool) (labels []string, texts []backup.Text, qrText string, err error) {
	enc := desc.Encode()
	// The bare encoding is the checksummed one minus the "#xxxxxxxx"
	// suffix; strip it instead of encoding the descriptor twice.
	qrText = enc
	if i := strings.LastIndexByte(enc, '#'); i >= 0 {
		qrText = enc[:i]
	}
	qrSize, err := qr.MinSize(qrText, qr.L)
	if err != nil {
		return nil, nil, "", err
	}
	qrc := darkCode(qrSize)
	// A titled wallet names its descriptor plate the way the share
	// plates already do, so a drawer of plates sorts by eye. The QR-only
	// variant stays bare: its point is the minimal plate.
	body := enc
	if title := backup.TitleString(sh.Font, desc.Title); title != "" {
		body = title + "\n" + enc
	}
	type textEngraving struct {
		Label     string
		Paragraph backup.Paragraph
	}

	engravings := []textEngraving{
		{
			"TEXT + QR",
			backup.Paragraph{Text: body, QR: qrc},
		},
		{
			"TEXT ONLY",
			backup.Paragraph{Text: body},
		},
		{
			"QR ONLY",
			backup.Paragraph{QR: qrc},
		},
	}
	// For each variant, fall back to smaller text and then to finer
	// QR modules until the engraving fits the plate. QR module size
	// outranks text size because engraved QR codes are the hardest
	// element to scan.
	fontSizes := backup.FontSizes
	qrScales := []int{3, 2}
	// A scale whose lone code spans more than the engravable box
	// fails every ladder cell that carries it. The code's dot
	// centers span (modules·scale-1)·stroke, a floor under any
	// variant's measured hull, so a scale dropped here only skips
	// full layout walks the margin check was going to reject.
	box := plateBounds(params, plateSize)
	span := min(box.Max.X-box.Min.X, box.Max.Y-box.Min.Y)
	fitScales := make([]int, 0, len(qrScales))
	for _, s := range qrScales {
		if (qrSize*s-1)*params.StrokeWidth <= span {
			fitScales = append(fitScales, s)
		}
	}
	var validLabels []string
	var validTexts []backup.Text

	ladder := func(e textEngraving) ([]int, []float32) {
		sizes := fontSizes
		if e.Paragraph.Text == "" {
			// The text size doesn't affect a lone QR.
			sizes = fontSizes[:1]
		}
		scales := fitScales
		if e.Paragraph.QR == nil {
			scales = qrScales[:1]
		}
		return scales, sizes
	}
	attempts, total := 0, 0
	for _, e := range engravings {
		scales, sizes := ladder(e)
		total += len(scales) * len(sizes)
	}
	for _, e := range engravings {
		scales, sizes := ladder(e)
	search:
		for si, scale := range scales {
			for zi, size := range sizes {
				if attempts++; pump != nil && !pump(attempts, total) {
					return nil, nil, "", errPlanCanceled
				}
				p := e.Paragraph
				p.QRScale = scale
				descPlate := backup.Text{
					Size:       plateSize,
					Paragraphs: []backup.Paragraph{p},
					Font:       sh.Font,
					FontSize:   size,
				}
				if !layoutFits(backup.EngraveText(params, descPlate), params, plateSize) {
					continue
				}
				validLabels = append(validLabels, e.Label)
				validTexts = append(validTexts, descPlate)
				// Credit the ladder cells the accept skips, so the
				// gauge reaches its total instead of ending mid-scale.
				attempts += len(scales)*len(sizes) - (si*len(sizes) + zi + 1)
				if pump != nil && !pump(attempts, total) {
					return nil, nil, "", errPlanCanceled
				}
				break search
			}
		}
	}
	if len(validTexts) == 0 {
		return nil, nil, "", ErrTooLarge
	}
	return validLabels, validTexts, qrText, nil
}

// darkCode is an all-dark stand-in for a QR code of the given size,
// for measuring the space a code occupies without encoding one. A
// real code's finder patterns pin its hull to the full square, so
// layouts measure the same against either.
func darkCode(size int) *qr.Code {
	stride := (size + 7) / 8
	bitmap := make([]byte, stride*size)
	for i := range bitmap {
		bitmap[i] = 0xff
	}
	return &qr.Code{Bitmap: bitmap, Size: size, Stride: stride}
}

// planDescriptorPlate plans a variant chosen from a fit ladder,
// first swapping each paragraph's stand-in code for the real
// encoding of its qrTexts entry. The encodes run in the plan's
// worker, behind the progress screen: the heap-heaviest step of the
// whole flow, deferred until the operator picked a variant. The
// preview raster fills stroke by stroke under the progress label and
// carries into the returned view for the engrave screen.
func planDescriptorPlate(ctx *Context, th *Colors, params engrave.Params, plateSize PlateSize, txt backup.Text, qrTexts []string, title string) (Plate, *CurvesScreen, error) {
	cs := &CurvesScreen{title: title}
	r := newSplineRasterizer(previewSide(ctx.Platform.DisplaySize(), plateSize), params, plateSize)
	cs.preview = r.preview
	plate, err := runJob(ctx, th, func(pump func(done, total int) bool) (Plate, error) {
		collected := false
		for i := range txt.Paragraphs {
			p := &txt.Paragraphs[i]
			if p.QR == nil {
				continue
			}
			if !collected {
				// The fit walk leaves the heap strewn with collectable
				// garbage; reclaim it before the encoder's contiguous
				// allocations, which a fragmented heap cannot satisfy.
				runtime.GC()
				collected = true
			}
			qrc, err := qr.Encode(qrTexts[i], qr.L)
			if err != nil {
				return Plate{}, err
			}
			p.QR = qrc
		}
		return planPlateWalk(backup.EngraveText(params, txt), params, plateSize, pump, r.knot)
	}, planFrame(ctx, th, cs.Draw))
	if err != nil {
		return Plate{}, nil, err
	}
	cs.initText(plate, r, params)
	return plate, cs, nil
}

// shareText composes cosigner k's descriptor share plate: a pairing
// header (plate number, cosigner fingerprint, wallet title) so the
// share physically stays with the right cosigner's seed plate, then
// every UR share as text wrapped around its code. The codes are
// all-dark stand-ins sized by qr.MinSize; planDescriptorPlate swaps
// in the real encodes, keeping the fit ladder encode-free like the
// single-plate path.
func shareText(desc *bip380.Descriptor, data []byte, k int, fontSize float32, scale int) (backup.Text, []string, error) {
	urs := ur.Split(ur.Data{Data: data, Threshold: desc.Threshold, Shards: len(desc.Keys)}, k)
	header := fmt.Sprintf("%d/%d", k+1, len(desc.Keys))
	if mfp := desc.Keys[k].MasterFingerprint; mfp != 0 {
		header = fmt.Sprintf("%s %.8X", header, mfp)
	}
	if title := backup.TitleString(sh.Font, desc.Title); title != "" {
		header += " " + title
	}
	txt := backup.Text{Font: sh.Font, FontSize: fontSize}
	qrTexts := make([]string, len(urs))
	for i, u := range urs {
		body := u
		if i == 0 {
			body = header + "\n" + u
		}
		// Level L, like every descriptor code on this machine: the
		// two-UR quorums (2-of-4, 3-of-5) stack two codes plus the
		// header on one plate, and level M's extra modules push that
		// stack past the engravable span.
		size, err := qr.MinSize(u, qr.L)
		if err != nil {
			return backup.Text{}, nil, err
		}
		txt.Paragraphs = append(txt.Paragraphs, backup.Paragraph{
			Text:    body,
			QR:      darkCode(size),
			QRScale: scale,
		})
		qrTexts[i] = u
	}
	return txt, qrTexts, nil
}

// fitShares picks the single ladder cell — QR scale outranking font
// size, mirroring fitDescriptor — at which EVERY cosigner's share
// plate fits, so a split set engraves as a matched family instead of
// each plate at whatever size its share happens to reach. It returns
// the descriptor's CBOR encoding, the input to ur.Split, alongside
// the chosen cell.
func fitShares(params engrave.Params, desc *bip380.Descriptor, pump func(done, total int) bool) (data []byte, fontSize float32, scale int, err error) {
	data = urtypes.EncodeDescriptor(desc)
	n := len(desc.Keys)
	// The widest code across all shares pre-drops scales the plate
	// cannot hold, as fitDescriptor does for its lone code.
	maxQR := 0
	for k := range n {
		for _, u := range ur.Split(ur.Data{Data: data, Threshold: desc.Threshold, Shards: n}, k) {
			size, err := qr.MinSize(u, qr.L)
			if err != nil {
				return nil, 0, 0, err
			}
			maxQR = max(maxQR, size)
		}
	}
	box := plateBounds(params, SquarePlate)
	span := min(box.Max.X-box.Min.X, box.Max.Y-box.Min.Y)
	qrScales := []int{3, 2}
	fitScales := make([]int, 0, len(qrScales))
	for _, s := range qrScales {
		if (maxQR*s-1)*params.StrokeWidth <= span {
			fitScales = append(fitScales, s)
		}
	}
	attempts := 0
	total := len(fitScales) * len(backup.FontSizes) * n
	for _, sc := range fitScales {
		for _, size := range backup.FontSizes {
			all := true
			for k := range n {
				if attempts++; pump != nil && !pump(attempts, total) {
					return nil, 0, 0, errPlanCanceled
				}
				txt, _, err := shareText(desc, data, k, size, sc)
				if err != nil {
					return nil, 0, 0, err
				}
				if !layoutFits(backup.EngraveText(params, txt), params, SquarePlate) {
					all = false
					break
				}
			}
			if all {
				if pump != nil && !pump(total, total) {
					return nil, 0, 0, errPlanCanceled
				}
				return data, size, sc, nil
			}
		}
	}
	return nil, 0, 0, ErrTooLarge
}

// fitText chooses the largest font size that holds the text without
// re-breaking any line the composition deliberately sized. Every
// line caps the fit at the largest ladder size whose columns hold
// it unwrapped, and the smallest cap over the composition is the
// ceiling: a line composed one short of a grid's columns pins the
// plate to that grid, so a later overflowing line wraps at the
// anchored size instead of re-flowing the whole plate into a bigger
// font. Lines longer than every grid cap nothing — they wrap at
// whatever size wins, so a lone overlong payload still engraves at
// the largest size whose grid holds it wrapped. A composition that
// fits some grid unwrapped keeps exactly the old fit: with every
// line capped, nothing wraps at or under the ceiling and the search
// reduces to the grid check. The returned slice descends from the
// chosen size for the fallback ladder; wrapped reports that the
// engraved layout re-breaks the composed lines, so a caller can
// warn.
func fitText(params engrave.Params, plateSize PlateSize, text string) (sizes []float32, wrapped bool, err error) {
	cpls := make([]int, len(backup.FontSizes))
	for i, size := range backup.FontSizes {
		cpls[i] = backup.CharsPerLine(params, sh.Font, size)
	}
	// FontSizes descends, so the smallest cap is the largest index.
	ceiling := 0
	for start := 0; start <= len(text); {
		end := start
		for end < len(text) && text[end] != '\n' {
			end++
		}
		n := end - start
		c := -1
		for i := range cpls {
			if n <= cpls[i] {
				c = i
				break
			}
		}
		if c < 0 {
			wrapped = true // longer than every grid: wraps at any size, binds nothing
		} else {
			ceiling = max(ceiling, c)
		}
		start = end + 1
	}
	for i := ceiling; i < len(backup.FontSizes); i++ {
		size := backup.FontSizes[i]
		if wrappedLines(params, text, size) <= backup.LinesPerPlate(params, plateSize, size) {
			return backup.FontSizes[i:], wrapped, nil
		}
	}
	return nil, false, ErrTooLarge
}

// wrappedLines counts the plate lines text occupies at the given
// size when lines longer than the grid row wrap, mirroring
// EngraveText's own chunking.
func wrappedLines(params engrave.Params, text string, size float32) int {
	cpl := max(1, backup.CharsPerLine(params, sh.Font, size))
	lines := 0
	for len(text) > 0 {
		seg := text
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			seg = text[:i]
			text = text[i+1:]
		} else {
			text = ""
		}
		lines += max(1, (len(seg)+cpl-1)/cpl)
	}
	return lines
}

// validateText builds a plate from free-form text at the largest
// fitting size; planText is its progress-screen twin for the device
// flow — a fit change in one must land in the other.
func validateText(params engrave.Params, plateSize PlateSize, text string) (Plate, error) {
	sizes, _, err := fitText(params, plateSize, text)
	if err != nil {
		return Plate{}, err
	}
	var lastErr error
	for _, size := range sizes {
		plan := backup.EngraveText(params, backup.Text{
			Size:       plateSize,
			Paragraphs: []backup.Paragraph{{Text: text}},
			Font:       sh.Font,
			FontSize:   size,
		})
		plate, err := toPlate(plan, params, plateSize)
		if err != nil {
			lastErr = err
			continue
		}
		return plate, nil
	}
	return Plate{}, lastErr
}

// planText is validateText behind the progress screen: same fit,
// same ladder, but the plan walk runs in the background and the back
// button cancels. The preview rasterizer rides the walk — the plate
// fills stroke by stroke under the progress label, the same raster
// path scanned curves drawings get — and the returned screen carries
// the finished layout for the plate confirm.
func planText(ctx *Context, th *Colors, params engrave.Params, plateSize PlateSize, text string) (Plate, *CurvesScreen, error) {
	sizes, _, err := fitText(params, plateSize, text)
	if err != nil {
		return Plate{}, nil, err
	}
	dims := ctx.Platform.DisplaySize()
	cs := &CurvesScreen{title: "Engrave Text"}
	var lastErr error
	for _, size := range sizes {
		plan := backup.EngraveText(params, backup.Text{
			Size:       plateSize,
			Paragraphs: []backup.Paragraph{{Text: text}},
			Font:       sh.Font,
			FontSize:   size,
		})
		// A fresh raster per ladder size: a failed larger fit must not
		// leave its strokes in the preview.
		r := newSplineRasterizer(previewSide(dims, plateSize), params, plateSize)
		cs.preview = r.preview
		plate, err := planPlate(ctx, th, cs.Draw, plan, params, plateSize, r.knot)
		if err != nil {
			if errors.Is(err, errPlanCanceled) {
				return Plate{}, nil, err
			}
			lastErr = err
			continue
		}
		cs.initText(plate, r, params)
		return plate, cs, nil
	}
	return Plate{}, nil, lastErr
}

type Plate struct {
	Size     PlateSize
	Duration uint
	// Spline is the planned engraving in the plate's own frame: the
	// preview, the recorded plate and the duration all read it.
	Spline bspline.Curve
	// Machine is the engraver's plan: the same commands planned in
	// the machine frame, so the approach from the homing origin is
	// real, budgeted, needle-up travel. Nil for display-only plates.
	Machine bspline.Curve
}

// machinePlan places a plate-local command stream in the machine
// frame before planning. The machine origin is the square plate's
// top-left corner and only the top edge moves between formats:
// smaller plates keep the bottom and side edges, so their frame
// starts lower by the height difference. The offset must precede the
// planner: a planned spline carries tick budgets, and geometry moved
// after planning is distance the stepper can only chase with the
// needle following the original schedule.
func machinePlan(params engrave.Params, plateSize PlateSize, plan engrave.Engraving) engrave.Engraving {
	dy := (SquarePlate.Dims().Y - plateSize.Dims().Y) * params.Millimeter
	if dy == 0 {
		return plan
	}
	return func(yield func(engrave.Command) bool) {
		t := engrave.NewTransform(yield)
		off := t.Offset(0, dy)
		plan(off.Yield)
	}
}

// machineBounds is plateBounds in the machine frame: the band the
// engraver plan must stay inside.
func machineBounds(params engrave.Params, plateSize PlateSize) bspline.Bounds {
	b := plateBounds(params, plateSize)
	dy := (SquarePlate.Dims().Y - plateSize.Dims().Y) * params.Millimeter
	b.Min.Y += dy
	b.Max.Y += dy
	return b
}

// seedPlate does the plate-size-independent work of a seed plate —
// the passphrase-derived fingerprint and the SeedQR — once, so the
// per-size plans afterwards are cheap. The derivation is seconds of
// device time; callers run it behind a progress screen.
func seedPlate(m bip39.Mnemonic, passphrase, title string) (backup.Seed, error) {
	// The fingerprint names the wallet the plates actually open, which
	// with a passphrase is not the one the words alone would.
	mfp, err := masterFingerprintFor(m, passphrase, &chaincfg.MainNetParams)
	if err != nil {
		return backup.Seed{}, err
	}
	qrc, err := qr.Encode(string(seedqr.QR(m)), qr.M)
	if err != nil {
		return backup.Seed{}, err
	}
	words := make([]string, len(m))
	for i, w := range m {
		words[i] = bip39.LabelFor(w)
	}
	return backup.Seed{
		// The layout has reserved this spot since v1; the SH2 flows
		// simply never filled it. A too-wide title cannot overflow: the
		// plan's bounds check refuses the plate, and the small-plate
		// offer already rides that same fit.
		Title:             title,
		Mnemonic:          words,
		ShortestWord:      bip39.ShortestWord,
		LongestWord:       bip39.LongestWord,
		QR:                qrc,
		MasterFingerprint: mfp,
		Font:              constant.Font,
	}, nil
}

func engraveSeed(params engrave.Params, plateSize PlateSize, m bip39.Mnemonic, passphrase, title string) (engrave.Engraving, error) {
	seedDesc, err := seedPlate(m, passphrase, title)
	if err != nil {
		return nil, err
	}
	seedDesc.Size = plateSize
	return backup.EngraveSeed(params, seedDesc)
}

func masterFingerprintFor(m bip39.Mnemonic, passphrase string, network *chaincfg.Params) (uint32, error) {
	mk, ok := deriveMasterKey(m, passphrase, network)
	if !ok {
		return 0, errors.New("failed to derive mnemonic master key")
	}
	pkey, err := mk.ECPubKey()
	if err != nil {
		return 0, err
	}
	return bip32.Fingerprint(pkey), nil
}

func isEmptyMnemonic(m bip39.Mnemonic) bool {
	for _, w := range m {
		if w != -1 {
			return false
		}
	}
	return true
}

func emptySLIP39Mnemonic(nwords int) slip39words.Mnemonic {
	m := make(slip39words.Mnemonic, nwords)
	for i := range m {
		m[i] = -1
	}
	return m
}

func emptyBIP39Mnemonic(nwords int) bip39.Mnemonic {
	m := make(bip39.Mnemonic, nwords)
	for i := range m {
		m[i] = -1
	}
	return m
}

const scrollFadeDist = 16

func fadeClip(b *op.Buffer, o op.Op, r image.Rectangle) op.Op {
	// op.ParamImageOp(ops, scrollMask, true, r, nil, nil)
	return o.Offset(image.Pt(0, 0))
}

// var scrollMask = op.RegisterParameterizedImage(func(args op.ImageArguments, x, y int) color.RGBA64 {
// 	alpha := 0xffff
// 	if d := y - args.Bounds.Min.Y; d < scrollFadeDist {
// 		alpha = 0xffff * d / scrollFadeDist
// 	} else if d := args.Bounds.Max.Y - y; d < scrollFadeDist {
// 		alpha = 0xffff * d / scrollFadeDist
// 	}
// 	a16 := uint16(alpha)
// 	return color.RGBA64{A: a16}
// })

const wordKeys = "qwertyuiop\nasdfghjkl\nzxcvbnm"

func inputWordsFlow(ctx *Context, th *Colors, mnemonic bip39.Mnemonic, selected int) {
	kbd := NewKeyboard(ctx, wordKeys)
	wordLabel := ""
	backBtn := &Clickable{Button: Button1}
	okBtn := &Clickable{Button: Button2}
	// The offer to draw the final word shares Button2 with the checkmark.
	// They can never both apply: the offer requires an empty fragment,
	// and bip39.Complete("") is false, so the checkmark is not drawn then.
	pickBtn := &Clickable{Button: Button2}
	longest := wordBoxSize(ctx, th)
	for !ctx.Done {
		for kbd.Update(ctx) {
			updateValidBIP39Keys(kbd.Fragment, kbd.allKeys)
			wordLabel = kbd.Fragment
			if completedWord, complete := bip39.Complete(wordLabel); complete {
				wordLabel = bip39.LabelFor(completedWord)
			}
		}
		if backBtn.Clicked(ctx) {
			return
		}
		if lastWordOffer(mnemonic, selected, kbd.Fragment) {
			if pickBtn.Clicked(ctx) {
				if w, ok := pickLastWordFlow(ctx, th, mnemonic, longest); ok {
					mnemonic[selected] = w
					return
				}
				continue
			}
		} else {
			for okBtn.Clicked(ctx) {
				w, complete := bip39.Complete(kbd.Fragment)
				if !complete {
					continue
				}
				kbd.Clear()
				wordLabel = ""
				updateValidBIP39Keys("", kbd.allKeys)
				mnemonic[selected] = w
				for {
					selected++
					if selected == len(mnemonic) {
						return
					}
					if mnemonic[selected] == -1 {
						break
					}
				}
			}
		}
		// Evaluated after the handler above may have advanced to the
		// final word. Nothing redraws until the next event, so the frame
		// that lands on that word has to carry the offer already.
		offer := lastWordOffer(mnemonic, selected, kbd.Fragment)
		dims := ctx.Platform.DisplaySize()

		screen := layout.Rectangle{Max: dims}
		_, content := screen.CutTop(leadingSize)
		content, _ = content.CutBottom(8)

		kbdOp, kbdsz := kbd.Layout(ctx, th)
		kbdOp = kbdOp.Offset(content.S(kbdsz))

		top, _ := content.CutBottom(kbdsz.Y)
		txtBg := wordBox(ctx, th, top, longest, selected+1, wordLabel)

		// The band between the word and the keyboard is empty on every
		// other word, so the offer costs no layout elsewhere.
		hint := op.Op{}
		if offer {
			hintOp, hintsz := widget.Labelw(&ctx.B, ctx.Styles.lead, top.Dx(), th.Text, lastWordHint)
			hint = hintOp.Offset(top.S(hintsz))
		}

		nav, _ := layoutNavigation(&ctx.B, th, dims, []NavButton{{Clickable: backBtn, Style: StyleSecondary, Icon: assets.IconBack}}...)
		switch {
		case offer:
			// An info glyph, not a pencil: it opens an explanation, and
			// a pencil in this slot would read as "the device fills this
			// in for you", which is the misreading to avoid.
			nav2, _ := layoutNavigation(&ctx.B, th, dims, []NavButton{{Clickable: pickBtn, Style: StyleSecondary, Icon: assets.IconInfo}}...)
			nav = op.Layer(nav, nav2)
		default:
			if _, complete := bip39.Complete(kbd.Fragment); complete {
				nav2, _ := layoutNavigation(&ctx.B, th, dims, []NavButton{{Clickable: okBtn, Style: StylePrimary, Icon: assets.IconCheckmark}}...)
				nav = op.Layer(
					nav,
					nav2,
				)
			}
		}
		title, _ := layoutTitle(ctx, dims.X, th.Text, "Input Words")
		ctx.Frame(op.Layer(
			kbdOp,
			txtBg,
			hint,
			nav,
			title,
			op.Color(&ctx.B, th.Background),
		))
	}
}

// lastWordOffer reports whether the device can complete the phrase. Only
// the final word has a set of valid completions, only an empty box can
// take one, and every other word must be in, because those words are
// what determine the completions.
func lastWordOffer(mnemonic bip39.Mnemonic, selected int, frag string) bool {
	return selected == len(mnemonic)-1 && frag == "" &&
		!slices.Contains(mnemonic[:selected], bip39.Word(-1))
}

// Copy for the device-completed final word. It never says calculate,
// find, recover or missing: a user who has forgotten their own last word
// must not read this as an offer to retrieve it.
const (
	lastWordHint      = "New seed? Get a random last word."
	lastWordGateTitle = "New Seed?"
	lastWordGateBody  = "The device picks the last word at random.\n\n" +
		"It is not the word you forgot. A different last word is a different wallet."
	// Not "press for another". Rerolling until a word looks right is
	// choosing, and the drawing rules forbid exactly that for the tiles.
	// The button stays for a genuine misdraw.
	lastWordLead = "A new wallet, not a recovered word.\nTake the first word drawn."
)

// wordBoxSize measures the plate that shows one numbered seed word. It
// is sized for the widest number and word so the plate neither jumps
// between words nor moves between screens, and it is measured once per
// flow because the frame loop must not allocate.
func wordBoxSize(ctx *Context, th *Colors) image.Point {
	_, sz := widget.Labelf(nil, ctx.Styles.word, th.Background, "%2d: %s", 24, widestWord)
	return sz
}

func wordBox(ctx *Context, th *Colors, area layout.Rectangle, longest image.Point, n int, word string) op.Op {
	r := image.Rectangle{Max: longest}
	r.Min.Y -= 3 + buttonPadY
	r.Max.Y += buttonPadY
	r.Min.X -= buttonPadX
	r.Max.X += buttonPadX
	lbl, _ := widget.Labelf(&ctx.B, ctx.Styles.word, th.Background, "%2d: %s", n, word)
	return op.Layer(
		lbl,
		op.Compose(
			op.Color(&ctx.B, th.Text),
			op.RoundedRect2(&ctx.B, r, cornerRadius),
		),
	).Offset(area.Center(longest))
}

// pickLastWordFlow gates on an explanation, then draws a final word that
// gives mnemonic a valid checksum and offers to draw another. It reports
// false if the user backs out, leaving mnemonic untouched.
//
// The gate comes before the draw on purpose. A user who has lost their
// own last word is most tempted at the moment a plausible one is on
// screen, so the sentence that says this is a different wallet has to
// land while the screen is still empty.
func pickLastWordFlow(ctx *Context, th *Colors, mnemonic bip39.Mnemonic, longest image.Point) (bip39.Word, bool) {
	gate := &noticeScreen{
		Title: strings.ToTitle(lastWordGateTitle),
		Body:  lastWordGateBody,
		// The hold button confirms; the icon is the action, as the
		// discard warning's is. An "i" there reads as "more information".
		Icon: assets.IconCheckmark,
	}
	for !ctx.Done {
		dims := ctx.Platform.DisplaySize()
		d, res := gate.Layout(ctx, th, dims)
		switch res {
		case ConfirmNo:
			return -1, false
		case ConfirmYes:
			return drawLastWordFlow(ctx, th, mnemonic, longest)
		}
		ctx.Frame(d)
	}
	return -1, false
}

func drawLastWordFlow(ctx *Context, th *Colors, mnemonic bip39.Mnemonic, longest image.Point) (bip39.Word, bool) {
	prefix := mnemonic[:len(mnemonic)-1]
	draw := func() (bip39.Word, bool) {
		if Rand == nil {
			// A build problem, not a hardware one: cmd/controller
			// installs the source at startup.
			showError(ctx, th, errNoRNG, blankScreen)
			return -1, false
		}
		w, err := bip39.PickLastWord(prefix, Rand)
		if err != nil {
			showError(ctx, th, errNoEntropy, blankScreen)
			return -1, false
		}
		return w, true
	}
	word, ok := draw()
	if !ok {
		return -1, false
	}
	backBtn := &Clickable{Button: Button1}
	rerollBtn := &Clickable{Button: Button2}
	acceptBtn := &Clickable{Button: Button3, AltButton: Center}
	for !ctx.Done {
		if backBtn.Clicked(ctx) {
			return -1, false
		}
		if rerollBtn.Clicked(ctx) {
			if w, ok := draw(); ok {
				word = w
			} else {
				return -1, false
			}
		}
		if acceptBtn.Clicked(ctx) {
			return word, true
		}
		dims := ctx.Platform.DisplaySize()
		screen := layout.Rectangle{Max: dims}
		_, content := screen.CutTop(leadingSize)
		content, _ = content.CutBottom(8)

		leadOp, leadsz := widget.Labelw(&ctx.B, ctx.Styles.lead, content.Dx(), th.Text, lastWordLead)
		top, _ := content.CutBottom(leadsz.Y)
		box := wordBox(ctx, th, top, longest, len(mnemonic), bip39.LabelFor(word))

		nav, _ := layoutNavigation(&ctx.B, th, dims,
			NavButton{Clickable: backBtn, Style: StyleSecondary, Icon: assets.IconBack},
			NavButton{Clickable: rerollBtn, Style: StyleSecondary, Icon: assets.IconEdit},
			NavButton{Clickable: acceptBtn, Style: StylePrimary, Icon: assets.IconCheckmark},
		)
		title, _ := layoutTitlef(ctx, dims.X, th.Text, "Random Word %d", len(mnemonic))
		ctx.Frame(op.Layer(
			box,
			leadOp.Offset(content.S(leadsz)),
			nav,
			title,
			op.Color(&ctx.B, th.Background),
		))
	}
	return -1, false
}

func inputCodex32Flow(ctx *Context, th *Colors) (codex32.String, bool) {
	const alph = "1234567890\nqwertyup\nasdfghjk\nlzxcvnm"

	kbd := NewKeyboard(ctx, alph)
	backBtn := &Clickable{Button: Button1}
	okBtn := &Clickable{Button: Button2}
	var share codex32.String
	valid := false
	for !ctx.Done {
		for kbd.Update(ctx) {
			s, err := codex32.New(kbd.Fragment)
			share, valid = s, err == nil
		}
		if backBtn.Clicked(ctx) {
			break
		}
		if valid && okBtn.Clicked(ctx) {
			return share, true
		}
		dims := ctx.Platform.DisplaySize()

		screen := layout.Rectangle{Max: dims}
		_, content := screen.CutTop(leadingSize)
		content, _ = content.CutBottom(8)

		kbdOp, kbdsz := kbd.Layout(ctx, th)
		kbdOp = kbdOp.Offset(content.S(kbdsz))

		word, frgSize := widget.Labelw(&ctx.B, ctx.Styles.word, dims.X-50, th.Background, kbd.Fragment)
		frgSize.X = max(frgSize.X, 100)
		r := image.Rectangle{Max: frgSize}
		r.Min.Y -= 3
		r.Max.Y += buttonPadY
		r.Min.X -= buttonPadX
		r.Max.X += buttonPadX
		top, _ := content.CutBottom(kbdsz.Y)
		word = op.Layer(
			word,
			op.Compose(
				op.Color(&ctx.B, th.Text),
				op.RoundedRect2(&ctx.B, r, cornerRadius),
			),
		).Offset(top.Center(frgSize))

		nav, _ := layoutNavigation(&ctx.B, th, dims, []NavButton{{Clickable: backBtn, Style: StyleSecondary, Icon: assets.IconBack}}...)
		if valid {
			nav2, _ := layoutNavigation(&ctx.B, th, dims, []NavButton{{Clickable: okBtn, Style: StylePrimary, Icon: assets.IconCheckmark}}...)
			nav = op.Layer(nav, nav2)
		}
		title, _ := layoutTitle(ctx, dims.X, th.Text, "Input Codex32 Share")
		ctx.Frame(op.Layer(
			kbdOp,
			word,
			nav,
			title,
			op.Color(&ctx.B, th.Background),
		))
	}
	return codex32.String{}, false
}

func inputSLIP39Flow(ctx *Context, th *Colors, mnemonic slip39words.Mnemonic, selected int) bool {
	kbd := NewKeyboard(ctx, wordKeys)
	wordLabel := ""
	backBtn := &Clickable{Button: Button1}
	okBtn := &Clickable{Button: Button2}
	layoutWord := func(b *op.Buffer, n int, word string) (op.Op, image.Point) {
		style := ctx.Styles.word
		return widget.Labelf(b, style, th.Background, "%2d: %s", n, word)
	}
	const (
		widestWord = "WITHDRAW"
	)
	_, longest := layoutWord(nil, len(mnemonic), widestWord)
	var nvalid int
	for !ctx.Done {
		for kbd.Update(ctx) {
			nvalid = updateValidSLIP39Keys(kbd.Fragment, kbd.allKeys)
			wordLabel = kbd.Fragment
			if completedWord, complete := completeSLIP39Word(wordLabel, nvalid); complete {
				wordLabel = slip39words.LabelFor(completedWord)
			}
		}
		if backBtn.Clicked(ctx) {
			break
		}
		for okBtn.Clicked(ctx) {
			w, complete := completeSLIP39Word(kbd.Fragment, nvalid)
			if !complete {
				continue
			}
			kbd.Clear()
			wordLabel = ""
			nvalid = updateValidSLIP39Keys("", kbd.allKeys)
			mnemonic[selected] = w
			for {
				selected++
				if selected == len(mnemonic) {
					return true
				}
				if mnemonic[selected] == -1 {
					break
				}
			}
		}
		dims := ctx.Platform.DisplaySize()
		screen := layout.Rectangle{Max: dims}
		_, content := screen.CutTop(leadingSize)
		content, _ = content.CutBottom(8)

		kbdOp, kbdsz := kbd.Layout(ctx, th)
		kbdOp = kbdOp.Offset(content.S(kbdsz))

		word, _ := layoutWord(&ctx.B, selected+1, wordLabel)
		r := image.Rectangle{Max: longest}
		r.Min.Y -= 3
		r.Max.Y += buttonPadY
		r.Min.X -= buttonPadX
		r.Max.X += buttonPadX
		top, _ := content.CutBottom(kbdsz.Y)
		word = op.Layer(
			word,
			op.Compose(
				op.Color(&ctx.B, th.Text),
				op.RoundedRect2(&ctx.B, r, cornerRadius),
			),
		).Offset(top.Center(longest))

		nav, _ := layoutNavigation(&ctx.B, th, dims, []NavButton{{Clickable: backBtn, Style: StyleSecondary, Icon: assets.IconBack}}...)
		if _, complete := completeSLIP39Word(kbd.Fragment, nvalid); complete {
			nav2, _ := layoutNavigation(&ctx.B, th, dims, []NavButton{{Clickable: okBtn, Style: StylePrimary, Icon: assets.IconCheckmark}}...)
			nav = op.Layer(nav, nav2)
		}
		title, _ := layoutTitle(ctx, dims.X, th.Text, "Input Words")

		ctx.Frame(op.Layer(
			kbdOp,
			word,
			nav,
			title,
			op.Color(&ctx.B, th.Background),
		))
	}
	return false
}

type Keyboard struct {
	Fragment string

	// Verbatim types and draws runes as given. The word and share
	// keyboards want the uppercase the wordlists are written in; a
	// passphrase is case-sensitive and must not be folded.
	Verbatim bool
	// Layer is a key reported to the caller rather than typed, drawn as
	// an upward arrow. It carries no character so none is lost from the
	// alphabet, and it is how a passphrase reaches upper case and
	// symbols without a keyboard too tall for the screen.
	Layer bool

	keys      [][]keyboardKey
	widest    image.Point
	backspace image.Point
	size      image.Point

	row, col int
	inp      InputTracker

	allKeys []keyboardKey
}

type keyboardKey struct {
	r        rune
	disabled bool
	pos      image.Point
	clk      Clickable
}

func NewKeyboard(ctx *Context, alphabet string) *Keyboard {
	// Add backspace and end row.
	alphabet += "⌫\n"

	k := new(Keyboard)
	k.widest = ctx.Styles.keyboard.Measure(math.MaxInt, "W")
	bsb := assets.KeyBackspace.Bounds()
	bsWidth := bsb.Min.X*2 + bsb.Dx()
	k.backspace = image.Pt(max(bsWidth, k.widest.X), k.widest.Y)
	bgbnds := image.Rectangle{Max: k.widest}
	bgbnds.Min.X -= keyPadX
	bgbnds.Max.X += keyPadX
	bgbnds.Min.Y -= keyPadY
	bgbnds.Max.Y += keyPadY
	const margin = 2
	bgsz := bgbnds.Size().Add(image.Pt(margin, margin))
	longest := 0
	prevIdx := 0
	for _, r := range alphabet {
		if r == '\n' {
			row := k.allKeys[prevIdx:]
			prevIdx = len(k.allKeys)
			k.keys = append(k.keys, row)
			longest = max(longest, len(row))
			continue
		}
		k.allKeys = append(k.allKeys, keyboardKey{r: r})
	}
	maxw := longest*bgsz.X - margin
	allKeys := k.allKeys[:]
	for i, row := range k.keys {
		n := len(row)
		if i == len(k.keys)-1 {
			// Center row without the backspace key.
			n--
		}
		w := bgsz.X*n - margin
		off := image.Pt((maxw-w)/2, 0)
		k.keys[i] = allKeys[:len(row)]
		allKeys = allKeys[len(row):]
		for j := range row {
			pos := image.Pt(j*bgsz.X, i*bgsz.Y)
			pos = pos.Add(off)
			pos = pos.Sub(bgbnds.Min)
			k.keys[i][j].pos = pos
		}
	}
	k.size = image.Point{
		X: maxw,
		Y: len(k.keys)*bgsz.Y - margin,
	}
	k.Clear()
	return k
}

func (k *Keyboard) Clear() {
	k.Fragment = ""
	k.row = len(k.keys) / 2
	k.col = len(k.keys[k.row]) / 2
}

func completeSLIP39Word(frag string, nvalid int) (slip39words.Word, bool) {
	w, ok := slip39words.ClosestWord(frag)
	if !ok {
		return -1, false
	}
	// The word is complete if it's in the word list or is the only option.
	return w, nvalid == 1 || frag == slip39words.LabelFor(w)
}

func updateValidBIP39Keys(frag string, keys []keyboardKey) int {
	first, nvalid := bip39.Matches(frag)
	if nvalid == 0 {
		panic("invalid fragment")
	}
	mask := ^uint32(0)
	if nvalid > 1 {
		for w := first; w < first+bip39.Word(nvalid); w++ {
			suffix := bip39.LabelFor(w)[len(frag):]
			if len(suffix) > 0 {
				idx := unicode.ToLower(rune(suffix[0])) - 'a'
				mask &^= 1 << idx
			}
		}
	}
	updateValidKeys(mask, keys)
	return nvalid
}

func updateValidSLIP39Keys(frag string, keys []keyboardKey) int {
	mask := ^uint32(0)
	w, valid := slip39words.ClosestWord(frag)
	if !valid {
		panic("invalid fragment")
	}
	nvalid := 0
	for ; w < slip39words.NumWords; w++ {
		bip39w := slip39words.LabelFor(w)
		if !strings.HasPrefix(bip39w, frag) {
			break
		}
		nvalid++
		suffix := bip39w[len(frag):]
		if len(suffix) > 0 {
			idx := unicode.ToLower(rune(suffix[0])) - 'a'
			mask &^= 1 << idx
		}
	}
	if nvalid == 1 {
		mask = ^uint32(0)
	}
	updateValidKeys(mask, keys)
	return nvalid
}

func updateValidKeys(mask uint32, keys []keyboardKey) {
	for i := range keys {
		key := &keys[i]
		idx := key.r - 'a'
		if idx < 0 || idx >= 32 {
			continue
		}
		key.disabled = mask&(1<<idx) != 0
	}
}

func (k *Keyboard) Valid(key keyboardKey) bool {
	if key.r == '⌫' {
		return len(k.Fragment) > 0
	}
	return !key.disabled
}

func (k *Keyboard) Update(ctx *Context) bool {
	k.adjust(k.keys[k.row][k.col].r == '⌫')
	for i, row := range k.keys {
		for j := range row {
			key := &row[j]
			if k.Valid(*key) && key.clk.Clicked(ctx) {
				k.row, k.col = i, j
				k.rune()
				return true
			}
		}
	}
	for {
		e, ok := k.inp.Next(ctx, ButtonFilter(Left), ButtonFilter(Right), ButtonFilter(Up), ButtonFilter(Down), ButtonFilter(Center), RuneFilter(), ButtonFilter(Button3))
		if !ok {
			break
		}
		if e, ok := e.AsButton(); ok {
			if !e.Pressed {
				continue
			}
			switch e.Button {
			case Left:
				next := k.col
				for {
					next--
					if next == -1 {
						next = len(k.keys[k.row]) - 1
					}
					if !k.Valid(k.keys[k.row][next]) {
						continue
					}
					k.col = next
					k.adjust(true)
					break
				}
			case Right:
				next := k.col
				for {
					next++
					if next == len(k.keys[k.row]) {
						next = 0
					}
					if !k.Valid(k.keys[k.row][next]) {
						continue
					}
					k.col = next
					k.adjust(true)
					break
				}
			case Up:
				n := len(k.keys)
				next := k.row
				for {
					next = (next - 1 + n) % n
					if k.adjustCol(next) {
						k.adjust(true)
						break
					}
				}
			case Down:
				n := len(k.keys)
				next := k.row
				for {
					next = (next + 1) % n
					if k.adjustCol(next) {
						k.adjust(true)
						break
					}
				}
			case Center, Button3:
				k.rune()
				return true
			}
		}
		if e, ok := e.AsRune(); ok {
			// A Verbatim keyboard's layers hold both cases, so folding
			// here would make the upper-case layer drop every letter.
			r := e.Rune
			if r == '\n' || r == '\r' {
				r = newlineKey
			}
			if !k.Verbatim {
				r = unicode.ToLower(r)
			}
			for i, row := range k.keys {
				for j, key := range row {
					if key.r == r && k.Valid(key) {
						k.row, k.col = i, j
						k.rune()
						return true
					}
				}
			}
		}
	}
	return false
}

func (k *Keyboard) rune() {
	r := k.keys[k.row][k.col].r
	switch {
	case r == '⌫':
		_, n := utf8.DecodeLastRuneInString(k.Fragment)
		k.Fragment = k.Fragment[:len(k.Fragment)-n]
	case r == layerKey:
		k.Layer = true
	case r == newlineKey:
		k.Fragment += "\n"
	case k.Verbatim:
		k.Fragment += string(r)
	default:
		k.Fragment += string(unicode.ToUpper(r))
	}
}

// layerKey is outside printable ASCII so it cannot collide with a
// character a passphrase might contain.
const layerKey = '\x01'

// newlineKey is the return key. It is a sentinel for the same reason
// layerKey is, plus one of its own: rows of an alphabet split on '\n',
// so the key cannot be the literal newline it types.
const newlineKey = '\x02'

// adjust resets the row and column to the nearest valid key, if any.
func (k *Keyboard) adjust(allowBackspace bool) {
	dist := int(1e6)
	current := k.keys[k.row][k.col].pos
	found := false
	for i, row := range k.keys {
		j := 0
		for _, key := range row {
			if !k.Valid(key) || key.r == '⌫' && !allowBackspace {
				j++
				continue
			}
			p := key.pos
			d := p.Sub(current)
			d2 := d.X*d.X + d.Y*d.Y
			if d2 < dist {
				dist = d2
				k.row, k.col = i, j
				found = true
			}
			j++
		}
	}
	// Only if no other key was found, select backspace.
	if !found {
		k.row = len(k.keys) - 1
		k.col = len(k.keys[k.row]) - 1
	}
}

// adjustCol sets the column to the one nearest the x position.
func (k *Keyboard) adjustCol(row int) bool {
	dist := int(1e6)
	found := false
	x := k.keys[k.row][k.col].pos.X
	for i, r := range k.keys[row] {
		if !k.Valid(r) {
			continue
		}
		p := k.keys[row][i].pos
		found = true
		k.row = row
		d := p.X - x
		if d < 0 {
			d = -d
		}
		if d < dist {
			dist = d
			k.col = i
		}
	}
	return found
}

func (k *Keyboard) Layout(ctx *Context, th *Colors) (op.Op, image.Point) {
	var content op.Op
	for i, row := range k.keys {
		for j, key := range row {
			valid := k.Valid(key)
			bgsz := k.widest
			if key.r == '⌫' {
				bgsz = k.backspace
			}
			bgcol := th.Text
			style := ctx.Styles.keyboard
			col := th.Text
			active := false
			switch {
			case !valid:
				bgcol = mulAlpha(bgcol, theme.inactiveMask)
				col = bgcol
			case i == k.row && j == k.col:
				active = true
				col = th.Background
			}
			bgr := image.Rectangle{Max: bgsz}
			inpOp := op.Input(&ctx.B, &k.keys[i][j].clk).Clip(bgr)
			var keyOp op.Op
			var sz image.Point
			switch {
			case key.r == '⌫':
				icn := assets.KeyBackspace
				sz = image.Pt(k.backspace.X, icn.Bounds().Dy())
				keyOp = op.Compose(
					op.Color(&ctx.B, col),
					op.Mask(&ctx.B, icn),
				)
			case key.r == layerKey:
				icn := assets.ArrowUp
				sz = icn.Bounds().Size()
				keyOp = op.Compose(
					op.Color(&ctx.B, col),
					op.Mask(&ctx.B, icn),
				)
			case key.r == newlineKey:
				icn := assets.KeyReturn
				sz = icn.Bounds().Size()
				keyOp = op.Compose(
					op.Color(&ctx.B, col),
					op.Mask(&ctx.B, icn),
				)
			case k.Verbatim:
				keyOp, sz = widget.Labelf(&ctx.B, style, col, "%c", key.r)
			default:
				keyOp, sz = widget.Labelf(&ctx.B, style, col, "%c", unicode.ToUpper(key.r))
			}
			keyOp = keyOp.Offset(bgsz.Sub(sz).Div(2))
			bgr.Min.X -= keyPadX
			bgr.Max.X += keyPadX
			bgr.Min.Y -= keyPadY
			bgr.Max.Y += keyPadY
			bgOp := op.Color(&ctx.B, bgcol)
			var mask op.MaskOp
			if active {
				mask = op.RoundedRect2(&ctx.B, bgr, keyCornerRadius)
			} else {
				mask = op.RoundedOutline2(&ctx.B, bgr, keyCornerRadius, keyLineWidth)
			}
			btnOp := op.Layer(
				inpOp,
				keyOp,
				op.Compose(
					bgOp,
					mask,
				),
			).Offset(k.keys[i][j].pos)
			content = op.Layer(
				content, btnOp,
			)
		}
	}
	return content, k.size
}

func mulAlpha(col color.RGBA, a uint8) color.RGBA {
	col.R = uint8(uint(col.R) * uint(a) / 255)
	col.G = uint8(uint(col.G) * uint(a) / 255)
	col.B = uint8(uint(col.B) * uint(a) / 255)
	col.A = uint8(uint(col.A) * uint(a) / 255)
	return col
}

type ChoiceScreen struct {
	Title    string
	Lead     string
	Choices  []string
	children []Choice
	choice   int
}

type Choice struct {
	Size  image.Point
	W     op.Op
	click Clickable
}

func (s *ChoiceScreen) Choose(ctx *Context, th *Colors) (int, bool) {
	inp := new(InputTracker)
	cancelBtn := &Clickable{Button: Button1}
	chooseBtn := &Clickable{Button: Button3, AltButton: Center}
frames:
	for !ctx.Done {
		switch {
		case cancelBtn.Clicked(ctx):
			break frames
		case chooseBtn.Clicked(ctx):
			return s.choice, true
		}
		for i := range s.children {
			c := &s.children[i]
			if c.click.Clicked(ctx) {
				s.choice = i
			}
		}
		for {
			e, ok := inp.Next(ctx, ButtonFilter(Up), ButtonFilter(Down))
			if !ok {
				break
			}
			if e, ok := e.AsButton(); ok {
				switch e.Button {
				case Up:
					if e.Pressed {
						if s.choice > 0 {
							s.choice--
						}
					}
				case Down:
					if e.Pressed {
						if s.choice < len(s.Choices)-1 {
							s.choice++
						}
					}
				}
			}
		}

		dims := ctx.Platform.DisplaySize()
		nav, _ := layoutNavigation(&ctx.B, th, dims, []NavButton{
			{Clickable: cancelBtn, Style: StyleSecondary, Icon: assets.IconBack},
			{Clickable: chooseBtn, Style: StylePrimary, Icon: assets.IconCheckmark},
		}...)
		content := s.Draw(ctx, th, dims)
		ctx.Frame(op.Layer(nav, content))
	}
	return 0, false
}

func (s *ChoiceScreen) Draw(ctx *Context, th *Colors, dims image.Point) op.Op {
	r := layout.Rectangle{Max: dims}
	_, bottom := r.CutTop(leadingSize)
	leadOp, sz := widget.Labelw(&ctx.B, ctx.Styles.lead, dims.X-2*8, th.Text, s.Lead)
	content, lead := bottom.CutBottom(leadingSize)
	leadOp = leadOp.Offset(lead.Center(sz))

	content = content.Shrink(16, 0, 16, 0)

	if len(s.children) != len(s.Choices) {
		s.children = make([]Choice, len(s.Choices))
	}
	maxW := 0
	for i, c := range s.Choices {
		style := ctx.Styles.button
		col := th.Text
		if i == s.choice {
			col = th.Background
		}
		o, sz := widget.Label(&ctx.B, style, col, c)
		s.children[i].Size = sz
		s.children[i].W = o
		if sz.X > maxW {
			maxW = sz.X
		}
	}

	h := 0
	var children op.Op
	for i := range s.children {
		c := &s.children[i]
		xoff := (maxW-c.Size.X)/2 + buttonPadX
		pos := image.Pt(xoff, h)
		txt := c.W
		bg := image.Rectangle{Max: c.Size}
		bg.Min.X -= xoff
		bg.Max.X += xoff
		bg.Min.Y -= buttonPadY
		bg.Max.Y += buttonPadY
		if i == s.choice {
			txt = op.Layer(
				txt,
				op.Compose(
					op.Color(&ctx.B, th.Text),
					op.RoundedRect2(&ctx.B, bg, cornerRadius),
				),
			)
		}
		children = op.Layer(
			children,
			txt.Offset(pos),
			op.Input(&ctx.B, &c.click).Clip(bg).Offset(pos),
		)
		h += c.Size.Y
	}
	title, _ := layoutTitle(ctx, dims.X, th.Text, s.Title)

	return op.Layer(
		leadOp,
		children.Offset(content.Center(image.Pt(maxW, h))),
		title,
		op.Color(&ctx.B, th.Background),
	)
}

func uiFlow(ctx *Context, version string) {
	th := &descriptorTheme
	s := &StartScreen{
		Version: version,
	}
	for {
		act, ok := s.Flow(ctx, th)
		if !ok {
			continue
		}
		obj := act.scan
		if obj == nil {
			switch act.prog {
			case qaProgram:
				qaEngraveFlow(ctx)
				continue
			case backupWallet:
				mnemonic, ok := newInputFlow(ctx, th)
				if !ok {
					continue
				}
				obj = mnemonic
			}
		}
		if !engraveObjectFlow(ctx, th, obj) {
			s.Status = scanUnknownFormat
		}
	}
}

type StartScreen struct {
	Version     string
	Status      scanStatus
	prog        program
	scanTimeout time.Time
}

type startScreenAction struct {
	prog program
	scan any
}

const scanStatusTimeout = 1 * time.Second

func (m *StartScreen) Flow(ctx *Context, th *Colors) (startScreenAction, bool) {
	scans := make(chan scanResult, 1)
	stop := scanWorker(ctx, scans)
	defer stop()
	inp := new(InputTracker)
	selectBtn := &Clickable{Button: Button3, AltButton: Center}
	for !ctx.Done {
		if selectBtn.Clicked(ctx) {
			return startScreenAction{prog: m.prog}, true
		}
		select {
		case scan := <-scans:
			if time.Now().Before(m.scanTimeout) {
				m.Status = max(m.Status, scan.Status)
			} else {
				m.Status = scan.Status
			}
			m.scanTimeout = time.Now().Add(scanStatusTimeout)
			if scan.Object == nil && scan.Status == scanIdle {
				break
			}
			if cnt := scan.Object; cnt != nil {
				switch cnt := cnt.(type) {
				case debugCommand:
					switch cmd := cnt.Command; cmd {
					case "FOREVERLAURA!":
						return startScreenAction{prog: qaProgram}, true
					case "lock-boot":
						m.Status = scanIdle
						if err := ctx.Platform.LockBoot(); err != nil {
							log.Printf("lock-boot: %v", err)
							m.Status = scanFailed
						}
						continue
					default:
						log.Printf("unknown debug command: %q", cmd)
						m.Status = scanUnknownFormat
						continue
					}
				}
				return startScreenAction{scan: cnt}, true
			}
		default:
		}
		for {
			e, ok := inp.Next(ctx,
				ButtonFilter(Left),
				ButtonFilter(Right),
			)
			if !ok {
				break
			}
			if e, ok := e.AsButton(); ok {
				switch e.Button {
				case Left:
					if !e.Pressed {
						break
					}
					m.prog--
					if m.prog < 0 {
						m.prog = backupWallet
					}
				case Right:
					if !e.Pressed {
						break
					}
					m.prog++
					if m.prog > backupWallet {
						m.prog = 0
					}
				}
			}
		}
		dims := ctx.Platform.DisplaySize()
		nav, _ := layoutNavigation(&ctx.B, th, dims,
			NavButton{Clickable: selectBtn, Style: StylePrimary, Icon: assets.IconCheckmark},
		)
		content := m.draw(ctx, th, dims)
		ctx.Frame(op.Layer(nav, content))
	}
	return startScreenAction{}, false
}

func (m *StartScreen) draw(ctx *Context, th *Colors, dims image.Point) op.Op {
	var titleTxt string
	switch m.prog {
	case backupWallet:
		titleTxt = "Backup Wallet"
	}

	title, _ := layoutTitle(ctx, dims.X, th.Text, titleTxt)

	r := layout.Rectangle{Max: dims}
	content, sz := m.layout(&ctx.B, th, dims.X)
	content = content.Offset(r.Center(sz))

	inner, sz := layoutMainPager(&ctx.B, th, m.prog)
	_, middle := r.CutBottom(leadingSize)
	inner = inner.Offset(middle.Center(sz))
	sttxt := ""
	if time.Now().Before(m.scanTimeout) {
		ctx.WakeupAt(m.scanTimeout)
		sttxt = scanStatusText(m.Status)
	}
	subt, sz := widget.Labelw(&ctx.B, ctx.Styles.subtitle, 300, th.Text, sttxt)
	subt = subt.Offset(r.S(sz).Sub(image.Pt(0, 16)))

	ver, sz := widget.Labelw(&ctx.B, ctx.Styles.debug, 200, th.Text, m.Version)
	ver = ver.Offset(r.SE(sz.Add(image.Pt(4, 0))))
	logo, sz := widget.Labelw(&ctx.B, ctx.Styles.debug, 100, th.Text, "SeedHammer")
	logo = logo.Offset(r.SW(sz).Add(image.Pt(3, 0)))
	return op.Layer(
		title,
		content,
		inner,
		subt,
		ver, logo,
		op.Color(&ctx.B, th.Background),
	)
}

func layoutTitle(ctx *Context, width int, col color.RGBA, title string) (op.Op, image.Rectangle) {
	return layoutTitlef(ctx, width, col, "%s", title)
}

func layoutTitlef(ctx *Context, width int, col color.RGBA, format string, args ...any) (op.Op, image.Rectangle) {
	const margin = 8
	lbl, sz := widget.Labelwf(&ctx.B, ctx.Styles.title, width-2*16, col, format, args...)
	pos := image.Pt((width-sz.X)/2, margin)
	return lbl.Offset(pos), image.Rectangle{
		Min: pos,
		Max: pos.Add(sz),
	}
}

type ButtonStyle int

const (
	StyleNone ButtonStyle = iota
	StyleSecondary
	StylePrimary
)

type NavButton struct {
	Clickable *Clickable
	Style     ButtonStyle
	Icon      image.Image
	Progress  float32
}

func layoutNavigation(buf *op.Buffer, th *Colors, dims image.Point, btns ...NavButton) (op.Op, image.Rectangle) {
	navsz := assets.NavBtnPrimary.Bounds().Size()
	button := func(buf *op.Buffer, b NavButton, t op.Tag, pressed bool) op.Op {
		if b.Style == StyleNone {
			return op.Op{}
		}
		content := op.Input(buf, t).Clip(assets.NavBtnPrimary.Bounds())
		if b.Progress == 0 && pressed {
			content = op.Layer(
				content,
				op.Compose(
					op.Color(buf, color.RGBA{A: theme.activeMask}),
					op.Mask(buf, assets.NavBtnPrimary),
				),
			)
		}
		const offset = 9
		var icn op.MaskOp
		if b.Progress > 0 {
			icn = (&ProgressImage{
				Progress: b.Progress,
				Src:      assets.IconProgress,
			}).Op(buf)
		} else {
			icn = op.Mask(buf, b.Icon)
		}
		content = op.Layer(
			content,
			op.Compose(
				op.Color(buf, th.Text),
				icn.Offset(image.Pt(offset, offset)),
			),
		)
		switch b.Style {
		case StyleSecondary:
			content = op.Layer(
				content,
				op.Compose(
					op.Color(buf, th.Text),
					op.Mask(buf, assets.NavBtnSecondary),
				),
				op.Compose(
					op.Color(buf, th.Background),
					op.Mask(buf, assets.NavBtnPrimary),
				),
			)
		case StylePrimary:
			content = op.Layer(
				content,
				op.Compose(
					op.Color(buf, th.Primary),
					op.Mask(buf, assets.NavBtnPrimary),
				),
			)
		}
		return content
	}
	btnsz := assets.NavBtnPrimary.Bounds().Size()
	ys := [3]int{
		leadingSize,
		(dims.Y - btnsz.Y) / 2,
		dims.Y - leadingSize - btnsz.Y,
	}
	var r image.Rectangle
	var content op.Op
	for _, b := range btns {
		clk := b.Clickable
		idx := int(clk.Button - Button1)
		pressed := clk.Pressed && clk.Entered
		bop := button(buf, b, clk, pressed)
		y := ys[idx]
		pos := image.Pt(dims.X-btnsz.X, y)
		content = op.Layer(content, bop.Offset(pos))
		r = r.Union(image.Rectangle{
			Min: pos,
			Max: pos.Add(navsz),
		})
	}
	return content, r
}

func (m *StartScreen) layout(buf *op.Buffer, th *Colors, width int) (op.Op, image.Point) {
	const margin = 16

	left := op.Compose(
		op.Color(buf, th.Text),
		op.Mask(buf, assets.ArrowLeft),
	)
	var h layout.Align
	leftsz := h.Add(assets.ArrowLeft.Bounds().Size())
	left = left.Offset(image.Pt(margin, h.Y(leftsz)))

	right := op.Compose(
		op.Color(buf, th.Text),
		op.Mask(buf, assets.ArrowRight),
	)
	rightsz := h.Add(assets.ArrowRight.Bounds().Size())
	right = right.Offset(image.Pt(width-margin-rightsz.X, h.Y(rightsz)))

	plates, sz := layoutMainPlates(buf, m.prog)
	contentsz := h.Add(sz)

	content := plates.Offset(image.Pt((width-contentsz.X)/2, 8+h.Y(contentsz)))
	const npage = int(backupWallet) + 1
	if npage > 1 {
		content = op.Layer(content, left, right)
	}

	return content, image.Pt(width, h.Size.Y)
}

func layoutMainPlates(buf *op.Buffer, page program) (op.Op, image.Point) {
	switch page {
	case backupWallet:
		img := assets.Hammer
		o := op.Image(buf, img)
		return o, img.Bounds().Size()
	}
	panic("invalid page")
}

func layoutMainPager(buf *op.Buffer, th *Colors, page program) (op.Op, image.Point) {
	const npages = int(backupWallet) + 1
	const space = 4
	if npages <= 1 {
		return op.Op{}, image.Point{}
	}
	sz := assets.CircleFilled.Bounds().Size()
	var content op.Op
	for i := range npages {
		mask := assets.Circle
		if i == int(page) {
			mask = assets.CircleFilled
		}
		content = op.Layer(content,
			op.Compose(
				op.Color(buf, th.Text),
				op.Mask(buf, mask),
			).Offset(image.Pt((sz.X+space)*i, 0)),
		)
	}
	return content, image.Pt((sz.X+space)*npages-space, sz.Y)
}

func engraveObjectFlow(ctx *Context, th *Colors, obj any) bool {
	switch scan := obj.(type) {
	case bip39.Mnemonic:
		backupWalletFlow(ctx, th, scan)
		// SLIP39 share engraving stays disabled by choice: parsing a
		// scanned share needs the external go-slip39 module, against the
		// no-dependencies rule (see scan.go). The engrave flow below is
		// kept for reference should a dep-free parser ever land.
	// case slip39.Share:
	// 	w, err := scan.Words()
	// 	// No space for secrets > 128 bits.
	// 	const maximumLength = 20
	// 	if err != nil || len(w) > maximumLength {
	// 		return false
	// 	}
	// 	title := fmt.Sprintf("%d #%d 1/%d", scan.Identifier, scan.MemberIndex+1, scan.MemberThreshold)
	// 	seedDesc := backup.Seed{
	// 		Mnemonic:     w,
	// 		ShortestWord: slip39words.ShortestWord,
	// 		LongestWord:  slip39words.LongestWord,
	// 		Title:        title,
	// 		Font:         constant.Font,
	// 	}
	// 	params := ctx.Platform.EngraverParams()
	// 	seedSide, err := backup.EngraveSeed(params, seedDesc)
	// 	if err != nil {
	// 		return false
	// 	}
	// 	plate, err := toPlate(seedSide, params)
	// 	if err != nil {
	// 		return false
	// 	}
	// 	for {
	// 		completed := NewEngraveScreen(ctx, plate).Engrave(ctx, ops, &engraveTheme)
	// 		if completed {
	// 			return true
	// 		}
	// 	}
	case codex32.String:
		id, _, _ := scan.Split()
		s := backup.SeedString{
			Title: id,
			Seed:  scan.String(),
			Font:  constant.Font,
		}
		backupSeedStringFlow(ctx, th, s)
	case *bip380.Descriptor:
		descriptorFlow(ctx, th, scan)
	case plainText:
		textFlow(ctx, th, scan, "")
	case curvesPayload:
		curvesFlow(ctx, th, scan)
	case nip19.Key:
		nostrFlow(ctx, th, scan)
	default:
		return false
	}
	return true
}

func backupWalletFlow(ctx *Context, th *Colors, mnemonic bip39.Mnemonic) {
	ss := new(SeedScreen)
	// Asked once and carried across retries. Every path back to the seed
	// screen would otherwise re-enter passphraseFlow, which starts from
	// an empty field, and retyping a confirmed passphrase from memory is
	// how a second and different one gets made. That is the hazard the
	// edit path inside passphraseFlow already avoids.
	var passphrase string
	var havePassphrase bool
	// Also asked once: the title is the set's name, and every plate of
	// the set must carry the same one.
	var title string
	var haveTitle bool
	for {
		if !ss.Confirm(ctx, th, mnemonic) {
			return
		}
		if !havePassphrase {
			p, ok := passphraseFlow(ctx, th, mnemonic)
			if !ok {
				continue
			}
			passphrase, havePassphrase = p, true
		}
		if !haveTitle {
			t, ok := titleFlow(ctx, th, false, title)
			if !ok {
				continue
			}
			title, haveTitle = t, true
		}
		// Three plates, each offered and each declinable. A seed already
		// on metal may need only a descriptor; a descriptor can be cut
		// again years later without touching the seed plate.
		if !seedPlateFlow(ctx, th, ss, mnemonic, passphrase, title, "") {
			continue
		}
		path := walletDescriptorFlow(ctx, th, mnemonic, passphrase, title)
		passphrasePlateFlow(ctx, th, mnemonic, passphrase, path, title)
		return
	}
}

// askPlateSize offers the plate choice when the layout also fits the
// small plate; content that only fits the square one keeps its plate
// without a question. The small plate leads: the question only
// appears because it fits, so it is the likely answer. Backing out
// reports false.
func askPlateSize(ctx *Context, th *Colors, fitsSmall bool) (PlateSize, bool) {
	if !fitsSmall {
		return SquarePlate, true
	}
	cs := &ChoiceScreen{
		Title:   "Plate Size",
		Lead:    "The engraving fits the small plate.",
		Choices: []string{"SMALL PLATE", "SQUARE PLATE"},
	}
	choice, ok := cs.Choose(ctx, th)
	if !ok {
		return SquarePlate, false
	}
	if choice == 0 {
		return SmallPlate, true
	}
	return SquarePlate, true
}

// seedPlateFlow offers the seed plate and reports whether to carry on
// to the other two: skipping does, and so does engraving, while a
// cancelled engrave goes back to the seed screen so it can be tried
// again.
func seedPlateFlow(ctx *Context, th *Colors, ss *SeedScreen, mnemonic bip39.Mnemonic, passphrase, title, lead string) (ok bool) {
	// Engraving sits first so the selection lands on it, the same reason
	// walletDescriptorFlow puts its SKIP last. Someone who reached this
	// screen came to cut a seed plate; skipping is the exception, and an
	// exception should cost a keypress rather than be the default.
	if lead == "" {
		lead = "Engrave the seed phrase?"
	}
	cs := &ChoiceScreen{
		Title:   "Seed Plate",
		Lead:    lead,
		Choices: []string{"ENGRAVE SEED", "SKIP"},
	}
	choice, ok := cs.Choose(ctx, th)
	if !ok {
		return false
	}
	if choice == 1 {
		return true
	}
	params := ctx.Platform.EngraverParams()
	// The fingerprint derivation takes seconds with a passphrase; run
	// it behind the progress screen so the plate question that follows
	// lands on a live screen, not a queued press.
	seedDesc, err := runJob(ctx, th, func(pump func(done, total int) bool) (backup.Seed, error) {
		return seedPlate(mnemonic, passphrase, title)
	}, planFrame(ctx, th, func(ctx *Context, th *Colors, dims image.Point) op.Op {
		return ss.Draw(ctx, th, dims, mnemonic)
	}))
	var plate Plate
	var view *CurvesScreen
	if err == nil {
		seedDesc.Size = SmallPlate
		small, serr := backup.EngraveSeed(params, seedDesc)
		fitsSmall := serr == nil && layoutFits(small, params, SmallPlate)
		plateSize, sizeOK := askPlateSize(ctx, th, fitsSmall)
		if !sizeOK {
			return false
		}
		seedDesc.Size = plateSize
		var plan engrave.Engraving
		plan, err = backup.EngraveSeed(params, seedDesc)
		if err == nil {
			plate, view, err = planPreviewPlate(ctx, th, "Engrave Seed", plan, params, plateSize)
		}
	}
	if err != nil {
		if errors.Is(err, errPlanCanceled) {
			return false
		}
		showError(ctx, th, err, func(ctx *Context, th *Colors, dims image.Point) op.Op {
			return ss.Draw(ctx, th, dims, mnemonic)
		})
		return false
	}
	done := NewEngraveScreen(ctx, plate, view).Engrave(ctx, &engraveTheme)
	return done
}

func backupSeedStringFlow(ctx *Context, th *Colors, s backup.SeedString) {
	params := ctx.Platform.EngraverParams()
	s.Size = SmallPlate
	small, err := backup.EngraveSeedString(params, s)
	if err != nil {
		showError(ctx, th, err, blankScreen)
		return
	}
	plateSize, ok := askPlateSize(ctx, th, layoutFits(small, params, SmallPlate))
	if !ok {
		return
	}
	s.Size = plateSize
	p, err := backup.EngraveSeedString(params, s)
	if err != nil {
		showError(ctx, th, err, blankScreen)
		return
	}
	plate, view, err := planPreviewPlate(ctx, th, "Engrave Seed", p, params, plateSize)
	if err != nil {
		if !errors.Is(err, errPlanCanceled) {
			showError(ctx, th, err, blankScreen)
		}
		return
	}
	NewEngraveScreen(ctx, plate, view).Engrave(ctx, &engraveTheme)
}

func descriptorFlow(ctx *Context, th *Colors, desc *bip380.Descriptor) {
	ds := &DescriptorScreen{
		Descriptor: desc,
	}
	for {
		pp, split, ok := ds.Confirm(ctx, th)
		if !ok {
			break
		}
		if split != nil {
			if splitEngraveFlow(ctx, th, ds, split) {
				return
			}
			continue
		}
		completed := NewEngraveScreen(ctx, pp.plate, pp.view).Engrave(ctx, &engraveTheme)
		if completed {
			return
		}
	}
}

// splitEngraveFlow cuts the descriptor backup as one plate per
// cosigner. Every plate opens on an insert prompt — the physical
// pause to load a blank, with SKIP to pass over plates already cut
// in an earlier, aborted run — and closes on the another-copy
// prompt, so extra duplicates of a plate cost one choice instead of
// a rescan. Plates plan just in time, one layout and one code held
// at a time; a copy re-engraves the planned plate without
// replanning. It reports whether the operator finished the set;
// false unwinds to the descriptor screen.
func splitEngraveFlow(ctx *Context, th *Colors, ds *DescriptorScreen, sp *splitPlan) bool {
	params := ctx.Platform.EngraverParams()
	desc := ds.Descriptor
	n := len(desc.Keys)
	var plate Plate
	var view *CurvesScreen
	planned := false
	for k := 0; k < n; k++ {
		if !sp.copies {
			// Every cosigner's share is its own layout; full copies
			// share the one planned plate across the set.
			planned = false
		}
		for {
			// The insert instruction itself lives on the engrave
			// screen; the gate carries what that screen cannot know —
			// which cosigner's seed plate this share pairs with.
			lead := "Full descriptor copy"
			if !sp.copies {
				lead = "Descriptor share"
				if mfp := desc.Keys[k].MasterFingerprint; mfp != 0 {
					lead = fmt.Sprintf("For cosigner %.8X", mfp)
				}
			}
			gate := &ChoiceScreen{
				Title:   fmt.Sprintf("Plate %d of %d", k+1, n),
				Lead:    lead,
				Choices: []string{"ENGRAVE PLATE", "SKIP"},
			}
			g, ok := gate.Choose(ctx, th)
			if !ok {
				return false
			}
			if g == 1 {
				break
			}
			if !planned {
				txt, qrTexts, err := sp.plateContent(desc, k)
				if err == nil {
					plate, view, err = planDescriptorPlate(ctx, th, params, SquarePlate, txt, qrTexts, fmt.Sprintf("Plate %d of %d", k+1, n))
				}
				if err != nil {
					if errors.Is(err, errPlanCanceled) {
						continue
					}
					showError(ctx, th, err, ds.Draw)
					return false
				}
				planned = true
			}
			if !NewEngraveScreen(ctx, plate, view).Engrave(ctx, &engraveTheme) {
				continue
			}
			next := "NEXT PLATE"
			if k == n-1 {
				next = "DONE"
			}
			done := &ChoiceScreen{
				Title:   fmt.Sprintf("Plate %d of %d", k+1, n),
				Lead:    "Plate engraved",
				Choices: []string{next, "ANOTHER COPY"},
			}
			c, ok := done.Choose(ctx, th)
			if !ok {
				return false
			}
			if c == 1 {
				continue
			}
			break
		}
	}
	return true
}

func textFlow(ctx *Context, th *Colors, txt plainText, named string) bool {
	// The plan walk fills the plate preview behind the progress label,
	// then the engrave screen holds that preview — the composed lines
	// at the fitted size, wraps and all — with its dimensions and
	// duration. What the operator approves is the plate, not a
	// display-width rendering of the text. Completing the engrave
	// reports true; a refused plate, a cancelled plan or a backed-out
	// engrave screen reports false so a typed text can return to its
	// editor. A payload that names its plate skips the question: the
	// emitter already chose.
	params := ctx.Platform.EngraverParams()
	plateSize, ok := SquarePlate, true
	switch named {
	case curves.PlateSmall:
		plateSize = SmallPlate
	case curves.PlateSquare:
	default:
		_, _, smallErr := fitText(params, SmallPlate, string(txt))
		plateSize, ok = askPlateSize(ctx, th, smallErr == nil)
	}
	if !ok {
		return false
	}
	plate, preview, err := planText(ctx, th, params, plateSize, string(txt))
	if err != nil {
		if errors.Is(err, errPlanCanceled) {
			return false
		}
		showError(ctx, th, err, blankScreen)
		return false
	}
	preview.notice = textNotice(string(txt))
	return NewEngraveScreen(ctx, plate, preview).Engrave(ctx, &engraveTheme)
}

// NostrScreen confirms a scanned Nostr key before engraving. For an
// nsec, Npub holds the derived public key shown alongside the secret;
// for an npub, Npub is zero. The full key is shown — visual
// side-channel through the on-device screen is out of scope per the
// security model, and a partial render would prevent the user from
// verifying the scan.
type NostrScreen struct {
	Key  nip19.Key
	Npub nip19.Key
}

func (s *NostrScreen) Confirm(ctx *Context, th *Colors) bool {
	backBtn := &Clickable{Button: Button1}
	confirmBtn := &Clickable{Button: Button3}
	for !ctx.Done {
		if backBtn.Clicked(ctx) {
			return false
		}
		if confirmBtn.Clicked(ctx) {
			return true
		}
		dims := ctx.Platform.DisplaySize()
		nav, _ := layoutNavigation(&ctx.B, th, dims, []NavButton{
			{Clickable: backBtn, Style: StyleSecondary, Icon: assets.IconBack},
			{Clickable: confirmBtn, Style: StylePrimary, Icon: assets.IconCheckmark},
		}...)
		ctx.Frame(op.Layer(nav, s.Draw(ctx, th, dims)))
	}
	return false
}

func (s *NostrScreen) Draw(ctx *Context, th *Colors, dims image.Point) op.Op {
	const infoSpacing = 8

	r := layout.Rectangle{Max: dims}
	btnw := assets.NavBtnPrimary.Bounds().Dx()
	body := r.Shrink(leadingSize, btnw, 0, btnw)

	var bodytxt richText
	bodyst := ctx.Styles.body
	subst := ctx.Styles.subtitle
	switch s.Key.HRP {
	case nip19.HRPSec:
		bodytxt.Add(&ctx.B, subst, body.Dx(), th.Text, "Secret (nsec)")
		bodytxt.Add(&ctx.B, bodyst, body.Dx(), th.Text, s.Key.Bech32())
		bodytxt.Y += infoSpacing
		bodytxt.Add(&ctx.B, subst, body.Dx(), th.Text, "Public (npub)")
		bodytxt.Add(&ctx.B, bodyst, body.Dx(), th.Text, s.Npub.Bech32())
	case nip19.HRPPub:
		bodytxt.Add(&ctx.B, subst, body.Dx(), th.Text, "Public (npub)")
		bodytxt.Add(&ctx.B, bodyst, body.Dx(), th.Text, s.Key.Bech32())
	}
	bodyOp := bodytxt.Content.Offset(body.Min.Add(image.Pt(0, scrollFadeDist)))

	title := "Engrave Nostr Key"
	if s.Key.HRP == nip19.HRPPub {
		title = "Engrave Public Key"
	}
	titleOp, _ := layoutTitle(ctx, dims.X, th.Text, title)
	return op.Layer(
		bodyOp,
		titleOp,
		op.Color(&ctx.B, th.Background),
	)
}

var (
	errNoEntropy  = errors.New("The random number generator reported a fault. No word was drawn.")
	errNoRNG      = errors.New("This firmware has no random number generator.")
	errNsecPlate  = errors.New("The secret-key plate does not fit.")
	errNpubPlate  = errors.New("The public-key plate does not fit.")
	errNpubDerive = errors.New("Could not derive the public key.")
)

func nostrFlow(ctx *Context, th *Colors, key nip19.Key) {
	scr := &NostrScreen{Key: key}
	if key.HRP == nip19.HRPSec {
		npub, err := nip19.NpubFrom(key)
		if err != nil {
			showError(ctx, th, errNpubDerive, scr.Draw)
			return
		}
		scr.Npub = npub
		if !scr.Confirm(ctx, th) {
			return
		}
		if !nostrEngrave(ctx, th, scr, func(ps PlateSize) nostrPlan { return backupNsecPlan(ctx, key, ps) }) {
			return
		}
		// Re-use the confirmation screen for the public-plate prompt so
		// the user sees the same key info before the second engraving.
		scr.Key = npub
		scr.Npub = nip19.Key{}
		if !scr.Confirm(ctx, th) {
			return
		}
		nostrEngrave(ctx, th, scr, func(ps PlateSize) nostrPlan { return backupNpubPlan(ctx, npub, ps) })
	} else {
		if !scr.Confirm(ctx, th) {
			return
		}
		nostrEngrave(ctx, th, scr, func(ps PlateSize) nostrPlan { return backupNpubPlan(ctx, key, ps) })
	}
}

// nostrPlan pairs a plate plan with the error shown when it cannot fit.
type nostrPlan struct {
	plan engrave.Engraving
	err  error
	fail error
}

func backupNsecPlan(ctx *Context, nsec nip19.Key, plateSize PlateSize) nostrPlan {
	plan, err := backup.EngraveNsec(ctx.Platform.EngraverParams(), backup.Nsec{
		Size:  plateSize,
		Title: "NSEC",
		Key:   nsec,
		Font:  constant.Font,
	})
	return nostrPlan{plan: plan, err: err, fail: errNsecPlate}
}

func backupNpubPlan(ctx *Context, npub nip19.Key, plateSize PlateSize) nostrPlan {
	plan, err := backup.EngraveNpub(ctx.Platform.EngraverParams(), backup.Npub{
		Size:  plateSize,
		Title: "NPUB",
		Key:   npub,
		Font:  constant.Font,
	})
	return nostrPlan{plan: plan, err: err, fail: errNpubPlate}
}

func nostrEngrave(ctx *Context, th *Colors, scr *NostrScreen, mk func(PlateSize) nostrPlan) bool {
	params := ctx.Platform.EngraverParams()
	p := mk(SmallPlate)
	if p.err != nil {
		showError(ctx, th, p.fail, scr.Draw)
		return false
	}
	plateSize, ok := askPlateSize(ctx, th, layoutFits(p.plan, params, SmallPlate))
	if !ok {
		return false
	}
	if plateSize != SmallPlate {
		p = mk(plateSize)
		if p.err != nil {
			showError(ctx, th, p.fail, scr.Draw)
			return false
		}
	}
	title := "Engrave Nostr Key"
	if scr.Key.HRP == nip19.HRPPub {
		title = "Engrave Public Key"
	}
	plate, view, err := planPreviewPlate(ctx, th, title, p.plan, params, plateSize)
	if err != nil {
		if errors.Is(err, errPlanCanceled) {
			return false
		}
		showError(ctx, th, p.fail, scr.Draw)
		return false
	}
	return NewEngraveScreen(ctx, plate, view).Engrave(ctx, &engraveTheme)
}

func newInputFlow(ctx *Context, th *Colors) (any, bool) {
	for {
		cs := &ChoiceScreen{
			Title:   "Input",
			Lead:    "Choose what to enter",
			Choices: []string{"12 WORDS", "24 WORDS", "ENGRAVE TEXT", "MULTISIG WALLET" /* , "CODEX32", "SLIP-39" */},
		}
		for {
			choice, ok := cs.Choose(ctx, th)
			if !ok {
				return nil, false
			}
			switch choice {
			case 0, 1:
				mnemonic := emptyBIP39Mnemonic([]int{12, 24}[choice])
				inputWordsFlow(ctx, th, mnemonic, 0)
				if !isEmptyMnemonic(mnemonic) {
					return mnemonic, true
				}
			case 2:
				// Free text typed on the device engraves exactly like
				// a scanned text payload: the same canonicalization,
				// plan, confirm and engrave. Backing out of the
				// confirm, cancelling the plan or a refused plate
				// carries the text back into the editor; a completed
				// engrave ends at the start screen.
				txt := ""
				for {
					var ok bool
					txt, ok = inputTextFlow(ctx, th, "Engrave Text", &textLayers, txt)
					if !ok {
						break
					}
					t, ok := parsePlainText([]byte(txt))
					if !ok {
						// Nothing visible to engrave; keep editing.
						continue
					}
					if textFlow(ctx, th, t, "") {
						return nil, false
					}
				}
			case 3:
				// A finished wallet ends at the start screen; backing
				// out of the builder returns to this menu.
				if multisigWalletFlow(ctx, th) {
					return nil, false
				}
			case 4:
				s, ok := inputCodex32Flow(ctx, th)
				if ok {
					return s, true
				}
				// SLIP-39 keyboard entry stays disabled with the scan
				// path: producing a share to engrave needs go-slip39
				// (external dep, see scan.go). Kept for reference.
				// case 4:
				// 	mnemonic := emptySLIP39Mnemonic(20)
				// 	if ok := inputSLIP39Flow(ctx, th, mnemonic, 0); !ok {
				// 		break
				// 	}
				// 	share := new(strings.Builder)
				// 	for i, w := range mnemonic {
				// 		if i > 0 {
				// 			share.WriteByte(' ')
				// 		}
				// 		share.WriteString(slip39words.LabelFor(w))
				// 	}
				// 	s, err := slip39.ParseShare(share.String())
				// 	if err != nil {
				// 		break
				// 	}
				// 	return s, true
			}
		}
	}
}

type SeedScreen struct {
	// Title names the screen; empty keeps the single-seed flow's
	// "Engrave Seed", the multisig builder names the cosigner.
	Title    string
	selected int
	words    []Clickable
}

func (s *SeedScreen) Confirm(ctx *Context, th *Colors, mnemonic bip39.Mnemonic) bool {
	inp := new(InputTracker)
	backBtn := &Clickable{Button: Button1}
	editBtn := &Clickable{Button: Button2, AltButton: Center}
	confirmBtn := &Clickable{Button: Button3}
events:
	for !ctx.Done {
		for i := range s.words {
			c := &s.words[i]
			if c.Clicked(ctx) {
				s.selected = i
			}
		}
		if backBtn.Clicked(ctx) {
			if isEmptyMnemonic(mnemonic) {
				break
			}
			confirm := &ConfirmWarningScreen{
				Title: strings.ToTitle("Discard Seed?"),
				Body:  "Going back will discard the seed.\n\nHold button to confirm.",
				Icon:  assets.IconDiscard,
			}
			for !ctx.Done {
				dims := ctx.Platform.DisplaySize()
				d, res := confirm.Layout(ctx, th, dims)
				switch res {
				case ConfirmNo:
					continue events
				case ConfirmYes:
					return false
				}
				main := s.Draw(ctx, th, dims, mnemonic)
				ctx.Frame(op.Layer(d, main))
			}
		}
		if editBtn.Clicked(ctx) {
			inputWordsFlow(ctx, th, mnemonic, s.selected)
			continue
		}
		if confirmBtn.Clicked(ctx) {
			if !isMnemonicComplete(mnemonic) {
				continue
			}
			showErr := func(scr *ErrorScreen) {
				for !ctx.Done {
					dims := ctx.Platform.DisplaySize()
					d, dismissed := scr.Layout(ctx, th, dims)
					if dismissed {
						break
					}
					main := s.Draw(ctx, th, dims, mnemonic)
					ctx.Frame(op.Layer(d, main))
				}
			}
			if !mnemonic.Valid() {
				scr := &ErrorScreen{
					Title: "Invalid Seed",
				}
				var words []string
				for _, w := range mnemonic {
					words = append(words, bip39.LabelFor(w))
				}
				// Electrum seeds share the BIP39 wordlist but use their
				// own HMAC version-check instead of the BIP39 checksum,
				// and derive keys via a non-BIP32-standard scheme.
				// Supporting them would engrave plates that standard
				// BIP39 restore tools cannot recover, so we deliberately
				// detect them only to give this specific rejection
				// (upstream 3e8d306) rather than the generic
				// invalid-seed message. Intentionally not supported.
				if nonstandard.ElectrumSeed(strings.Join(words, " ")) {
					scr.Body = "Electrum seeds are not supported."
				} else {
					// Naming no word leaves a stuck user to guess it is
					// the last one, and the device can now "fix" that
					// into a valid but different wallet.
					scr.Body = "The checksum does not match.\n\nAny of the words could be wrong, not just the last one."
				}
				showErr(scr)
				continue
			}
			if _, ok := deriveMasterKey(mnemonic, "", &chaincfg.MainNetParams); !ok {
				showErr(&ErrorScreen{
					Title: "Invalid Seed",
					Body:  "The seed is invalid.",
				})
				continue
			}
			return true
		}
		for {
			e, ok := inp.Next(ctx, ButtonFilter(Up), ButtonFilter(Down))
			if !ok {
				break
			}
			if e, ok := e.AsButton(); ok {
				switch e.Button {
				case Down:
					if e.Pressed && s.selected < len(mnemonic)-1 {
						s.selected++
					}
				case Up:
					if e.Pressed && s.selected > 0 {
						s.selected--
					}
				}
			}
		}

		dims := ctx.Platform.DisplaySize()
		nav, _ := layoutNavigation(&ctx.B, th, dims, []NavButton{
			{Clickable: backBtn, Style: StyleSecondary, Icon: assets.IconBack},
			{Clickable: editBtn, Style: StyleSecondary, Icon: assets.IconEdit},
		}...)
		if isMnemonicComplete(mnemonic) {
			nav2, _ := layoutNavigation(&ctx.B, th, dims, []NavButton{
				{Clickable: confirmBtn, Style: StylePrimary, Icon: assets.IconCheckmark},
			}...)
			nav = op.Layer(nav, nav2)
		}
		content := s.Draw(ctx, th, dims, mnemonic)

		ctx.Frame(op.Layer(
			nav,
			content,
		))
	}
	return false
}

func isMnemonicComplete(m bip39.Mnemonic) bool {
	if slices.Contains(m, -1) {
		return false
	}
	return len(m) > 0
}

// seedColumns returns the number of columns that fit n seed words in a
// content area contentDx wide with colWidth-wide columns, and the number
// of rows per column needed to balance the words across them.
func seedColumns(n, contentDx, colWidth int) (cols, rows int) {
	cols = max(1, contentDx/colWidth)
	rows = (n + cols - 1) / cols
	return
}

func (s *SeedScreen) Draw(ctx *Context, th *Colors, dims image.Point, mnemonic bip39.Mnemonic) op.Op {
	if len(s.words) != len(mnemonic) {
		s.words = make([]Clickable, len(mnemonic))
	}

	style := ctx.Styles.word
	longestPrefix := style.Measure(math.MaxInt, "24: ")
	layoutWord := func(b *op.Buffer, col color.RGBA, n int, word string) (op.Op, image.Point) {
		numOp, prefix := widget.Labelf(b, style, col, "%d: ", n)
		numOp = numOp.Offset(image.Pt(longestPrefix.X-prefix.X, 0))
		txtOp, txt := widget.Label(b, style, col, word)
		txtOp = txtOp.Offset(image.Pt(longestPrefix.X, 0))
		return op.Layer(numOp, txtOp), image.Pt(longestPrefix.X+txt.X, txt.Y)
	}

	y := 0
	_, longest := layoutWord(nil, color.RGBA{}, 24, widestWord)
	r := layout.Rectangle{Max: dims}
	navw := assets.NavBtnPrimary.Bounds().Dx()
	list := r.Shrink(leadingSize, 0, 0, 0)
	content := list.Shrink(scrollFadeDist, navw, scrollFadeDist, navw)
	lineHeight := longest.Y + 2
	linesPerPage := content.Dy() / lineHeight
	scroll := s.selected - linesPerPage/2
	maxScroll := len(mnemonic) - linesPerPage
	if scroll > maxScroll {
		scroll = maxScroll
	}
	if scroll < 0 {
		scroll = 0
	}
	largeScreen := dims.X >= 480
	rows := len(mnemonic)
	if largeScreen {
		scroll = 0
		_, rows = seedColumns(len(mnemonic), content.Dx(), longest.X+16)
	}
	off := content.Min.Add(image.Pt(0, -scroll*lineHeight))
	var m op.Op
	for i, w := range mnemonic {
		col := th.Text
		if i == s.selected {
			col = th.Background
		}
		r := image.Rectangle{Max: longest}
		word := bip39.LabelFor(w)
		w, _ := layoutWord(&ctx.B, col, i+1, word)
		inp := op.Input(&ctx.B, &s.words[i]).Clip(r)
		wordOp := op.Layer(
			w,
			inp,
		)
		if i == s.selected {
			col = th.Background
			r.Min.Y -= 3
			r.Max.Y += buttonPadY
			r.Min.X -= buttonPadX
			r.Max.X += buttonPadX
			wordOp = op.Layer(
				wordOp,
				op.Compose(
					op.Color(&ctx.B, th.Text),
					op.RoundedRect2(&ctx.B, r, cornerRadius),
				),
			)
		}
		pos := image.Pt(0, y).Add(off)
		m = op.Layer(
			m,
			wordOp.Offset(pos),
		)
		y += lineHeight
		// Wrap into the next column in touch mode.
		if largeScreen && (i+1)%rows == 0 {
			y = 0
			off.X += longest.X + 16
		}
	}
	m = fadeClip(&ctx.B, m, image.Rectangle(list))
	titleTxt := s.Title
	if titleTxt == "" {
		titleTxt = "Engrave Seed"
	}
	title, _ := layoutTitle(ctx, dims.X, th.Text, titleTxt)
	return op.Layer(
		m,
		title,
		op.Color(&ctx.B, th.Background),
	)
}

type DescriptorScreen struct {
	Descriptor *bip380.Descriptor
}

// showError overlays err's screen over draw until dismissed.
func showError(ctx *Context, th *Colors, err error, draw func(*Context, *Colors, image.Point) op.Op) {
	scr := NewErrorScreen(err)
	for !ctx.Done {
		dims := ctx.Platform.DisplaySize()
		d, dismissed := scr.Layout(ctx, th, dims)
		if dismissed {
			break
		}
		ctx.Frame(op.Layer(d, draw(ctx, th, dims)))
	}
}

// plannedPlate pairs a planned plate with the preview view the
// engrave screen renders it through.
type plannedPlate struct {
	plate Plate
	view  *CurvesScreen
}

// confirmScreen drives a back/confirm navigation loop over draw. The
// confirm button calls validate: an error overlays the screen and the
// loop continues; ok reports whether the plate is ready, and false
// with a nil error re-enters the loop (e.g. a cancelled choice).
func confirmScreen(ctx *Context, th *Colors, draw func(*Context, *Colors, image.Point) op.Op, validate func() (plannedPlate, bool, error)) (plannedPlate, bool) {
	backBtn := &Clickable{Button: Button1}
	confirmBtn := &Clickable{Button: Button3}
	for !ctx.Done {
		if backBtn.Clicked(ctx) {
			break
		}
		if confirmBtn.Clicked(ctx) {
			plate, ok, err := validate()
			if err != nil {
				showError(ctx, th, err, draw)
				continue
			}
			if ok {
				return plate, true
			}
			continue
		}

		dims := ctx.Platform.DisplaySize()
		nav, _ := layoutNavigation(&ctx.B, th, dims, []NavButton{
			{Clickable: backBtn, Style: StyleSecondary, Icon: assets.IconBack},
			{Clickable: confirmBtn, Style: StylePrimary, Icon: assets.IconCheckmark},
		}...)
		ctx.Frame(op.Layer(nav, draw(ctx, th, dims)))
	}
	return plannedPlate{}, false
}

// splitPlan carries the operator's choice to cut the descriptor
// backup as one plate per cosigner out of the descriptor confirm:
// the partition cell for quorums ur.Split has a scheme for, or the
// chosen single-plate variant when every cosigner receives a full
// copy instead.
type splitPlan struct {
	// data is the descriptor's CBOR encoding, ur.Split's input.
	data     []byte
	fontSize float32
	scale    int

	// copies marks the full-copy fallback; copyText and copyQR are
	// the chosen fitDescriptor variant, engraved once per cosigner.
	copies   bool
	copyText backup.Text
	copyQR   string
}

// plateContent is plate k of the split set: its layout with stand-in
// codes, and the text of each paragraph's real code.
func (sp *splitPlan) plateContent(desc *bip380.Descriptor, k int) (backup.Text, []string, error) {
	if sp.copies {
		return sp.copyText, []string{sp.copyQR}, nil
	}
	return shareText(desc, sp.data, k, sp.fontSize, sp.scale)
}

func (s *DescriptorScreen) Confirm(ctx *Context, th *Colors) (plannedPlate, *splitPlan, bool) {
	var split *splitPlan
	plate, ok := confirmScreen(ctx, th, s.Draw, func() (plannedPlate, bool, error) {
		split = nil
		params := ctx.Platform.EngraverParams()
		desc := s.Descriptor
		type fitResult struct {
			labels []string
			texts  []backup.Text
			qrText string

			// The small plate's own ladder outcome: its variant list
			// can be shorter than the square one's, so the operator
			// must choose from the plate they picked.
			smallLabels []string
			smallTexts  []backup.Text
			smallQR     string
			fitsSmall   bool
		}
		fit, err := runJob(ctx, th, func(pump func(done, total int) bool) (fitResult, error) {
			labels, texts, qrText, err := fitDescriptor(params, SquarePlate, desc, pump)
			res := fitResult{labels: labels, texts: texts, qrText: qrText}
			// The small ladder rides the tail of the gauge: it walks
			// fewer accepted cells and stays planner-free.
			smLabels, smTexts, smQR, smErr := fitDescriptor(params, SmallPlate, desc, nil)
			if smErr == nil && len(smLabels) > 0 {
				res.smallLabels, res.smallTexts, res.smallQR = smLabels, smTexts, smQR
				res.fitsSmall = true
			}
			return res, err
		}, planFrame(ctx, th, s.Draw))
		labels, texts := fit.labels, fit.texts
		n := len(desc.Keys)
		scheme := n > 1 && ur.HasScheme(desc.Threshold, n)
		if err != nil {
			if errors.Is(err, errPlanCanceled) {
				return plannedPlate{}, false, nil
			}
			if !scheme || !errors.Is(err, ErrTooLarge) {
				return plannedPlate{}, false, err
			}
			// No single-plate variant fits, but the partition's smaller
			// shares still can: offer the split alone.
			labels, texts = nil, nil
		}
		wantCopies := false
		if n > 1 {
			// One descriptor plate, or one plate per cosigner? The rows
			// keep the operator's reading order: as-is first, split
			// second, back abandons as everywhere else.
			choices := make([]string, 0, 2)
			if len(labels) > 0 {
				choices = append(choices, "ONE PLATE")
			}
			lead := "Every plate is complete"
			row := fmt.Sprintf("%d FULL COPIES", n)
			if scheme {
				lead = fmt.Sprintf("Any %d of %d plates recover", desc.Threshold, n)
				row = fmt.Sprintf("SPLIT: %d PLATES", n)
			}
			choices = append(choices, row)
			mc := &ChoiceScreen{
				Title:   "Engrave",
				Lead:    lead,
				Choices: choices,
			}
			mchoice, ok := mc.Choose(ctx, th)
			if !ok {
				return plannedPlate{}, false, nil
			}
			if mchoice == len(choices)-1 {
				if scheme {
					sp, err := runJob(ctx, th, func(pump func(done, total int) bool) (*splitPlan, error) {
						data, size, scale, err := fitShares(params, desc, pump)
						if err != nil {
							return nil, err
						}
						return &splitPlan{data: data, fontSize: size, scale: scale}, nil
					}, planFrame(ctx, th, s.Draw))
					if err != nil {
						if errors.Is(err, errPlanCanceled) {
							return plannedPlate{}, false, nil
						}
						return plannedPlate{}, false, err
					}
					split = sp
					return plannedPlate{}, true, nil
				}
				wantCopies = true
			}
		}
		plateSize := SquarePlate
		useLabels, useTexts, useQR := labels, texts, fit.qrText
		if !wantCopies {
			// Full copies engrave the same layout once per cosigner and
			// stay on the square plate; the single plate is the small
			// format's customer.
			var ok bool
			plateSize, ok = askPlateSize(ctx, th, fit.fitsSmall)
			if !ok {
				return plannedPlate{}, false, nil
			}
			if plateSize == SmallPlate {
				useLabels, useTexts, useQR = fit.smallLabels, fit.smallTexts, fit.smallQR
			}
		}
		cs := &ChoiceScreen{
			Title:   "Engrave",
			Lead:    "Choose engraving",
			Choices: useLabels,
		}
		choice, ok := cs.Choose(ctx, th)
		if !ok {
			return plannedPlate{}, false, nil
		}
		if wantCopies {
			split = &splitPlan{copies: true, copyText: useTexts[choice], copyQR: useQR}
			return plannedPlate{}, true, nil
		}
		plate, view, err := planDescriptorPlate(ctx, th, params, plateSize, useTexts[choice], []string{useQR}, "Engrave Descriptor")
		if err != nil {
			if errors.Is(err, errPlanCanceled) {
				return plannedPlate{}, false, nil
			}
			return plannedPlate{}, false, err
		}
		return plannedPlate{plate: plate, view: view}, true, nil
	})
	return plate, split, ok
}

func (s *DescriptorScreen) Draw(ctx *Context, th *Colors, dims image.Point) op.Op {
	const infoSpacing = 8

	desc := s.Descriptor

	// Title.
	r := layout.Rectangle{Max: dims}

	btnw := assets.NavBtnPrimary.Bounds().Dx()
	body := r.Shrink(leadingSize, btnw, 0, btnw)

	var bodytxt richText

	bodyst := ctx.Styles.body
	subst := ctx.Styles.subtitle
	if desc.Title != "" {
		bodytxt.Add(&ctx.B, subst, body.Dx(), th.Text, "Title")
		bodytxt.Add(&ctx.B, bodyst, body.Dx(), th.Text, desc.Title)
		bodytxt.Y += infoSpacing
	}
	bodytxt.Add(&ctx.B, subst, body.Dx(), th.Text, "Type")
	// Explicit any conversion: verified on TinyGo 0.41.1 that boxing a
	// non-constant string at the variadic call heap-allocates per frame,
	// while constant conversions lower to static globals.
	testnet := any("")
	if len(desc.Keys) > 0 && desc.Keys[0].Network != &chaincfg.MainNetParams {
		testnet = " (testnet)"
	}
	switch desc.Type {
	case bip380.Singlesig:
		bodytxt.Addf(&ctx.B, bodyst, body.Dx(), th.Text, "Singlesig%s", testnet)
	default:
		bodytxt.Addf(&ctx.B, bodyst, body.Dx(), th.Text, "%d-of-%d multisig%s", desc.Threshold, len(desc.Keys), testnet)
	}
	bodytxt.Y += infoSpacing
	bodytxt.Add(&ctx.B, subst, body.Dx(), th.Text, "Script")
	bodytxt.Add(&ctx.B, bodyst, body.Dx(), th.Text, desc.Script.String())

	bodyOp := bodytxt.Content.Offset(body.Min.Add(image.Pt(0, scrollFadeDist)))

	title, _ := layoutTitle(ctx, dims.X, th.Text, "Engrave Descriptor")
	return op.Layer(
		bodyOp,
		title,
		op.Color(&ctx.B, th.Background),
	)
}

// textNotice warns when a text payload resembles a corrupted wallet
// backup, so an operator does not engrave a broken descriptor or seed
// phrase believing it still works. Intact backups never reach the
// text flow; the scanner's structured parsers take them first.
func textNotice(text string) string {
	const engraved = " It engraves as plain text."
	first := text
	if i := strings.IndexByte(first, '\n'); i >= 0 {
		first = first[:i]
	}
	first = strings.ToLower(strings.TrimLeft(first, " "))
	for _, p := range []string{"wsh(", "wpkh(", "sh(", "pkh(", "tr(", "multi(", "sortedmulti(", "[", "xpub", "ypub", "zpub", "tpub"} {
		if strings.HasPrefix(first, p) {
			return "Looks like a corrupted descriptor." + engraved
		}
	}
	if strings.HasPrefix(first, "ms1") {
		return "Looks like a corrupted codex32 share." + engraved
	}
	// A seed phrase length worth of words, all but at most one in the
	// BIP39 word list. Valid mnemonics parse before reaching the text
	// flow, so a match here means a bad word or checksum.
	words := strings.Fields(text)
	if n := len(words); n >= 12 && n <= 24 && n%3 == 0 {
		known := 0
		for _, w := range words {
			if _, ok := bip39.ClosestWord(strings.ToUpper(w)); ok {
				known++
			}
		}
		if known >= n-1 {
			return "Looks like a seed phrase with a bad word or checksum." + engraved
		}
	}
	return ""
}

// plateRecorder is an optional Platform capability: a platform that wants a
// vector artifact of an engraving implements it and NewEngraveScreen hands it
// the planned plate. The emulator uses it to render an SVG that resembles the
// steel; the real controller does not implement it, so this is a no-op there.
// Same optional-interface shape as recordTyper in scan.go — nothing is added
// to the Platform interface, so no implementer needs to change.
type plateRecorder interface {
	RecordPlate(Plate)
}

// engraverSpline is the plan the machine runs: tests and the QA flow
// construct display-only plates without one, and those stay in the
// plate frame.
func engraverSpline(plate Plate) bspline.Curve {
	if plate.Machine != nil {
		return plate.Machine
	}
	return plate.Spline
}

func NewEngraveScreen(ctx *Context, plate Plate, view *CurvesScreen) *EngraveScreen {
	if r, ok := ctx.Platform.(plateRecorder); ok {
		r.RecordPlate(plate)
	}
	return &EngraveScreen{
		duration: plate.Duration,
		job:      newEngraverJob(ctx.Platform, engraverSpline(plate), 0),
		view:     view,
	}
}

type EngraveScreen struct {
	duration uint
	job      *engraveJob

	// view is the plate preview the screen renders through: the idle
	// state is the plate confirm, the countdown runs over the same
	// preview. nil falls back to a text layout, as does the failed
	// state whose error must stay readable at full width.
	view *CurvesScreen
	// warned records that the view's notice was shown, so a paused and
	// resumed engraving is not re-warned about its own content.
	warned bool

	// The hold-to-confirm gesture, in ConfirmWarningScreen's shape:
	// edge-detect the press, arm the delay, a release disarms.
	pressed bool
	confirm ConfirmDelay
}

func (s *EngraveScreen) Engrave(ctx *Context, th *Colors) bool {
	defer s.job.Stop()
	backBtn := &Clickable{Button: Button1}
	hideBtn := &Clickable{Button: Button2}
	selectBtn := &Clickable{Button: Button3, AltButton: Center}
	for !ctx.Done {
		for hideBtn.Clicked(ctx) {
			// Hides what the plate says, not that it is engraving: the
			// toggle holds through the run, which is the point of it.
			if s.view != nil && s.view.preview != nil {
				s.view.hidden = !s.view.hidden
			}
		}
		for backBtn.Clicked(ctx) {
			st := s.job.Status()
			if st.State != engraveRunning {
				return false
			}
			s.job.Stop()
		}
		progress := float32(0)
		switch s.job.Status().State {
		case engraveDone:
			if selectBtn.Clicked(ctx) {
				return true
			}
		case engraveIdle, engraveStopped, engraveFailed:
			for {
				if _, ok := selectBtn.Next(ctx); !ok {
					break
				}
				if selectBtn.Pressed != s.pressed {
					s.pressed = selectBtn.Pressed
					if s.pressed {
						s.confirm.Start(ctx, confirmDelay)
					} else {
						s.confirm = ConfirmDelay{}
					}
				}
			}
			progress = s.confirm.Progress(ctx)
			if progress == 1 {
				s.confirm = ConfirmDelay{}
				s.pressed = false
				progress = 0
				if s.job.Status().State == engraveIdle && !s.warned && s.view != nil && s.view.notice != "" {
					// The content warning gets its own page, between
					// the completed hold and the first start; backing
					// out returns to the idle preview.
					if !s.confirmNotice(ctx, th) {
						continue
					}
					s.warned = true
				}
				s.job.Start()
			}
		default:
			// Running and stopping ignore the confirm button; drain its
			// events so a stray press cannot arm a stale hold when an
			// armable state returns.
			for {
				if _, ok := selectBtn.Next(ctx); !ok {
					break
				}
				s.pressed = selectBtn.Pressed
			}
			s.confirm = ConfirmDelay{}
		}

		if s.job.Status().State == engraveRunning {
			// Update progress twice a second.
			ctx.WakeupAt(time.Now().Add(time.Second / 2))
		}

		dims := ctx.Platform.DisplaySize()
		nav := s.drawNav(&ctx.B, th, dims, progress, backBtn, hideBtn, selectBtn)
		content := s.draw(ctx, th, dims)

		ctx.Frame(op.Layer(nav, content))
	}
	return false
}

// countdown is the remaining engraving time, rounded up to whole
// seconds, as m:ss.
func (s *EngraveScreen) countdown(ctx *Context, st engraveStatus) (int, uint) {
	rem := s.duration - st.Completed
	tps := ctx.Platform.EngraverParams().TicksPerSecond
	remSec := (rem + tps - 1) / tps
	return int(remSec / 60), remSec % 60
}

// confirmNotice interposes the view's content warning between the
// completed hold and the first start: the plate preview was on
// screen up to the hold, the warning gets a page of its own, and
// confirming it begins the engraving.
func (s *EngraveScreen) confirmNotice(ctx *Context, th *Colors) bool {
	gate := &noticeScreen{
		Title: s.view.title,
		Body:  s.view.notice,
		Icon:  assets.IconHammer,
	}
	for !ctx.Done {
		dims := ctx.Platform.DisplaySize()
		d, res := gate.Layout(ctx, th, dims)
		switch res {
		case ConfirmYes:
			return true
		case ConfirmNo:
			return false
		}
		ctx.Frame(d)
	}
	return false
}

func (s *EngraveScreen) draw(ctx *Context, th *Colors, dims image.Point) op.Op {
	st := s.job.Status()
	title := "Engrave Plate"
	if s.view != nil && s.view.title != "" {
		title = s.view.title
	}
	titleOp, _ := layoutTitle(ctx, dims.X, th.Text, title)
	if s.view == nil || s.view.preview == nil || st.State == engraveFailed {
		return op.Layer(
			s.drawBody(ctx, th, dims, st),
			titleOp,
			op.Color(&ctx.B, th.Background),
		)
	}
	// The preview is the screen; the strip under the plate carries the
	// state. Idle shows the dims/duration line — the idle screen IS
	// the plate confirm — and running counts the same line down.
	// One line under the plate; the strip has no wrap, so its texts
	// stay short enough for the narrowest display.
	strip := s.view.info
	switch st.State {
	case engraveRunning:
		min, sec := s.countdown(ctx, st)
		strip = fmt.Sprintf("%d:%.2d", min, sec)
	case engraveDone:
		strip = "Engraving completed."
	case engraveStopped:
		strip = "Paused. Hold to resume."
	case engraveStopping:
		strip = "Stopping..."
	}
	return op.Layer(
		s.view.plateOp(ctx, th, dims, strip),
		titleOp,
		op.Color(&ctx.B, th.Background),
	)
}

// drawBody is the text layout for states without a preview: a
// viewless screen, and the failed state, whose error must stay
// readable at full width.
func (s *EngraveScreen) drawBody(ctx *Context, th *Colors, dims image.Point, st engraveStatus) op.Op {
	r := layout.Rectangle{Max: dims}
	const margin = 8
	_, content := r.CutTop(leadingSize)
	content = content.Shrink(0, margin, 0, margin)
	content, _ = content.CutBottom(leadingSize)
	var bodysz image.Point
	var bodyOp op.Op
	switch st.State {
	case engraveIdle:
		const body = "Hold button to start the engraving."
		bodyOp, bodysz = widget.Labelw(&ctx.B, ctx.Styles.lead, content.Dx(), th.Text, body)
	case engraveRunning:
		min, sec := s.countdown(ctx, st)
		bodyOp, bodysz = widget.Labelf(&ctx.B, ctx.Styles.lead, th.Text, "Engraving plate   %d:%.2d", min, sec)
	case engraveDone:
		const body = "Engraving completed successfully."
		bodyOp, bodysz = widget.Labelw(&ctx.B, ctx.Styles.lead, content.Dx(), th.Text, body)
	case engraveStopped:
		const body = "Engraving paused.\nHold button to resume."
		bodyOp, bodysz = widget.Labelw(&ctx.B, ctx.Styles.lead, content.Dx(), th.Text, body)
	case engraveStopping:
		const body = "Engraving stopping..."
		bodyOp, bodysz = widget.Labelw(&ctx.B, ctx.Styles.lead, content.Dx(), th.Text, body)
	case engraveFailed:
		bodyOp, bodysz = widget.Labelwf(&ctx.B, ctx.Styles.lead, content.Dx(), th.Text,
			"Engraving failed.\nHold button to retry.\n\nError: %s", st.Error)
	}
	return bodyOp.Offset(content.Center(bodysz))
}

func (s *EngraveScreen) drawNav(b *op.Buffer, th *Colors, dims image.Point, progress float32, backBtn, hideBtn, selectBtn *Clickable) op.Op {
	st := s.job.Status()
	// A button's slot comes from which button it is, so the rows are
	// built by appending: the hide toggle takes the middle slot
	// wherever a preview is on screen, and its icon is the checkbox —
	// filled while the plate's content is withheld, empty while the
	// plate reads plainly.
	btns := make([]NavButton, 0, 3)
	switch st.State {
	case engraveRunning:
		btns = append(btns, NavButton{Clickable: backBtn, Style: StyleSecondary, Icon: assets.IconLeft})
	case engraveStopping:
		btns = append(btns, NavButton{Clickable: backBtn, Style: StyleSecondary, Icon: assets.IconBack})
	case engraveDone:
	default:
		btns = append(btns, NavButton{Clickable: backBtn, Style: StyleSecondary, Icon: assets.IconBack})
	}
	if s.view != nil && s.view.preview != nil && st.State != engraveStopping {
		icon := assets.Circle
		if s.view.hidden {
			icon = assets.CircleFilled
		}
		btns = append(btns, NavButton{Clickable: hideBtn, Style: StyleSecondary, Icon: icon})
	}
	switch st.State {
	case engraveDone:
		btns = append(btns, NavButton{Clickable: selectBtn, Style: StylePrimary, Icon: assets.IconRight})
	case engraveRunning, engraveStopping:
	default:
		btns = append(btns, NavButton{Clickable: selectBtn, Style: StylePrimary, Icon: assets.IconHammer, Progress: progress})
	}
	nav, _ := layoutNavigation(b, th, dims, btns...)
	return nav
}

type Platform interface {
	LockBoot() error
	AppendEvents(deadline time.Time, evts []Event) []Event
	Wakeup()
	Engraver(stall bool) (Engraver, error)
	NFCReader() io.ReadCloser
	EngraverParams() engrave.Params
	DisplaySize() image.Point
	// Dirty begins a refresh of the content
	// specified by r.
	Dirty(r image.Rectangle) error
	// NextChunk returns the next chunk of the refresh.
	NextChunk() (draw.RGBA64Image, bool)
	Features() Features
	HardwareVersion() string
}

type Features int

const (
	FeatureSecureBoot Features = 1 << iota
)

func (f Features) Has(feat Features) bool {
	return f&feat != 0
}

type EngraverStats struct {
	StallSpeed       int
	XSpeed, YSpeed   int
	XLoad, YLoad     int
	XStalls, YStalls int
	Error            error
}

const idleTimeout = 3 * time.Minute

func Run(pl Platform, version string) func(yield func() bool) {
	return func(yield func() bool) {
		ctx := NewContext(pl)
		a := struct {
			mask *image.Alpha
			idle struct {
				start  time.Time
				active bool
				state  saver.State
			}
		}{}
		a.idle.start = time.Now()

		it := func(yield func(op.Op) bool) {
			ctx.FrameCallback = func(op op.Op) {
				ctx.Done = ctx.Done || !yield(op)
			}
			version := "Firmware: " + version + "\nHardware: " + pl.HardwareVersion()
			if !pl.Features().Has(FeatureSecureBoot) {
				version += " (UNLOCKED)"
			}
			uiFlow(ctx, version)
		}
		startTime := time.Now()
		var evts []Event
		stats := new(runtimeStats)
		d := new(op.Drawer)
		for content := range it {
			d.Reset()
			dirty := image.Rectangle{Max: pl.DisplaySize()}
			layoutTime := time.Since(startTime)
			if err := pl.Dirty(dirty); err != nil {
				panic(err)
			}
			for {
				fb, ok := pl.NextChunk()
				if !ok {
					break
				}
				// Yield between chunks: rasterization doesn't block
				// on I/O, and goroutines such as the NFC pump must
				// respond within their protocol deadlines even while
				// a frame is drawn.
				runtime.Gosched()
				fbdims := fb.Bounds().Size()
				npix := fbdims.X * fbdims.Y
				if a.mask == nil || len(a.mask.Pix) < npix {
					a.mask = image.NewAlpha(image.Rectangle{Max: fbdims})
				}
				a.mask.Rect = image.Rectangle{Max: fbdims}
				d.Draw(fb, a.mask, content)
			}
			drawTime := time.Since(startTime)
			if debug {
				stats.Dump(drawTime, layoutTime)
			}
			for {
				if ctx.Done || !yield() {
					return
				}
				wakeup := ctx.Wakeup
				evts = pl.AppendEvents(wakeup, evts[:0])
				now := time.Now()
				if len(evts) > 0 {
					a.idle.start = now
				}
				ctx.Reset()
				if !a.idle.active {
					ctx.Router.Events(d, evts...)
				}
				idleWakeup := a.idle.start.Add(idleTimeout)
				idle := now.Sub(idleWakeup) >= 0
				if a.idle.active != idle {
					a.idle.active = idle
					if idle {
						a.idle.state = saver.State{}
					}
				}
				if a.idle.active {
					a.idle.state.Draw(pl)
					// Throttle screen saver speed.
					const minFrameTime = 40 * time.Millisecond
					ctx.WakeupAt(now.Add(minFrameTime))
					continue
				}
				ctx.WakeupAt(idleWakeup)
				break
			}
			startTime = time.Now()
		}
	}
}

type runtimeStats struct {
	mallocs uint64
	buf     [200]byte
}

func (r *runtimeStats) Dump(drawTime, layoutTime time.Duration) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	dm := mem.Mallocs - r.mallocs
	r.mallocs = mem.Mallocs
	format := "frame: %dms layout: %dms draw: %dms mem %d allocs %d total %d\n"
	// Cast values to int to avoid a TinyGo allocation for larger integers.
	args := []any{int(drawTime.Milliseconds()), int(layoutTime.Milliseconds()), int((drawTime - layoutTime).Milliseconds()),
		uint(mem.HeapInuse), uint(dm), uint(mem.Mallocs - mem.Frees)}
	f := new(text.Formatter)
	buf := r.buf[:0]
	for {
		r, ok := f.Next(format, args...)
		if !ok {
			break
		}
		buf = utf8.AppendRune(buf, r)
	}
	log.Writer().Write(buf)
}

func rgb(c uint32) color.RGBA {
	return color.RGBA{A: 0xff, R: uint8(c >> 16), G: uint8(c >> 8), B: uint8(c)}
}

func toPlate(plan engrave.Engraving, params engrave.Params, plateSize PlateSize) (Plate, error) {
	spline := engrave.PlanEngraving(params.StepperConfig, plan)
	attrs := bspline.Measure(spline)
	if !attrs.Bounds.In(plateBounds(params, plateSize)) {
		return Plate{}, ErrTooLarge
	}
	machine := engrave.PlanEngraving(params.StepperConfig, machinePlan(params, plateSize, plan))
	if !bspline.Measure(machine).Bounds.In(machineBounds(params, plateSize)) {
		return Plate{}, ErrTooLarge
	}
	return Plate{
		Size:     plateSize,
		Duration: attrs.Duration,
		Spline:   spline,
		Machine:  machine,
	}, nil
}

// measureLayout walks an engraving without planning it. The returned
// bounds are the hull of its engraved marks, measured with the same
// windowing the planner's measure applies, so a layout can be
// rejected on the plate margin at glyph-replay cost; knots counts
// the commands the planner would consume, the denominator for a
// plan's progress gauge.
func measureLayout(plan engrave.Engraving) (bspline.Bounds, int) {
	knots := 0
	attrs := bspline.Measure(func(yield func(bspline.Knot) bool) {
		for cmd := range plan {
			k, ok := cmd.AsKnot()
			if !ok {
				continue
			}
			if knots++; knots%256 == 0 {
				// The fit ladder walks whole layouts between progress
				// pumps; yield inside the walk too, or the cooperative
				// scheduler starves the frame loop for a walk at a
				// time.
				runtime.Gosched()
			}
			// Clamped commands repeat their knot in the planned
			// spline; mirror the repetition so the measure windows
			// pad out exactly like the planner's.
			for m := 0; m < k.Multiplicity; m++ {
				if !yield(bspline.Knot{Ctrl: k.Knot, T: 1, Engrave: k.Engrave}) {
					return
				}
			}
		}
	})
	return attrs.Bounds, knots
}

// plateBounds is the engravable region inside the safety margin.
func plateBounds(params engrave.Params, plateSize PlateSize) bspline.Bounds {
	sz := plateDims(plateSize, params.Millimeter)
	m := bezier.Pt(safetyMargin*params.Millimeter, safetyMargin*params.Millimeter)
	return bspline.Bounds{Min: m, Max: sz.Sub(m)}
}

// layoutFits reports whether an engraving's marks stay inside the
// plate's safety margin, without planning it.
func layoutFits(plan engrave.Engraving, params engrave.Params, plateSize PlateSize) bool {
	bounds, _ := measureLayout(plan)
	return bounds.In(plateBounds(params, plateSize))
}

// planPlateWalk is planPlate's compute, callable without a screen: a
// counting walk sizes the progress gauge, then one planning walk
// produces the plate, with toPlate's margin check and figures. pump,
// if not nil, observes the planner's consumption of the layout and
// cancels with errPlanCanceled by returning false. tee, if not nil,
// sees every planned knot as the measuring walk streams by — the
// preview rasterizer rides the walk instead of costing one of its
// own.
func planPlateWalk(plan engrave.Engraving, params engrave.Params, plateSize PlateSize, pump func(done, total int) bool, tee func(bspline.Knot)) (Plate, error) {
	bounds, total := measureLayout(plan)
	if !bounds.In(plateBounds(params, plateSize)) {
		return Plate{}, ErrTooLarge
	}
	fed := 0
	canceled := false
	src := func(yield func(engrave.Command) bool) {
		for cmd := range plan {
			if _, ok := cmd.AsKnot(); ok {
				fed++
				if pump != nil && !pump(fed, total) {
					canceled = true
					return
				}
			}
			if !yield(cmd) {
				return
			}
		}
	}
	spline := engrave.PlanEngraving(params.StepperConfig, src)
	if tee != nil {
		inner := spline
		spline = func(yield func(bspline.Knot) bool) {
			for k := range inner {
				tee(k)
				if !yield(k) {
					return
				}
			}
		}
	}
	attrs := bspline.Measure(spline)
	if canceled {
		return Plate{}, errPlanCanceled
	}
	if !attrs.Bounds.In(plateBounds(params, plateSize)) {
		return Plate{}, ErrTooLarge
	}
	machine := engrave.PlanEngraving(params.StepperConfig, machinePlan(params, plateSize, plan))
	if !bspline.Measure(machine).Bounds.In(machineBounds(params, plateSize)) {
		return Plate{}, ErrTooLarge
	}
	return Plate{
		Size:     plateSize,
		Duration: attrs.Duration,
		// Fresh plans for the preview's and the engraver's own
		// re-iterations; the machine one is planned in its frame.
		Spline:  engrave.PlanEngraving(params.StepperConfig, plan),
		Machine: machine,
	}, nil
}

// planRefresh is the redraw cadence of the progress screen while a
// plate plans in the background.
const planRefresh = time.Second / 4

// errPlanCanceled reports a planning job stopped by the back button
// or shutdown.
var errPlanCanceled = errors.New("gui: plan canceled")

// runJob runs work in a worker goroutine while the flow redraws
// frame with the walked percentage; the back button or shutdown
// cancels with errPlanCanceled.
//
// The worker exists for its stack, not for parallelism: the walk
// pipeline and the frame rasterizer each fill a good share of a
// fixed 16KB TinyGo stack, so the walk keeps its own. The tasks
// scheduler is cooperative; the walk yields to the frame loop from
// its pump callback.
func runJob[T any](ctx *Context, th *Colors, work func(pump func(done, total int) bool) (T, error), frame func(pct int) op.Op) (T, error) {
	type result struct {
		val T
		err error
	}
	res := make(chan result, 1)
	progress := make(chan [2]int, 1)
	quit := make(chan struct{})
	go func() {
		defer ctx.Platform.Wakeup()
		val, err := work(func(done, total int) bool {
			select {
			case <-progress:
			default:
			}
			select {
			case progress <- [2]int{done, total}:
			default:
			}
			runtime.Gosched()
			select {
			case <-quit:
				return false
			default:
				return true
			}
		})
		res <- result{val, err}
	}()
	// The worker aliases flow state; every return drains res so
	// nothing outlives the walk.
	backBtn := &Clickable{Button: Button1}
	pct := 0
	canceled := false
	cancel := func() {
		if !canceled {
			canceled = true
			close(quit)
		}
	}
	for !ctx.Done {
		// The cancel outranks a racing completion: a click pending
		// from the previous frame must not be swallowed by a walk
		// that finished in the same window. The loop keeps drawing
		// while the worker unwinds; draining res here instead would
		// hold the pressed frame on screen for as long as the walk
		// runs between two pumps.
		if backBtn.Clicked(ctx) {
			cancel()
		}
		select {
		case r := <-res:
			if canceled {
				var zero T
				return zero, errPlanCanceled
			}
			return r.val, r.err
		default:
		}
		select {
		case p := <-progress:
			if p[1] > 0 {
				pct = p[0] * 100 / p[1]
			}
		default:
		}
		dims := ctx.Platform.DisplaySize()
		nav, _ := layoutNavigation(&ctx.B, th, dims,
			NavButton{Clickable: backBtn, Style: StyleSecondary, Icon: assets.IconBack},
		)
		ctx.WakeupAt(time.Now().Add(planRefresh))
		ctx.Frame(op.Layer(nav, frame(pct)))
	}
	cancel()
	<-res
	var zero T
	return zero, errPlanCanceled
}

// planPlate plans an engraving into a Plate behind the progress
// screen: draw underneath, the planned fraction at the foot, and the
// back button canceling with errPlanCanceled. planPlateWalk rejects
// out-of-plate layouts inside the worker, before any planning, so an
// oversized layout costs one measuring walk behind the progress
// screen instead of a frameless stall on the flow goroutine.
func planPlate(ctx *Context, th *Colors, draw func(*Context, *Colors, image.Point) op.Op, plan engrave.Engraving, params engrave.Params, plateSize PlateSize, tee func(bspline.Knot)) (Plate, error) {
	return runJob(ctx, th, func(pump func(done, total int) bool) (Plate, error) {
		return planPlateWalk(plan, params, plateSize, pump, tee)
	}, planFrame(ctx, th, draw))
}

// planFrame overlays the walked percentage on a screen's draw for a
// runJob progress loop.
func planFrame(ctx *Context, th *Colors, draw func(*Context, *Colors, image.Point) op.Op) func(pct int) op.Op {
	return func(pct int) op.Op {
		dims := ctx.Platform.DisplaySize()
		label, lsz := widget.Labelwf(&ctx.B, ctx.Styles.subtitle, 300, th.Text, "Preparing %d%%", pct)
		r := layout.Rectangle{Max: dims}
		return op.Layer(label.Offset(r.S(lsz).Sub(image.Pt(0, 16))), draw(ctx, th, dims))
	}
}

// planPreviewPlate plans an engraving with its preview filling under
// the progress label — the funnel every simple plate flow shares.
// The returned view carries the raster and the dims/duration line
// for the engrave screen.
func planPreviewPlate(ctx *Context, th *Colors, title string, plan engrave.Engraving, params engrave.Params, plateSize PlateSize) (Plate, *CurvesScreen, error) {
	cs := &CurvesScreen{title: title}
	r := newSplineRasterizer(previewSide(ctx.Platform.DisplaySize(), plateSize), params, plateSize)
	cs.preview = r.preview
	plate, err := planPlate(ctx, th, cs.Draw, plan, params, plateSize, r.knot)
	if err != nil {
		return Plate{}, nil, err
	}
	cs.initText(plate, r, params)
	return plate, cs, nil
}

// PlateSize aliases backup.PlateSize; the plate geometry lives with
// the layouts.
type PlateSize = backup.PlateSize

const (
	SquarePlate = backup.SquarePlate
	SmallPlate  = backup.SmallPlate
)

// plateDims returns the plate dimensions in machine units.
func plateDims(p PlateSize, mm int) bezier.Point {
	d := p.Dims()
	return bezier.Pt(d.X*mm, d.Y*mm)
}

type scanResult struct {
	Object any
	Status scanStatus
}

type scanStatus int

const (
	scanIdle scanStatus = iota
	scanStarted
	scanOverflow
	scanUnknownFormat
	scanFailed
)
