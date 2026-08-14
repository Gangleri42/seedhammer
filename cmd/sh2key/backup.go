package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"seedhammer.com/bip39"
)

func cmdBackup(stdout io.Writer, args []string) error {
	fs := newFlagSet("backup", "backup <key.pem> [-o file|-] [-instructions] [-nfc]")
	outPath := fs.String("o", "", "write to `file` instead of printing (words: 0600, never clobbers; - for stdout)")
	instructions := fs.Bool("instructions", false, "emit the plate-2 restore instructions instead of the words (no secret)")
	nfc := fs.Bool("nfc", false, "send to the engraver over NFC")
	lead, rest := popArg(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	keyPath, err := oneArg(fs, lead)
	if err != nil {
		return err
	}
	if keyPath == "" {
		return errors.New("backup: name the key file, e.g. sh2key backup sh2-bootkey.pem")
	}
	priv, err := loadKeyFile(keyPath)
	if err != nil {
		return err
	}
	if *instructions {
		return emitInstructions(stdout, priv, *outPath, *nfc)
	}
	return emitWords(stdout, keyPath, priv, *outPath, *nfc)
}

// emitWords delivers the mnemonic. The words render to a terminal or
// an explicitly named sink only: with stdout redirected and no -o or
// -nfc, backup refuses, so a stray pipe or tee cannot capture the key.
func emitWords(stdout io.Writer, keyPath string, priv *secp256k1.PrivateKey, outPath string, nfc bool) error {
	m := mnemonicFromKey(priv)
	line := m.String() + "\n"
	u := newUI(os.Stderr)
	delivered := false
	switch outPath {
	case "":
	case "-":
		if _, err := io.WriteString(stdout, line); err != nil {
			return err
		}
		delivered = true
	default:
		if err := writeSecretFile(outPath, []byte(line)); err != nil {
			return err
		}
		u.printf("  wrote %s\n", u.bold(outPath))
		delivered = true
	}
	if nfc {
		u.printf("  sending the words to the engraver; the device offers the seed-plate flow\n")
		if err := sendNFC(u, nfcRaw, []byte(line)); err != nil {
			return err
		}
		delivered = true
	}
	if delivered {
		u.printf("  fingerprint  %s\n", u.bold(fingerprintHex(priv)[:16])+u.dim(fingerprintHex(priv)[16:]))
		return nil
	}
	if !isTTYWriter(stdout) {
		return errors.New("stdout is not a terminal, the words would be captured by the pipe; use -o <file>, -o -, or -nfc")
	}
	printWordsScreen(newUI(stdout), keyPath, m, priv)
	return nil
}

func printWordsScreen(u *ui, keyPath string, m bip39.Mnemonic, priv *secp256k1.PrivateKey) {
	u.printf("\n  %s  ->  24 words\n\n", u.bold(keyPath))
	for _, l := range wordsGridLines(u, m) {
		u.printf("%s\n", l)
	}
	fp := fingerprintHex(priv)
	u.printf("\n  fingerprint  %s\n", u.bold(fp[:16])+u.dim(fp[16:]))
	u.printf("  %s\n", u.dim("plate 2 carries the bold prefix; it is public and proves a restore"))
	u.printf("\n  %s\n", u.warn("these words ARE the private key: no passphrase, no BIP32, not a wallet seed"))
	u.printf("  %s\n\n", u.dim("engrave:  sh2key backup "+keyPath+" -nfc      instructions plate:  sh2key backup "+keyPath+" -instructions -nfc"))
}

// wordsGridLines lays a mnemonic out as the numbered four-column
// grid shared by the backup subcommand and the TUI.
func wordsGridLines(u *ui, m bip39.Mnemonic) []string {
	var out []string
	for i := 0; i < len(m); i += 4 {
		var row strings.Builder
		row.WriteString(" ")
		for j := i; j < min(i+4, len(m)); j++ {
			fmt.Fprintf(&row, " %s %-8s ", u.dim(fmt.Sprintf("%2d", j+1)), strings.ToLower(bip39.LabelFor(m[j])))
		}
		out = append(out, row.String())
	}
	return out
}

// emitInstructions delivers the plate-2 restore document as a
// rich-text curves payload, rendered and validated by the same
// packages cmd/svgplate uses: what passes here is what the machine
// engraves. Bare invocation prints the markdown source; -o and -nfc
// carry the payload.
func emitInstructions(stdout io.Writer, priv *secp256k1.PrivateKey, outPath string, nfc bool) error {
	src := instructionsMarkdown(fingerprintHex(priv))
	payload, err := instructionsPayload(src)
	if err != nil {
		return err
	}
	u := newUI(os.Stderr)
	delivered := false
	switch outPath {
	case "":
	case "-":
		if _, err := stdout.Write(payload); err != nil {
			return err
		}
		delivered = true
	default:
		// The instructions are public and regenerable at any time;
		// unlike key material they may be overwritten freely. The one
		// exception is the key itself: -o aimed back at the PEM must
		// not replace the thing being backed up with its own manual.
		if err := refuseKeyPEMTarget(outPath); err != nil {
			return err
		}
		if err := os.WriteFile(outPath, payload, 0o644); err != nil {
			return err
		}
		u.printf("  wrote %s %s\n", u.bold(outPath),
			u.dim(fmt.Sprintf("(%d-byte curves payload, rich text at %gmm; send: write-nfc.py --curves)", len(payload), instructionsBodyMM)))
		delivered = true
	}
	if nfc {
		u.printf("  sending the instructions plate; the device previews before engraving\n")
		if err := sendNFC(u, nfcCurves, payload); err != nil {
			return err
		}
		delivered = true
	}
	if !delivered {
		if _, err := io.WriteString(stdout, src); err != nil {
			return err
		}
		u.printf("  %s\n", u.dim(fmt.Sprintf("markdown source; the rendered payload is %d bytes (-o file, -nfc to engrave)", len(payload))))
	}
	return nil
}

// writeSecretFile creates a 0600 file that must not already exist.
func writeSecretFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%s exists; refusing to overwrite (pick another name)", path)
		}
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func cmdMint(stdout io.Writer, args []string) error {
	fs := newFlagSet("mint", "mint [-key file]")
	keyPath := fs.String("key", defaultKeyPath, "where to write the new key `file`")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("mint takes no arguments, got %q", fs.Arg(0))
	}
	priv, err := mintKey(*keyPath)
	if err != nil {
		return err
	}
	u := newUI(stdout)
	fp := fingerprintHex(priv)
	u.printf("  minted %s %s\n", u.bold(*keyPath), u.dim("(mode 0600)"))
	u.printf("  fingerprint  %s\n", u.bold(fp[:16])+u.dim(fp[16:]))
	if excl, err := gitExclude(*keyPath); err == nil && excl != "" {
		u.printf("  %s\n", u.dim("added "+*keyPath+" to "+excl))
	}
	// The steel backup is engraved by the machine, and the machine
	// engraves only once it runs firmware this key signed; the
	// backup hint comes after provision, where it is actionable.
	u.printf("\n  %s\n", u.dim("next:  sh2key provision   (fuse the key, sign, flash - then the machine can engrave its backup)"))
	return nil
}

