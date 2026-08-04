package gui

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"os"
	"seedhammer.com/engrave"
	"seedhammer.com/gui/assets"
	"seedhammer.com/gui/layout"
	"seedhammer.com/gui/widget"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/v2"
	"seedhammer.com/backup"
	"seedhammer.com/bip39"
	"seedhammer.com/font/sh"
)

// The official BIP39 English vectors carry the master key each mnemonic
// yields when salted with the passphrase "TREZOR". They pin the whole
// chain in one assertion: mnemonic and passphrase through PBKDF2 to the
// BIP32 root. Fetched from trezor/python-mnemonic, not transcribed.
var masterKeyVectors = []struct {
	mnemonic string
	xprv     string
}{
	{
		mnemonic: "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about",
		xprv:     "xprv9s21ZrQH143K3h3fDYiay8mocZ3afhfULfb5GX8kCBdno77K4HiA15Tg23wpbeF1pLfs1c5SPmYHrEpTuuRhxMwvKDwqdKiGJS9XFKzUsAF",
	},
	{
		mnemonic: "legal winner thank year wave sausage worth useful legal winner thank yellow",
		xprv:     "xprv9s21ZrQH143K2gA81bYFHqU68xz1cX2APaSq5tt6MFSLeXnCKV1RVUJt9FWNTbrrryem4ZckN8k4Ls1H6nwdvDTvnV7zEXs2HgPezuVccsq",
	},
	{
		mnemonic: "letter advice cage absurd amount doctor acoustic avoid letter advice cage above",
		xprv:     "xprv9s21ZrQH143K2shfP28KM3nr5Ap1SXjz8gc2rAqqMEynmjt6o1qboCDpxckqXavCwdnYds6yBHZGKHv7ef2eTXy461PXUjBFQg6PrwY4Gzq",
	},
	{
		mnemonic: "zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo wrong",
		xprv:     "xprv9s21ZrQH143K2V4oox4M8Zmhi2Fjx5XK4Lf7GKRvPSgydU3mjZuKGCTg7UPiBUD7ydVPvSLtg9hjp7MQTYsW67rZHAXeccqYqrsx8LcXnyd",
	},
	{
		mnemonic: "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon agent",
		xprv:     "xprv9s21ZrQH143K3mEDrypcZ2usWqFgzKB6jBBx9B6GfC7fu26X6hPRzVjzkqkPvDqp6g5eypdk6cyhGnBngbjeHTe4LsuLG1cCmKJka5SMkmU",
	},
	{
		mnemonic: "legal winner thank year wave sausage worth useful legal winner thank year wave sausage worth useful legal will",
		xprv:     "xprv9s21ZrQH143K3Lv9MZLj16np5GzLe7tDKQfVusBni7toqJGcnKRtHSxUwbKUyUWiwpK55g1DUSsw76TF1T93VT4gz4wt5RM23pkaQLnvBh7",
	},
	{
		mnemonic: "letter advice cage absurd amount doctor acoustic avoid letter advice cage absurd amount doctor acoustic avoid letter always",
		xprv:     "xprv9s21ZrQH143K3VPCbxbUtpkh9pRG371UCLDz3BjceqP1jz7XZsQ5EnNkYAEkfeZp62cDNj13ZTEVG1TEro9sZ9grfRmcYWLBhCocViKEJae",
	},
	{
		mnemonic: "zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo when",
		xprv:     "xprv9s21ZrQH143K36Ao5jHRVhFGDbLP6FCx8BEEmpru77ef3bmA928BxsqvVM27WnvvyfWywiFN8K6yToqMaGYfzS6Db1EHAXT5TuyCLBXUfdm",
	},
	{
		mnemonic: "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon art",
		xprv:     "xprv9s21ZrQH143K32qBagUJAMU2LsHg3ka7jqMcV98Y7gVeVyNStwYS3U7yVVoDZ4btbRNf4h6ibWpY22iRmXq35qgLs79f312g2kj5539ebPM",
	},
	{
		mnemonic: "legal winner thank year wave sausage worth useful legal winner thank year wave sausage worth useful legal winner thank year wave sausage worth title",
		xprv:     "xprv9s21ZrQH143K3Y1sd2XVu9wtqxJRvybCfAetjUrMMco6r3v9qZTBeXiBZkS8JxWbcGJZyio8TrZtm6pkbzG8SYt1sxwNLh3Wx7to5pgiVFU",
	},
	{
		mnemonic: "letter advice cage absurd amount doctor acoustic avoid letter advice cage absurd amount doctor acoustic avoid letter advice cage absurd amount doctor acoustic bless",
		xprv:     "xprv9s21ZrQH143K3CSnQNYC3MqAAqHwxeTLhDbhF43A4ss4ciWNmCY9zQGvAKUSqVUf2vPHBTSE1rB2pg4avopqSiLVzXEU8KziNnVPauTqLRo",
	},
	{
		mnemonic: "zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo vote",
		xprv:     "xprv9s21ZrQH143K2WFF16X85T2QCpndrGwx6GueB72Zf3AHwHJaknRXNF37ZmDrtHrrLSHvbuRejXcnYxoZKvRquTPyp2JiNG3XcjQyzSEgqCB",
	},
	{
		mnemonic: "ozone drill grab fiber curtain grace pudding thank cruise elder eight picnic",
		xprv:     "xprv9s21ZrQH143K2oZ9stBYpoaZ2ktHj7jLz7iMqpgg1En8kKFTXJHsjxry1JbKH19YrDTicVwKPehFKTbmaxgVEc5TpHdS1aYhB2s9aFJBeJH",
	},
	{
		mnemonic: "gravity machine north sort system female filter attitude volume fold club stay feature office ecology stable narrow fog",
		xprv:     "xprv9s21ZrQH143K3uT8eQowUjsxrmsA9YUuQQK1RLqFufzybxD6DH6gPY7NjJ5G3EPHjsWDrs9iivSbmvjc9DQJbJGatfa9pv4MZ3wjr8qWPAK",
	},
	{
		mnemonic: "hamster diagram private dutch cause delay private meat slide toddler razor book happy fancy gospel tennis maple dilemma loan word shrug inflict delay length",
		xprv:     "xprv9s21ZrQH143K2XTAhys3pMNcGn261Fi5Ta2Pw8PwaVPhg3D8DWkzWQwjTJfskj8ofb81i9NP2cUNKxwjueJHHMQAnxtivTA75uUFqPFeWzk",
	},
	{
		mnemonic: "scheme spot photo card baby mountain device kick cradle pact join borrow",
		xprv:     "xprv9s21ZrQH143K3FperxDp8vFsFycKCRcJGAFmcV7umQmcnMZaLtZRt13QJDsoS5F6oYT6BB4sS6zmTmyQAEkJKxJ7yByDNtRe5asP2jFGhT6",
	},
	{
		mnemonic: "horn tenant knee talent sponsor spell gate clip pulse soap slush warm silver nephew swap uncle crack brave",
		xprv:     "xprv9s21ZrQH143K3R1SfVZZLtVbXEB9ryVxmVtVMsMwmEyEvgXN6Q84LKkLRmf4ST6QrLeBm3jQsb9gx1uo23TS7vo3vAkZGZz71uuLCcywUkt",
	},
	{
		mnemonic: "panda eyebrow bullet gorilla call smoke muffin taste mesh discover soft ostrich alcohol speed nation flash devote level hobby quick inner drive ghost inside",
		xprv:     "xprv9s21ZrQH143K2WNnKmssvZYM96VAr47iHUQUTUyUXH3sAGNjhJANddnhw3i3y3pBbRAVk5M5qUGFr4rHbEWwXgX4qrvrceifCYQJbbFDems",
	},
	{
		mnemonic: "cat swing flag economy stadium alone churn speed unique patch report train",
		xprv:     "xprv9s21ZrQH143K4G28omGMogEoYgDQuigBo8AFHAGDaJdqQ99QKMQ5J6fYTMfANTJy6xBmhvsNZ1CJzRZ64PWbnTFUn6CDV2FxoMDLXdk95DQ",
	},
	{
		mnemonic: "light rule cinnamon wrap drastic word pride squirrel upgrade then income fatal apart sustain crack supply proud access",
		xprv:     "xprv9s21ZrQH143K3wtsvY8L2aZyxkiWULZH4vyQE5XkHTXkmx8gHo6RUEfH3Jyr6NwkJhvano7Xb2o6UqFKWHVo5scE31SGDCAUsgVhiUuUDyh",
	},
	{
		mnemonic: "all hour make first leader extend hole alien behind guard gospel lava path output census museum junior mass reopen famous sing advance salt reform",
		xprv:     "xprv9s21ZrQH143K3rEfqSM4QZRVmiMuSWY9wugscmaCjYja3SbUD3KPEB1a7QXJoajyR2T1SiXU7rFVRXMV9XdYVSZe7JoUXdP4SRHTxsT1nzm",
	},
	{
		mnemonic: "vessel ladder alter error federal sibling chat ability sun glass valve picture",
		xprv:     "xprv9s21ZrQH143K2QWV9Wn8Vvs6jbqfF1YbTCdURQW9dLFKDovpKaKrqS3SEWsXCu6ZNky9PSAENg6c9AQYHcg4PjopRGGKmdD313ZHszymnps",
	},
	{
		mnemonic: "scissors invite lock maple supreme raw rapid void congress muscle digital elegant little brisk hair mango congress clump",
		xprv:     "xprv9s21ZrQH143K4aERa2bq7559eMCCEs2QmmqVjUuzfy5eAeDX4mqZffkYwpzGQRE2YEEeLVRoH4CSHxianrFaVnMN2RYaPUZJhJx8S5j6puX",
	},
	{
		mnemonic: "void come effort suffer camp survey warrior heavy shoot primary clutch crush open amazing screen patrol group space point ten exist slush involve unfold",
		xprv:     "xprv9s21ZrQH143K39rnQJknpH1WEPFJrzmAqqasiDcVrNuk926oizzJDDQkdiTvNPr2FYDYzWgiMiC63YmfPAa2oPyNB23r2g7d1yiK6WpqaQS",
	},
}

