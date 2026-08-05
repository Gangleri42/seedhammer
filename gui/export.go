package gui

import (
	"errors"
	"image"
	"runtime"
	"strings"
	"time"

	qr "github.com/seedhammer/kortschak-qr"
	"seedhammer.com/address"
	"seedhammer.com/bc/ur"
	"seedhammer.com/bc/urtypes"
	"seedhammer.com/bip380"
	"seedhammer.com/gui/assets"
	"seedhammer.com/gui/layout"
	"seedhammer.com/gui/op"
	"seedhammer.com/gui/widget"
)

// exportFrameInterval paces the animated parts. Wallet camera loops
// decode a few frames per second; slower only stretches the scan,
// faster risks the panel's stripe refresh tearing a frame mid-scan.
const exportFrameInterval = 500 * time.Millisecond

// exportQuiet matches the descriptor QR screen: two modules sampled
// into the mask, two painted around it.
const exportQuiet = 4

// exportMinScale is the same floor as the single-sig screen: four
// pixels per module is where a phone camera at arm's length stops
// guessing.
const exportMinScale = 4

var errExportTooLarge = errors.New("The descriptor does not fit an animated code.")

// exportMaxModules is the largest code the screen shows at the scale
// floor, quiet zone included — the budget exportParts splits against.
func exportMaxModules(dims image.Point) int {
	height := dims.Y - leadingSize - 8
	return height/exportMinScale - 2*exportQuiet
}

// exportParts splits the descriptor's CBOR into the fewest sequential
// UR parts whose codes all fit the module budget. One part returns
// the single-part UR, which wallets accept without an animation.
func exportParts(data []byte, maxModules int) ([]string, error) {
	for k := 1; k <= 64; k++ {
		parts := make([]string, k)
		fits := true
		for i := range k {
			p := strings.ToUpper(ur.Encode("crypto-output", data, i+1, k))
			size, err := qr.MinSize(p, qr.L)
			if err != nil || size > maxModules {
				fits = false
				break
			}
			parts[i] = p
		}
		if fits {
			return parts, nil
		}
	}
	// Sixty-four parts of a descriptor that fits a plate cannot miss
	// the budget; this is a backstop, not a path.
	return nil, errExportTooLarge
}

// exportDescriptorFlow shows the assembled wallet as an animated
// ur:crypto-output for the coordinator's camera, then the first
// receive address as the cross-check that both ends built the same
// wallet. It reports true when the operator confirmed the address;
// back unwinds to the review.
func exportDescriptorFlow(ctx *Context, th *Colors, desc *bip380.Descriptor) bool {
	type export struct {
		codes  []*qr.Code
		labels []string
	}
	exp, err := runJob(ctx, th, func(pump func(done, total int) bool) (export, error) {
		data := urtypes.EncodeDescriptor(desc)
		parts, err := exportParts(data, exportMaxModules(ctx.Platform.DisplaySize()))
		if err != nil {
			return export{}, err
		}
		// The encoder wants contiguous room; give it a collected heap
		// first, as planDescriptorPlate does before its plate codes.
		runtime.GC()
		e := export{
			codes:  make([]*qr.Code, len(parts)),
			labels: make([]string, len(parts)),
		}
		for i, p := range parts {
			c, err := qr.Encode(p, qr.L)
			if err != nil {
				return export{}, err
			}
			e.codes[i] = c
			e.labels[i] = "Part " + itoa(i+1) + " of " + itoa(len(parts))
		}
		return e, nil
	}, planFrame(ctx, th, blankScreen))
	if err != nil {
		if !errors.Is(err, errPlanCanceled) {
			showError(ctx, th, err, blankScreen)
		}
		return false
	}
	backBtn := &Clickable{Button: Button1}
	nextBtn := &Clickable{Button: Button3, AltButton: Center}
	// Hoisted across frames like the single-sig screen: op.Mask boxes
	// its argument, and boxing beyond pointer size allocates under
	// TinyGo, which the frame loop may not do.
	img := new(qrImage)
	quorum := itoa(desc.Threshold) + "-of-" + itoa(len(desc.Keys)) + " multisig"
	frameIdx := 0
	var next time.Time
	for !ctx.Done {
		if backBtn.Clicked(ctx) {
			return false
		}
		if nextBtn.Clicked(ctx) {
			if firstAddressFlow(ctx, th, desc) {
				return true
			}
			continue
		}
		if len(exp.codes) > 1 {
			now := time.Now()
			if next.IsZero() {
				next = now.Add(exportFrameInterval)
			}
			for !now.Before(next) {
				frameIdx = (frameIdx + 1) % len(exp.codes)
				next = next.Add(exportFrameInterval)
			}
			ctx.WakeupAt(next)
		}
		dims := ctx.Platform.DisplaySize()
		ctx.Frame(op.Layer(
			exportQRScreen(ctx, th, desc, exp.codes[frameIdx], exp.labels[frameIdx], quorum, dims, img),
			navDescriptorQR(ctx, th, dims, backBtn, nextBtn),
			op.Color(&ctx.B, th.Background),
		))
	}
	return false
}

