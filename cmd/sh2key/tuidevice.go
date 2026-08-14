package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// signScreen doubles as the flash screen: the same picker over the
// working directory's images, annotated with what inspection actually
// found, then the run transcript underneath.

type signScreen struct {
	app    *tuiApp
	flash  bool
	rows   []listRow
	files  []string
	hidden int
	cur    int
	pane   *pane
	ran    bool
}

func newSignScreen(a *tuiApp, flash bool) *signScreen {
	s := &signScreen{app: a, flash: flash, pane: &pane{app: a}}
	// The flash list is filtered by what the board will accept, so it
	// needs a scan to filter against.
	if flash && a.board == nil && a.boardErr == nil {
		a.refreshBoard()
	}
	s.rescan()
	return s
}

func (s *signScreen) rescan() {
	s.rows, s.files, s.hidden = nil, nil, 0
	matches, _ := filepath.Glob("*.uf2")
	sort.Strings(matches)
	for _, m := range matches {
		row := listRow{label: m}
		info, err := inspectUF2(m)
		switch {
		case err != nil:
			row.disabled = "unreadable: " + firstLine(err.Error())
		case info.img.NumBlocks > 2:
			row.disabled = fmt.Sprintf("%d metadata blocks: sealed twice", info.img.NumBlocks)
		case info.img.SignatureOffset == 0:
			row.detail = "no signature section"
			if !s.flash {
				row.disabled = "no signature section"
			}
		case info.sigZero:
			row.detail = "unsigned, signature section present"
		default:
			sum := sha256.Sum256(info.pubKey)
			row.detail = fmt.Sprintf("signed by %x…", sum[:8])
			if s.app.board != nil {
				if slot := slotTrusting(s.app.board, sum); slot >= 0 {
					row.detail = fmt.Sprintf("signed for slot %d  %x…", slot, sum[:8])
				}
			}
		}
		// Flashing an image this board's ROM rejects only drops it back
		// into BOOTSEL, so those images are not offered at all.
		if s.flash && !boardAccepts(s.app.board, m, info, err) {
			s.hidden++
			continue
		}
		if fi, err := os.Stat(m); err == nil {
			row.detail += fmt.Sprintf("  %.1f MB", float64(fi.Size())/(1<<20))
		}
		s.rows = append(s.rows, row)
		s.files = append(s.files, m)
	}
	s.cur = 0
	for i, r := range s.rows {
		if r.disabled == "" {
			s.cur = i
			break
		}
	}
}

// slotTrusting returns the slot whose fused hash is sum and which the
// ROM would accept, or -1.
func slotTrusting(b *otpBoard, sum [32]byte) int {
	for i := range b.slots {
		if b.slotValid(i) && !b.slotRevoked(i) && b.slots[i].hash == sum {
			return i
		}
	}
	return -1
}

// boardAccepts reports whether the attached board would boot this
// image: on a board enforcing signatures, one signed by a key in a
// valid, non-revoked slot, whose signature verifies. With no scan to
// judge by, or with enforcement off, everything is offered.
func boardAccepts(b *otpBoard, path string, info *uf2Info, ierr error) bool {
	switch {
	case b == nil:
		return true
	case ierr != nil || info == nil:
		return false
	case !b.secureBoot:
		return true
	case info.pubKey == nil || info.sigZero || info.img.NumBlocks > 2:
		return false
	}
	if slotTrusting(b, sha256.Sum256(info.pubKey)) < 0 {
		return false
	}
	return verifySignedImage(path, nil) == nil
}

func (s *signScreen) title() string {
	if s.flash {
		return "flash"
	}
	return "sign"
}

func (s *signScreen) footer() string {
	verb := "sign"
	if s.flash {
		verb = "flash"
	}
	return "↑↓ move · enter " + verb + " · g rescan files · esc back"
}

