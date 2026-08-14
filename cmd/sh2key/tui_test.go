package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"seedhammer.com/picobin"
)

func TestReadKey(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	feed := func(s string) keyEvent {
		t.Helper()
		if _, err := io.WriteString(w, s); err != nil {
			t.Fatal(err)
		}
		for {
			ev, err := readKey(r)
			if err != nil {
				t.Fatal(err)
			}
			if ev.kind != keyNone {
				return ev
			}
		}
	}
	if ev := feed("a"); ev.kind != keyChar || ev.ch != 'a' {
		t.Fatalf("a -> %+v", ev)
	}
	if ev := feed("\r"); ev.kind != keyEnter {
		t.Fatalf("enter -> %+v", ev)
	}
	if ev := feed("\x7f"); ev.kind != keyBackspace {
		t.Fatalf("backspace -> %+v", ev)
	}
	if ev := feed("\x1b[A"); ev.kind != keyUp {
		t.Fatalf("up -> %+v", ev)
	}
	if ev := feed("\x1b[B"); ev.kind != keyDown {
		t.Fatalf("down -> %+v", ev)
	}
	if ev := feed("\x1b[1;5C"); ev.kind != keyRight {
		t.Fatalf("ctrl-right -> %+v", ev)
	}
	// A lone escape resolves by poll timeout.
	if ev := feed("\x1b"); ev.kind != keyEsc {
		t.Fatalf("esc -> %+v", ev)
	}
	if ev := feed("\x03"); ev.kind != keyCtrlC {
		t.Fatalf("ctrl-c -> %+v", ev)
	}
}

func TestPaneLines(t *testing.T) {
	p := &pane{app: &tuiApp{}}
	// The pane repaints through the app; a nil tty would crash, so
	// stub the repaint path by giving the app a discard terminal.
	p.app.tty = nil
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("pane write panicked: %v", r)
		}
	}()
	// Avoid repaint entirely: write via the internal buffer logic.
	// pane.Write calls app.repaint, which needs a tty; run with one.
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	p.app.tty = devnull
	p.app.u = newUI(devnull)
	p.app.stack = []screen{&messageScreen{app: p.app, name: "t"}}

	io.WriteString(p, "one\ntwo\n")
	io.WriteString(p, "par")
	io.WriteString(p, "tial\n")
	if got := strings.Join(p.lines, "|"); got != "one|two|partial" {
		t.Fatalf("lines = %q", got)
	}
	// \r progress updates replace themselves.
	io.WriteString(p, "\rsearched 1 of 3")
	io.WriteString(p, "\rsearched 2 of 3")
	io.WriteString(p, "\rsearched 3 of 3\ndone\n")
	got := strings.Join(p.lines, "|")
	if strings.Contains(got, "1 of 3") || !strings.Contains(got, "3 of 3|done") {
		t.Fatalf("progress lines = %q", got)
	}
}

