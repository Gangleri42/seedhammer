package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"seedhammer.com/bip39"
)

// wordEntry is one position of a 24-word entry. An unknown entry was
// typed as '?': a word illegible on the plate, to be recovered later
// against the public key fingerprint.
type wordEntry struct {
	w       bip39.Word
	unknown bool
}

const maxUnknown = 2

var errAborted = errors.New("aborted")

// enterMnemonic collects 24 words interactively on the terminal, in
// raw mode: candidates narrow as the user types, words auto-advance
// the moment they are unambiguous, and the final position offers only
// the eight checksum-valid words. Everything renders to the tty and
// nothing to stdout, so redirections cannot capture the words.
func enterMnemonic(tty *os.File) ([]wordEntry, error) {
	if os.Getenv("TERM") == "dumb" {
		return promptedEntry(tty, tty)
	}
	showCursor := func() { io.WriteString(tty, "\x1b[?25h") }
	restore, err := rawMode(tty, showCursor)
	if err != nil {
		return nil, fmt.Errorf("terminal: %w", err)
	}
	e := &interactiveEntry{tty: tty, u: newUI(tty)}
	io.WriteString(tty, "\x1b[?25l") // hide the cursor; the block draws its own
	defer func() {
		showCursor()
		restore()
	}()
	e.draw()
	var buf [1]byte
	for len(e.entries) < 24 {
		if _, err := tty.Read(buf[:]); err != nil {
			e.clear()
			return nil, fmt.Errorf("terminal: %w", err)
		}
		if err := e.key(buf[0]); err != nil {
			e.clear()
			io.WriteString(tty, "  "+e.u.dim("entry aborted, nothing kept")+"\n")
			return nil, err
		}
		e.draw()
	}
	return e.entries, nil
}

type interactiveEntry struct {
	tty     *os.File
	u       *ui
	entries []wordEntry
	frag    string
	finals  []bip39.Word // checksum-valid words for position 24, nil when not computable
	lines   int          // physical lines of the last draw
	hint    string       // transient message, cleared by the next key
}

func (e *interactiveEntry) unknowns() int {
	n := 0
	for _, en := range e.entries {
		if en.unknown {
			n++
		}
	}
	return n
}

func (e *interactiveEntry) accept(w bip39.Word, unknown bool) {
	e.entries = append(e.entries, wordEntry{w: w, unknown: unknown})
	e.frag = ""
	e.finals = nil
	if len(e.entries) == 23 && e.unknowns() == 0 {
		e.finals = validFinalWords(e.entries)
	}
}

// key digests one input byte. It returns errAborted on ctrl-c/ctrl-d.
func (e *interactiveEntry) key(c byte) error {
	e.hint = ""
	switch {
	case c == 0x03 || c == 0x04:
		return errAborted
	case c == 0x1b:
		e.swallowEscape()
	case c == 0x0c: // ctrl-l: full repaint
		io.WriteString(e.tty, "\x1b[2J\x1b[H")
		e.lines = 0
	case c == 0x7f || c == 0x08:
		switch {
		case e.frag != "":
			e.frag = e.frag[:len(e.frag)-1]
		case len(e.entries) > 0:
			e.entries = e.entries[:len(e.entries)-1]
			e.finals = nil
		default:
			e.bel()
		}
	case c == '\r' || c == '\n':
		if e.finals != nil {
			if m := finalsWithPrefix(e.finals, e.frag); len(m) == 1 {
				e.accept(m[0], false)
				return nil
			}
			e.bel()
			return nil
		}
		if w, ok := bip39.Complete(e.frag); ok && e.frag != "" {
			e.accept(w, false)
			return nil
		}
		e.bel()
	case c == '?':
		if e.frag != "" {
			e.bel()
			return nil
		}
		if e.unknowns() >= maxUnknown {
			e.hint = fmt.Sprintf("at most %d unknown words are recoverable", maxUnknown)
			e.bel()
			return nil
		}
		e.accept(-1, true)
	case e.finals != nil && e.frag == "" && '1' <= c && c <= '9':
		if i := int(c - '1'); i < len(e.finals) {
			e.accept(e.finals[i], false)
		} else {
			e.bel()
		}
	case 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z':
		frag := e.frag + strings.ToUpper(string(c))
		if e.finals != nil {
			if len(finalsWithPrefix(e.finals, frag)) == 0 {
				e.bel()
				return nil
			}
			e.frag = frag
			if m := finalsWithPrefix(e.finals, frag); len(m) == 1 {
				e.accept(m[0], false)
			}
			return nil
		}
		first, n := bip39.Matches(frag)
		if n == 0 {
			e.bel()
			return nil
		}
		e.frag = frag
		if n == 1 {
			e.accept(first, false)
		}
	default:
		e.bel()
	}
	return nil
}

func (e *interactiveEntry) bel() { io.WriteString(e.tty, "\a") }

