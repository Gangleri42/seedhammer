package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"seedhammer.com/bip39"
	"seedhammer.com/seedqr"
)

func cmdRestore(stdout io.Writer, stdin io.Reader, args []string) error {
	fs := newFlagSet("restore", "restore [-qr file|-] [-o file|-] [-f] [-verify hex] [-repair] [-repair2]")
	qrPath := fs.String("qr", "", "read a SeedQR or CompactSeedQR payload from `file` (- for stdin) instead of typing")
	outPath := fs.String("o", "", "write the PEM to `file` (0600, never clobbers a key; - for stdout)")
	force := fs.Bool("f", false, "allow overwriting -o targets that are not a different key")
	verify := fs.String("verify", "", "require this public key `fingerprint` (hex, from plate 2); refuse on mismatch")
	repair := fs.Bool("repair", false, "find a single mis-read word by fingerprint search (needs -verify)")
	repair2 := fs.Bool("repair2", false, "find two mis-read words; minutes, not seconds (needs -verify)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("restore takes no arguments, got %q", fs.Arg(0))
	}
	var want []byte
	if *verify != "" {
		w, err := parseFingerprint(*verify)
		if err != nil {
			return err
		}
		want = w
	}
	if (*repair || *repair2) && want == nil {
		return errors.New("-repair needs -verify <fingerprint>: without it the search returns ~190 candidate keys with no way to choose (the fingerprint is on plate 2)")
	}

	// Progress and verdicts go to stderr; stdout stays clean for the PEM.
	u := newUI(os.Stderr)

	var entries []wordEntry
	switch {
	case *qrPath != "":
		m, err := mnemonicFromQRPayload(*qrPath, stdin)
		if err != nil {
			return err
		}
		for _, w := range m {
			entries = append(entries, wordEntry{w: w})
		}
	default:
		var err error
		entries, err = collectWords(stdin)
		if err != nil {
			return err
		}
	}

	words := make([]bip39.Word, 24)
	var unknown []int
	for i, en := range entries {
		if en.unknown {
			unknown = append(unknown, i)
			words[i] = -1
		} else {
			words[i] = en.w
		}
	}

	m, err := resolveMnemonic(u, words, unknown, want, *repair, *repair2)
	if err != nil {
		return err
	}
	priv, err := keyFromMnemonic(m)
	if err != nil {
		return err
	}
	fp := fingerprint(priv)
	fpHex := fingerprintHex(priv)
	if want != nil {
		if !fingerprintMatches(want, fp) {
			return fmt.Errorf("fingerprint mismatch, nothing written:\n  entered   %s\n  expected  %x", fpHex, want)
		}
		u.printf("  fingerprint  %s  %s\n", u.bold(fpHex[:16])+u.dim(fpHex[16:]), u.tick()+u.good(" matches"))
	} else {
		u.printf("  fingerprint  %s\n", u.bold(fpHex[:16])+u.dim(fpHex[16:]))
	}
	return emitPEM(stdout, u, *outPath, *force, priv)
}

// collectWords picks the entry mode: raw-mode interactive on a
// terminal, prompted lines on TERM=dumb, plain tokens from a pipe.
func collectWords(stdin io.Reader) ([]wordEntry, error) {
	f, ok := stdin.(*os.File)
	if !ok || !isTerminal(f) {
		return readMnemonicTokens(stdin)
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		// No controlling terminal; degrade to line entry on stdin.
		return promptedEntry(os.Stderr, stdin)
	}
	defer tty.Close()
	return enterMnemonic(tty)
}