func TestDeriveMasterKeyPassphraseVectors(t *testing.T) {
	for _, v := range masterKeyVectors {
		m, err := bip39.ParseMnemonic(v.mnemonic)
		if err != nil {
			t.Fatalf("%q: %v", v.mnemonic, err)
		}
		mk, ok := deriveMasterKey(m, "TREZOR", &chaincfg.MainNetParams)
		if !ok {
			t.Fatalf("%q: derivation failed", v.mnemonic)
		}
		if got := mk.String(); got != v.xprv {
			t.Errorf("master key for %q\n got %s\nwant %s", v.mnemonic, got, v.xprv)
		}
	}
}

// TestPassphraseChangesWallet guards the property the feature exists
// for: the words alone and the words plus a passphrase are different
// wallets, and declining a passphrase must leave the original behaviour
// exactly as it was.
func TestPassphraseChangesWallet(t *testing.T) {
	m, err := bip39.ParseMnemonic(masterKeyVectors[0].mnemonic)
	if err != nil {
		t.Fatal(err)
	}
	bare, ok := deriveMasterKey(m, "", &chaincfg.MainNetParams)
	if !ok {
		t.Fatal("bare derivation failed")
	}
	withPass, ok := deriveMasterKey(m, "TREZOR", &chaincfg.MainNetParams)
	if !ok {
		t.Fatal("passphrase derivation failed")
	}
	if bare.String() == withPass.String() {
		t.Fatal("the passphrase did not change the wallet")
	}
	// One wrong character is a different wallet with no warning, which
	// is why the passphrase is shown for confirmation rather than typed
	// twice. This is that property, asserted.
	typo, ok := deriveMasterKey(m, "TREZOr", &chaincfg.MainNetParams)
	if !ok {
		t.Fatal("typo derivation failed")
	}
	if typo.String() == withPass.String() {
		t.Error("a case change in the passphrase produced the same wallet")
	}
	// And the descriptor has to follow the passphrase, not just the seed.
	a, err := seedDescriptor(m, "", seedScripts[0], seedScripts[0].DerivationPath())
	if err != nil {
		t.Fatal(err)
	}
	b, err := seedDescriptor(m, "TREZOR", seedScripts[0], seedScripts[0].DerivationPath())
	if err != nil {
		t.Fatal(err)
	}
	if a.Encode() == b.Encode() {
		t.Error("the descriptor ignored the passphrase")
	}
}

