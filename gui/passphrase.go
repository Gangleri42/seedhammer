package gui

import (
	"errors"
	"image"
	"strings"
	"unicode/utf8"

	"github.com/btcsuite/btcd/chaincfg/v2"
	"seedhammer.com/bip32"
	"seedhammer.com/bip39"
	"seedhammer.com/gui/assets"
	"seedhammer.com/gui/layout"
	"seedhammer.com/gui/op"
	"seedhammer.com/gui/widget"
)

// The passphrase alphabet is every printable ASCII character, split
// across three layers because one keyboard holding 95 keys would not fit
// the screen. The layer key opens every bottom row, next to the back
// button; the digits stay on every layer so cycling never costs access
// to the numbers. The symbols layer parks '~' on its digits row: its
// bottom row is at the 12 cells the screen fits once the layer key,
// space and backspace are on it.
//
// ASCII is the whole domain: bip39.MnemonicSeed does not normalise, and
// normalising ASCII is the identity. See its doc comment.
var passLayers = [3]string{
	"1234567890\nqwertyuiop\nasdfghjkl\n" + string(layerKey) + "zxcvbnm" + string(spaceKey),
	"1234567890\nQWERTYUIOP\nASDFGHJKL\n" + string(layerKey) + "ZXCVBNM" + string(spaceKey),
	"1234567890~\n!\"#$%&'()*+\n,-./:;<=>?@\n" + string(layerKey) + "[\\]^_`{|}" + string(spaceKey),
}

// textLayers is the free-text alphabet: the passphrase layers plus a
// return key on the letter layers. The symbols bottom row is already at
// the screen's width, so a newline after a symbol costs one layer
// cycle, which suits how rare it is.
var textLayers = [3]string{
	passLayers[0] + string(newlineKey),
	passLayers[1] + string(newlineKey),
	passLayers[2],
}

// spaceKey types a space. Spaced passphrases are ordinary, and without
// this the alphabet covers 94 of the 95 printable ASCII characters and
// silently locks out every wallet whose passphrase contains one.
const spaceKey = ' '

// There is no length cap. BIP39 sets none, and a passphrase reaches
// PBKDF2 as salt rather than as the HMAC key, so no block boundary
// applies either. The confirm screen scrolls and the input box shows a
// tail, so neither bounds it. What does bound it is the plate: the
// engraved grid is 44 by 26 at the smallest font, and the headers take
// what they take, so a passphrase of roughly 880 characters is the most
// that can be recorded alongside a standard derivation path, and less
// with a long one. Past that the passphrase plate is refused and the
// wallet still works, which is a deliberate trade: the machine does not
// decide for you how long a secret may be.

// passphraseFlow asks for a passphrase and returns it. The empty string
// with ok means the user declined, which is the same wallet as no
// passphrase at all.
func passphraseFlow(ctx *Context, th *Colors, mnemonic bip39.Mnemonic) (string, bool) {
	cs := &ChoiceScreen{
		Title:   "Passphrase",
		Lead:    "Add a passphrase to this seed?",
		Choices: []string{"NO PASSPHRASE", "ADD PASSPHRASE"},
	}
	choice, ok := cs.Choose(ctx, th)
	if !ok || choice == 0 {
		return "", ok
	}
	pass := ""
	for {
		var ok bool
		// Carried back in on edit: the screen exists to catch a typo,
		// and retyping from memory is how a second one gets made.
		pass, ok = inputTextFlow(ctx, th, "Passphrase", &passLayers, pass)
		if !ok {
			return "", false
		}
		switch confirmPassphraseFlow(ctx, th, mnemonic, pass) {
		case ConfirmYes:
			return pass, true
		case ConfirmNo:
			return "", false
		}
	}
}

