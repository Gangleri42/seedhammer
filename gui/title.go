package gui

import (
	"errors"

	"seedhammer.com/backup"
	"seedhammer.com/font/sh"
)

// A title longer than the plate cap engraves truncated, and a set
// whose name loses its tail on steel identifies the wrong wallet.
// Rejecting at entry keeps the screens and every plate in agreement.
var errTitleTooLong = errors.New("A title engraves at up to " + itoa(backup.MaxTitleLen) + " characters.")

// titleFlow asks for the wallet title that names the plates of a set.
// required skips the opening choice: the multisig builder cannot
// label its set without one, while the single-seed flow keeps the
// untitled plate as the ordinary case it always was. The title is
// carried as typed; plates uppercase it through backup.TitleString.
func titleFlow(ctx *Context, th *Colors, required bool, initial string) (string, bool) {
	if !required {
		// ADD first: the screen only appears on the way to a plate, so
		// the selection lands on the action, as on the plate offers.
		cs := &ChoiceScreen{
			Title:   "Wallet Title",
			Lead:    "Name this wallet on its plates?",
			Choices: []string{"ADD TITLE", "NO TITLE"},
		}
		choice, ok := cs.Choose(ctx, th)
		if !ok {
			return "", false
		}
		if choice == 1 {
			return "", true
		}
	}
	title := initial
	for {
		var ok bool
		// The passphrase alphabet: every printable ASCII character and
		// no newline, which is exactly what a one-line title can carry.
		title, ok = inputTextFlow(ctx, th, "Wallet Title", &passLayers, title)
		if !ok {
			return "", false
		}
		// ASCII by construction (the keyboard's alphabet), so length in
		// bytes is length in characters, the same count TitleString cuts
		// at when engraving.
		if len(title) > backup.MaxTitleLen {
			showError(ctx, th, errTitleTooLong, blankScreen)
			continue
		}
		if backup.TitleString(sh.Font, title) == "" {
			// Unreachable from the device keyboard, whose characters all
			// engrave; guards a future initial value that does not.
			continue
		}
		return title, true
	}
}
