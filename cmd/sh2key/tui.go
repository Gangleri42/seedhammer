package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"golang.org/x/sys/unix"
)

// The interactive face of sh2key: launch it bare on a terminal and
// every job lives behind one full-screen tool. The subcommands remain
// the scripting face; both call the same verbs underneath, so the
// safety rules (never clobber, fail-closed gates, typed consent) are
// identical in either.
//
// Plain ANSI over stdlib throughout: full repaint per frame with
// erase-to-end-of-line, an alternate screen buffer, and no terminal
// library. Deliberately synchronous: a tool that burns fuses does one
// thing at a time, visibly.

type keyKind int

const (
	keyNone keyKind = iota
	keyChar
	keyEnter
	keyEsc
	keyBackspace
	keyUp
	keyDown
	keyLeft
	keyRight
	keyCtrlC
	keyCtrlL
)

type keyEvent struct {
	kind keyKind
	ch   byte // valid for keyChar
}

// readKey decodes one key press. A lone escape is told apart from an
// escape sequence by a short poll: if no byte follows within 25ms,
// the user pressed esc.
func readKey(tty *os.File) (keyEvent, error) {
	var b [1]byte
	if _, err := tty.Read(b[:]); err != nil {
		return keyEvent{}, err
	}
	switch c := b[0]; {
	case c == 0x03:
		return keyEvent{kind: keyCtrlC}, nil
	case c == 0x0c:
		return keyEvent{kind: keyCtrlL}, nil
	case c == '\r' || c == '\n':
		return keyEvent{kind: keyEnter}, nil
	case c == 0x7f || c == 0x08:
		return keyEvent{kind: keyBackspace}, nil
	case c == 0x1b:
		if !readable(tty, 25) {
			return keyEvent{kind: keyEsc}, nil
		}
		if _, err := tty.Read(b[:]); err != nil {
			return keyEvent{kind: keyEsc}, nil
		}
		if b[0] != '[' && b[0] != 'O' {
			// Alt-<key>; ignore both.
			return keyEvent{}, nil
		}
		for {
			if _, err := tty.Read(b[:]); err != nil {
				return keyEvent{}, err
			}
			if 0x40 <= b[0] && b[0] <= 0x7e {
				switch b[0] {
				case 'A':
					return keyEvent{kind: keyUp}, nil
				case 'B':
					return keyEvent{kind: keyDown}, nil
				case 'C':
					return keyEvent{kind: keyRight}, nil
				case 'D':
					return keyEvent{kind: keyLeft}, nil
				}
				return keyEvent{}, nil
			}
		}
	case c >= 0x20 && c < 0x7f:
		return keyEvent{kind: keyChar, ch: c}, nil
	}
	return keyEvent{}, nil
}

func readable(f *os.File, timeoutMs int) bool {
	fds := []unix.PollFd{{Fd: int32(f.Fd()), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, timeoutMs)
	return err == nil && n > 0
}

// navAction is what a screen's key handler asks of the app.
type navAction interface{ nav() }

type navPop struct{}
type navPush struct{ s screen }
type navQuit struct{}

func (navPop) nav()  {}
func (navPush) nav() {}
func (navQuit) nav() {}

// screen is one full-screen view. render must be pure; handle may run
// device work and repaint mid-step through the app.
type screen interface {
	title() string
	render(w, h int) []string
	footer() string
	handle(ev keyEvent) navAction
}

type tuiApp struct {
	tty  *os.File
	u    *ui
	quit bool

	// leave exits the alternate screen; rawRestore returns the
	// terminal to cooked mode. suspend swaps both out around a child
	// process that needs the real terminal (sudo's password prompt).
	leave      func()
	rawRestore func()

	stack []screen

	// busy narrates a blocking device step. Everything here runs
	// synchronously, so the frame is painted before the call that
	// blocks and cleared after it returns: without that the tool looks
	// frozen for the seconds picotool takes.
	busy string

	// Shared state, refreshed on demand and after actions.
	keyFlag  string
	keyPath  string
	keyCands []string
	priv     *secp256k1.PrivateKey
	keyErr   error
	pico     *pico
	picoErr  error
	// buildGate is buildGateReason() from startup: why the build
	// action is disabled, empty when it can run.
	buildGate string
	board     *otpBoard
	boardErr  error
	records   *recordFile
}

func runTUI(keyFlag string) error {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("the interactive tool needs a terminal: %w", err)
	}
	defer tty.Close()
	leave := func() {
		io.WriteString(tty, "\x1b[?25h\x1b[?1049l")
	}
	restore, err := rawMode(tty, leave)
	if err != nil {
		return fmt.Errorf("terminal: %w", err)
	}
	app := &tuiApp{tty: tty, u: newUI(tty), keyFlag: keyFlag, leave: leave, rawRestore: restore}
	// suspend replaces rawRestore; the deferred call must follow it.
	defer func() { app.rawRestore() }()
	io.WriteString(tty, "\x1b[?1049h\x1b[?25l\x1b[2J")
	defer leave()
	app.reloadKey()
	app.loadPicotool()
	app.buildGate = buildGateReason()
	app.push(newHomeScreen(app))
	// Scan on arrival: the board panel is the reason to open the tool,
	// and an empty one asking to be filled in is a step for nothing.
	// The scan narrates itself, and a missing board or picotool is
	// ordinary panel content rather than a failure to start.
	app.refreshBoard()
	for !app.quit && len(app.stack) > 0 {
		app.repaint()
		ev, err := readKey(tty)
		if err != nil {
			return err
		}
		switch ev.kind {
		case keyNone:
			continue
		case keyCtrlL:
			io.WriteString(tty, "\x1b[2J")
			continue
		case keyCtrlC:
			// ctrl-c backs out of a screen and quits from home,
			// matching esc; it never yanks the terminal away.
			ev = keyEvent{kind: keyEsc}
		}
		app.dispatch(app.top().handle(ev))
	}
	return nil
}

