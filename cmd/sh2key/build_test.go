package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// build takes no positionals, and the refusal must fire before any
// checkout walk, nix lookup, or child process.
func TestCmdBuildRejectsArgs(t *testing.T) {
	err := cmdBuild(io.Discard, []string{"extra"})
	if err == nil || !strings.Contains(err.Error(), "takes no arguments") {
		t.Fatalf("err = %v", err)
	}
}

// Wherever the tests run, findModuleRoot's source-location fallback
// pins the checkout that built them: the root it names must be the
// seedhammer module with this command inside it.
func TestFindModuleRoot(t *testing.T) {
	root, err := findModuleRoot()
	if err != nil {
		t.Fatal(err)
	}
	if !isSeedhammerModule(root) {
		t.Fatalf("root %q is not the seedhammer module", root)
	}
	if _, err := os.Stat(filepath.Join(root, "cmd", "sh2key")); err != nil {
		t.Fatal(err)
	}
}
