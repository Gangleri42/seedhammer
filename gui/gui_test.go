package gui

import (
	"bytes"
	"errors"
	"image"
	"image/draw"
	"image/png"
	"io"
	"iter"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/btcsuite/btcd/btcutil/v2/hdkeychain"
	"github.com/btcsuite/btcd/chaincfg/v2"
	"seedhammer.com/backup"
	"seedhammer.com/bip32"
	"seedhammer.com/bip380"
	"seedhammer.com/bip39"
	"seedhammer.com/bspline"
	"seedhammer.com/engrave"
	"seedhammer.com/font/sh"
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

func newTestEngraveScreen(t *testing.T, ctx *Context) *EngraveScreen {
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

	_, engravings, err := validateDescriptor(ctx.Platform.EngraverParams(), desc)
	if err != nil {
		t.Fatal(err)
	}
	return NewEngraveScreen(
		ctx,
		engravings[0],
	)
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
	}
	for _, test := range tests {
		labels, engravings, err := validateDescriptor(engraverParams, multisig(test.threshold, test.nkeys))
		if err != nil {
			t.Fatalf("%d-of-%d: %v", test.threshold, test.nkeys, err)
		}
		if !slices.Equal(labels, test.want) {
			t.Errorf("%d-of-%d: got engravings %q, want %q", test.threshold, test.nkeys, labels, test.want)
		}
		if len(engravings) != len(labels) {
			t.Errorf("%d-of-%d: %d engravings for %d labels", test.threshold, test.nkeys, len(engravings), len(labels))
		}
	}
	// Beyond the largest QR code that fits the plate.
	if _, _, err := validateDescriptor(engraverParams, multisig(9, 16)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("16-key descriptor: got %v, want ErrTooLarge", err)
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
		plate, err := toPlate(plan, engraverParams)
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
		{"short text at the largest size", "IN CASE OF FIRE\n\nBREAK GLASS", 3.8},
		{"full 3.8mm grid", grid(34, 20), 3.8},
		{"wide lines fall back", grid(38, 23), 3.4},
		{"tall compositions fall back", grid(1, 26), 3.0},
		{"full 3.0mm grid", grid(44, 26), 3.0},
		{"descenders on the last row", grid(34, 19) + "\ngjpqy([])", 3.8},
	}
	for _, test := range fits {
		plate, err := validateText(engraverParams, test.text)
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
		{"too wide", line},
		{"too tall", grid(1, 27)},
	}
	for _, test := range tooLarge {
		if _, err := validateText(engraverParams, test.text); !errors.Is(err, ErrTooLarge) {
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
	return nil
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
