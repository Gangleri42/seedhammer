package gui

import (
	"bytes"
	"errors"
	"image"
	"image/draw"
	"image/png"
	"io"
	"iter"
	"math"
	"os"
	"slices"
	"strings"

	"testing"
	"testing/synctest"
	"time"

	"github.com/btcsuite/btcd/btcutil/v2/hdkeychain"
	"github.com/btcsuite/btcd/chaincfg/v2"
	qr "github.com/seedhammer/kortschak-qr"
	"seedhammer.com/backup"
	"seedhammer.com/bip32"
	"seedhammer.com/bip380"
	"seedhammer.com/bip39"
	"seedhammer.com/bspline"
	"seedhammer.com/engrave"
	"seedhammer.com/font/sh"
	"seedhammer.com/gui/assets"
	"seedhammer.com/gui/layout"
	"seedhammer.com/gui/op"
	"seedhammer.com/image/rgb565"
)

func BenchmarkRedraw(b *testing.B) {
	b.ReportAllocs()

	ctx := NewContext(newPlatform())
	var frame op.Op
	ctx.FrameCallback = func(content op.Op) {
		frame = content
		ctx.Done = true
	}
	m := new(StartScreen)
	m.Flow(ctx, &descriptorTheme)
	clip := image.Rectangle{Max: ctx.Platform.DisplaySize()}
	fb := rgb565.New(clip)
	maskfb := image.NewAlpha(clip)
	d := new(op.Drawer)
	for b.Loop() {
		d.Draw(fb, maskfb, frame)
	}
}

func BenchmarkAllocs(b *testing.B) {
	b.ReportAllocs()

	desc := &bip380.Descriptor{
		Script:    bip380.P2WSH,
		Type:      bip380.SortedMulti,
		Threshold: 2,
		Keys:      make([]bip380.Key, 5),
	}
	fillDescriptor(b, desc, desc.Script.DerivationPath(), 12, 0)
	ds := &DescriptorScreen{
		Descriptor: desc,
	}
	m := new(StartScreen)
	screens := []func(*Context){
		func(ctx *Context) {
			m.Flow(ctx, &descriptorTheme)
		},
		func(ctx *Context) {
			ds.Confirm(ctx, &descriptorTheme)
		},
	}
	var frames []func()
	for _, s := range screens {
		it := func(yield func(struct{}) bool) {
			ctx := NewContext(newPlatform())
			ctx.FrameCallback = func(op.Op) {
				ctx.Done = !yield(struct{}{})
				ctx.Reset()
			}
			s(ctx)
		}
		next, quit := iter.Pull(it)
		defer quit()
		frames = append(frames, func() { next() })
	}
	for b.Loop() {
		for _, f := range frames {
			f()
		}
	}
}

func TestAllocs(t *testing.T) {
	res := testing.Benchmark(BenchmarkAllocs)
	if a := res.AllocsPerOp(); a > 0 {
		t.Errorf("got %d allocs, expected %d", a, 0)
	}
}