func (s *signScreen) render(w, h int) []string {
	a, u := s.app, s.app.u
	out := []string{""}
	if s.flash {
		switch {
		case a.board != nil && a.board.secureBoot:
			out = append(out, "  "+u.dim("this board boots only signed firmware; only images it accepts are listed"))
		case a.board != nil:
			out = append(out, "  "+u.dim("secure boot is off on this board: any image here can be flashed"))
		case a.boardErr != nil:
			out = append(out, "  "+u.warn(firstLine(a.boardErr.Error())),
				"  "+u.dim("without a scan the list cannot be filtered; press g after fixing it"))
		}
		out = append(out, "")
	}
	if len(s.rows) == 0 {
		if s.hidden > 0 {
			out = append(out, "  "+u.warn(fmt.Sprintf("no flashable image here: %d hidden as unsigned or signed by a key this board does not trust", s.hidden)),
				"  "+u.dim("sign one first, or provision this board with the key that signed it"))
		} else {
			out = append(out, "  "+u.dim("no .uf2 images in this directory;"),
				"  "+u.dim("build one with 'nix run .#build-firmware' or fetch the *-unsigned edge asset"))
		}
	}
	// Firmware names run long and vary in length; size the column to
	// the longest so a name can never run into its own detail.
	col := 44
	for _, r := range s.rows {
		col = max(col, len(r.label)+2)
	}
	col = min(col, max(20, w-40))
	for i, r := range s.rows {
		cur := "  "
		label, detail := r.label, r.detail
		switch {
		case r.disabled != "":
			label, detail = u.dim(label), u.dim(r.disabled)
		case i == s.cur:
			cur = u.accent(cursorMark(u)) + " "
			label, detail = u.bold(label), u.dim(detail)
		default:
			detail = u.dim(detail)
		}
		out = append(out, "  "+cur+padVisible(label, col)+" "+detail)
	}
	if s.hidden > 0 && len(s.rows) > 0 {
		out = append(out, "", "  "+u.dim(fmt.Sprintf("%d image(s) hidden: unsigned, or signed by a key this board does not trust", s.hidden)))
	}
	if lines := s.pane.tail(max(3, h-len(out)-1)); len(lines) > 0 {
		out = append(out, "")
		for _, l := range lines {
			out = append(out, "  "+l)
		}
	}
	return out
}

func (s *signScreen) handle(ev keyEvent) navAction {
	switch ev.kind {
	case keyEsc:
		return navPop{}
	case keyUp:
		s.move(-1)
	case keyDown:
		s.move(1)
	case keyChar:
		switch ev.ch {
		case 'g':
			s.rescan()
		case 'j':
			s.move(1)
		case 'k':
			s.move(-1)
		}
	case keyEnter:
		if s.cur < len(s.rows) && s.rows[s.cur].disabled == "" {
			s.run(s.files[s.cur])
		}
	}
	return nil
}

func (s *signScreen) move(d int) {
	for i := s.cur + d; 0 <= i && i < len(s.rows); i += d {
		if s.rows[i].disabled == "" {
			s.cur = i
			return
		}
	}
}

func (s *signScreen) run(file string) {
	a := s.app
	u := a.paneUI(s.pane)
	if !s.flash {
		out := signedName(file)
		if out == file {
			u.printf("%s\n", u.bad("refusing to sign "+file+" onto itself"))
			return
		}
		u.printf("signing %s with %s\n", file, a.keyPath)
		var err error
		a.working("signing "+file+"...", func() { err = signImage(u, a.priv, file, out) })
		if err != nil {
			u.printf("%s\n", u.bad(err.Error()))
			return
		}
		s.rescan()
		return
	}
	// Flash: a fresh board scan feeds the preflight; never a stale one.
	a.refreshBoard()
	if a.boardErr != nil {
		u.printf("%s\n", u.bad(firstLine(a.boardErr.Error())))
		u.printf("%s\n", u.dim("BOOTSEL dance: unplug, hold the button, plug, release; then retry"))
		return
	}
	var err error
	a.working("checking "+file+" against the board's fused keys...", func() {
		err = flashPreflight(u, a.board, file)
	})
	if err != nil {
		u.printf("%s\n", u.bad(err.Error()))
		return
	}
	a.working("writing "+file+" to the board and verifying, this takes a while...", func() {
		err = flashAndReboot(u, a.pico, file)
	})
	if err != nil {
		u.printf("%s\n", u.bad(err.Error()))
		return
	}
	u.printf("the BOOTSEL volume disappears and the display comes up on success\n")
	a.board, a.boardErr = nil, nil // the board rebooted; the scan is stale
	s.rescan()
}

