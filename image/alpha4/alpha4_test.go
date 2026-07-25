package alpha4

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"testing"
)

// opaqueSource wraps an image.Image, hiding any RGBA64At method so
// that draw.Draw falls back to the generic dst.Set path.
type opaqueSource struct {
	img image.Image
}

func (s opaqueSource) ColorModel() color.Model { return s.img.ColorModel() }
func (s opaqueSource) Bounds() image.Rectangle { return s.img.Bounds() }
func (s opaqueSource) At(x, y int) color.Color { return s.img.At(x, y) }

func TestSet(t *testing.T) {
	r := Rectangle{10, 10, 12, 12}
	img := New(r)
	img.Set(10, 10, color.Alpha{A: 0xAB})
	if got := img.AlphaAt(10, 10).A; got != 0xAA {
		t.Errorf("Set(color.Alpha{0xAB}): got alpha %#x, want 0xAA", got)
	}
	img.Set(11, 10, color.RGBA{R: 0x12, G: 0x34, B: 0x56, A: 0xCD})
	if got := img.AlphaAt(11, 10).A; got != 0xCC {
		t.Errorf("Set(color.RGBA{A: 0xCD}): got alpha %#x, want 0xCC", got)
	}
	before := append([]byte(nil), img.Pix...)
	img.Set(0, 0, color.Alpha{A: 0xFF})
	img.Set(12, 12, color.Alpha{A: 0xFF})
	if !bytes.Equal(img.Pix, before) {
		t.Error("out-of-bounds Set modified pixels")
	}
}

func TestSetDrawFallback(t *testing.T) {
	r := Rectangle{10, 10, 12, 12}
	src := image.NewAlpha(r.Rect())
	src.SetAlpha(10, 10, color.Alpha{A: 0x11})
	src.SetAlpha(11, 10, color.Alpha{A: 0x99})
	src.SetAlpha(10, 11, color.Alpha{A: 0xFF})

	got := New(r)
	draw.Draw(got, got.Bounds(), opaqueSource{src}, src.Bounds().Min, draw.Src)

	want := New(r)
	for y := 10; y < 12; y++ {
		for x := 10; x < 12; x++ {
			want.SetAlpha4(x, y, src.AlphaAt(x, y).A>>4)
		}
	}
	if !bytes.Equal(got.Pix, want.Pix) {
		t.Errorf("draw fallback: got pix %v, want %v", got.Pix, want.Pix)
	}
}

func TestExhausting(t *testing.T) {
	r := Rectangle{10, 10, 12, 12}
	img1 := image.NewAlpha(r.Rect())
	a4 := New(r)
	img2 := image.NewAlpha(img1.Rect)
	for a1 := byte(0); a1 <= 0b1111; a1++ {
		for a2 := byte(0); a2 <= 0b1111; a2++ {
			for a3 := byte(0); a3 <= 0b1111; a3++ {
				img1.SetAlpha(10, 10, color.Alpha{A: a1<<4 | a1})
				img1.SetAlpha(11, 10, color.Alpha{A: a2<<4 | a2})
				img1.SetAlpha(10, 11, color.Alpha{A: a3<<4 | a3})
				draw.Draw(a4, a4.Bounds(), img1, img1.Bounds().Min, draw.Src)
				draw.Draw(img2, img2.Bounds(), a4, a4.Bounds().Min, draw.Src)
				if !bytes.Equal(img1.Pix, img2.Pix) {
					t.Errorf("%.8b %.8b %.8b roundtripped to %.8b %.8b %.8b",
						img1.AlphaAt(10, 10), img1.AlphaAt(11, 10), img1.AlphaAt(10, 11),
						img2.AlphaAt(10, 10), img2.AlphaAt(11, 10), img2.AlphaAt(11, 10))
				}
			}
		}
	}
}
