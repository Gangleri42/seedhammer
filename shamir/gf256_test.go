package shamir

import "testing"

// TestMulExhaustive cross-checks the log/exp tables against the
// table-free reference multiplication for every pair of field
// elements.
func TestMulExhaustive(t *testing.T) {
	for a := 0; a < 256; a++ {
		for b := 0; b < 256; b++ {
			if got, want := mul(byte(a), byte(b)), byte(mulSlow(byte(a), byte(b))); got != want {
				t.Fatalf("mul(%d, %d) = %d, want %d", a, b, got, want)
			}
		}
	}
}

// TestDivExhaustive checks that div inverts mul for every nonzero
// divisor.
func TestDivExhaustive(t *testing.T) {
	for a := 0; a < 256; a++ {
		for b := 1; b < 256; b++ {
			if got := mul(div(byte(a), byte(b)), byte(b)); got != byte(a) {
				t.Fatalf("div round trip failed for %d/%d: got %d", a, b, got)
			}
		}
	}
}

// TestLogExp checks the tables are inverse bijections of the nonzero
// elements and the exponents 0..254.
func TestLogExp(t *testing.T) {
	for x := 1; x < 256; x++ {
		if got := exp[log[byte(x)]]; got != byte(x) {
			t.Fatalf("exp[log[%d]] = %d", x, got)
		}
	}
	for i := 0; i < 255; i++ {
		if got := log[exp[i]]; got != byte(i) {
			t.Fatalf("log[exp[%d]] = %d", i, got)
		}
		if exp[i] == 0 || exp[i+255] != exp[i] {
			t.Fatalf("exp table invalid at %d", i)
		}
	}
}
