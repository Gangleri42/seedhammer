package main

import (
	"strings"
	"testing"

	"seedhammer.com/curves"
	"seedhammer.com/engrave"
)

// The restore document must render, encode and validate exactly as
// the machine will accept it, whatever fingerprint fills it in: the
// template is checked at build time here, not on the bench.
func TestInstructionsPayload(t *testing.T) {
	priv, _ := loadFixture(t)
	fp := fingerprintHex(priv)
	src := instructionsMarkdown(fp)
	grouped := fp[0:4] + " " + fp[4:8] + " " + fp[8:12] + " " + fp[12:16]
	if !strings.Contains(src, "## "+grouped) {
		t.Fatalf("markdown lacks the grouped fingerprint header %q", grouped)
	}
	payload, err := instructionsPayload(src)
	if err != nil {
		t.Fatal(err)
	}
	d, err := curves.Parse(payload, engrave.SH2Params)
	if err != nil {
		t.Fatal(err)
	}
	if d.Strokes < 400 {
		t.Errorf("suspiciously few strokes: %d", d.Strokes)
	}
	mm := engrave.SH2Params.Millimeter
	if d.Bounds.Min.X < 3*mm || d.Bounds.Min.Y < 3*mm ||
		d.Bounds.Max.X > 82*mm || d.Bounds.Max.Y > 82*mm {
		t.Errorf("bounds %v outside the 85mm plate's 3mm margin", d.Bounds)
	}
}
