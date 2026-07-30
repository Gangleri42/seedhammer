package main

import (
	"strings"
)

// pane is an io.Writer that turns the ceremony verbs' command-style
// output into screen lines, repainting after every write. The CLI
// transcript IS the wizard transcript; nothing renders twice.
type pane struct {
	app      *tuiApp
	lines    []string
	partial  string
	progress bool // the last line is a \r-updating progress line
}

func (p *pane) Write(b []byte) (int, error) {
	width := 76
	if p.app.tty != nil {
		w, _ := p.app.size()
		width = max(40, w-8)
	}
	s := p.partial + string(b)
	for {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimSuffix(s[:i], "\r")
		if cr := strings.LastIndexByte(line, '\r'); cr >= 0 {
			// A \r-updating progress line replaces itself.
			line = line[cr+1:]
			if n := len(p.lines); n > 0 && p.progress {
				p.lines = p.lines[:n-1]
			}
		}
		p.lines = append(p.lines, wrapStyled(line, width)...)
		p.progress = false
		s = s[i+1:]
	}
	// A trailing \r-fragment is a live progress update: show it as a
	// provisional line replaced on the next write.
	if cr := strings.LastIndexByte(s, '\r'); cr >= 0 {
		if p.progress {
			p.lines = p.lines[:len(p.lines)-1]
		}
		p.lines = append(p.lines, s[cr+1:])
		p.progress = true
		s = ""
	}
	p.partial = s
	p.app.repaint()
	return len(b), nil
}

