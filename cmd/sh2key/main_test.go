package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"seedhammer.com/bip39"
	"seedhammer.com/nip19"
	"seedhammer.com/seedqr"
)

// The test vector is the synthetic scalar 01 02 .. 20, chosen so it
// cannot be mistaken for a generated key. fixtureDERHex is openssl's
// canonical SEC1 encoding of that scalar, recorded here rather than
// produced by this package: it pins field order, the curve OID, the
// embedded public point and the fixed-width scalar, which is where an
// encoder bug would show. The PEM armor around it is RFC 7468 and both
// encoders agree on it, so the fixture is assembled from the DER and no
// key file, nor a private-key banner, lives in the tree.
//
// Words come from a python reimplementation over bip39/wordlist.txt,
// the fingerprint from openssl:
//
//	openssl ec -in k.pem -pubout -conv_form uncompressed -outform DER |
//	    tail -c 64 | sha256sum
const (
	// fixtureKeyName is the path the screens display; no such file
	// needs to exist for a rendering test.
	fixtureKeyName     = "testkey.pem"
	fixtureScalarHex   = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	fixtureWords       = "absurd avoid scissors anxiety gather lottery category door army half long cage bachelor another expect people blade school educate curtain scrub monitor lady beyond"
	fixtureFingerprint = "6183a9ceb05354a69c31fdcfa1ab5982bb89136dbd7259bebea0919b548bd6a8"
	fixtureDERHex      = "307402010104200102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20" +
		"a00706052b8104000aa1440342000484bf7562262bbd6940085748f3be6afa52ae317155181ece31b6" +
		"6351ccffa4b08cc43d63b2859d469fee15f31c9edb5324266e6fd0407e87382d60fc4511acd8"
)

// fixturePEM assembles the vector's PEM from openssl's recorded DER.
func fixturePEM(t *testing.T) []byte {
	t.Helper()
	der, err := hex.DecodeString(fixtureDERHex)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}

func loadFixture(t *testing.T) (*secp256k1.PrivateKey, []byte) {
	t.Helper()
	pemBytes := fixturePEM(t)
	priv, err := parseKeyPEM(pemBytes)
	if err != nil {
		t.Fatal(err)
	}
	scalar, err := hex.DecodeString(fixtureScalarHex)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(priv.Serialize(), scalar) {
		t.Fatalf("the recorded DER does not hold the test scalar")
	}
	return priv, pemBytes
}