func dumpUI(t testing.TB, o op.Op, path string) {
	t.Helper()
	clip := image.Rectangle{Max: image.Pt(testDisplayDim, testDisplayDim)}
	fb := rgb565.New(clip)
	maskfb := image.NewAlpha(clip)
	d := new(op.Drawer)
	d.Draw(fb, maskfb, o)
	buf := new(bytes.Buffer)
	if err := png.Encode(buf, fb); err != nil {
		t.Error(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Error(err)
	}
}

func newTestEngraveScreen(t testing.TB, ctx *Context) *EngraveScreen {
	desc := &bip380.Descriptor{
		Script:    bip380.P2WSH,
		Threshold: 2,
		Type:      bip380.SortedMulti,
		Keys: []bip380.Key{
			{
				Network:           &chaincfg.MainNetParams,
				MasterFingerprint: 0x5a0804e3,
				DerivationPath:    bip32.Path{0x80000030, 0x80000000, 0x80000000, 0x80000002},
				KeyData:           []byte{0x3, 0xa9, 0x39, 0x4a, 0x2f, 0x1a, 0x4f, 0x99, 0x61, 0x3a, 0x71, 0x69, 0x56, 0xc8, 0x54, 0xf, 0x6d, 0xba, 0x6f, 0x18, 0x93, 0x1c, 0x26, 0x39, 0x10, 0x72, 0x21, 0xb2, 0x67, 0xd7, 0x40, 0xaf, 0x23},
				ChainCode:         []byte{0xdb, 0xe8, 0xc, 0xbb, 0x4e, 0xe, 0x41, 0x8b, 0x6, 0xf4, 0x70, 0xd2, 0xaf, 0xe7, 0xa8, 0xc1, 0x7b, 0xe7, 0x1, 0xab, 0x20, 0x6c, 0x59, 0xa6, 0x5e, 0x65, 0xa8, 0x24, 0x1, 0x6a, 0x6c, 0x70},
				ParentFingerprint: 0xc7bce7a8,
			},
			{
				Network:           &chaincfg.MainNetParams,
				MasterFingerprint: 0xdd4fadee,
				DerivationPath:    bip32.Path{0x80000030, 0x80000000, 0x80000000, 0x80000002},
				KeyData:           []byte{0x2, 0x21, 0x96, 0xad, 0xc2, 0x5f, 0xde, 0x16, 0x9f, 0xe9, 0x2e, 0x70, 0x76, 0x90, 0x59, 0x10, 0x22, 0x75, 0xd2, 0xb4, 0xc, 0xc9, 0x87, 0x76, 0xea, 0xab, 0x92, 0xb8, 0x2a, 0x86, 0x13, 0x5e, 0x92},
				ChainCode:         []byte{0x43, 0x8e, 0xff, 0x7b, 0x3b, 0x36, 0xb6, 0xd1, 0x1a, 0x60, 0xa2, 0x2c, 0xcb, 0x93, 0x6, 0xee, 0xa3, 0x5, 0xb0, 0x43, 0x9f, 0x1e, 0xa0, 0x9d, 0x59, 0x28, 0x1, 0x5d, 0xe3, 0x73, 0x81, 0x16},
				ParentFingerprint: 0x22969377,
			},
			{
				Network:           &chaincfg.MainNetParams,
				MasterFingerprint: 0x9bacd5c0,
				DerivationPath:    bip32.Path{0x80000030, 0x80000000, 0x80000000, 0x80000002},
				KeyData:           []byte{0x2, 0xfb, 0x72, 0x50, 0x7f, 0xc2, 0xd, 0xdb, 0xa9, 0x29, 0x91, 0xb1, 0x7c, 0x4b, 0xb4, 0x66, 0x13, 0xa, 0xd9, 0x3a, 0x88, 0x6e, 0x73, 0x17, 0x50, 0x33, 0xbb, 0x43, 0xe3, 0xbc, 0x78, 0x5a, 0x6d},
				ChainCode:         []byte{0x95, 0xb3, 0x49, 0x13, 0x93, 0x7f, 0xa5, 0xf1, 0xc6, 0x20, 0x5b, 0x52, 0x5b, 0xb5, 0x7d, 0xe1, 0x51, 0x76, 0x25, 0xe0, 0x45, 0x86, 0xb5, 0x95, 0xbe, 0x68, 0xe7, 0x13, 0x62, 0xd3, 0xed, 0xc5},
				ParentFingerprint: 0x97ec38f9,
			},
		},
	}

	params := ctx.Platform.EngraverParams()
	_, texts, _, err := fitDescriptor(params, SquarePlate, desc, nil)
	if err != nil {
		t.Fatal(err)
	}
	view := &CurvesScreen{title: "Engrave Descriptor"}
	r := newSplineRasterizer(previewSide(ctx.Platform.DisplaySize(), SquarePlate), params, SquarePlate)
	view.preview = r.preview
	plate, err := planPlateWalk(backup.EngraveText(params, texts[0]), params, SquarePlate, nil, r.knot)
	if err != nil {
		t.Fatal(err)
	}
	view.initText(plate, r, params)
	return NewEngraveScreen(
		ctx,
		plate,
		view,
	)
}

// BenchmarkEngraveFrame guards the unified engrave screen's frame:
// the idle state draws the preview mask, the outline and the info
// strip, and the frame loop must not allocate (op.Mask boxes a
// pointer-sized value without allocating; anything larger would).
func BenchmarkEngraveFrame(b *testing.B) {
	ctx := NewContext(newPlatform())
	scr := newTestEngraveScreen(b, ctx)
	dims := image.Pt(480, 320)
	b.ReportAllocs()
	for b.Loop() {
		scr.draw(ctx, &engraveTheme, dims)
		ctx.B.Reset()
	}
}

func TestEngraveFrameAllocs(t *testing.T) {
	if a := testing.Benchmark(BenchmarkEngraveFrame).AllocsPerOp(); a > 0 {
		t.Errorf("engrave frame allocates %d objects per frame, want 0", a)
	}
}

func TestValidateDescriptorFallback(t *testing.T) {
	multisig := func(threshold, nkeys int) *bip380.Descriptor {
		desc := &bip380.Descriptor{
			Script:    bip380.P2WSH,
			Threshold: threshold,
			Type:      bip380.SortedMulti,
			Keys:      make([]bip380.Key, nkeys),
		}
		fillDescriptor(t, desc, desc.Script.DerivationPath(), 12, 0)
		return desc
	}
	tests := []struct {
		threshold, nkeys int
		want             []string
	}{
		// Fits every layout, falling back to smaller text and finer
		// QR modules where needed.
		{2, 3, []string{"TEXT + QR", "TEXT ONLY", "QR ONLY"}},
		// Too long for text wrapped around a QR at any fallback.
		{4, 6, []string{"TEXT ONLY", "QR ONLY"}},
		// Densest supported: the scale-3 code alone overflows the
		// engravable box, so the ladder drops the scale up front.
		{5, 7, []string{"TEXT ONLY", "QR ONLY"}},
	}
	for _, test := range tests {
		labels, texts, qrText, err := fitDescriptor(engraverParams, SquarePlate, multisig(test.threshold, test.nkeys), nil)
		if err != nil {
			t.Fatalf("%d-of-%d: %v", test.threshold, test.nkeys, err)
		}
		if !slices.Equal(labels, test.want) {
			t.Errorf("%d-of-%d: got engravings %q, want %q", test.threshold, test.nkeys, labels, test.want)
		}
		if len(texts) != len(labels) {
			t.Errorf("%d-of-%d: %d engravings for %d labels", test.threshold, test.nkeys, len(texts), len(labels))
		}
		// The fit verdict must agree with the planner: every offered
		// variant plans inside the plate with the real code swapped in
		// for the fit's stand-in, the same substitution
		// planDescriptorPlate makes after the choice.
		for i, txt := range texts {
			if p := &txt.Paragraphs[0]; p.QR != nil {
				qrc, err := qr.Encode(qrText, qr.L)
				if err != nil {
					t.Fatal(err)
				}
				if qrc.Size != p.QR.Size {
					t.Errorf("%d-of-%d %s: stand-in code size %d, real code size %d", test.threshold, test.nkeys, labels[i], p.QR.Size, qrc.Size)
				}
				p.QR = qrc
			}
			if _, err := toPlate(backup.EngraveText(engraverParams, txt), engraverParams, SquarePlate); err != nil {
				t.Errorf("%d-of-%d %s: fit accepted but planning rejects: %v", test.threshold, test.nkeys, labels[i], err)
			}
		}
	}
	// Beyond the largest QR code that fits the plate.
	if _, _, _, err := fitDescriptor(engraverParams, SquarePlate, multisig(9, 16), nil); !errors.Is(err, ErrTooLarge) {
		t.Errorf("16-key descriptor: got %v, want ErrTooLarge", err)
	}

	// The mark-hull fit verdict must agree with the planner's own
	// bounds verdict on every ladder combination, or fitDescriptor's
	// menu would drift from what planning accepts.
	for _, cfg := range [][2]int{{2, 3}, {4, 6}, {5, 7}} {
		desc := multisig(cfg[0], cfg[1])
		enc := desc.Encode()
		qrc, err := qr.Encode(desc.EncodeNoChecksum(), qr.L)
		if err != nil {
			t.Fatal(err)
		}
		// The all-dark stand-in the fit measures with must match the
		// real code in size and in every ladder verdict, or the menu
		// would drift from what the real engraving needs.
		syn := darkCode(qrc.Size)
		if sz, err := qr.MinSize(desc.EncodeNoChecksum(), qr.L); err != nil || sz != qrc.Size {
			t.Errorf("%d-of-%d: MinSize %d (err %v), Encode size %d", cfg[0], cfg[1], sz, err, qrc.Size)
		}
		for _, p := range []backup.Paragraph{
			{Text: enc, QR: qrc},
			{Text: enc},
			{QR: qrc},
		} {
			for _, scale := range []int{3, 2} {
				for _, size := range backup.FontSizes {
					p := p
					p.QRScale = scale
					plan := backup.EngraveText(engraverParams, backup.Text{
						Paragraphs: []backup.Paragraph{p},
						Font:       sh.Font,
						FontSize:   size,
					})
					fits := layoutFits(plan, engraverParams, SquarePlate)
					_, perr := toPlate(plan, engraverParams, SquarePlate)
					if fits != (perr == nil) {
						t.Errorf("%d-of-%d qr=%v text=%v scale=%d size=%v: fit says %v, planner says %v",
							cfg[0], cfg[1], p.QR != nil, p.Text != "", scale, size, fits, perr)
					}
					if p.QR != nil {
						ps := p
						ps.QR = syn
						plan := backup.EngraveText(engraverParams, backup.Text{
							Paragraphs: []backup.Paragraph{ps},
							Font:       sh.Font,
							FontSize:   size,
						})
						if sfits := layoutFits(plan, engraverParams, SquarePlate); sfits != fits {
							t.Errorf("%d-of-%d text=%v scale=%d size=%v: stand-in fit %v, real fit %v",
								cfg[0], cfg[1], p.Text != "", scale, size, sfits, fits)
						}
					}
				}
			}
		}
	}
}

func TestPlanPlateWalk(t *testing.T) {
	plate := backup.Text{
		Paragraphs: []backup.Paragraph{{Text: "IN CASE OF FIRE\nBREAK GLASS"}},
		Font:       sh.Font,
		FontSize:   3.8,
	}
	plan := backup.EngraveText(engraverParams, plate)
	ref, err := toPlate(plan, engraverParams, SquarePlate)
	if err != nil {
		t.Fatal(err)
	}
	calls, lastDone, total := 0, -1, 0
	got, err := planPlateWalk(plan, engraverParams, SquarePlate, func(done, tot int) bool {
		calls++
		if done < lastDone {
			t.Fatalf("pump went backwards: %d after %d", done, lastDone)
		}
		lastDone, total = done, tot
		return true
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Duration != ref.Duration {
		t.Errorf("planned duration %d, toPlate %d", got.Duration, ref.Duration)
	}
	if calls == 0 || total == 0 || lastDone != total {
		t.Errorf("pump: %d calls, final %d/%d, want a full walk", calls, lastDone, total)
	}

	// A pump returning false abandons the plan.
	if _, err := planPlateWalk(plan, engraverParams, SquarePlate, func(done, tot int) bool {
		return false
	}, nil); !errors.Is(err, errPlanCanceled) {
		t.Errorf("cancelled plan: %v, want errPlanCanceled", err)
	}

	// Oversized layouts are rejected without planning.
	big := backup.EngraveText(engraverParams, backup.Text{
		Paragraphs: []backup.Paragraph{{Text: strings.Repeat("W", 2000)}},
		Font:       sh.Font,
		FontSize:   6.0,
	})
	if _, err := planPlateWalk(big, engraverParams, SquarePlate, nil, nil); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversized layout: %v, want ErrTooLarge", err)
	}
}

func TestValidateText(t *testing.T) {
	line := strings.Repeat("W", 45)
	grid := func(cols, rows int) string {
		lines := make([]string, rows)
		for i := range lines {
			lines[i] = line[:cols]
		}
		return strings.Join(lines, "\n")
	}
	// The chosen font size is inferred by comparing durations with a
	// directly built plate.
	directPlate := func(text string, size float32) Plate {
		plan := backup.EngraveText(engraverParams, backup.Text{
			Paragraphs: []backup.Paragraph{{Text: text}},
			Font:       sh.Font,
			FontSize:   size,
		})
		plate, err := toPlate(plan, engraverParams, SquarePlate)
		if err != nil {
			t.Fatal(err)
		}
		return plate
	}
	fits := []struct {
		name string
		text string
		size float32
	}{
		{"short text at the largest size", "IN CASE OF FIRE\n\nBREAK GLASS", 6.0},
		{"full 6.0mm grid", grid(22, 13), 6.0},
		{"full 5.0mm grid", grid(26, 15), 5.0},
		{"full 4.4mm grid", grid(30, 17), 4.4},
		{"full 3.8mm grid", grid(34, 20), 3.8},
		{"wide lines fall back", grid(38, 23), 3.4},
		{"tall compositions fall back", grid(1, 26), 3.0},
		{"full 3.0mm grid", grid(44, 26), 3.0},
		{"descenders on the last row", grid(34, 19) + "\ngjpqy([])", 3.8},
		// Overflowing lines wrap at the largest size whose grid holds
		// the wrapped text, instead of failing outright.
		{"one-liner wraps at the largest size", line, 6.0},
		{"long one-liner wraps mid-ladder", strings.Repeat("W", 500), 4.4},
		{"descriptor-length one-liner wraps at 3.0mm", strings.Repeat("W", 973), 3.0},
		// A composition that fits some grid unwrapped keeps that fit,
		// even when wrapping would allow a larger font: the engraving
		// must match the composed layout.
		{"composed lines never re-wrap", grid(30, 2), 4.4},
		// A line composed one short of a grid's columns anchors the
		// fit: an overflowing line added after it wraps at the
		// anchored size instead of re-flowing the whole plate (and
		// the anchored line) into a bigger font.
		{"anchored 3.0mm line pins the fit", grid(43, 1) + "\n" + strings.Repeat("W", 60), 3.0},
		{"mid-ladder anchor pins the fit", grid(30, 1) + "\n" + strings.Repeat("W", 60), 4.4},
	}
	for _, test := range fits {
		plate, err := validateText(engraverParams, SquarePlate, test.text)
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		if want := directPlate(test.text, test.size); plate.Duration != want.Duration {
			t.Errorf("%s: duration %d, want %d (%.1fmm)", test.name, plate.Duration, want.Duration, test.size)
		}
	}
	tooLarge := []struct {
		name string
		text string
	}{
		{"too tall", grid(1, 27)},
		{"too long to wrap", strings.Repeat("W", 44*26+1)},
	}
	for _, test := range tooLarge {
		if _, err := validateText(engraverParams, SquarePlate, test.text); !errors.Is(err, ErrTooLarge) {
			t.Errorf("%s: got %v, want ErrTooLarge", test.name, err)
		}
	}
}

func TestTextNotice(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		notice string
	}{
		{"plain text", "IN CASE OF FIRE\n\nBREAK GLASS", ""},
		{"prose with common words", "in case of fire break glass and stay calm for the day", ""},
		{"corrupted descriptor", "wsh(sortedmulti(2,[dc567276/48h", "descriptor"},
		{"key origin", "[dc567276/48h/0h/0h/2h]xpub6DiYrf", "descriptor"},
		{"lone xpub", "xpub6DiYrfRwNnjeX4vHsWMajJVFKrb", "descriptor"},
		{"corrupted codex32", "ms13cashsllhdmn9m42vcsamx24zrxgs3qq", "codex32"},
		{"mnemonic with a typo", "legal winner thank year wave sausage worth useful legal winner thank yelow", "seed phrase"},
		{"mnemonic with a bad checksum", "legal winner thank year wave sausage worth useful legal winner thank abandon", "seed phrase"},
	}
	for _, test := range tests {
		got := textNotice(test.text)
		if test.notice == "" && got != "" {
			t.Errorf("%s: got notice %q, want none", test.name, got)
		}
		if test.notice != "" && !strings.Contains(got, test.notice) {
			t.Errorf("%s: got notice %q, want mention of %q", test.name, got, test.notice)
		}
	}
}

func TestEngraveScreenCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEngraver()
		p := newPlatform()
		p.engraver = e
		ctx := NewContext(p)
		frame, quit := runUI(ctx, func() {
			scr := NewEngraveScreen(
				ctx,
				// A slow engrave job, to allow for cancelling to
				// take effect.
				Plate{
					Spline: func(yield func(bspline.Knot) bool) {
						time.Sleep(10 * time.Second)
					},
				},
				nil,
			)
			if ok := scr.Engrave(ctx, &engraveTheme); ok {
				t.Error("EngraveScreen: succeeded unexpectedly")
			}
		})
		defer quit()

		// Start engraving.
		click(&ctx.Router, Button3, Button3, Button3)
		// Hold confirm.
		press(&ctx.Router, Button3)
		if _, ok := frame(); !ok {
			t.Fatal("EngraveScreen: exited unexpectedly")
		}
		time.Sleep(confirmDelay)
		if _, ok := frame(); !ok {
			t.Fatal("EngraveScreen: exited unexpectedly")
		}
		<-e.opens

		// Go back.
		click(&ctx.Router, Button1, Button1, Button1)
		if _, ok := frame(); ok {
			t.Fatal("engrave screen did not cancel")
		}
		// Let the engrave job complete.
		time.Sleep(10 * time.Second)
	})
}