// exportQRScreen lays the animated part beside what identifies the
// wallet, mirroring the single-sig descriptor screen's shape.
func exportQRScreen(ctx *Context, th *Colors, desc *bip380.Descriptor, code *qr.Code, partLabel, quorum string, dims image.Point, img *qrImage) op.Op {
	const quiet = 2
	const paperQuiet = 2
	const margin = 8

	screen := layout.Rectangle{Max: dims}
	_, content := screen.CutTop(leadingSize)
	content, _ = content.CutBottom(margin)
	content, _ = content.CutEnd(assets.NavBtnPrimary.Bounds().Dx() + margin)

	scale := qrScale(content.Dy(), code.Size, quiet+paperQuiet)
	img.code, img.scale, img.quiet = code, scale, quiet
	pad := paperQuiet * scale
	paperSz := img.Bounds().Size().Add(image.Pt(2*pad, 2*pad))

	qrArea, textArea := content.CutStart(paperSz.X + margin)
	qrOp := op.Layer(
		op.Compose(
			op.Color(&ctx.B, qrInk),
			op.Mask(&ctx.B, img),
		).Offset(image.Pt(pad, pad)),
		op.Compose(
			op.Color(&ctx.B, qrPaper),
			op.RoundedRect2(&ctx.B, image.Rectangle{Max: paperSz}, 0),
		),
	).Offset(qrArea.Center(paperSz))

	var detail richText
	// A display the paper nearly fills leaves the detail column no
	// width, and the text layouter never terminates at a width no
	// glyph fits. The code is the point of the screen; the details
	// yield.
	if w := textArea.Dx(); w >= 60 {
		sub := ctx.Styles.subtitle
		body := ctx.Styles.body
		if desc.Title != "" {
			detail.Add(&ctx.B, sub, w, th.Text, "Title")
			detail.Add(&ctx.B, body, w, th.Text, desc.Title)
			detail.Y += margin
		}
		detail.Add(&ctx.B, sub, w, th.Text, "Type")
		detail.Add(&ctx.B, body, w, th.Text, quorum)
		detail.Y += margin
		detail.Add(&ctx.B, sub, w, th.Text, "Scan")
		detail.Add(&ctx.B, body, w, th.Text, partLabel)
	}

	title, _ := layoutTitle(ctx, dims.X, th.Text, "Export Wallet")
	return op.Layer(
		qrOp,
		detail.Content.Offset(image.Pt(textArea.Min.X, textArea.Min.Y)),
		title,
	)
}

// firstAddressFlow is the cross-check after the export: both ends of
// the QR derive the wallet's first receive address independently, and
// they either agree or the setup stops here, before any steel.
func firstAddressFlow(ctx *Context, th *Colors, desc *bip380.Descriptor) bool {
	addr, err := address.Receive(desc, 0)
	if err != nil {
		showError(ctx, th, err, blankScreen)
		return false
	}
	backBtn := &Clickable{Button: Button1}
	nextBtn := &Clickable{Button: Button3, AltButton: Center}
	// Grouped for reading against another screen; the spaces are not
	// part of the address.
	grouped := groupAddress(addr, 4)
	for !ctx.Done {
		if backBtn.Clicked(ctx) {
			return false
		}
		if nextBtn.Clicked(ctx) {
			return true
		}
		dims := ctx.Platform.DisplaySize()
		r := layout.Rectangle{Max: dims}
		_, content := r.CutTop(leadingSize)
		content, lead := content.CutBottom(leadingSize)
		content, _ = content.CutEnd(assets.NavBtnPrimary.Bounds().Dx() + 8)
		content = content.Shrink(0, 8, 0, 8)

		leadOp, lsz := widget.Labelw(&ctx.B, ctx.Styles.lead, dims.X-2*8, th.Text,
			"The coordinator must show this exact first receive address.")
		addrOp, asz := widget.Labelw(&ctx.B, ctx.Styles.word, content.Dx(), th.Text, grouped)

		nav, _ := layoutNavigation(&ctx.B, th, dims,
			NavButton{Clickable: backBtn, Style: StyleSecondary, Icon: assets.IconBack},
			NavButton{Clickable: nextBtn, Style: StylePrimary, Icon: assets.IconCheckmark},
		)
		title, _ := layoutTitle(ctx, dims.X, th.Text, "First Address")
		ctx.Frame(op.Layer(
			addrOp.Offset(content.Center(asz)),
			leadOp.Offset(lead.Center(lsz)),
			nav,
			title,
			op.Color(&ctx.B, th.Background),
		))
	}
	return false
}

func groupAddress(addr string, group int) string {
	var b strings.Builder
	for i := 0; i < len(addr); i += group {
		if i > 0 {
			b.WriteByte(' ')
		}
		end := min(i+group, len(addr))
		b.WriteString(addr[i:end])
	}
	return b.String()
}
