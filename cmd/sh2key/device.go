package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// pico wraps the picotool binary. Every device access goes through
// run so tests can substitute canned transcripts, and every OTP
// parse is anchored on values rather than on picotool's column
// layout, which is version-sensitive.
type pico struct {
	exe string
	run func(args ...string) (string, error)
}

func findPicotool() (*pico, error) {
	exe, err := exec.LookPath("picotool")
	if err != nil {
		return nil, errors.New(`picotool not found in PATH; the device steps need version 2.0 or later.
  nix develop            provides it in this repo's dev shell
  brew install picotool  on macOS
  otherwise: pre-built binaries from the pico-sdk-tools releases`)
	}
	p := &pico{exe: exe}
	p.run = func(args ...string) (string, error) {
		cmd := exec.Command(exe, args...)
		var buf bytes.Buffer
		cmd.Stdout = &buf
		cmd.Stderr = &buf
		err := cmd.Run()
		return buf.String(), err
	}
	if out, err := p.run("version"); err == nil {
		if m := regexp.MustCompile(`v?(\d+)\.\d+`).FindStringSubmatch(out); m != nil {
			if major, _ := strconv.Atoi(m[1]); major < 2 {
				return nil, fmt.Errorf("picotool %s is too old; the OTP commands need 2.0 or later", strings.TrimSpace(out))
			}
		}
	}
	return p, nil
}

// errNoUSBAccess marks the permission flavor of a failed device
// probe, so the TUI can offer setup-udev right where it hurts.
var errNoUSBAccess = errors.New("the device is present but cannot be opened")

var errNotBootsel = errors.New(`no RP2350 in BOOTSEL mode was found.

  Enter BOOTSEL: unplug USB, hold the BOOTSEL button on the board,
  plug USB back in while holding it, release after about a second,
  then rerun this command. OTP state is never inferred from an
  absent device.`)

// requireDevice is gate G1: confirm a device answers before reading
// any OTP state, and turn the two classic failures into their fixes.
// The permission patterns are checked before the not-found line
// because picotool prints both in the same breath - "No accessible
// RP-series devices" first, then "appears to be in BOOTSEL mode, but
// picotool was unable to connect. Maybe try 'sudo'" - and the second
// half is the truth.
func (p *pico) requireDevice() (string, error) {
	out, err := p.run("info")
	switch {
	case strings.Contains(out, "unable to connect") || strings.Contains(out, "not able to be opened") ||
		strings.Contains(out, "Permission denied") || strings.Contains(out, "try 'sudo'"):
		fix := "run 'sh2key setup-udev' once; it shows and installs the one udev rule picotool needs"
		if runtime.GOOS == "darwin" {
			fix = "retry with sudo"
		}
		return "", fmt.Errorf("%w; USB permission problem: %s\n%s", errNoUSBAccess, fix, strings.TrimSpace(out))
	case strings.Contains(out, "No accessible RP-series devices"):
		return "", errNotBootsel
	case err != nil:
		return "", fmt.Errorf("picotool info: %v\n%s", err, strings.TrimSpace(out))
	}
	return out, nil
}

// picotool's `otp get` prints block-structured output, one block per
// row:
//
//	ROW 0x004b: OTP_DATA_BOOT_FLAGS1 (RBIT-3)
//
//	    VALUE 0x000003
//
//	    field KEY_VALID (bits 0-3) = 3
//	    field KEY_INVALID (bits 8-11) = 0
//
// VALUE is the raw 24-bit row (for ECC rows the data is the low 16
// bits; -e applies correction first); field lines are decoded
// decimals. -n suppresses the quoted descriptions so free text can
// never be mistaken for data. With -c, a redundant row also prints a
// RAW_VALUE line holding every copy, which is the only way to see a
// write that reached some copies and not others.
var (
	otpRowRe   = regexp.MustCompile(`^ROW (0[xX][0-9a-fA-F]+)(?::\s*OTP_DATA_([A-Za-z0-9_]+))?`)
	otpValueRe = regexp.MustCompile(`^\s*VALUE 0[xX]([0-9a-fA-F]+)`)
	otpFieldRe = regexp.MustCompile(`^\s*field ([A-Za-z0-9_]+) \([^)]*\) = (0[xX][0-9a-fA-F]+|\d+)`)
	otpRawRe   = regexp.MustCompile(`^\s*RAW_VALUE=([0-9a-fA-Fx;X]+)`)
)

