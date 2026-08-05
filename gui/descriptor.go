package gui

import (
	"errors"
	"image"
	"image/color"
	"strings"

	"github.com/btcsuite/btcd/chaincfg/v2"
	qr "github.com/seedhammer/kortschak-qr"
	"seedhammer.com/bip32"
	"seedhammer.com/bip380"
	"seedhammer.com/bip39"
	"seedhammer.com/gui/assets"
	"seedhammer.com/gui/layout"
	"seedhammer.com/gui/op"
	"seedhammer.com/gui/widget"
)

var errDescriptorDerive = errors.New("Could not derive the wallet descriptor.")

// seedScripts are the address types offered for a derived descriptor,
// in the order a new wallet would consider them. Nested and legacy are
// included: a new seed may still be destined for a system that only
// speaks an older standard.
// The multisig scripts are deliberately absent: selecting one would
// yield a 1-of-1 descriptor, which is not the cosigner record a
// coordinator wants, and pretending otherwise would mislead.
var seedScripts = []bip380.Script{
	bip380.P2WPKH, bip380.P2TR, bip380.P2SH_P2WPKH, bip380.P2PKH,
}

// pathKeys is the digits plus the two characters a derivation path
// needs. Hardening is the apostrophe rather than "h" because the
// keyboard uppercases every rune it appends and bip32 only recognises a
// lowercase h; an "H" key would produce silently unparseable paths.
const pathKeys = "1234567890\n'/"

// pathPrefix is fixed rather than typed. bip32.ParsePath requires it,
// and a deletable prefix with no key to restore it is a trap.
const pathPrefix = "m/"

// seedDescriptor derives the single-signature wallet descriptor implied
// by a seed, so a watch-only wallet can be set up without the seed
// itself leaving the machine.
func seedDescriptor(m bip39.Mnemonic, passphrase string, script bip380.Script, path bip32.Path) (*bip380.Descriptor, error) {
	network := &chaincfg.MainNetParams
	mk, ok := deriveMasterKey(m, passphrase, network)
	if !ok {
		return nil, errDescriptorDerive
	}
	master, err := mk.ECPubKey()
	if err != nil {
		return nil, err
	}
	xpub, err := bip32.Derive(mk, path)
	if err != nil {
		return nil, err
	}
	account, err := xpub.ECPubKey()
	if err != nil {
		return nil, err
	}
	return &bip380.Descriptor{
		Type:      bip380.Singlesig,
		Threshold: 1,
		Script:    script,
		Keys: []bip380.Key{{
			Network:           network,
			MasterFingerprint: bip32.Fingerprint(master),
			DerivationPath:    path,
			// The receive and change branches have to be in the
			// descriptor itself. address.derivePubKey defaults to
			// <0;1>/* when Children is empty, but that default is this
			// firmware's own: Descriptor.Encode emits children only by
			// ranging this slice, so leaving it empty exports a
			// descriptor for one key rather than for the wallet, and a
			// wallet importing it watches a single script.
			Children: []bip380.Derivation{
				{Type: bip380.RangeDerivation, Index: 0, End: 1},
				{Type: bip380.WildcardDerivation},
			},
			KeyData:           account.SerializeCompressed(),
			ChainCode:         xpub.ChainCode(),
			ParentFingerprint: xpub.ParentFingerprint(),
		}},
	}, nil
}

// qrScale picks the pixels per module that fit height, quiet zone
// included. Below about four the code stops being reliably scannable at
// arm's length, which is the point of showing it at all.
func qrScale(height, size, quiet int) int {
	scale := height / (size + 2*quiet)
	if scale < 1 {
		scale = 1
	}
	return scale
}

// qrImage presents a QR code as an alpha mask, one module per scale by
// scale block, with a quiet zone scanners need to find the code.
//
// It samples rather than rasterizes. Materializing a 45-module code at
// five pixels per module costs around 60 KB as a one-byte-per-pixel
// mask, and a contiguous request that size is exactly the class this
// heap is documented to fail. The drawer reads masks through At, so
// there is nothing to materialize.
type qrImage struct {
	code  *qr.Code
	scale int
	quiet int
}

func (q *qrImage) ColorModel() color.Model { return color.AlphaModel }

func (q *qrImage) Bounds() image.Rectangle {
	d := (q.code.Size + 2*q.quiet) * q.scale
	return image.Rectangle{Max: image.Pt(d, d)}
}

func (q *qrImage) At(x, y int) color.Color {
	mx, my := x/q.scale-q.quiet, y/q.scale-q.quiet
	if mx < 0 || my < 0 || mx >= q.code.Size || my >= q.code.Size {
		return color.Alpha{}
	}
	if !q.code.Black(mx, my) {
		return color.Alpha{}
	}
	return color.Alpha{A: 0xff}
}

// The code is drawn dark on light in both themes. A scanner reading an
// inverted code is a coin toss, and this one has to work the first time.
var (
	qrInk   = color.RGBA{A: 0xff}
	qrPaper = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
)