func TestEngraveScreenError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEngraver()
		p := newPlatform()
		p.engraver = e
		ctx := NewContext(p)
		scr := newTestEngraveScreen(t, ctx)
		frame, quit := runUI(ctx, func() {
			scr.Engrave(ctx, &engraveTheme)
		})
		defer quit()

		// Fail during engraving.
		ioErr := errors.New("error during engraving")
		e.ioErr = ioErr
		// Press next until connect is reached.
		click(&ctx.Router, Button3, Button3, Button3)
		// Hold connect.
		press(&ctx.Router, Button3)
		frame()
		time.Sleep(confirmDelay)
	out:
		for {
			select {
			case <-e.closes:
				break out
			default:
				frame()
			}
		}
		content, ok := frame()
		if !ok || !uiContains(content, ioErr.Error()) {
			t.Fatalf("EngraveScreen: no error reported, expected %v", ioErr)
		}
	})
}

func TestEngraveScreen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEngraver()
		p := newPlatform()
		p.engraver = e
		ctx := NewContext(p)
		scr := newTestEngraveScreen(t, ctx)
		success := false
		frame, quit := runUI(ctx, func() {
			success = scr.Engrave(ctx, &engraveTheme)
		})
		defer quit()

		// Press next until connect is reached.
		click(&ctx.Router, Button3, Button3, Button3)
		// Hold connect.
		press(&ctx.Router, Button3)
		frame()
		time.Sleep(confirmDelay)
	loop:
		for {
			frame()
			select {
			case <-e.closes:
				break loop
			case <-p.wakeups:
			}
		}
		click(&ctx.Router, Button3)
		synctest.Wait()
		if _, ok := frame(); ok || !success {
			t.Fatal("EngraveScreen: didn't complete successfully")
		}
	})
}

