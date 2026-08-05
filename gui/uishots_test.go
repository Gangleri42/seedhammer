package gui

import (
	"bytes"
	"flag"
	"image"
	"image/color"
	"image/png"
	"iter"
	"os"
	"path/filepath"
	"testing"
	"time"

	qr "github.com/seedhammer/kortschak-qr"
	"seedhammer.com/backup"
	"seedhammer.com/bip39"
	"seedhammer.com/engrave"
	"seedhammer.com/font/sh"
	"seedhammer.com/gui/op"
	"seedhammer.com/image/rgb565"
)

// The manual's screenshots render here: the real widget code at the
// device's 480x320 and engraver profile, driven by the fixture seeds,
// written as PNGs. Deterministic by construction — fixture phrases, a
// fixed entropy source for the last-word draw — so a regenerated set
// only differs where a screen really changed.
//
//	go test ./gui -run TestUIShots -uishots ../docs/images
var uishotsDir = flag.String("uishots", "", "write the manual's screenshots to this directory")

// shotUI is runUI plus a capture hook: naming a pending shot makes
// the next frame rasterize through the same drawer the device runs,
// before the op buffer resets.
type shotUI struct {
	t       *testing.T
	dir     string
	ctx     *Context
	frame   func() (string, bool)
	quit    func()
	pending string
}

func newShotUI(t *testing.T, dir string, p *testPlatform, ui func(ctx *Context)) *shotUI {
	s := &shotUI{t: t, dir: dir}
	ctx := NewContext(p)
	s.ctx = ctx
	// runUI's pull loop, with the rasterize hook ahead of the reset:
	// the ops are only valid inside the callback.
	s.frame, s.quit = iter.Pull(func(yield func(content string) bool) {
		ctx.FrameCallback = func(o op.Op) {
			r := image.Rectangle{Max: ctx.Platform.DisplaySize()}
			if s.pending != "" {
				fb := rgb565.New(r)
				new(op.Drawer).Draw(fb, image.NewAlpha(r), o)
				s.save(fb)
				s.pending = ""
			}
			content := new(op.Drawer).ExtractText(r, o)
			ctx.Reset()
			ctx.Done = ctx.Done || !yield(content)
		}
		ui(ctx)
	})
	return s
}

func (s *shotUI) save(fb *rgb565.Image) {
	s.t.Helper()
	b := fb.Bounds()
	rgba := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			rgba.Set(x, y, fb.At(x, y))
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, rgba); err != nil {
		s.t.Fatal(err)
	}
	path := filepath.Join(s.dir, s.pending+".png")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		s.t.Fatal(err)
	}
	s.t.Logf("wrote %s", path)
}

func (s *shotUI) await(marker string) {
	s.t.Helper()
	awaitUI(s.t, s.frame, marker)
}

func (s *shotUI) pump(n int) {
	for range n {
		s.frame()
	}
}

// capture names the next frame and pumps it out.
func (s *shotUI) capture(name string) {
	s.t.Helper()
	s.pending = name
	s.frame()
	if s.pending != "" {
		s.t.Fatalf("shot %s never rendered", name)
	}
}

func (s *shotUI) drain() {
	for range 10000 {
		if _, ok := s.frame(); !ok {
			return
		}
	}
}

func shotPlatform(nfc *testNFC) *testPlatform {
	p := newPlatform()
	p.dims = image.Pt(480, 320)
	p.params = engrave.SH2Params
	p.nfc = nfc
	return p
}