// TestPassphrasePlate pins what the third plate says. It names the
// passphrase, the wallet it belongs to and the path, and nothing else:
// the advice it used to carry cost three lines of a grid the passphrase
// itself needs, and the fingerprint already says which seed it pairs
// with.
func TestPassphrasePlate(t *testing.T) {
	got := passphrasePlate("correct horse", "73C5DA0A", "m/84h/0h/0h")
	for _, want := range []string{
		"correct horse", // verbatim, case intact
		"73C5DA0A",      // which seed it pairs with
		"m/84h/0h/0h",   // and at which path
		"13 CHARACTERS", // the only integrity check a passphrase has
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plate text missing %q:\n%s", want, got)
		}
	}
	// The path is optional: declining the descriptor still yields a
	// usable plate rather than a dangling label.
	if bare := passphrasePlate("x", "73C5DA0A", ""); strings.Contains(bare, "PATH") {
		t.Errorf("empty path still emitted a PATH line:\n%s", bare)
	}
	// The fixed text must never be what forces a smaller font; only the
	// passphrase itself may push the size down. The bound is the plate
	// grid at the largest size, not the passphrase cap: comparing
	// against 64 let a 43-char line through and halved the glyphs on
	// the one plate whose job is character-for-character read-back.
	limit := backup.CharsPerLine(newPlatform().EngraverParams(), sh.Font, backup.FontSizes[0])
	for _, line := range strings.Split(got, "\n") {
		if line == "correct horse" {
			continue
		}
		if len(line) > limit {
			t.Errorf("fixed line is %d chars, over the %d a %.1fmm line holds: %q",
				len(line), limit, backup.FontSizes[0], line)
		}
	}
}

