package gui

import (
	"bytes"
	"fmt"
	"math/bits"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	qr "github.com/seedhammer/kortschak-qr"
	"seedhammer.com/backup"
	"seedhammer.com/bbqr"
	"seedhammer.com/bc/urtypes"
	"seedhammer.com/bip380"
	"seedhammer.com/font/sh"
	"seedhammer.com/shamir"
)

func testMultisig(t testing.TB, threshold, nkeys int) *bip380.Descriptor {
	t.Helper()
	desc := &bip380.Descriptor{
		Script:    bip380.P2WSH,
		Threshold: threshold,
		Type:      bip380.SortedMulti,
		Keys:      make([]bip380.Key, nkeys),
	}
	fillDescriptor(t, desc, desc.Script.DerivationPath(), 12, 0)
	return desc
}

func TestFitShares(t *testing.T) {
	tests := []struct {
		threshold, nkeys int
		parts            int // BBQr parts per plate
	}{
		{2, 3, 1},
		{3, 4, 1},
		{2, 4, 1},
		{3, 5, 1},
	}
	for _, test := range tests {
		desc := testMultisig(t, test.threshold, test.nkeys)
		labels, plans, err := fitShares(engraverParams, desc, nil)
		if err != nil {
			t.Fatalf("%d-of-%d: %v", test.threshold, test.nkeys, err)
		}
		for vi, sp := range plans {
			t.Logf("%d-of-%d variant %s: font %.1f scale %d", test.threshold, test.nkeys, labels[vi], sp.fontSize, sp.scale)
			testFitSharesVariant(t, test.threshold, test.nkeys, test.parts, desc, sp, labels[vi] == "TEXT ONLY")
		}
	}
}

// testFitSharesVariant checks one variant of one quorum: part counts,
// headers, and that the fit verdict agrees with the planner.
func testFitSharesVariant(t *testing.T, threshold, nkeys, parts int, desc *bip380.Descriptor, sp *splitPlan, textOnly bool) {
	t.Helper()
	// Plate k names cosigner k of the canonical descriptor, whatever
	// order the fixture listed the keys in.
	canon := splitDescriptor(desc)
	{
		for k := range nkeys {
			txt, qrTexts, err := sp.plateContent(k)
			if err != nil {
				t.Fatal(err)
			}
			if textOnly {
				if len(qrTexts) != 0 {
					t.Errorf("%d-of-%d share %d: text-only variant carries %d codes", threshold, nkeys, k, len(qrTexts))
				}
				// The part strings must be engraved verbatim: the
				// paragraphs' text minus the header is the exact QR
				// content.
				body := strings.Join(splitParagraphTexts(txt), "\n")
				for _, p := range sp.shares[k].Parts {
					if !strings.Contains(body, p) {
						t.Errorf("%d-of-%d share %d: part string missing from the text-only plate", threshold, nkeys, k)
					}
				}
			} else if len(qrTexts) != parts {
				t.Errorf("%d-of-%d share %d: %d parts, want %d",
					threshold, nkeys, k, len(qrTexts), parts)
			}
			// The pairing header opens the first paragraph, threshold
			// included: the plate itself says how many recover.
			head := fmt.Sprintf("%d/%d ANY %d %.8X", k+1, nkeys, threshold, canon.Keys[k].MasterFingerprint)
			if !strings.HasPrefix(txt.Paragraphs[0].Text, head) {
				t.Errorf("%d-of-%d share %d: header %q missing from %.40q",
					threshold, nkeys, k, head, txt.Paragraphs[0].Text)
			}
			// The tag marks the set the plate belongs to.
			if !strings.Contains(txt.Paragraphs[0].Text, fmt.Sprintf("#%04X", sp.tag)) {
				t.Errorf("%d-of-%d share %d: tag missing from %.40q",
					threshold, nkeys, k, txt.Paragraphs[0].Text)
			}
			// The fit verdict must agree with the planner: the plate
			// must plan inside the margins with the real codes swapped
			// in for the fit's stand-ins, the same substitution
			// planDescriptorPlate makes per paragraph. Text-only
			// paragraphs carry no code.
			qi := 0
			for i := range txt.Paragraphs {
				p := &txt.Paragraphs[i]
				if p.QR == nil {
					continue
				}
				qrc, err := qr.Encode(qrTexts[qi], qr.L)
				if err != nil {
					t.Fatal(err)
				}
				if qrc.Size != p.QR.Size {
					t.Errorf("%d-of-%d share %d part %d: stand-in code size %d, real code size %d",
						threshold, nkeys, k, qi, p.QR.Size, qrc.Size)
				}
				p.QR = qrc
				qi++
			}
			if qi != len(qrTexts) {
				t.Errorf("%d-of-%d share %d: %d codes for %d parts",
					threshold, nkeys, k, qi, len(qrTexts))
			}
			if _, err := toPlate(backup.EngraveText(engraverParams, txt), engraverParams, SquarePlate); err != nil {
				t.Errorf("%d-of-%d share %d: fit accepted but planning rejects: %v",
					threshold, nkeys, k, err)
			}
		}
	}
}

