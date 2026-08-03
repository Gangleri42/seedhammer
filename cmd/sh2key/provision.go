package main

import (
	"bufio"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// provision runs the whole ceremony against the attached board and
// skips every step that is already done: mint if no key, fuse if the
// key is not on the board, then sign and flash. Re-run against a new
// firmware build it recognises the fused key and performs zero OTP
// writes. Every fuse write is gated on a fresh readback (gates G1-G11
// in deliverables/bootkey-ceremony-script-spec.md).

func cmdProvision(stdout io.Writer, args []string) error {
	fs := newFlagSet("provision", "provision [uf2] [-key file] [-slot n]")
	keyFlag := fs.String("key", defaultKeyPath, "key `file`; minted here if missing")
	slotFlag := fs.Int("slot", -1, "boot-key `slot` to fuse (default: lowest pristine; must satisfy gate G4)")
	lead, rest := popArg(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	u := newUI(stdout)
	u.printf("\n")

	priv, keyPath, minted, err := provisionKey(u, *keyFlag)
	if err != nil {
		return err
	}
	fpHex := fingerprintHex(priv)
	u.printf("  key          %s  fingerprint %s\n", u.bold(keyPath), u.bold(fpHex[:16])+u.dim(fpHex[16:]))

	// Resolve and preflight the firmware before any device talk, so a
	// double-sealed or missing image fails while everything is still
	// reversible.
	in, err := oneArg(fs, lead)
	if err != nil {
		return err
	}
	if in == "" {
		in, err = findUnsignedUF2()
		if err != nil {
			return err
		}
	}
	info, err := inspectUF2(in)
	if err != nil {
		return err
	}
	if err := checkSignable(in, info); err != nil {
		return err
	}
	u.printf("  firmware     %s\n\n", u.bold(in))

	p, board, err := connectBoard()
	if err != nil {
		return err
	}
	if err := runCeremony(u, p, board, priv, keyPath, in, *slotFlag); err != nil {
		return err
	}
	if minted {
		u.printf("\n  %s\n", u.warn("the key was minted this run and exists nowhere else."))
		u.printf("  %s\n", u.warn("the machine now boots firmware it signed, so it can engrave the backup:"))
		u.printf("  %s\n", u.warn("  sh2key backup "+keyPath+" -nfc"))
	}
	u.printf("\n")
	return nil
}

// runCeremony is the whole board ceremony against an already-scanned
// board: recognise, fuse what is missing, sign fresh, cross-check,
// flash, record. The CLI and the TUI wizard both run exactly this;
// consent arrives through u.confirmLine either way.
func runCeremony(u *ui, p *pico, board *otpBoard, priv *secp256k1.PrivateKey, keyPath, in string, slotChoice int) error {
	fp := fingerprint(priv)
	fpHex := fingerprintHex(priv)
	_, label := board.classify()
	u.printf("  board        %s\n", u.bold(label))
	if board.chipID != "" {
		u.printf("  chip id      %s\n", board.chipID)
	}

	// Recognition against the record: bookkeeping only, the live scan
	// is the authority (gate G3).
	records, err := loadRecords(keyPath)
	if err != nil {
		return err
	}
	if rec := records.find(board.chipID); rec != nil {
		u.printf("  record       %s\n", u.dim(fmt.Sprintf("seen before: slot %d, provisioned %s", rec.Slot, rec.Provisioned)))
		if rec.RandID != "" && board.randID != "" && rec.RandID != board.randID {
			u.printf("  %s\n", u.warn("rand id differs from the record; treating this as a new board"))
		}
	}
	u.printf("\n")

	plan, err := applySlotChoice(makeFusePlan(board, fp), slotChoice, board)
	if err != nil {
		return err
	}
	slot := plan.slot
	switch plan.kind {
	case planHalt:
		return fmt.Errorf("halting: %s.\nRun 'sh2key status' for the full picture; nothing was written", plan.reason)
	case planDone:
		u.printf("  slot %d already holds this key and is marked valid %s %s\n", slot, u.tick(), u.dim("(zero OTP writes this run)"))
	default:
		if plan.kind == planResume {
			u.printf("  slot %d holds an interrupted write of this key; resuming is a pure 0-to-1 re-assert\n", slot)
		}
		if err := confirmFuse(u, board, slot, fpHex); err != nil {
			return err
		}
		if err := fuseSlot(u, p, board, slot, fp, plan.kind == planResume); err != nil {
			return err
		}
	}

	// Flashing replaces whatever currently boots, so on a board that
	// enforces signatures, confirm from a fresh scan that this key is
	// trusted before doing it. Without this, a fuse write that looked
	// fine but did not take leaves the board holding an image its ROM
	// rejects.
	if board.secureBoot && plan.kind != planDone {
		fresh, err := p.readBoard()
		if err != nil {
			return fmt.Errorf("could not re-scan the board after the fuse write: %w", err)
		}
		if slotOfKey(fresh, fp) < 0 {
			return fmt.Errorf("after the fuse write this key is still not valid on the board "+
				"(KEY_VALID 0b%04b, copies %s), and this board only boots signed firmware.\n"+
				"Nothing was flashed, so it still boots what it booted before. Rerun 'sh2key status' and provision again",
				fresh.keyValid, copiesText(fresh.flagCopies))
		}
	}

	// Sign fresh from the PEM on every run; cached artifacts from
	// other keys are exactly how boards die.
	out := signedName(in)
	if err := signImage(u, priv, in, out); err != nil {
		return err
	}
	if err := picotoolSignatureCheck(p, out); err != nil {
		return err
	}
	if err := flashAndReboot(u, p, out); err != nil {
		return err
	}

	prior := records.find(board.chipID)
	records.upsert(boardRecord{
		ChipID:                board.chipID,
		RandID:                board.randID,
		Slot:                  slot,
		Population:            label,
		KeyFingerprint:        fpHex,
		SecureBootEnabledByUs: prior != nil && prior.SecureBootEnabledByUs,
		Provisioned:           nowUTC(),
	})
	if err := records.save(keyPath); err != nil {
		return err
	}

	u.printf("\n  done. the board reboots into the new image\n")
	if !board.secureBoot {
		u.printf("  %s\n", u.dim("nothing is enforced yet: the board still boots anything."))
		u.printf("  %s\n", u.dim("after confirming this image boots, 'enable secure boot' is the separate one-way door"))
	} else if board.holdsManufacturerKey() {
		u.printf("  %s\n", u.dim("an (UNLOCKED) suffix on the device's version string is expected: the firmware flag is cosmetic,"))
		u.printf("  %s\n", u.dim("the ROM still enforces signatures against the fused slots"))
	}
	return nil
}

// A fusePlan is what one OTP scan means for one key: nothing left to
// write, an interrupted own write to resume, a pristine slot to burn,
// or a state that needs a human.
type planKind int

const (
	planDone planKind = iota
	planResume
	planFuse
	planHalt
)

type fusePlan struct {
	slot   int
	kind   planKind
	reason string
}

func makeFusePlan(board *otpBoard, fp [32]byte) fusePlan {
	if s := slotOfKey(board, fp); s >= 0 {
		return fusePlan{slot: s, kind: planDone}
	}
	if s := resumableSlot(board, fp); s >= 0 {
		return fusePlan{slot: s, kind: planResume}
	}
	if pop, label := board.classify(); pop == popC {
		return fusePlan{slot: -1, kind: planHalt, reason: label}
	}
	if s := firstFreeSlot(board); s >= 0 {
		return fusePlan{slot: s, kind: planFuse}
	}
	return fusePlan{slot: -1, kind: planHalt,
		reason: "no pristine non-revoked slot remains; this board cannot take another key"}
}

// applySlotChoice folds an explicit slot request into the plan. A key
// already tied to a slot cannot be re-aimed, and a chosen slot must
// satisfy gate G4 exactly like the automatic choice would.
func applySlotChoice(plan fusePlan, override int, board *otpBoard) (fusePlan, error) {
	if override < 0 || plan.kind == planHalt || override == plan.slot {
		return plan, nil
	}
	if override >= otpNumSlots {
		return plan, fmt.Errorf("slot %d does not exist; the RP2350 has slots 0-%d", override, otpNumSlots-1)
	}
	switch plan.kind {
	case planDone:
		return plan, fmt.Errorf("this key is already fused and valid in slot %d; OTP is one-way and a key cannot move slots", plan.slot)
	case planResume:
		return plan, fmt.Errorf("slot %d holds an interrupted write of this key; it must be finished there, not moved", plan.slot)
	}
	if !slotPristine(board, override) {
		return plan, fmt.Errorf("slot %d is not a candidate: %s (pristine: %v)", override, slotStateText(board, override), pristineSlots(board))
	}
	plan.slot = override
	return plan, nil
}

const otpNumSlots = 4

func provisionKey(u *ui, keyFlag string) (priv *secp256k1.PrivateKey, path string, minted bool, err error) {
	path = keyFlag
	if path == "" {
		path = defaultKeyPath
	}
	if resolved, rerr := resolveKeyPath(keyFlag); rerr == nil {
		priv, err = loadKeyFile(resolved)
		return priv, resolved, false, err
	} else if !errors.Is(rerr, fs.ErrNotExist) && !errors.Is(rerr, errNoKeyHere) {
		// Only a genuine absence mints. resolveKeyPath also reports an
		// ambiguous directory, and this is the one command that burns a
		// write-once fuse: minting past "several keys here" spends a
		// slot and then silences the guard everywhere, because
		// resolveKeyPath prefers the convention name once it exists.
		return nil, "", false, rerr
	}
	priv, err = mintKey(path)
	if err != nil {
		return nil, "", false, err
	}
	u.printf("  minted       %s %s\n", u.bold(path), u.dim("(mode 0600; unrecoverable if lost)"))
	if excl, err := gitExclude(path); err == nil && excl != "" {
		u.printf("               %s\n", u.dim("added to "+excl))
	}
	return priv, path, true, nil
}

// confirmFuse holds the two consent gates. On a locked board a slot
// burn is the guide's normal path: one keystroke. On a board without
// secure boot the tool cannot vouch for what the board is (gate G2),
// so consent means typing the chip id back.
func confirmFuse(u *ui, board *otpBoard, slot int, fpHex string) error {
	if board.secureBoot {
		u.printf("\n  about to burn boot-key slot %d with %s\n", slot, u.bold(fpHex[:16])+"…")
		u.printf("  OTP is one-way: this slot is spent forever, though 2 more remain after it.\n")
		return u.confirmLine("  proceed? [y/N] ", "y")
	}
	u.printf("\n  %s\n", u.warn("this board has secure boot OFF. The reversible default is 'sh2key flash':"))
	u.printf("  %s\n", u.warn("no fuses, reflash anything forever. Continuing here instead burns OTP fuses"))
	u.printf("  %s\n", u.warn("that permanently tie slot "+fmt.Sprint(slot)+" to this key."))
	u.printf("  %s\n", u.dim("(secure boot itself stays off; 'sh2key enable-secure-boot' is a separate, later step)"))
	expect := "FUSE"
	if board.chipID != "" {
		expect = board.chipID[len(board.chipID)-8:]
		u.printf("\n  type the last 8 digits of the chip id (%s) to continue: ", u.bold(board.chipID))
	} else {
		u.printf("\n  type FUSE to continue: ")
	}
	return u.confirmLine("", expect)
}

// confirmLine reads one line of consent. Piped stdin cannot answer:
// consent to a fuse write is interactive by design. The default reads
// the controlling terminal; a TUI ui substitutes its own field.
func (u *ui) confirmLine(prompt, expect string) error {
	if u.confirm != nil {
		return u.confirm(prompt, expect)
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return errors.New("fuse writes need an interactive terminal to confirm; nothing was written")
	}
	defer tty.Close()
	if prompt != "" {
		io.WriteString(tty, prompt)
	}
	line, err := bufio.NewReader(tty).ReadString('\n')
	if err != nil {
		return errors.New("confirmation aborted; nothing was written")
	}
	if !strings.EqualFold(strings.TrimSpace(line), expect) {
		return errors.New("confirmation did not match; nothing was written")
	}
	return nil
}

// fuseSlot writes the key hash into a slot and marks it valid, with
// the readback between the two steps that picotool itself does not
// do: otp load writes JSON and reads nothing back (gates G5, G6, G7).
func fuseSlot(u *ui, p *pico, board *otpBoard, slot int, fp [32]byte, resumed bool) error {
	expected := expectedRows(fp)

	// G5: one hash source. The fingerprint came from the PEM through
	// pubXY; its row packing and the readback unpacking are inverse
	// functions pinned by TestExpectedRows against the howto's worked
	// example, so a packing bug cannot produce a matching readback.
	var ints []string
	for _, b := range fp {
		ints = append(ints, fmt.Sprint(b))
	}
	body := fmt.Sprintf("{\n    \"bootkey%d\": [ %s ]\n}\n", slot, strings.Join(ints, ", "))
	u.printf("\n  writing hash rows to slot %d (picotool otp load)...\n", slot)
	if out, err := p.otpLoadJSON(body); err != nil {
		if strings.Contains(out, "clear bits") || strings.Contains(err.Error(), "clear bits") {
			return fmt.Errorf("picotool refused the write because it would clear already-set bits; the slot holds different data than the scan saw. Nothing was changed; rerun 'sh2key status'")
		}
		return err
	}

	// G6: verify all sixteen rows before anything is marked valid.
	var names []string
	for row := range 16 {
		names = append(names, fmt.Sprintf("BOOTKEY%d_%d", slot, row))
	}
	got, err := p.otpQuery(true, names...)
	if err != nil {
		return fmt.Errorf("readback failed after the hash write; the slot is NOT marked valid and the board boots as before. %w", err)
	}
	var bad []string
	for row, name := range names {
		v, ok := got.rows[name]
		switch {
		case !ok:
			bad = append(bad, fmt.Sprintf("%s: unreadable", name))
		case eccData(v) != expected[row]:
			bad = append(bad, fmt.Sprintf("%s: got 0x%04x, want 0x%04x", name, eccData(v), expected[row]))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("readback disagrees with the expected rows; STOPPING before KEY_VALID.\nThe slot is not marked valid; the board still boots exactly as before.\n  %s", strings.Join(bad, "\n  "))
	}
	u.printf("  all 16 rows read back correctly %s\n", u.tick())

	// G7: the valid bit for the slot actually written, OR-ed in via
	// otp set -s, which refuses any write that would clear a bit.
	//
	// KEY_VALID lives in BOOT_FLAGS1, a row the boot ROM reads as a
	// majority vote across three copies, so the bit goes into every
	// copy and every copy is verified.
	mask := uint32(1) << slot
	u.printf("  marking slot %d valid across all %d copies of BOOT_FLAGS1...\n", slot, bootFlags1Copies)
	if err := p.setRedundantBits(rowBootFlags1, bootFlags1Copies, mask); err != nil {
		return err
	}
	copies, err := p.readRedundantCopies(nameBootFlags1, bootFlags1Copies)
	if err != nil {
		return fmt.Errorf("could not read BOOT_FLAGS1's copies back: %w", err)
	}
	if !copiesEqual(copies) {
		return fmt.Errorf("BOOT_FLAGS1's copies disagree after the write (%s), so the valid bit is not settled.\n"+
			"The hash rows are written and re-asserting them flips no bits, so rerunning provision on this slot completes it",
			copiesText(copies))
	}
	// The decoded row is what the ROM acts on; with the copies in
	// agreement it must carry the bit.
	flags, err := p.otpQuery(false, nameBootFlags1)
	if err != nil {
		return err
	}
	valid, ok := flags.fields["BOOT_FLAGS1.KEY_VALID"]
	if !ok || valid&uint64(mask) == 0 {
		return fmt.Errorf("the copies agree (%s) but KEY_VALID decodes as 0x%x without the bit for slot %d; stopping",
			copiesText(copies), valid, slot)
	}
	if flags.fields["BOOT_FLAGS1.KEY_INVALID"]&uint64(mask) != 0 {
		return errors.New("KEY_INVALID is set for the slot; the board will not accept this key")
	}
	u.printf("  slot %d fused and valid %s %s\n\n", slot, u.tick(), u.dim("(copies "+copiesText(copies)+")"))
	return nil
}

// picotoolSignatureCheck is the cross-tool half of gate G8: picotool
// must independently agree the file's signature verifies before it is
// flashed onto a board that enforces it.
func picotoolSignatureCheck(p *pico, path string) error {
	out, err := p.run("info", "-a", path)
	if err != nil {
		return fmt.Errorf("picotool info -a %s: %v\n%s", path, err, strings.TrimSpace(out))
	}
	for line := range strings.Lines(out) {
		l := strings.ToLower(line)
		if strings.Contains(l, "signature") && strings.Contains(l, "verified") {
			return nil
		}
	}
	return fmt.Errorf("picotool does not report the signature of %s as verified; not flashing.\n%s", path, strings.TrimSpace(out))
}

func flashAndReboot(u *ui, p *pico, path string) error {
	u.printf("  flashing %s (picotool load --verify)...\n", path)
	if out, err := p.run("load", "--verify", path); err != nil {
		return fmt.Errorf("picotool load: %v\n%s", err, strings.TrimSpace(out))
	}
	u.printf("  flash verified %s, rebooting\n", u.tick())
	if out, err := p.run("reboot"); err != nil {
		return fmt.Errorf("picotool reboot: %v\n%s (the image is flashed; a full unplug-replug also boots it)", err, strings.TrimSpace(out))
	}
	return nil
}

func cmdFlash(stdout io.Writer, args []string) error {
	fs := newFlagSet("flash", "flash [uf2]")
	lead, rest := popArg(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	u := newUI(stdout)
	image, err := oneArg(fs, lead)
	if err != nil {
		return err
	}
	if image == "" {
		image, err = findFlashable()
		if err != nil {
			return err
		}
	}
	p, board, err := connectBoard()
	if err != nil {
		return err
	}
	if err := flashPreflight(u, board, image); err != nil {
		return err
	}
	u.printf("\n")
	if err := flashAndReboot(u, p, image); err != nil {
		return err
	}
	u.printf("\n  the BOOTSEL volume disappears and the display comes up on success.\n")
	u.printf("  %s\n", u.dim("a dark screen with no BOOTSEL volume usually needs a full unplug-replug;"))
	u.printf("  %s\n\n", u.dim("reappearing in BOOTSEL means the ROM rejected the image"))
	return nil
}

// findFlashable prefers the one signed artifact, else the one image.
func findFlashable() (string, error) {
	signed, _ := filepath.Glob("*.signed.uf2")
	if len(signed) == 1 {
		return signed[0], nil
	}
	all, _ := filepath.Glob("*.uf2")
	if len(all) == 1 {
		return all[0], nil
	}
	if len(all) == 0 {
		return "", errors.New("no .uf2 image here; name one")
	}
	return "", fmt.Errorf("several images here: %s; name one", strings.Join(all, ", "))
}

// flashPreflight catches, before any flash, the failure the ROM would
// otherwise report as a silent fallback to BOOTSEL: an image this
// board's fused state rejects.
func flashPreflight(u *ui, board *otpBoard, image string) error {
	info, err := inspectUF2(image)
	if err != nil {
		return err
	}
	if !board.secureBoot {
		if info.pubKey == nil || info.sigZero {
			u.printf("  %s\n", u.dim("unsigned image on a board with secure boot off: fine, nothing is enforced"))
		}
		return nil
	}
	if info.pubKey == nil || info.sigZero {
		return fmt.Errorf("%s is unsigned and this board enforces signatures; it would fall straight back to BOOTSEL. Sign it first: sh2key sign", image)
	}
	if err := verifySignedImage(image, nil); err != nil {
		return fmt.Errorf("%s does not verify: %w", image, err)
	}
	sum := sha256.Sum256(info.pubKey)
	for i := range board.slots {
		if board.slotValid(i) && !board.slotRevoked(i) && board.slots[i].hash == sum {
			return nil
		}
	}
	return fmt.Errorf("%s is signed by a key this board does not trust (no valid slot holds %x…); the ROM would reject it", image, sum[:8])
}

func cmdEnableSecureBoot(stdout io.Writer, args []string) error {
	fs := newFlagSet("enable-secure-boot", "enable-secure-boot [-key file]")
	keyFlag := fs.String("key", defaultKeyPath, "key `file` the board must already trust")
	if err := fs.Parse(args); err != nil {
		return err
	}
	u := newUI(stdout)
	keyPath, err := resolveKeyPath(*keyFlag)
	if err != nil {
		return err
	}
	priv, err := loadKeyFile(keyPath)
	if err != nil {
		return err
	}
	p, board, err := connectBoard()
	if err != nil {
		return err
	}
	u.printf("\n")
	return enableSecureBootCore(u, p, board, priv, keyPath)
}

// enableSecureBootCore burns CRIT1.SECURE_BOOT_ENABLE behind its
// gates: a fresh sixteen-row proof of the key, offline-verified
// signed evidence, and typed consent. Population B's separate
// invocation, shared by the CLI and the TUI wizard.
func enableSecureBootCore(u *ui, p *pico, board *otpBoard, priv *secp256k1.PrivateKey, keyPath string) error {
	fp := fingerprint(priv)
	if board.secureBoot {
		u.printf("  secure boot is already enabled on this board; nothing to do\n")
		return nil
	}

	// Gate: the exact key must be fully fused and valid, proven by a
	// fresh sixteen-row comparison, not by the record.
	slot := slotOfKey(board, fp)
	if slot < 0 {
		return errors.New("this key is not fused and valid on this board; run 'sh2key provision' first. Nothing was written")
	}
	expected := expectedRows(fp)
	if board.slots[slot].rows != expected {
		return fmt.Errorf("slot %d does not read back exactly as this key's hash; refusing. Nothing was written", slot)
	}

	// Gate: offline proof of a bootable signed image. On this board
	// enforcement is off, so a successful boot proves nothing about
	// the signature chain; the file must verify on its own.
	evidence, err := findVerifiedSignedImage(pubXY(priv.PubKey()))
	if err != nil {
		return err
	}
	if err := picotoolSignatureCheck(p, evidence); err != nil {
		return err
	}
	u.printf("  key          fused and valid in slot %d %s\n", slot, u.tick())
	u.printf("  evidence     %s verifies against this key %s\n\n", u.bold(evidence), u.tick())

	u.printf("  %s\n", u.bad("ONE-WAY DOOR. After this fuse the boot ROM accepts only images signed by"))
	u.printf("  %s\n", u.bad("keys in valid slots. Enabled over a broken chain, the board is bricked"))
	u.printf("  %s\n\n", u.bad("forever: no reset, no recovery slot, no support path."))
	u.printf("  confirm the board has BOOTED the signed image (screen came up after\n  'sh2key flash %s').\n", evidence)
	expect := "ENABLE"
	if board.chipID != "" {
		expect = board.chipID[len(board.chipID)-8:]
		u.printf("  type the last 8 digits of the chip id (%s) to burn the fuse: ", u.bold(board.chipID))
	} else {
		u.printf("  type ENABLE to burn the fuse: ")
	}
	if err := u.confirmLine("", expect); err != nil {
		return err
	}

	// CRIT1 spreads over eight copies, majority-voted like
	// BOOT_FLAGS1, and set-bits-only cannot clear anything, which an
	// otp load JSON of the whole field could.
	if err := p.setRedundantBits(rowCrit1, crit1Copies, 1); err != nil {
		return err
	}
	copies, err := p.readRedundantCopies(nameCrit1, crit1Copies)
	if err != nil {
		return fmt.Errorf("could not read CRIT1's copies back: %w", err)
	}
	flags, err := p.otpQuery(false, nameCrit1)
	if err != nil {
		return err
	}
	if flags.fields["CRIT1.SECURE_BOOT_ENABLE"]&1 == 0 {
		return fmt.Errorf("CRIT1.SECURE_BOOT_ENABLE reads back 0; the fuse did not take (copies %s). Rerun 'sh2key status'", copiesText(copies))
	}
	if !copiesEqual(copies) {
		u.printf("  %s\n", u.warn("CRIT1's copies disagree ("+copiesText(copies)+"); secure boot holds by majority vote, rerun to finish the write"))
	}
	u.printf("\n  secure boot enabled %s; this board now boots only firmware signed by its fused keys\n\n", u.tick())

	records, err := loadRecords(keyPath)
	if err == nil {
		rec := boardRecord{
			ChipID: board.chipID, RandID: board.randID, Slot: slot,
			Population: "B: provisioned by sh2key", KeyFingerprint: fingerprintHex(priv),
			SecureBootEnabledByUs: true, Provisioned: nowUTC(),
		}
		if old := records.find(board.chipID); old != nil {
			old.SecureBootEnabledByUs = true
		} else {
			records.upsert(rec)
		}
		return records.save(keyPath)
	}
	return nil
}

// findVerifiedSignedImage looks for a *.signed.uf2 here that verifies
// against this key: the offline evidence gate G9 requires.
func findVerifiedSignedImage(pub []byte) (string, error) {
	matches, _ := filepath.Glob("*.signed.uf2")
	for _, m := range matches {
		if verifySignedImage(m, pub) == nil {
			return m, nil
		}
	}
	return "", errors.New("no *.signed.uf2 here verifies against this key; run 'sh2key provision' (or sign+flash) first and confirm the board boots it. Nothing was written")
}