func TestWordKeyboardScreen(t *testing.T) {
	ctx := NewContext(newPlatform())
	for i := range bip39.NumWords {
		w := bip39.LabelFor(i)
		runes(&ctx.Router, w)
		click(&ctx.Router, Button2)
		m := make(bip39.Mnemonic, 1)
		inputWordsFlow(ctx, &descriptorTheme, m, 0)
		if got := bip39.LabelFor(m[0]); got != w {
			t.Errorf("keyboard mapped %q to %q", w, got)
		}
	}
}

func fillDescriptor(t testing.TB, desc *bip380.Descriptor, path bip32.Path, seedlen int, keyIdx int) bip39.Mnemonic {
	var mnemonic bip39.Mnemonic
	for i := range desc.Keys {
		m := make(bip39.Mnemonic, seedlen)
		for j := range m {
			m[j] = bip39.Word(i*seedlen + j)
		}
		m = m.FixChecksum()
		seed := bip39.MnemonicSeed(m, "")
		network := &chaincfg.MainNetParams
		mk, err := hdkeychain.NewMaster(seed, network)
		if err != nil {
			t.Fatal(err)
		}
		pkey, err := mk.ECPubKey()
		if err != nil {
			t.Fatal(err)
		}
		mfp := bip32.Fingerprint(pkey)
		xpub, err := bip32.Derive(mk, path)
		if err != nil {
			t.Fatal(err)
		}
		pub, err := xpub.ECPubKey()
		if err != nil {
			t.Fatal(err)
		}
		desc.Keys[i] = bip380.Key{
			Network:           network,
			MasterFingerprint: mfp,
			DerivationPath:    path,
			KeyData:           pub.SerializeCompressed(),
			ChainCode:         xpub.ChainCode(),
			ParentFingerprint: xpub.ParentFingerprint(),
		}
		if i == keyIdx {
			mnemonic = m
		}
	}
	return mnemonic
}

