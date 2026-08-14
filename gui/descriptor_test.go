package gui

import (
	"bytes"
	"image"
	"slices"
	"strings"
	"testing"

	qr "github.com/seedhammer/kortschak-qr"

	"seedhammer.com/address"
	"seedhammer.com/bip380"
	"seedhammer.com/bip39"
)

// The BIP39/84/86 shared test mnemonic.
const vectorMnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"

// Vectors are quoted from the BIPs themselves, not from memory: the
// account xpub and index 0 can match while later indices do not.
//
// TestSeedDescriptorVectors checks the derived descriptor against the
// addresses published in BIP84 and BIP86 for their shared test mnemonic.
// Deriving the wrong key would still produce a well-formed descriptor
// and a scannable code, so nothing short of a known address proves it.
func TestSeedDescriptorVectors(t *testing.T) {
	const vector = vectorMnemonic
	m, err := bip39.ParseMnemonic(vector)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		script  bip380.Script
		path    string
		receive [2]string
		change  string
	}{
		{
			// BIP84.
			script: bip380.P2WPKH,
			path:   "m/84h/0h/0h",
			receive: [2]string{
				"bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu",
				"bc1qnjg0jd8228aq7egyzacy8cys3knf9xvrerkf9g",
			},
			change: "bc1q8c6fshw2dlwun7ekn9qwf37cu2rn755upcp6el",
		},
		{
			// BIP86.
			script: bip380.P2TR,
			path:   "m/86h/0h/0h",
			receive: [2]string{
				"bc1p5cyxnuxmeuwuvkwfem96lqzszd02n6xdcjrs20cac6yqjjwudpxqkedrcr",
				"bc1p4qhjn9zdvkux4e44uhx8tc55attvtyu358kutcqkudyccelu0was9fqzwh",
			},
			change: "bc1p3qkhfews2uk44qtvauqyr2ttdsw7svhkl9nkm9s9c3x4ax5h60wqwruhk7",
		},
	} {
		t.Run(tc.script.String(), func(t *testing.T) {
			desc, err := seedDescriptor(m, "", tc.script, tc.script.DerivationPath())
			if err != nil {
				t.Fatal(err)
			}
			if got := desc.Keys[0].DerivationPath.String(); got != tc.path {
				t.Errorf("derivation path %q, want %q", got, tc.path)
			}
			for i, want := range tc.receive {
				got, err := address.Receive(desc, uint32(i))
				if err != nil {
					t.Fatalf("receive %d: %v", i, err)
				}
				if got != want {
					t.Errorf("receive address %d:\n got %s\nwant %s", i, got, want)
				}
			}
			got, err := address.Change(desc, 0)
			if err != nil {
				t.Fatalf("change 0: %v", err)
			}
			if got != tc.change {
				t.Errorf("change address 0:\n got %s\nwant %s", got, tc.change)
			}
			// The descriptor is what a wallet imports, so assert the
			// exported string, not just the key it was built from.
			// Addresses alone cannot catch an export gap: address
			// derivation supplies <0;1>/* internally when Children is
			// empty, so they pass while the descriptor names one key.
			enc := desc.Encode()
			if !strings.Contains(enc, "/<0;1>/*") {
				t.Errorf("descriptor has no receive/change range:\n%s", enc)
			}
			back, err := bip380.Parse(enc)
			if err != nil {
				t.Fatalf("descriptor does not parse back: %v\n%s", err, enc)
			}
			if len(back.Keys) != 1 {
				t.Fatalf("round trip yielded %d keys", len(back.Keys))
			}
			gotKey, wantKey := back.Keys[0], desc.Keys[0]
			if gotKey.MasterFingerprint != wantKey.MasterFingerprint {
				t.Errorf("fingerprint round trip: got %08x want %08x",
					gotKey.MasterFingerprint, wantKey.MasterFingerprint)
			}
			if gotKey.ParentFingerprint != wantKey.ParentFingerprint {
				t.Errorf("parent fingerprint round trip: got %08x want %08x",
					gotKey.ParentFingerprint, wantKey.ParentFingerprint)
			}
			if !slices.Equal(gotKey.DerivationPath, wantKey.DerivationPath) {
				t.Errorf("path round trip: got %v want %v", gotKey.DerivationPath, wantKey.DerivationPath)
			}
			if !bytes.Equal(gotKey.KeyData, wantKey.KeyData) || !bytes.Equal(gotKey.ChainCode, wantKey.ChainCode) {
				t.Error("key material did not survive the round trip")
			}
			t.Logf("%s", enc)
		})
	}
}

