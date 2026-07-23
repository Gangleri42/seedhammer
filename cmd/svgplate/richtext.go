package main

import (
	"fmt"
	"strings"

	"seedhammer.com/font/glyph"
	"seedhammer.com/svgpath"
)

// Rich-text layout constants, in millimeters or multiples of the body
// size. The subset is deliberately small: five header levels, italic
// via an oblique shear, and GFM pipe tables.
const (
	textMarginMM = 5.0  // top-left origin of the text block.
	lineSpacing  = 1.18 // line advance as a multiple of cell height.
	italicShear  = 0.21 // tan(~12 degrees).
	cellPadMM    = 1.2  // table cell horizontal padding.
	rowPadMM     = 0.6  // table cell vertical padding.
)

// headerScale maps a header level to its size as a multiple of the
// body size. Each level halves the previous level's excess over 1.0
// (1.0, 0.5, 0.25, 0.125, 0.0625), so every level stays visibly larger
// than body text and the steps taper smoothly. A level absent from
// this map would render at size 0 (invisible), so keep it in sync with
// the header() loop bound below.
const maxHeaderLevel = 5

var headerScale = map[int]float64{1: 2.0, 2: 1.5, 3: 1.25, 4: 1.125, 5: 1.0625}

// renderMarkdown lays out a markdown subset as plate geometry in
// millimeters. bodyMM is the body-text cell height; headers scale off
// it. It reports the used runes it could not engrave.
func renderMarkdown(src string, bodyMM float64) ([]fseg, error) {
	ascent, cellH := glyph.Metrics()
	if cellH == 0 {
		return nil, fmt.Errorf("richtext: font has no height")
	}
	r := &textRenderer{ascent: float64(ascent), cellH: float64(cellH), bodyMM: bodyMM}
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	r.penY = textMarginMM
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			r.penY += r.bodyMM * lineSpacing * 0.6
			continue
		}
		// A table is two or more '|' lines with a dash separator second.
		if isTableRow(line) && i+1 < len(lines) && isSeparatorRow(lines[i+1]) {
			block := []string{line}
			j := i + 1
			for j < len(lines) && isTableRow(lines[j]) {
				block = append(block, lines[j])
				j++
			}
			r.table(block)
			i = j - 1
			continue
		}
		size := r.bodyMM
		if lvl, rest := header(line); lvl > 0 {
			size = r.bodyMM * headerScale[lvl]
			line = rest
		}
		r.line(line, size)
	}
	if len(r.missing) > 0 {
		return r.segs, fmt.Errorf("richtext: cannot engrave %s", strings.Join(r.missing, " "))
	}
	return r.segs, nil
}

type textRenderer struct {
	ascent, cellH float64
	bodyMM        float64
	penY          float64
	segs          []fseg
	missing       []string
	seenMissing   map[rune]bool
}

// line renders a single text line with inline italic and advances penY.
func (r *textRenderer) line(text string, sizeMM float64) {
	r.run(splitFormat(text), textMarginMM, r.penY, sizeMM)
	r.penY += sizeMM * lineSpacing
}

// span is a run of text with one style.
type span struct {
	text      string
	italic    bool
	underline bool
}

// run draws styled spans left to right from (x, y) and returns the
// pen's end x. Underlined spans get a rule just below the baseline.
func (r *textRenderer) run(spans []span, x, y, sizeMM float64) float64 {
	scale := sizeMM / r.cellH
	for _, sp := range spans {
		x0 := x
		for _, ch := range sp.text {
			segs, adv, ok := glyph.Segments(ch)
			if !ok {
				if ch != ' ' && !r.seen(ch) {
					r.missing = append(r.missing, fmt.Sprintf("%q", ch))
				}
				// Advance by the space width so layout stays sane.
				_, sadv, _ := glyph.Segments(' ')
				x += float64(sadv) * scale
				continue
			}
			r.place(segs, x, y, scale, sp.italic)
			x += float64(adv) * scale
		}
		if sp.underline && x > x0 {
			uy := y + r.ascent*scale + sizeMM*0.1
			r.segs = append(r.segs,
				fseg{op: svgpath.MoveTo, p: [3]fpt{{x0, uy}}},
				fseg{op: svgpath.LineTo, p: [3]fpt{{x, uy}}})
		}
	}
	return x
}

func (r *textRenderer) seen(ch rune) bool {
	if r.seenMissing == nil {
		r.seenMissing = map[rune]bool{}
	}
	if r.seenMissing[ch] {
		return true
	}
	r.seenMissing[ch] = true
	return false
}

// place emits one glyph's outline at (x, y) in mm, scaled and
// optionally sheared for italic. Glyph coordinates are font units with
// y=0 the cell top and the baseline at ascent.
func (r *textRenderer) place(segs []svgpath.Segment, x, y, scale float64, italic bool) {
	tx := func(p fpt) fpt {
		fx := p.X
		if italic {
			fx += italicShear * (r.ascent - p.Y)
		}
		return fpt{X: x + fx*scale, Y: y + p.Y*scale}
	}
	for _, s := range segs {
		out := fseg{op: s.Op}
		switch s.Op {
		case svgpath.MoveTo, svgpath.LineTo:
			out.p[0] = tx(bpt(s.Args[0]))
		case svgpath.QuadTo:
			out.p[0], out.p[1] = tx(bpt(s.Args[0])), tx(bpt(s.Args[1]))
		case svgpath.CubeTo:
			out.p[0], out.p[1], out.p[2] = tx(bpt(s.Args[0])), tx(bpt(s.Args[1])), tx(bpt(s.Args[2]))
		}
		r.segs = append(r.segs, out)
	}
}