// splitParagraphTexts collects a plate's paragraph texts.
func splitParagraphTexts(txt backup.Text) []string {
	var out []string
	for _, p := range txt.Paragraphs {
		out = append(out, p.Text)
	}
	return out
}

// TestShareRecovery decodes the exact strings the share plates
// engrave — the artifact, not the scheme underneath: every
// quorum-sized subset of plates must reconstruct the descriptor in
// its canonical form through the BBQr join and Shamir set a
// recovering wallet runs, and every smaller subset must not.
func TestShareRecovery(t *testing.T) {
	for _, test := range []struct{ threshold, nkeys int }{{2, 3}, {2, 4}, {3, 5}} {
		desc := testMultisig(t, test.threshold, test.nkeys)
		want := splitDescriptor(desc).Encode()
		labels, plans, err := fitShares(engraverParams, desc, nil)
		if err != nil {
			t.Fatal(err)
		}
		// The QR ONLY variant's plate strings; TestFitShares pins that
		// the text-only variant engraves the same strings verbatim.
		sp := plans[slices.Index(labels, "QR ONLY")]
		plates := make([][]string, test.nkeys)
		for k := range test.nkeys {
			_, qrTexts, err := sp.plateContent(k)
			if err != nil {
				t.Fatal(err)
			}
			plates[k] = qrTexts
		}
		for c := uint64(1); c < 1<<test.nkeys; c++ {
			var s shamir.Set
			under := bits.OnesCount64(c) < test.threshold
			for rem := c; rem != 0; {
				k := bits.TrailingZeros64(rem)
				rem &^= 1 << k
				_, payload, err := bbqr.Join(plates[k])
				if err != nil {
					t.Fatal(err)
				}
				if err := s.Add(payload); err != nil {
					t.Fatal(err)
				}
			}
			if under {
				if s.Complete() {
					t.Errorf("%d-of-%d: under-quorum subset %b complete",
						test.threshold, test.nkeys, c)
				}
				continue
			}
			rec, err := s.Recover()
			if err != nil {
				t.Errorf("%d-of-%d: subset %b failed to recover: %v",
					test.threshold, test.nkeys, c, err)
				continue
			}
			if rec.FileType != bbqr.TypeCBOR {
				t.Errorf("%d-of-%d: subset %b recovered type %c, want %c",
					test.threshold, test.nkeys, c, rec.FileType, bbqr.TypeCBOR)
			}
			got, err := urtypes.Parse("crypto-output", rec.Data)
			if err != nil {
				t.Errorf("%d-of-%d: subset %b: %v", test.threshold, test.nkeys, c, err)
				continue
			}
			if got := got.(*bip380.Descriptor).Encode(); got != want {
				t.Errorf("%d-of-%d: subset %b recovered %q, want %q",
					test.threshold, test.nkeys, c, got, want)
			}
		}
	}
}

// The 2-of-2 of the canonical-input tests. By wire bytes canonKeyB
// (9A6A2580) sorts below canonKeyA (2A77E0A6), so plate 1 of any
// spelling is 9A6A2580, and canonReversed, which lists canonKeyA
// first, arrives in the reverse of canonical order.
const (
	canonKeyA     = "[2a77e0a6/48h/0h/0h/2h]xpub6F8WgTkiV8iDPFG1Kv4sNrcBNMMgKK4cjfxjdZWvR3kChfbt3L2dJF7xmCHBMGMmxjyzwgjdFkh9UN3623YpsmqN1KwZGR45Y3ANLQQX87u"
	canonKeyB     = "[9a6a2580/48h/0h/0h/2h]xpub6EeqK2JLwngrHJEQ4X4iqrySZV9qU3TgwMgf6NStLZa37AfNiHTtTE9ji1F9YQDLArJMLy8sw3Q2samVj5VQQjaaUHr5z2Hz57NWHJCfh31"
	canonReversed = "wsh(sortedmulti(2," + canonKeyA + "/<0;1>/*," + canonKeyB + "/<0;1>/*))"
)

