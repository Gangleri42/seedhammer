package richtext

import (
	"strings"
	"testing"

	"seedhammer.com/svgpath"
)

// width is the rendered block width in mm, from the placed groups.
func width(groups []Group) float64 {
	minX, maxX, any := 0.0, 0.0, false
	for _, g := range groups {
		for _, s := range g.Segs {
			for i := 0; i < s.NPts(); i++ {
				x := g.At.X + s.P[i].X
				if !any || x < minX {
					minX = x
				}
				if !any || x > maxX {
					maxX = x
				}
				any = true
			}
		}
	}
	return maxX - minX
}

func TestHeaderIsLarger(t *testing.T) {
	body, err := Render("Hi", 4)
	if err != nil {
		t.Fatal(err)
	}
	head, err := Render("# Hi", 4)
	if err != nil {
		t.Fatal(err)
	}
	if width(head) <= width(body) {
		t.Errorf("header (%.1f) should be wider than body (%.1f)", width(head), width(body))
	}
}

func TestHeaderLevels(t *testing.T) {
	// Every supported header level renders larger than body text, and
	// the levels shrink monotonically as the '#' prefix grows.
	body, err := Render("Hi", 4)
	if err != nil {
		t.Fatal(err)
	}
	bodyW := width(body)
	prev := 0.0
	for lvl := 1; lvl <= maxHeaderLevel; lvl++ {
		groups, err := Render(strings.Repeat("#", lvl)+" Hi", 4)
		if err != nil {
			t.Fatalf("level %d: %v", lvl, err)
		}
		w := width(groups)
		if w <= bodyW {
			t.Errorf("level %d width %.1f should exceed body %.1f", lvl, w, bodyW)
		}
		if prev != 0 && w >= prev {
			t.Errorf("level %d width %.1f should be smaller than level %d width %.1f", lvl, w, lvl-1, prev)
		}
		prev = w
	}
}

func TestUnderline(t *testing.T) {
	// "_" underlines (distinct from "*" italic): the underlined run
	// adds exactly one rule group over the same glyphs.
	plain, err := Render("a b c", 4)
	if err != nil {
		t.Fatal(err)
	}
	under, err := Render("a _b_ c", 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(under) != len(plain)+1 {
		t.Fatalf("underline should add one rule group: plain %d, under %d", len(plain), len(under))
	}
	// Somewhere there is a horizontal rule group: a MoveTo at the local
	// origin and a LineTo rightward on the same line.
	found := false
	for _, g := range under {
		if len(g.Segs) != 2 {
			continue
		}
		a, b := g.Segs[0], g.Segs[1]
		if a.Op == svgpath.MoveTo && b.Op == svgpath.LineTo && a.P[0] == (Point{}) && b.P[0].Y == 0 && b.P[0].X > 0 {
			found = true
			break
		}
	}
	if !found {
		t.Error("no horizontal underline rule found")
	}
}

func TestProseNotTablified(t *testing.T) {
	// A shell pipe in prose over a plain rule must not become a table:
	// the delimiter row needs a pipe to count (GFM).
	if isSeparatorRow("------------------------") {
		t.Error("a pipe-less rule must not be a table separator")
	}
	if !isSeparatorRow("| --- | --- |") {
		t.Error("a real pipe delimiter row should still match")
	}
	md := "Run: cat f | grep x\n------------------------\nDone.\n"
	plain := "Run: cat f X grep x\n------------------------\nDone.\n"
	withPipe, err := Render(md, 4)
	if err != nil {
		t.Fatal(err)
	}
	noPipe, _ := Render(plain, 4)
	// No spurious table rules, so the two render the same group count
	// aside from the single glyph difference; a tablified version would
	// add a group per table rule.
	if len(withPipe) > len(noPipe)+2 {
		t.Errorf("prose with a pipe tablified: %d vs %d groups", len(withPipe), len(noPipe))
	}
}