// Redundant flag rows: their addresses and copy counts, mirroring
// driver/otp/otp.go's row constants and its writeOrRow calls
// (BOOT_FLAGS1 across 3 rows, CRIT1 across 8). The copies of a
// redundant row are consecutive rows starting at the named one, and
// the boot ROM reads the row as a bitwise majority vote across them,
// so a bit present in only some copies can be outvoted and have no
// effect.
const (
	nameBootFlags1   = "BOOT_FLAGS1"
	rowBootFlags1    = 0x04b
	bootFlags1Copies = 3
	nameCrit1        = "CRIT1"
	rowCrit1         = 0x040
	crit1Copies      = 8
)

// rowSelector names an absolute OTP row for picotool.
func rowSelector(row int) string {
	return fmt.Sprintf("0x%x", row)
}

// setRedundantBits ORs mask into every copy of a redundant row, each
// addressed by its own row number. Writing the row by its field name
// reaches only the first copy, and picotool's -c does not take a
// count on this build, so the copies are written one by one, exactly
// as the firmware's writeOrRow does. Set-bits-only throughout: the
// write can never clear a bit.
func (p *pico) setRedundantBits(rowBase, copies int, mask uint32) error {
	for i := range copies {
		sel := rowSelector(rowBase + i)
		out, err := p.run("otp", "set", "-s", sel, fmt.Sprintf("0x%x", mask))
		if err != nil {
			return fmt.Errorf("picotool otp set %s: %v\n%s", sel, err, strings.TrimSpace(out))
		}
	}
	return nil
}

// readRedundantCopies returns the copies of a redundant row. Reading
// it by name is what tells the truth: picotool prints a RAW_VALUE line
// listing every copy exactly when they disagree, and otherwise the one
// decoded VALUE is what all copies hold. (Selecting a copy by row
// number instead reads that row alone, and the first copy's number
// resolves back to the named row and its majority vote, so per-number
// reads cannot distinguish the two cases.)
func (p *pico) readRedundantCopies(name string, copies int) ([]uint32, error) {
	r, err := p.otpQuery(false, name)
	if err != nil {
		return nil, err
	}
	if c := r.copies[name]; len(c) > 0 {
		return c, nil
	}
	v, ok := r.rows[name]
	if !ok {
		return nil, fmt.Errorf("could not read OTP row %s", name)
	}
	out := make([]uint32, copies)
	for i := range out {
		out[i] = v
	}
	return out, nil
}

// otpRead is one parsed `otp get` result: decoded row values, decoded
// fields, and the raw redundant copies when picotool printed them.
type otpRead struct {
	rows   map[string]uint32
	fields map[string]uint64
	copies map[string][]uint32
}

// A disagreement among a redundant row's copies means an interrupted
// write: the majority vote decides what the boot ROM sees, so the
// minority bits are not yet in effect. See copiesEqual/copiesText.