// TestSplitInputCanonical: the split input is the wallet, not its
// spelling. One 2-of-2 written four ways (multipath children, plain
// receive children, origin-only keys, keys in the other order) must
// canonicalize to one CBOR; the scanned descriptors must come out
// untouched; the canonical CBOR must be a fixed point through the
// receiving parser; the title must not reach the bytes; and the share
// plates must number cosigners in canonical order whichever order the
// scan arrived in.
func TestSplitInputCanonical(t *testing.T) {
	forms := []struct{ name, text string }{
		{"multipath", canonReversed},
		{"plain", "wsh(sortedmulti(2," + canonKeyA + "/0/*," + canonKeyB + "/0/*))"},
		{"origin-only", "wsh(sortedmulti(2," + canonKeyA + "," + canonKeyB + "))"},
		{"swapped", "wsh(sortedmulti(2," + canonKeyB + "/<0;1>/*," + canonKeyA + "/<0;1>/*))"},
	}
	var want []byte
	var canon *bip380.Descriptor
	for _, f := range forms {
		desc, err := bip380.Parse(f.text)
		if err != nil {
			t.Fatalf("%s: %v", f.name, err)
		}
		before := desc.Encode()
		c := splitDescriptor(desc)
		got := urtypes.EncodeDescriptor(c)
		if after := desc.Encode(); after != before {
			t.Errorf("%s: splitDescriptor modified its input:\n%s\n%s", f.name, before, after)
		}
		if want == nil {
			want, canon = got, c
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: canonical CBOR differs from the multipath form's (%d vs %d bytes)", f.name, len(got), len(want))
		}
	}
	if bytes.Compare(urtypes.EncodeKey(canon.Keys[0]), urtypes.EncodeKey(canon.Keys[1])) >= 0 {
		t.Error("canonical keys are not in ascending wire order")
	}
	for k := range canon.Keys {
		if len(canon.Keys[k].Children) != 0 {
			t.Errorf("canonical key %d keeps %d children", k, len(canon.Keys[k].Children))
		}
	}

	// What the plates carry parses back to the canonical descriptor
	// and re-encodes to the same bytes, with or without another
	// canonicalization: recovery and re-split agree.
	obj, err := urtypes.Parse("crypto-output", want)
	if err != nil {
		t.Fatal(err)
	}
	rec := obj.(*bip380.Descriptor)
	if got := urtypes.EncodeDescriptor(splitDescriptor(rec)); !bytes.Equal(got, want) {
		t.Error("recovered descriptor canonicalizes to different bytes")
	}
	if got := urtypes.EncodeDescriptor(rec); !bytes.Equal(got, want) {
		t.Error("recovered descriptor re-encodes to different bytes")
	}
	if got := rec.Encode(); got != canon.Encode() {
		t.Errorf("recovered %q, want %q", got, canon.Encode())
	}

	// The title lives in the plate header only.
	titled := *canon
	titled.Title = "BENCH VAULT"
	if got := urtypes.EncodeDescriptor(splitDescriptor(&titled)); !bytes.Equal(got, want) {
		t.Error("the title changed the canonical CBOR")
	}

	// Two keys sharing an xpub under different origins are equal by
	// KeyData and distinct on the wire: the wire bytes order them, so
	// either input order canonicalizes to one CBOR.
	twin := canon.Keys[0]
	twin.MasterFingerprint ^= 1
	twin.DerivationPath = append(slices.Clone(canon.Keys[0].DerivationPath), 7)
	if !bytes.Equal(twin.KeyData, canon.Keys[0].KeyData) || bytes.Equal(urtypes.EncodeKey(twin), urtypes.EncodeKey(canon.Keys[0])) {
		t.Fatal("the twin key must share its original's KeyData and differ on the wire")
	}
	var twinCBOR [][]byte
	for _, keys := range [][]bip380.Key{{canon.Keys[0], twin}, {twin, canon.Keys[0]}} {
		d := *canon
		d.Keys = keys
		c := splitDescriptor(&d)
		if bytes.Compare(urtypes.EncodeKey(c.Keys[0]), urtypes.EncodeKey(c.Keys[1])) >= 0 {
			t.Error("twin keys are not in ascending wire order")
		}
		twinCBOR = append(twinCBOR, urtypes.EncodeDescriptor(c))
	}
	if !bytes.Equal(twinCBOR[0], twinCBOR[1]) {
		t.Error("the two input orders of the twin keys canonicalize to different CBOR")
	}

	// The plate headers of every form name the same cosigner at the
	// same plate number (the swapped form's plates are not swapped),
	// the bytes the split sealed are the canonical CBOR (the plates'
	// own quorum recovers them), and every form cuts the same plates:
	// the split is derived from the canonical CBOR, so the parts and
	// the tag are identical across the four spellings.
	var first *splitPlan
	for _, f := range forms {
		desc, err := bip380.Parse(f.text)
		if err != nil {
			t.Fatal(err)
		}
		labels, plans, err := fitShares(engraverParams, desc, nil)
		if err != nil {
			t.Fatalf("%s: %v", f.name, err)
		}
		sp := plans[slices.Index(labels, "QR ONLY")]
		if first == nil {
			first = sp
		} else {
			if sp.tag != first.tag {
				t.Errorf("%s: tag %04X, the %s form's is %04X", f.name, sp.tag, forms[0].name, first.tag)
			}
			for k := range canon.Keys {
				if !slices.Equal(sp.shares[k].Parts, first.shares[k].Parts) {
					t.Errorf("%s: plate %d parts differ from the %s form's", f.name, k+1, forms[0].name)
				}
			}
		}
		var set shamir.Set
		for k := range canon.Keys {
			txt, _, err := sp.plateContent(k)
			if err != nil {
				t.Fatal(err)
			}
			head, _, ok := strings.Cut(txt.Paragraphs[0].Text, " #")
			want := fmt.Sprintf("%d/%d ANY 2 %.8X", k+1, len(canon.Keys), canon.Keys[k].MasterFingerprint)
			if !ok || head != want {
				t.Errorf("%s plate %d: header %q, want %q", f.name, k+1, head, want)
			}
			_, payload, err := bbqr.Join(sp.shares[k].Parts)
			if err != nil {
				t.Fatal(err)
			}
			if err := set.Add(payload); err != nil {
				t.Fatal(err)
			}
		}
		rec, err := set.Recover()
		if err != nil {
			t.Fatalf("%s: %v", f.name, err)
		}
		if !bytes.Equal(rec.Data, want) || len(rec.Corrupt) != 0 {
			t.Errorf("%s: the split sealed %d bytes that differ from the canonical CBOR (corrupt %v)", f.name, len(rec.Data), rec.Corrupt)
		}
	}
}

