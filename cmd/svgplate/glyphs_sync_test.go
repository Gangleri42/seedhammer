package main

import (
	"os"
	"testing"
)

// TestEditorGlyphsInSync guards the editor's self-contained glyphs.js
// copy against the canonical cmd/textplate/glyphs.js. The editor ships
// its own copy so it hosts as a self-contained static app; this test
// makes the copy drift a build failure. Regenerate with
// "go run seedhammer.com/cmd/textplate cmd/textplate/glyphs.js" and
// copy it into cmd/svgplate/editor/glyphs.js.
func TestEditorGlyphsInSync(t *testing.T) {
	canonical, err := os.ReadFile("../textplate/glyphs.js")
	if err != nil {
		t.Fatal(err)
	}
	editor, err := os.ReadFile("editor/glyphs.js")
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical) != string(editor) {
		t.Errorf("editor/glyphs.js (%d bytes) differs from cmd/textplate/glyphs.js (%d bytes); recopy it",
			len(editor), len(canonical))
	}
}