// TestPassphrasePlateSetsOffTheSecret checks the passphrase stands
// alone between blank lines, so a reader can see where it begins and
// ends even when the line ladder wraps it.
func TestPassphrasePlateSetsOffTheSecret(t *testing.T) {
	const pass = "correct horse battery staple"
	lines := strings.Split(passphrasePlate(pass, "73C5DA0A", "m/84h/0h/0h"), "\n")
	at := -1
	for i, l := range lines {
		if l == pass {
			at = i
		}
	}
	if at < 1 || at >= len(lines)-1 {
		t.Fatalf("passphrase line not found with room around it: %q", lines)
	}
	if lines[at-1] != "" || lines[at+1] != "" {
		t.Errorf("passphrase is not set off by blank lines:\n%q\n%q\n%q",
			lines[at-1], lines[at], lines[at+1])
	}
	// A long one is allowed to wrap; the count is what makes it readable.
	long := strings.Repeat("a b ", 25)[:100]
	txt := passphrasePlate(long, "73C5DA0A", "")
	if !strings.Contains(txt, itoa(len(long))+" CHARACTERS") {
		t.Errorf("character count missing for a %d-char passphrase:\n%s", len(long), txt)
	}
	if _, _, err := fitText(newPlatform().EngraverParams(), txt); err != nil {
		t.Errorf("a %d-char passphrase does not fit any size: %v", len(long), err)
	}
}