// TestSplitReproducible: the split is derived from the canonical
// descriptor and the threshold, so two runs of the same wallet cut
// identical plates under the same tag, and the split draws no
// randomness at all: gui.Rand is an exhausted reader for the
// duration, which fails any read.
func TestSplitReproducible(t *testing.T) {
	oldRand := Rand
	Rand = bytes.NewReader(nil)
	defer func() { Rand = oldRand }()
	for _, kn := range [][2]int{{2, 2}, {2, 3}, {3, 5}} {
		desc := testMultisig(t, kn[0], kn[1])
		labels, first, err := fitShares(engraverParams, desc, nil)
		if err != nil {
			t.Fatalf("%d-of-%d: %v", kn[0], kn[1], err)
		}
		again, second, err := fitShares(engraverParams, desc, nil)
		if err != nil {
			t.Fatalf("%d-of-%d, second run: %v", kn[0], kn[1], err)
		}
		if !slices.Equal(labels, again) {
			t.Fatalf("%d-of-%d: variants %v, then %v", kn[0], kn[1], labels, again)
		}
		if len(first) == 0 {
			t.Fatalf("%d-of-%d: no variant offered", kn[0], kn[1])
		}
		for vi := range first {
			a, b := first[vi], second[vi]
			if a.tag != b.tag {
				t.Errorf("%d-of-%d %s: tag %04X, then %04X", kn[0], kn[1], labels[vi], a.tag, b.tag)
			}
			if a.fontSize != b.fontSize || a.scale != b.scale {
				t.Errorf("%d-of-%d %s: fit cell differs between runs", kn[0], kn[1], labels[vi])
			}
			for k := range kn[1] {
				if !slices.Equal(a.shares[k].Parts, b.shares[k].Parts) {
					t.Errorf("%d-of-%d %s: plate %d parts differ between runs", kn[0], kn[1], labels[vi], k+1)
				}
				aTxt, aQR, err := a.plateContent(k)
				if err != nil {
					t.Fatal(err)
				}
				bTxt, bQR, err := b.plateContent(k)
				if err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(aQR, bQR) || !slices.Equal(splitParagraphTexts(aTxt), splitParagraphTexts(bTxt)) {
					t.Errorf("%d-of-%d %s: plate %d composes differently between runs", kn[0], kn[1], labels[vi], k+1)
				}
			}
		}
	}
}

