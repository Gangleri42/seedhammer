package gui

import (
	"testing"
	"testing/synctest"
	"time"

	"seedhammer.com/address"
	"seedhammer.com/bip380"
	"seedhammer.com/bip39"
)

// The emulator fixture family: repeated-word seeds whose checksum
// word the device's own last-word arithmetic picks. The literals
// below were generated once from these seeds and pin the whole
// assembly chain — derivation path, children, key order, checksum —
// plus the first address the verify screen shows. Sparrow accepting
// the animated export of this exact descriptor is the bench check.
const (
	goldenSeedBacon = "bacon bacon bacon bacon bacon bacon bacon bacon bacon bacon bacon bacon bacon bacon bacon bacon bacon bacon bacon bacon bacon bacon bacon bacon"
	goldenSeedOil   = "oil oil oil oil oil oil oil oil oil oil oil oil"
	goldenSeedRug   = "rug rug rug rug rug rug rug rug rug rug rug sad"

	golden2of3 = "wsh(sortedmulti(2," +
		"[9a6a2580/48h/0h/0h/2h]xpub6EeqK2JLwngrHJEQ4X4iqrySZV9qU3TgwMgf6NStLZa37AfNiHTtTE9ji1F9YQDLArJMLy8sw3Q2samVj5VQQjaaUHr5z2Hz57NWHJCfh31/<0;1>/*," +
		"[2a77e0a6/48h/0h/0h/2h]xpub6F8WgTkiV8iDPFG1Kv4sNrcBNMMgKK4cjfxjdZWvR3kChfbt3L2dJF7xmCHBMGMmxjyzwgjdFkh9UN3623YpsmqN1KwZGR45Y3ANLQQX87u/<0;1>/*," +
		"[c20d0c81/48h/0h/0h/2h]xpub6Dyvg74MADonsv1hPvMFKNtHPyvuSZ3mc8c7A6CLhD21ef6qSfbqgqWHFfjtV8H7Vz9YSKdeXq6n2NkvE5GapUmZJnsvn5p1pfQwV6aTmXd/<0;1>/*" +
		"))#6ctaeks9"
	golden2of3Receive0 = "bc1qzsqvlehla8tp3ntt4qn4ckw0enfqme38kkw0nfft2dhflpan27hsgkx7qz"
)

func goldenMnemonic(t testing.TB, words string) bip39.Mnemonic {
	t.Helper()
	m, err := bip39.ParseMnemonic(words)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Valid() {
		t.Fatalf("golden seed %q does not validate", words)
	}
	return m
}

// goldenDescriptor assembles the fixture-family 2-of-3 the way the
// builder does, one cosignerKey per seed.
func goldenDescriptor(t testing.TB) *bip380.Descriptor {
	t.Helper()
	desc := &bip380.Descriptor{
		Title:     "Vault",
		Script:    bip380.P2WSH,
		Type:      bip380.SortedMulti,
		Threshold: 2,
	}
	for _, words := range []string{goldenSeedBacon, goldenSeedOil, goldenSeedRug} {
		key, err := cosignerKey(goldenMnemonic(t, words), "")
		if err != nil {
			t.Fatal(err)
		}
		desc.Keys = append(desc.Keys, key)
	}
	return desc
}

func TestMultisigDescriptorGolden(t *testing.T) {
	desc := goldenDescriptor(t)
	enc := desc.Encode()
	if enc != golden2of3 {
		t.Errorf("assembled descriptor drifted:\n got %s\nwant %s", enc, golden2of3)
	}
	// The artifact must parse back: the exported string is what a
	// wallet receives, and what a rescan feeds the split flow.
	back, err := bip380.Parse(enc)
	if err != nil {
		t.Fatal(err)
	}
	if got := back.Encode(); got != enc {
		t.Errorf("round trip drifted:\n got %s\nwant %s", got, enc)
	}
	addr, err := address.Receive(desc, 0)
	if err != nil {
		t.Fatal(err)
	}
	if addr != golden2of3Receive0 {
		t.Errorf("first address drifted:\n got %s\nwant %s", addr, golden2of3Receive0)
	}
}

// A passphrase moves the cosigner to a different key: the fingerprint
// and xpub must change, the path must not.
func TestCosignerKeyPassphrase(t *testing.T) {
	m := goldenMnemonic(t, goldenSeedOil)
	plain, err := cosignerKey(m, "")
	if err != nil {
		t.Fatal(err)
	}
	passed, err := cosignerKey(m, "hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if plain.MasterFingerprint == passed.MasterFingerprint {
		t.Error("passphrase left the master fingerprint unchanged")
	}
	if plain.String() == passed.String() {
		t.Error("passphrase left the account key unchanged")
	}
	if plain.DerivationPath.String() != passed.DerivationPath.String() {
		t.Error("passphrase changed the derivation path")
	}
}