// swallowEscape consumes the remainder of an ANSI escape sequence
// (arrow keys and friends), which the entry ignores.
func (e *interactiveEntry) swallowEscape() {
	var b [1]byte
	if _, err := e.tty.Read(b[:]); err != nil || (b[0] != '[' && b[0] != 'O') {
		return
	}
	for {
		if _, err := e.tty.Read(b[:]); err != nil {
			return
		}
		if 0x40 <= b[0] && b[0] <= 0x7e { // final byte of a CSI sequence
			return
		}
	}
}

func (e *interactiveEntry) draw() {
	block := e.render()
	if e.lines > 0 {
		fmt.Fprintf(e.tty, "\x1b[%dA\r\x1b[J", e.lines)
	}
	io.WriteString(e.tty, block)
	e.lines = strings.Count(block, "\n")
}

func (e *interactiveEntry) clear() {
	if e.lines > 0 {
		fmt.Fprintf(e.tty, "\x1b[%dA\r\x1b[J", e.lines)
		e.lines = 0
	}
}

// clip hard-truncates a rendered line to width printable columns so
// it occupies one physical terminal row; a wrapped line would break
// the redraw arithmetic. ANSI escape sequences cost no columns, UTF-8
// continuation bytes count toward the rune they extend, and a
// truncated styled line is closed with a reset so styles cannot bleed.
func clip(s string, width int) string {
	if width < 1 {
		width = 1
	}
	n := 0
	styled := false
	inEsc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inEsc:
			if 0x40 <= c && c <= 0x7e && c != '[' {
				inEsc = false
			}
			continue
		case c == 0x1b:
			inEsc = true
			styled = true
			continue
		case c&0xc0 == 0x80:
			// Inside a rune already counted at its lead byte.
			continue
		}
		if n == width {
			if styled {
				return s[:i] + sgrReset
			}
			return s[:i]
		}
		n++
	}
	return s
}

func (e *interactiveEntry) render() string {
	width := termWidth(e.tty)
	var b strings.Builder
	line := func(s string) {
		b.WriteString(clip(s, width-1))
		b.WriteByte('\n')
	}

	// Header: direction on the left, the position being entered on
	// the right.
	head := "24 words  ->  boot key"
	counter := fmt.Sprintf("[ %2d / 24 ]", min(len(e.entries)+1, 24))
	gap := min(width-1, 62) - len("  "+head) - len(counter)
	if gap < 2 {
		gap = 2
	}
	b.WriteString("\n")
	line("  " + e.u.bold(head) + strings.Repeat(" ", gap) + e.u.dim(counter))
	line("")

	// Accepted words, numbered, in as many columns as fit.
	cols := max(1, min(4, (width-3)/13))
	for i := 0; i < len(e.entries); i += cols {
		var row strings.Builder
		row.WriteString(" ")
		for j := i; j < min(i+cols, len(e.entries)); j++ {
			label := "?"
			if !e.entries[j].unknown {
				label = strings.ToLower(bip39.LabelFor(e.entries[j].w))
			}
			fmt.Fprintf(&row, " %2d %-8s ", j+1, label)
		}
		line(row.String())
	}
	if len(e.entries) > 0 {
		line("")
	}

	if len(e.entries) == 24 {
		return b.String()
	}

	// Prompt.
	caret := "_"
	if e.u.unicode {
		caret = "█"
	}
	line(fmt.Sprintf("  %2d > %s%s", len(e.entries)+1, strings.ToLower(e.frag), e.u.accent(caret)))

	// Candidates.
	countLine := ""
	switch {
	case e.finals != nil:
		m := finalsWithPrefix(e.finals, e.frag)
		line("       " + e.u.dim("the last word must be one of:"))
		for i := 0; i < len(m); i += 4 {
			var row strings.Builder
			row.WriteString("       ")
			for j := i; j < min(i+4, len(m)); j++ {
				fmt.Fprintf(&row, "%s %-9s", e.u.dim(fmt.Sprintf("%d", j+1)), strings.ToLower(bip39.LabelFor(m[j])))
			}
			line(row.String())
		}
	case e.frag != "":
		first, n := bip39.Matches(e.frag)
		if n > 1 && n <= 16 {
			words := make([]string, 0, n)
			for w := first; w < first+bip39.Word(n); w++ {
				words = append(words, strings.ToLower(bip39.LabelFor(w)))
			}
			for _, l := range wrapWords(words, "  ", width-9) {
				line("       " + l)
			}
		}
		countLine = fmt.Sprintf("%d of %d left", n, bip39.NumWords)
	}
	if countLine != "" {
		pad := min(width-1, 62) - len(countLine)
		if pad < 7 {
			pad = 7
		}
		line(strings.Repeat(" ", pad) + e.u.dim(countLine))
	}
	line("")

	// Footer.
	keys := "ctrl-c abort   backspace fix   ? unknown word"
	if e.finals != nil {
		keys = "ctrl-c abort   backspace fix   1-" + fmt.Sprint(len(finalsWithPrefix(e.finals, e.frag))) + " pick"
	}
	if w, ok := bip39.Complete(e.frag); ok && e.frag != "" && e.finals == nil {
		keys += fmt.Sprintf("   enter accept %q", strings.ToLower(bip39.LabelFor(w)))
	}
	if e.hint != "" {
		keys = e.hint
	}
	line("  " + e.u.dim(keys))
	return b.String()
}

