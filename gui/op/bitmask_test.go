package op

import (
	"image"
	"image/color"
	"testing"

	"golang.org/x/image/draw"
	"seedhammer.com/image/rgb565"
)

// TestBitMaskDrawParity pins the word-skipping fast path to the
// generic mask draw it replaces, across word boundaries, offsets and
// clips.
func TestBitMaskDrawParity(t *testing.T) {
	m := NewBitMask(image.Pt(70, 41))
	for i := 0; i < 70*41; i++ {
		// A mix of runs, singles and long gaps, straddling the 32-bit
		// word grid.
		if i%97 < 3 || i%13 == 0 && i%5 != 0 {
			m.Set(i%70, i/70)
		}
	}
	src := &rgbaUniform{C: color.RGBA{R: 0xff, G: 0xa5, A: 0xff}}
	cases := []struct {
		dr      image.Rectangle
		maskOff image.Point
	}{
		{image.Rect(0, 0, 70, 41), image.Pt(0, 0)},
		{image.Rect(10, 5, 60, 30), image.Pt(0, 0)},
		{image.Rect(3, 7, 50, 40), image.Pt(17, 2)},
		{image.Rect(0, 0, 33, 1), image.Pt(31, 40)},
	}
	for _, c := range cases {
		got := rgb565.New(image.Rect(0, 0, 80, 48))
		want := rgb565.New(image.Rect(0, 0, 80, 48))
		drawBitUniformOver(got, c.dr, src.C, m, c.maskOff)
		draw.DrawMask(want, c.dr, src, image.Point{}, m, c.maskOff, draw.Over)
		for i := range want.Pix {
			if got.Pix[i] != want.Pix[i] {
				t.Fatalf("dr %v maskOff %v: pixel %d = %#x, want %#x",
					c.dr, c.maskOff, i, got.Pix[i], want.Pix[i])
			}
		}
	}
}