// TestDescriptorQRIsScannable pins the code big enough to read off the
// screen at the real display size. The point of showing a QR at all is
// that nobody transcribes an xpub by hand, so a layout change that
// shrinks it below a usable module size is a regression, not a detail.
func TestDescriptorQRIsScannable(t *testing.T) {
	const (
		lcdWidth  = 480
		lcdHeight = 320
		// Two modules sampled plus two painted: the scale is chosen
		// against the whole quiet zone, or the paper overruns the
		// screen and the text beside it.
		quiet = 4
		// Four pixels per module is the floor for a phone camera at
		// arm's length. Nested segwit is the longest descriptor and
		// sits exactly on it; anything that lengthens the payload
		// further needs the layout revisited, not this number lowered.
		minScale = 4
	)
	m, err := bip39.ParseMnemonic(vectorMnemonic)
	if err != nil {
		t.Fatal(err)
	}
	for _, script := range seedScripts {
		desc, err := seedDescriptor(m, "", script, script.DerivationPath())
		if err != nil {
			t.Fatal(err)
		}
		enc := desc.Encode()
		// Through the same encoder the screen uses: re-encoding here
		// would pass while the screen shipped a different payload or
		// error-correction level.
		code, err := descriptorCode(desc)
		if err != nil {
			t.Fatalf("%s: %v", script, err)
		}
		// The area descriptorQRScreen lays the code into.
		height := lcdHeight - leadingSize - 8
		scale := qrScale(height, code.Size, quiet)
		px := (code.Size + 2*quiet) * scale
		t.Logf("%s: %d chars, %d modules, %d px/module, %d px square",
			script, len(enc), code.Size, scale, px)
		if scale < minScale {
			t.Errorf("%s: %d px per module, want at least %d", script, scale, minScale)
		}
		if px > height {
			t.Errorf("%s: code is %d px tall, taller than the %d px available", script, px, height)
		}
	}
}

// TestPathPrefillRoundTrips checks the editor starts from a path that
// parses back to the one it was given. Path.Encode writes hardening as
// "h" and the keyboard can only produce an apostrophe, so the prefill is
// rewritten between alphabets and a mistake there would silently offer
// an unusable starting point.
func TestPathPrefillRoundTrips(t *testing.T) {
	for _, script := range seedScripts {
		std := script.DerivationPath()
		frag := strings.ReplaceAll(strings.TrimPrefix(std.Encode(), "/"), "h", "'")
		got, ok := parsePathFragment(frag)
		if !ok {
			t.Errorf("%s: prefill %q does not parse", script, frag)
			continue
		}
		if !slices.Equal(got, std) {
			t.Errorf("%s: prefill %q parsed to %v, want %v", script, frag, got, std)
		}
	}
}

func TestParsePathFragment(t *testing.T) {
	for _, tc := range []struct {
		frag string
		ok   bool
	}{
		{"84'/0'/0'", true},
		{"84'/0'/1'", true},   // a second account
		{"84'/0'/0'/0", true}, // an explicit branch
		{"0", true},
		{"84h/0h/0h", true}, // lowercase h is what bip32 reads natively
		{"", false},         // m/ alone is not a path
		{"84'/", false},     // trailing separator
		{"84'//0'", false},  // empty element
		{"abc", false},
		{"84'/0'/x", false},
		{"2147483648", false}, // 2^31, past the hardening offset
		{"-1", false},
	} {
		got, ok := parsePathFragment(tc.frag)
		if ok != tc.ok {
			t.Errorf("parsePathFragment(%q) = %v, %v; want ok=%v", tc.frag, got, ok, tc.ok)
		}
	}
}

