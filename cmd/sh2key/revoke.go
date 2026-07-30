package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// Revoking a boot key sets its KEY_INVALID bit, which takes precedence
// over KEY_VALID and can never be cleared. It is the one operation here
// that can leave a board unable to boot anything ever again, so the
// gates below are stricter than any other: the write is refused unless
// something that can still boot demonstrably remains.

// revokePlan is what revoking one slot would mean for one board.
type revokePlan struct {
	slot     int
	holds    string   // what the slot holds today
	remains  []int    // valid, non-revoked slots afterwards
	bootable []string // what could still boot, in words
	refuse   string   // non-empty: why this must not happen
}

func makeRevokePlan(b *otpBoard, priv *secp256k1.PrivateKey, keyPath string, slot int) revokePlan {
	p := revokePlan{slot: slot}
	switch {
	case slot < 0 || slot >= otpNumSlots:
		p.refuse = fmt.Sprintf("slot %d does not exist; the RP2350 has slots 0-%d", slot, otpNumSlots-1)
		return p
	case b.slotRevoked(slot):
		p.refuse = fmt.Sprintf("slot %d is already revoked", slot)
		return p
	}
	p.holds = slotStateText(b, slot)
	for i := range b.slots {
		if i != slot && b.slotValid(i) && !b.slotRevoked(i) {
			p.remains = append(p.remains, i)
		}
	}
	if len(p.remains) == 0 {
		p.refuse = "that is the last usable boot key on this board. Revoking it means no firmware " +
			"can ever be accepted again, with no way back"
		return p
	}
	var fp [32]byte
	if priv != nil {
		fp = fingerprint(priv)
	}
	for _, i := range p.remains {
		switch {
		case hexOf(b.slots[i].hash) == signKeyHashSH2:
			p.bootable = append(p.bootable, fmt.Sprintf("slot %d, so official SeedHammer releases keep booting", i))
		case priv != nil && b.slots[i].hash == fp:
			p.bootable = append(p.bootable, fmt.Sprintf("slot %d, so firmware you sign with %s keeps booting", i, keyPath))
		}
	}
	if len(p.bootable) == 0 {
		p.refuse = fmt.Sprintf("the slots that would remain valid (%v) hold neither the manufacturer key "+
			"nor the key loaded here, so nothing provably bootable would be left. Load the key for one of "+
			"them with -key first", p.remains)
	}
	return p
}

// keyInvalidMask is the BOOT_FLAGS1 bit that revokes a slot;
// KEY_INVALID occupies bits 8 to 11.
func keyInvalidMask(slot int) uint32 {
	return 1 << (8 + slot)
}

func cmdRevoke(stdout io.Writer, args []string) error {
	fs := newFlagSet("revoke", "revoke -slot n [-key file]")
	keyFlag := fs.String("key", defaultKeyPath, "key `file` whose slot must survive the revocation")
	slotFlag := fs.Int("slot", -1, "boot-key `slot` to revoke; required, never guessed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("revoke takes no arguments, got %q", fs.Arg(0))
	}
	if *slotFlag < 0 {
		return errors.New("revoke needs -slot <n>: which key to revoke is never inferred")
	}
	u := newUI(stdout)
	var priv *secp256k1.PrivateKey
	keyPath, kerr := resolveKeyPath(*keyFlag)
	if kerr == nil {
		var err error
		priv, err = loadKeyFile(keyPath)
		if err != nil {
			return err
		}
	}
	p, board, err := connectBoard()
	if err != nil {
		return err
	}
	u.printf("\n")
	return revokeCore(u, p, board, priv, keyPath, *slotFlag)
}

// revokeCore holds the gates and performs the write. Shared by the
// subcommand and the interactive screen.
func revokeCore(u *ui, p *pico, board *otpBoard, priv *secp256k1.PrivateKey, keyPath string, slot int) error {
	plan := makeRevokePlan(board, priv, keyPath, slot)
	if plan.refuse != "" {
		return fmt.Errorf("refusing to revoke slot %d: %s.\nNothing was written", slot, plan.refuse)
	}

	u.printf("  slot %d   %s\n", slot, plan.holds)
	for _, b := range plan.bootable {
		u.printf("  remains   %s\n", u.dim(b))
	}

	// Evidence gate: when the only thing that could still boot is
	// firmware signed here, prove that firmware exists and verifies
	// before removing the alternative.
	onlyOurs := true
	for _, i := range plan.remains {
		if hexOf(board.slots[i].hash) == signKeyHashSH2 {
			onlyOurs = false
		}
	}
	if onlyOurs {
		if priv == nil {
			return errors.New("refusing to revoke: nothing but a locally signed image could boot afterwards, and no key is loaded")
		}
		evidence, err := findVerifiedSignedImage(pubXY(priv.PubKey()))
		if err != nil {
			return fmt.Errorf("refusing to revoke: afterwards only firmware signed with %s can boot, and no image here verifies against it. "+
				"Sign and flash one, confirm the board boots it, then revoke.\nNothing was written", keyPath)
		}
		u.printf("  evidence  %s verifies against %s %s\n", u.bold(evidence), keyPath, u.tick())
	}

	u.printf("\n  %s\n", u.bad("REVOKING IS FINAL. KEY_INVALID outranks KEY_VALID and can never be cleared."))
	u.printf("  %s\n", u.bad("This slot is spent, and any firmware signed only by its key stops booting"))
	u.printf("  %s\n\n", u.bad("on this board for good."))

	expect := hexOf(board.slots[slot].hash)[:8]
	prompt := fmt.Sprintf("  type the first 8 digits of the hash in slot %d (%s) to revoke it: ",
		slot, u.bold(hexOf(board.slots[slot].hash)[:16]))
	if board.slots[slot].zero {
		// An empty slot has no hash to quote; pin the confirmation to
		// the board instead, so it cannot be typed on the wrong one.
		if board.chipID == "" {
			return errors.New("refusing to revoke an empty slot on a board whose chip id could not be read")
		}
		expect = board.chipID[len(board.chipID)-8:]
		prompt = fmt.Sprintf("  slot %d is empty; revoking blocks it forever.\n"+
			"  type the last 8 digits of the chip id (%s) to continue: ", slot, u.bold(board.chipID))
	}
	if err := u.confirmLine(prompt, expect); err != nil {
		return err
	}

	mask := keyInvalidMask(slot)
	u.printf("\n  revoking slot %d across all %d copies of BOOT_FLAGS1...\n", slot, bootFlags1Copies)
	if err := p.setRedundantBits(rowBootFlags1, bootFlags1Copies, mask); err != nil {
		return err
	}
	copies, err := p.readRedundantCopies(nameBootFlags1, bootFlags1Copies)
	if err != nil {
		return fmt.Errorf("could not read BOOT_FLAGS1's copies back: %w", err)
	}
	if !copiesEqual(copies) {
		return fmt.Errorf("BOOT_FLAGS1's copies disagree after the write (%s), so the revocation is not settled; rerun to finish it",
			copiesText(copies))
	}
	flags, err := p.otpQuery(false, nameBootFlags1)
	if err != nil {
		return err
	}
	if flags.fields["BOOT_FLAGS1.KEY_INVALID"]&uint64(1<<slot) == 0 {
		return fmt.Errorf("KEY_INVALID reads 0x%x, without the bit for slot %d; stopping",
			flags.fields["BOOT_FLAGS1.KEY_INVALID"], slot)
	}
	u.printf("  slot %d revoked %s %s\n", slot, u.tick(), u.dim("(copies "+copiesText(copies)+")"))
	u.printf("\n  what still boots: %s\n", strings.Join(plan.bootable, "; "))
	return nil
}
