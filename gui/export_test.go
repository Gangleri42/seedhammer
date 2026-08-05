package gui

import (
	"image"
	"testing"

	qr "github.com/seedhammer/kortschak-qr"
	"seedhammer.com/bc/ur"
	"seedhammer.com/bc/urtypes"
	"seedhammer.com/bip380"
)

// TestExportPartsRoundTrip decodes the exact strings the screen
// animates — the artifact, not the splitter underneath: the parts
// must reassemble into the descriptor through the same UR decoder
// wallets run, at the real display budget and at a small one that
// forces the animation.
func TestExportPartsRoundTrip(t *testing.T) {
	desc := goldenDescriptor(t)
	data := urtypes.EncodeDescriptor(desc)
	budgets := []struct {
		name       string
		maxModules int
		multi      bool
	}{
		{"display", exportMaxModules(image.Pt(480, 320)), false},
		{"tiny", 33, true},
	}
	for _, budget := range budgets {
		parts, err := exportParts(data, budget.maxModules)
		if err != nil {
			t.Fatalf("%s: %v", budget.name, err)
		}
		if budget.multi && len(parts) < 2 {
			t.Errorf("%s: %d parts, want an animation", budget.name, len(parts))
		}
		d := new(ur.Decoder)
		for _, p := range parts {
			if err := d.Add(p); err != nil {
				t.Fatalf("%s: %v", budget.name, err)
			}
		}
		typ, enc, err := d.Result()
		if err != nil || enc == nil {
			t.Fatalf("%s: decode failed: %v", budget.name, err)
		}
		got, err := urtypes.Parse(typ, enc)
		if err != nil {
			t.Fatalf("%s: %v", budget.name, err)
		}
		if got, want := got.(*bip380.Descriptor).Encode(), desc.Encode(); got != want {
			t.Errorf("%s: recovered %q, want %q", budget.name, got, want)
		}
	}
}

// TestExportQRIsScannable pins every animated frame to the same
// module-size floor as the single-sig descriptor screen, on the real
// display, for the widest quorum the fixtures carry.
func TestExportQRIsScannable(t *testing.T) {
	const (
		lcdWidth  = 480
		lcdHeight = 320
		quiet     = exportQuiet
		minScale  = exportMinScale
	)
	dims := image.Pt(lcdWidth, lcdHeight)
	for _, test := range []struct {
		name string
		desc *bip380.Descriptor
	}{
		{"2-of-3", goldenDescriptor(t)},
		{"3-of-5", testMultisig(t, 3, 5)},
		{"5-of-7", testMultisig(t, 5, 7)},
	} {
		data := urtypes.EncodeDescriptor(test.desc)
		parts, err := exportParts(data, exportMaxModules(dims))
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		for i, p := range parts {
			// Through the same encoder and level the screen uses.
			code, err := qr.Encode(p, qr.L)
			if err != nil {
				t.Fatalf("%s part %d: %v", test.name, i+1, err)
			}
			height := lcdHeight - leadingSize - 8
			scale := qrScale(height, code.Size, quiet)
			px := (code.Size + 2*quiet) * scale
			t.Logf("%s part %d/%d: %d chars, %d modules, %d px/module, %d px square",
				test.name, i+1, len(parts), len(p), code.Size, scale, px)
			if scale < minScale {
				t.Errorf("%s part %d: %d px per module, want at least %d", test.name, i+1, scale, minScale)
			}
			if px > height {
				t.Errorf("%s part %d: %d px square exceeds the %d px area", test.name, i+1, px, height)
			}
		}
	}
}

func TestGroupAddress(t *testing.T) {
	if got, want := groupAddress("abcdefghij", 4), "abcd efgh ij"; got != want {
		t.Errorf("groupAddress = %q, want %q", got, want)
	}
}

// TestChildrenDescriptorsStillFit: keys now spell out <0;1>/*, which
// lengthens every descriptor a fixture or the builder produces. The
// plate ladders must keep offering variants at the quorums the
// emulator demos, and the split partition must keep fitting where a
// scheme exists.
func TestChildrenDescriptorsStillFit(t *testing.T) {
	children := []bip380.Derivation{
		{Type: bip380.RangeDerivation, Index: 0, End: 1},
		{Type: bip380.WildcardDerivation},
	}
	for _, test := range []struct{ m, n int }{{2, 3}, {3, 5}, {5, 7}} {
		desc := testMultisig(t, test.m, test.n)
		for i := range desc.Keys {
			desc.Keys[i].Children = children
		}
		labels, _, _, err := fitDescriptor(engraverParams, SquarePlate, desc, nil)
		if err != nil {
			t.Fatalf("%d-of-%d: %v", test.m, test.n, err)
		}
		if len(labels) == 0 {
			t.Errorf("%d-of-%d: no single-plate variant fits", test.m, test.n)
		}
		t.Logf("%d-of-%d: %v", test.m, test.n, labels)
		if ur.HasScheme(test.m, test.n) {
			if _, _, _, err := fitShares(engraverParams, desc, nil); err != nil {
				t.Errorf("%d-of-%d shares: %v", test.m, test.n, err)
			}
		}
	}
}