// otpQuery reads rows and fields by selector. Rows are keyed by their
// address ("0x4c") and, when picotool names them, by name as well. A
// selector picotool printed nothing for is simply absent from the
// result, and the caller decides whether that is fatal.
func (p *pico) otpQuery(ecc bool, selectors ...string) (otpRead, error) {
	args := []string{"otp", "get", "-n"}
	if ecc {
		args = append(args, "-e")
	}
	args = append(args, selectors...)
	out, err := p.run(args...)
	if err != nil && len(selectors) > 1 {
		// Degrade to one call per selector if a build rejects many.
		merged := otpRead{rows: map[string]uint32{}, fields: map[string]uint64{}, copies: map[string][]uint32{}}
		for _, s := range selectors {
			r, serr := p.otpQuery(ecc, s)
			if serr != nil {
				continue
			}
			for k, v := range r.rows {
				merged.rows[k] = v
			}
			for k, v := range r.fields {
				merged.fields[k] = v
			}
			for k, v := range r.copies {
				merged.copies[k] = v
			}
		}
		if len(merged.rows) > 0 || len(merged.fields) > 0 {
			return merged, nil
		}
		return otpRead{}, fmt.Errorf("picotool otp get: %v\n%s", err, strings.TrimSpace(out))
	}
	if err != nil {
		return otpRead{}, fmt.Errorf("picotool otp get %s: %v\n%s", selectors[0], err, strings.TrimSpace(out))
	}
	return parseOTPBlocks(out), nil
}

func parseOTPBlocks(out string) otpRead {
	r := otpRead{
		rows:   make(map[string]uint32),
		fields: make(map[string]uint64),
		copies: make(map[string][]uint32),
	}
	// keys are the row's address and, when named, its name; a value
	// line applies to both.
	var keys []string
	setRow := func(v uint32) {
		for _, k := range keys {
			if _, dup := r.rows[k]; !dup {
				r.rows[k] = v
			}
		}
	}
	for line := range strings.Lines(out) {
		if m := otpRowRe.FindStringSubmatch(line); m != nil {
			addr, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimPrefix(m[1], "0x"), "0X"), 16, 32)
			keys = nil
			if err == nil {
				keys = append(keys, rowSelector(int(addr)))
			}
			if m[2] != "" {
				keys = append(keys, m[2])
			}
			continue
		}
		if len(keys) == 0 {
			continue
		}
		current := keys[len(keys)-1]
		if m := otpRawRe.FindStringSubmatch(line); m != nil {
			if _, dup := r.copies[current]; !dup {
				var vals []uint32
				for _, f := range strings.Split(m[1], ";") {
					f = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(f), "0x"), "0X")
					if v, err := strconv.ParseUint(f, 16, 32); err == nil {
						vals = append(vals, uint32(v))
					}
				}
				if len(vals) > 0 {
					r.copies[current] = vals
				}
			}
			continue
		}
		if m := otpValueRe.FindStringSubmatch(line); m != nil {
			if v, err := strconv.ParseUint(m[1], 16, 32); err == nil {
				setRow(uint32(v))
			}
			continue
		}
		if m := otpFieldRe.FindStringSubmatch(line); m != nil {
			key := current + "." + m[1]
			val := m[2]
			var v uint64
			var perr error
			if strings.HasPrefix(val, "0x") || strings.HasPrefix(val, "0X") {
				v, perr = strconv.ParseUint(val[2:], 16, 64)
			} else {
				v, perr = strconv.ParseUint(val, 10, 64)
			}
			if perr == nil {
				r.fields[key] = v
			}
		}
	}
	return r
}

// eccData extracts the 16 data bits from a raw 24-bit ECC row value.
func eccData(raw uint32) uint16 {
	return uint16(raw & 0xffff)
}

// otpBoard is one full OTP scan: the tuple that classifies a board.
type otpBoard struct {
	secureBoot bool
	keyValid   uint8
	keyInvalid uint8
	slots      [4]otpSlot
	chipID     string // 16 hex digits, or "" when unreadable
	randID     string
	// Redundant copies of the flag rows as read. Unequal copies mean
	// an interrupted write; the majority vote above is what the boot
	// ROM acts on until the write is completed.
	flagCopies []uint32
	critCopies []uint32
}