// provisionScreen is the ceremony wizard, and with esb set, the
// separate secure-boot one-way door. It previews the plan from the
// current scan, then runs the exact CLI core with consent collected
// in modals; the transcript below is the CLI's own output.

type provisionScreen struct {
	app     *tuiApp
	esb     bool
	fw      string
	fwErr   error
	fwCands []string
	slotSel int // -1: the plan's automatic choice
	pane    *pane
	ran     bool
}

func newProvisionScreen(a *tuiApp, esb bool) *provisionScreen {
	p := &provisionScreen{app: a, esb: esb, slotSel: -1, pane: &pane{app: a}}
	if a.board == nil {
		a.refreshBoard()
	}
	if !esb {
		p.fwCands = unsignedCandidates()
		switch len(p.fwCands) {
		case 1:
			p.fw = p.fwCands[0]
		case 0:
			_, p.fwErr = findUnsignedUF2()
		}
	}
	return p
}

func (p *provisionScreen) title() string {
	if p.esb {
		return "enable secure boot"
	}
	return "provision"
}

func (p *provisionScreen) footer() string {
	if p.ran {
		return "r rescan board · esc back"
	}
	f := "enter run"
	if len(p.fwCands) > 1 {
		f += " · f firmware"
	}
	if p.slotChoosable() {
		f += " · s slot"
	}
	return f + " · r rescan board · esc back"
}

// slotChoosable reports whether a slot picker makes sense: a fuse is
// actually planned and more than one pristine slot exists.
func (p *provisionScreen) slotChoosable() bool {
	a := p.app
	if p.esb || a.board == nil || a.priv == nil {
		return false
	}
	plan := makeFusePlan(a.board, fingerprint(a.priv))
	return plan.kind == planFuse && len(pristineSlots(a.board)) > 1
}

func (p *provisionScreen) render(w, h int) []string {
	a, u := p.app, p.app.u
	out := []string{""}
	line := func(s string) { out = append(out, s) }

	if a.priv != nil {
		fp := fingerprintHex(a.priv)
		line("  key       " + u.bold(a.keyPath) + "  " + u.bold(fp[:16]) + u.dim(fp[16:32]+"…"))
	} else {
		line("  key       " + u.warn("none; mint or restore from home first"))
	}
	if !p.esb {
		switch {
		case p.fw != "":
			line("  firmware  " + u.bold(p.fw))
		case len(p.fwCands) > 1:
			line("  firmware  " + u.warn(fmt.Sprintf("%d unsigned images here", len(p.fwCands))) + "  " + u.accent("press f to choose"))
		case p.fwErr != nil:
			line("  firmware  " + u.warn(firstLine(p.fwErr.Error())))
		}
	}
	switch {
	case a.board != nil:
		_, label := a.board.classify()
		line("  board     " + u.bold(label) + u.dim("  chip "+shortID(a.board.chipID)))
		if a.priv != nil {
			plan, perr := applySlotChoice(makeFusePlan(a.board, fingerprint(a.priv)), p.slotSel, a.board)
			var what string
			switch {
			case perr != nil:
				what = firstLine(perr.Error())
			case plan.kind == planDone:
				what = fmt.Sprintf("key already valid in slot %d: sign and flash only, zero OTP writes", plan.slot)
			case plan.kind == planResume:
				what = fmt.Sprintf("resume the interrupted write in slot %d, then sign and flash", plan.slot)
			case plan.kind == planFuse:
				what = fmt.Sprintf("fuse slot %d (typed consent follows), then sign and flash", plan.slot)
				if p.slotChoosable() {
					if p.slotSel >= 0 {
						what += "  " + u.dim("(chosen; s to change)")
					} else {
						what += "  " + u.dim("(lowest pristine; s to change)")
					}
				}
			default:
				what = firstLine(plan.reason)
			}
			if p.esb {
				what = "burn CRIT1.SECURE_BOOT_ENABLE; gates: key fused+valid, verified signed image, typed chip id"
				if a.board.secureBoot {
					what = "secure boot is already enabled; nothing to do"
				}
			}
			line("  plan      " + what)
		}
	case a.boardErr != nil:
		for i, l := range wrapText(a.boardErr.Error(), w-14) {
			if i == 0 {
				line("  board     " + u.warn(l))
			} else {
				line("            " + u.dim(l))
			}
			if i == 7 {
				break
			}
		}
	}
	if lines := p.pane.tail(max(3, h-len(out)-2)); len(lines) > 0 {
		line("")
		for _, l := range lines {
			line("  " + l)
		}
	}
	return out
}