// wrapText wraps prose to width columns on spaces, preserving
// explicit newlines. Panels use it for error text: an instruction
// clipped at the screen edge is an instruction the operator never
// got.
func wrapText(s string, width int) []string {
	if width < 20 {
		width = 20
	}
	var out []string
	for _, ln := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		out = append(out, wrapWords(strings.Fields(ln), " ", width)...)
	}
	return out
}

// wrapWords lays words out into lines of at most width columns,
// never breaking inside a word.
func wrapWords(words []string, sep string, width int) []string {
	if width < 12 {
		width = 12
	}
	var lines []string
	cur := ""
	for _, w := range words {
		switch {
		case cur == "":
			cur = w
		case len(cur)+len(sep)+len(w) <= width:
			cur += sep + w
		default:
			lines = append(lines, cur)
			cur = w
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// validFinalWords returns the words that complete first23 to a valid
// checksum. Word 24 carries 3 entropy bits and 8 checksum bits, so
// there are always exactly 8.
func validFinalWords(first23 []wordEntry) []bip39.Word {
	m := make(bip39.Mnemonic, 24)
	for i, en := range first23 {
		m[i] = en.w
	}
	var out []bip39.Word
	for w := bip39.Word(0); w < bip39.NumWords; w++ {
		m[23] = w
		if m.Valid() {
			out = append(out, w)
		}
	}
	return out
}

func finalsWithPrefix(finals []bip39.Word, frag string) []bip39.Word {
	var out []bip39.Word
	for _, w := range finals {
		if strings.HasPrefix(bip39.LabelFor(w), frag) {
			out = append(out, w)
		}
	}
	return out
}

// promptedEntry is the degraded interactive mode for TERM=dumb: one
// word per line, canonical input, no redraws.
func promptedEntry(out io.Writer, in io.Reader) ([]wordEntry, error) {
	fmt.Fprintln(out, "24 words -> boot key. One word per line; ? for an illegible word.")
	sc := bufio.NewScanner(in)
	var entries []wordEntry
	for len(entries) < 24 {
		fmt.Fprintf(out, "word %2d: ", len(entries)+1)
		if !sc.Scan() {
			if err := sc.Err(); err != nil {
				return nil, err
			}
			return nil, errors.New("input ended before word 24")
		}
		tok := strings.TrimSpace(sc.Text())
		if tok == "" {
			continue
		}
		en, err := tokenToEntry(tok, len(entries)+1)
		if err != nil {
			fmt.Fprintf(out, "  %v\n", err)
			continue
		}
		if en.unknown {
			n := 0
			for _, e := range entries {
				if e.unknown {
					n++
				}
			}
			if n >= maxUnknown {
				fmt.Fprintf(out, "  at most %d unknown words are recoverable\n", maxUnknown)
				continue
			}
		}
		if !en.unknown {
			fmt.Fprintf(out, "  -> %s\n", strings.ToLower(bip39.LabelFor(en.w)))
		}
		entries = append(entries, en)
	}
	return entries, nil
}

// readMnemonicTokens is the non-interactive path: whitespace-separated
// words from a pipe or file. Stricter than the device's NFC parser on
// purpose: an ambiguous prefix is an error here, never a silent pick.
func readMnemonicTokens(r io.Reader) ([]wordEntry, error) {
	sc := bufio.NewScanner(r)
	sc.Split(bufio.ScanWords)
	var entries []wordEntry
	for sc.Scan() {
		if len(entries) == 24 {
			return nil, errors.New("more than 24 words in input")
		}
		en, err := tokenToEntry(sc.Text(), len(entries)+1)
		if err != nil {
			return nil, err
		}
		entries = append(entries, en)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(entries) != 24 {
		return nil, fmt.Errorf("got %d words; the boot key backup is 24", len(entries))
	}
	n := 0
	for _, e := range entries {
		if e.unknown {
			n++
		}
	}
	if n > maxUnknown {
		return nil, fmt.Errorf("%d unknown words; at most %d are recoverable", n, maxUnknown)
	}
	return entries, nil
}

// tokenToEntry resolves one typed token: '?', a full word, or an
// unambiguous prefix.
func tokenToEntry(tok string, pos int) (wordEntry, error) {
	if tok == "?" {
		return wordEntry{w: -1, unknown: true}, nil
	}
	up := strings.ToUpper(tok)
	first, n := bip39.Matches(up)
	switch {
	case n == 0:
		return wordEntry{}, fmt.Errorf("word %d: %q is not in the BIP39 wordlist", pos, tok)
	case n == 1 || up == bip39.LabelFor(first):
		return wordEntry{w: first}, nil
	default:
		return wordEntry{}, fmt.Errorf("word %d: %q is ambiguous (%d words start with it)", pos, tok, n)
	}
}
