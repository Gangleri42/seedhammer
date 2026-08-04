package gui

import (
	"testing"
	"testing/synctest"

	"seedhammer.com/bip380"
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