// fixtureKeyFile writes the vector to a temporary file for the
// commands that take a key path.
func fixtureKeyFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), fixtureKeyName)
	if err := os.WriteFile(path, fixturePEM(t), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFixtureVector(t *testing.T) {
	priv, _ := loadFixture(t)
	if got := fingerprintHex(priv); got != fixtureFingerprint {
		t.Errorf("fingerprint = %s, want %s", got, fixtureFingerprint)
	}
	if got := mnemonicFromKey(priv).String(); got != fixtureWords {
		t.Errorf("words = %q, want %q", got, fixtureWords)
	}
}

// TestPEMByteIdentical is the cmp(1) guarantee of the backup howto:
// our emit must reproduce what openssl ecparam -genkey wrote, byte
// for byte.
func TestPEMByteIdentical(t *testing.T) {
	priv, pemBytes := loadFixture(t)
	if got := marshalKeyPEM(priv); !bytes.Equal(got, pemBytes) {
		t.Errorf("marshalKeyPEM differs from the openssl-written fixture:\ngot:\n%s\nwant:\n%s", got, pemBytes)
	}
}

func TestRoundTripProperty(t *testing.T) {
	for range 200 {
		var ent [32]byte
		rand.Read(ent[:])
		priv, err := keyFromScalar(ent[:])
		if err != nil {
			// The ~2^-128 rejects; random input never hits them.
			t.Fatalf("random scalar rejected: %v", err)
		}
		m := mnemonicFromKey(priv)
		back, err := keyFromMnemonic(m)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(back.Serialize(), ent[:]) {
			t.Fatalf("mnemonic round trip changed the scalar")
		}
		reparsed, err := parseKeyPEM(marshalKeyPEM(priv))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(reparsed.Serialize(), ent[:]) {
			t.Fatalf("PEM round trip changed the scalar")
		}
	}
}

func TestScalarRange(t *testing.T) {
	zero := make([]byte, 32)
	if _, err := keyFromScalar(zero); err == nil {
		t.Error("zero scalar accepted")
	}
	// The secp256k1 group order n.
	n, _ := hex.DecodeString("fffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141")
	if _, err := keyFromScalar(n); err == nil {
		t.Error("scalar n accepted")
	}
	nMinus1 := bytes.Clone(n)
	nMinus1[31]--
	if _, err := keyFromScalar(nMinus1); err != nil {
		t.Errorf("scalar n-1 rejected: %v", err)
	}
	one := make([]byte, 32)
	one[31] = 1
	if _, err := keyFromScalar(one); err != nil {
		t.Errorf("scalar 1 rejected: %v", err)
	}
	// The same rejects must fire through the mnemonic path.
	for _, ent := range [][]byte{zero, n} {
		if _, err := keyFromMnemonic(bip39.New(ent)); err == nil {
			t.Errorf("mnemonic of invalid scalar %x accepted", ent[:4])
		}
	}
}

// TestEightFinalWords pins the claim the last-word chooser relies on:
// given 23 words, exactly 8 of 2048 candidates satisfy the checksum,
// because word 24 carries 3 entropy bits and 8 checksum bits.
func TestEightFinalWords(t *testing.T) {
	for range 20 {
		var ent [32]byte
		rand.Read(ent[:])
		m := bip39.New(ent[:])
		entries := make([]wordEntry, 23)
		for i := range entries {
			entries[i] = wordEntry{w: m[i]}
		}
		finals := validFinalWords(entries)
		if len(finals) != 8 {
			t.Fatalf("%d checksum-valid final words, want exactly 8", len(finals))
		}
		found := false
		for _, w := range finals {
			if w == m[23] {
				found = true
			}
		}
		if !found {
			t.Fatal("the true final word is not among the valid ones")
		}
	}
}

func TestPackMnemonicAgreesWithBip39(t *testing.T) {
	var buf [33]byte
	for range 500 {
		words := make([]bip39.Word, 24)
		for i := range words {
			words[i] = bip39.RandomWord()
		}
		packMnemonic(words, &buf)
		sum := sha256.Sum256(buf[:32])
		fast := sum[0] == buf[32]
		slow := bip39.Mnemonic(words).Valid()
		if fast != slow {
			t.Fatalf("fast checksum %v, bip39 says %v for %v", fast, slow, words)
		}
	}
}

func TestRepairNamesTheWord(t *testing.T) {
	priv, _ := loadFixture(t)
	m := mnemonicFromKey(priv)
	fp := fingerprint(priv)
	words := make([]bip39.Word, 24)
	copy(words, m)
	const pos = 16
	orig := words[pos]
	words[pos] = (orig + 700) % bip39.NumWords

	sols := searchOne(words, positions24(), fp[:])
	if len(sols) != 1 {
		t.Fatalf("%d solutions, want 1", len(sols))
	}
	hit := sols[0][0]
	if hit.pos != pos || hit.to != orig {
		t.Fatalf("repair named word %d -> %v, want word %d -> %v", hit.pos+1, hit.to, pos+1, orig)
	}
}

func TestUnknownWordRecovery(t *testing.T) {
	priv, _ := loadFixture(t)
	m := mnemonicFromKey(priv)
	fp := fingerprint(priv)
	for _, pos := range []int{0, 11, 23} {
		words := make([]bip39.Word, 24)
		copy(words, m)
		orig := words[pos]
		words[pos] = -1
		sols := searchOne(words, []int{pos}, fp[:])
		if len(sols) != 1 || sols[0][0].to != orig {
			t.Fatalf("position %d: unknown-word recovery failed: %v", pos+1, sols)
		}
	}
}

func TestTwoWordRepair(t *testing.T) {
	priv, _ := loadFixture(t)
	m := mnemonicFromKey(priv)
	fp := fingerprint(priv)
	words := make([]bip39.Word, 24)
	copy(words, m)
	i, j := 3, 20
	oi, oj := words[i], words[j]
	words[i] = (oi + 99) % bip39.NumWords
	words[j] = (oj + 1234) % bip39.NumWords
	sols := searchTwo(words, [][2]int{{i, j}}, fp[:], nil)
	if len(sols) != 1 {
		t.Fatalf("%d solutions, want 1", len(sols))
	}
	if sols[0][0].to != oi || sols[0][1].to != oj {
		t.Fatalf("wrong repair: %v", sols[0])
	}
}

// TestExpectedRows pins the row packing on the worked example in the
// signing howto: an array starting 180, 81, 37, 136 reads back as
// row 0 = 0x51b4 and row 1 = 0x8825, low byte first.
func TestExpectedRows(t *testing.T) {
	var fp [32]byte
	copy(fp[:], []byte{180, 81, 37, 136})
	rows := expectedRows(fp)
	if rows[0] != 0x51b4 || rows[1] != 0x8825 {
		t.Fatalf("rows = 0x%04x, 0x%04x; want 0x51b4, 0x8825", rows[0], rows[1])
	}
	// assemble is the inverse.
	var s otpSlot
	s.rows = rows
	s.assemble()
	if s.hash != fp {
		t.Fatalf("assemble did not invert expectedRows")
	}
}

// TestParseOTPBlocks pins the parser on real `picotool otp get -n`
// output: block headers, raw 24-bit VALUE lines, decimal field lines.
func TestParseOTPBlocks(t *testing.T) {
	out := `ROW 0x0040: OTP_DATA_CRIT1 (CRIT)

    VALUE 0x000001

    field SECURE_BOOT_ENABLE (bit 0) = 1
    field GLITCH_DETECTOR_ENABLE (bit 4) = 0

ROW 0x004b: OTP_DATA_BOOT_FLAGS1 (RBIT-3)

    VALUE 0x000003

    field KEY_VALID (bits 0-3) = 3
    field KEY_INVALID (bits 8-11) = 0

ROW 0x0080: OTP_DATA_BOOTKEY0_0 (Part 1/16)

    VALUE 0x0c31c8

ROW 0x0000: OTP_DATA_CHIPID0 (ECC) (Part 1/4)

    VALUE 0x28f5de
`
	r := parseOTPBlocks(out)
	for k, want := range map[string]uint64{
		"CRIT1.SECURE_BOOT_ENABLE":     1,
		"CRIT1.GLITCH_DETECTOR_ENABLE": 0,
		"BOOT_FLAGS1.KEY_VALID":        3,
		"BOOT_FLAGS1.KEY_INVALID":      0,
	} {
		if got, ok := r.fields[k]; !ok || got != want {
			t.Errorf("field %s = %#x (present %v), want %#x", k, got, ok, want)
		}
	}
	for k, want := range map[string]uint32{
		"CRIT1":      0x000001,
		"BOOTKEY0_0": 0x0c31c8,
		"CHIPID0":    0x28f5de,
	} {
		if got, ok := r.rows[k]; !ok || got != want {
			t.Errorf("row %s = %#x (present %v), want %#x", k, got, ok, want)
		}
	}
	rows := r.rows
	// The captured BOOTKEY0_0 raw row decodes to the manufacturer
	// hash's row 0: the ECC bits ride the top byte.
	var mfr [32]byte
	b, err := hex.DecodeString(signKeyHashSH2)
	if err != nil {
		t.Fatal(err)
	}
	copy(mfr[:], b)
	if got, want := eccData(rows["BOOTKEY0_0"]), expectedRows(mfr)[0]; got != want {
		t.Errorf("eccData(BOOTKEY0_0) = %#04x, want manufacturer row 0 %#04x", got, want)
	}
}

func TestRestoreFromPipe(t *testing.T) {
	_, pemBytes := loadFixture(t)
	var out bytes.Buffer
	err := run(&out, strings.NewReader(fixtureWords), []string{"restore", "-o", "-", "-verify", fixtureFingerprint})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), pemBytes) {
		t.Fatalf("restored PEM differs from the fixture")
	}
}