// TestSplitFlowCanonicalOrder: the plate gates name the cosigners in
// canonical key order, whichever order the scan listed them in. The
// 2-of-2 arrives with 2A77E0A6 first and opens on 9A6A2580, its lower
// key by wire bytes.
func TestSplitFlowCanonicalOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		desc, err := bip380.Parse(canonReversed)
		if err != nil {
			t.Fatal(err)
		}
		splitFlowHarness(t, desc, func(ctx *Context, await func(string) string, engrave func()) {
			await("Engrave Descriptor")
			click(&ctx.Router, Button3)
			await("SPLIT: 2 PLATES")
			click(&ctx.Router, Down)
			await("Any 2 of 2 plates recover")
			click(&ctx.Router, Button3)
			await("Choose engraving")
			click(&ctx.Router, Button3)
			order := []string{"9A6A2580", "2A77E0A6"}
			for plate, mfp := range order {
				gate := await(fmt.Sprintf("Plate %d of 2", plate+1))
				other := order[1-plate]
				if !uiContains(gate, "For cosigner "+mfp) || uiContains(gate, other) {
					t.Errorf("plate %d gate names the wrong cosigner (want %s, not %s): %q", plate+1, mfp, other, gate)
				}
				click(&ctx.Router, Down)
				await("SKIP")
				click(&ctx.Router, Button3)
			}
		})
	})
}

// awaitUI pumps frames until marker renders and returns that frame,
// failing with the last frame when it never does.
func awaitUI(t *testing.T, frame func() (string, bool), marker string) string {
	t.Helper()
	last := ""
	for range 10000 {
		content, ok := frame()
		if !ok {
			t.Fatalf("flow ended waiting for %q", marker)
		}
		if uiContains(content, marker) {
			return content
		}
		last = content
	}
	t.Fatalf("%q never appeared; last frame: %q", marker, last)
	return ""
}

// splitFlowHarness drives descriptorFlow far enough to exercise the
// per-plate loop with a mock engraver under a synctest clock.
func splitFlowHarness(t *testing.T, desc *bip380.Descriptor, script func(ctx *Context, await func(string) string, engrave func())) {
	t.Helper()
	e := newEngraver()
	p := newPlatform()
	p.engraver = e
	ctx := NewContext(p)
	completed := false
	frame, quit := runUI(ctx, func() {
		descriptorFlow(ctx, &descriptorTheme, desc)
		completed = true
	})
	defer quit()
	await := func(marker string) string {
		t.Helper()
		return awaitUI(t, frame, marker)
	}
	engrave := func() {
		t.Helper()
		// The engrave screen's idle strip is the dims/duration line
		// under the plate preview; "mm" marks it, since no choice or
		// progress screen in the flow renders dimensions.
		await("mm")
		press(&ctx.Router, Button3)
		frame()
		time.Sleep(confirmDelay)
		// The job's compute outlives any fixed frame budget: pump
		// frames until the mock engraver closes, as TestEngraveScreen
		// does, then wait for the done body.
	loop:
		for {
			frame()
			select {
			case <-e.closes:
				break loop
			case <-p.wakeups:
			}
		}
		await("Engraving completed")
		click(&ctx.Router, Button3)
	}
	script(ctx, await, engrave)
	for range 10 {
		if _, ok := frame(); !ok {
			break
		}
	}
	synctest.Wait()
	if !completed {
		t.Error("the split flow did not complete descriptorFlow")
	}
}