type testPlatform struct {
	events   []Event
	wakeups  chan struct{}
	engraver *testEngraver
	nfc      io.ReadCloser
}

const (
	mm             = 6400
	strokeWidth    = 0.3 * mm
	topSpeed       = 30 * mm
	engravingSpeed = 8 * mm
	acceleration   = 250 * mm
	jerk           = 2600 * mm

	testDisplayDim = 240
)

var (
	engraverConf = engrave.StepperConfig{
		TicksPerSecond: topSpeed,
		Speed:          topSpeed,
		EngravingSpeed: engravingSpeed,
		Acceleration:   acceleration,
		Jerk:           jerk,
	}
	engraverParams = engrave.Params{
		StrokeWidth:   strokeWidth,
		Millimeter:    mm,
		StepperConfig: engraverConf,
	}
)

func (*testPlatform) DisplaySize() image.Point {
	return image.Pt(testDisplayDim, testDisplayDim)
}

func (*testPlatform) Dirty(r image.Rectangle) error {
	return nil
}

func (*testPlatform) NextChunk() (draw.RGBA64Image, bool) {
	return nil, false
}

func (p *testPlatform) Wakeup() {
	select {
	case <-p.wakeups:
	default:
	}
	p.wakeups <- struct{}{}
}

func (p *testPlatform) AppendEvents(deadline time.Time, evts []Event) []Event {
	evts = append(evts, p.events...)
	p.events = nil
	return evts
}

func (p *testPlatform) HardwareVersion() string {
	return "v1.0.0-testing"
}

func (p *testPlatform) Features() Features {
	return 0
}

func (p *testPlatform) LockBoot() error {
	panic("not implemented")
}

func (p *testPlatform) EngraverParams() engrave.Params {
	return engraverParams
}

func (p *testPlatform) NFCReader() io.ReadCloser {
	// The explicit nil keeps the interface nil for the default
	// platform, so flows skip the scan worker as on a reader-less
	// build.
	if p.nfc == nil {
		return nil
	}
	return p.nfc
}

func (p *testPlatform) Engraver(stall bool) (Engraver, error) {
	if p.engraver == nil {
		return nil, errors.New("engraver unavailable")
	}
	select {
	case p.engraver.opens <- struct{}{}:
	default:
	}
	return p.engraver, nil
}

type testEngraver struct {
	ioErr  error
	closes chan struct{}
	opens  chan struct{}
}

func (p *testEngraver) Stats() EngraverStats {
	return EngraverStats{}
}

func (p *testEngraver) Write(steps []uint32) (int, error) {
	err := p.ioErr
	p.ioErr = nil
	if err != nil {
		return 0, err
	}
	return len(steps), nil
}

func (p *testEngraver) Close() error {
	select {
	case p.closes <- struct{}{}:
	default:
	}
	err := p.ioErr
	p.ioErr = nil
	return err
}

func newPlatform() *testPlatform {
	t := &testPlatform{
		wakeups: make(chan struct{}, 1),
	}
	return t
}

func newEngraver() *testEngraver {
	t := &testEngraver{
		closes: make(chan struct{}, 1),
		opens:  make(chan struct{}, 1),
	}
	return t
}

func runUI(ctx *Context, ui func()) (frame func() (string, bool), close func()) {
	return iter.Pull(func(yield func(content string) bool) {
		ctx.FrameCallback = func(o op.Op) {
			r := image.Rectangle{Max: ctx.Platform.DisplaySize()}
			d := new(op.Drawer)
			content := d.ExtractText(r, o)
			ctx.Reset()
			ctx.Done = ctx.Done || !yield(content)
		}
		ui()
	})
}