func (a *tuiApp) dispatch(act navAction) {
	switch act := act.(type) {
	case navPop:
		if len(a.stack) > 1 {
			a.stack = a.stack[:len(a.stack)-1]
		} else {
			a.quit = true
		}
	case navPush:
		a.stack = append(a.stack, act.s)
	case navQuit:
		a.quit = true
	}
}

func (a *tuiApp) top() screen   { return a.stack[len(a.stack)-1] }
func (a *tuiApp) push(s screen) { a.stack = append(a.stack, s) }
func (a *tuiApp) pop()          { a.dispatch(navPop{}) }
func (a *tuiApp) size() (w, h int) {
	w = termWidth(a.tty)
	ws, err := unix.IoctlGetWinsize(int(a.tty.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Row == 0 {
		return w, 24
	}
	return w, int(ws.Row)
}

// reloadKey resolves and parses the local key, tolerating absence.
// Ambiguity (several key PEMs, no explicit choice) surfaces as the
// keyErr and the home screen's picker.
func (a *tuiApp) reloadKey() {
	a.priv, a.keyErr = nil, nil
	a.keyCands = keyCandidates()
	path, err := resolveKeyPath(a.keyFlag)
	if err != nil {
		a.keyPath, a.keyErr = "", err
		return
	}
	a.keyPath = path
	a.priv, a.keyErr = loadKeyFile(path)
	if a.priv != nil {
		a.records, _ = loadRecords(path)
	}
}

// keyPicker lets the user choose among the key PEMs found here; the
// attached board's scan annotates the one it is fused with.
func (a *tuiApp) keyPicker() navAction {
	cands := a.keyCands
	if len(cands) < 2 {
		return nil
	}
	rows := make([]listRow, len(cands))
	for i, c := range cands {
		detail := ""
		if priv, err := loadKeyFile(c); err == nil {
			fp := fingerprintHex(priv)
			detail = fp[:16] + "…"
			if a.board != nil {
				if s := slotOfKey(a.board, fingerprint(priv)); s >= 0 {
					detail += fmt.Sprintf("   fused in slot %d of the attached board", s)
				}
			}
		}
		rows[i] = listRow{label: c, detail: detail}
	}
	l := &listScreen{app: a, name: "choose key", rows: rows,
		intro: []string{"Several key PEMs here; the chosen one drives every screen."}}
	l.pick = func(i int) navAction {
		a.keyFlag = cands[i]
		a.reloadKey()
		return navPop{}
	}
	return navPush{l}
}

func (a *tuiApp) loadPicotool() {
	a.pico, a.picoErr = findPicotool()
}

// suspend hands the real terminal to f: out of the alternate screen,
// back to cooked mode, so a child process can own the tty (sudo's
// password prompt). It waits for enter afterwards, so f's output is
// read rather than vanished by the returning screen.
func (a *tuiApp) suspend(f func() error) error {
	io.WriteString(a.tty, "\x1b[?25h\x1b[?1049l")
	a.rawRestore()
	err := f()
	if err != nil {
		u := newUI(a.tty)
		fmt.Fprintf(a.tty, "\n  %s\n", u.bad(err.Error()))
	}
	io.WriteString(a.tty, "\n  press enter to return ")
	bufio.NewReader(a.tty).ReadString('\n')
	restore, rerr := rawMode(a.tty, a.leave)
	if rerr != nil {
		a.quit = true
		return rerr
	}
	a.rawRestore = restore
	io.WriteString(a.tty, "\x1b[?1049h\x1b[?25l\x1b[2J")
	return err
}

// working paints a frame naming the step, runs it, and clears the
// notice afterwards.
func (a *tuiApp) working(msg string, f func()) {
	a.busy = msg
	if len(a.stack) > 0 {
		a.repaint()
	}
	defer func() { a.busy = "" }()
	f()
}

// refreshBoard performs one full OTP scan; the result (or its error)
// stays on screen until the next explicit refresh.
func (a *tuiApp) refreshBoard() {
	a.working("reading the board's OTP over USB, a few seconds...", a.scanBoard)
}

func (a *tuiApp) scanBoard() {
	a.board, a.boardErr = nil, nil
	if a.pico == nil {
		// picotool may have appeared on the PATH since launch.
		a.loadPicotool()
	}
	if a.pico == nil {
		a.boardErr = a.picoErr
		return
	}
	info, err := a.pico.requireDevice()
	if err != nil {
		a.boardErr = err
		return
	}
	if err := checkRP2350(info); err != nil {
		a.boardErr = err
		return
	}
	a.board, a.boardErr = a.pico.readBoard()
}

// repaint composes chrome and the top screen into one frame and
// writes it in a single syscall-sized burst.
func (a *tuiApp) repaint() {
	w, h := a.size()
	u := a.u
	s := a.top()

	var lines []string
	head := "  " + u.bold("sh2key") + u.dim(" · SeedHammer II boot key")
	crumb := s.title()
	pad := w - 1 - visibleLen(head) - len(crumb) - 2
	if pad < 1 {
		pad = 1
	}
	lines = append(lines, head+strings.Repeat(" ", pad)+u.dim(crumb))
	lines = append(lines, "  "+u.dim(rule(u, w-4)))
	body := s.render(w, h-4)
	lines = append(lines, body...)
	for len(lines) < h-2 {
		lines = append(lines, "")
	}
	lines = lines[:h-2]
	lines = append(lines, "  "+u.dim(rule(u, w-4)))
	if a.busy != "" {
		spin := "*"
		if u.unicode {
			spin = "◆"
		}
		lines = append(lines, "  "+u.accent(spin)+" "+u.bold(a.busy))
	} else {
		lines = append(lines, "  "+u.dim(s.footer()))
	}

	var b strings.Builder
	b.WriteString("\x1b[H")
	for i, l := range lines {
		b.WriteString(clip(l, w-1))
		b.WriteString("\x1b[K")
		if i < len(lines)-1 {
			b.WriteString("\r\n")
		}
	}
	io.WriteString(a.tty, b.String())
}

func rule(u *ui, n int) string {
	if n < 1 {
		n = 1
	}
	if u.unicode {
		return strings.Repeat("─", n)
	}
	return strings.Repeat("-", n)
}

// stripANSI removes escape sequences, leaving printable text.
func stripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inEsc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inEsc:
			if 0x40 <= c && c <= 0x7e && c != '[' {
				inEsc = false
			}
		case c == 0x1b:
			inEsc = true
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// visibleLen counts printable columns, skipping ANSI sequences and
// counting each UTF-8 rune once.
func visibleLen(s string) int {
	n := 0
	inEsc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inEsc:
			if 0x40 <= c && c <= 0x7e && c != '[' {
				inEsc = false
			}
		case c == 0x1b:
			inEsc = true
		case c&0xc0 == 0x80:
		default:
			n++
		}
	}
	return n
}

func (a *tuiApp) shortKeyLine() string {
	switch {
	case a.priv != nil:
		return a.keyPath + "  " + fingerprintHex(a.priv)[:16]
	case a.keyErr != nil:
		return "no key"
	default:
		return ""
	}
}