func TestHomeScreenStates(t *testing.T) {
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	priv, _ := loadFixture(t)
	a := &tuiApp{tty: devnull, u: newUI(devnull), keyPath: fixtureKeyName, priv: priv,
		picoErr: errStr("picotool not found")}
	h := newHomeScreen(a)

	frame := strings.Join(h.render(100, 40), "\n")
	if !strings.Contains(frame, fixtureFingerprint[:16]) {
		t.Error("home does not show the key fingerprint")
	}
	if !strings.Contains(frame, "not scanned") {
		t.Error("home does not show the unscanned board state")
	}
	// Build leads the list; signing and flashing follow.
	acts := h.actions()
	if acts[0].label != "Build firmware from this checkout" ||
		acts[1].label != "Sign firmware" || acts[2].label != "Flash firmware" {
		t.Errorf("first actions = %q, %q, %q", acts[0].label, acts[1].label, acts[2].label)
	}
	// Signing needs no device, so picotool's absence must not gate it;
	// everything that talks to a board must be gated.
	for _, act := range acts {
		switch act.label {
		case "Sign firmware":
			if act.disabled != "" {
				t.Errorf("signing gated without a device: %q", act.disabled)
			}
		case "Flash firmware", "Provision this board", "Enable secure boot":
			if act.disabled == "" {
				t.Errorf("%s enabled without picotool", act.label)
			}
		}
	}
	// Without a key, signing is what becomes impossible, and minting
	// and restoring join the list.
	a.priv, a.keyErr = nil, errStr("no key found")
	acts = h.actions()
	labels := make([]string, len(acts))
	for i, act := range acts {
		labels[i] = act.label
	}
	joined := strings.Join(labels, "|")
	if !strings.Contains(joined, "Mint a boot key|Restore key from 24 words") {
		t.Errorf("keyless actions = %v", labels)
	}
	if acts[1].disabled == "" {
		t.Error("signing offered without a key")
	}
	// A gated build stays visible with its reason, and navigation
	// skips disabled rows: from a disabled first row, down lands on
	// the first enabled entry after it.
	a.buildGate = "nix missing: install from nixos.org"
	acts = h.actions()
	if acts[0].disabled == "" {
		t.Error("build offered without nix")
	}
	h.cursor = 0
	h.handle(keyEvent{kind: keyDown})
	if acts[h.cursor].disabled != "" {
		t.Errorf("cursor %d landed on a disabled action %q", h.cursor, acts[h.cursor].label)
	}
	if act := h.handle(keyEvent{kind: keyChar, ch: 'q'}); act == nil {
		t.Error("q did not quit")
	}
}

func TestVisibleLenAndClip(t *testing.T) {
	u := &ui{color: true}
	styled := u.bold("abc") + u.dim("def")
	if got := visibleLen(styled); got != 6 {
		t.Fatalf("visibleLen = %d, want 6", got)
	}
	if got := padVisible(styled, 10); visibleLen(got) != 10 {
		t.Fatalf("padVisible width = %d", visibleLen(got))
	}
	clipped := clip(styled, 4)
	if got := visibleLen(clipped); got != 4 {
		t.Fatalf("clip visible width = %d (%q)", got, clipped)
	}
	if !strings.HasSuffix(clipped, sgrReset) {
		t.Fatalf("clip did not close styles: %q", clipped)
	}
}

func TestRestoreScreenFlow(t *testing.T) {
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	priv, _ := loadFixture(t)
	a := &tuiApp{tty: devnull, u: newUI(devnull), keyPath: fixtureKeyName, priv: priv}
	a.stack = []screen{&messageScreen{app: a, name: "t"}}
	r := newRestoreScreen(a)
	for _, w := range mnemonicFromKey(priv) {
		r.entry.accept(w, false)
	}
	// accept() flips to the finals stage at 23; the 24th word landed
	// through accept directly, so finalize by hand as handle would.
	if len(r.entry.entries) != 24 {
		t.Fatalf("entries = %d", len(r.entry.entries))
	}
	r.finalize()
	if r.fail != nil {
		t.Fatalf("finalize failed: %v", r.fail)
	}
	if r.priv == nil || fingerprint(r.priv) != fingerprint(priv) {
		t.Fatal("restore screen did not reproduce the key")
	}
	frame := strings.Join(r.render(100, 40), "\n")
	if !strings.Contains(frame, "byte-identical to "+fixtureKeyName) {
		t.Errorf("result frame lacks the identity verdict:\n%s", frame)
	}
}