// redundancyWarnings reports interrupted writes of the flag rows.
// The decoded values above are the majority vote, which is what the
// boot ROM acts on; a disagreement means some bits are written but
// not yet in effect, and completing the write is a pure 0-to-1
// operation that provision performs.
func (b *otpBoard) redundancyWarnings() []string {
	var out []string
	if len(b.flagCopies) > 1 && !copiesEqual(b.flagCopies) {
		out = append(out, "BOOT_FLAGS1's "+fmt.Sprint(len(b.flagCopies))+" redundant copies disagree ("+copiesText(b.flagCopies)+
			"): an interrupted valid-bit write. The majority vote above is what the ROM sees; provision completes it.")
	}
	if len(b.critCopies) > 1 && !copiesEqual(b.critCopies) {
		out = append(out, "CRIT1's "+fmt.Sprint(len(b.critCopies))+" redundant copies disagree ("+copiesText(b.critCopies)+
			"): an interrupted critical-flag write.")
	}
	return out
}

func copiesEqual(c []uint32) bool {
	for i := 1; i < len(c); i++ {
		if c[i] != c[0] {
			return false
		}
	}
	return true
}

func copiesText(c []uint32) string {
	parts := make([]string, len(c))
	for i, v := range c {
		parts[i] = fmt.Sprintf("0x%06x", v)
	}
	return strings.Join(parts, ";")
}

type otpSlot struct {
	readable bool
	rows     [16]uint16
	hash     [32]byte
	zero     bool
}

func (s *otpSlot) assemble() {
	s.zero = true
	for i, r := range s.rows {
		s.hash[2*i] = byte(r)
		s.hash[2*i+1] = byte(r >> 8)
		if r != 0 {
			s.zero = false
		}
	}
}

// expectedRows converts a key hash to the sixteen OTP row values, low
// byte first, matching picotool's bootkey array expansion. Its
// inverse lives in otpSlot.assemble; the pair is pinned by a test
// against the worked example in the signing howto.
func expectedRows(hash [32]byte) [16]uint16 {
	var rows [16]uint16
	for i := range rows {
		rows[i] = uint16(hash[2*i]) | uint16(hash[2*i+1])<<8
	}
	return rows
}

func (p *pico) readBoard() (*otpBoard, error) {
	b := &otpBoard{}
	flagRead, err := p.otpQuery(false, "CRIT1", "BOOT_FLAGS1")
	if err != nil {
		return nil, err
	}
	fields := flagRead.fields
	for _, n := range []string{"CRIT1.SECURE_BOOT_ENABLE", "BOOT_FLAGS1.KEY_VALID", "BOOT_FLAGS1.KEY_INVALID"} {
		if _, ok := fields[n]; !ok {
			return nil, fmt.Errorf("could not read %s; refusing to classify the board", n)
		}
	}
	b.secureBoot = fields["CRIT1.SECURE_BOOT_ENABLE"]&1 != 0
	b.keyValid = uint8(fields["BOOT_FLAGS1.KEY_VALID"] & 0xf)
	b.keyInvalid = uint8(fields["BOOT_FLAGS1.KEY_INVALID"] & 0xf)
	// The decoded values above are the majority vote. Read every copy
	// by row number too, so an interrupted write is visible instead of
	// hidden behind that vote.
	b.flagCopies, _ = p.readRedundantCopies(nameBootFlags1, bootFlags1Copies)
	b.critCopies, _ = p.readRedundantCopies(nameCrit1, crit1Copies)

	var rowNames []string
	for slot := range 4 {
		for row := range 16 {
			rowNames = append(rowNames, fmt.Sprintf("BOOTKEY%d_%d", slot, row))
		}
	}
	keyRead, err := p.otpQuery(true, rowNames...)
	if err != nil {
		return nil, err
	}
	for slot := range 4 {
		s := &b.slots[slot]
		s.readable = true
		for row := range 16 {
			v, ok := keyRead.rows[fmt.Sprintf("BOOTKEY%d_%d", slot, row)]
			if !ok {
				// An ECC read error counts as "not pristine", never
				// as empty (gate G4).
				s.readable = false
				continue
			}
			s.rows[row] = eccData(v)
		}
		s.assemble()
	}

	// The board pin. Present on every RP2350 including virgin parts;
	// an unreadable pin degrades recognition, nothing else.
	idNames := []string{
		"CHIPID0", "CHIPID1", "CHIPID2", "CHIPID3",
		"RANDID0", "RANDID1", "RANDID2", "RANDID3",
	}
	if ids, err := p.otpQuery(true, idNames...); err == nil {
		b.chipID = assembleID(ids.rows, "CHIPID")
		b.randID = assembleID(ids.rows, "RANDID")
	}
	return b, nil
}