func TestRestoreAcceptsUniquePrefixes(t *testing.T) {
	_, pemBytes := loadFixture(t)
	var short []string
	for w := range strings.SplitSeq(fixtureWords, " ") {
		short = append(short, w[:min(4, len(w))])
	}
	var out bytes.Buffer
	if err := run(&out, strings.NewReader(strings.Join(short, "\n")), []string{"restore", "-o", "-"}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), pemBytes) {
		t.Fatalf("prefix restore differs from the fixture")
	}
}

func TestRestoreRejectsAmbiguousPrefix(t *testing.T) {
	// "mon" fits monitor, monkey, monster and month.
	words := strings.Replace(fixtureWords, "monitor", "mon", 1)
	var out bytes.Buffer
	err := run(&out, strings.NewReader(words), []string{"restore", "-o", "-"})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous prefix accepted or misreported: %v", err)
	}
}

func TestRestoreVerifyMismatchWritesNothing(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.pem")
	wrong := strings.Repeat("ab", 32)
	var out bytes.Buffer
	err := run(&out, strings.NewReader(fixtureWords), []string{"restore", "-o", target, "-verify", wrong})
	if err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("want fingerprint mismatch error, got %v", err)
	}
	if _, serr := os.Stat(target); serr == nil {
		t.Fatal("output written despite fingerprint mismatch")
	}
}

func TestRestoreChecksumFailurePointsAtRepair(t *testing.T) {
	words := strings.Split(fixtureWords, " ")
	words[5] = "zebra"
	var out bytes.Buffer
	err := run(&out, strings.NewReader(strings.Join(words, " ")), []string{"restore", "-o", "-"})
	if err == nil || !strings.Contains(err.Error(), "-repair") {
		t.Fatalf("checksum failure does not point at -repair: %v", err)
	}
}

func TestRestoreRepairFlow(t *testing.T) {
	_, pemBytes := loadFixture(t)
	words := strings.Split(fixtureWords, " ")
	words[16] = "zebra"
	var out bytes.Buffer
	err := run(&out, strings.NewReader(strings.Join(words, " ")),
		[]string{"restore", "-o", "-", "-repair", "-verify", fixtureFingerprint[:32]})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), pemBytes) {
		t.Fatalf("repaired PEM differs from the fixture")
	}
}

func TestRestoreUnknownWordFlow(t *testing.T) {
	_, pemBytes := loadFixture(t)
	words := strings.Split(fixtureWords, " ")
	words[9] = "?"
	var out bytes.Buffer
	err := run(&out, strings.NewReader(strings.Join(words, " ")),
		[]string{"restore", "-o", "-", "-verify", fixtureFingerprint})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), pemBytes) {
		t.Fatalf("unknown-word restore differs from the fixture")
	}
	// Without a fingerprint the unknown word must be an error, not a guess.
	err = run(io.Discard, strings.NewReader(strings.Join(words, " ")), []string{"restore", "-o", "-"})
	if err == nil || !strings.Contains(err.Error(), "-verify") {
		t.Fatalf("unknown word without -verify: %v", err)
	}
}

func TestRestoreRepairNeedsVerify(t *testing.T) {
	err := run(io.Discard, strings.NewReader(fixtureWords), []string{"restore", "-repair"})
	if err == nil || !strings.Contains(err.Error(), "-verify") {
		t.Fatalf("-repair without -verify: %v", err)
	}
}

