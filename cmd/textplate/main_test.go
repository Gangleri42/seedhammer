package main

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"

	"seedhammer.com/backup"
	"seedhammer.com/bspline"
	"seedhammer.com/curves"
	"seedhammer.com/engrave"
	"seedhammer.com/font/sh"
)

// TestGlyphsInSync fails if the committed glyphs.js has drifted from the
// generator. write-nfc.py reads this file at runtime for its charset and
// glyph geometry, so a stale copy would engrave the wrong shapes.
func TestGlyphsInSync(t *testing.T) {
	committed, err := os.ReadFile("glyphs.js")
	if err != nil {
		t.Fatal(err)
	}
	if got := render(); !bytes.Equal(got, committed) {
		t.Errorf("glyphs.js (%d bytes) is stale vs a fresh render (%d bytes); regenerate with go run seedhammer.com/cmd/textplate cmd/textplate/glyphs.js",
			len(committed), len(got))
	}
}

// TestCurvesRoundTrip compiles a composition to a curves payload the
// way the editor does and validates it as the firmware would.
func TestCurvesRoundTrip(t *testing.T) {
	m := sh.Font.Metrics()
	adv, _, _ := sh.Font.Decode('W')
	for _, size := range backup.FontSizes {
		unitsPerMM := int(math.Round(float64(m.Height) / float64(size)))
		strokeWidth := int(math.Round(0.3 * float64(unitsPerMM)))
		margin := 3 * unitsPerMM
		var b strings.Builder
		fmt.Fprintf(&b, "%d %s %d %d\n", curves.Version, curves.ModePath, unitsPerMM, strokeWidth)
		lines := []string{"IN CASE OF FIRE", "BREAK GLASS", "*@0Q9#8{}"}
		for row, line := range lines {
			for col, ch := range line {
				if ch == ' ' {
					continue
				}
				d := glyphPath(ch)
				b.WriteString(translatePath(d, margin+col*adv, margin+row*m.Height))
				b.WriteString("\n")
			}
		}
		drawing, err := curves.Parse([]byte(b.String()), params)
		if err != nil {
			t.Fatalf("%.1fmm: %v", size, err)
		}
		plate := 85 * params.Millimeter
		safety := 3 * params.Millimeter
		if b := drawing.Bounds; b.Min.X < safety || b.Min.Y < safety || b.Max.X > plate-safety || b.Max.Y > plate-safety {
			t.Errorf("%.1fmm: drawing bounds %v exceed the plate safe area", size, b)
		}
		attrs := bspline.Measure(engrave.PlanEngraving(conf, drawing.Engraving()))
		if attrs.Duration == 0 {
			t.Errorf("%.1fmm: planned engraving has no duration", size)
		}
	}
}

// translatePath offsets every coordinate of glyph path data, as the
// editor does when compiling a composition.
func translatePath(d string, dx, dy int) string {
	var b strings.Builder
	i := 0
	x := true
	for i < len(d) {
		switch c := d[i]; {
		case c == 'M' || c == 'C':
			b.WriteByte(c)
			x = true
			i++
		case c == ' ':
			i++
		default:
			j := i
			for j < len(d) && d[j] != ' ' && d[j] != 'M' && d[j] != 'C' {
				j++
			}
			v, err := strconv.Atoi(d[i:j])
			if err != nil {
				panic(err)
			}
			if x {
				v += dx
			} else {
				v += dy
			}
			if last := b.String(); len(last) > 0 && last[len(last)-1] != 'M' && last[len(last)-1] != 'C' {
				b.WriteByte(' ')
			}
			b.WriteString(strconv.Itoa(v))
			x = !x
			i = j
		}
	}
	return b.String()
}