func TestUIShots(t *testing.T) {
	if *uishotsDir == "" {
		t.Skip("run with -uishots <dir> to write the manual's screenshots")
	}
	if err := os.MkdirAll(*uishotsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The last-word draw reads its bits from here; zeros keep the
	// drawn word stable across regenerations.
	oldRand := Rand
	Rand = bytes.NewReader(make([]byte, 1024))
	defer func() { Rand = oldRand }()

	t.Run("multisig", func(t *testing.T) { shootMultisig(t, *uishotsDir) })
	t.Run("seed", func(t *testing.T) { shootSeed(t, *uishotsDir) })
	t.Run("plates", func(t *testing.T) { shootPlates(t, *uishotsDir) })
}

// shootMultisig walks the builder end to end: the fixture 2-of-2 the
// flow test drives, with a detour through the passphrase warning.
func shootMultisig(t *testing.T, dir string) {
	nfc := newTestNFC()
	s := newShotUI(t, dir, shotPlatform(nfc), func(ctx *Context) {
		newInputFlow(ctx, &descriptorTheme)
	})
	defer s.quit()

	s.await("Choose what to enter")
	click(&s.ctx.Router, Down, Down, Down) // MULTISIG WALLET
	s.pump(2)
	s.capture("msw-01-input-menu")
	click(&s.ctx.Router, Button3)

	s.await("Wallet Title")
	runes(&s.ctx.Router, "vault 1")
	s.pump(2)
	s.capture("msw-02-title")
	click(&s.ctx.Router, Button2)

	s.await("How many keys share the wallet?")
	s.capture("msw-03-cosigners")
	click(&s.ctx.Router, Up) // 2 COSIGNERS
	s.pump(2)
	click(&s.ctx.Router, Button3)

	s.await("How many must sign to spend?")
	s.capture("msw-04-threshold")
	click(&s.ctx.Router, Button3) // 2 OF 2

	s.await("Add the cosigner's key")
	s.capture("msw-05-source")
	click(&s.ctx.Router, Down) // TAP SEED
	s.pump(2)
	click(&s.ctx.Router, Button3)

	s.await("Tap the cosigner's seed")
	s.capture("msw-06-tap-seed")
	nfc.payloads <- []byte(goldenSeedOil)
	s.await("oil")
	s.capture("msw-07-seed-review")
	click(&s.ctx.Router, Button3)

	// The passphrase ask, into the warning and back out to NO.
	s.await("Add a passphrase to this seed?")
	click(&s.ctx.Router, Down)
	s.pump(2)
	click(&s.ctx.Router, Button3) // ADD PASSPHRASE
	s.await("changes this cosigner")
	s.capture("msw-08-passphrase-warning")
	click(&s.ctx.Router, Button1) // back out
	s.await("Add a passphrase to this seed?")
	click(&s.ctx.Router, Button3) // NO PASSPHRASE

	s.await("Confirm to add this cosigner")
	s.capture("msw-09-cosigner-confirm")
	click(&s.ctx.Router, Button3)

	// Cosigner 2, without detours.
	s.await("Cosigner 2 of 2")
	click(&s.ctx.Router, Down)
	s.pump(2)
	click(&s.ctx.Router, Button3)
	s.await("Tap the cosigner's seed")
	nfc.payloads <- []byte(goldenSeedBacon)
	s.await("bacon")
	click(&s.ctx.Router, Button3)
	s.await("Add a passphrase to this seed?")
	click(&s.ctx.Router, Button3)
	s.await("Confirm to add this cosigner")
	click(&s.ctx.Router, Button3)

	s.await("Create Wallet?")
	s.capture("msw-10-review")
	press(&s.ctx.Router, Button3)
	s.frame()
	time.Sleep(confirmDelay + 50*time.Millisecond)
	s.pump(3)

	s.await("Export Wallet")
	s.capture("msw-11-export")
	click(&s.ctx.Router, Button3)

	s.await("First Address")
	s.capture("msw-12-first-address")
	click(&s.ctx.Router, Button3)

	s.await("Cosigner 1 of 2, 2A77E0A6")
	s.capture("msw-13-seed-plate-gate")
	click(&s.ctx.Router, Down)
	s.pump(2)
	click(&s.ctx.Router, Button3) // SKIP
	s.await("Cosigner 2 of 2, 9A6A2580")
	click(&s.ctx.Router, Down)
	s.pump(2)
	click(&s.ctx.Router, Button3) // SKIP

	s.await("Engrave Descriptor")
	s.capture("msw-14-descriptor")
	click(&s.ctx.Router, Button3)
	s.await("SPLIT: 2 PLATES")
	s.capture("msw-15-split-choice")
	click(&s.ctx.Router, Button1) // back out; the split how-to owns the rest
	s.await("Engrave Descriptor")
	click(&s.ctx.Router, Button1)
	s.drain()
}

// shootSeed captures the single-seed additions: the title question,
// the last-word offer and draw, and a titled small plate's preview.
func shootSeed(t *testing.T, dir string) {
	// The last-word screens, driven directly on the words flow.
	{
		s := newShotUI(t, dir, shotPlatform(nil), func(ctx *Context) {
			m := goldenMnemonic(t, goldenSeedOil)
			words := make(bip39.Mnemonic, len(m))
			copy(words, m)
			words[len(words)-1] = -1
			inputWordsFlow(ctx, &descriptorTheme, words, len(words)-1)
		})
		s.await("Input Words")
		s.capture("seed-01-lastword-offer")
		click(&s.ctx.Router, Button2)
		s.await("New Seed?")
		s.capture("seed-02-lastword-gate")
		click(&s.ctx.Router, Button3)
		s.await("Random Word 12")
		s.capture("seed-03-lastword-draw")
		click(&s.ctx.Router, Button3) // accept; flow returns
		s.drain()
	}
	// The title question and the titled small plate, through the
	// backup flow with the plates left uncut.
	{
		s := newShotUI(t, dir, shotPlatform(nil), func(ctx *Context) {
			backupWalletFlow(ctx, &descriptorTheme, goldenMnemonic(t, goldenSeedOil))
		})
		defer s.quit()
		s.await("Engrave Seed")
		click(&s.ctx.Router, Button3)
		s.await("Add a passphrase to this seed?")
		click(&s.ctx.Router, Button3) // NO
		s.await("Name this wallet on its plates?")
		s.capture("seed-04-title-ask")
		click(&s.ctx.Router, Button3) // ADD TITLE
		s.await("Wallet Title")
		runes(&s.ctx.Router, "vault 1")
		s.pump(2)
		click(&s.ctx.Router, Button2)
		s.await("Engrave the seed phrase?")
		click(&s.ctx.Router, Button3) // ENGRAVE SEED
		s.await("Plate Size")
		s.capture("seed-05-plate-size")
		click(&s.ctx.Router, Button3) // SMALL PLATE
		s.await("mm")                 // the engrave screen's dims line
		s.capture("seed-06-titled-plate")
		// Leave without cutting: back out of the engrave screen, then
		// discard the seed so the flow ends.
		click(&s.ctx.Router, Button1)
		s.await("Engrave Seed")
		click(&s.ctx.Router, Button1)
		s.await("Discard Seed?")
		press(&s.ctx.Router, Button3)
		s.frame()
		time.Sleep(confirmDelay + 50*time.Millisecond)
		s.pump(3)
		s.drain()
	}
}

// writePNG saves img under dir as name.png.
func writePNG(t *testing.T, dir, name string, img image.Image) {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".png")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s", path)
}

