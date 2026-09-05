package shamir

import (
	"bytes"
	"math/rand"
	"testing"
)

func TestSplitCombineRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for _, kn := range [][2]int{{1, 1}, {1, 5}, {2, 2}, {2, 4}, {3, 5}, {5, 5}, {2, 16}, {16, 16}, {3, 255}} {
		k, n := kn[0], kn[1]
		for _, size := range []int{1, 16, 32, 257} {
			secret := make([]byte, size)
			rng.Read(secret)
			shares, err := Split(secret, k, n, rng)
			if err != nil {
				t.Fatalf("Split(%d-of-%d, %d bytes): %v", k, n, size, err)
			}
			if len(shares) != n {
				t.Fatalf("Split returned %d shares, want %d", len(shares), n)
			}
			got, err := Combine(shares[:k])
			if err != nil {
				t.Fatalf("Combine(%d-of-%d, %d bytes): %v", k, n, size, err)
			}
			if !bytes.Equal(got, secret) {
				t.Fatalf("round trip failed for %d-of-%d, %d bytes", k, n, size)
			}
		}
	}
}

// TestAllSubsets recovers from every threshold-sized and larger subset
// for a few small schemes.
func TestAllSubsets(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	for _, kn := range [][2]int{{2, 3}, {2, 4}, {3, 5}, {2, 6}, {4, 6}} {
		k, n := kn[0], kn[1]
		secret := make([]byte, 48)
		rng.Read(secret)
		shares, err := Split(secret, k, n, rng)
		if err != nil {
			t.Fatal(err)
		}
		// Iterate over all subsets of size >= k.
		for mask := 0; mask < 1<<n; mask++ {
			var sel [][]byte
			for i := 0; i < n; i++ {
				if mask&(1<<i) != 0 {
					sel = append(sel, shares[i])
				}
			}
			if len(sel) < k {
				continue
			}
			got, err := Combine(sel)
			if err != nil {
				t.Fatalf("%d-of-%d subset %06b: %v", k, n, mask, err)
			}
			if !bytes.Equal(got, secret) {
				t.Fatalf("%d-of-%d subset %06b: wrong secret", k, n, mask)
			}
		}
	}
}

// TestPrivacy checks the information-theoretic property directly for
// one-byte secrets and k=2: for a fixed secret byte and share index,
// the 256 raw random bytes map onto all 256 share values. A lone
// share value is therefore consistent with every secret equally, each
// explained by exactly one random byte, hedge included.
func TestPrivacy(t *testing.T) {
	for _, s := range []byte{0, 1, 42, 255} {
		for x := 1; x <= 8; x++ {
			var seen [256]bool
			for r := 0; r < 256; r++ {
				shares, err := Split([]byte{s}, 2, 8, &constReader{b: byte(r)})
				if err != nil {
					t.Fatal(err)
				}
				y := shares[x-1][1]
				if seen[y] {
					t.Fatalf("secret %d, x %d: share value %d for two rand bytes", s, x, y)
				}
				seen[y] = true
			}
		}
	}
}

// TestSharesDistinct verifies shares of a nontrivial secret are
// pairwise different (overwhelmingly likely with a sound RNG, and a
// tripwire for coefficient-reuse bugs).
func TestSharesDistinct(t *testing.T) {
	secret := bytes.Repeat([]byte{0x42}, 64)
	shares, err := Split(secret, 3, 5, rand.New(rand.NewSource(3)))
	if err != nil {
		t.Fatal(err)
	}
	for i := range shares {
		for j := i + 1; j < len(shares); j++ {
			if bytes.Equal(shares[i][1:], shares[j][1:]) {
				t.Fatalf("shares %d and %d are identical", i, j)
			}
		}
	}
}

// TestSplitBrokenRand pins the hedge: a random source failed to
// constant output must not produce the degree-0 polynomials that
// would hand every share holder the secret.
func TestSplitBrokenRand(t *testing.T) {
	secret := []byte("the quick brown fox jumps over the lazy dog")
	shares, err := Split(secret, 2, 3, &constReader{0})
	if err != nil {
		t.Fatal(err)
	}
	for _, sh := range shares {
		if bytes.Equal(sh[1:], secret) {
			t.Fatalf("share %d equals the secret: coefficients were not hedged", sh[0])
		}
	}
	got, err := Combine(shares[:2])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, secret) {
		t.Fatal("hedged shares do not combine to the secret")
	}
}

func TestSplitErrors(t *testing.T) {
	rng := rand.New(rand.NewSource(4))
	if _, err := Split(nil, 2, 3, rng); err == nil {
		t.Fatal("empty secret: expected error")
	}
	if _, err := Split([]byte{1}, 0, 3, rng); err == nil {
		t.Fatal("k=0: expected error")
	}
	if _, err := Split([]byte{1}, 4, 3, rng); err == nil {
		t.Fatal("k>n: expected error")
	}
	if _, err := Split([]byte{1}, 2, 256, rng); err == nil {
		t.Fatal("n>255: expected error")
	}
	// A failing rand must surface.
	if _, err := Split([]byte{1, 2, 3}, 2, 3, &bytes.Reader{}); err == nil {
		t.Fatal("exhausted rand: expected error")
	}
}

func TestCombineErrors(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	shares, err := Split([]byte("0123456789abcdef"), 2, 3, rng)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Combine(nil); err == nil {
		t.Fatal("no shares: expected error")
	}
	if _, err := Combine([][]byte{shares[0], shares[0]}); err == nil {
		t.Fatal("duplicate share: expected error")
	}
	bad := bytes.Clone(shares[1])
	bad[0] = 0
	if _, err := Combine([][]byte{shares[0], bad}); err == nil {
		t.Fatal("index 0: expected error")
	}
	short := [][]byte{shares[0], {1, 2}}
	if _, err := Combine(short); err == nil {
		t.Fatal("different lengths: expected error")
	}
}

func FuzzSplitCombine(f *testing.F) {
	f.Add([]byte("seedhammer"))
	f.Add([]byte{0})
	f.Fuzz(func(t *testing.T, secret []byte) {
		if len(secret) == 0 || len(secret) > 4096 {
			t.Skip("out of scope for the round trip fuzzer")
		}
		// Derive k, n deterministically from the input.
		n := 1 + int(secret[0])%16
		k := 1 + int(secret[len(secret)-1])%n
		rng := rand.New(rand.NewSource(int64(len(secret))))
		shares, err := Split(secret, k, n, rng)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Combine(shares[:k])
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, secret) {
			t.Fatal("round trip mismatch")
		}
	})
}

func FuzzCombine(f *testing.F) {
	f.Add([]byte{1, 2, 3}, []byte{2, 4, 6})
	f.Fuzz(func(t *testing.T, a, b []byte) {
		Combine([][]byte{a, b}) //nolint:errcheck // fuzzing for panics, not errors
	})
}

// constReader is an io.Reader that yields one fixed byte forever.
type constReader struct{ b byte }

func (r *constReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.b
	}
	return len(p), nil
}