// walletDescriptorFlow offers the descriptor a seed implies: a QR to
// scan into a watch-only wallet, and the option to engrave it. Backing
// out of the address-type choice declines the whole thing.
//
// Each address type carries its standard path, which is the answer for
// a wallet being created here. ADVANCED opens the path for editing, for
// a seed whose wallet already exists at a different account or depth.
// It reports the derivation path it used, so a passphrase plate can
// record which wallet the set belongs to. Empty means declined.
func walletDescriptorFlow(ctx *Context, th *Colors, mnemonic bip39.Mnemonic, passphrase, title string) string {
	// Declining is a visible choice rather than a back button nobody
	// thinks to press, and it sits last so the selection lands on the
	// common answer instead of on the way out.
	labels := make([]string, 0, len(seedScripts)+2)
	for _, s := range seedScripts {
		labels = append(labels, scriptChoiceLabel(s))
	}
	labels = append(labels, "ADVANCED", "SKIP")
	for {
		cs := &ChoiceScreen{
			Title:   "Descriptor",
			Lead:    "Engrave or show a wallet descriptor?",
			Choices: labels,
		}
		choice, ok := cs.Choose(ctx, th)
		if !ok || choice == len(labels)-1 {
			return ""
		}
		script := bip380.UnknownScript
		var path bip32.Path
		if choice < len(seedScripts) {
			script = seedScripts[choice]
			path = script.DerivationPath()
		} else {
			script, path, ok = advancedPathFlow(ctx, th)
			if !ok {
				continue
			}
		}
		desc, err := seedDescriptor(mnemonic, passphrase, script, path)
		if err != nil {
			showError(ctx, th, err, blankScreen)
			continue
		}
		// The descriptor string has no title field (BIP380 defines
		// none); the title rides the struct onto the screens and the
		// plate headers.
		desc.Title = title
		if showDescriptorFlow(ctx, th, desc) {
			return path.String()
		}
	}
}

// advancedPathFlow picks an address type and then its derivation path,
// prefilled with the standard one for that type.
func advancedPathFlow(ctx *Context, th *Colors) (bip380.Script, bip32.Path, bool) {
	labels := make([]string, len(seedScripts))
	for i, s := range seedScripts {
		labels[i] = scriptChoiceLabel(s)
	}
	for {
		cs := &ChoiceScreen{
			Title:   "Address Type",
			Lead:    "Choose the type your wallet uses",
			Choices: labels,
		}
		choice, ok := cs.Choose(ctx, th)
		if !ok {
			return bip380.UnknownScript, nil, false
		}
		script := seedScripts[choice]
		path, ok := inputPathFlow(ctx, th, script.DerivationPath())
		if ok {
			return script, path, true
		}
	}
}

// inputPathFlow edits a derivation path, starting from std. The path is
// accepted only when it parses, so a half-typed one cannot be used.
func inputPathFlow(ctx *Context, th *Colors, std bip32.Path) (bip32.Path, bool) {
	kbd := NewKeyboard(ctx, pathKeys)
	// Encode writes hardening as "h"; the keyboard can only produce the
	// apostrophe, so the prefill has to speak the same alphabet.
	kbd.Fragment = strings.ReplaceAll(strings.TrimPrefix(std.Encode(), "/"), "h", "'")
	backBtn := &Clickable{Button: Button1}
	okBtn := &Clickable{Button: Button2}
	path, valid := parsePathFragment(kbd.Fragment)
	for !ctx.Done {
		for kbd.Update(ctx) {
			path, valid = parsePathFragment(kbd.Fragment)
		}
		if backBtn.Clicked(ctx) {
			return nil, false
		}
		if valid && okBtn.Clicked(ctx) {
			return path, true
		}
		dims := ctx.Platform.DisplaySize()

		screen := layout.Rectangle{Max: dims}
		_, content := screen.CutTop(leadingSize)
		content, _ = content.CutBottom(8)
		// The box is centred over content, so content has to stop short
		// of the navigation column or a long fragment paints over it.
		content, _ = content.CutEnd(assets.NavBtnPrimary.Bounds().Dx() + 8)

		kbdOp, kbdsz := kbd.Layout(ctx, th)
		kbdOp = kbdOp.Offset(content.S(kbdsz))

		lbl, lblsz := widget.Labelw(&ctx.B, ctx.Styles.word, content.Dx()-2*buttonPadX, th.Background,
			pathPrefix+kbd.Fragment)
		lblsz.X = max(lblsz.X, 100)
		r := image.Rectangle{Max: lblsz}
		r.Min.Y -= 3
		r.Max.Y += buttonPadY
		r.Min.X -= buttonPadX
		r.Max.X += buttonPadX
		top, _ := content.CutBottom(kbdsz.Y)
		box := op.Layer(
			lbl,
			op.Compose(
				op.Color(&ctx.B, th.Text),
				op.RoundedRect2(&ctx.B, r, cornerRadius),
			),
		).Offset(top.Center(lblsz))

		nav, _ := layoutNavigation(&ctx.B, th, dims,
			NavButton{Clickable: backBtn, Style: StyleSecondary, Icon: assets.IconBack})
		if valid {
			nav2, _ := layoutNavigation(&ctx.B, th, dims,
				NavButton{Clickable: okBtn, Style: StylePrimary, Icon: assets.IconCheckmark})
			nav = op.Layer(nav, nav2)
		}
		title, _ := layoutTitle(ctx, dims.X, th.Text, "Derivation Path")
		ctx.Frame(op.Layer(
			kbdOp,
			box,
			nav,
			title,
			op.Color(&ctx.B, th.Background),
		))
	}
	return nil, false
}