// resolveMnemonic turns the raw entry into a checksum-valid mnemonic,
// running the unknown-word and repair searches as permitted.
func resolveMnemonic(u *ui, words []bip39.Word, unknown []int, want []byte, repair, repair2 bool) (bip39.Mnemonic, error) {
	switch len(unknown) {
	case 0:
	case 1:
		if want == nil {
			return nil, errors.New("one word is unknown; recovering it needs -verify <fingerprint> from plate 2")
		}
		sols := searchOne(words, unknown, want)
		return applySolutions(u, words, sols, "no word at position %d completes to the fingerprint; another word is off too, or the fingerprint is wrong", unknown)
	case 2:
		if want == nil {
			return nil, errors.New("two words are unknown; recovering them needs -verify <fingerprint> from plate 2")
		}
		u.printf("  %s\n", u.dim("searching two unknown words against the fingerprint..."))
		sols := searchTwo(words, [][2]int{{unknown[0], unknown[1]}}, want, nil)
		return applySolutions(u, words, sols, "no word pair at positions %d completes to the fingerprint", unknown)
	default:
		return nil, fmt.Errorf("%d unknown words; at most %d are recoverable", len(unknown), maxUnknown)
	}

	m := bip39.Mnemonic(words)
	checksumOK := m.Valid()
	fingerprintOK := true
	if checksumOK && want != nil {
		if priv, err := keyFromMnemonic(m); err == nil {
			fingerprintOK = fingerprintMatches(want, fingerprint(priv))
		}
	}
	if checksumOK && fingerprintOK {
		return m, nil
	}
	reason := "the checksum fails"
	if checksumOK {
		reason = "the fingerprint does not match"
	}
	switch {
	case repair2:
		u.printf("  %s\n", u.dim(reason+"; trying single-word repair..."))
		if sols := searchOne(words, positions24(), want); len(sols) > 0 {
			return applySolutions(u, words, sols, "", nil)
		}
		u.printf("  %s\n", u.dim("no single-word repair; searching all word pairs (this takes minutes)..."))
		progress := func(done, total int) {
			u.printf("\r  %s", u.dim(fmt.Sprintf("searched %d of %d position pairs", done, total)))
		}
		sols := searchTwo(words, allPairs(), want, progress)
		u.printf("\n")
		return applySolutions(u, words, sols, "no one- or two-word substitution matches the fingerprint; re-read the plate against the fingerprint on plate 2", nil)
	case repair:
		u.printf("  %s\n", u.dim(reason+"; searching single-word substitutions..."))
		sols := searchOne(words, positions24(), want)
		return applySolutions(u, words, sols, "no single-word substitution matches the fingerprint; two wrong words is a longer search: rerun with -repair2", nil)
	case !checksumOK:
		return nil, errors.New("checksum invalid: one word is likely mis-read; rerun with -repair -verify <fingerprint> (the fingerprint is on plate 2)")
	default:
		priv, _ := keyFromMnemonic(m)
		return nil, fmt.Errorf("fingerprint mismatch, nothing written:\n  entered   %s\n  expected  %x\nif one word was mis-read, rerun with -repair", fingerprintHex(priv), want)
	}
}

func positions24() []int {
	p := make([]int, 24)
	for i := range p {
		p[i] = i
	}
	return p
}

// applySolutions demands exactly one solution: zero is a failed
// search, several means the fingerprint prefix is too short to
// decide, and guessing is worse than an error.
func applySolutions(u *ui, words []bip39.Word, sols [][]repairHit, emptyMsg string, emptyArgs []int) (bip39.Mnemonic, error) {
	switch len(sols) {
	case 0:
		args := make([]any, 0, len(emptyArgs))
		for _, a := range emptyArgs {
			args = append(args, a+1)
		}
		return nil, fmt.Errorf(emptyMsg, args...)
	case 1:
	default:
		var b strings.Builder
		fmt.Fprintf(&b, "%d different repairs match the fingerprint; pass the full 64-digit fingerprint to decide:", len(sols))
		for _, sol := range sols {
			b.WriteString("\n ")
			for _, h := range sol {
				fmt.Fprintf(&b, " word %d: %s -> %s", h.pos+1, wordLabelOrQ(h.from), strings.ToLower(bip39.LabelFor(h.to)))
			}
		}
		return nil, errors.New(b.String())
	}
	m := make(bip39.Mnemonic, 24)
	copy(m, words)
	for _, h := range sols[0] {
		m[h.pos] = h.to
		u.printf("  word %d: %s -> %s\n", h.pos+1, u.bad(wordLabelOrQ(h.from)), u.good(strings.ToLower(bip39.LabelFor(h.to))))
	}
	if !m.Valid() {
		return nil, errors.New("internal error: repaired mnemonic fails its checksum")
	}
	return m, nil
}