// measure returns the advance width of plain text at scale.
func (r *textRenderer) measure(text string, scale float64) float64 {
	w := 0.0
	for _, ch := range text {
		_, adv, ok := glyph.Segments(ch)
		if !ok {
			_, adv, _ = glyph.Segments(' ')
		}
		w += float64(adv) * scale
	}
	return w
}

// table lays out a GFM pipe table: column rules plus each cell's text.
func (r *textRenderer) table(block []string) {
	scale := r.bodyMM / r.cellH
	var rows [][]string
	for i, line := range block {
		if i == 1 && isSeparatorRow(line) {
			continue // the dash row only marks the header.
		}
		rows = append(rows, splitCells(line))
	}
	cols := 0
	for _, row := range rows {
		if len(row) > cols {
			cols = len(row)
		}
	}
	widths := make([]float64, cols)
	for _, row := range rows {
		for c, cell := range row {
			if w := r.measure(strings.TrimSpace(cell), scale) + 2*cellPadMM; w > widths[c] {
				widths[c] = w
			}
		}
	}
	rowH := r.bodyMM*lineSpacing + 2*rowPadMM
	x0, y0 := textMarginMM, r.penY
	tableW := 0.0
	for _, w := range widths {
		tableW += w
	}
	tableH := rowH * float64(len(rows))
	// Rules: horizontal at every row boundary, vertical at every column.
	for i := 0; i <= len(rows); i++ {
		y := y0 + float64(i)*rowH
		r.hline(x0, x0+tableW, y)
	}
	x := x0
	r.vline(y0, y0+tableH, x)
	for _, w := range widths {
		x += w
		r.vline(y0, y0+tableH, x)
	}
	// Cell text, left-aligned with padding, baseline within the row.
	for ri, row := range rows {
		cx := x0
		for ci := 0; ci < cols; ci++ {
			var cell string
			if ci < len(row) {
				cell = strings.TrimSpace(row[ci])
			}
			ty := y0 + float64(ri)*rowH + rowPadMM
			r.run(splitFormat(cell), cx+cellPadMM, ty, r.bodyMM)
			cx += widths[ci]
		}
	}
	r.penY = y0 + tableH + r.bodyMM*lineSpacing*0.5
}

func (r *textRenderer) hline(x0, x1, y float64) {
	r.segs = append(r.segs,
		fseg{op: svgpath.MoveTo, p: [3]fpt{{x0, y}}},
		fseg{op: svgpath.LineTo, p: [3]fpt{{x1, y}}},
	)
}

func (r *textRenderer) vline(y0, y1, x float64) {
	r.segs = append(r.segs,
		fseg{op: svgpath.MoveTo, p: [3]fpt{{x, y0}}},
		fseg{op: svgpath.LineTo, p: [3]fpt{{x, y1}}},
	)
}

// header reports a line's header level (1-5) and its text, or 0.
func header(line string) (int, string) {
	for lvl := maxHeaderLevel; lvl >= 1; lvl-- {
		p := strings.Repeat("#", lvl) + " "
		if strings.HasPrefix(line, p) {
			return lvl, strings.TrimSpace(line[len(p):])
		}
	}
	return 0, line
}

// splitFormat breaks text into spans, toggling italic on * and
// underline on _. The two are distinct: the fixed needle cannot vary
// weight, so underline stands in for the emphasis bold would give.
func splitFormat(text string) []span {
	var spans []span
	var b strings.Builder
	italic, underline := false, false
	flush := func() {
		if b.Len() > 0 {
			spans = append(spans, span{text: b.String(), italic: italic, underline: underline})
			b.Reset()
		}
	}
	for _, ch := range text {
		switch ch {
		case '*':
			flush()
			italic = !italic
		case '_':
			flush()
			underline = !underline
		default:
			b.WriteRune(ch)
		}
	}
	flush()
	if len(spans) == 0 {
		return []span{{text: ""}}
	}
	return spans
}

func isTableRow(line string) bool {
	return strings.Contains(line, "|")
}

// isSeparatorRow matches a GFM header separator: a pipe row whose
// cells are dashes and optional alignment colons. The pipe is
// required, else a plain horizontal rule under a line that merely
// contains a '|' (a shell pipe in prose, say) would tablify the prose.
func isSeparatorRow(line string) bool {
	if !strings.Contains(line, "|") || !strings.Contains(line, "-") {
		return false
	}
	for _, cell := range splitCells(line) {
		cell = strings.TrimSpace(cell)
		if cell == "" {
			continue
		}
		for _, ch := range cell {
			if ch != '-' && ch != ':' {
				return false
			}
		}
	}
	return true
}

// splitCells splits a pipe table row into its cells, dropping the
// leading and trailing empties from surrounding pipes.
func splitCells(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	return strings.Split(line, "|")
}