// parsePathFragment validates the edited path. Lowercasing is belt and
// braces: the keyboard uppercases what it appends, and bip32 reads only
// a lowercase h as hardening.
func parsePathFragment(frag string) (bip32.Path, bool) {
	if frag == "" {
		return nil, false
	}
	p, err := bip32.ParsePath(strings.ToLower(pathPrefix + frag))
	if err != nil || len(p) == 0 {
		return nil, false
	}
	return p, true
}

func scriptChoiceLabel(s bip380.Script) string {
	switch s {
	case bip380.P2WPKH:
		return "SEGWIT"
	case bip380.P2TR:
		return "TAPROOT"
	case bip380.P2SH_P2WPKH:
		return "NESTED SEGWIT"
	case bip380.P2PKH:
		return "LEGACY"
	default:
		return strings.ToUpper(s.String())
	}
}

// descriptorCode encodes what the screen shows and the wallet scans.
// The test that pins the module size has to call this, not re-encode
// the descriptor itself, or it guards nothing about the screen.
func descriptorCode(desc *bip380.Descriptor) (*qr.Code, error) {
	return qr.Encode(desc.Encode(), qr.L)
}

// showDescriptorFlow displays the descriptor as a scannable code and
// offers to engrave it. It reports whether the user is done with the
// descriptor, as opposed to going back to pick another address type.
func showDescriptorFlow(ctx *Context, th *Colors, desc *bip380.Descriptor) bool {
	code, err := descriptorCode(desc)
	if err != nil {
		showError(ctx, th, err, blankScreen)
		return false
	}
	backBtn := &Clickable{Button: Button1}
	nextBtn := &Clickable{Button: Button3, AltButton: Center}
	img := new(qrImage)
	pathStr := desc.Keys[0].DerivationPath.String()
	for !ctx.Done {
		if backBtn.Clicked(ctx) {
			return false
		}
		if nextBtn.Clicked(ctx) {
			descriptorFlow(ctx, th, desc)
			return true
		}
		dims := ctx.Platform.DisplaySize()
		ctx.Frame(op.Layer(
			descriptorQRScreen(ctx, th, desc, code, dims, img, pathStr),
			navDescriptorQR(ctx, th, dims, backBtn, nextBtn),
			op.Color(&ctx.B, th.Background),
		))
	}
	return true
}

func navDescriptorQR(ctx *Context, th *Colors, dims image.Point, back, next *Clickable) op.Op {
	nav, _ := layoutNavigation(&ctx.B, th, dims,
		NavButton{Clickable: back, Style: StyleSecondary, Icon: assets.IconBack},
		// A checkmark, not the hammer: this leads to the descriptor
		// confirm and a layout choice before anything is cut. The
		// hammer belongs to the button that actually starts engraving.
		NavButton{Clickable: next, Style: StylePrimary, Icon: assets.IconCheckmark},
	)
	return nav
}

// descriptorQRScreen lays the code out beside the details that identify
// it, so the wallet you scan into can be checked against the plate.
// img and pathStr are reused across frames: op.Mask boxes its argument
// into the op buffer, and boxing a value larger than a pointer allocates
// under TinyGo, which the frame loop may not do.
func descriptorQRScreen(ctx *Context, th *Colors, desc *bip380.Descriptor, code *qr.Code, dims image.Point, img *qrImage, pathStr string) op.Op {
	const quiet = 2
	const paperQuiet = 2
	const margin = 8

	screen := layout.Rectangle{Max: dims}
	_, content := screen.CutTop(leadingSize)
	content, _ = content.CutBottom(margin)
	// Keep clear of the navigation column.
	content, _ = content.CutEnd(assets.NavBtnPrimary.Bounds().Dx() + margin)

	// ISO/IEC 18004 asks for four modules of quiet zone. Two are
	// sampled into the mask and two are painted around it, so the
	// scale has to be chosen against all four or the paper runs off
	// the screen and over the text beside it.
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
		detail.Add(&ctx.B, body, w, th.Text, desc.Script.String())
		detail.Y += margin
		detail.Add(&ctx.B, sub, w, th.Text, "Fingerprint")
		detail.Addf(&ctx.B, body, w, th.Text, "%.8x", desc.Keys[0].MasterFingerprint)
		detail.Y += margin
		detail.Add(&ctx.B, sub, w, th.Text, "Path")
		detail.Add(&ctx.B, body, w, th.Text, pathStr)
	}

	title, _ := layoutTitle(ctx, dims.X, th.Text, "Descriptor")
	return op.Layer(
		qrOp,
		detail.Content.Offset(image.Pt(textArea.Min.X, textArea.Min.Y)),
		title,
	)
}
