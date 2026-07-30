package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// status is the command to reach for first: it classifies the
// attached board from its OTP tuple, relates the local key to it, and
// names the next step. It writes nothing, ever.

func cmdStatus(stdout io.Writer, args []string) error {
	fs := newFlagSet("status", "status [-key file]")
	keyFlag := fs.String("key", defaultKeyPath, "key `file` to relate the board to")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("status takes no arguments, got %q", fs.Arg(0))
	}
	u := newUI(stdout)
	u.printf("\n")

	// The local half renders even with no board attached.
	var priv *secp256k1.PrivateKey
	keyPath, kerr := resolveKeyPath(*keyFlag)
	if kerr == nil {
		var err error
		priv, err = loadKeyFile(keyPath)
		if err != nil {
			return err
		}
		fp := fingerprintHex(priv)
		u.printf("  local key    %s  fingerprint %s\n", u.bold(keyPath), u.bold(fp[:16])+u.dim(fp[16:]))
		if rf, err := loadRecords(keyPath); err == nil && len(rf.Boards) > 0 {
			for _, r := range rf.Boards {
				u.printf("               %s\n", u.dim(fmt.Sprintf("record: board %s… slot %d, provisioned %s", shortID(r.ChipID), r.Slot, r.Provisioned)))
			}
		}
	} else {
		u.printf("  local key    %s\n", u.dim("none ("+kerr.Error()+")"))
	}
	u.printf("\n")

	p, board, err := connectBoard()
	if err != nil {
		return err
	}
	_ = p
	pop, label := board.classify()

	u.printf("  device       RP2350 in BOOTSEL mode\n")
	if board.chipID != "" {
		u.printf("  chip id      %s   rand id %s\n", board.chipID, board.randID)
	} else {
		u.printf("  chip id      %s\n", u.warn("unreadable (board recognition unavailable)"))
	}
	u.printf("  population   %s\n\n", u.bold(label))
	onOff := u.bad("enabled")
	if !board.secureBoot {
		onOff = "off"
	}
	u.printf("  secure boot  %s\n", onOff)
	u.printf("  key valid    0b%04b    key invalid  0b%04b\n", board.keyValid, board.keyInvalid)
	for _, w := range board.redundancyWarnings() {
		u.printf("  %s\n", u.warn(w))
	}
	u.printf("\n")

	var ourFP [32]byte
	if priv != nil {
		ourFP = fingerprint(priv)
	}
	for i := range board.slots {
		state, detail := slotView(u, board, i, priv != nil, ourFP)
		u.printf("  slot %d  %s  %s\n", i, state, detail)
	}
	u.printf("\n")

	// The verdict: what this board and this key mean for each other.
	switch {
	case priv == nil:
		u.printf("  %s\n", u.dim("no local key to relate; 'sh2key mint' creates one"))
	case slotOfKey(board, ourFP) >= 0:
		s := slotOfKey(board, ourFP)
		u.printf("  this key signs for slot %d %s\n", s, u.tick())
		if !board.secureBoot {
			u.printf("  %s\n", u.warn("secure boot is off: nothing is enforced yet; 'sh2key enable-secure-boot' is the remaining one-way door"))
		} else {
			u.printf("  %s\n", u.dim("'sh2key sign && sh2key flash' rebuilds and reflashes; zero OTP writes"))
		}
	case pop == popA:
		if s := firstFreeSlot(board); s >= 0 {
			u.printf("  %s\n", u.dim(fmt.Sprintf("this key is not on the board; 'sh2key provision' would add it to slot %d", s)))
		} else {
			u.printf("  %s\n", u.bad("no pristine non-revoked slot remains; this board cannot take another key"))
		}
	case pop == popB:
		u.printf("  %s\n", u.dim("virgin board: 'sh2key flash' flashes with zero fuse writes and stays reversible forever;"))
		u.printf("  %s\n", u.dim("'sh2key provision' is the opt-in hardening path and starts burning fuses"))
	default:
		u.printf("  %s\n", u.warn("this state needs a human: nothing is safe to do automatically"))
	}
	u.printf("\n")
	return nil
}