func uiContains(content, str string) bool {
	str = strings.ToLower(str)
	txt := strings.ToLower(content)
	clean := strings.ReplaceAll(strings.ToLower(str), " ", "")
	return strings.Contains(txt, clean)
}

func TestSeedColumns(t *testing.T) {
	// Mirror SeedScreen.Draw's measurements at the touch-mode 480x320
	// display: the derived column split must reproduce the historical
	// two-column, 12-row layout for 24 words and keep every mnemonic
	// length inside the list area.
	style := NewStyles().word
	longestPrefix := style.Measure(math.MaxInt, "24: ")
	txt := style.Measure(math.MaxInt, widestWord)
	longest := image.Pt(longestPrefix.X+txt.X, txt.Y)
	lineHeight := longest.Y + 2
	dims := image.Pt(480, 320)
	navw := assets.NavBtnPrimary.Bounds().Dx()
	r := layout.Rectangle{Max: dims}
	list := r.Shrink(leadingSize, 0, 0, 0)
	content := list.Shrink(scrollFadeDist, navw, scrollFadeDist, navw)
	tests := []struct {
		words, cols, rows int
	}{
		{12, 2, 6},
		{18, 2, 9},
		{24, 2, 12},
	}
	for _, test := range tests {
		cols, rows := seedColumns(test.words, content.Dx(), longest.X+16)
		if cols != test.cols || rows != test.rows {
			t.Errorf("seedColumns(%d, %d, %d) = %d cols, %d rows; want %d, %d",
				test.words, content.Dx(), longest.X+16, cols, rows, test.cols, test.rows)
		}
		if h := rows * lineHeight; h > list.Dy() {
			t.Errorf("%d words: %d rows (%dpx) exceed the list height %dpx",
				test.words, rows, h, list.Dy())
		}
	}
}

func TestFitDescriptorScaleFilter(t *testing.T) {
	// fitDescriptor drops a QR scale when the lone code's dot
	// centers span more than the engravable box, skipping the
	// scale's whole ladder row. The bound must stay a floor under
	// the measured hull: every skipped cell must be one the walk
	// would reject, or the variant menu would change.
	multisig := func(threshold, nkeys int) *bip380.Descriptor {
		desc := &bip380.Descriptor{
			Script:    bip380.P2WSH,
			Threshold: threshold,
			Type:      bip380.SortedMulti,
			Keys:      make([]bip380.Key, nkeys),
		}
		fillDescriptor(t, desc, desc.Script.DerivationPath(), 12, 0)
		return desc
	}
	box := plateBounds(engraverParams, SquarePlate)
	span := min(box.Max.X-box.Min.X, box.Max.Y-box.Min.Y)
	droppedAny := false
	for _, cfg := range [][2]int{{2, 3}, {4, 6}, {5, 7}, {9, 16}} {
		desc := multisig(cfg[0], cfg[1])
		qrSize, err := qr.MinSize(desc.EncodeNoChecksum(), qr.L)
		if err != nil {
			t.Fatal(err)
		}
		qrc := darkCode(qrSize)
		for _, scale := range []int{3, 2} {
			if (qrSize*scale-1)*engraverParams.StrokeWidth <= span {
				continue
			}
			droppedAny = true
			for _, text := range []string{desc.Encode(), ""} {
				for _, size := range backup.FontSizes {
					plan := backup.EngraveText(engraverParams, backup.Text{
						Paragraphs: []backup.Paragraph{{Text: text, QR: qrc, QRScale: scale}},
						Font:       sh.Font,
						FontSize:   size,
					})
					if layoutFits(plan, engraverParams, SquarePlate) {
						t.Errorf("%d-of-%d scale %d text=%v size %v: dropped scale fits the plate",
							cfg[0], cfg[1], scale, text != "", size)
					}
				}
			}
		}
	}
	if !droppedAny {
		t.Error("no scale dropped across the corpus; the filter went unexercised")
	}
}

func TestRunJobCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := NewContext(newPlatform())
		var jobErr error
		done := make(chan struct{})
		pumped := make(chan struct{})
		frame, quit := runUI(ctx, func() {
			defer close(done)
			_, jobErr = runJob(ctx, &engraveTheme, func(pump func(done, total int) bool) (struct{}, error) {
				for i := 1; pump(i, 100); i++ {
					if i == 1 {
						close(pumped)
					}
					// A long stretch between pumps: the window in
					// which a cancel must keep the screen alive.
					time.Sleep(time.Minute)
				}
				return struct{}{}, errPlanCanceled
			}, func(pct int) op.Op {
				return op.Op{}
			})
		})
		defer quit()

		if _, ok := frame(); !ok {
			t.Fatal("runJob exited before the cancel")
		}
		// Wait out the worker's first pump, so the cancel lands
		// mid-stretch instead of before it: an early cancel would
		// be acknowledged without any stretch to survive.
		<-pumped
		click(&ctx.Router, Button1)
		// The worker sleeps deep in its stretch; the canceled loop
		// must keep producing frames until a pump observes quit.
		for i := range 8 {
			if _, ok := frame(); !ok {
				t.Fatalf("runJob exited before the worker acknowledged the cancel (frame %d, jobErr %v)", i, jobErr)
			}
		}
		select {
		case <-done:
			t.Fatal("runJob returned while the worker was mid-stretch")
		default:
		}
		// Let the stretch elapse so the next pump unwinds the
		// worker; one drawn frame may still be in flight.
		time.Sleep(2 * time.Minute)
		exited := false
		for range 3 {
			if _, ok := frame(); !ok {
				exited = true
				break
			}
		}
		if !exited {
			t.Fatal("runJob kept drawing after the worker acknowledged the cancel")
		}
		<-done
		if !errors.Is(jobErr, errPlanCanceled) {
			t.Errorf("runJob returned %v, want errPlanCanceled", jobErr)
		}
	})
}