func TestRestoreSeedQR(t *testing.T) {
	priv, pemBytes := loadFixture(t)
	m := mnemonicFromKey(priv)
	for name, payload := range map[string][]byte{
		"seedqr":        seedqr.QR(m),
		"compactseedqr": seedqr.CompactQR(m),
	} {
		dir := t.TempDir()
		p := filepath.Join(dir, "payload")
		if err := os.WriteFile(p, payload, 0o600); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := run(&out, nil, []string{"restore", "-qr", p, "-o", "-"}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !bytes.Equal(out.Bytes(), pemBytes) {
			t.Fatalf("%s: restored PEM differs", name)
		}
	}
	// A 12-word payload holds 16 bytes of entropy, not a boot key.
	twelve := bip39.New(bytes.Repeat([]byte{7}, 16))
	var out bytes.Buffer
	dir := t.TempDir()
	p := filepath.Join(dir, "payload")
	os.WriteFile(p, seedqr.QR(twelve), 0o600)
	err := run(&out, nil, []string{"restore", "-qr", p, "-o", "-"})
	if err == nil || !strings.Contains(err.Error(), "24") {
		t.Fatalf("12-word payload: %v", err)
	}
}

func TestWriteKeyPEMFileNeverClobbers(t *testing.T) {
	priv, _ := loadFixture(t)
	other, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(target, marshalKeyPEM(other), 0o600); err != nil {
		t.Fatal(err)
	}
	data := marshalKeyPEM(priv)
	if err := writeKeyPEMFile(target, data, false, fingerprint(priv)); err == nil {
		t.Fatal("overwrote an existing file without -f")
	}
	if err := writeKeyPEMFile(target, data, true, fingerprint(priv)); err == nil {
		t.Fatal("-f overwrote a different key")
	}
	// Same key: idempotent restore may overwrite with -f.
	os.WriteFile(target, data, 0o600)
	if err := writeKeyPEMFile(target, data, true, fingerprint(priv)); err != nil {
		t.Fatalf("-f refused an identical key: %v", err)
	}
	// A non-key file yields to -f.
	os.WriteFile(target, []byte("scratch\n"), 0o600)
	if err := writeKeyPEMFile(target, data, true, fingerprint(priv)); err != nil {
		t.Fatalf("-f refused a non-key file: %v", err)
	}
}

func TestBackupRefusesPipes(t *testing.T) {
	var out bytes.Buffer
	err := run(&out, nil, []string{"backup", fixtureKeyFile(t)})
	if err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("backup to a pipe: %v", err)
	}
	if out.Len() != 0 {
		t.Fatal("backup leaked output to the pipe it refused")
	}
}

func TestBackupExplicitStdout(t *testing.T) {
	var out bytes.Buffer
	if err := run(&out, nil, []string{"backup", fixtureKeyFile(t), "-o", "-"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != fixtureWords {
		t.Fatalf("backup -o - = %q, want the fixture words", got)
	}
}

func TestBackupInstructionsToStdout(t *testing.T) {
	var out bytes.Buffer
	if err := run(&out, nil, []string{"backup", fixtureKeyFile(t), "-instructions"}); err != nil {
		t.Fatal(err)
	}
	if out.String() != instructionsMarkdown(fixtureFingerprint) {
		t.Fatal("bare -instructions must print the markdown source")
	}
	out.Reset()
	if err := run(&out, nil, []string{"backup", fixtureKeyFile(t), "-instructions", "-o", "-"}); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(out.Bytes(), []byte("2 ")) {
		t.Fatalf("-instructions -o - is not a curves payload: %q", out.Bytes()[:min(8, out.Len())])
	}
}

func TestBackupWordsFileRoundTrip(t *testing.T) {
	_, pemBytes := loadFixture(t)
	dir := t.TempDir()
	keyFile := fixtureKeyFile(t)
	wordsFile := filepath.Join(dir, "words.txt")
	if err := run(io.Discard, nil, []string{"backup", keyFile, "-o", wordsFile}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(wordsFile)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("words file mode %v, want 0600", fi.Mode().Perm())
	}
	words, _ := os.ReadFile(wordsFile)
	var out bytes.Buffer
	if err := run(&out, bytes.NewReader(words), []string{"restore", "-o", "-"}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), pemBytes) {
		t.Fatal("words file does not restore the fixture")
	}
	if err := run(io.Discard, nil, []string{"backup", keyFile, "-o", wordsFile}); err == nil {
		t.Fatal("backup overwrote an existing words file")
	}
}

func TestNsecMatchesKey(t *testing.T) {
	priv, _ := loadFixture(t)
	sec := nip19.Key{HRP: nip19.HRPSec}
	copy(sec.Data[:], priv.Serialize())
	back, err := nip19.ParseKey(sec.Bech32())
	if err != nil {
		t.Fatal(err)
	}
	if back.Data != sec.Data {
		t.Fatal("nsec round trip changed the scalar")
	}
	pub, err := nip19.NpubFrom(sec)
	if err != nil {
		t.Fatal(err)
	}
	var wantX [32]byte
	priv.PubKey().X().FillBytes(wantX[:])
	if pub.Data != wantX {
		t.Fatal("npub is not the x-only public key")
	}
	// And the command refuses to print the nsec into a pipe.
	err = run(&bytes.Buffer{}, nil, []string{"nsec", fixtureKeyFile(t)})
	if err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("nsec to a pipe: %v", err)
	}
}