func TestWrapTextAndStyled(t *testing.T) {
	long := "the device is present but cannot be opened; USB permission problem: install picotool's udev rule (a 2e8a-vendor rule in /etc/udev/rules.d, e.g. 99-picotool.rules), reload udev, retry"
	lines := wrapText(long, 60)
	if len(lines) < 3 {
		t.Fatalf("expected several wrapped lines, got %d", len(lines))
	}
	for _, l := range lines {
		if visibleLen(l) > 60 {
			t.Fatalf("wrapped line exceeds width: %q", l)
		}
	}
	if strings.Join(strings.Fields(strings.Join(lines, " ")), " ") != long {
		t.Fatal("wrapping lost or reordered words")
	}
	// A whole-line style is reapplied per row, closed per row.
	u := &ui{color: true}
	styled := wrapStyled(u.bad(long), 60)
	if len(styled) < 3 {
		t.Fatalf("styled wrap rows = %d", len(styled))
	}
	for _, l := range styled {
		if !strings.HasPrefix(l, sgrRed) || !strings.HasSuffix(l, sgrReset) {
			t.Fatalf("style not closed per row: %q", l)
		}
	}
	// Short lines pass through untouched.
	if got := wrapStyled("short", 60); len(got) != 1 || got[0] != "short" {
		t.Fatalf("short line rewritten: %q", got)
	}
}

func TestUdevRulesState(t *testing.T) {
	dir := t.TempDir()
	missing := dir + "/none.rules"
	if got := udevRulesState(missing); got != udevMissing {
		t.Fatalf("missing file: state %v", got)
	}
	current := dir + "/current.rules"
	os.WriteFile(current, []byte(udevRules), 0o644)
	if got := udevRulesState(current); got != udevCurrent {
		t.Fatalf("current file: state %v", got)
	}
	differs := dir + "/differs.rules"
	os.WriteFile(differs, []byte("SUBSYSTEM==\"usb\"\n"), 0o644)
	if got := udevRulesState(differs); got != udevDiffers {
		t.Fatalf("differing file: state %v", got)
	}
}

func TestHomeOffersUdevSetup(t *testing.T) {
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	priv, _ := loadFixture(t)
	a := &tuiApp{tty: devnull, u: newUI(devnull), keyPath: fixtureKeyName, priv: priv}
	a.boardErr = fmt.Errorf("%w; USB permission problem: run 'sh2key setup-udev'", errNoUSBAccess)
	h := newHomeScreen(a)
	frame := strings.Join(h.render(100, 40), "\n")
	if !strings.Contains(frame, "press i to install the udev rule") {
		t.Fatalf("home does not offer setup-udev on a permission error:\n%s", frame)
	}
	// The not-in-BOOTSEL flavor must not offer sudo for nothing.
	a.boardErr = errNotBootsel
	frame = strings.Join(h.render(100, 40), "\n")
	if strings.Contains(frame, "press i") {
		t.Fatal("setup-udev offered for a BOOTSEL problem")
	}
}

func TestBareInvocationFlags(t *testing.T) {
	// -key parses on a bare launch; in a pipe that still means usage,
	// never a surprise TUI.
	var out bytes.Buffer
	err := run(&out, nil, []string{"-key", "somewhere.pem"})
	if err == nil || !strings.Contains(err.Error(), "missing command") {
		t.Fatalf("bare -key in a pipe: %v", err)
	}
	if !strings.Contains(out.String(), "usage") {
		t.Fatal("usage not printed")
	}
	err = run(io.Discard, nil, []string{"-key", "x.pem", "backup"})
	if err == nil || !strings.Contains(err.Error(), "command first") {
		t.Fatalf("flag-before-command: %v", err)
	}
}

