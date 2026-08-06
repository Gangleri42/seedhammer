package main

import (
	"fmt"
	"regexp"
	"strings"
)

// lexicon is the mechanically checkable slice of the house style: the
// words that read as generated prose. Each entry names the root; the
// pattern catches its inflections. Words with a legitimate technical
// reading here (harness, landscape) are deliberately absent — those
// calls need a human.
var lexicon = []struct {
	word string
	re   *regexp.Regexp
}{
	{"delve", regexp.MustCompile(`(?i)\bdelv(e|es|ed|ing)\b`)},
	{"leverage", regexp.MustCompile(`(?i)\bleverag(e|es|ed|ing)\b`)},
	{"robust", regexp.MustCompile(`(?i)\brobust(ly|ness)?\b`)},
	{"comprehensive", regexp.MustCompile(`(?i)\bcomprehensive(ly|ness)?\b`)},
	{"crucial", regexp.MustCompile(`(?i)\bcrucial(ly)?\b`)},
	{"pivotal", regexp.MustCompile(`(?i)\bpivotal(ly)?\b`)},
	{"seamless", regexp.MustCompile(`(?i)\bseamless(ly)?\b`)},
	{"intricate", regexp.MustCompile(`(?i)\bintrica(te|tely|cy|cies)\b`)},
	{"nuanced", regexp.MustCompile(`(?i)\bnuanced\b`)},
	{"navigate", regexp.MustCompile(`(?i)\bnavigat(e|es|ed|ing)\b`)},
	{"underscore", regexp.MustCompile(`(?i)\bunderscor(e|es|ed|ing)\b`)},
	{"foster", regexp.MustCompile(`(?i)\bfoster(s|ed|ing)?\b`)},
	{"showcase", regexp.MustCompile(`(?i)\bshowcas(e|es|ed|ing)\b`)},
	{"utilize", regexp.MustCompile(`(?i)\butiliz(e|es|ed|ing|ation)\b`)},
	{"facilitate", regexp.MustCompile(`(?i)\bfacilitat(e|es|ed|ing|ion)\b`)},
	{"tapestry", regexp.MustCompile(`(?i)\btapestr(y|ies)\b`)},
	{"realm", regexp.MustCompile(`(?i)\brealms?\b`)},
	{"journey", regexp.MustCompile(`(?i)\bjourneys?\b`)},
	{"testament", regexp.MustCompile(`(?i)\btestaments?\b`)},
	{"paradigm", regexp.MustCompile(`(?i)\bparadigms?\b`)},
	{"synergy", regexp.MustCompile(`(?i)\bsynerg(y|ies|istic)\b`)},
	{"moreover", regexp.MustCompile(`(?i)\bmoreover\b`)},
	{"furthermore", regexp.MustCompile(`(?i)\bfurthermore\b`)},
	{"additionally", regexp.MustCompile(`(?i)\badditionally\b`)},
	{"consequently", regexp.MustCompile(`(?i)\bconsequently\b`)},
	{"notably", regexp.MustCompile(`(?i)\bnotably\b`)},
	{"ultimately", regexp.MustCompile(`(?i)\bultimately\b`)},
	{"essentially", regexp.MustCompile(`(?i)\bessentially\b`)},
	{"subsequently", regexp.MustCompile(`(?i)\bsubsequently\b`)},
	{"worth noting", regexp.MustCompile(`(?i)worth noting`)},
	{"important to note", regexp.MustCompile(`(?i)important to note`)},
	{"in conclusion", regexp.MustCompile(`(?i)\bin conclusion\b`)},
	{"at its core", regexp.MustCompile(`(?i)\bat its core\b`)},
}

// imageRef is one markdown inline image in a manual.
type imageRef struct {
	file string
	line int
	alt  string
	path string
}

var imageRe = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)

// scanProse walks one manual: fenced blocks are skipped whole, inline
// code spans are blanked in place so columns stay true, and what
// remains is prose under the lexicon and em-dash rules. Image
// references are collected from the unblanked line, fences excluded.
func scanProse(file string, src []byte) (findings []string, refs []imageRef) {
	inFence := false
	for i, line := range strings.Split(string(src), "\n") {
		lineno := i + 1
		trimmed := strings.TrimLeft(line, " ")
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		for _, m := range imageRe.FindAllStringSubmatch(line, -1) {
			refs = append(refs, imageRef{file: file, line: lineno, alt: m[1], path: m[2]})
		}
		prose := blankInlineCode(line)
		if col := strings.IndexRune(prose, '—'); col >= 0 {
			findings = append(findings, fmt.Sprintf("%s:%d:%d: em-dash (use a comma, colon, period, or parentheses)", file, lineno, col+1))
		}
		for _, lex := range lexicon {
			if loc := lex.re.FindStringIndex(prose); loc != nil {
				findings = append(findings, fmt.Sprintf("%s:%d:%d: %q (house lexicon: use the plain word)", file, lineno, loc[0]+1, lex.word))
			}
		}
	}
	return findings, refs
}

// blankInlineCode replaces `code` spans with spaces, backticks
// included, preserving every other byte's column. An unmatched
// backtick leaves the rest of the line as prose, which errs toward
// checking too much rather than too little.
func blankInlineCode(line string) string {
	b := []byte(line)
	for {
		open := strings.IndexByte(string(b), '`')
		if open < 0 {
			break
		}
		end := strings.IndexByte(string(b[open+1:]), '`')
		if end < 0 {
			break
		}
		for i := open; i <= open+1+end; i++ {
			b[i] = ' '
		}
	}
	return string(b)
}
