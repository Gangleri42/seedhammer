package bip380

import (
	"strings"
	"testing"
)

func TestChecksumExpandCapacity(t *testing.T) {
	// Both callers append the eight checksum symbols to the expanded
	// slice; the reservation must absorb them for every length class
	// modulo three, or a nearly complete symbol buffer reallocates.
	for _, n := range []int{0, 1, 2, 3, 4, 5, 6, 972, 973, 974} {
		syms, ok := checksumExpand(strings.Repeat("a", n))
		if !ok {
			t.Fatalf("len %d: expand failed", n)
		}
		if want := n + (n+2)/3; len(syms) != want {
			t.Errorf("len %d: %d symbols, want %d", n, len(syms), want)
		}
		if cap(syms) < len(syms)+8 {
			t.Errorf("len %d: cap %d cannot take the checksum without reallocating", n, cap(syms))
		}
	}
}