// renderPlate draws a finished plate the way the manual wants to show
// it: the planner's own spline sampled exactly as the on-device
// preview samples it, at eight pixels per millimeter, dilated to the
// engraver's stroke width, dark on paper inside the plate's outline.
func renderPlate(t *testing.T, dir, name string, plan engrave.Engraving, params engrave.Params, plateSize PlateSize) {
	t.Helper()
	const pxPerMM = 8
	side := 85 * pxPerMM
	r := newSplineRasterizer(side, params, plateSize)
	if _, err := planPlateWalk(plan, params, plateSize, nil, r.knot); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	b := r.preview.Bounds()
	const margin = 24
	img := image.NewRGBA(image.Rect(0, 0, b.Dx()+2*margin, b.Dy()+2*margin))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	// The plate outline, two pixels, at the plate's exact edge.
	edge := color.RGBA{R: 0xb0, G: 0xb0, B: 0xb0, A: 0xff}
	for x := margin - 2; x < margin+b.Dx()+2; x++ {
		for _, y := range []int{margin - 2, margin - 1, margin + b.Dy(), margin + b.Dy() + 1} {
			img.SetRGBA(x, y, edge)
		}
	}
	for y := margin - 2; y < margin+b.Dy()+2; y++ {
		for _, x := range []int{margin - 2, margin - 1, margin + b.Dx(), margin + b.Dx() + 1} {
			img.SetRGBA(x, y, edge)
		}
	}
	// The strokes: every sampled point as a disc of the engraver's
	// stroke radius.
	radius := max(1, params.StrokeWidth*pxPerMM/(2*params.Millimeter))
	ink := color.RGBA{R: 0x20, G: 0x20, B: 0x20, A: 0xff}
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			if _, _, _, a := r.preview.At(x, y).RGBA(); a == 0 {
				continue
			}
			for dy := -radius; dy <= radius; dy++ {
				for dx := -radius; dx <= radius; dx++ {
					if dx*dx+dy*dy > radius*radius {
						continue
					}
					img.SetRGBA(margin+x+dx, margin+y+dy, ink)
				}
			}
		}
	}
	writePNG(t, dir, name, img)
}

