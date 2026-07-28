package bip32

import (
	"testing"

	"github.com/btcsuite/btcd/btcutil/v2/hdkeychain"
)

func TestParsePathElement(t *testing.T) {
	tests := []struct {
		element string
		want    uint32
		ok      bool
	}{
		{"0", 0, true},
		{"1", 1, true},
		{"2147483647", hdkeychain.HardenedKeyStart - 1, true},
		{"0h", hdkeychain.HardenedKeyStart, true},
		{"0'", hdkeychain.HardenedKeyStart, true},
		{"2147483647h", 1<<32 - 1, true},
		// The element range is [0, 2^31) on every platform; parsing
		// with the platform int accepted the first four below on
		// 64-bit hosts, aliasing [2^31, 2^32) with the hardened
		// range, while the 32-bit device rejected them.
		{"2147483648", 0, false},
		{"2147483648h", 0, false},
		{"4294967295", 0, false},
		{"9223372036854775807", 0, false},
		{"18446744073709551616", 0, false},
		{"-1", 0, false},
		{"", 0, false},
		{"x", 0, false},
	}
	for _, test := range tests {
		got, err := ParsePathElement(test.element)
		if test.ok != (err == nil) {
			t.Errorf("ParsePathElement(%q): error %v, want ok %v", test.element, err, test.ok)
			continue
		}
		if err == nil && got != test.want {
			t.Errorf("ParsePathElement(%q) = %d, want %d", test.element, got, test.want)
		}
	}
}

func TestPathRoundTrip(t *testing.T) {
	p, err := ParsePath("m/48'/0'/0'/2'/1/2147483647")
	if err != nil {
		t.Fatal(err)
	}
	if enc, want := p.Encode(), "/48h/0h/0h/2h/1/2147483647"; enc != want {
		t.Errorf("Encode = %q, want %q", enc, want)
	}
}
