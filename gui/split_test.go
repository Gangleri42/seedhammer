package gui

import (
	"fmt"
	"math/bits"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	qr "github.com/seedhammer/kortschak-qr"
	"seedhammer.com/backup"
	"seedhammer.com/bc/ur"
	"seedhammer.com/bc/urtypes"
	"seedhammer.com/bip380"
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
		urs              int // fragment URs per plate
	}{
		{2, 3, 1},
		{3, 4, 1},
		{2, 4, 2},
		{3, 5, 2},
	}
	for _, test := range tests {
		desc := testMultisig(t, test.threshold, test.nkeys)
		data, size, scale, err := fitShares(engraverParams, desc, nil)
		if err != nil {
			t.Fatalf("%d-of-%d: %v", test.threshold, test.nkeys, err)
		}
		for k := range test.nkeys {
			txt, qrTexts, err := shareText(desc, data, k, size, scale)
			if err != nil {
				t.Fatal(err)
			}
			if len(qrTexts) != test.urs {
				t.Errorf("%d-of-%d share %d: %d URs, want %d",
					test.threshold, test.nkeys, k, len(qrTexts), test.urs)
			}
			// The pairing header opens the first paragraph.
			head := fmt.Sprintf("%d/%d %.8X\n", k+1, test.nkeys, desc.Keys[k].MasterFingerprint)
			if !strings.HasPrefix(txt.Paragraphs[0].Text, head) {
				t.Errorf("%d-of-%d share %d: header %q missing from %.40q",
					test.threshold, test.nkeys, k, head, txt.Paragraphs[0].Text)
			}
			// The fit verdict must agree with the planner: the plate
			// must plan inside the margins with the real codes swapped
			// in for the fit's stand-ins, the same substitution
			// planDescriptorPlate makes per paragraph.
			for i := range txt.Paragraphs {
				p := &txt.Paragraphs[i]
				qrc, err := qr.Encode(qrTexts[i], qr.L)
				if err != nil {
					t.Fatal(err)
				}
				if qrc.Size != p.QR.Size {
					t.Errorf("%d-of-%d share %d UR %d: stand-in code size %d, real code size %d",
						test.threshold, test.nkeys, k, i, p.QR.Size, qrc.Size)
				}
				p.QR = qrc
			}
			if _, err := toPlate(backup.EngraveText(engraverParams, txt), engraverParams); err != nil {
				t.Errorf("%d-of-%d share %d: fit accepted but planning rejects: %v",
					test.threshold, test.nkeys, k, err)
			}
		}
	}
}

// TestShareRecovery decodes the exact strings the share plates
// engrave — the artifact, not the scheme underneath: every
// quorum-sized subset of plates must reconstruct the descriptor
// through the same UR decoder wallets run, and every smaller subset
// must not.
func TestShareRecovery(t *testing.T) {
	for _, test := range []struct{ threshold, nkeys int }{{2, 3}, {2, 4}, {3, 5}} {
		desc := testMultisig(t, test.threshold, test.nkeys)
		data, size, scale, err := fitShares(engraverParams, desc, nil)
		if err != nil {
			t.Fatal(err)
		}
		plates := make([][]string, test.nkeys)
		for k := range test.nkeys {
			_, qrTexts, err := shareText(desc, data, k, size, scale)
			if err != nil {
				t.Fatal(err)
			}
			plates[k] = qrTexts
		}
		for c := uint64(1); c < 1<<test.nkeys; c++ {
			d := new(ur.Decoder)
			for rem := c; rem != 0; {
				k := bits.TrailingZeros64(rem)
				rem &^= 1 << k
				for _, u := range plates[k] {
					if err := d.Add(u); err != nil {
						t.Fatal(err)
					}
				}
			}
			typ, enc, err := d.Result()
			if bits.OnesCount64(c) < test.threshold {
				if enc != nil {
					t.Errorf("%d-of-%d: under-quorum subset %b recovered the descriptor",
						test.threshold, test.nkeys, c)
				}
				continue
			}
			if err != nil || enc == nil {
				t.Errorf("%d-of-%d: subset %b failed to recover: %v",
					test.threshold, test.nkeys, c, err)
				continue
			}
			got, err := urtypes.Parse(typ, enc)
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
	engrave := func() {
		t.Helper()
		await("close the lock")
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
		await("completed successfully")
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
// gate), then the set completes by skipping plate two — the resume
// path for a set whose remaining plates were cut in an earlier run.
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
			// Plate 2: skip; a fully skipped tail still completes the set.
			await("Plate 2 of 2")
			click(&ctx.Router, Down)
			await("SKIP")
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