func slotView(u *ui, b *otpBoard, i int, haveKey bool, ourFP [32]byte) (string, string) {
	s := &b.slots[i]
	switch {
	case b.slotRevoked(i):
		return u.bad("revoked "), u.dim("KEY_INVALID set; never writable again")
	case !s.readable:
		return u.warn("unread  "), u.dim("ECC read error; treated as not pristine")
	case b.slotValid(i):
		h := hexOf(s.hash)
		// Without a local key there is nothing to compare against, so
		// the slot is unrecognized rather than foreign.
		who := u.dim("unrecognized key")
		switch {
		case h == signKeyHashSH2:
			who = u.dim("SeedHammer manufacturer key")
		case haveKey && s.hash == ourFP:
			who = u.accent("this key")
		case haveKey:
			who = u.dim("another key, not this one")
		}
		return u.good("valid   "), u.bold(h[:16]) + u.dim(h[16:32]+"…") + "  " + who
	case s.zero:
		return u.dim("empty   "), u.dim("all zeros, writable")
	default:
		h := hexOf(s.hash)
		detail := u.dim(h[:16] + "…  written but not marked valid")
		if haveKey && subsetOf(s.hash, ourFP) {
			detail = u.dim(h[:16]+"…  ") + u.warn("partial write of this key; 'sh2key provision' resumes it")
		}
		return u.warn("partial "), detail
	}
}

// subsetOf reports whether every set bit of got is set in want: the
// definition of "resuming this write only ever flips 0s to 1s".
func subsetOf(got, want [32]byte) bool {
	for i := range got {
		if got[i]&^want[i] != 0 {
			return false
		}
	}
	return true
}

func slotOfKey(b *otpBoard, fp [32]byte) int {
	for i := range b.slots {
		if b.slots[i].readable && b.slotValid(i) && !b.slotRevoked(i) && b.slots[i].hash == fp {
			return i
		}
	}
	return -1
}

// slotPristine applies gate G4 to one slot: it reads exactly zero in
// all sixteen rows, is not revoked, not valid, and read without
// error. The device firmware's best-fit partial matching stays on the
// device; on the host a slot is pristine or it is not a candidate.
func slotPristine(b *otpBoard, i int) bool {
	s := &b.slots[i]
	return s.readable && s.zero && !b.slotValid(i) && !b.slotRevoked(i)
}

// pristineSlots lists the G4 candidates, lowest first.
func pristineSlots(b *otpBoard) []int {
	var out []int
	for i := range b.slots {
		if slotPristine(b, i) {
			out = append(out, i)
		}
	}
	return out
}

func firstFreeSlot(b *otpBoard) int {
	if p := pristineSlots(b); len(p) > 0 {
		return p[0]
	}
	return -1
}

// slotStateText names a slot's state in plain words, for errors and
// pickers.
func slotStateText(b *otpBoard, i int) string {
	s := &b.slots[i]
	switch {
	case b.slotRevoked(i):
		return "revoked (KEY_INVALID set)"
	case !s.readable:
		return "unreadable (treated as not pristine)"
	case b.slotValid(i):
		return "valid, holds " + hexOf(s.hash)[:16] + "…"
	case s.zero:
		return "pristine, all zeros"
	default:
		return "partially written, holds " + hexOf(s.hash)[:16] + "…"
	}
}

// resumableSlot finds a slot holding an interrupted write of this
// exact key: not valid, not revoked, nonzero, and every set bit
// already agrees with the key hash. Re-asserting it is harmless
// because OTP writes only ever flip 0 to 1.
func resumableSlot(b *otpBoard, fp [32]byte) int {
	for i := range b.slots {
		s := &b.slots[i]
		if s.readable && !s.zero && !b.slotValid(i) && !b.slotRevoked(i) && subsetOf(s.hash, fp) {
			return i
		}
	}
	return -1
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func connectBoard() (*pico, *otpBoard, error) {
	p, err := findPicotool()
	if err != nil {
		return nil, nil, err
	}
	info, err := p.requireDevice()
	if err != nil {
		return nil, nil, err
	}
	if err := checkRP2350(info); err != nil {
		return nil, nil, err
	}
	b, err := p.readBoard()
	if err != nil {
		return nil, nil, err
	}
	return p, b, nil
}

// checkRP2350 is the chip half of gate G2. Unknown info formats pass;
// the OTP reads that follow fail loudly on anything exotic.
func checkRP2350(infoOut string) error {
	if strings.Contains(infoOut, "RP2040") {
		return fmt.Errorf("the connected device is an RP2040; the boot-key ceremony is RP2350-only")
	}
	return nil
}