// TestSplitDescriptorFlow cuts a 2-of-2 as share plates: plate one
// twice (the another-copy prompt routes back through the insert
// gate), then skips plate two. One of two needed plates is cut, so
// the flow must end on the set-unfinished screen, which says that
// splitting again cuts the missing plate.
func TestSplitDescriptorFlow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		splitFlowHarness(t, testMultisig(t, 2, 2), func(ctx *Context, await func(string) string, engrave func()) {
			await("Engrave Descriptor")
			click(&ctx.Router, Button3)
			// Mode choice: the split row sits under ONE PLATE.
			await("SPLIT: 2 PLATES")
			click(&ctx.Router, Down)
			// Cursor and choice land in separate frames: Choose tests
			// its confirm button before it reads the cursor keys, so
			// every Down is followed by an await before the confirm.
			await("Any 2 of 2 plates recover")
			click(&ctx.Router, Button3)
			// The variant choice: TEXT + QR, the first row.
			await("Choose engraving")
			click(&ctx.Router, Button3)
			// Plate 1: engrave, then one extra copy through the gate.
			await("Plate 1 of 2")
			click(&ctx.Router, Button3)
			engrave()
			await("ANOTHER COPY")
			click(&ctx.Router, Down)
			await("NEXT PLATE")
			click(&ctx.Router, Button3)
			await("Plate 1 of 2")
			click(&ctx.Router, Button3)
			engrave()
			await("NEXT PLATE")
			click(&ctx.Router, Button3)
			// Plate 2: skip. One of two needed plates cut: the
			// set-unfinished screen gates the way out.
			await("Plate 2 of 2")
			click(&ctx.Router, Down)
			await("SKIP")
			click(&ctx.Router, Button3)
			await("Set unfinished")
			await("1 of 2 plates cut; recovery needs 2")
			click(&ctx.Router, Button3)
		})
	})
}

// TestSplitBackOutAfterCopy backs out at the insert gate that follows
// ANOTHER COPY: the plate already cut counts, so the unfinished-set
// screen reports one plate cut of the two the recovery needs.
func TestSplitBackOutAfterCopy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		splitFlowHarness(t, testMultisig(t, 2, 2), func(ctx *Context, await func(string) string, engrave func()) {
			await("Engrave Descriptor")
			click(&ctx.Router, Button3)
			await("SPLIT: 2 PLATES")
			click(&ctx.Router, Down)
			await("Any 2 of 2 plates recover")
			click(&ctx.Router, Button3)
			await("Choose engraving")
			click(&ctx.Router, Button3)
			await("Plate 1 of 2")
			click(&ctx.Router, Button3)
			engrave()
			await("ANOTHER COPY")
			click(&ctx.Router, Down)
			await("NEXT PLATE")
			click(&ctx.Router, Button3)
			// Back at the gate for the copy: leave.
			await("Plate 1 of 2")
			click(&ctx.Router, Button1)
			await("Set unfinished")
			await("1 of 2 plates cut; recovery needs 2")
			click(&ctx.Router, Button3)
			// The back-out unwinds to the descriptor screen; leave it
			// so the flow completes.
			await("Engrave Descriptor")
			click(&ctx.Router, Button1)
		})
	})
}

// TestCopiesDescriptorFlow drives the full-copy fallback: a 1-of-2
// has no partition scheme, so the split row offers one complete
// descriptor plate per cosigner.
func TestCopiesDescriptorFlow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		splitFlowHarness(t, testMultisig(t, 1, 2), func(ctx *Context, await func(string) string, engrave func()) {
			await("Engrave Descriptor")
			click(&ctx.Router, Button3)
			await("2 FULL COPIES")
			click(&ctx.Router, Down)
			await("Every plate is complete")
			click(&ctx.Router, Button3)
			// The fallback picks a fitDescriptor variant.
			await("Choose engraving")
			click(&ctx.Router, Button3)
			await("Full descriptor copy")
			click(&ctx.Router, Button3)
			engrave()
			await("NEXT PLATE")
			click(&ctx.Router, Button3)
			await("Plate 2 of 2")
			click(&ctx.Router, Button3)
			engrave()
			await("DONE")
			click(&ctx.Router, Button3)
		})
	})
}