func TestCosignerFromPayload(t *testing.T) {
	golden, err := cosignerKey(goldenMnemonic(t, goldenSeedOil), "")
	if err != nil {
		t.Fatal(err)
	}
	// The expression a cosigner device exports: origin, xpub, branches.
	expr := "[2a77e0a6/48h/0h/0h/2h]" + golden.String() + "/<0;1>/*"
	key, ok := cosignerFromPayload(plainText(expr))
	if !ok {
		t.Fatalf("key expression rejected: %s", expr)
	}
	if key.MasterFingerprint != golden.MasterFingerprint {
		t.Errorf("fingerprint %.8x, want %.8x", key.MasterFingerprint, golden.MasterFingerprint)
	}
	if key.DerivationPath.String() != golden.DerivationPath.String() {
		t.Errorf("path %s, want %s", key.DerivationPath, golden.DerivationPath)
	}
	if key.String() != golden.String() {
		t.Errorf("xpub %s, want %s", key.String(), golden.String())
	}
	if len(key.Children) != 2 {
		t.Errorf("children %v, want <0;1>/*", key.Children)
	}
	if _, ok := cosignerFromPayload(plainText("hello plate")); ok {
		t.Error("free text accepted as a cosigner key")
	}
	// A single-sig descriptor export wraps the same key.
	desc := &bip380.Descriptor{
		Type:      bip380.Singlesig,
		Threshold: 1,
		Script:    bip380.P2WPKH,
		Keys:      []bip380.Key{golden},
	}
	if _, ok := cosignerFromPayload(desc); !ok {
		t.Error("single-sig descriptor rejected as a cosigner key")
	}
	multi := &bip380.Descriptor{Type: bip380.SortedMulti, Threshold: 2, Keys: []bip380.Key{golden, golden}}
	if _, ok := cosignerFromPayload(multi); ok {
		t.Error("a multisig descriptor accepted as a single cosigner key")
	}
}

func TestSlotWithFingerprint(t *testing.T) {
	key, err := cosignerKey(goldenMnemonic(t, goldenSeedOil), "")
	if err != nil {
		t.Fatal(err)
	}
	slots := []cosignerSlot{{key: key}}
	if got := slotWithFingerprint(slots, key.MasterFingerprint); got != 0 {
		t.Errorf("duplicate fingerprint not found: %d", got)
	}
	if got := slotWithFingerprint(slots, key.MasterFingerprint+1); got != -1 {
		t.Errorf("fresh fingerprint reported duplicate: %d", got)
	}
}

// TestMultisigWalletFlow walks the whole builder: title, quorum, two
// seeds tapped over NFC, review held to confirm, both seed plates
// skipped, and the descriptor screen reached — the point where the
// scanned-descriptor machinery takes over.
func TestMultisigWalletFlow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPlatform()
		nfc := newTestNFC()
		p.nfc = nfc
		ctx := NewContext(p)
		done := false
		frame, quit := runUI(ctx, func() {
			done = multisigWalletFlow(ctx, &descriptorTheme)
		})
		defer quit()
		await := func(marker string) {
			t.Helper()
			awaitUI(t, frame, marker)
		}
		pump := func() {
			for range 3 {
				frame()
			}
		}
		hold := func() {
			t.Helper()
			press(&ctx.Router, Button3)
			frame()
			time.Sleep(confirmDelay)
			pump()
		}

		await("Wallet Title")
		runes(&ctx.Router, "vault")
		pump()
		click(&ctx.Router, Button2)

		await("How many keys share the wallet?")
		click(&ctx.Router, Up) // 3 COSIGNERS -> 2 COSIGNERS
		await("COSIGNERS")
		click(&ctx.Router, Button3)

		await("How many must sign to spend?")
		click(&ctx.Router, Button3) // preset 2 OF 2

		// Cosigner 1: tapped seed.
		await("Add the cosigner's key")
		click(&ctx.Router, Down) // TAP SEED
		await("TAP SEED")
		click(&ctx.Router, Button3)
		await("Tap the cosigner's seed")
		nfc.payloads <- []byte(goldenSeedOil)
		await("oil") // the seed review screen
		click(&ctx.Router, Button3)
		await("Add a passphrase to this seed?")
		click(&ctx.Router, Button3) // NO PASSPHRASE
		await("Confirm to add this cosigner")
		click(&ctx.Router, Button3)

		// Cosigner 2: tapped seed.
		await("Cosigner 2 of 2")
		click(&ctx.Router, Down)
		await("TAP SEED")
		click(&ctx.Router, Button3)
		await("Tap the cosigner's seed")
		nfc.payloads <- []byte(goldenSeedBacon)
		await("bacon")
		click(&ctx.Router, Button3)
		await("Add a passphrase to this seed?")
		click(&ctx.Router, Button3)
		await("Confirm to add this cosigner")
		click(&ctx.Router, Button3)

		// Review: both fingerprints on one held screen.
		await("Create Wallet?")
		await("2A77E0A6")
		await("9A6A2580")
		hold()

		// Export: the animated code, then the first-address check.
		await("Export Wallet")
		click(&ctx.Router, Button3)
		await("First Address")
		click(&ctx.Router, Button3)

		// The entrusted seeds' plates, skipped here.
		await("Cosigner 1 of 2, 2A77E0A6")
		click(&ctx.Router, Down)
		await("SKIP")
		click(&ctx.Router, Button3)
		await("Cosigner 2 of 2, 9A6A2580")
		click(&ctx.Router, Down)
		await("SKIP")
		click(&ctx.Router, Button3)

		// The ordinary descriptor machinery takes over; backing out
		// of it still completes the builder.
		await("Engrave Descriptor")
		await("2-of-2 multisig")
		click(&ctx.Router, Button1)

		for range 100 {
			if _, more := frame(); !more {
				break
			}
		}
		synctest.Wait()
		if !done {
			t.Error("the builder did not complete")
		}
	})
}