func (p *provisionScreen) handle(ev keyEvent) navAction {
	a := p.app
	switch {
	case ev.kind == keyEsc:
		return navPop{}
	case ev.kind == keyChar && ev.ch == 'r':
		a.refreshBoard()
	case ev.kind == keyChar && ev.ch == 'f' && !p.ran && len(p.fwCands) > 1:
		return p.firmwarePicker()
	case ev.kind == keyChar && ev.ch == 's' && !p.ran && p.slotChoosable():
		return p.slotPicker()
	case ev.kind == keyEnter && !p.ran:
		if a.priv == nil {
			return a.message(p.title(), true, "no key; mint or restore from home first")
		}
		if !p.esb && p.fw == "" {
			if len(p.fwCands) > 1 {
				// Ambiguity is a choice, not an error: enter opens
				// the picker until a firmware is chosen.
				return p.firmwarePicker()
			}
			p.fw, p.fwErr = findUnsignedUF2()
			if p.fw == "" {
				return nil
			}
		}
		// Fuses burn on whatever board is attached now: the ceremony
		// and its consent line run on a fresh scan, never on the one
		// this screen was rendering, so a board swapped in after that
		// scan cannot ride a cached identity into an irreversible
		// write. The flash path set the pattern.
		a.refreshBoard()
		if a.board == nil {
			return nil
		}
		u := a.paneUI(p.pane)
		var err error
		a.working("running the ceremony; each device step can take seconds...", func() {
			if p.esb {
				err = enableSecureBootCore(u, a.pico, a.board, a.priv, a.keyPath)
			} else {
				err = runCeremony(u, a.pico, a.board, a.priv, a.keyPath, p.fw, p.slotSel)
			}
		})
		if err != nil {
			for _, l := range strings.Split(err.Error(), "\n") {
				u.printf("%s\n", u.bad(l))
			}
			return nil
		}
		p.ran = true
		a.board, a.boardErr = nil, nil // rebooted or changed; rescan to see it
		a.records, _ = loadRecords(a.keyPath)
	}
	return nil
}

// firmwarePicker chooses among the unsigned images here, annotated
// by inspection.
func (p *provisionScreen) firmwarePicker() navAction {
	rows := make([]listRow, len(p.fwCands))
	for i, c := range p.fwCands {
		detail := "unsigned, signature section present"
		if fi, err := os.Stat(c); err == nil {
			detail += fmt.Sprintf("  %.1f MB  %s", float64(fi.Size())/(1<<20), fi.ModTime().Format("2006-01-02 15:04"))
		}
		rows[i] = listRow{label: c, detail: detail}
	}
	l := &listScreen{app: p.app, name: "choose firmware", rows: rows,
		intro: []string{"Several unsigned images here; the chosen one is signed fresh and flashed."}}
	l.pick = func(i int) navAction {
		p.fw = p.fwCands[i]
		return navPop{}
	}
	return navPush{l}
}

// slotPicker chooses the boot-key slot for a planned fuse. Only
// pristine slots are selectable; the others show why they are not.
func (p *provisionScreen) slotPicker() navAction {
	board := p.app.board
	rows := make([]listRow, otpNumSlots)
	for i := range rows {
		rows[i] = listRow{label: fmt.Sprintf("slot %d", i), detail: slotStateText(board, i)}
		if !slotPristine(board, i) {
			rows[i].disabled = slotStateText(board, i)
		}
	}
	l := &listScreen{app: p.app, name: "choose slot", rows: rows,
		intro: []string{"OTP is one-way: the chosen slot is spent forever.",
			"Gate G4 holds either way; only pristine slots are selectable."}}
	l.cursor = firstFreeSlot(board)
	l.pick = func(i int) navAction {
		p.slotSel = i
		return navPop{}
	}
	return navPush{l}
}
