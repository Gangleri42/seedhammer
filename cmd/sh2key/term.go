package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// ui carries the styling decisions for one output stream. Colors are
// ANSI over stdlib, off for pipes, NO_COLOR (any value, per the
// no-color.org convention) and TERM=dumb; glyphs degrade to ASCII
// outside UTF-8 locales.
type ui struct {
	w       io.Writer
	color   bool
	unicode bool
	// confirm overrides how one typed line of consent is collected.
	// nil means the default: a canonical-mode read from /dev/tty. The
	// TUI substitutes its inline input field; the gate semantics
	// (exact match or nothing was written) stay identical.
	confirm func(prompt, expect string) error
}

func newUI(w io.Writer) *ui {
	u := &ui{w: w}
	f, isFile := w.(*os.File)
	if !isFile || !isTerminal(f) {
		return u
	}
	if _, noColor := os.LookupEnv("NO_COLOR"); noColor || os.Getenv("TERM") == "dumb" {
		return u
	}
	u.color = true
	locale := strings.ToLower(os.Getenv("LC_ALL") + os.Getenv("LC_CTYPE") + os.Getenv("LANG"))
	u.unicode = strings.Contains(locale, "utf-8") || strings.Contains(locale, "utf8")
	return u
}

const (
	sgrReset  = "\x1b[0m"
	sgrBold   = "\x1b[1m"
	sgrDim    = "\x1b[2m"
	sgrRed    = "\x1b[31;1m"
	sgrGreen  = "\x1b[32m"
	sgrYellow = "\x1b[33m"
	sgrCyan   = "\x1b[36m"
)

func (u *ui) style(sgr, s string) string {
	if !u.color {
		return s
	}
	return sgr + s + sgrReset
}

func (u *ui) bold(s string) string   { return u.style(sgrBold, s) }
func (u *ui) dim(s string) string    { return u.style(sgrDim, s) }
func (u *ui) good(s string) string   { return u.style(sgrGreen, s) }
func (u *ui) bad(s string) string    { return u.style(sgrRed, s) }
func (u *ui) warn(s string) string   { return u.style(sgrYellow, s) }
func (u *ui) accent(s string) string { return u.style(sgrCyan, s) }

// tick and cross are the pass/fail marks; arrow the flow mark.
func (u *ui) tick() string {
	if u.unicode {
		return u.good("✓")
	}
	return u.good("ok")
}

func (u *ui) cross() string {
	if u.unicode {
		return u.bad("✗")
	}
	return u.bad("XX")
}

func (u *ui) printf(format string, args ...any) {
	fmt.Fprintf(u.w, format, args...)
}

func isTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), ioctlGetTermios)
	return err == nil
}

func isTTYWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && isTerminal(f)
}

// termWidth reports the terminal column count, 80 when unknowable.
func termWidth(f *os.File) int {
	ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 {
		return 80
	}
	return int(ws.Col)
}

// rawMode switches the terminal to character-at-a-time input without
// echo. Signals are handled in-band (ctrl-c arrives as a byte), so
// the caller must run restore on every exit path; rawMode installs a
// handler that runs onSignal (screen cleanup: cursor, alternate
// buffer) and restores the terminal if the process is killed.
func rawMode(f *os.File, onSignal func()) (restore func(), err error) {
	fd := int(f.Fd())
	old, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return nil, err
	}
	raw := *old
	raw.Lflag &^= unix.ICANON | unix.ECHO | unix.ISIG
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios, &raw); err != nil {
		return nil, err
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	go func() {
		if _, ok := <-sig; ok {
			if onSignal != nil {
				onSignal()
			}
			unix.IoctlSetTermios(fd, ioctlSetTermios, old)
			os.Exit(1)
		}
	}()
	// Idempotent: the TUI's suspend path and its exit path may both
	// restore; the second call must be a no-op, not a double close.
	var once sync.Once
	return func() {
		once.Do(func() {
			signal.Stop(sig)
			close(sig)
			unix.IoctlSetTermios(fd, ioctlSetTermios, old)
		})
	}, nil
}