// The frame loop must not allocate: op.Mask boxes its argument into the
// op buffer, and boxing a value larger than a pointer allocates under
// TinyGo. TestAllocs only drives StartScreen and DescriptorScreen, so
// every new screen needs its own guard.
func BenchmarkDescriptorFrame(b *testing.B) {
	ctx := NewContext(newPlatform())
	m, _ := bip39.ParseMnemonic(vectorMnemonic)
	d, _ := seedDescriptor(m, "", seedScripts[0], seedScripts[0].DerivationPath())
	code, _ := qr.Encode(d.Encode(), qr.L)
	dims := image.Pt(480, 320)
	img := new(qrImage)
	pathStr := d.Keys[0].DerivationPath.String()
	b.ReportAllocs()
	for b.Loop() {
		descriptorQRScreen(ctx, &descriptorTheme, d, code, dims, img, pathStr)
		ctx.B.Reset()
	}
}

func TestDescriptorFrameAllocs(t *testing.T) {
	if a := testing.Benchmark(BenchmarkDescriptorFrame).AllocsPerOp(); a > 0 {
		t.Errorf("descriptor frame allocates %d objects per frame, want 0", a)
	}
}

// TestScriptLabelsAreDistinct pins the menu. Three of the four address
// types once rendered as "SEGWIT", legacy included, and the choice
// screen shows nothing but these strings.
func TestScriptLabelsAreDistinct(t *testing.T) {
	seen := map[string]bip380.Script{}
	for _, s := range seedScripts {
		l := scriptChoiceLabel(s)
		if prev, dup := seen[l]; dup {
			t.Errorf("%s and %s both render as %q", prev, s, l)
		}
		seen[l] = s
	}
	if len(seen) != len(seedScripts) {
		t.Errorf("%d labels for %d address types", len(seen), len(seedScripts))
	}
}

// TestDescriptorSkipIsAChoice pins that declining the descriptor is a
// visible entry, as it is for the other two plates, not a back button
// the user has to guess at.
func TestDescriptorSkipIsAChoice(t *testing.T) {
	ctx := NewContext(newPlatform())
	m, err := bip39.ParseMnemonic(vectorMnemonic)
	if err != nil {
		t.Fatal(err)
	}
	// SKIP is the last entry: step down past every script and ADVANCED,
	// then choose. It must return without a path and without engraving.
	for range len(seedScripts) + 1 {
		click(&ctx.Router, Down)
	}
	click(&ctx.Router, Button3)
	if path := walletDescriptorFlow(ctx, &descriptorTheme, m, "", ""); path != "" {
		t.Errorf("choosing SKIP reported a path: %q", path)
	}
}

// A descriptor scanned without its checksum carries a notice on the
// confirm screen: nothing verified the transcription, and the screen
// points the eye at the coordinator for the comparison. A checksummed
// scan stays notice-free.
func TestDescriptorChecksumNotice(t *testing.T) {
	for _, tc := range []struct {
		name       string
		noChecksum bool
	}{
		{"without checksum", true},
		{"with checksum", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			desc := &bip380.Descriptor{
				Script:     bip380.P2WSH,
				Type:       bip380.SortedMulti,
				Threshold:  2,
				Keys:       make([]bip380.Key, 3),
				NoChecksum: tc.noChecksum,
			}
			fillDescriptor(t, desc, desc.Script.DerivationPath(), 12, 0)
			ctx := NewContext(newPlatform())
			ds := &DescriptorScreen{Descriptor: desc}
			frame, quit := runUI(ctx, func() {
				ds.Confirm(ctx, &descriptorTheme)
			})
			defer quit()
			content, ok := frame()
			if !ok {
				t.Fatal("confirm screen exited immediately")
			}
			if got := uiContains(content, "No checksum"); got != tc.noChecksum {
				t.Errorf("notice shown = %v, want %v; frame: %q", got, tc.noChecksum, content)
			}
		})
	}
}
