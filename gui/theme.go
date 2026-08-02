package gui

import (
	"image/color"

	"seedhammer.com/font/comfortaa"
	"seedhammer.com/font/poppins"
	"seedhammer.com/gui/text"
)

var theme struct {
	overlayMask  uint8
	activeMask   uint8
	inactiveMask uint8
}

type Styles struct {
	title    text.Style
	subtitle text.Style
	body     text.Style
	lead     text.Style
	button   text.Style
	word     text.Style
	keyboard text.Style
	warning  text.Style
	debug    text.Style
	progress text.Style
}

type Colors struct {
	Background color.RGBA
	Text       color.RGBA
	Primary    color.RGBA
}

var (
	descriptorTheme Colors
	singleTheme     Colors
	engraveTheme    Colors
	cameraTheme     Colors
)

const leadingSize = 44

// The palette is three colours and no more: a black ground, white text,
// and orange for whatever the primary action is. It replaces a scheme
// that carried a green, two blues and three near-whites, where the mode
// a screen belonged to was signalled by its background colour.
const (
	black  = 0x000000
	white  = 0xffffff
	orange = 0xdd9700
)

func init() {
	// One palette across every mode. The names are kept because the
	// screens still ask for them, and because a mode may want to differ
	// again; nothing should introduce a fourth colour to do it.
	palette := Colors{
		Background: rgb(black),
		Text:       rgb(white),
		Primary:    rgb(orange),
	}
	descriptorTheme = palette
	singleTheme = palette
	engraveTheme = palette
	cameraTheme = Colors{
		Text: rgb(white),
	}
	theme.overlayMask = 0x55
	theme.activeMask = 0x55
	theme.inactiveMask = 0x55
}

func NewStyles() Styles {
	return Styles{
		title: text.Style{
			Face:            poppins.Bold25,
			Alignment:       text.AlignCenter,
			LetterSpacing:   -1,
			LineHeightScale: 0.75,
		},
		body: text.Style{
			Face:            poppins.Regular16,
			LineHeightScale: 0.75,
		},
		debug: text.Style{
			Face: poppins.Bold10,
		},
		warning: text.Style{
			Face:            poppins.Bold25,
			LineHeightScale: 0.75,
			Alignment:       text.AlignCenter,
		},
		lead: text.Style{
			Face:            poppins.Regular16,
			LineHeightScale: 0.9,
			Alignment:       text.AlignCenter,
		},
		subtitle: text.Style{
			Face:            poppins.Bold16,
			LineHeightScale: 0.9,
		},
		button: text.Style{
			Face:            poppins.Bold20,
			Alignment:       text.AlignCenter,
			LineHeightScale: 0.70,
		},
		word: text.Style{
			Face: comfortaa.Bold17,
		},
		keyboard: text.Style{
			Face: poppins.Bold25,
		},
		progress: text.Style{
			Face:          poppins.Boldprogress45,
			Alignment:     text.AlignCenter,
			LetterSpacing: -1,
		},
	}
}
