package main

import (
	"os"
	"path/filepath"
	"testing"
)

// pythonForNFC must prefer the installer's venv exactly when its
// python exists and is executable, and fall back to the PATH name
// otherwise, so `-nfc` works right after ./install.sh send with no
// activation and keeps working on hosts that installed nfcpy by hand.
func TestPythonForNFC(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := pythonForNFC(); got != "python3" {
		t.Fatalf("no venv: got %q, want python3", got)
	}
	bin := filepath.Join(home, ".nfc-venv", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	py := filepath.Join(bin, "python3")
	if err := os.WriteFile(py, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := pythonForNFC(); got != py {
		t.Fatalf("with venv: got %q, want %q", got, py)
	}
	if err := os.Chmod(py, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := pythonForNFC(); got != "python3" {
		t.Fatalf("venv python not executable: got %q, want python3", got)
	}
}
