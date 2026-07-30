package main

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// parseFingerprint accepts the bare hex run and the plate's grouped
// form: plate 2 engraves the prefix in blocks of four, and what the
// plate shows must be typable verbatim.
func TestParseFingerprintGrouping(t *testing.T) {
	want, err := hex.DecodeString(fixtureFingerprint[:16])
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{
		"6183a9ceb05354a6",
		"6183 a9ce b053 54a6",
		"  6183 A9CE B053 54A6  ",
	} {
		got, err := parseFingerprint(in)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("parseFingerprint(%q) = %x, %v", in, got, err)
		}
	}
	if _, err := parseFingerprint("6183 a9ce b053 54a"); err == nil {
		t.Fatal("odd-length hex accepted")
	}
}