// mintKey generates a fresh key and writes it where the ceremony
// expects it, refusing to touch an existing file.
func mintKey(path string) (*secp256k1.PrivateKey, error) {
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("%s already exists; refusing to mint over a key file", path)
	}
	priv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, err
	}
	if err := writeSecretFile(path, marshalKeyPEM(priv)); err != nil {
		return nil, err
	}
	return priv, nil
}

// gitExclude appends the key file to .git/info/exclude when inside a
// git checkout, so a world-readable secret never lands in a commit by
// accident. Best-effort: an error only means the note is skipped.
func gitExclude(name string) (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		gitPath := filepath.Join(dir, ".git")
		if fi, err := os.Stat(gitPath); err == nil {
			infoDir := filepath.Join(gitPath, "info")
			if !fi.IsDir() {
				// A worktree: .git is a "gitdir: <path>" file.
				b, err := os.ReadFile(gitPath)
				if err != nil {
					return "", err
				}
				gitdir := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(b)), "gitdir:"))
				if !filepath.IsAbs(gitdir) {
					gitdir = filepath.Join(dir, gitdir)
				}
				infoDir = filepath.Join(gitdir, "info")
			}
			exclude := filepath.Join(infoDir, "exclude")
			existing, _ := os.ReadFile(exclude)
			for l := range strings.Lines(string(existing)) {
				if strings.TrimSpace(l) == name {
					return exclude, nil
				}
			}
			if err := os.MkdirAll(infoDir, 0o755); err != nil {
				return "", err
			}
			f, err := os.OpenFile(exclude, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
			if err != nil {
				return "", err
			}
			defer f.Close()
			if len(existing) > 0 && existing[len(existing)-1] != '\n' {
				io.WriteString(f, "\n")
			}
			if _, err := io.WriteString(f, name+"\n"); err != nil {
				return "", err
			}
			return exclude, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}
