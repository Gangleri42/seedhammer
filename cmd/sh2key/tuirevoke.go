package main

import (
	"fmt"
	"strings"
)

// revokeScreen picks a slot and shows what revoking it would leave
// behind before anything is written. Slots the gates would refuse are
// listed with the reason, so the screen explains the rule rather than
// hiding the option.

type revokeScreen struct {
	app  *tuiApp
	cur  int
	pane *pane
	ran  bool
}

func newRevokeScreen(a *tuiApp) *revokeScreen {
	s := &revokeScreen{app: a, pane: &pane{app: a}}
	if a.board == nil && a.boardErr == nil {
		a.refreshBoard()
	}
	s.cur = s.firstAllowed()
	return s
}

func (s *revokeScreen) title() string { return "revoke" }

func (s *revokeScreen) footer() string {
	if s.app.board == nil {
		return "r rescan board · esc back"
	}
	return "↑↓ move · enter revoke (final) · r rescan board · esc back"
}

func (s *revokeScreen) plans() []revokePlan {
	a := s.app
	if a.board == nil {
		return nil
	}
	out := make([]revokePlan, otpNumSlots)
	for i := range out {
		out[i] = makeRevokePlan(a.board, a.priv, a.keyPath, i)
	}
	return out
}

func (s *revokeScreen) firstAllowed() int {
	for i, p := range s.plans() {
		if p.refuse == "" {
			return i
		}
	}
	return 0
}

func (s *revokeScreen) render(w, h int) []string {
	a, u := s.app, s.app.u
	out := []string{""}
	if a.board == nil {
		if a.boardErr != nil {
			for _, l := range wrapText(a.boardErr.Error(), w-6) {
				out = append(out, "  "+u.warn(l))
			}
		} else {
			out = append(out, "  "+u.dim("no board scanned; press r with it in BOOTSEL"))
		}
		return out
	}
	out = append(out,
		"  "+u.bad("Revoking is final: KEY_INVALID outranks KEY_VALID and never clears."),
		"  "+u.dim("A slot is only offered when something provably bootable would remain."),
		"")
	for i, p := range s.plans() {
		cur := "  "
		label := fmt.Sprintf("slot %d", i)
		detail := p.holds
		switch {
		case p.refuse != "":
			label, detail = u.dim(label), u.dim(firstLine(p.refuse))
		case i == s.cur:
			cur = u.accent(cursorMark(u)) + " "
			label, detail = u.bold(label), u.dim(detail)
		default:
			detail = u.dim(detail)
		}
		out = append(out, "  "+cur+padVisible(label, 10)+detail)
	}
	if p := s.plans(); len(p) > s.cur && p[s.cur].refuse == "" {
		out = append(out, "")
		for _, b := range p[s.cur].bootable {
			out = append(out, "  "+u.dim("afterwards: "+b))
		}
	}
	if lines := s.pane.tail(max(3, h-len(out)-2)); len(lines) > 0 {
		out = append(out, "")
		for _, l := range lines {
			out = append(out, "  "+l)
		}
	}
	return out
}

func (s *revokeScreen) handle(ev keyEvent) navAction {
	a := s.app
	plans := s.plans()
	switch {
	case ev.kind == keyEsc:
		return navPop{}
	case ev.kind == keyChar && ev.ch == 'r':
		a.refreshBoard()
		s.cur = s.firstAllowed()
	case ev.kind == keyUp:
		s.move(plans, -1)
	case ev.kind == keyDown:
		s.move(plans, 1)
	case ev.kind == keyEnter && !s.ran && len(plans) > s.cur:
		if plans[s.cur].refuse != "" {
			return nil
		}
		// Fuses burn on whatever board is attached now: rescan and
		// re-derive the slot gate from the fresh state, never from
		// the cached scan the cursor was browsing. The refusal shows
		// up on the redrawn screen, which lists every slot's reason.
		a.refreshBoard()
		if a.board == nil {
			return nil
		}
		if p := makeRevokePlan(a.board, a.priv, a.keyPath, s.cur); p.refuse != "" {
			return nil
		}
		u := a.paneUI(s.pane)
		var err error
		a.working(fmt.Sprintf("revoking slot %d...", s.cur), func() {
			err = revokeCore(u, a.pico, a.board, a.priv, a.keyPath, s.cur)
		})
		if err != nil {
			for _, l := range strings.Split(err.Error(), "\n") {
				u.printf("%s\n", u.bad(l))
			}
			return nil
		}
		s.ran = true
		a.refreshBoard()
	}
	return nil
}

func (s *revokeScreen) move(plans []revokePlan, d int) {
	for i := s.cur + d; 0 <= i && i < len(plans); i += d {
		if plans[i].refuse == "" {
			s.cur = i
			return
		}
	}
}