func TestDrawLastWordFlow(t *testing.T) {
	for _, nwords := range []int{12, 24} {
		ctx := NewContext(newPlatform())
		th := &descriptorTheme
		longest := wordBoxSize(ctx, th)
		m := emptyBIP39Mnemonic(nwords)
		for i := range m[:nwords-1] {
			m[i] = bip39.Word(i)
		}
		valid := bip39.LastWords(m[:nwords-1])

		// Backing out must leave the mnemonic alone.
		click(&ctx.Router, Button1)
		if _, ok := drawLastWordFlow(ctx, th, m, longest); ok {
			t.Fatalf("%d words: back accepted a word", nwords)
		}
		if m[nwords-1] != -1 {
			t.Fatalf("%d words: back wrote word %v", nwords, m[nwords-1])
		}

		// Accepting must yield one of the checksum-valid completions.
		click(&ctx.Router, Button3)
		w, ok := drawLastWordFlow(ctx, th, m, longest)
		if !ok {
			t.Fatalf("%d words: accept returned no word", nwords)
		}
		if !slices.Contains(valid, w) {
			t.Errorf("%d words: drew %s, not a valid completion", nwords, bip39.LabelFor(w))
		}
		m[nwords-1] = w
		if !m.Valid() {
			t.Errorf("%d words: completed mnemonic fails its checksum", nwords)
		}
	}
}

// TestDrawLastWordCoversCandidates checks the draw reaches every valid
// completion rather than favouring one, which a masking bug would show
// as a stuck high bit. It is a smoke test, not the distribution soak
// that lives in package bip39.
func TestDrawLastWordCoversCandidates(t *testing.T) {
	ctx := NewContext(newPlatform())
	th := &descriptorTheme
	longest := wordBoxSize(ctx, th)
	m := emptyBIP39Mnemonic(24)
	for i := range m[:23] {
		m[i] = bip39.Word(i)
	}
	seen := make(map[bip39.Word]bool)
	// 8 candidates; 200 draws leaves a miss vanishingly unlikely.
	for range 200 {
		click(&ctx.Router, Button3)
		w, ok := drawLastWordFlow(ctx, th, m, longest)
		if !ok {
			t.Fatal("accept returned no word")
		}
		seen[w] = true
	}
	if want := len(bip39.LastWords(m[:23])); len(seen) != want {
		t.Errorf("drew %d distinct words over 200 draws, want all %d", len(seen), want)
	}
}

// TestLastWordOfferDrawnOnArrival guards a frame-ordering bug. Nothing
// redraws between events, so the offer has to be on the very frame that
// lands on the final word. Reading the state before the word advances
// leaves a button that is pressable but invisible until the next event.
func TestLastWordOfferDrawnOnArrival(t *testing.T) {
	// arrival renders the first frame drawn after typing the
	// second-to-last word and accepting it.
	arrival := func(t *testing.T, prefixComplete bool) image.Image {
		t.Helper()
		ctx := NewContext(newPlatform())
		clip := image.Rectangle{Max: ctx.Platform.DisplaySize()}
		fb := rgb565.New(clip)
		// Rasterise inside the callback: Context.Frame resets the op
		// buffer as soon as it returns, so the ops cannot be drawn later.
		ctx.FrameCallback = func(content op.Op) {
			new(op.Drawer).Draw(fb, image.NewAlpha(clip), content)
			ctx.Done = true
		}
		m := emptyBIP39Mnemonic(12)
		for i := range m[:10] {
			m[i] = bip39.Word(i)
		}
		if !prefixComplete {
			// One hole earlier in the phrase leaves the completions
			// undetermined, so there is nothing to offer.
			m[0] = -1
		}
		runes(&ctx.Router, bip39.LabelFor(bip39.Word(10)))
		click(&ctx.Router, Button2)
		inputWordsFlow(ctx, &descriptorTheme, m, 10)
		if m[10] == -1 {
			t.Fatal("the typed word was not accepted")
		}
		return fb
	}
	withOffer, without := arrival(t, true), arrival(t, false)
	b := withOffer.Bounds()
	diff := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if withOffer.At(x, y) != without.At(x, y) {
				diff++
			}
		}
	}
	// The two frames differ only in the offer, so identical frames mean
	// the button and hint were not drawn on arrival.
	if diff == 0 {
		t.Error("the frame that lands on the final word is identical with and without the offer")
	}
}

// A passphrase survives every path that returns to the seed screen.
// backupWalletFlow used to call passphraseFlow inside its retry loop, so
// cancelling the seed plate sent the user back to an empty passphrase
// field and asked them to retype a confirmed secret from memory.
func TestBackupWalletAsksPassphraseOnce(t *testing.T) {
	ctx := NewContext(newPlatform())
	m, err := bip39.ParseMnemonic(
		"legal winner thank year wave sausage worth useful legal winner thank yellow")
	if err != nil {
		t.Fatal(err)
	}
	frame, quit := runUI(ctx, func() {
		backupWalletFlow(ctx, &descriptorTheme, m)
	})
	defer quit()

	asks, titleAsks, backs := 0, 0, 0
	titleDown := false
	for range 400 {
		content, ok := frame()
		if !ok {
			break
		}
		switch {
		case uiContains(content, "Add a passphrase to this seed?"):
			asks++
			if asks > 1 {
				t.Fatalf("passphrase asked %d times; cancelling the seed plate discarded it", asks)
			}
			click(&ctx.Router, Button3) // NO PASSPHRASE
		case uiContains(content, "Name this wallet on its plates?"):
			// The title is carried like the passphrase: asked once,
			// whatever path returns to the seed screen.
			if !titleDown {
				titleAsks++
				if titleAsks > 1 {
					t.Fatalf("title asked %d times; cancelling the seed plate discarded it", titleAsks)
				}
				titleDown = true
				click(&ctx.Router, Down) // move to NO TITLE
				continue
			}
			titleDown = false
			click(&ctx.Router, Button3) // choose NO TITLE
		case uiContains(content, "Engrave the seed phrase?"):
			backs++
			if backs > 1 {
				return // reached the seed plate twice, asked once: correct
			}
			click(&ctx.Router, Button1) // cancel, back to the seed screen
		default:
			click(&ctx.Router, Button3) // seed screen, and anything else, advances
		}
	}
	t.Fatalf("flow never returned to the seed plate: asks=%d titles=%d backs=%d", asks, titleAsks, backs)
}