func (p *pane) tail(n int) []string {
	lines := p.lines
	if p.partial != "" {
		lines = append(append([]string{}, lines...), p.partial)
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// paneUI builds a ui rendering into a pane with the app's palette.
func (a *tuiApp) paneUI(p *pane) *ui {
	return &ui{w: p, color: a.u.color, unicode: a.u.unicode, confirm: a.modalConfirm}
}

// wrapStyled wraps a transcript line without letting a style bleed
// across rows. Pane callers style whole lines (u.bad on an error), so
// a leading SGR prefix is recovered and reapplied per wrapped row.
func wrapStyled(line string, width int) []string {
	if visibleLen(line) <= width {
		return []string{line}
	}
	if !strings.Contains(line, "\x1b") {
		return wrapText(line, width)
	}
	prefix := ""
	if strings.HasPrefix(line, "\x1b[") {
		if i := strings.IndexByte(line, 'm'); i >= 0 {
			prefix = line[:i+1]
		}
	}
	segs := wrapText(stripANSI(line), width)
	if prefix == "" {
		return segs
	}
	for i := range segs {
		segs[i] = prefix + segs[i] + sgrReset
	}
	return segs
}

// messageScreen shows lines until any key. bad renders the first line
// as an error.
type messageScreen struct {
	app   *tuiApp
	name  string
	lines []string
	bad   bool
}

func (m *messageScreen) title() string  { return m.name }
func (m *messageScreen) footer() string { return "any key to go back" }
func (m *messageScreen) handle(keyEvent) navAction {
	return navPop{}
}

func (m *messageScreen) render(w, h int) []string {
	u := m.app.u
	out := []string{""}
	for i, l := range m.lines {
		if i == 0 && m.bad {
			out = append(out, "  "+u.bad(l))
			continue
		}
		out = append(out, "  "+l)
	}
	return out
}

func (a *tuiApp) message(name string, bad bool, lines ...string) navAction {
	return navPush{&messageScreen{app: a, name: name, lines: lines, bad: bad}}
}

func (a *tuiApp) errorScreen(name string, err error) navAction {
	return a.message(name, true, strings.Split(err.Error(), "\n")...)
}

// inputScreen is a one-line editor used through modalInput.
type inputScreen struct {
	app      *tuiApp
	name     string
	intro    []string
	prompt   string
	text     string
	done     bool
	accepted bool
}

func (in *inputScreen) title() string  { return in.name }
func (in *inputScreen) footer() string { return "enter confirm · esc cancel" }

func (in *inputScreen) render(w, h int) []string {
	u := in.app.u
	out := []string{""}
	for _, l := range in.intro {
		out = append(out, "  "+l)
	}
	caret := "_"
	if u.unicode {
		caret = "█"
	}
	out = append(out, "", "  "+in.prompt+" "+u.bold(in.text)+u.accent(caret))
	return out
}

func (in *inputScreen) handle(ev keyEvent) navAction {
	switch ev.kind {
	case keyEnter:
		in.done, in.accepted = true, true
	case keyEsc:
		in.done, in.accepted = true, false
	case keyBackspace:
		if in.text != "" {
			in.text = in.text[:len(in.text)-1]
		}
	case keyChar:
		if len(in.text) < 128 {
			in.text += string(ev.ch)
		}
	}
	return nil
}

// modalInput runs a nested event loop around an inputScreen: the
// synchronous ceremony steps stay synchronous, and consent is
// collected without a callback in sight.
func (a *tuiApp) modalInput(name string, intro []string, prompt, prefill string) (string, bool) {
	in := &inputScreen{app: a, name: name, intro: intro, prompt: prompt, text: prefill}
	a.push(in)
	// A prompt inside a running step owns the footer: the step is
	// waiting on the operator, not working.
	busy := a.busy
	a.busy = ""
	defer func() {
		a.busy = busy
		a.pop()
	}()
	for !in.done {
		a.repaint()
		ev, err := readKey(a.tty)
		if err != nil {
			return "", false
		}
		if ev.kind == keyCtrlC {
			ev = keyEvent{kind: keyEsc}
		}
		in.handle(ev)
	}
	return strings.TrimSpace(in.text), in.accepted
}

// modalConfirm is the ui.confirm hook: same contract as the CLI's
// typed consent, collected in a modal.
func (a *tuiApp) modalConfirm(prompt, expect string) error {
	intro := []string{}
	if p := strings.TrimSpace(prompt); p != "" {
		intro = append(intro, p)
	}
	shown := "type " + strings.ToUpper(expect) + " to proceed, esc to stop:"
	if expect == "y" {
		shown = "type y to proceed, esc to stop:"
	}
	text, ok := a.modalInput("confirm", intro, shown, "")
	if !ok {
		return errConfirmDeclined
	}
	if !strings.EqualFold(text, expect) {
		return errConfirmMismatch
	}
	return nil
}

var (
	errConfirmDeclined = errStr("declined; nothing was written")
	errConfirmMismatch = errStr("confirmation did not match; nothing was written")
)

// listScreen is a picker: rows with annotations, some disabled.
type listScreen struct {
	app    *tuiApp
	name   string
	intro  []string
	rows   []listRow
	cursor int
	pick   func(i int) navAction
}

type listRow struct {
	label    string
	detail   string
	disabled string // non-empty: why this row cannot be picked
}

func (l *listScreen) title() string  { return l.name }
func (l *listScreen) footer() string { return "↑↓ move · enter select · esc back" }

func (l *listScreen) render(w, h int) []string {
	u := l.app.u
	out := []string{""}
	for _, in := range l.intro {
		out = append(out, "  "+in)
	}
	if len(l.intro) > 0 {
		out = append(out, "")
	}
	for i, r := range l.rows {
		cur := "  "
		label := r.label
		detail := r.detail
		switch {
		case r.disabled != "":
			label = u.dim(label)
			detail = u.dim(r.disabled)
		case i == l.cursor:
			cur = u.accent(cursorMark(u)) + " "
			label = u.bold(label)
			detail = u.dim(detail)
		default:
			detail = u.dim(detail)
		}
		out = append(out, "  "+cur+padVisible(label, 36)+detail)
	}
	return out
}

func (l *listScreen) handle(ev keyEvent) navAction {
	switch ev.kind {
	case keyEsc:
		return navPop{}
	case keyUp:
		l.move(-1)
	case keyDown:
		l.move(1)
	case keyChar:
		if '1' <= ev.ch && ev.ch <= '9' {
			if i := int(ev.ch - '1'); i < len(l.rows) && l.rows[i].disabled == "" {
				l.cursor = i
				return l.pick(i)
			}
		}
	case keyEnter:
		if l.rows[l.cursor].disabled == "" {
			return l.pick(l.cursor)
		}
	}
	return nil
}

func (l *listScreen) move(d int) {
	for i := l.cursor + d; 0 <= i && i < len(l.rows); i += d {
		if l.rows[i].disabled == "" {
			l.cursor = i
			return
		}
	}
}

func cursorMark(u *ui) string {
	if u.unicode {
		return "▸"
	}
	return ">"
}

// padVisible pads a styled string to n printable columns.
func padVisible(s string, n int) string {
	if d := n - visibleLen(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}
