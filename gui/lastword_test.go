package gui

import (
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
