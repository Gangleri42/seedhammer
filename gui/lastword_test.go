package gui

import (
	"strings"
	"testing"

	"seedhammer.com/bip39"
)

// The offer belongs to a new seed's one empty final box. A complete
// scanned phrase being edited must never see it: accepting would
// silently replace the user's word, and the swapped plate would still
// be checksum-valid, so the backup check could not flag it.
func TestLastWordOffer(t *testing.T) {
	complete := make(bip39.Mnemonic, 12)
	for i := range complete {
		complete[i] = bip39.Word(i)
	}
	empty := append(bip39.Mnemonic{}, complete...)
	empty[11] = -1
	missingEarlier := append(bip39.Mnemonic{}, empty...)
	missingEarlier[3] = -1
	tests := []struct {
		name     string
		m        bip39.Mnemonic
		selected int
		frag     string
		want     bool
	}{
		{"empty final box", empty, 11, "", true},
		{"typing in the final box", empty, 11, "ab", false},
		{"final box already filled", complete, 11, "", false},
		{"earlier word missing", missingEarlier, 11, "", false},
		{"not the final box", empty, 3, "", false},
	}
	for _, test := range tests {
		if got := lastWordOffer(test.m, test.selected, test.frag); got != test.want {
			t.Errorf("%s: offer = %v, want %v", test.name, got, test.want)
		}
	}
}

// A filled box opens on its word and edits like any fragment. Deleting
// the word empties the box, which on the final word brings the offer
// back; Back keeps the word, the checkmark commits the edit.
func TestFilledBoxOpensOnItsWord(t *testing.T) {
	m := make(bip39.Mnemonic, 12)
	for i := range m {
		m[i] = bip39.Word(i)
	}
	last := bip39.LabelFor(m[11])
	backspaces := strings.Repeat("⌫", len(last))

	// Final box: the word shows, deleting it brings the offer, Back
	// puts the word back.
	ctx := NewContext(newPlatform())
	frame, quit := runUI(ctx, func() { inputWordsFlow(ctx, &descriptorTheme, m, 11) })
	awaitUI(t, frame, last)
	if content, _ := frame(); uiContains(content, lastWordHint) {
		t.Fatal("the offer showed while the box held its word")
	}
	runes(&ctx.Router, backspaces)
	awaitUI(t, frame, lastWordHint)
	click(&ctx.Router, Button1)
	for range 10000 {
		if _, more := frame(); !more {
			break
		}
	}
	quit()
	if bip39.LabelFor(m[11]) != last {
		t.Fatalf("Back left word 12 as %q, want %q", bip39.LabelFor(m[11]), last)
	}

	// An earlier box: delete the word, type another, the checkmark
	// commits it and the flow ends because every other box is filled.
	fourth := bip39.LabelFor(m[3])
	ctx = NewContext(newPlatform())
	frame, quit = runUI(ctx, func() { inputWordsFlow(ctx, &descriptorTheme, m, 3) })
	awaitUI(t, frame, fourth)
	runes(&ctx.Router, strings.Repeat("⌫", len(fourth))+"zoo")
	click(&ctx.Router, Button2)
	for range 10000 {
		if _, more := frame(); !more {
			break
		}
	}
	quit()
	if got := bip39.LabelFor(m[3]); !strings.EqualFold(got, "zoo") {
		t.Fatalf("checkmark left word 4 as %q, want zoo", got)
	}
	// Nothing else moved.
	if bip39.LabelFor(m[11]) != last {
		t.Fatalf("editing word 4 changed word 12 to %q", bip39.LabelFor(m[11]))
	}
}