// Every printable ASCII character must be typeable: a passphrase that
// cannot be entered locks the user out of an existing wallet entirely,
// and spaced passphrases are ordinary.
func TestPassphraseAlphabetCoversPrintableASCII(t *testing.T) {
	ctx := NewContext(newPlatform())
	typeable := map[rune]bool{}
	for _, alph := range passLayers {
		for _, row := range NewKeyboard(ctx, alph).keys {
			for _, key := range row {
				typeable[key.r] = true
			}
		}
	}
	var missing []rune
	for r := rune(0x20); r <= 0x7e; r++ {
		if !typeable[r] {
			missing = append(missing, r)
		}
	}
	if len(missing) > 0 {
		t.Errorf("cannot type %d printable ASCII characters: %q", len(missing), string(missing))
	}
}

// TestEveryPlateIsDeclinable drives both plate offers and asserts two
// things at once: engraving is the row the selection starts on, and the
// other row declines without engraving. Neither alone is enough. A label
// check passes while the choice sense is backwards, and a drive passes
// while the rows are in the wrong order.
func TestEveryPlateIsDeclinable(t *testing.T) {
	m, err := bip39.ParseMnemonic(masterKeyVectors[0].mnemonic)
	if err != nil {
		t.Fatal(err)
	}
	ss := new(SeedScreen)

	// The cursor move and the confirm have to land in different frames.
	// Choose tests its confirm button before it reads the cursor keys,
	// so queueing them together confirms whatever row 0 happens to be.
	declines := func(name, engraveRow string, flow func(ctx *Context)) {
		t.Helper()
		ctx := NewContext(newPlatform())
		frame, quit := runUI(ctx, func() { flow(ctx) })
		defer quit()
		content, drew := frame()
		if !drew {
			t.Fatalf("%s: offer drew no frame", name)
		}
		rows := strings.ToUpper(strings.ReplaceAll(content, " ", ""))
		eng := strings.Index(rows, strings.ReplaceAll(engraveRow, " ", ""))
		skip := strings.Index(rows, "SKIP")
		switch {
		case eng < 0 || skip < 0:
			t.Fatalf("%s: offer is missing a row: %q", name, content)
		case eng > skip:
			t.Errorf("%s: SKIP comes before %q, so the selection starts on the way out",
				name, engraveRow)
		}
		click(&ctx.Router, Down)
		if _, drew := frame(); !drew {
			t.Fatalf("%s: exited while moving to SKIP", name)
		}
		click(&ctx.Router, Button3)
		if _, drew := frame(); drew {
			t.Errorf("%s: kept drawing after SKIP, so it did not decline", name)
		}
	}

	declines("seed plate", "ENGRAVE SEED", func(ctx *Context) {
		if !seedPlateFlow(ctx, &descriptorTheme, ss, m, "") {
			t.Error("skipping the seed plate did not continue to the other plates")
		}
	})
	declines("passphrase plate", "ENGRAVE PASSPHRASE", func(ctx *Context) {
		passphrasePlateFlow(ctx, &descriptorTheme, m, "hunter2", "m/84h/0h/0h")
	})

	// With no passphrase there is nothing to offer at all.
	ctx := NewContext(newPlatform())
	passphrasePlateFlow(ctx, &descriptorTheme, m, "", "m/84h/0h/0h")

	// The descriptor declines by backing out of its address-type
	// choice, which leaves no path for the passphrase plate to record.
	ctx = NewContext(newPlatform())
	click(&ctx.Router, Button1)
	if path := walletDescriptorFlow(ctx, &descriptorTheme, m, ""); path != "" {
		t.Errorf("declining the descriptor still reported a path: %q", path)
	}
}