func wordLabelOrQ(w bip39.Word) string {
	if w < 0 {
		return "?"
	}
	return strings.ToLower(bip39.LabelFor(w))
}

// mnemonicFromQRPayload decodes a SeedQR (24 groups of 4 digits) or
// CompactSeedQR (the raw 32 entropy bytes) payload. Any QR reader
// produces the payload; this consumes it, since the repo deliberately
// has no image decoder.
func mnemonicFromQRPayload(path string, stdin io.Reader) (bip39.Mnemonic, error) {
	var raw []byte
	var err error
	if path == "-" {
		raw, err = io.ReadAll(stdin)
	} else {
		raw, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, err
	}
	// Try the exact payload first: CompactSeedQR is raw bytes and may
	// legitimately end in 0x0a. Only then retry trimmed, for digit
	// payloads saved with a trailing newline.
	m, ok := seedqr.Parse(raw)
	if !ok {
		m, ok = seedqr.Parse(bytes.TrimSpace(raw))
	}
	if !ok {
		return nil, fmt.Errorf("%s: payload parses as neither SeedQR (24 groups of 4 digits) nor CompactSeedQR (32 raw bytes)", path)
	}
	if len(m) != 24 {
		return nil, fmt.Errorf("%s: payload decodes to %d words; the boot key backup is 24", path, len(m))
	}
	return m, nil
}

// emitPEM delivers the restored key: to -o, to a pipe, or - when
// stdout is a terminal - nowhere, because printing a private key into
// scrollback serves nobody. The bare form is the plate-verification
// mode of the howto.
func emitPEM(stdout io.Writer, u *ui, outPath string, force bool, priv *secp256k1.PrivateKey) error {
	pemBytes := marshalKeyPEM(priv)
	switch outPath {
	case "":
		if isTTYWriter(stdout) {
			u.printf("  %s\n", u.dim("PEM not written; pass -o <file> to write it"))
			return nil
		}
		_, err := stdout.Write(pemBytes)
		return err
	case "-":
		_, err := stdout.Write(pemBytes)
		return err
	default:
		if err := writeKeyPEMFile(outPath, pemBytes, force, fingerprint(priv)); err != nil {
			return err
		}
		u.printf("  wrote %s\n", u.bold(outPath))
		return nil
	}
}

// writeKeyPEMFile never clobbers: O_EXCL and 0600. -f relaxes that
// for a readable regular file that is not a key or holds this same
// key; a target holding a different key is refused even then, because
// overwriting it would destroy the thing being backed up, and so is a
// target that cannot be read or is not a regular file, because a
// guard that cannot look has to assume the worst. Replacing goes
// through a fresh 0600 file in the target's directory and a rename,
// so the key is on disk under its name at every instant and a
// looser mode on the old file (say 0644) never survives onto restored
// key material. A symlink is followed: the key stays where the link
// points, and the link stays a link.
func writeKeyPEMFile(path string, data []byte, force bool, fp [32]byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err == nil {
		if _, err := f.Write(data); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	}
	if !errors.Is(err, fs.ErrExist) {
		return err
	}
	if !force {
		return fmt.Errorf("%s exists; refusing to overwrite a key file (rerun with -f to allow replacing an identical key or a non-key file)", path)
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	fi, err := os.Stat(target)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("refusing even with -f: %s is not a regular file", path)
	}
	existing, err := os.ReadFile(target)
	if err != nil {
		return fmt.Errorf("refusing even with -f: %s exists but cannot be read, so it may hold a different key: %w", path, err)
	}
	if old, perr := parseKeyPEM(existing); perr == nil {
		if fingerprint(old) != fp {
			return fmt.Errorf("refusing even with -f: %s holds a different key (fingerprint %s); overwriting it would destroy the thing being backed up", path, fingerprintHex(old)[:16])
		}
	}
	return replaceFile(target, data)
}

// replaceFile swaps a fresh 0600 file holding data in for target by
// rename, so target is never absent and never half-written, and the
// old file's mode is not inherited. The temporary file lives in
// target's directory, which is what makes the rename atomic.
func replaceFile(target string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, target); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