func assembleID(vals map[string]uint32, prefix string) string {
	var parts [4]uint16
	for i := range 4 {
		v, ok := vals[fmt.Sprintf("%s%d", prefix, i)]
		if !ok {
			return ""
		}
		parts[i] = eccData(v)
	}
	// Row 0 holds the least significant 16 bits.
	return fmt.Sprintf("%04x%04x%04x%04x", parts[3], parts[2], parts[1], parts[0])
}

func (b *otpBoard) slotValid(i int) bool   { return b.keyValid&(1<<i) != 0 }
func (b *otpBoard) slotRevoked(i int) bool { return b.keyInvalid&(1<<i) != 0 }

// population is the organising idea of the ceremony: boards are
// classified by OTP state, never by branding.
type population int

const (
	popA population = iota // secure boot on, at least one valid key
	popB                   // virgin: no fuse of interest set
	popC                   // partial, foreign, or inconsistent
)

func (b *otpBoard) classify() (population, string) {
	allZero := true
	allReadable := true
	for i := range b.slots {
		if !b.slots[i].readable {
			allReadable = false
		}
		if !b.slots[i].zero {
			allZero = false
		}
	}
	switch {
	case b.secureBoot && b.keyValid != 0:
		label := "A: secure boot on, locked RP2350"
		if b.holdsManufacturerKey() {
			label = "A: locked SeedHammer II"
		}
		return popA, label
	case !b.secureBoot && b.keyValid == 0 && b.keyInvalid == 0 && allZero && allReadable:
		return popB, "B: virgin board, secure boot off, nothing fused"
	case b.secureBoot && b.keyValid == 0:
		return popC, "C: secure boot on with no valid key; this board cannot boot"
	default:
		return popC, "C: partially fused or foreign OTP state"
	}
}

// signKeyHashSH2 mirrors signKeyHash in cmd/controller/platform_sh2.go:
// the SHA-256 of SeedHammer's manufacturer signing key. Display-only;
// classification never keys on branding.
const signKeyHashSH2 = "c8314536d6af61ac2e62e5991e3e4711629c54696ba8c4af08965a1d319a473b"

func (b *otpBoard) holdsManufacturerKey() bool {
	for i := range b.slots {
		if b.slotValid(i) && !b.slotRevoked(i) && hexOf(b.slots[i].hash) == signKeyHashSH2 {
			return true
		}
	}
	return false
}

func hexOf(h [32]byte) string {
	return fmt.Sprintf("%x", h[:])
}

// otpLoadJSON writes an OTP JSON via a file in a fresh temp dir,
// never next to the key: stale picotool byproducts in a working
// directory are exactly how wrong hashes get fused.
func (p *pico) otpLoadJSON(jsonBody string) (string, error) {
	dir, err := os.MkdirTemp("", "sh2key-otp-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	path := dir + "/otp.json"
	if err := os.WriteFile(path, []byte(jsonBody), 0o600); err != nil {
		return "", err
	}
	out, err := p.run("otp", "load", path)
	if err != nil {
		return out, fmt.Errorf("picotool otp load: %v\n%s", err, strings.TrimSpace(out))
	}
	return out, nil
}