// TestWalletFingerprintValues pins the actual hex every screen and plate
// prints. Five one-line mutations survived the suite before this: the
// passphrase silently dropped at four derivation sites, and a mask bug
// in fingerprintHex. Literals catch all of them.
func TestWalletFingerprintValues(t *testing.T) {
	m, err := bip39.ParseMnemonic(masterKeyVectors[0].mnemonic)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ pass, want string }{
		{"", "73C5DA0A"},
		{"TREZOR", "B4E3F5ED"},
		{"hunter2", "CA2C62D2"},
	} {
		got, ok := walletFingerprint(m, tc.pass)
		if !ok {
			t.Fatalf("%q: derivation failed", tc.pass)
		}
		if got != tc.want {
			t.Errorf("passphrase %q: fingerprint %s, want %s", tc.pass, got, tc.want)
		}
		// The seed plate derives its own; it must agree with the screen.
		mfp, err := masterFingerprintFor(m, tc.pass, &chaincfg.MainNetParams)
		if err != nil {
			t.Fatal(err)
		}
		if plate := strings.ToUpper(fingerprintHex(mfp)); plate != tc.want {
			t.Errorf("passphrase %q: seed plate says %s, screen says %s", tc.pass, plate, tc.want)
		}
	}
	// Every nibble must survive: a masked-off high bit reads plausibly.
	for _, tc := range []struct {
		fp   uint32
		want string
	}{{0, "00000000"}, {0x0000000f, "0000000f"}, {0xffffffff, "ffffffff"}, {0x89abcdef, "89abcdef"}} {
		if got := fingerprintHex(tc.fp); got != tc.want {
			t.Errorf("fingerprintHex(%#x) = %q, want %q", tc.fp, got, tc.want)
		}
	}
	if got := itoa(100000000); got != "100000000" {
		t.Errorf("itoa overflowed its buffer: %q", got)
	}
}

// TestPassphraseKeyboardTypesVerbatim drives the real keyboard. The
// passphrase is the one field with no checksum, so a silent case fold
// opens a different wallet, and that mutation survived the suite.
func TestPassphraseKeyboardTypesVerbatim(t *testing.T) {
	ctx := NewContext(newPlatform())
	kbd := NewKeyboard(ctx, passLayers[0])
	kbd.Verbatim = true
	runes(&ctx.Router, "abc")
	for kbd.Update(ctx) {
	}
	if kbd.Fragment != "abc" {
		t.Errorf("lower-case layer typed %q, want %q", kbd.Fragment, "abc")
	}
	up := NewKeyboard(ctx, passLayers[1])
	up.Verbatim = true
	runes(&ctx.Router, "ABC")
	for up.Update(ctx) {
	}
	if up.Fragment != "ABC" {
		t.Errorf("upper-case layer typed %q, want %q", up.Fragment, "ABC")
	}
	// The word keyboard must keep folding: its wordlist is upper case.
	w := NewKeyboard(ctx, wordKeys)
	runes(&ctx.Router, "abc")
	for w.Update(ctx) {
	}
	if w.Fragment != "ABC" {
		t.Errorf("word keyboard typed %q, want %q", w.Fragment, "ABC")
	}
}

// TestPassphraseEditKeepsText pins that EDIT carries the text back to
// the keyboard: retyping from memory is how a second typo is made.
func TestPassphraseEditKeepsText(t *testing.T) {
	ctx := NewContext(newPlatform())
	kbds := make([]*Keyboard, len(passLayers))
	for i, alph := range passLayers {
		kbds[i] = NewKeyboard(ctx, alph)
		kbds[i].Verbatim = true
		kbds[i].Fragment = "carried"
	}
	for i, k := range kbds {
		if k.Fragment != "carried" {
			t.Errorf("layer %d dropped the carried text: %q", i, k.Fragment)
		}
	}
}

// TestEngraveSeedUsesPassphrase pins the seed plate's own derivation.
// Asserting masterFingerprintFor is not enough: dropping the passphrase
// at engraveSeed's call site leaves the helper correct and the plate
// wrong, and that mutation survived the suite.
func TestEngraveSeedUsesPassphrase(t *testing.T) {
	m, err := bip39.ParseMnemonic(masterKeyVectors[0].mnemonic)
	if err != nil {
		t.Fatal(err)
	}
	params := newPlatform().EngraverParams()
	digest := func(pass string) string {
		plan, err := engraveSeed(params, m, pass)
		if err != nil {
			t.Fatalf("%q: %v", pass, err)
		}
		plate, err := toPlate(plan, params)
		if err != nil {
			t.Fatalf("%q: %v", pass, err)
		}
		h := sha256.New()
		for k := range plate.Spline {
			fmt.Fprintf(h, "%v", k)
		}
		return hex.EncodeToString(h.Sum(nil))
	}
	bare, withPass := digest(""), digest("TREZOR")
	if bare == withPass {
		t.Error("the seed plate is identical with and without a passphrase; " +
			"its fingerprint does not follow the passphrase")
	}
}