// TestSplitPartialSetFlow cuts two plates of a 2-of-3 and skips the
// third: the set recovers as cut, and the flow still reports the
// plate left for a later run before finishing.
func TestSplitPartialSetFlow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		splitFlowHarness(t, testMultisig(t, 2, 3), func(ctx *Context, await func(string) string, engrave func()) {
			await("Engrave Descriptor")
			click(&ctx.Router, Button3)
			await("SPLIT: 3 PLATES")
			click(&ctx.Router, Down)
			await("Any 2 of 3 plates recover")
			click(&ctx.Router, Button3)
			await("Choose engraving")
			click(&ctx.Router, Button3)
			for plate := 1; plate <= 2; plate++ {
				await(fmt.Sprintf("Plate %d of 3", plate))
				click(&ctx.Router, Button3)
				engrave()
				await("NEXT PLATE")
				click(&ctx.Router, Button3)
			}
			await("Plate 3 of 3")
			click(&ctx.Router, Down)
			await("SKIP")
			click(&ctx.Router, Button3)
			await("Set unfinished")
			await("2 of 3 plates cut. Split this wallet again to cut the rest")
			click(&ctx.Router, Button3)
		})
	})
}

// TestShareHeaderOneLine: within a scale the ladder prefers the
// largest font whose pairing header stays on one line, so the tag
// never breaks across lines; an absurd title falls back to
// wrapping rather than failing.
func TestShareHeaderOneLine(t *testing.T) {
	for _, title := range []string{"", "BENCH VAULT 3OF7"} {
		desc := testMultisig(t, 5, 7)
		desc.Title = title
		labels, plans, err := fitShares(engraverParams, desc, nil)
		if err != nil {
			t.Fatal(err)
		}
		for vi, sp := range plans {
			if labels[vi] == "TEXT + QR" {
				// The wrap-around composition holds its header in the
				// column beside the code, which the full-width check
				// below does not model.
				continue
			}
			for k := range sp.desc.Keys {
				head := shareHeader(sp.desc, k, sp.tag)
				if cpl := backup.CharsPerLine(engraverParams, sh.Font, sp.fontSize); len(head) > cpl {
					t.Errorf("title %q variant %s share %d: header %d chars wraps at font %.1f (cpl %d)",
						title, labels[vi], k, len(head), sp.fontSize, cpl)
				}
			}
		}
		if !slices.Contains(labels, "TEXT ONLY") {
			t.Errorf("title %q: no TEXT ONLY variant for a 7-key wallet (labels %v)", title, labels)
		}
	}
	long := testMultisig(t, 5, 7)
	long.Title = strings.Repeat("VERY LONG TITLE ", 4)
	if _, _, err := fitShares(engraverParams, long, nil); err != nil {
		t.Fatalf("over-long title must wrap, not fail: %v", err)
	}
}

// TestShareCodeTitleInvariant: the title lives in the header text
// only; the code's QR version and dot scale must be byte-identical
// with and without it.
func TestShareCodeTitleInvariant(t *testing.T) {
	for _, kn := range [][2]int{{3, 5}, {5, 7}} {
		plain := testMultisig(t, kn[0], kn[1])
		titled := testMultisig(t, kn[0], kn[1])
		titled.Title = "BENCH VAULT WITH A NAME"
		pick := func(desc *bip380.Descriptor) *splitPlan {
			labels, plans, err := fitShares(engraverParams, desc, nil)
			if err != nil {
				t.Fatal(err)
			}
			i := slices.Index(labels, "QR ONLY")
			if i < 0 {
				t.Fatalf("no QR ONLY variant (labels %v)", labels)
			}
			return plans[i]
		}
		p, q := pick(plain), pick(titled)
		ps, err := qr.MinSize(p.shares[0].Parts[0], qr.L)
		if err != nil {
			t.Fatal(err)
		}
		qs, err := qr.MinSize(q.shares[0].Parts[0], qr.L)
		if err != nil {
			t.Fatal(err)
		}
		if p.scale != q.scale || ps != qs {
			t.Errorf("%d-of-%d: title changed the code: %d modules at scale %d vs %d at %d",
				kn[0], kn[1], ps, p.scale, qs, q.scale)
		}
	}
}
