package main

import (
	"image"
	"image/color"
	"image/png"
	"os"

	"seedhammer.com/bezier"
	"seedhammer.com/bspline"
	"seedhammer.com/curves"
	"seedhammer.com/engrave"
)

// writePreview renders the drawing as the device would engrave it,
// planned strokes sampled to a black-on-white PNG of the whole plate.
// side is the plate's pixel width; the height follows the plate's
// aspect.
func writePreview(path string, d *curves.Drawing, side int, plateW, plateH float64) error {
	const pen = 1 // stroke half-width in pixels.
	img := image.NewGray(image.Rect(0, 0, side, int(float64(side)*plateH/plateW+0.5)))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	plate := plateW * float64(sh2.Millimeter)
	px := func(p bezier.Point) (int, int) {
		return int(float64(p.X) / plate * float64(side)), int(float64(p.Y) / plate * float64(side))
	}
	dot := func(cx, cy int) {
		for dy := -pen; dy <= pen; dy++ {
			for dx := -pen; dx <= pen; dx++ {
				if dx*dx+dy*dy <= pen*pen {
					x, y := cx+dx, cy+dy
					if image.Pt(x, y).In(img.Rect) {
						img.SetGray(x, y, color.Gray{0})
					}
				}
			}
		}
	}
	var samples []bezier.Point
	var seg bspline.Segment
	for k := range engrave.PlanEngraving(sh2.StepperConfig, d.Engraving()) {
		c, dt, engrave := seg.Knot(k)
		if dt == 0 || !engrave {
			continue
		}
		samples = append(samples[:0], c.C0)
		samples = bezier.Sample(samples, c, sh2.StrokeWidth/3)
		for _, p := range samples {
			dot(px(p))
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}
