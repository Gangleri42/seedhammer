package gui

import (
	"image"
	"testing"

	"seedhammer.com/bip39"
	"seedhammer.com/gui/op"
	"seedhammer.com/image/rgb565"
)

// TestHideWithholdsPlateContent: with the toggle on, the plate is
// drawn empty. Asserted on rendered pixels, not on the raster: the
// question is what reaches the screen, and the plate outline and the
// figures under it must survive while everything engraved goes.
func TestHideWithholdsPlateContent(t *testing.T) {
	view, plate := testSeedView(t)
	ctx := NewContext(newPlatform())
	scr := NewEngraveScreen(ctx, plate, view)
	dims := image.Pt(480, 320)
	side := view.preview.Bounds().Dx()
	// The plate interior, inset past the outline's corner radius so
	// the rounded corners are never counted as content. Plate margins
	// keep every engraved mark well inside this.
	inner := image.Rect(0, 0, side, side).
		Add(image.Pt((dims.X-side)/2, leadingSize+4)).
		Inset(3*side/85 + 2)

	shown := litIn(ctx, scr, dims, inner)
	if shown == 0 {
		t.Fatal("the plate preview draws nothing to hide")
	}
	view.hidden = true
	if got := litIn(ctx, scr, dims, inner); got != 0 {
		t.Errorf("hiding left %d lit pixels inside the plate, want none", got)
	}
	// The plate is still on screen: the frame keeps ink outside the
	// interior (the outline, the title, the dimensions line).
	if litIn(ctx, scr, dims, image.Rect(0, 0, dims.X, dims.Y)) == 0 {
		t.Error("hiding the content blanked the whole screen")
	}
	view.hidden = false
	if got := litIn(ctx, scr, dims, inner); got != shown {
		t.Errorf("unhiding drew %d pixels, want the original %d", got, shown)
	}
}

// litIn counts lit pixels of a drawn frame inside r.
func litIn(ctx *Context, scr *EngraveScreen, dims image.Point, r image.Rectangle) int {
	o := scr.draw(ctx, &engraveTheme, dims)
	clip := image.Rectangle{Max: dims}
	fb := rgb565.New(clip)
	maskfb := image.NewAlpha(clip)
	new(op.Drawer).Draw(fb, maskfb, o)
	ctx.B.Reset()
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			c := fb.RGBA64At(x, y)
			if uint32(c.R)+uint32(c.G)+uint32(c.B) > 0x6000 {
				n++
			}
		}
	}
	return n
}

// TestHideTogglePersistsIntoEngraving: the toggle exists for a plate
// left running unattended, so it must survive the state change from
// confirm to engraving, and the countdown must survive with it.
func TestHideTogglePersistsIntoEngraving(t *testing.T) {
	view, plate := testSeedView(t)
	ctx := NewContext(newPlatform())
	scr := NewEngraveScreen(ctx, plate, view)
	dims := image.Pt(480, 320)
	scr.draw(ctx, &engraveTheme, dims)
	ctx.B.Reset()
	view.hidden = true
	scr.job.status.State = engraveRunning
	if view.mask() != nil {
		t.Error("the hide does not hold once the engraving starts")
	}
	// The countdown still reads: hiding the content must not hide the
	// state of the machine.
	o := scr.draw(ctx, &engraveTheme, dims)
	d := new(op.Drawer)
	txt := d.ExtractText(image.Rectangle{Max: dims}, o)
	ctx.B.Reset()
	if !uiContains(txt, ":") {
		t.Errorf("the hidden running screen shows no countdown: %q", txt)
	}
}

// TestHideButtonToggles wires the middle nav button to the toggle:
// the checkbox is what the operator actually presses, and a screen
// without a preview must ignore it rather than fault.
func TestHideButtonToggles(t *testing.T) {
	view, plate := testSeedView(t)
	ctx := NewContext(newPlatform())
	frame, quit := runUI(ctx, func() {
		NewEngraveScreen(ctx, plate, view).Engrave(ctx, &engraveTheme)
	})
	defer quit()
	frame()
	for _, want := range []bool{true, false, true} {
		click(&ctx.Router, Button2)
		if _, ok := frame(); !ok {
			t.Fatal("the engrave screen exited on the blur button")
		}
		if view.hidden != want {
			t.Fatalf("blur is %t after the toggle, want %t", view.hidden, want)
		}
	}
	quit()

	// A viewless screen (no preview planned) ignores the button.
	ctx2 := NewContext(newPlatform())
	frame2, quit2 := runUI(ctx2, func() {
		NewEngraveScreen(ctx2, plate, nil).Engrave(ctx2, &engraveTheme)
	})
	defer quit2()
	frame2()
	click(&ctx2.Router, Button2)
	if _, ok := frame2(); !ok {
		t.Fatal("a viewless engrave screen exited on the blur button")
	}
}

func testSeedView(t *testing.T) (*CurvesScreen, Plate) {
	t.Helper()
	m, err := bip39.ParseMnemonic(
		"legal winner thank year wave sausage worth useful legal winner thank yellow")
	if err != nil {
		t.Fatal(err)
	}
	params := engraverParams
	plan, err := engraveSeed(params, SquarePlate, m, "")
	if err != nil {
		t.Fatal(err)
	}
	r := newSplineRasterizer(previewSide(image.Pt(480, 320), SquarePlate), params, SquarePlate)
	plate, err := planPlateWalk(plan, params, SquarePlate, nil, r.knot)
	if err != nil {
		t.Fatal(err)
	}
	view := &CurvesScreen{title: "Engrave Seed"}
	view.preview = r.preview
	view.initText(plate, r, params)
	return view, plate
}
