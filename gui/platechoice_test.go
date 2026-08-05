package gui

import (
	"testing"
	"testing/synctest"

	"seedhammer.com/bezier"
	"seedhammer.com/bip380"
	"seedhammer.com/curves"
	"seedhammer.com/svgpath"
)

// TestPlateSizeQuestion drives the descriptor confirm end to end: a
// singlesig descriptor fits the small plate, so the Plate Size screen
// appears with SMALL PLATE leading; a dense multisig fits no
// small-plate cell, so the single-plate path goes straight to the
// variant list.
func TestPlateSizeQuestion(t *testing.T) {
	singlesig := func() *bip380.Descriptor {
		desc := &bip380.Descriptor{
			Script:    bip380.P2WPKH,
			Threshold: 1,
			Type:      bip380.Singlesig,
			Keys:      make([]bip380.Key, 1),
		}
		fillDescriptor(t, desc, desc.Script.DerivationPath(), 12, 0)
		return desc
	}
	multisig := func(threshold, nkeys int) *bip380.Descriptor {
		desc := &bip380.Descriptor{
			Script:    bip380.P2WSH,
			Threshold: threshold,
			Type:      bip380.SortedMulti,
			Keys:      make([]bip380.Key, nkeys),
		}
		fillDescriptor(t, desc, desc.Script.DerivationPath(), 12, 0)
		return desc
	}

	run := func(t *testing.T, desc *bip380.Descriptor, drive func(ctx *Context, seek func(want, forbid string))) (planned plannedPlate, confirmed bool) {
		synctest.Test(t, func(t *testing.T) {
			ctx := NewContext(newPlatform())
			ds := &DescriptorScreen{Descriptor: desc}
			frame, quit := runUI(ctx, func() {
				planned, _, confirmed = ds.Confirm(ctx, &descriptorTheme)
			})
			defer quit()
			seek := func(want, forbid string) {
				t.Helper()
				for range 100000 {
					content, ok := frame()
					if !ok {
						t.Fatalf("confirm flow exited while seeking %q", want)
					}
					if forbid != "" && uiContains(content, forbid) {
						t.Fatalf("saw %q while seeking %q", forbid, want)
					}
					if uiContains(content, want) {
						return
					}
				}
				t.Fatalf("never saw %q", want)
			}
			drive(ctx, seek)
			for {
				if _, ok := frame(); !ok {
					break
				}
			}
		})
		return planned, confirmed
	}

	t.Run("singlesig-small", func(t *testing.T) {
		planned, confirmed := run(t, singlesig(), func(ctx *Context, seek func(want, forbid string)) {
			click(&ctx.Router, Button3) // confirm the descriptor
			seek("Plate Size", "")
			click(&ctx.Router, Button3) // SMALL PLATE leads
			seek("Choose engraving", "")
			click(&ctx.Router, Button3) // first variant
		})
		if !confirmed {
			t.Fatal("confirm flow did not complete")
		}
		if planned.plate.Size != SmallPlate {
			t.Errorf("planned plate size %v, want SmallPlate", planned.plate.Size)
		}
	})

	t.Run("singlesig-square", func(t *testing.T) {
		planned, confirmed := run(t, singlesig(), func(ctx *Context, seek func(want, forbid string)) {
			click(&ctx.Router, Button3)
			seek("Plate Size", "")
			click(&ctx.Router, Down)    // move to SQUARE PLATE
			click(&ctx.Router, Button3) // choose it
			seek("Choose engraving", "")
			click(&ctx.Router, Button3)
		})
		if !confirmed {
			t.Fatal("confirm flow did not complete")
		}
		if planned.plate.Size != SquarePlate {
			t.Errorf("planned plate size %v, want SquarePlate", planned.plate.Size)
		}
	})

	t.Run("multisig-no-question", func(t *testing.T) {
		planned, confirmed := run(t, multisig(5, 7), func(ctx *Context, seek func(want, forbid string)) {
			click(&ctx.Router, Button3) // confirm the descriptor
			seek("ONE PLATE", "Plate Size")
			click(&ctx.Router, Button3) // ONE PLATE leads
			seek("Choose engraving", "Plate Size")
			click(&ctx.Router, Button3)
		})
		if !confirmed {
			t.Fatal("confirm flow did not complete")
		}
		if planned.plate.Size != SquarePlate {
			t.Errorf("planned plate size %v, want SquarePlate", planned.plate.Size)
		}
	})
}

// TestCurvesPlateToken pins the payload-named plate: a token skips
// the question entirely, and a tokenless drawing that fits the small
// frame is measured and asked — the ask needs a real walk, since Open
// leaves the bounds zero.
func TestCurvesPlateToken(t *testing.T) {
	seg := func(op svgpath.SegmentOp, x, y int) svgpath.Segment {
		return svgpath.Segment{Op: op, Args: [4]bezier.Point{bezier.Pt(x, y)}}
	}
	// A 10..40mm box: inside the small frame in the plate-local sense.
	segs := []svgpath.Segment{
		seg(svgpath.MoveTo, 100, 100),
		seg(svgpath.LineTo, 400, 100),
		seg(svgpath.LineTo, 400, 400),
		seg(svgpath.LineTo, 100, 400),
		seg(svgpath.LineTo, 100, 100),
	}
	payload, err := curves.EncodePath(10, 3, segs)
	if err != nil {
		t.Fatal(err)
	}

	run := func(t *testing.T, payload []byte, drive func(ctx *Context, seek func(want, forbid string))) {
		synctest.Test(t, func(t *testing.T) {
			ctx := NewContext(newPlatform())
			frame, quit := runUI(ctx, func() {
				curvesPathFlow(ctx, &descriptorTheme, curvesPayload(payload))
			})
			defer quit()
			seek := func(want, forbid string) {
				t.Helper()
				for range 100000 {
					content, ok := frame()
					if !ok {
						t.Fatalf("flow exited while seeking %q", want)
					}
					if forbid != "" && uiContains(content, forbid) {
						t.Fatalf("saw %q while seeking %q", forbid, want)
					}
					if uiContains(content, want) {
						return
					}
				}
				t.Fatalf("never saw %q", want)
			}
			drive(ctx, seek)
			click(&ctx.Router, Button1) // leave the engrave screen
			for {
				if _, ok := frame(); !ok {
					break
				}
			}
		})
	}

	t.Run("tokenless-asks", func(t *testing.T) {
		run(t, payload, func(ctx *Context, seek func(want, forbid string)) {
			seek("Plate Size", "")
			click(&ctx.Router, Button3) // SMALL PLATE
			seek("Engrave Curves", "")
		})
	})
	t.Run("small-token-no-question", func(t *testing.T) {
		stamped, err := curves.WithPlate(payload, curves.PlateSmall)
		if err != nil {
			t.Fatal(err)
		}
		run(t, stamped, func(ctx *Context, seek func(want, forbid string)) {
			seek("mm", "Plate Size") // straight to the plate confirm
		})
	})
	t.Run("square-token-no-question", func(t *testing.T) {
		stamped, err := curves.WithPlate(payload, curves.PlateSquare)
		if err != nil {
			t.Fatal(err)
		}
		run(t, stamped, func(ctx *Context, seek func(want, forbid string)) {
			seek("mm", "Plate Size")
		})
	})
}