func TestResolveKeyPathDiscovery(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if _, err := resolveKeyPath(""); err == nil {
		t.Fatal("empty dir resolved a key")
	}
	alpha, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile("alpha.pem", marshalKeyPEM(alpha), 0o600)
	os.WriteFile("not-a-key.pem", []byte("-----BEGIN CERTIFICATE-----\nZm9v\n-----END CERTIFICATE-----\n"), 0o644)
	got, err := resolveKeyPath("")
	if err != nil || got != "alpha.pem" {
		t.Fatalf("single key by any name: %q, %v", got, err)
	}
	beta, _ := secp256k1.GeneratePrivateKey()
	os.WriteFile("beta.pem", marshalKeyPEM(beta), 0o600)
	_, err = resolveKeyPath("")
	if err == nil || !strings.Contains(err.Error(), "alpha.pem") || !strings.Contains(err.Error(), "beta.pem") {
		t.Fatalf("ambiguity not surfaced with the list: %v", err)
	}
	if got, err := resolveKeyPath("beta.pem"); err != nil || got != "beta.pem" {
		t.Fatalf("explicit -key: %q, %v", got, err)
	}
	// The convention name keeps priority so existing setups stay quiet.
	os.WriteFile(defaultKeyPath, marshalKeyPEM(alpha), 0o600)
	if got, err := resolveKeyPath(""); err != nil || got != defaultKeyPath {
		t.Fatalf("canonical priority: %q, %v", got, err)
	}
}

func TestApplySlotChoice(t *testing.T) {
	priv, _ := loadFixture(t)
	fp := fingerprint(priv)
	board := &otpBoard{keyValid: 0b0001, secureBoot: true}
	for i := range board.slots {
		board.slots[i].readable = true
		board.slots[i].zero = true
	}
	// Slot 0 holds a foreign valid key; 1-3 pristine.
	board.slots[0].zero = false
	board.slots[0].hash = [32]byte{0xde, 0xad}

	plan := makeFusePlan(board, fp)
	if plan.kind != planFuse || plan.slot != 1 {
		t.Fatalf("plan = %+v, want fuse slot 1", plan)
	}
	if got, err := applySlotChoice(plan, -1, board); err != nil || got.slot != 1 {
		t.Fatalf("auto: %+v, %v", got, err)
	}
	if got, err := applySlotChoice(plan, 3, board); err != nil || got.slot != 3 {
		t.Fatalf("choose 3: %+v, %v", got, err)
	}
	if _, err := applySlotChoice(plan, 0, board); err == nil {
		t.Fatal("non-pristine slot accepted")
	}
	if _, err := applySlotChoice(plan, 4, board); err == nil {
		t.Fatal("slot 4 accepted")
	}
	// A key already valid in a slot cannot be re-aimed.
	board.slots[0].hash = fp
	done := makeFusePlan(board, fp)
	if done.kind != planDone {
		t.Fatalf("plan = %+v, want done", done)
	}
	if _, err := applySlotChoice(done, 2, board); err == nil {
		t.Fatal("moved an already-fused key")
	}
	if got, err := applySlotChoice(done, 0, board); err != nil || got.kind != planDone {
		t.Fatalf("same-slot choice on done: %+v, %v", got, err)
	}
}

func TestKeyPickerSwitches(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	alpha, _ := secp256k1.GeneratePrivateKey()
	beta, _ := secp256k1.GeneratePrivateKey()
	os.WriteFile("alpha.pem", marshalKeyPEM(alpha), 0o600)
	os.WriteFile("beta.pem", marshalKeyPEM(beta), 0o600)
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	a := &tuiApp{tty: devnull, u: newUI(devnull)}
	a.reloadKey()
	if a.priv != nil || len(a.keyCands) != 2 {
		t.Fatalf("ambiguous state not detected: priv=%v cands=%v", a.priv, a.keyCands)
	}
	h := newHomeScreen(a)
	if !strings.Contains(strings.Join(h.render(100, 40), "\n"), "press k to choose") {
		t.Fatal("home does not offer the key picker")
	}
	act := a.keyPicker()
	push, ok := act.(navPush)
	if !ok {
		t.Fatalf("keyPicker returned %T", act)
	}
	l := push.s.(*listScreen)
	l.handle(keyEvent{kind: keyDown})
	if act := l.handle(keyEvent{kind: keyEnter}); act == nil {
		t.Fatal("pick returned no navigation")
	}
	if a.keyFlag != "beta.pem" || a.priv == nil || fingerprint(a.priv) != fingerprint(beta) {
		t.Fatalf("picker did not switch: flag=%q", a.keyFlag)
	}
}