// The per-cosigner passphrase ask carries the recovery-complexity
// warning between the choice and the editor, and backing out of the
// warning returns to the choice rather than into the editor.
func TestMultisigPassphraseWarning(t *testing.T) {
	ctx := NewContext(newPlatform())
	var pass string
	var ok bool
	m := goldenMnemonic(t, goldenSeedOil)
	frame, quit := runUI(ctx, func() {
		pass, ok = multisigPassphraseFlow(ctx, &descriptorTheme, m)
	})
	defer quit()
	await := func(marker string) {
		t.Helper()
		awaitUI(t, frame, marker)
	}
	await("Add a passphrase to this seed?")
	click(&ctx.Router, Down)
	await("ADD PASSPHRASE")
	click(&ctx.Router, Button3)
	await("changes this cosigner")
	click(&ctx.Router, Button1) // back out of the warning
	await("Add a passphrase to this seed?")
	click(&ctx.Router, Button3) // NO PASSPHRASE (selection reset on the fresh screen)
	for range 100 {
		if _, more := frame(); !more {
			break
		}
	}
	if !ok || pass != "" {
		t.Errorf("declining after the warning returned %q, %v", pass, ok)
	}
}

// TestBuiltDescriptorPlateParity: the plates a built wallet cuts must
// be the plates a rescan of its own exported descriptor cuts, or an
// aborted set could not resume from a rescan. The descriptor string
// carries no title, so the rescan reinstates it the way the flow's
// operator would; everything else must agree byte for byte.
func TestBuiltDescriptorPlateParity(t *testing.T) {
	built := goldenDescriptor(t)
	rescanned, err := bip380.Parse(built.Encode())
	if err != nil {
		t.Fatal(err)
	}
	rescanned.Title = built.Title

	bLabels, bTexts, bQR, err := fitDescriptor(engraverParams, SquarePlate, built, nil)
	if err != nil {
		t.Fatal(err)
	}
	rLabels, rTexts, rQR, err := fitDescriptor(engraverParams, SquarePlate, rescanned, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(bLabels) != len(rLabels) || bQR != rQR {
		t.Fatalf("single-plate ladders diverge: %v/%q vs %v/%q", bLabels, bQR, rLabels, rQR)
	}
	for i := range bTexts {
		if len(bTexts[i].Paragraphs) != len(rTexts[i].Paragraphs) {
			t.Fatalf("variant %s paragraph counts diverge", bLabels[i])
		}
		for j := range bTexts[i].Paragraphs {
			bp, rp := bTexts[i].Paragraphs[j], rTexts[i].Paragraphs[j]
			if bp.Text != rp.Text || bp.QRScale != rp.QRScale {
				t.Errorf("variant %s paragraph %d diverges:\n%q\n%q", bLabels[i], j, bp.Text, rp.Text)
			}
		}
	}

	bData, bSize, bScale, err := fitShares(engraverParams, built, nil)
	if err != nil {
		t.Fatal(err)
	}
	rData, rSize, rScale, err := fitShares(engraverParams, rescanned, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(bData) != string(rData) || bSize != rSize || bScale != rScale {
		t.Fatal("share partitions diverge between built and rescanned")
	}
	for k := range built.Keys {
		bTxt, bURs, err := shareText(built, bData, k, bSize, bScale)
		if err != nil {
			t.Fatal(err)
		}
		rTxt, rURs, err := shareText(rescanned, rData, k, rSize, rScale)
		if err != nil {
			t.Fatal(err)
		}
		if len(bURs) != len(rURs) {
			t.Fatalf("share %d UR counts diverge", k)
		}
		for i := range bURs {
			if bURs[i] != rURs[i] {
				t.Errorf("share %d UR %d diverges:\n%s\n%s", k, i, bURs[i], rURs[i])
			}
		}
		for i := range bTxt.Paragraphs {
			if bTxt.Paragraphs[i].Text != rTxt.Paragraphs[i].Text {
				t.Errorf("share %d paragraph %d diverges:\n%q\n%q",
					k, i, bTxt.Paragraphs[i].Text, rTxt.Paragraphs[i].Text)
			}
		}
	}
}
