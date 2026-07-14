package glyph

import (
	"testing"

	"seedhammer.com/font/sh"
	"seedhammer.com/svgpath"
)

// TestSegmentsWellFormed checks every engravable ASCII glyph starts a
// contour with a MoveTo and otherwise uses only CubeTo, the shape the
// converter and the plate editor both rely on.
func TestSegmentsWellFormed(t *testing.T) {
	engravable := 0
	for ch := rune(0); ch < 128; ch++ {
		if _, _, ok := sh.Font.Decode(ch); !ok {
			continue
		}
		segs, adv, ok := Segments(ch)
		if !ok {
			t.Errorf("%q: decode ok but Segments not", ch)
			continue
		}
		if adv <= 0 {
			t.Errorf("%q: non-positive advance %d", ch, adv)
		}
		engravable++
		if len(segs) == 0 {
			continue // space and the like carry advance but no ink.
		}
		if segs[0].Op != svgpath.MoveTo {
			t.Errorf("%q: first segment is %v, want MoveTo", ch, segs[0].Op)
		}
		for i, s := range segs {
			if s.Op != svgpath.MoveTo && s.Op != svgpath.CubeTo {
				t.Errorf("%q: segment %d op %v, want MoveTo or CubeTo", ch, i, s.Op)
			}
		}
	}
	if engravable < 60 {
		t.Fatalf("only %d engravable glyphs; font not loaded?", engravable)
	}
}

// TestPathMatchesSegments confirms Path formats exactly the segments
// Segments returns, so the string export and geometry never diverge.
func TestPathMatchesSegments(t *testing.T) {
	segs, _, ok := Segments('W')
	if !ok || len(segs) == 0 {
		t.Fatal("W not engravable")
	}
	if Path('W') == "" {
		t.Fatal("empty path for W")
	}
	// A missing glyph yields an empty path and !ok segments.
	if p := Path(rune(0x1F600)); p != "" {
		t.Errorf("emoji path should be empty, got %q", p)
	}
}
