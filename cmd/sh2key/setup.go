package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// setup-udev installs the one piece of host configuration the device
// ceremony needs on Linux: a udev rule granting the user access to
// Raspberry Pi USB devices, so picotool works without sudo. The rule
// content lives here as a constant, so the tool, the manual and the
// installed file cannot drift apart.
//
// Elevation is by consent and by exec: the tool shows the exact file
// and the exact commands first, then runs sudo, which owns the
// password prompt on the terminal. The password never touches this
// process.

const udevRulesPath = "/etc/udev/rules.d/99-picotool.rules"

const udevRules = `# Raspberry Pi RP2040/RP2350 (BOOTSEL and picoboot modes) accessible
# to picotool without sudo. uaccess grants the seated user; plugdev is
# the fallback for non-seat sessions. Installed by 'sh2key setup-udev'.
SUBSYSTEM=="usb", ATTRS{idVendor}=="2e8a", TAG+="uaccess", MODE="0660", GROUP="plugdev"
`

// udevSetupCommands are the exact commands setup-udev runs, in order.
// The trigger is scoped to the Raspberry Pi vendor id so nothing else
// on the system is poked.
func udevSetupCommands(tmpFile string) [][]string {
	return [][]string{
		{"sudo", "install", "-m", "0644", tmpFile, udevRulesPath},
		{"sudo", "udevadm", "control", "--reload-rules"},
		{"sudo", "udevadm", "trigger", "--subsystem-match=usb", "--attr-match=idVendor=2e8a"},
	}
}

type udevState int

const (
	udevMissing udevState = iota
	udevDiffers
	udevCurrent
)

func udevRulesState(path string) udevState {
	existing, err := os.ReadFile(path)
	switch {
	case err != nil:
		return udevMissing
	case string(existing) == udevRules:
		return udevCurrent
	default:
		return udevDiffers
	}
}

func cmdSetupUdev(stdout io.Writer, args []string) error {
	fs := newFlagSet("setup-udev", "setup-udev")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("setup-udev takes no arguments, got %q", fs.Arg(0))
	}
	return runSetupUdev(newUI(stdout))
}

// runSetupUdev is shared by the subcommand and the TUI (which calls
// it with the terminal restored to cooked mode, so sudo can prompt).
func runSetupUdev(u *ui) error {
	if runtime.GOOS != "linux" {
		return errors.New("udev is Linux-only; on macOS picotool needs no rule (retry with sudo if a device cannot be opened)")
	}
	switch udevRulesState(udevRulesPath) {
	case udevCurrent:
		u.printf("  %s is already installed and current; nothing to do\n", udevRulesPath)
		u.printf("  %s\n", u.dim("if the device still cannot be opened, unplug and replug it once"))
		return nil
	case udevDiffers:
		u.printf("  %s exists but differs from this tool's rule; it will be replaced\n\n", udevRulesPath)
	}

	u.printf("  sh2key will write %s:\n\n", u.bold(udevRulesPath))
	for _, l := range strings.Split(strings.TrimSuffix(udevRules, "\n"), "\n") {
		u.printf("    %s\n", u.dim(l))
	}
	u.printf("\n  by running exactly:\n\n")
	cmds := udevSetupCommands("<the rule file>")
	for _, c := range cmds {
		u.printf("    %s\n", strings.Join(c, " "))
	}
	u.printf("\n  it grants your user access to Raspberry Pi USB devices (vendor id 2e8a).\n")
	u.printf("  sudo asks for your password itself; sh2key never reads it.\n\n")
	if err := u.confirmLine("  proceed? [y/N] ", "y"); err != nil {
		return err
	}

	if _, err := exec.LookPath("sudo"); err != nil {
		return fmt.Errorf("sudo not found; install the rule by hand: write the file shown above to %s, then reload udev", udevRulesPath)
	}
	tmp, err := os.CreateTemp("", "99-picotool-*.rules")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(udevRules); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	for _, c := range udevSetupCommands(tmp.Name()) {
		u.printf("\n  %s %s\n", u.dim("$"), strings.Join(c, " "))
		cmd := exec.Command(c[0], c[1:]...)
		// sudo prompts on the real terminal; the tool stays out of it.
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s failed: %v; nothing after it was run", strings.Join(c[:2], " "), err)
		}
	}
	if udevRulesState(udevRulesPath) != udevCurrent {
		return errors.New("the rule file does not read back as written; check " + udevRulesPath)
	}
	u.printf("\n  installed %s %s\n", udevRulesPath, u.tick())
	u.printf("  %s\n", u.dim("if the board was already plugged in, one unplug-replug guarantees the rule applies"))
	return nil
}
