package gui

import (
	"strings"
	"testing"

	"seedhammer.com/backup"
)

// titleFlowHarness drives titleFlow under the frame pump and returns
// its result once the flow ends. pump runs a handful of frames so
// queued events land: the 240x240 test display leaves the input box
// no room, so typed text cannot be awaited by content.
func titleFlowHarness(t *testing.T, required bool, script func(ctx *Context, await func(string), pump func())) (string, bool) {
	t.Helper()
	ctx := NewContext(newPlatform())
	var got string
	var ok bool
	frame, quit := runUI(ctx, func() {
		got, ok = titleFlow(ctx, &descriptorTheme, required, "")
	})
	defer quit()
	script(ctx, func(marker string) {
		t.Helper()
		awaitUI(t, frame, marker)
	}, func() {
		for range 3 {
			frame()
		}
	})
	for range 100 {
		if _, more := frame(); !more {
			return got, ok
		}
	}
	t.Fatal("titleFlow did not end")
	return got, ok
}

func TestTitleFlowDeclined(t *testing.T) {
	got, ok := titleFlowHarness(t, false, func(ctx *Context, await func(string), pump func()) {
		await("Name this wallet")
		click(&ctx.Router, Down)
		await("NO TITLE")
		click(&ctx.Router, Button3)
	})
	if !ok || got != "" {
		t.Errorf("declining returned %q, %v; want empty, true", got, ok)
	}
}

func TestTitleFlowTyped(t *testing.T) {
	// The editor opens on the lowercase layer; runes must speak the
	// active layer's alphabet, as on the device.
	got, ok := titleFlowHarness(t, true, func(ctx *Context, await func(string), pump func()) {
		await("Wallet Title")
		runes(&ctx.Router, "vault 1")
		pump()
		click(&ctx.Router, Button2)
	})
	if !ok || got != "vault 1" {
		t.Errorf("typed title returned %q, %v; want %q, true", got, ok, "vault 1")
	}
}

// A title past the plate cap is rejected at entry, not truncated on
// steel: the screens and every plate of the set must agree on the name.
func TestTitleFlowTooLong(t *testing.T) {
	long := strings.Repeat("a", backup.MaxTitleLen+1)
	_, ok := titleFlowHarness(t, true, func(ctx *Context, await func(string), pump func()) {
		await("Wallet Title")
		runes(&ctx.Router, long)
		pump()
		click(&ctx.Router, Button2)
		await("characters")
		click(&ctx.Router, Button3)
		pump()
		click(&ctx.Router, Button1)
	})
	if ok {
		t.Error("an over-cap title was accepted")
	}
}

// The single-seed flow feeds the typed title through to the plates:
// a titled seed plate engraves more strokes than a bare one, in the
// spot the layout has reserved since v1.
func TestSeedPlateTitle(t *testing.T) {
	m := testMnemonic(t, 24)
	title := strings.Repeat("W", backup.MaxTitleLen)
	titled, err := engraveSeed(engraverParams, SquarePlate, m, "", title)
	if err != nil {
		t.Fatal(err)
	}
	bare, err := engraveSeed(engraverParams, SquarePlate, m, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := toPlate(titled, engraverParams, SquarePlate); err != nil {
		t.Fatalf("titled 24-word square plate does not plan: %v", err)
	}
	_, titledKnots := measureLayout(titled)
	_, bareKnots := measureLayout(bare)
	if titledKnots <= bareKnots {
		t.Errorf("title engraved no strokes: %d knots titled, %d bare", titledKnots, bareKnots)
	}
}

// The widest allowed title must not silently cost the 12-word seed its
// small-plate offer: the title reads up the small plate's edge, and the
// fit that gates the plate question has to keep accepting it.
func TestSeedPlateTitleFitsSmall(t *testing.T) {
	m := testMnemonic(t, 12)
	title := strings.Repeat("W", backup.MaxTitleLen)
	plan, err := engraveSeed(engraverParams, SmallPlate, m, "", title)
	if err != nil {
		t.Fatal(err)
	}
	if !layoutFits(plan, engraverParams, SmallPlate) {
		t.Errorf("a %d-character title pushes the 12-word seed off the small plate", backup.MaxTitleLen)
	}
}

func TestPassphrasePlateTitle(t *testing.T) {
	txt := passphrasePlate("hunter2", "CA2C62D2", "m/84h/0h/0h", "My Vault")
	lines := strings.Split(txt, "\n")
	var wallet, titleLine, path int
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "WALLET "):
			wallet = i
		case strings.HasPrefix(l, "TITLE "):
			titleLine = i
		case strings.HasPrefix(l, "PATH "):
			path = i
		}
	}
	if lines[titleLine] != "TITLE MY VAULT" {
		t.Errorf("title line %q, want %q", lines[titleLine], "TITLE MY VAULT")
	}
	if !(wallet < titleLine && titleLine < path) {
		t.Errorf("line order WALLET=%d TITLE=%d PATH=%d, want WALLET < TITLE < PATH", wallet, titleLine, path)
	}
	if bare := passphrasePlate("hunter2", "CA2C62D2", "m/84h/0h/0h", ""); strings.Contains(bare, "TITLE") {
		t.Error("an untitled passphrase plate carries a TITLE line")
	}
}