// shootPlates renders the finished plates of the manual's wallet: the
// titled small seed plate, the 2-of-3 descriptor as one plate and as
// cosigner 1's share, and a titled passphrase plate.
func shootPlates(t *testing.T, dir string) {
	params := engrave.SH2Params

	seedDesc, err := seedPlate(goldenMnemonic(t, goldenSeedOil), "", "vault 1")
	if err != nil {
		t.Fatal(err)
	}
	seedDesc.Size = SmallPlate
	plan, err := backup.EngraveSeed(params, seedDesc)
	if err != nil {
		t.Fatal(err)
	}
	renderPlate(t, dir, "plate-seed-small-titled", plan, params, SmallPlate)

	desc := goldenDescriptor(t)
	labels, texts, qrText, err := fitDescriptor(params, SquarePlate, desc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if labels[0] != "TEXT + QR" {
		t.Fatalf("expected TEXT + QR first, got %v", labels)
	}
	txt := texts[0]
	for i := range txt.Paragraphs {
		p := &txt.Paragraphs[i]
		if p.QR == nil {
			continue
		}
		code, err := qr.Encode(qrText, qr.L)
		if err != nil {
			t.Fatal(err)
		}
		p.QR = code
	}
	renderPlate(t, dir, "plate-descriptor", backup.EngraveText(params, txt), params, SquarePlate)

	data, size, scale, err := fitShares(params, desc, nil)
	if err != nil {
		t.Fatal(err)
	}
	stxt, urs, err := shareText(desc, data, 0, size, scale)
	if err != nil {
		t.Fatal(err)
	}
	for i := range stxt.Paragraphs {
		p := &stxt.Paragraphs[i]
		if p.QR == nil {
			continue
		}
		code, err := qr.Encode(urs[i], qr.L)
		if err != nil {
			t.Fatal(err)
		}
		p.QR = code
	}
	renderPlate(t, dir, "plate-share-1of3", backup.EngraveText(params, stxt), params, SquarePlate)

	m := goldenMnemonic(t, goldenSeedOil)
	fp, ok := walletFingerprint(m, "hunter2")
	if !ok {
		t.Fatal("no fingerprint")
	}
	text := passphrasePlate("hunter2", fp, "m/84h/0h/0h", "vault 1")
	sizes, _, err := fitText(params, SmallPlate, text)
	if err != nil {
		t.Fatal(err)
	}
	renderPlate(t, dir, "plate-passphrase", backup.EngraveText(params, backup.Text{
		Size:       SmallPlate,
		Paragraphs: []backup.Paragraph{{Text: text}},
		Font:       sh.Font,
		FontSize:   sizes[0],
	}), params, SmallPlate)
}
