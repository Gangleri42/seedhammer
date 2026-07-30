package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
)

// The home screen: what exists (key, board), what it means, and what
// can be done about it. Actions that make no sense in the current
// state stay visible but disabled, each with its reason: the screen
// teaches the ceremony by reading it.

type homeScreen struct {
	app    *tuiApp
	cursor int
}

func newHomeScreen(a *tuiApp) *homeScreen {
	return &homeScreen{app: a}
}

func (h *homeScreen) title() string { return "home" }

func (h *homeScreen) footer() string {
	f := "↑↓ move · enter select · r rescan board"
	if len(h.app.keyCands) > 1 {
		f += " · k key"
	}
	return f + " · q quit"
}

type homeAction struct {
	label    string
	detail   string
	disabled string
	run      func() navAction
}

func (h *homeScreen) actions() []homeAction {
	a := h.app
	var out []homeAction

	picoGate := ""
	if a.pico == nil {
		picoGate = "picotool missing: nix develop provides it"
	}
	signGate := ""
	if a.priv == nil {
		signGate = "needs a key: mint or restore first"
	}

	// A fresh checkout starts at the top: build, then the everyday
	// pair, sign and flash; the rest of the ceremony is rarer.
	out = append(out,
		homeAction{
			label:    "Build firmware from this checkout",
			detail:   "nix run .#build-firmware; unsigned, lands here",
			disabled: a.buildGate,
			run: func() navAction {
				a.suspend(func() error { return runBuild(newUI(os.Stdout)) })
				return nil
			},
		},
		homeAction{
			label:    "Sign firmware",
			detail:   "picosign flow in-process; the input survives",
			disabled: signGate,
			run:      func() navAction { return navPush{newSignScreen(a, false)} },
		},
		homeAction{
			label:    "Flash firmware",
			detail:   "only images this board's fused keys accept",
			disabled: picoGate,
			run:      func() navAction { return navPush{newSignScreen(a, true)} },
		})

	if a.priv == nil {
		out = append(out,
			homeAction{
				label:  "Mint a boot key",
				detail: "fresh secp256k1 key, 0600, git-excluded",
				run:    func() navAction { return a.runMint() },
			},
			homeAction{
				label:  "Restore key from 24 words",
				detail: "type the plate back into a byte-identical PEM",
				run:    func() navAction { return navPush{newRestoreScreen(a)} },
			})
	} else {
		out = append(out,
			homeAction{
				label:  "Back up key as 24 words",
				detail: "view, engrave, or save the words plate",
				run:    func() navAction { return navPush{newBackupScreen(a)} },
			},
			homeAction{
				label:  "Restore / verify from 24 words",
				detail: "prove a plate rebuilds this key",
				run:    func() navAction { return navPush{newRestoreScreen(a)} },
			},
			homeAction{
				label:  "Nostr identity",
				detail: "the key as nsec1/npub1, engravable",
				run:    func() navAction { return navPush{newNsecScreen(a)} },
			})
	}

	out = append(out,
		homeAction{
			label:    "Provision this board",
			detail:   "mint + fuse + sign + flash, skipping what is done",
			disabled: picoGate,
			run:      func() navAction { return navPush{newProvisionScreen(a, false)} },
		})

	esbGate := picoGate
	switch {
	case esbGate != "":
	case a.priv == nil:
		esbGate = "needs a key: mint or restore first"
	case a.board != nil && a.board.secureBoot:
		esbGate = "already enabled on the attached board"
	}
	revokeGate := picoGate
	if revokeGate == "" && a.board != nil {
		allowed := false
		for i := range otpNumSlots {
			if makeRevokePlan(a.board, a.priv, a.keyPath, i).refuse == "" {
				allowed = true
			}
		}
		if !allowed {
			revokeGate = "no slot can be revoked while leaving something bootable"
		}
	}
	out = append(out,
		homeAction{
			label:    "Enable secure boot",
			detail:   "one-way door: the board starts requiring signatures",
			disabled: esbGate,
			run:      func() navAction { return navPush{newProvisionScreen(a, true)} },
		},
		homeAction{
			label:    "Revoke a boot key",
			detail:   "final: that key stops booting this board for good",
			disabled: revokeGate,
			run:      func() navAction { return navPush{newRevokeScreen(a)} },
		},
		homeAction{
			label:  "Quit",
			detail: "",
			run:    func() navAction { return navQuit{} },
		})
	return out
}