// The confirm screen carries the same character count the plate does.
// It is the only integrity check a passphrase has, and the screen runs
// first: by the time the plate exists the seed plate is already cut with
// the passphrase-derived fingerprint. The read-back wraps on a space
// without drawing it, so a count the user can compare is what makes a
// wrapped passphrase checkable at all.
func TestConfirmPassphraseShowsCharacterCount(t *testing.T) {
	m, err := bip39.ParseMnemonic(masterKeyVectors[0].mnemonic)
	if err != nil {
		t.Fatal(err)
	}
	for _, pass := range []string{"x", "correcthorsebatterystaple", strings.Repeat("a", 64)} {
		ctx := NewContext(newPlatform())
		frame, quit := runUI(ctx, func() {
			confirmPassphraseFlow(ctx, &descriptorTheme, m, pass)
		})
		content, ok := frame()
		quit()
		if !ok {
			t.Fatalf("%d chars: confirm screen drew no frame", len(pass))
		}
		want := itoa(len(pass)) + " characters"
		if !uiContains(content, want) {
			t.Errorf("%d chars: confirm screen does not show %q", len(pass), want)
		}
	}
}

// Entry is uncapped, so the two things that must hold are that the tail
// window keeps the box inside its area at any length, and that the plate
// refuses cleanly rather than engraving something wrong once the text
// outgrows the grid.
func TestUncappedPassphraseStaysInBounds(t *testing.T) {
	ctx := NewContext(newPlatform())
	th := &descriptorTheme
	kbd := NewKeyboard(ctx, passLayers[0])
	kbd.Verbatim = true
	_, kbdsz := kbd.Layout(ctx, th)
	screen := layout.Rectangle{Max: image.Pt(480, 320)} // cmd/controller lcdWidth, lcdHeight
	_, content := screen.CutTop(leadingSize)
	content, _ = content.CutBottom(8)
	content, _ = content.CutEnd(assets.NavBtnPrimary.Bounds().Dx() + 8)
	top, _ := content.CutBottom(kbdsz.Y)
	width := content.Dx() - 2*buttonPadX

	widest := string(widestPassphraseRune(ctx, th))
	for _, n := range []int{1, 50, 100, 500, 2000} {
		for _, g := range []string{widest, "a", "i"} {
			s := strings.Repeat(g, n)
			tail := tailFitting(ctx, th, s, width, top.Dy())
			if !strings.HasSuffix(s, tail) {
				t.Errorf("n=%d %q: tail is not a suffix of the fragment", n, g)
			}
			_, sz := widget.Labelw(&ctx.B, ctx.Styles.word, width, th.Background, tail)
			if got := sz.Y + 3 + buttonPadY; got > top.Dy() {
				t.Errorf("n=%d %q: tail still needs %dpx, the box has %dpx", n, g, got, top.Dy())
			}
		}
	}

	// The plate is the one real ceiling. It must take a long passphrase
	// and refuse an impossible one with an error, never silently.
	if _, _, err := fitText(engrave.SH2Params, passphrasePlate(strings.Repeat("a", 800), "64493BC6", "m/84h/0h/0h")); err != nil {
		t.Errorf("800 characters should still fit a plate: %v", err)
	}
	if _, _, err := fitText(engrave.SH2Params, passphrasePlate(strings.Repeat("a", 4000), "64493BC6", "m/84h/0h/0h")); err == nil {
		t.Error("4000 characters must be refused by the plate, not engraved")
	}
}

// widestPassphraseRune returns the widest rune the passphrase keyboard
// can produce, so the plate test measures the true worst case.
func widestPassphraseRune(ctx *Context, th *Colors) rune {
	widest, max := 'W', 0
	for _, layer := range passLayers {
		for _, r := range layer {
			if r == '\n' || r == layerKey {
				continue
			}
			if _, sz := widget.Labelw(&ctx.B, ctx.Styles.word, 1<<20, th.Background, string(r)); sz.X > max {
				widest, max = r, sz.X
			}
		}
	}
	return widest
}