// inputTextFlow types free text across keyboard layers, seeded with
// initial and carried between the layers as one field. OK asks for at
// least one character; back reports false.
func inputTextFlow(ctx *Context, th *Colors, title string, layers *[3]string, initial string) (string, bool) {
	var kbds [len(layers)]*Keyboard
	for i, alph := range layers {
		kbds[i] = NewKeyboard(ctx, alph)
		kbds[i].Verbatim = true
		kbds[i].Fragment = initial
	}
	layer := 0
	backBtn := &Clickable{Button: Button1}
	okBtn := &Clickable{Button: Button2}
	for !ctx.Done {
		kbd := kbds[layer]
		for kbd.Update(ctx) {
			if kbd.Layer {
				kbd.Layer = false
				next := (layer + 1) % len(kbds)
				// Carry the text across; the layers are one field.
				kbds[next].Fragment = kbd.Fragment
				layer = next
				kbd = kbds[layer]
			}
		}
		if backBtn.Clicked(ctx) {
			return "", false
		}
		if kbd.Fragment != "" && okBtn.Clicked(ctx) {
			return kbd.Fragment, true
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

		// The fragment is unbounded, so the box would grow through the
		// keyboard and off the screen. Show its tail: what you just
		// typed is what needs checking, and the confirm screen carries
		// the whole thing. Trimming is a binary search over the head, so
		// a long passphrase costs a handful of measurements per frame
		// rather than one per character.
		boxTop, _ := content.CutBottom(kbdsz.Y)
		frag := tailFitting(ctx, th, kbd.Fragment, content.Dx()-2*buttonPadX, boxTop.Dy())
		lbl, lblsz := widget.Labelw(&ctx.B, ctx.Styles.word, content.Dx()-2*buttonPadX, th.Background, frag)
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
		if kbd.Fragment != "" {
			nav2, _ := layoutNavigation(&ctx.B, th, dims,
				NavButton{Clickable: okBtn, Style: StylePrimary, Icon: assets.IconCheckmark})
			nav = op.Layer(nav, nav2)
		}
		titleOp, _ := layoutTitle(ctx, dims.X, th.Text, title)
		ctx.Frame(op.Layer(
			kbdOp,
			box,
			nav,
			titleOp,
			op.Color(&ctx.B, th.Background),
		))
	}
	return "", false
}

// tailFitting returns the longest suffix of s whose label fits height,
// trimmed on a rune boundary. An empty result means not even one line
// fits, which the caller draws as an empty box rather than overflowing.
func tailFitting(ctx *Context, th *Colors, s string, width, height int) string {
	if _, sz := widget.Labelw(nil, ctx.Styles.word, width, th.Background, s); sz.Y+3+buttonPadY <= height {
		return s
	}
	// Binary search the first byte to keep. lo always fits, hi never does.
	lo, hi := len(s), 0
	for hi < lo {
		mid := (lo + hi) / 2
		for mid < len(s) && !utf8.RuneStart(s[mid]) {
			mid++
		}
		if _, sz := widget.Labelw(nil, ctx.Styles.word, width, th.Background, s[mid:]); sz.Y+3+buttonPadY <= height {
			lo = mid
		} else {
			hi = mid + 1
		}
	}
	return s[lo:]
}

// confirmPassphraseFlow shows the passphrase exactly as typed, beside
// the fingerprint of the wallet it opens.
//
// A passphrase carries no checksum, so one wrong character yields a
// valid wallet that is simply not yours, and nothing downstream can tell.
// Reading it back is the only check there is; the fingerprint is the
// second, for anyone who knows what their wallet's should be.
func confirmPassphraseFlow(ctx *Context, th *Colors, mnemonic bip39.Mnemonic, pass string) ConfirmResult {
	fp, ok := walletFingerprint(mnemonic, pass)
	if !ok {
		fp = "unavailable"
	}
	editBtn := &Clickable{Button: Button2}
	scr := &ConfirmWarningScreen{
		Title: strings.ToTitle("Confirm Passphrase"),
		Body: "\"" + pass + "\"\n\n" +
			// The same count the plate carries. The screen decides, and
			// it runs before the plate exists, so a break that swallowed
			// a space has to be catchable here too.
			itoa(len(pass)) + " characters\n" +
			"Wallet fingerprint " + fp + "\n\n" +
			"Read it back character for character. A passphrase has no " +
			"checksum: one wrong character opens a different wallet and " +
			"nothing will warn you.\n\n" +
			"Hold button to confirm.",
		// The hold button confirms; the icon is the action, as the
		// discard warning's is. An "i" there reads as "more information".
		Icon: assets.IconCheckmark,
	}
	for !ctx.Done {
		dims := ctx.Platform.DisplaySize()
		d, res := scr.Layout(ctx, th, dims)
		if res != ConfirmNone {
			return res
		}
		if editBtn.Clicked(ctx) {
			return ConfirmNone
		}
		nav, _ := layoutNavigation(&ctx.B, th, dims,
			NavButton{Clickable: editBtn, Style: StyleSecondary, Icon: assets.IconEdit})
		ctx.Frame(op.Layer(nav, d))
	}
	return ConfirmNo
}

// walletFingerprint names the wallet a seed and passphrase open. Both
// the confirm screen and the plate ask the same question, and a
// difference between them would be invisible and wrong.
func walletFingerprint(m bip39.Mnemonic, pass string) (string, bool) {
	mk, ok := deriveMasterKey(m, pass, &chaincfg.MainNetParams)
	if !ok {
		return "", false
	}
	pk, err := mk.ECPubKey()
	if err != nil {
		return "", false
	}
	return strings.ToUpper(fingerprintHex(bip32.Fingerprint(pk))), true
}

func fingerprintHex(fp uint32) string {
	const hexdigits = "0123456789abcdef"
	var b [8]byte
	for i := range b {
		b[len(b)-1-i] = hexdigits[fp&0xf]
		fp >>= 4
	}
	return string(b[:])
}

// passphrasePlate is what a passphrase plate says. The passphrase is
// the payload; everything else is what a stranger holding only this
// plate needs to know, and what you need to pair it with the right seed
// years from now.
//
// It is a text plate rather than rich text: the firmware already
// engraves these, whereas composing rich text on device would pull the
// whole vector-font renderer in for a handful of headings.
//
// It is an ordinary plate in the other sense too: unlike the seed and
// nsec plates, which pad every glyph through engrave.NewConstantStringer,
// this one engraves in time that depends on its content. Measured, a
// 25-character passphrase spans 193 to 257 seconds across the alphabet,
// and a single substitution shifts the stroke count. That is a decision,
// not an oversight. Recovering a passphrase from it needs a recording of
// the room during this specific plate, and the constant-stroke face
// cannot render one anyway: font/constant covers 52 of the 95 printable
// ASCII characters, so NewConstantStringer panics on any ordinary
// passphrase. Extending it is not planned. Anyone who revisits that
// should read backup/nostr.go, where the same trade is written down for
// the npub.
func passphrasePlate(pass, fingerprint, path string) string {
	var b strings.Builder
	b.WriteString("BIP39 PASSPHRASE\n")
	// The only integrity check a passphrase can carry, and it is what
	// makes a wrapped one safe to read: if the break swallowed a space,
	// the count you transcribe will not match the count on the plate.
	b.WriteString(itoa(len(pass)))
	b.WriteString(" CHARACTERS\n\n")
	// Alone between blank lines, so where the secret starts and ends is
	// never in question even when it wraps.
	b.WriteString(pass)
	b.WriteString("\n\nWALLET ")
	b.WriteString(fingerprint)
	if path != "" {
		b.WriteString("\nPATH ")
		b.WriteString(path)
	}
	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	// Wide enough for any int; a narrow buffer here panics rather than
	// wrapping, and the length it formats is caller-controlled.
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// passphrasePlateFlow offers the passphrase as a plate of its own, the
// third of the set. Declining is the common case and costs one screen.
func passphrasePlateFlow(ctx *Context, th *Colors, mnemonic bip39.Mnemonic, pass, path string) {
	if pass == "" {
		return
	}
	// Engraving first, as on the seed plate: the selection lands on the
	// answer someone who set a passphrase came here for, and declining
	// costs a keypress instead of being what happens by default.
	cs := &ChoiceScreen{
		Title:   "Passphrase",
		Lead:    "Engrave the passphrase on its own plate?",
		Choices: []string{"ENGRAVE PASSPHRASE", "SKIP"},
	}
	choice, ok := cs.Choose(ctx, th)
	if !ok || choice == 1 {
		return
	}
	fp, ok := walletFingerprint(mnemonic, pass)
	if !ok {
		fp = "UNAVAILABLE"
	}
	text := passphrasePlate(pass, fp, path)
	params := ctx.Platform.EngraverParams()
	_, _, smallErr := fitText(params, SmallPlate, text)
	plateSize, sizeOK := askPlateSize(ctx, th, smallErr == nil)
	if !sizeOK {
		return
	}
	plate, preview, err := planText(ctx, th, params, plateSize, text)
	if err != nil {
		if !errors.Is(err, errPlanCanceled) {
			showError(ctx, th, err, blankScreen)
		}
		return
	}
	preview.title = "Engrave Passphrase"
	NewEngraveScreen(ctx, plate, preview).Engrave(ctx, &engraveTheme)
}