func (h *homeScreen) render(w, hgt int) []string {
	a, u := h.app, h.app.u
	var out []string
	line := func(s string) { out = append(out, s) }

	// The key panel.
	line("")
	line("  " + u.dim("key"))
	switch {
	case a.priv != nil:
		fp := fingerprintHex(a.priv)
		line("    " + u.bold(a.keyPath) + "   " + u.bold(fp[:16]) + u.dim(fp[16:32]+"…"))
		if a.records != nil {
			for _, r := range a.records.Boards {
				line("    " + u.dim(fmt.Sprintf("board %s… slot %d, provisioned %s", shortID(r.ChipID), r.Slot, r.Provisioned)))
			}
		}
		if len(a.keyCands) > 1 {
			line("    " + u.dim(fmt.Sprintf("%d key PEMs here; press k to switch", len(a.keyCands))))
		}
	case len(a.keyCands) > 1:
		line("    " + u.warn(fmt.Sprintf("%d key PEMs here, none chosen", len(a.keyCands))))
		line("    " + u.accent("press k to choose which key drives the tool"))
	case a.keyErr != nil:
		line("    " + u.dim("none: "+firstLine(a.keyErr.Error())))
	}

	// The board panel: only ever what the last explicit scan saw.
	line("")
	line("  " + u.dim("board"))
	switch {
	case a.board != nil:
		b := a.board
		_, label := b.classify()
		line("    RP2350 in BOOTSEL · " + u.bold(label))
		sb := "off"
		if b.secureBoot {
			sb = u.bad("on")
		}
		line("    " + u.dim("secure boot ") + sb +
			u.dim(fmt.Sprintf("   valid 0b%04b   invalid 0b%04b   chip %s", b.keyValid, b.keyInvalid, shortID(b.chipID))))
		for _, warn := range b.redundancyWarnings() {
			for _, l := range wrapText(warn, w-8) {
				line("    " + u.warn(l))
			}
		}
		var ourFP [32]byte
		if a.priv != nil {
			ourFP = fingerprint(a.priv)
		}
		for i := range b.slots {
			state, detail := slotView(u, b, i, a.priv != nil, ourFP)
			line(fmt.Sprintf("    %d %s %s", i, state, detail))
		}
		line("    " + h.verdict())
	case a.boardErr != nil:
		if errors.Is(a.boardErr, errNotBootsel) {
			line("    " + u.warn("no RP2350 in BOOTSEL mode was found"))
			line("    " + u.dim("BOOTSEL dance: unplug, hold the button, plug, release; then press r"))
		} else {
			for i, l := range wrapText(a.boardErr.Error(), w-8) {
				if i == 0 {
					line("    " + u.warn(l))
				} else {
					line("    " + u.dim(l))
				}
				if i == 7 {
					break
				}
			}
			if offerUdevSetup(a.boardErr) {
				line("    " + u.accent("press i to install the udev rule now (sudo asks for your password, sh2key never sees it)"))
			}
		}
	case a.busy != "":
		line("    " + u.dim("scanning..."))
	default:
		line("    " + u.dim("not scanned; press r with the board in BOOTSEL"))
	}

	// The actions.
	line("")
	line("  " + u.dim("actions"))
	for i, act := range h.actions() {
		cur := "  "
		label, detail := act.label, act.detail
		switch {
		case act.disabled != "":
			label, detail = u.dim(label), u.dim(act.disabled)
		case i == h.cursor:
			cur = u.accent(cursorMark(u)) + " "
			label, detail = u.bold(label), u.dim(detail)
		default:
			detail = u.dim(detail)
		}
		line("  " + cur + padVisible(label, 34) + detail)
	}
	return out
}

// verdict is the one sentence relating this key to this board.
func (h *homeScreen) verdict() string {
	a, u := h.app, h.app.u
	b := a.board
	if a.priv == nil {
		return u.dim("no local key to relate")
	}
	fp := fingerprint(a.priv)
	plan := makeFusePlan(b, fp)
	switch plan.kind {
	case planDone:
		s := fmt.Sprintf("this key signs for slot %d ", plan.slot) + u.tick()
		if !b.secureBoot {
			s += u.warn("  (secure boot off: nothing enforced yet)")
		}
		return s
	case planResume:
		return u.warn(fmt.Sprintf("slot %d holds an interrupted write of this key; provision resumes it", plan.slot))
	case planFuse:
		return u.dim(fmt.Sprintf("this key is not on the board; provision would use slot %d", plan.slot))
	default:
		return u.warn(firstLine(plan.reason))
	}
}

func (h *homeScreen) handle(ev keyEvent) navAction {
	acts := h.actions()
	if h.cursor >= len(acts) {
		h.cursor = len(acts) - 1
	}
	switch ev.kind {
	case keyEsc:
		return navQuit{}
	case keyUp:
		h.move(acts, -1)
	case keyDown:
		h.move(acts, 1)
	case keyEnter:
		if acts[h.cursor].disabled == "" {
			return acts[h.cursor].run()
		}
	case keyChar:
		switch ev.ch {
		case 'q':
			return navQuit{}
		case 'r':
			h.app.refreshBoard()
		case 'i':
			if offerUdevSetup(h.app.boardErr) {
				h.app.suspend(func() error { return runSetupUdev(newUI(os.Stdout)) })
				h.app.refreshBoard()
			}
		case 'k':
			return h.app.keyPicker()
		}
	}
	return nil
}

// offerUdevSetup reports whether the board error is the one
// setup-udev fixes.
func offerUdevSetup(err error) bool {
	return err != nil && errors.Is(err, errNoUSBAccess) && runtime.GOOS == "linux"
}

func (h *homeScreen) move(acts []homeAction, d int) {
	for i := h.cursor + d; 0 <= i && i < len(acts); i += d {
		if acts[i].disabled == "" {
			h.cursor = i
			return
		}
	}
}

// runMint mints through a confirm modal and reloads the key panel.
// The backup-on-steel hint deliberately does not appear here: the
// machine engraves the words plate only once it runs firmware this
// key signed, so provision comes first and prints that hint when it
// is actionable.
func (a *tuiApp) runMint() navAction {
	path := defaultKeyPath
	text, ok := a.modalInput("mint", []string{
		"Mint a fresh secp256k1 boot key (the name is editable below).",
		"Until it is fused and backed up, this one file is the whole story.",
	}, "write it to:", path)
	if !ok {
		return nil
	}
	if text != "" {
		path = text
	}
	priv, err := mintKey(path)
	if err != nil {
		return a.errorScreen("mint", err)
	}
	note := "not inside a git checkout"
	if excl, xerr := gitExclude(path); xerr == nil && excl != "" {
		note = "added to " + excl
	}
	a.keyFlag = path
	a.reloadKey()
	fp := fingerprintHex(priv)
	return a.message("mint", false,
		"minted "+path+" (mode 0600)",
		"fingerprint "+fp,
		note,
		"",
		"next: provision this board (fuse the key, sign, flash);",
		"once the machine runs that firmware it can engrave the words backup")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