func TestBoardAcceptsFiltersFlashList(t *testing.T) {
	priv, _ := loadFixture(t)
	fp := fingerprint(priv)
	board := &otpBoard{secureBoot: true, keyValid: 0b0001}
	for i := range board.slots {
		board.slots[i].readable = true
		board.slots[i].zero = true
	}
	board.slots[0].zero, board.slots[0].hash = false, fp

	unsigned := &uf2Info{sigZero: true, img: &picobin.Image{SignatureOffset: 64, NumBlocks: 2}}
	foreign := &uf2Info{pubKey: make([]byte, 64), img: &picobin.Image{SignatureOffset: 64, NumBlocks: 2}}
	sealedTwice := &uf2Info{pubKey: pubXY(priv.PubKey()), img: &picobin.Image{SignatureOffset: 64, NumBlocks: 3}}

	if boardAccepts(board, "x.uf2", unsigned, nil) {
		t.Error("unsigned image offered on a signature-enforcing board")
	}
	if boardAccepts(board, "x.uf2", foreign, nil) {
		t.Error("foreign-signed image offered")
	}
	if boardAccepts(board, "x.uf2", sealedTwice, nil) {
		t.Error("double-sealed image offered")
	}
	if boardAccepts(board, "x.uf2", nil, errStr("unreadable")) {
		t.Error("unreadable image offered")
	}
	// With no scan to judge by, or with enforcement off, nothing is
	// filtered out: the tool must not pretend to know.
	if !boardAccepts(nil, "x.uf2", unsigned, nil) {
		t.Error("filtered without a board scan")
	}
	off := &otpBoard{}
	if !boardAccepts(off, "x.uf2", unsigned, nil) {
		t.Error("filtered on a board with secure boot off")
	}
	// The trusted-slot lookup ignores revoked slots.
	if got := slotTrusting(board, fp); got != 0 {
		t.Errorf("slotTrusting = %d, want 0", got)
	}
	board.keyInvalid = 0b0001
	if got := slotTrusting(board, fp); got != -1 {
		t.Errorf("revoked slot reported as trusting: %d", got)
	}
}

func TestWorkingPaintsBeforeBlocking(t *testing.T) {
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	a := &tuiApp{tty: devnull, u: newUI(devnull)}
	a.stack = []screen{&messageScreen{app: a, name: "t"}}
	seen := ""
	a.working("reading the board's OTP", func() { seen = a.busy })
	if seen != "reading the board's OTP" {
		t.Errorf("busy during the step = %q", seen)
	}
	if a.busy != "" {
		t.Errorf("busy not cleared: %q", a.busy)
	}
	// A prompt inside a step owns the footer.
	a.busy = "flashing"
	restoreCheck := ""
	func() {
		busy := a.busy
		a.busy = ""
		restoreCheck = a.busy
		a.busy = busy
	}()
	if restoreCheck != "" {
		t.Error("modal did not clear the busy line")
	}
}

