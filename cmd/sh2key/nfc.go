package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// The machine's only input channel is NFC, and the repo's canonical
// host-side writer is cmd/textplate/write-nfc.py (nfcpy over a USB
// reader). sh2key pipes payloads to it instead of growing a second
// NFC stack: one transport, shared with the howtos and the scripts.

type nfcMode int

const (
	// nfcRaw sends the payload as-is; the firmware's scanner decides
	// what it is (seed words, nsec, descriptor, free text).
	nfcRaw nfcMode = iota
	// nfcPlate validates the payload against the text-plate grid and
	// reports the engraving size before writing.
	nfcPlate
)

// pythonForNFC picks the interpreter for write-nfc.py: the installer's
// venv when it exists (that is where ./install.sh send pins the nfcpy
// stack), else whatever python3 the PATH offers. Either way, nothing
// needs activating.
func pythonForNFC() string {
	if home, err := os.UserHomeDir(); err == nil {
		venv := filepath.Join(home, ".nfc-venv", "bin", "python3")
		if fi, err := os.Stat(venv); err == nil && !fi.IsDir() && fi.Mode().Perm()&0o111 != 0 {
			return venv
		}
	}
	return "python3"
}

func sendNFC(u *ui, mode nfcMode, payload []byte) error {
	script, err := findWriteNFC()
	if err != nil {
		return err
	}
	args := []string{script}
	if mode == nfcRaw {
		args = append(args, "--raw")
	}
	args = append(args, "-")
	cmd := exec.Command(pythonForNFC(), args...)
	cmd.Stdin = bytes.NewReader(payload)
	// The script narrates ("hold a tag...") on stderr; route it where
	// this ui renders, which is the pane inside the TUI.
	cmd.Stdout = u.w
	cmd.Stderr = u.w
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return errors.New("python3 not found; './install.sh send' creates ~/.nfc-venv with the nfcpy stack, or install python3 with nfcpy and ndeflib")
		}
		return fmt.Errorf("write-nfc.py: %v (it needs a USB NFC reader and the nfcpy stack; './install.sh send' provides the stack in ~/.nfc-venv)", err)
	}
	return nil
}

// findWriteNFC locates cmd/textplate/write-nfc.py: from the module
// checkout the working directory is in, else relative to this source
// file, which covers `go run seedhammer.com/cmd/sh2key` from anywhere
// on the machine that built it.
func findWriteNFC() (string, error) {
	const rel = "cmd/textplate/write-nfc.py"
	if dir, err := os.Getwd(); err == nil {
		for {
			if isSeedhammerModule(dir) {
				p := filepath.Join(dir, rel)
				if _, err := os.Stat(p); err == nil {
					return p, nil
				}
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	if _, src, _, ok := runtime.Caller(0); ok {
		p := filepath.Join(filepath.Dir(src), "..", "..", "cmd", "textplate", "write-nfc.py")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", errors.New("cannot find cmd/textplate/write-nfc.py; run from inside the seedhammer checkout")
}

func isSeedhammerModule(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return false
	}
	for l := range strings.Lines(string(b)) {
		if strings.HasPrefix(strings.TrimSpace(l), "module ") {
			return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "module")) == "seedhammer.com"
		}
	}
	return false
}
