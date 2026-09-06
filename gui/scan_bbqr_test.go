package gui

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"seedhammer.com/bbqr"
	"seedhammer.com/bip380"
	"seedhammer.com/bip39"
	"seedhammer.com/shamir"
)

// scanRecord delivers one NFC record and returns the scanner's verdict
// for it: the loop drains the record's chunks until Scan settles.
func scanRecord(t *testing.T, s *scanner, record string) (any, error) {
	t.Helper()
	buf := new(bytes.Buffer)
	buf.WriteString(record)
	for {
		obj, err := s.Scan(buf)
		if err == errScanInProgress {
			continue
		}
		return obj, err
	}
}

func TestScanBBQrSingle(t *testing.T) {
	m, err := bip39.ParseMnemonic("legal winner thank year wave sausage worth useful legal winner thank yellow")
	if err != nil {
		t.Fatal(err)
	}
	ser, err := bbqr.Split([]byte(m.String()), bbqr.TypeText, bbqr.SplitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	s := new(scanner)
	for _, part := range ser.Parts {
		obj, err := scanRecord(t, s, part)
		if err != nil {
			t.Fatal(err)
		}
		if obj != nil {
			got, ok := obj.(bip39.Mnemonic)
			if !ok {
				t.Fatalf("got %T, want bip39.Mnemonic", obj)
			}
			if !reflect.DeepEqual(got, m) {
				t.Fatal("mnemonic mismatch")
			}
			return
		}
	}
	t.Fatal("series completed without an object")
}

func TestScanBBQrMultiPart(t *testing.T) {
	// Incompressible in the engraveable charset: random letters.
	rng := rand.New(rand.NewSource(9))
	var sb strings.Builder
	for sb.Len() < 1000 {
		sb.WriteByte('A' + byte(rng.Intn(26)))
	}
	text := sb.String()
	ser, err := bbqr.Split([]byte(text), bbqr.TypeText, bbqr.SplitOptions{Encoding: bbqr.EncBase32, MaxVersion: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(ser.Parts) < 3 {
		t.Fatalf("test needs several parts, got %d", len(ser.Parts))
	}
	s := new(scanner)
	// Feed parts out of order: progress must track and the final part
	// must unwrap to the text.
	order := rand.New(rand.NewSource(1)).Perm(len(ser.Parts))
	for i, idx := range order {
		obj, err := scanRecord(t, s, ser.Parts[idx])
		if i < len(order)-1 {
			if !errors.Is(err, errScanProgress) {
				t.Fatalf("part %d: got %v, want progress", i, err)
			}
			if s.detail == "" {
				t.Fatal("no progress detail")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := obj.(plainText); !ok || string(got) != text {
			t.Fatalf("got %v %v, want the text", obj, err)
		}
	}
}

func TestScanBBQrShares(t *testing.T) {
	m, err := bip39.ParseMnemonic("legal winner thank year wave sausage worth useful legal winner thank yellow")
	if err != nil {
		t.Fatal(err)
	}
	series, err := shamir.SplitData(bbqr.TypeText, []byte(m.String()), 2, 3, rand.New(rand.NewSource(2)))
	if err != nil {
		t.Fatal(err)
	}
	s := new(scanner)
	// Feed share 2's series, then share 0's, part by part.
	for _, share := range []int{2, 0} {
		for _, part := range series[share].Parts {
			obj, err := scanRecord(t, s, part)
			if obj == nil {
				if !errors.Is(err, errScanProgress) {
					t.Fatalf("want progress, got %v", err)
				}
				continue
			}
			got, ok := obj.(bip39.Mnemonic)
			if !ok || !reflect.DeepEqual(got, m) {
				t.Fatalf("recovered %v (%T), want the mnemonic", obj, obj)
			}
			return
		}
	}
	t.Fatal("two shares did not recover")
}

// TestScanBBQrSeriesReset: a part from a different series mid-way
// drops the partial one and starts over.
func TestScanBBQrSeriesReset(t *testing.T) {
	textA := strings.Repeat("AAAA ", 199) + "AAAA"
	textB := strings.Repeat("BBBB ", 199) + "BBBB"
	a, err := bbqr.Split([]byte(textA), bbqr.TypeText, bbqr.SplitOptions{Encoding: bbqr.EncBase32, MaxVersion: 5})
	if err != nil {
		t.Fatal(err)
	}
	b, err := bbqr.Split([]byte(textB), bbqr.TypeText, bbqr.SplitOptions{Encoding: bbqr.EncBase32, MaxVersion: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Parts) < 2 || len(b.Parts) < 2 {
		t.Fatal("test needs multi-part series")
	}
	s := new(scanner)
	if _, err := scanRecord(t, s, a.Parts[0]); !errors.Is(err, errScanProgress) {
		t.Fatalf("part of A: %v", err)
	}
	// Switch to B: the partial A is dropped.
	for i, part := range b.Parts {
		obj, err := scanRecord(t, s, part)
		if i < len(b.Parts)-1 {
			if !errors.Is(err, errScanProgress) {
				t.Fatalf("part %d of B: %v", i, err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := obj.(plainText); !ok || string(got) != textB {
			t.Fatalf("got %v, want B's text", obj)
		}
	}
}

// TestScanBBQrNested: a series whose payload is itself a (single part)
// series unwraps twice.
func TestScanBBQrNested(t *testing.T) {
	inner, err := bbqr.Split([]byte("INNER MOST TEXT"), bbqr.TypeText, bbqr.SplitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	outer, err := bbqr.Split([]byte(inner.Parts[0]), bbqr.TypeBinary, bbqr.SplitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	s := new(scanner)
	for _, part := range outer.Parts {
		obj, err := scanRecord(t, s, part)
		if obj == nil {
			if !errors.Is(err, errScanProgress) {
				t.Fatalf("want progress, got %v", err)
			}
			continue
		}
		if got, ok := obj.(plainText); !ok || string(got) != "INNER MOST TEXT" {
			t.Fatalf("got %v, want the nested text", obj)
		}
		return
	}
	t.Fatal("nested series produced no object")
}

// TestScanBBQrTextLookalike: text that opens with "B$" but fails the
// header rules is still plain text.
func TestScanBBQrTextLookalike(t *testing.T) {
	s := new(scanner)
	obj, err := scanRecord(t, s, "B$ NOT A QR AT ALL")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := obj.(plainText); !ok || string(got) != "B$ NOT A QR AT ALL" {
		t.Fatalf("got %v, want plain text", obj)
	}
}

// TestScanBBQrShareDescriptor recovers the machine's own share plates:
// the exact part strings fitShares engraves, fed back through the
// scanner, must land a descriptor object, or the split how-to's
// recovery promise is false. The plates carry the canonical form of
// the wallet (splitDescriptor), so that is what comes back.
func TestScanBBQrShareDescriptor(t *testing.T) {
	desc := testMultisig(t, 2, 3)
	_, plans, err := fitShares(engraverParams, desc, nil)
	if err != nil {
		t.Fatal(err)
	}
	sp := plans[0]
	s := new(scanner)
	for _, share := range []int{2, 0} {
		_, qrTexts, err := sp.plateContent(share)
		if err != nil {
			t.Fatal(err)
		}
		for _, part := range qrTexts {
			obj, err := scanRecord(t, s, part)
			if obj == nil {
				if !errors.Is(err, errScanProgress) {
					t.Fatalf("want progress, got %v", err)
				}
				continue
			}
			got, ok := obj.(*bip380.Descriptor)
			if !ok {
				t.Fatalf("recovered %T, want a descriptor", obj)
			}
			if got.Encode() != splitDescriptor(desc).Encode() {
				t.Fatal("recovered descriptor differs")
			}
			return
		}
	}
	t.Fatal("a quorum of share plates did not recover the descriptor")
}

// sharePlates returns the QR contents of every share plate of a
// threshold-of-n split of a test wallet, plate by plate, with the
// canonical descriptor the plates recover to.
func sharePlates(t *testing.T, threshold, nkeys int) (plates [][]string, want string) {
	t.Helper()
	desc := testMultisig(t, threshold, nkeys)
	labels, plans, err := fitShares(engraverParams, desc, nil)
	if err != nil {
		t.Fatal(err)
	}
	// QR ONLY fits every quorum the machine splits and carries the
	// codes the other styles engrave.
	sp := plans[slices.Index(labels, "QR ONLY")]
	for k := range nkeys {
		_, qrTexts, err := sp.plateContent(k)
		if err != nil {
			t.Fatal(err)
		}
		plates = append(plates, qrTexts)
	}
	return plates, splitDescriptor(desc).Encode()
}

// corruptPlate re-encodes a plate's share envelope with one byte of
// its share data flipped: a plate whose parts scan clean but whose
// share lies off the set's polynomial.
func corruptPlate(t *testing.T, parts []string) []string {
	t.Helper()
	_, payload, err := bbqr.Join(parts)
	if err != nil {
		t.Fatal(err)
	}
	payload[len(payload)-1] ^= 0xFF
	bad, err := bbqr.Split(payload, bbqr.TypeShamir, bbqr.SplitOptions{Encoding: bbqr.EncBase32})
	if err != nil {
		t.Fatal(err)
	}
	return bad.Parts
}

// scanPlate feeds one plate's parts and returns the last part's
// verdict; every earlier part must report progress.
func scanPlate(t *testing.T, s *scanner, parts []string) (any, error) {
	t.Helper()
	for _, part := range parts[:len(parts)-1] {
		if _, err := scanRecord(t, s, part); !errors.Is(err, errScanProgress) {
			t.Fatalf("mid-plate part: %v, want progress", err)
		}
	}
	return scanRecord(t, s, parts[len(parts)-1])
}

// TestScanBBQrShareAmbiguous: plates 1 and 4 of a 3-of-5 wrong in the
// same byte by the same mask read two ways once all five are held (1
// xor 4 = 5 makes every Lagrange weight of {1, 4, 5} one, so the
// errors cancel), and the machine asks for another plate instead of
// a spare.
func TestScanBBQrShareAmbiguous(t *testing.T) {
	plates, _ := sharePlates(t, 3, 5)
	plates[0] = corruptPlate(t, plates[0])
	plates[3] = corruptPlate(t, plates[3])
	s := new(scanner)
	for k := range 5 {
		obj, err := scanPlate(t, s, plates[k])
		if obj != nil || !errors.Is(err, errScanProgress) {
			t.Fatalf("plate %d: %v %v, want progress", k+1, obj, err)
		}
	}
	if s.detail != detailAmbiguous {
		t.Fatalf("detail %q, want %q", s.detail, detailAmbiguous)
	}
	if have, _ := s.shares.Progress(); have != 5 {
		t.Fatalf("set holds %d shares, want 5", have)
	}
}

// TestScanBBQrShareCorrupt: a corrupt plate inside the quorum is not
// the end of the set. At the threshold the scanner keeps every plate
// and asks for a spare; the spare recovers the descriptor and names
// the corrupt plate beside it.
func TestScanBBQrShareCorrupt(t *testing.T) {
	plates, want := sharePlates(t, 3, 5)
	const corrupt = 2 // plate 2, the second of the quorum
	plates[corrupt-1] = corruptPlate(t, plates[corrupt-1])
	s := new(scanner)
	for k := range 3 {
		obj, err := scanPlate(t, s, plates[k])
		if obj != nil || !errors.Is(err, errScanProgress) {
			t.Fatalf("plate %d: %v %v, want progress", k+1, obj, err)
		}
		want := fmt.Sprintf("SHARE %d OF 3", k+1)
		if k == 2 {
			want = detailCorrupt
		}
		if s.detail != want {
			t.Errorf("plate %d: detail %q, want %q", k+1, s.detail, want)
		}
	}
	if have, _ := s.shares.Progress(); have != 3 {
		t.Fatalf("set holds %d shares after the corrupt quorum, want 3", have)
	}

	// A clean spare: the leave-one-out recovers the descriptor bare
	// and names plate 2 beside it.
	obj, err := scanPlate(t, s, plates[3])
	if err != nil {
		t.Fatal(err)
	}
	got, ok := obj.(*bip380.Descriptor)
	if !ok {
		t.Fatalf("recovered %T, want a descriptor", obj)
	}
	if got.Encode() != want {
		t.Error("recovered descriptor differs")
	}
	if !slices.Equal(s.corrupt, []int{corrupt}) {
		t.Errorf("corrupt plates %v, want [%d]", s.corrupt, corrupt)
	}
	if have, _ := s.shares.Progress(); have != 0 {
		t.Errorf("set holds %d shares after recovery, want none", have)
	}
	// The verdict belongs to that recovery alone: the next record
	// starts clean.
	if _, err := scanRecord(t, s, "hello plate"); err != nil {
		t.Fatal(err)
	}
	if s.corrupt != nil {
		t.Errorf("corrupt plates %v carried into the next record", s.corrupt)
	}
}

// TestCorruptScreen pins the acknowledgment's words and that they fit
// the device display the way ChoiceScreen lays them out: the title on
// one line, the lead inside its band.
func TestCorruptScreen(t *testing.T) {
	styles := NewStyles()
	for _, test := range []struct {
		plates      []int
		title, lead string
	}{
		{[]int{4}, "Plate 4 corrupt", "Re-cut it from this descriptor."},
		{[]int{1, 4}, "2 plates corrupt", "Plates 1, 4: re-cut them from this descriptor."},
		{[]int{1, 2, 3}, "3 plates corrupt", "Plates 1, 2, 3: re-cut them from this descriptor."},
		{[]int{12}, "Plate 12 corrupt", "Re-cut it from this descriptor."},
	} {
		s := corruptScreen(test.plates, &bip380.Descriptor{})
		if s.Title != test.title || s.Lead != test.lead || !slices.Equal(s.Choices, []string{"OK"}) {
			t.Errorf("corruptScreen(%v) = %q / %q / %v, want %q / %q / [OK]", test.plates, s.Title, s.Lead, s.Choices, test.title, test.lead)
		}
		if got, one := styles.title.Measure(testDisplayDim-2*16, "%s", s.Title), styles.title.Measure(math.MaxInt, "%s", s.Title); got.Y != one.Y {
			t.Errorf("title %q wraps on the %d px display", s.Title, testDisplayDim)
		}
		if sz := styles.lead.Measure(testDisplayDim-2*8, "%s", s.Lead); sz.Y > leadingSize {
			t.Errorf("lead %q is %d px tall, over its %d px band", s.Lead, sz.Y, leadingSize)
		}
	}
}

// TestScanBBQrShareCorruptFlow drives the device path end to end: the
// start screen holds a quorum with a bad plate open for a spare and
// keeps the line asking for one up past the status decay; the spare
// tapped, the corrupt plate is named on its own screen before the
// descriptor screen opens.
func TestScanBBQrShareCorruptFlow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		plates, _ := sharePlates(t, 3, 5)
		plates[1] = corruptPlate(t, plates[1])
		p := newPlatform()
		nfc := newTestNFC()
		p.nfc = nfc
		ctx := NewContext(p)
		frame, quit := runUI(ctx, func() {
			startFlow(ctx, &descriptorTheme, new(StartScreen))
		})
		defer quit()
		await := func(marker string) string {
			t.Helper()
			return awaitUI(t, frame, marker)
		}
		tap := func(plate []string) {
			for _, part := range plate {
				nfc.payloads <- []byte(part)
			}
		}
		await("Backup Wallet")
		tap(plates[0])
		await("SHARE 1 OF 3")
		tap(plates[1])
		await("SHARE 2 OF 3")
		tap(plates[2])
		await(detailCorrupt)
		// The set waits for a spare, and the line saying so outlasts
		// the decay of an ordinary progress count.
		wait := scanStatusTimeout + scanStatusTimeout/2
		time.Sleep(wait)
		if content, ok := frame(); !ok || !uiContains(content, detailCorrupt) {
			t.Fatalf("%q gone %v after the tap; frame: %q", detailCorrupt, wait, content)
		}
		tap(plates[3])
		ack := await("Plate 2 corrupt")
		for _, want := range []string{"Re-cut it from this descriptor.", "OK"} {
			if !uiContains(ack, want) {
				t.Errorf("acknowledgment lacks %q: %q", want, ack)
			}
		}
		click(&ctx.Router, Button3) // OK
		await("Engrave Descriptor")
		click(&ctx.Router, Button1) // back out; the start screen pass ends
		for range 10 {
			if _, ok := frame(); !ok {
				return
			}
		}
		t.Fatal("startFlow did not end after the descriptor screen")
	})
}

// TestCosignerEntryCorruptShares: the cosigner landing page gives the
// same word. A share set recovered past a bad plate names it before
// the recovered object is judged; the multisig descriptor is then
// refused, as any descriptor is at the cosigner entry.
func TestCosignerEntryCorruptShares(t *testing.T) {
	plates, _ := sharePlates(t, 3, 5)
	plates[1] = corruptPlate(t, plates[1])
	_, ok := cosignerEntryHarness(t, func(ctx *Context, nfc *testNFC, await func(string)) {
		await("Enter the seed words")
		for _, plate := range plates[:4] {
			for _, part := range plate {
				nfc.payloads <- []byte(part)
			}
		}
		await("Plate 2 corrupt")
		click(&ctx.Router, Button3) // OK
		await("Not a seed or cosigner key")
		click(&ctx.Router, Button1)
	})
	if ok {
		t.Fatal("backing out reported an action")
	}
}

// TestScanBBQrShareForeignResets: a share of another split is not a
// corrupt plate. It drops the held set and starts its own, as before.
func TestScanBBQrShareForeignResets(t *testing.T) {
	a, _ := sharePlates(t, 3, 5)
	b, _ := sharePlates(t, 3, 5)
	s := new(scanner)
	for k := range 2 {
		if _, err := scanPlate(t, s, a[k]); !errors.Is(err, errScanProgress) {
			t.Fatalf("set A plate %d: %v", k+1, err)
		}
	}
	if _, err := scanPlate(t, s, b[0]); !errors.Is(err, errScanProgress) {
		t.Fatalf("set B plate 1: %v", err)
	}
	if have, _ := s.shares.Progress(); have != 1 {
		t.Errorf("set holds %d shares after the foreign plate, want 1", have)
	}
	if s.detail != "SHARE 1 OF 3" {
		t.Errorf("detail %q, want the fresh set's count", s.detail)
	}
}

// TestScanBBQrShareSetLimit: a set kept for a corrupt plate grows by
// one envelope per spare and stops at scanSetLimit; the spare that
// would push it over is refused with the limit error and the set is
// dropped, so no digest failure can grow the heap without bound.
func TestScanBBQrShareSetLimit(t *testing.T) {
	// Envelopes of exactly scanShareLimit bytes: eight fill
	// scanSetLimit, a ninth would exceed it.
	rng := rand.New(rand.NewSource(4))
	data := make([]byte, scanShareLimit-9)
	rng.Read(data)
	series, err := shamir.SplitData(bbqr.TypeBinary, data, 8, 9, rng)
	if err != nil {
		t.Fatal(err)
	}
	if _, payload, _ := bbqr.Join(series[0].Parts); len(payload) != scanShareLimit {
		t.Fatalf("envelope is %d bytes, want %d", len(payload), scanShareLimit)
	}
	s := new(scanner)
	for k := range 8 {
		parts := series[k].Parts
		if k == 0 {
			parts = corruptPlate(t, parts)
		}
		if _, err := scanPlate(t, s, parts); !errors.Is(err, errScanProgress) {
			t.Fatalf("share %d: %v, want progress", k+1, err)
		}
	}
	if s.detail != detailCorrupt {
		t.Fatalf("detail %q, want %q", s.detail, detailCorrupt)
	}
	_, err = scanPlate(t, s, series[8].Parts)
	if err == nil || errors.Is(err, errScanProgress) || !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("ninth share: %v, want the set limit error", err)
	}
	if have, _ := s.shares.Progress(); have != 0 {
		t.Errorf("set holds %d shares after the limit, want none", have)
	}
}

// TestScanBBQrShareConflict: a corrupted duplicate of a held share
// must be rejected without discarding the set, so a clean rescan
// still completes it.
func TestScanBBQrShareConflict(t *testing.T) {
	m, _ := bip39.ParseMnemonic("legal winner thank year wave sausage worth useful legal winner thank yellow")
	series, err := shamir.SplitData(bbqr.TypeText, []byte(m.String()), 2, 3, rand.New(rand.NewSource(3)))
	if err != nil {
		t.Fatal(err)
	}
	s := new(scanner)
	if _, err := scanRecord(t, s, series[0].Parts[0]); !errors.Is(err, errScanProgress) {
		t.Fatalf("share 1: %v", err)
	}
	// Corrupt share 1's envelope body, re-encoded as a valid part.
	_, payload, err := bbqr.Join(series[0].Parts)
	if err != nil {
		t.Fatal(err)
	}
	payload[len(payload)-1] ^= 0xFF
	bad, err := bbqr.Split(payload, bbqr.TypeShamir, bbqr.SplitOptions{Encoding: bbqr.EncBase32})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scanRecord(t, s, bad.Parts[0]); err == nil || errors.Is(err, errScanProgress) {
		t.Fatalf("conflicting share accepted: %v", err)
	}
	// The held set survived: the second legit share completes it.
	obj, err := scanRecord(t, s, series[1].Parts[0])
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := obj.(bip39.Mnemonic); !ok || !reflect.DeepEqual(got, m) {
		t.Fatalf("recovered %v (%T), want the mnemonic", obj, obj)
	}
}

// TestScanBBQrSeriesTooLarge: a series over the decoded-size cap must
// fail the scan instead of silently resetting and looping forever.
func TestScanBBQrSeriesTooLarge(t *testing.T) {
	big, err := bbqr.Split(make([]byte, 20*1024), bbqr.TypeBinary, bbqr.SplitOptions{Encoding: bbqr.EncBase32, MaxVersion: 20})
	if err != nil {
		t.Fatal(err)
	}
	s := new(scanner)
	sawLimit := false
	for pass := 0; pass < 2 && !sawLimit; pass++ {
		for _, part := range big.Parts {
			_, err := scanRecord(t, s, part)
			if errors.Is(err, bbqr.ErrLimit) {
				sawLimit = true
				break
			}
			if !errors.Is(err, errScanProgress) {
				t.Fatalf("unexpected verdict: %v", err)
			}
		}
	}
	if !sawLimit {
		t.Fatal("oversized series never surfaced the size limit")
	}
}