// TestTextNoticeGate: a text resembling a damaged backup engraves
// only through the warning page. The hold on the idle preview
// surfaces it, back returns to the preview without starting the job,
// and confirming it starts the engraving.
func TestTextNoticeGate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEngraver()
		p := newPlatform()
		p.engraver = e
		ctx := NewContext(p)
		completed := false
		frame, quit := runUI(ctx, func() {
			completed = textFlow(ctx, &descriptorTheme, plainText("wsh(corrupted"), "")
		})
		defer quit()

		hold := func() {
			press(&ctx.Router, Button3)
			frame()
			time.Sleep(confirmDelay)
		}
		// The short text fits the small plate, so the size question
		// precedes the plan; stay on the square plate.
		awaitUI(t, frame, "Plate Size")
		click(&ctx.Router, Down)
		frame()
		click(&ctx.Router, Button3)
		awaitUI(t, frame, "mm") // the idle preview's dims line
		hold()
		awaitUI(t, frame, "corrupted descriptor")
		click(&ctx.Router, Button1)
		awaitUI(t, frame, "mm")
		select {
		case <-e.opens:
			t.Fatal("backing out of the warning still started the job")
		default:
		}
		hold()
		awaitUI(t, frame, "corrupted descriptor")
		click(&ctx.Router, Button3)
	loop:
		for {
			frame()
			select {
			case <-e.closes:
				break loop
			case <-p.wakeups:
			}
		}
		awaitUI(t, frame, "Engraving completed")
		click(&ctx.Router, Button3)
		for range 10 {
			if _, ok := frame(); !ok {
				break
			}
		}
		synctest.Wait()
		if !completed {
			t.Error("the gated engraving did not complete the text flow")
		}
	})
}

// TestEngraveTextEntryFlow drives the menu row end to end: ENGRAVE
// TEXT sits below the word counts, typing spans a newline, the plate
// confirm shows the planned grid, and backing out of the confirm
// returns to the editor with the text intact rather than discarding a
// composition one press from being engraved.
func TestEngraveTextEntryFlow(t *testing.T) {
	ctx := NewContext(newPlatform())
	frame, quit := runUI(ctx, func() {
		newInputFlow(ctx, &descriptorTheme)
	})
	defer quit()
	content, ok := frame()
	if !ok {
		t.Fatal("input menu drew no frame")
	}
	rows := strings.ToUpper(strings.ReplaceAll(content, " ", ""))
	i12 := strings.Index(rows, "12WORDS")
	i24 := strings.Index(rows, "24WORDS")
	itxt := strings.Index(rows, "ENGRAVETEXT")
	if i12 < 0 || i24 < 0 || itxt < 0 {
		t.Fatalf("input menu misses a row: %q", content)
	}
	if !(i12 < i24 && i24 < itxt) {
		t.Errorf("ENGRAVE TEXT is not below the word counts: %q", content)
	}
	// The cursor moves and the choice land in separate frames: Choose
	// tests its confirm button before it reads the cursor keys.
	click(&ctx.Router, Down)
	frame()
	click(&ctx.Router, Down)
	frame()
	click(&ctx.Router, Button3)
	// The square test display clips the keyboard's left columns, so
	// the marker is the tail of the qwerty row.
	content, ok = frame()
	if !ok || !uiContains(content, "ertyuiop") {
		t.Fatalf("choosing ENGRAVE TEXT did not open the editor: %q", content)
	}
	// The test display is too short for the fragment box (tailFitting
	// draws it empty), so typed text is asserted by behaviour: OK only
	// leads to a plate confirm when a non-empty text was typed.
	runes(&ctx.Router, "hi\nmom")
	frame()
	// The plan draws progress frames until the confirm shows the plate
	// dimensions line.
	confirm := func(step string) {
		t.Helper()
		click(&ctx.Router, Button2)
		// The typed text fits the small plate, so the size question
		// precedes the plan; stay on the square plate.
		for range 10000 {
			content, ok = frame()
			if !ok {
				t.Fatalf("%s: flow ended before the size question", step)
			}
			if uiContains(content, "Plate Size") {
				break
			}
		}
		click(&ctx.Router, Down)
		frame()
		click(&ctx.Router, Button3)
		for range 10000 {
			content, ok = frame()
			if !ok {
				t.Fatalf("%s: flow ended during the plan", step)
			}
			if uiContains(content, "mm") && !uiContains(content, "ertyuiop") {
				return
			}
		}
		t.Fatalf("%s: the plate confirm never appeared", step)
	}
	confirm("typed text")
	// Back returns to the editor with the text intact: OK leads to a
	// second confirm without any retyping.
	click(&ctx.Router, Button1)
	content, ok = frame()
	if !ok || !uiContains(content, "ertyuiop") {
		t.Fatalf("backing out of the confirm did not return to the editor: %q", content)
	}
	confirm("carried text")
	click(&ctx.Router, Button1)
	frame()
	click(&ctx.Router, Button1)
	content, ok = frame()
	if !ok || !uiContains(content, "12 WORDS") {
		t.Fatalf("backing out of the editor did not return to the menu: %q", content)
	}
	click(&ctx.Router, Button1)
	if _, ok := frame(); ok {
		t.Error("backing out of the menu kept the flow alive")
	}
}
