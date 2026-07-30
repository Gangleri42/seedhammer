package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// The firmware build is owned by flake.nix: the pinned tinygo, the
// dummy-seal that creates the signature structure, and the picosign
// clear. Reimplementing that pipeline here would fork it, so build is
// a thin wrapper: locate the checkout, require nix, stream the run.
// The output, seedhammerii-<version>.uf2 with the flake's default
// version (git describe --tags --always --dirty), lands in the
// checkout root, where the sign and flash pickers already look.

// findModuleRoot walks up from the working directory to a seedhammer
// checkout, else falls back to the source location of this file,
// which covers `go run seedhammer.com/cmd/sh2key` from anywhere on
// the machine that built it.
func findModuleRoot() (string, error) {
	if dir, err := os.Getwd(); err == nil {
		for {
			if isSeedhammerModule(dir) {
				return dir, nil
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	if _, src, _, ok := runtime.Caller(0); ok {
		dir := filepath.Join(filepath.Dir(src), "..", "..")
		if isSeedhammerModule(dir) {
			return dir, nil
		}
	}
	return "", errors.New("no seedhammer checkout found; run from inside one")
}

// buildGateReason is why build cannot run right now, empty when it
// can. Computed once for the TUI's action list; the CLI path lets
// runBuild produce the richer refusals itself.
func buildGateReason() string {
	root, err := findModuleRoot()
	if err != nil {
		return "no seedhammer checkout found"
	}
	if _, err := os.Stat(filepath.Join(root, "flake.nix")); err != nil {
		return "checkout has no flake.nix"
	}
	if _, err := exec.LookPath("nix"); err != nil {
		return "nix missing: install from nixos.org"
	}
	return ""
}

func cmdBuild(stdout io.Writer, args []string) error {
	fs := newFlagSet("build", "build")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("build takes no arguments, got %q", fs.Arg(0))
	}
	return runBuild(newUI(stdout))
}

// runBuild is shared by the subcommand and the TUI, which suspends to
// the real terminal for it: nix narrates downloads and tinygo takes
// its time, and that stream belongs on a scrolling screen.
func runBuild(u *ui) error {
	root, err := findModuleRoot()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(root, "flake.nix")); err != nil {
		return fmt.Errorf("%s has no flake.nix; the firmware build needs the repo's flake", root)
	}
	if _, err := exec.LookPath("nix"); err != nil {
		return errors.New(`nix not found in PATH; flake.nix pins the whole firmware toolchain.
  https://nixos.org/download    install it with flakes enabled, then rerun.
  Everything else in sh2key works without nix.`)
	}
	u.printf("  building from %s at its current tip, uncommitted changes included\n", root)
	u.printf("  %s\n\n", u.dim("$ nix run .#build-firmware    (a cold nix store first downloads the toolchain)"))
	cmd := exec.Command("nix", "run", ".#build-firmware")
	cmd.Dir = root
	cmd.Stdin = os.Stdin
	cmd.Stdout = u.w
	cmd.Stderr = u.w
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build-firmware: %v", err)
	}
	u.printf("\n  the image is unsigned and a locked board will not boot it as-is;\n")
	u.printf("  next: sign it, then flash, or let provision run the whole ceremony\n")
	return nil
}