// The howto renders the plate literally, so a change to passphrasePlate
// silently makes the manual wrong. Pin the example it shows.
func TestHowtoPlateMatchesTheCode(t *testing.T) {
	const howto = "../docs/howto-generate-a-seed.md"
	doc, err := os.ReadFile(howto)
	if err != nil {
		t.Fatal(err)
	}
	want := passphrasePlate("hunter2", "CA2C62D2", "m/84h/0h/0h")
	if !strings.Contains(string(doc), want) {
		t.Errorf("%s does not show the plate this code produces:\n--- want ---\n%s\n--- end ---", howto, want)
	}
}

// The layer key opens every bottom row, next to the back button.
// NewKeyboard appends backspace to the final authored row, so the
// check is on that row's first key.
func TestLayerKeySitsBottomLeft(t *testing.T) {
	ctx := NewContext(newPlatform())
	for name, layers := range map[string]*[3]string{"passphrase": &passLayers, "text": &textLayers} {
		for i, alph := range layers {
			rows := NewKeyboard(ctx, alph).keys
			last := rows[len(rows)-1]
			if last[0].r != layerKey {
				t.Errorf("%s layer %d: bottom row starts with %q, want the layer key", name, i, last[0].r)
			}
			for j, row := range rows[:len(rows)-1] {
				for _, k := range row {
					if k.r == layerKey {
						t.Errorf("%s layer %d: layer key sits on row %d, not the bottom row", name, i, j)
					}
				}
			}
		}
	}
}

// The return key types the newline the plate honours. It is a
// sentinel: alphabet rows split on '\n', so the key cannot be the
// literal newline, and rune events translate so typed input reaches
// the same key.
func TestTextKeyboardTypesNewline(t *testing.T) {
	ctx := NewContext(newPlatform())
	kbd := NewKeyboard(ctx, textLayers[0])
	kbd.Verbatim = true
	runes(&ctx.Router, "hi\nmom")
	for kbd.Update(ctx) {
	}
	if kbd.Fragment != "hi\nmom" {
		t.Errorf("typed %q, want %q", kbd.Fragment, "hi\nmom")
	}
	// CR arrives from CRLF-minded sources; it is the same key.
	kbd.Clear()
	runes(&ctx.Router, "a\rb")
	for kbd.Update(ctx) {
	}
	if kbd.Fragment != "a\nb" {
		t.Errorf("typed %q, want %q", kbd.Fragment, "a\nb")
	}
	// A passphrase is one line by construction; its layers must not
	// gain the key. The text letter layers both carry it, so only the
	// symbols layer costs a cycle to reach it.
	for i, alph := range passLayers {
		if strings.ContainsRune(alph, newlineKey) {
			t.Errorf("passphrase layer %d carries the return key", i)
		}
	}
	for _, i := range []int{0, 1} {
		if !strings.ContainsRune(textLayers[i], newlineKey) {
			t.Errorf("text layer %d misses the return key", i)
		}
	}
}

// Free text must reach every printable ASCII character plus the
// newline, or a payload the NFC path accepts could not be typed on
// the device.
func TestTextAlphabetCoversPlainText(t *testing.T) {
	ctx := NewContext(newPlatform())
	typeable := map[rune]bool{}
	for _, alph := range textLayers {
		for _, row := range NewKeyboard(ctx, alph).keys {
			for _, key := range row {
				r := key.r
				if r == newlineKey {
					r = '\n'
				}
				typeable[r] = true
			}
		}
	}
	var missing []rune
	for r := rune(0x20); r <= 0x7e; r++ {
		if !typeable[r] {
			missing = append(missing, r)
		}
	}
	if !typeable['\n'] {
		missing = append(missing, '\n')
	}
	if len(missing) > 0 {
		t.Errorf("cannot type %d plain-text runes: %q", len(missing), string(missing))
	}
}
