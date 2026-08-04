package op

import (
	"image"
	"image/color"
)

// BitMask is a 1-bit alpha raster: set pixels are opaque, unset ones
// transparent. The drawer has a word-at-a-time fast path for it, so a
// large, sparse mask (the plate preview) draws without a per-pixel
// interface call.
type BitMask struct {
	sz   image.Point
	bits []uint32
}

func NewBitMask(sz image.Point) *BitMask {
	return &BitMask{
		sz:   sz,
		bits: make([]uint32, (sz.X*sz.Y+31)/32),
	}
}

func (p *BitMask) Set(x, y int) {
	if x < 0 || y < 0 || x >= p.sz.X || y >= p.sz.Y {
		return
	}
	i := y*p.sz.X + x
	p.bits[i/32] |= 1 << (i % 32)
}

func (p *BitMask) alpha(x, y int) uint8 {
	if x < 0 || y < 0 || x >= p.sz.X || y >= p.sz.Y {
		return 0
	}
	i := y*p.sz.X + x
	if p.bits[i/32]&(1<<(i%32)) != 0 {
		return 0xff
	}
	return 0
}

func (p *BitMask) ColorModel() color.Model {
	return color.AlphaModel
}

func (p *BitMask) Bounds() image.Rectangle {
	return image.Rectangle{Max: p.sz}
}

func (p *BitMask) At(x, y int) color.Color {
	return color.Alpha{A: p.alpha(x, y)}
}

func (p *BitMask) RGBA64At(x, y int) color.RGBA64 {
	a := p.alpha(x, y)
	return color.RGBA64{A: uint16(a)<<8 | uint16(a)}
}