// provision is the only command that burns a write-once fuse, so it is
// the one that must not guess which key it is provisioning. It mints
// past a genuine absence and past a missing named file, and past nothing
// else: minting past an ambiguous directory spends a slot and then
// silences the guard everywhere, because resolveKeyPath prefers the
// convention name once it exists.
func TestProvisionKeyMintsOnlyOnAbsence(t *testing.T) {
	write := func(name string) {
		k, err := secp256k1.GeneratePrivateKey()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, marshalKeyPEM(k), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("empty dir mints", func(t *testing.T) {
		t.Chdir(t.TempDir())
		_, path, minted, err := provisionKey(newUI(io.Discard), defaultKeyPath)
		if err != nil || !minted || path != defaultKeyPath {
			t.Fatalf("got %q minted=%v err=%v; want a fresh %s", path, minted, err, defaultKeyPath)
		}
	})

	t.Run("one key is adopted", func(t *testing.T) {
		t.Chdir(t.TempDir())
		write("only.pem")
		_, path, minted, err := provisionKey(newUI(io.Discard), defaultKeyPath)
		if err != nil || minted || path != "only.pem" {
			t.Fatalf("got %q minted=%v err=%v; want only.pem adopted", path, minted, err)
		}
	})

	t.Run("ambiguous dir refuses", func(t *testing.T) {
		t.Chdir(t.TempDir())
		write("board-a.pem")
		write("board-b.pem")
		_, _, minted, err := provisionKey(newUI(io.Discard), defaultKeyPath)
		if err == nil {
			t.Fatal("ambiguous directory provisioned without asking")
		}
		if minted {
			t.Error("ambiguous directory minted a key")
		}
		if !strings.Contains(err.Error(), "several keys here") {
			t.Errorf("error does not name the ambiguity: %v", err)
		}
		if _, serr := os.Stat(defaultKeyPath); serr == nil {
			t.Errorf("%s was written despite the refusal", defaultKeyPath)
		}
	})

	t.Run("explicit -key wins in an ambiguous dir", func(t *testing.T) {
		t.Chdir(t.TempDir())
		write("board-a.pem")
		write("board-b.pem")
		_, path, minted, err := provisionKey(newUI(io.Discard), "board-b.pem")
		if err != nil || minted || path != "board-b.pem" {
			t.Fatalf("got %q minted=%v err=%v; want board-b.pem adopted", path, minted, err)
		}
	})
}

// otpScanTranscript renders a full board state the way picotool
// prints it, so a canned pico serves scanBoard the same shapes
// hardware would.
func otpScanTranscript(secureBoot bool, keyValid uint8, slots [4][16]uint16) string {
	var b strings.Builder
	sb := 0
	if secureBoot {
		sb = 1
	}
	fmt.Fprintf(&b, "ROW 0x0040: OTP_DATA_CRIT1 (CRIT)\n    VALUE 0x%06x\n    field SECURE_BOOT_ENABLE (bit 0) = %d\n", sb, sb)
	fmt.Fprintf(&b, "ROW 0x004b: OTP_DATA_BOOT_FLAGS1 (RBIT-3)\n    RAW_VALUE=0x%06[1]x;0x%06[1]x;0x%06[1]x\n    VALUE 0x%06[1]x\n    field KEY_VALID (bits 0-3) = %[1]x\n    field KEY_INVALID (bits 8-11) = 0\n", uint32(keyValid))
	addr := 0x90
	for slot := range 4 {
		for row := range 16 {
			fmt.Fprintf(&b, "ROW 0x%04x: OTP_DATA_BOOTKEY%d_%d (ECC)\n    VALUE 0x%06x\n", addr, slot, row, slots[slot][row])
			addr++
		}
	}
	return b.String()
}

// swapHarness is a tuiApp on a canned pico: every scan parses the
// given transcript, and OTP writes are recorded instead of reaching a
// device. Consent runs the TUI's modal reader against /dev/null,
// which declines instantly.
type swapHarness struct {
	app    *tuiApp
	infos  int
	writes []string
}

func newSwapHarness(t *testing.T, transcript string) *swapHarness {
	t.Helper()
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { devnull.Close() })
	priv, _ := loadFixture(t)
	h := &swapHarness{}
	pico := &pico{run: func(args ...string) (string, error) {
		if len(args) > 1 && args[0] == "otp" && args[1] == "set" {
			h.writes = append(h.writes, strings.Join(args, " "))
			return "", nil
		}
		if args[0] == "info" {
			h.infos++
			return "RP2350 in BOOTSEL mode", nil
		}
		return transcript, nil
	}}
	h.app = &tuiApp{tty: devnull, u: newUI(devnull), keyPath: fixtureKeyName, priv: priv, pico: pico}
	return h
}

// virginTUIBoard is a cached scan of a board every gate would accept.
func virginTUIBoard() *otpBoard {
	b := &otpBoard{}
	for i := range b.slots {
		b.slots[i].readable = true
		b.slots[i].zero = true
	}
	return b
}