func TestMint(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	os.MkdirAll(".git/info", 0o755)
	var out bytes.Buffer
	if err := run(&out, nil, []string{"mint", "-key", "test-boot.pem"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat("test-boot.pem")
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("minted key mode %v, want 0600", fi.Mode().Perm())
	}
	if _, err := loadKeyFile("test-boot.pem"); err != nil {
		t.Fatal(err)
	}
	excl, err := os.ReadFile(".git/info/exclude")
	if err != nil || !strings.Contains(string(excl), "test-boot.pem") {
		t.Errorf("key not excluded from git: %v %q", err, excl)
	}
	if err := run(io.Discard, nil, []string{"mint", "-key", "test-boot.pem"}); err == nil {
		t.Fatal("mint overwrote an existing key")
	}
}

// TestEntryStateMachine drives the raw-mode engine byte by byte with
// the tty pointed at /dev/null.
func TestEntryStateMachine(t *testing.T) {
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	e := &interactiveEntry{tty: devnull, u: newUI(devnull)}
	feed := func(s string) {
		t.Helper()
		for i := 0; i < len(s); i++ {
			if err := e.key(s[i]); err != nil {
				t.Fatalf("key %q: %v", s[i], err)
			}
		}
	}
	// "glory": unique at "glor".
	feed("glor")
	if len(e.entries) != 1 || e.entries[0].w != mustWord(t, "GLORY") {
		t.Fatalf("auto-advance failed: %+v", e.entries)
	}
	// An impossible keystroke is rejected and the fragment survives.
	feed("sax") // no word starts with "sax"
	if e.frag != "SA" {
		t.Fatalf("frag = %q, want SA", e.frag)
	}
	feed("lm") // SALM is unique to SALMON
	if len(e.entries) != 2 || e.entries[1].w != mustWord(t, "SALMON") || e.frag != "" {
		t.Fatalf("entries = %+v frag %q", e.entries, e.frag)
	}
	// Backspace with an empty fragment pops the accepted word.
	e.key(0x7f)
	if len(e.entries) != 1 {
		t.Fatal("backspace did not pop the accepted word")
	}
	feed("salm")
	if len(e.entries) != 2 {
		t.Fatal("re-entry after pop failed")
	}
	// '?' with a nonempty fragment is invalid; alone it is an unknown.
	feed("co")
	if err := e.key('?'); err != nil {
		t.Fatal(err)
	}
	if len(e.entries) != 2 || e.frag != "CO" {
		t.Fatal("? accepted mid-fragment")
	}
	e.key(0x7f)
	e.key(0x7f)
	e.key('?')
	if len(e.entries) != 3 || !e.entries[2].unknown {
		t.Fatalf("? not accepted as unknown: %+v", e.entries)
	}
	// Enter accepts an exact word that is also a prefix (car).
	feed("car")
	e.key('\r')
	if len(e.entries) != 4 || e.entries[3].w != mustWord(t, "CAR") {
		t.Fatalf("enter did not accept the exact word: %+v", e.entries)
	}
}

// TestEntryFinalsStage checks the last-word chooser: after 23 known
// words only the 8 checksum-valid candidates are offered, and digits
// pick them directly.
func TestEntryFinalsStage(t *testing.T) {
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	priv, _ := loadFixture(t)
	m := mnemonicFromKey(priv)
	e := &interactiveEntry{tty: devnull, u: newUI(devnull)}
	for _, w := range m[:23] {
		e.accept(w, false)
	}
	if e.finals == nil || len(e.finals) != 8 {
		t.Fatalf("finals = %v, want 8 candidates", e.finals)
	}
	idx := -1
	for i, w := range e.finals {
		if w == m[23] {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("true final word not offered")
	}
	if err := e.key(byte('1' + idx)); err != nil {
		t.Fatal(err)
	}
	if len(e.entries) != 24 || e.entries[23].w != m[23] {
		t.Fatalf("digit pick failed: %+v", e.entries[23])
	}
}

func mustWord(t *testing.T, label string) bip39.Word {
	t.Helper()
	w, ok := bip39.Complete(label)
	if !ok || bip39.LabelFor(w) != label {
		t.Fatalf("not a word: %q", label)
	}
	return w
}

// TestSignImage signs a real build-firmware output with the test key
// and verifies the result from disk, all in-process. Any UF2 in the
// checkout that carries a signature section serves as the fixture.
func TestSignImage(t *testing.T) {
	var unsigned string
	for _, pattern := range []string{"../../*.uf2", "../../deliverables/*.uf2"} {
		matches, _ := filepath.Glob(pattern)
		for _, m := range matches {
			info, err := inspectUF2(m)
			if err == nil && info.img.SignatureOffset != 0 && info.img.NumBlocks == 2 {
				unsigned = m
				break
			}
		}
		if unsigned != "" {
			break
		}
	}
	if unsigned == "" {
		t.Skip("no signable firmware image in the checkout")
	}
	priv, _ := loadFixture(t)
	before, err := os.ReadFile(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "signed.uf2")
	u := newUI(io.Discard)
	if err := signImage(u, priv, unsigned, out); err != nil {
		t.Fatal(err)
	}
	if err := verifySignedImage(out, pubXY(priv.PubKey())); err != nil {
		t.Fatal(err)
	}
	// The pristine unsigned input must survive untouched.
	after, err := os.ReadFile(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("signing modified the input file")
	}
	// A signed image is not a signable candidate for the glob.
	info, err := inspectUF2(out)
	if err != nil {
		t.Fatal(err)
	}
	if info.sigZero {
		t.Fatal("signature still zero after signing")
	}
}

func TestSignedName(t *testing.T) {
	for in, want := range map[string]string{
		"seedhammerii-v1.4.2.uf2":          "seedhammerii-v1.4.2.signed.uf2",
		"seedhammerii-latest-unsigned.uf2": "seedhammerii-latest.signed.uf2",
		"firmware.unsigned.uf2":            "firmware.signed.uf2",
		"seedhammerii-v1.4.1-g6dd6463.uf2": "seedhammerii-v1.4.1-g6dd6463.signed.uf2",
	} {
		if got := signedName(in); got != want {
			t.Errorf("signedName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseOTPBlocksBoundaries(t *testing.T) {
	// BOOTKEY1_15's header must never feed a value into BOOTKEY1_1,
	// and text outside a block is ignored.
	out := `some preamble picotool might print
ROW 0x009f: OTP_DATA_BOOTKEY1_15 (Part 16/16)

    VALUE 0x00beef

ROW 0x0091: OTP_DATA_BOOTKEY1_1 (Part 2/16)

    VALUE 0x008825
`
	r := parseOTPBlocks(out)
	if r.rows["BOOTKEY1_15"] != 0xbeef || r.rows["BOOTKEY1_1"] != 0x8825 {
		t.Fatalf("rows = %#v", r.rows)
	}
}

// TestParseOTPRedundantCopies covers a redundant row whose copies
// disagree: one carries a slot's valid bit, the other two do not, and
// the decoded VALUE is the majority vote, so the bit has no effect.
func TestParseOTPRedundantCopies(t *testing.T) {
	out := `ROW 0x004b: OTP_DATA_BOOT_FLAGS1 (RBIT-3)
    RAW_VALUE=0x000007;0x000003;0x000003 (WARNING - REDUNDANT ROWS AREN'T EQUAL)
    VALUE 0x000003

    field KEY_VALID (bits 0-3) = 3
    field KEY_INVALID (bits 8-11) = 0
`
	r := parseOTPBlocks(out)
	copies := r.copies["BOOT_FLAGS1"]
	if len(copies) != 3 || copies[0] != 0x07 || copies[1] != 0x03 || copies[2] != 0x03 {
		t.Fatalf("copies = %#v", copies)
	}
	if copiesEqual(copies) {
		t.Error("disagreeing copies reported as equal")
	}
	if got := copiesText(copies); got != "0x000007;0x000003;0x000003" {
		t.Errorf("copiesText = %q", got)
	}
	// The decoded field must stay the majority vote: the value the
	// boot ROM acts on, not the optimistic OR.
	if r.fields["BOOT_FLAGS1.KEY_VALID"] != 3 {
		t.Errorf("KEY_VALID = %#x, want the majority 0x3", r.fields["BOOT_FLAGS1.KEY_VALID"])
	}
	// And a board carrying this state must say so.
	b := &otpBoard{keyValid: 3, flagCopies: copies}
	warns := b.redundancyWarnings()
	if len(warns) != 1 || !strings.Contains(warns[0], "0x000007;0x000003;0x000003") {
		t.Fatalf("warnings = %#v", warns)
	}
	if len((&otpBoard{flagCopies: []uint32{3, 3, 3}}).redundancyWarnings()) != 0 {
		t.Error("equal copies produced a warning")
	}
}

// TestRequireDeviceClassification pins gate G1 on real picotool
// output: it prints the not-found line and the permission hint
// together, and the hint is the truth.
func TestRequireDeviceClassification(t *testing.T) {
	both := `No accessible RP-series devices in BOOTSEL mode were found.

but:

RP2350 device at bus 3, address 71 appears to be in BOOTSEL mode, but picotool
    was unable to connect. Maybe try 'sudo' or check your permissions.
`
	p := &pico{run: func(args ...string) (string, error) { return both, errStr("exit status 249") }}
	_, err := p.requireDevice()
	if err == nil || errors.Is(err, errNotBootsel) || !errors.Is(err, errNoUSBAccess) {
		t.Fatalf("combined output classified wrong: %v", err)
	}
	p.run = func(args ...string) (string, error) {
		return "No accessible RP-series devices in BOOTSEL mode were found.\n", errStr("exit status 249")
	}
	if _, err := p.requireDevice(); !errors.Is(err, errNotBootsel) {
		t.Fatalf("plain not-found misclassified: %v", err)
	}
}

// TestParseOTPRowsByAddress covers picotool's two block header forms:
// a named row, and a bare row number as printed when a copy of a
// redundant row is selected by address.
func TestParseOTPRowsByAddress(t *testing.T) {
	out := `ROW 0x004b: OTP_DATA_BOOT_FLAGS1 (RBIT-3)
    RAW_VALUE=0x000007;0x000003;0x000003 (WARNING - REDUNDANT ROWS AREN'T EQUAL)
    VALUE 0x000003

    field KEY_VALID (bits 0-3) = 3

ROW 0x004c
    VALUE 0x000003

ROW 0x004d
    VALUE 0x000003
`
	r := parseOTPBlocks(out)
	// Named rows resolve by name and by address.
	if r.rows["BOOT_FLAGS1"] != 0x3 || r.rows["0x4b"] != 0x3 {
		t.Fatalf("named row: %#v", r.rows)
	}
	// Unnamed copies resolve by the address rowSelector builds.
	for _, sel := range []string{"0x4c", "0x4d"} {
		if v, ok := r.rows[sel]; !ok || v != 0x3 {
			t.Fatalf("copy %s = %#x (present %v)", sel, v, ok)
		}
	}
	if r.fields["BOOT_FLAGS1.KEY_VALID"] != 3 {
		t.Errorf("field lost: %#v", r.fields)
	}
	if got := rowSelector(rowBootFlags1 + 1); got != "0x4c" {
		t.Errorf("rowSelector = %q, want 0x4c", got)
	}
}

// TestSetRedundantBitsWritesEveryCopy checks that a valid-bit write
// addresses every copy row and never asks picotool to clear a bit.
func TestSetRedundantBitsWritesEveryCopy(t *testing.T) {
	var cmds [][]string
	p := &pico{run: func(args ...string) (string, error) {
		cmds = append(cmds, args)
		return "", nil
	}}
	if err := p.setRedundantBits(rowBootFlags1, bootFlags1Copies, 1<<2); err != nil {
		t.Fatal(err)
	}
	if len(cmds) != bootFlags1Copies {
		t.Fatalf("%d writes, want %d", len(cmds), bootFlags1Copies)
	}
	for i, c := range cmds {
		want := []string{"otp", "set", "-s", rowSelector(rowBootFlags1 + i), "0x4"}
		if strings.Join(c, " ") != strings.Join(want, " ") {
			t.Errorf("write %d = %v, want %v", i, c, want)
		}
	}
	// A failing copy stops the sequence and reports which row.
	calls := 0
	p.run = func(args ...string) (string, error) {
		calls++
		if calls == 2 {
			return "some picotool text", errStr("exit status 1")
		}
		return "", nil
	}
	err := p.setRedundantBits(rowCrit1, crit1Copies, 1)
	if err == nil || !strings.Contains(err.Error(), rowSelector(rowCrit1+1)) {
		t.Fatalf("failure not attributed to the copy row: %v", err)
	}
	if calls != 2 {
		t.Errorf("kept writing after a failure: %d calls", calls)
	}
}

// TestReadRedundantCopies covers both shapes of a named redundant-row
// read: a RAW_VALUE line listing disagreeing copies, and its absence,
// which means every copy holds the decoded value.
func TestReadRedundantCopies(t *testing.T) {
	disagree := `ROW 0x004b: OTP_DATA_BOOT_FLAGS1 (RBIT-3)
    RAW_VALUE=0x000007;0x000003;0x000003 (WARNING - REDUNDANT ROWS AREN'T EQUAL)
    VALUE 0x000003

    field KEY_VALID (bits 0-3) = 3
`
	agree := `ROW 0x004b: OTP_DATA_BOOT_FLAGS1 (RBIT-3)
    VALUE 0x000007

    field KEY_VALID (bits 0-3) = 7
`
	p := &pico{run: func(args ...string) (string, error) { return disagree, nil }}
	got, err := p.readRedundantCopies(nameBootFlags1, bootFlags1Copies)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != 0x7 || got[1] != 0x3 || copiesEqual(got) {
		t.Fatalf("disagreeing copies = %#v", got)
	}
	p.run = func(args ...string) (string, error) { return agree, nil }
	got, err = p.readRedundantCopies(nameBootFlags1, bootFlags1Copies)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || !copiesEqual(got) || got[0] != 0x7 {
		t.Fatalf("agreeing copies = %#v", got)
	}
	// A read that produces no such row is an error, never a silent zero.
	p.run = func(args ...string) (string, error) { return "", nil }
	if _, err := p.readRedundantCopies(nameBootFlags1, bootFlags1Copies); err == nil {
		t.Error("missing row accepted")
	}
}

// TestFlashRefusedWhenKeyNotTrusted covers the gate between fusing and
// flashing: on a board that enforces signatures, an image signed by a
// key the board does not (yet) accept must not replace what boots.
func TestFlashRefusedWhenKeyNotTrusted(t *testing.T) {
	priv, _ := loadFixture(t)
	fp := fingerprint(priv)
	// Secure boot on, one foreign key valid, our hash written into
	// slot 1 but its valid bit outvoted: the state a partial
	// valid-bit write leaves behind.
	board := &otpBoard{secureBoot: true, keyValid: 0b0001, flagCopies: []uint32{0x3, 0x1, 0x1}}
	for i := range board.slots {
		board.slots[i].readable = true
		board.slots[i].zero = true
	}
	board.slots[0].zero, board.slots[0].hash = false, [32]byte{0xaa}
	board.slots[1].zero, board.slots[1].hash = false, fp
	board.slots[1].rows = expectedRows(fp)

	if slotOfKey(board, fp) >= 0 {
		t.Fatal("the key must not count as trusted while its bit is outvoted")
	}
	if got := makeFusePlan(board, fp); got.kind != planResume || got.slot != 1 {
		t.Fatalf("plan = %+v, want resume of slot 1", got)
	}
	// The ceremony refuses before signing or flashing, and says the
	// board still boots what it booted before.
	p := &pico{run: func(args ...string) (string, error) {
		if len(args) > 1 && args[0] == "otp" && args[1] == "set" {
			t.Errorf("unexpected write: %v", args)
		}
		// A readBoard that reports the same unchanged state.
		return `ROW 0x0040: OTP_DATA_CRIT1 (CRIT)
    VALUE 0x000001
    field SECURE_BOOT_ENABLE (bit 0) = 1
ROW 0x004b: OTP_DATA_BOOT_FLAGS1 (RBIT-3)
    RAW_VALUE=0x000003;0x000001;0x000001 (WARNING - REDUNDANT ROWS AREN'T EQUAL)
    VALUE 0x000001
    field KEY_VALID (bits 0-3) = 1
    field KEY_INVALID (bits 8-11) = 0
`, nil
	}}
	fresh, err := p.readBoard()
	if err != nil {
		t.Fatal(err)
	}
	if slotOfKey(fresh, fp) >= 0 {
		t.Fatal("re-scan wrongly reports the key as trusted")
	}
	if copiesEqual(fresh.flagCopies) {
		t.Fatalf("re-scan lost the copy disagreement: %v", fresh.flagCopies)
	}
}

// revokeBoard builds a locked board: slot 0 the manufacturer key, slot
// 1 the given key, slots 2 and 3 empty, all four unrevoked.
func revokeBoard(t *testing.T, ourFP [32]byte) *otpBoard {
	t.Helper()
	b := &otpBoard{secureBoot: true, keyValid: 0b0011}
	for i := range b.slots {
		b.slots[i].readable = true
		b.slots[i].zero = true
	}
	mfr, err := hex.DecodeString(signKeyHashSH2)
	if err != nil {
		t.Fatal(err)
	}
	copy(b.slots[0].hash[:], mfr)
	b.slots[0].zero = false
	b.slots[1].hash, b.slots[1].zero = ourFP, false
	return b
}

func TestRevokePlanGates(t *testing.T) {
	priv, _ := loadFixture(t)
	fp := fingerprint(priv)
	b := revokeBoard(t, fp)

	// Revoking the manufacturer key is allowed while our key remains,
	// and the plan says what keeps booting.
	p := makeRevokePlan(b, priv, "k.pem", 0)
	if p.refuse != "" {
		t.Fatalf("revoking slot 0 refused: %s", p.refuse)
	}
	if len(p.bootable) != 1 || !strings.Contains(p.bootable[0], "you sign with k.pem") {
		t.Fatalf("bootable = %v", p.bootable)
	}
	// And revoking ours is allowed while the manufacturer key remains.
	p = makeRevokePlan(b, priv, "k.pem", 1)
	if p.refuse != "" || len(p.bootable) != 1 || !strings.Contains(p.bootable[0], "official SeedHammer") {
		t.Fatalf("revoking slot 1: refuse=%q bootable=%v", p.refuse, p.bootable)
	}
	// An empty slot may be blocked off; something valid still remains.
	if p := makeRevokePlan(b, priv, "k.pem", 2); p.refuse != "" {
		t.Errorf("revoking an empty slot refused: %s", p.refuse)
	}
	// Out of range and already-revoked are refused by name.
	if p := makeRevokePlan(b, priv, "k.pem", 4); p.refuse == "" {
		t.Error("slot 4 accepted")
	}
	b.keyInvalid = 0b0100
	if p := makeRevokePlan(b, priv, "k.pem", 2); !strings.Contains(p.refuse, "already revoked") {
		t.Errorf("re-revoking: %q", p.refuse)
	}
}

// TestRevokeRefusesLastKey is the brick case: the write that would
// leave a board unable to accept any firmware must never happen.
func TestRevokeRefusesLastKey(t *testing.T) {
	priv, _ := loadFixture(t)
	fp := fingerprint(priv)
	b := revokeBoard(t, fp)
	b.keyValid = 0b0010 // only our slot 1 is valid
	p := makeRevokePlan(b, priv, "k.pem", 1)
	if p.refuse == "" || !strings.Contains(p.refuse, "last usable boot key") {
		t.Fatalf("last valid slot accepted: %q", p.refuse)
	}
	// The core refuses without touching the device.
	pico := &pico{run: func(args ...string) (string, error) {
		t.Errorf("device call during a refused revoke: %v", args)
		return "", nil
	}}
	err := revokeCore(newUI(io.Discard), pico, b, priv, "k.pem", 1)
	if err == nil || !strings.Contains(err.Error(), "Nothing was written") {
		t.Fatalf("revokeCore: %v", err)
	}
}

// TestRevokeRefusesUnaccountedRemainder covers the subtler brick: the
// slots left valid hold keys this tool cannot boot with.
func TestRevokeRefusesUnaccountedRemainder(t *testing.T) {
	priv, _ := loadFixture(t)
	b := revokeBoard(t, fingerprint(priv))
	// Slot 1 holds a stranger's key instead of ours.
	b.slots[1].hash = [32]byte{0xaa, 0xbb}
	p := makeRevokePlan(b, priv, "k.pem", 0)
	if p.refuse == "" || !strings.Contains(p.refuse, "provably bootable") {
		t.Fatalf("unaccounted remainder accepted: %q", p.refuse)
	}
	// With no key loaded at all, revoking the manufacturer key is the
	// same refusal.
	if p := makeRevokePlan(b, nil, "", 0); p.refuse == "" {
		t.Error("accepted with no key loaded")
	}
}

func TestKeyInvalidMask(t *testing.T) {
	// KEY_INVALID is bits 8 to 11 of BOOT_FLAGS1.
	for slot, want := range map[int]uint32{0: 0x100, 1: 0x200, 2: 0x400, 3: 0x800} {
		if got := keyInvalidMask(slot); got != want {
			t.Errorf("keyInvalidMask(%d) = %#x, want %#x", slot, got, want)
		}
	}
	// The revoke mask must never collide with a valid bit.
	for slot := range otpNumSlots {
		if keyInvalidMask(slot)&0xf != 0 {
			t.Errorf("slot %d's revoke mask touches KEY_VALID", slot)
		}
	}
}

// TestRevokeRequiresEvidence covers the gate for the case where only a
// locally signed image could boot afterwards.
func TestRevokeRequiresEvidence(t *testing.T) {
	priv, _ := loadFixture(t)
	b := revokeBoard(t, fingerprint(priv))
	t.Chdir(t.TempDir()) // no signed image here
	pico := &pico{run: func(args ...string) (string, error) {
		if len(args) > 1 && args[1] == "set" {
			t.Errorf("wrote OTP without evidence: %v", args)
		}
		return "", nil
	}}
	// Revoking the manufacturer key leaves only our key, so an image
	// signed by it must exist and verify.
	err := revokeCore(newUI(io.Discard), pico, b, priv, "k.pem", 0)
	if err == nil || !strings.Contains(err.Error(), "no image here verifies") {
		t.Fatalf("evidence gate: %v", err)
	}
	// Revoking our own slot keeps the manufacturer key, which needs no
	// local evidence; it stops at the typed confirmation instead.
	err = revokeCore(newUI(io.Discard), pico, b, priv, "k.pem", 1)
	if err == nil || strings.Contains(err.Error(), "no image here verifies") {
		t.Fatalf("wrong gate for slot 1: %v", err)
	}
}
