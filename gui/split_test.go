package gui

import (
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
	{
		for k := range nkeys {
			txt, qrTexts, err := sp.plateContent(desc, k)
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
			head := fmt.Sprintf("%d/%d ANY %d %.8X", k+1, nkeys, threshold, desc.Keys[k].MasterFingerprint)
			if !strings.HasPrefix(txt.Paragraphs[0].Text, head) {
				t.Errorf("%d-of-%d share %d: header %q missing from %.40q",
					threshold, nkeys, k, head, txt.Paragraphs[0].Text)
			}
			// The session tag marks the plate's split session.
			if !strings.Contains(txt.Paragraphs[0].Text, fmt.Sprintf("#%04X", sp.tag)) {
				t.Errorf("%d-of-%d share %d: session tag missing from %.40q",
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
// quorum-sized subset of plates must reconstruct the descriptor
// through the BBQr join and Shamir set a recovering wallet runs, and
// every smaller subset must not.
func TestShareRecovery(t *testing.T) {
	for _, test := range []struct{ threshold, nkeys int }{{2, 3}, {2, 4}, {3, 5}} {
		desc := testMultisig(t, test.threshold, test.nkeys)
		labels, plans, err := fitShares(engraverParams, desc, nil)
		if err != nil {
			t.Fatal(err)
		}
		// The QR ONLY variant's plate strings; TestFitShares pins that
		// the text-only variant engraves the same strings verbatim.
		sp := plans[slices.Index(labels, "QR ONLY")]
		plates := make([][]string, test.nkeys)
		for k := range test.nkeys {
			_, qrTexts, err := sp.plateContent(desc, k)
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
			if got, want := got.(*bip380.Descriptor).Encode(), desc.Encode(); got != want {
				t.Errorf("%d-of-%d: subset %b recovered %q, want %q",
					test.threshold, test.nkeys, c, got, want)
			}
		}
	}
}

// awaitUI pumps frames until marker renders, failing with the last
// frame when it never does.
func awaitUI(t *testing.T, frame func() (string, bool), marker string) {
	t.Helper()
	last := ""
	for range 10000 {
		content, ok := frame()
		if !ok {
			t.Fatalf("flow ended waiting for %q", marker)
		}
		if uiContains(content, marker) {
			return
		}
		last = content
	}
	t.Fatalf("%q never appeared; last frame: %q", marker, last)
}

// splitFlowHarness drives descriptorFlow far enough to exercise the
// per-plate loop with a mock engraver under a synctest clock.
func splitFlowHarness(t *testing.T, desc *bip380.Descriptor, script func(ctx *Context, await func(string), engrave func())) {
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
	await := func(marker string) {
		t.Helper()
		awaitUI(t, frame, marker)
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
// gate), then skips plate two. Shares of one session never combine
// with another's, so one cut plate of a 2-of-2 is unrecoverable
// steel: the flow must end on the incomplete-set warning.
func TestSplitDescriptorFlow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		splitFlowHarness(t, testMultisig(t, 2, 2), func(ctx *Context, await func(string), engrave func()) {
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
			// Plate 2: skip. One of two needed plates cut: the warning
			// gates the way out.
			await("Plate 2 of 2")
			click(&ctx.Router, Down)
			await("SKIP")
			click(&ctx.Router, Button3)
			await("Set incomplete")
			click(&ctx.Router, Button3)
		})
	})
}

// TestCopiesDescriptorFlow drives the full-copy fallback: a 1-of-2
// has no partition scheme, so the split row offers one complete
// descriptor plate per cosigner.
func TestCopiesDescriptorFlow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		splitFlowHarness(t, testMultisig(t, 1, 2), func(ctx *Context, await func(string), engrave func()) {
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
// third: the set recovers, but its missing plate can never come from
// another session, and the flow must say so before finishing.
func TestSplitPartialSetFlow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		splitFlowHarness(t, testMultisig(t, 2, 3), func(ctx *Context, await func(string), engrave func()) {
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
			await("Partial set")
			click(&ctx.Router, Button3)
		})
	})
}

// TestShareHeaderOneLine: within a scale the ladder prefers the
// largest font whose pairing header stays on one line, so the session
// tag never breaks across lines; an absurd title falls back to
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
			for k := range desc.Keys {
				head := shareHeader(desc, k, sp.tag)
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