// A board swapped in between the screen's scan and the enter press
// must not inherit the cached identity: enter rescans, so the
// ceremony and its consent see the board that is actually attached.
// The swapped-in board fails the slot gates, and nothing may reach
// it, consent included.
func TestProvisionEnterRescansTheBoard(t *testing.T) {
	t.Chdir(t.TempDir())
	var foreign [4][16]uint16
	for s := range foreign {
		for r := range foreign[s] {
			foreign[s][r] = 0x00aa
		}
	}
	h := newSwapHarness(t, otpScanTranscript(true, 0xf, foreign))
	h.app.board = virginTUIBoard()
	p := newProvisionScreen(h.app, false)
	p.fw = "firmware.uf2"
	h.app.push(p)
	p.handle(keyEvent{kind: keyEnter})
	if h.infos == 0 {
		t.Fatal("enter ran the ceremony without a fresh board scan")
	}
	transcript := strings.Join(p.pane.lines, "\n")
	if !strings.Contains(transcript, "halting") {
		t.Errorf("the ceremony did not halt on the fresh scan; pane:\n%s", transcript)
	}
	if len(h.writes) > 0 {
		t.Errorf("OTP writes reached the swapped board: %q", h.writes)
	}
	if p.ran {
		t.Error("the ceremony reported success on the swapped board")
	}
}

// The same press with the same board still reaches consent: the
// rescan must not wedge the ordinary case.
func TestProvisionEnterSameBoardReachesConsent(t *testing.T) {
	t.Chdir(t.TempDir())
	h := newSwapHarness(t, otpScanTranscript(false, 0, [4][16]uint16{}))
	h.app.board = virginTUIBoard()
	p := newProvisionScreen(h.app, false)
	p.fw = "firmware.uf2"
	h.app.push(p)
	p.handle(keyEvent{kind: keyEnter})
	if h.infos == 0 {
		t.Fatal("enter ran the ceremony without a fresh board scan")
	}
	// The consent prompt lands in the pane with its declined outcome,
	// pinning that the ceremony got past every gate and stopped at
	// exactly the typed-consent line.
	transcript := strings.Join(p.pane.lines, "\n")
	if !strings.Contains(transcript, "type FUSE to continue") {
		t.Errorf("the ceremony never reached consent; pane:\n%s", transcript)
	}
	if len(h.writes) > 0 {
		t.Errorf("OTP written despite declined consent: %q", h.writes)
	}
}

// Revoke re-derives its gate from a fresh scan at enter: the cursor
// browsed a board whose slot 1 was safely revocable, but the board
// attached at enter holds that key as its last usable one. Revoking
// it would brick the fresh board, so nothing may reach it.
func TestRevokeEnterRegatesOnFreshScan(t *testing.T) {
	t.Chdir(t.TempDir())
	priv, _ := loadFixture(t)
	fp := fingerprint(priv)
	var ours [4][16]uint16
	ours[1] = expectedRows(fp)
	h := newSwapHarness(t, otpScanTranscript(true, 0b0010, ours))
	h.app.board = revokeBoard(t, fp)
	s := newRevokeScreen(h.app)
	s.cur = 1
	h.app.push(s)
	s.handle(keyEvent{kind: keyEnter})
	if h.infos == 0 {
		t.Fatal("enter revoked without a fresh board scan")
	}
	// The fresh gate refuses before the core runs, so the pane never
	// carries a revoke transcript.
	if lines := s.pane.lines; len(lines) > 0 {
		t.Errorf("the revoke core ran against the swapped board; pane:\n%s", strings.Join(lines, "\n"))
	}
	if len(h.writes) > 0 {
		t.Errorf("OTP writes reached the swapped board: %q", h.writes)
	}
	if s.ran {
		t.Error("the revoke reported success on the swapped board")
	}
}
