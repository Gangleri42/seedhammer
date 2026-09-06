package shamir

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math/rand"
	"slices"
	"testing"

	"seedhammer.com/bbqr"
)

// joinParts decodes one BBQr series into its payload, checking the
// series file type.
func joinParts(t *testing.T, parts []string) []byte {
	t.Helper()
	typ, payload, err := bbqr.Join(parts)
	if err != nil {
		t.Fatal(err)
	}
	if typ != bbqr.TypeShamir {
		t.Fatalf("share series has file type %c, want %c", typ, bbqr.TypeShamir)
	}
	return payload
}

func TestSplitDataRecover(t *testing.T) {
	rng := rand.New(rand.NewSource(6))
	for _, data := range [][]byte{
		[]byte("abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"),
		bytes.Repeat([]byte("compressible "), 200), // compresses
		rngBytes(t, 1000), // does not compress
	} {
		for _, kn := range [][2]int{{2, 2}, {2, 4}, {3, 5}} {
			k, n := kn[0], kn[1]
			series, err := SplitData(bbqr.TypeText, data, k, n, rng)
			if err != nil {
				t.Fatalf("SplitData(%d bytes, %d-of-%d): %v", len(data), k, n, err)
			}
			if len(series) != n {
				t.Fatalf("got %d series, want %d", len(series), n)
			}
			var s Set
			// Add shares out of order, with duplicates.
			for _, idx := range rng.Perm(n)[:k] {
				payload := joinParts(t, series[idx].Parts)
				if err := s.Add(payload); err != nil {
					t.Fatal(err)
				}
				if err := s.Add(slices.Clone(payload)); err != nil {
					t.Fatal("duplicate share must be a no-op:", err)
				}
			}
			if !s.Complete() {
				have, need := s.Progress()
				t.Fatalf("not complete after %d of %d shares", have, need)
			}
			rec, err := s.Recover()
			if err != nil {
				t.Fatal(err)
			}
			if rec.FileType != bbqr.TypeText {
				t.Fatalf("recovered file type %c, want %c", rec.FileType, bbqr.TypeText)
			}
			if len(rec.Corrupt) != 0 {
				t.Fatalf("clean recovery named shares %v corrupt", rec.Corrupt)
			}
			if !bytes.Equal(rec.Data, data) {
				t.Fatalf("recover mismatch for %d bytes, %d-of-%d", len(data), k, n)
			}
		}
	}
}

// TestCorruptShareRecovery splits 3-of-5, corrupts one share, and
// expects Recover to succeed with a spare and name the corrupt share.
func TestCorruptShareRecovery(t *testing.T) {
	data := []byte("correct horse battery staple")
	series, err := SplitData(bbqr.TypeText, data, 3, 5, rand.New(rand.NewSource(7)))
	if err != nil {
		t.Fatal(err)
	}
	var s Set
	for i, sr := range series {
		payload := joinParts(t, sr.Parts)
		if i == 1 {
			payload[len(payload)-1] ^= 0xFF // corrupt the last share byte
		}
		if err := s.Add(payload); err != nil {
			t.Fatal(err)
		}
	}
	rec, err := s.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rec.Data, data) {
		t.Fatal("recovered data mismatch after corruption")
	}
	if !slices.Equal(rec.Corrupt, []int{2}) {
		t.Fatalf("corrupt shares named %v, want [2]", rec.Corrupt)
	}
}

// TestCorruptShareFatal corrupts a share when only the threshold
// number of shares is available: recovery must fail loudly via the
// digest, never silently.
func TestCorruptShareFatal(t *testing.T) {
	data := []byte("correct horse battery staple")
	series, err := SplitData(bbqr.TypeText, data, 2, 3, rand.New(rand.NewSource(8)))
	if err != nil {
		t.Fatal(err)
	}
	var s Set
	for _, sr := range series[:2] {
		payload := joinParts(t, sr.Parts)
		if err := s.Add(payload); err != nil {
			t.Fatal(err)
		}
	}
	payload := joinParts(t, series[2].Parts)
	payload[prefixLen] ^= 1
	if err := s.Add(payload); err != nil {
		t.Fatal(err)
	}
	// At 3 shares of a 2-of-3 whose corrupt share is the spare, the
	// first combination is clean and recovery succeeds; the spare is
	// checked against it and named. Remove the corrupt share's peers
	// instead: a fresh set with only corrupt data must fail.
	if rec, err := s.Recover(); err != nil {
		t.Fatal("expected recovery from the clean subset:", err)
	} else if !slices.Equal(rec.Corrupt, []int{3}) {
		t.Fatalf("corrupt shares named %v, want the spare [3]", rec.Corrupt)
	}
	var s2 Set
	if err := s2.Add(payload); err != nil {
		t.Fatal(err)
	}
	payload2 := joinParts(t, series[1].Parts)
	payload2[len(payload2)-2] ^= 0x10
	if err := s2.Add(payload2); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Recover(); err == nil {
		t.Fatal("corrupt shares recovered without digest error")
	}
}

// splitPayloads splits data k-of-n from the seed and returns the n
// share envelopes, index i+1 at position i.
func splitPayloads(t *testing.T, data []byte, k, n int, seed int64) [][]byte {
	t.Helper()
	series, err := SplitData(bbqr.TypeText, data, k, n, rand.New(rand.NewSource(seed)))
	if err != nil {
		t.Fatal(err)
	}
	payloads := make([][]byte, n)
	for i, sr := range series {
		payloads[i] = joinParts(t, sr.Parts)
	}
	return payloads
}

// collectCorrupt holds the envelopes with the 1-based indices in held,
// in that order, flipping one share byte of those listed in bad, at a
// different position per corrupt share: identical errors on two shares
// cancel in any combination that weighs the two equally, the ambiguity
// TestCorruptAttribution pins.
func collectCorrupt(t *testing.T, payloads [][]byte, held, bad []int) *Set {
	t.Helper()
	s := new(Set)
	for _, x := range held {
		payload := bytes.Clone(payloads[x-1])
		if j := slices.Index(bad, x); j >= 0 {
			payload[len(payload)-1-j] ^= 0xFF
		}
		if err := s.Add(payload); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// TestCorruptSpareNamed: every held share is checked against the
// verified polynomial, so a corrupt plate is named wherever it sits in
// the set, member of the first combination or spare, and a set with
// no clean spare fails instead of yielding a wrong secret.
func TestCorruptSpareNamed(t *testing.T) {
	data := []byte("correct horse battery staple")
	all := []int{1, 2, 3, 4, 5}
	for _, tc := range []struct {
		name string
		bad  []int
	}{
		{"spare", []int{4}},
		{"member and first spare", []int{1, 4}},
	} {
		rec, err := collectCorrupt(t, splitPayloads(t, data, 3, 5, 16), all, tc.bad).Recover()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !bytes.Equal(rec.Data, data) {
			t.Fatalf("%s: recovered data mismatch", tc.name)
		}
		if !slices.Equal(rec.Corrupt, tc.bad) {
			t.Fatalf("%s: corrupt shares named %v, want %v", tc.name, rec.Corrupt, tc.bad)
		}
	}
	// Only the threshold held and one of it corrupt: no spare to swap
	// in, and never a wrong secret.
	rec, err := collectCorrupt(t, splitPayloads(t, data, 2, 3, 17), []int{1, 2}, []int{2}).Recover()
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("corrupt share at the threshold: got %v, want ErrCorrupt", err)
	}
	if rec.Data != nil {
		t.Fatalf("failed recovery returned data %q", rec.Data)
	}
}

// lambda0 is the Lagrange coefficient of xs[i] at x=0 over the points
// xs, the weight Combine gives that share's bytes.
func lambda0(xs []byte, i int) byte {
	l := byte(1)
	for m, xm := range xs {
		if m != i {
			l = div(mul(l, xm), xs[i]^xm)
		}
	}
	return l
}

// cancellingPair corrupts shares 1 and 2 of the envelopes in the same
// byte position with errors that cancel in the combination of the
// first k shares: λ1·e1 ⊕ λ2·e2 = 0 at x=0, so that combination
// verifies with a wrong polynomial and the true sealed content. It
// returns the corrupted copies and checks the cancellation with the
// package's own Combine.
func cancellingPair(t *testing.T, payloads [][]byte, k int) [][]byte {
	t.Helper()
	xs := make([]byte, k)
	for i := range xs {
		xs[i] = byte(i + 1)
	}
	e1 := byte(0x5a)
	e2 := div(mul(lambda0(xs, 0), e1), lambda0(xs, 1))
	if e2 == 0 {
		t.Fatal("cancelling error is zero")
	}
	out := make([][]byte, len(payloads))
	for i, p := range payloads {
		out[i] = bytes.Clone(p)
	}
	pos := prefixLen + 3 // a payload byte, past the type byte
	out[0][pos] ^= e1
	out[1][pos] ^= e2
	points := func(ps [][]byte) [][]byte {
		raw := make([][]byte, k)
		for i := range raw {
			raw[i] = append([]byte{byte(i + 1)}, ps[i][prefixLen:]...)
		}
		return raw
	}
	clean, err := Combine(points(payloads))
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := Combine(points(out))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(clean, wrong) {
		t.Fatal("errors do not cancel at x=0")
	}
	return out
}

// TestCorruptAttribution pins attribution by maximal agreement against
// the case the digest alone gets wrong: two corrupt members of the
// first combination whose errors cancel at x=0. That combination
// verifies with a wrong polynomial; naming the shares off it would
// blame every clean spare and clear the culprits.
func TestCorruptAttribution(t *testing.T) {
	data := []byte("correct horse battery staple")
	hold := func(payloads [][]byte, held []int) *Set {
		s := new(Set)
		for _, x := range held {
			if err := s.Add(payloads[x-1]); err != nil {
				t.Fatal(err)
			}
		}
		return s
	}
	// Outvoted: with every share held, the clean shares (3 of 5, 4 of
	// 6) outnumber the wrong polynomial's support (its k members), so
	// the culprits are named and the data is right.
	for _, kn := range [][2]int{{2, 5}, {3, 6}} {
		k, n := kn[0], kn[1]
		payloads := cancellingPair(t, splitPayloads(t, data, k, n, 18), k)
		all := make([]int, n)
		for i := range all {
			all[i] = i + 1
		}
		rec, err := hold(payloads, all).Recover()
		if err != nil {
			t.Fatalf("%d-of-%d: %v", k, n, err)
		}
		if !bytes.Equal(rec.Data, data) {
			t.Fatalf("%d-of-%d: recovered data mismatch", k, n)
		}
		if !slices.Equal(rec.Corrupt, []int{1, 2}) {
			t.Fatalf("%d-of-%d: corrupt shares named %v, want [1 2]", k, n, rec.Corrupt)
		}
	}
	// One clean spare: k+1 shares held, the two corrupt members among
	// them. Only the wrong polynomial verifies (fewer than k clean
	// shares are held, so the true one has no verifying combination),
	// and the single spare cannot outvote its k members. The data is
	// still right, since the errors cancel at x=0, and the spare is
	// the share named: the evidence reads as one corrupt spare. The
	// SPEC states this limit; the tie case below is where the receiver
	// declines to guess.
	payloads := cancellingPair(t, splitPayloads(t, data, 3, 6, 19), 3)
	rec, err := hold(payloads, []int{1, 2, 3, 4}).Recover()
	if err != nil {
		t.Fatal("one spare:", err)
	}
	if !bytes.Equal(rec.Data, data) {
		t.Fatal("one spare: recovered data mismatch")
	}
	if !slices.Equal(rec.Corrupt, []int{4}) {
		t.Fatalf("one spare: corrupt shares named %v, want the outvoted spare [4]", rec.Corrupt)
	}
	// Tie: k+2 shares held. The wrong polynomial's support {1, 2, 3}
	// and the true one's {3, 4, 5} are both three shares, so the
	// receiver reports the ambiguity instead of a guess and returns no
	// data; one more clean share breaks the tie.
	s := hold(payloads, []int{1, 2, 3, 4, 5})
	rec, err = s.Recover()
	if !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("tie: got %v, want ErrAmbiguous", err)
	}
	if !errors.Is(err, ErrCorrupt) {
		t.Fatal("tie: ErrAmbiguous must wrap ErrCorrupt, the receiver's keep-the-set signal")
	}
	if rec.Data != nil || rec.Corrupt != nil {
		t.Fatalf("tie returned data %q, corrupt %v", rec.Data, rec.Corrupt)
	}
	if err := s.Add(payloads[5]); err != nil {
		t.Fatal(err)
	}
	rec, err = s.Recover()
	if err != nil {
		t.Fatal("tie broken:", err)
	}
	if !bytes.Equal(rec.Data, data) || !slices.Equal(rec.Corrupt, []int{1, 2}) {
		t.Fatalf("tie broken: corrupt shares named %v, want [1 2]", rec.Corrupt)
	}
	// Identical errors: 3-of-5 with the same mask flipped in the same
	// byte of shares 1 and 4. The combination {1, 4, 5} weighs its
	// members equally (1 ⊕ 4 = 5, so every Lagrange weight at x=0 is
	// 1) and the two errors cancel in it: a wrong polynomial with
	// three supporters against the true one's {2, 3, 5}, a tie.
	payloads = splitPayloads(t, data, 3, 5, 21)
	for _, x := range []int{1, 4} {
		payloads[x-1][len(payloads[x-1])-1] ^= 0xFF
	}
	if _, err := hold(payloads, []int{1, 2, 3, 4, 5}).Recover(); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("identical errors: got %v, want ErrAmbiguous", err)
	}
}

// TestCorruptOrderIndependent: Corrupt is ascending by share index
// whatever order the shares were added in, and the attribution does
// not depend on which shares form the first combination.
func TestCorruptOrderIndependent(t *testing.T) {
	data := []byte("correct horse battery staple")
	payloads := splitPayloads(t, data, 3, 5, 20)
	for _, held := range [][]int{{5, 4, 3, 2, 1}, {4, 1, 5, 2, 3}, {2, 3, 5, 1, 4}} {
		rec, err := collectCorrupt(t, payloads, held, []int{1, 4}).Recover()
		if err != nil {
			t.Fatalf("held %v: %v", held, err)
		}
		if !bytes.Equal(rec.Data, data) {
			t.Fatalf("held %v: recovered data mismatch", held)
		}
		if !slices.Equal(rec.Corrupt, []int{1, 4}) {
			t.Fatalf("held %v: corrupt shares named %v, want [1 4]", held, rec.Corrupt)
		}
	}
}

// TestCombinationHelpers pins the enumeration order and the subset
// count bound the attribution relies on.
func TestCombinationHelpers(t *testing.T) {
	idx := []int{0, 1}
	var seen [][]int
	for ok := true; ok; ok = nextCombination(idx, 4) {
		seen = append(seen, slices.Clone(idx))
	}
	want := [][]int{{0, 1}, {0, 2}, {0, 3}, {1, 2}, {1, 3}, {2, 3}}
	if !slices.EqualFunc(seen, want, slices.Equal) {
		t.Fatalf("combinations of 4 choose 2: %v", seen)
	}
	for _, tc := range []struct {
		n, k   int
		within bool
	}{
		{5, 3, true},      // 10
		{12, 6, true},     // 924
		{13, 6, false},    // 1716
		{46, 2, false},    // 1035
		{45, 2, true},     // 990
		{255, 2, false},   // 32385
		{255, 127, false}, // far beyond int range if computed in full
		{255, 255, true},  // 1
		{255, 254, true},  // 255
	} {
		if got := subsetsWithin(tc.n, tc.k, attributionCap); got != tc.within {
			t.Errorf("subsetsWithin(%d, %d, %d) = %v", tc.n, tc.k, attributionCap, got)
		}
	}
}

func TestSetErrors(t *testing.T) {
	data := []byte("some reasonably long secret here")
	a, err := SplitData(bbqr.TypeText, data, 2, 3, rand.New(rand.NewSource(9)))
	if err != nil {
		t.Fatal(err)
	}
	b, err := SplitData(bbqr.TypeText, data, 2, 3, rand.New(rand.NewSource(10)))
	if err != nil {
		t.Fatal(err)
	}
	c, err := SplitData(bbqr.TypeText, data, 3, 4, rand.New(rand.NewSource(11)))
	if err != nil {
		t.Fatal(err)
	}
	var s Set
	if s.Complete() {
		t.Fatal("empty set complete")
	}
	if _, err := s.Recover(); err == nil {
		t.Fatal("recover on empty set: expected error")
	}
	if err := s.Add(joinParts(t, a[0].Parts)); err != nil {
		t.Fatal(err)
	}
	if s.Complete() {
		t.Fatal("complete with 1 of 2")
	}
	if err := s.Add(joinParts(t, b[1].Parts)); err == nil {
		t.Fatal("share from a different split accepted")
	}
	if err := s.Add(joinParts(t, c[1].Parts)); err == nil {
		t.Fatal("share from a different scheme accepted")
	}
	if err := s.Add(joinParts(t, a[1].Parts)); err != nil {
		t.Fatal(err)
	}
	if rec, err := s.Recover(); err != nil || !bytes.Equal(rec.Data, data) {
		t.Fatalf("Recover: %v", err)
	}
}

func TestParseShareErrors(t *testing.T) {
	series, err := SplitData(bbqr.TypeText, []byte("twenty four bytes of data!!"), 2, 3, rand.New(rand.NewSource(12)))
	if err != nil {
		t.Fatal(err)
	}
	good := joinParts(t, series[0].Parts)
	sh, err := ParseShare(good)
	if err != nil {
		t.Fatal(err)
	}
	if sh.Threshold != 2 || sh.Index != 1 {
		t.Fatalf("got threshold %d share %d", sh.Threshold, sh.Index)
	}
	if _, err := ParseShare(nil); err == nil {
		t.Fatal("empty: expected error")
	}
	if _, err := ParseShare(good[:prefixLen+2]); err == nil {
		t.Fatal("truncated: expected error")
	}
	bad := bytes.Clone(good)
	bad[2] = 0
	if _, err := ParseShare(bad); err == nil {
		t.Fatal("index 0: expected error")
	}
	for _, k := range []byte{0, 1} {
		bad = bytes.Clone(good)
		bad[3] = k
		if _, err := ParseShare(bad); err == nil {
			t.Fatalf("threshold %d: expected error", k)
		}
	}
}

func TestSplitDataErrors(t *testing.T) {
	rng := rand.New(rand.NewSource(15))
	data := []byte("some data")
	if _, err := SplitData(bbqr.TypeText, nil, 2, 3, rng); err == nil {
		t.Fatal("empty data: expected error")
	}
	if _, err := SplitData(bbqr.TypeText, data, 1, 3, rng); err == nil {
		t.Fatal("1-of-n: expected error")
	}
	if _, err := SplitData(bbqr.TypeText, data, 4, 3, rng); err == nil {
		t.Fatal("k>n: expected error")
	}
	if _, err := SplitData('b', data, 2, 3, rng); err == nil {
		t.Fatal("lowercase file type: expected error")
	}
}

func TestRecoverLimit(t *testing.T) {
	data := bytes.Repeat([]byte("compressible "), 600)
	series, err := SplitData(bbqr.TypeText, data, 2, 3, rand.New(rand.NewSource(14)))
	if err != nil {
		t.Fatal(err)
	}
	collect := func(s *Set) {
		for _, sr := range series[:2] {
			if err := s.Add(joinParts(t, sr.Parts)); err != nil {
				t.Fatal(err)
			}
		}
	}
	capped := Set{Limit: 1024}
	collect(&capped)
	if _, err := capped.Recover(); err == nil {
		t.Fatal("recovery past the size limit succeeded")
	} else if errors.Is(err, ErrCorrupt) {
		t.Fatalf("size limit reported as a corrupt share: %v", err)
	}
	exact := Set{Limit: len(data)}
	collect(&exact)
	rec, err := exact.Recover()
	if err != nil {
		t.Fatalf("recovery at the size limit: %v", err)
	}
	if !bytes.Equal(rec.Data, data) {
		t.Fatal("recovered data differs from the input")
	}
}

// TestSharesAreBase32SingleQR pins the compactness promise: a 32-byte
// secret split 2-of-4 fits one base32 version 5 BBQr QR per share.
func TestSharesAreBase32SingleQR(t *testing.T) {
	secret := rngBytes(t, 32)
	series, err := SplitData(bbqr.TypeBinary, secret, 2, 4, rand.New(rand.NewSource(13)))
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range series {
		if s.Encoding != bbqr.EncBase32 {
			t.Fatalf("share %d encoded as %q", i, s.Encoding)
		}
		if len(s.Parts) != 1 {
			t.Fatalf("share %d spans %d parts", i, len(s.Parts))
		}
		if s.Version != 5 {
			t.Fatalf("share %d at QR version %d, want 5", i, s.Version)
		}
	}
}

func rngBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.New(rand.NewSource(int64(n))).Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// TestAboveCapAttribution covers the fallback past the enumeration
// cap: 2-of-50 with every share held has C(50, 2) = 1225 combinations.
func TestAboveCapAttribution(t *testing.T) {
	if subsetsWithin(50, 2, attributionCap) {
		t.Fatal("2-of-50 is within the cap; the test needs the fallback")
	}
	data := []byte("correct horse battery staple")
	hold := func(payloads [][]byte) *Set {
		s := new(Set)
		for _, p := range payloads {
			if err := s.Add(p); err != nil {
				t.Fatal(err)
			}
		}
		return s
	}
	// A cancelling pair in the first threshold verifies with two
	// supporters against 48 dissenters: the dissenters outvote it.
	rec, err := hold(cancellingPair(t, splitPayloads(t, data, 2, 50, 31), 2)).Recover()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rec.Data, data) || !slices.Equal(rec.Corrupt, []int{1, 2}) {
		t.Fatalf("cancelling pair above the cap: corrupt %v, want [1 2]", rec.Corrupt)
	}
	// Two plainly corrupt shares inside the first threshold defeat
	// every single swap; the next window of threshold positions reads
	// clean and outvotes nothing.
	payloads := splitPayloads(t, data, 2, 50, 32)
	payloads[0][prefixLen+3] ^= 0x11
	payloads[1][prefixLen+5] ^= 0x22
	rec, err = hold(payloads).Recover()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rec.Data, data) || !slices.Equal(rec.Corrupt, []int{1, 2}) {
		t.Fatalf("two corrupt members above the cap: corrupt %v, want [1 2]", rec.Corrupt)
	}
}

// TestTieBroken: two wrong readings tied for support are outvoted by
// a later reading with more, so a tie seen mid-enumeration must not
// stick. 2-of-7 with cancelling pairs on shares 1,2 and 3,4: each
// wrong polynomial has two supporters, the true one has three.
func TestTieBroken(t *testing.T) {
	data := []byte("correct horse battery staple")
	payloads := cancellingPair(t, splitPayloads(t, data, 2, 7, 33), 2)
	xs := []byte{3, 4}
	e1 := byte(0x3c)
	e2 := div(mul(lambda0(xs, 0), e1), lambda0(xs, 1))
	payloads[2][prefixLen+3] ^= e1
	payloads[3][prefixLen+3] ^= e2
	s := new(Set)
	for _, p := range payloads {
		if err := s.Add(p); err != nil {
			t.Fatal(err)
		}
	}
	rec, err := s.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rec.Data, data) || !slices.Equal(rec.Corrupt, []int{1, 2, 3, 4}) {
		t.Fatalf("corrupt %v, want [1 2 3 4]", rec.Corrupt)
	}
}

// TestDerivedReproducible: under the derived profile the set is a
// function of (k, data) alone. Two splits agree part for part, and a
// different threshold or different data changes every share's y
// values and the tag.
func TestDerivedReproducible(t *testing.T) {
	data := mustHex(t, descriptorCBORHex)
	a, err := SplitDataDerived(bbqr.TypeCBOR, data, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	b, err := SplitDataDerived(bbqr.TypeCBOR, data, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 4 || len(b) != 4 {
		t.Fatalf("got %d and %d series, want 4", len(a), len(b))
	}
	for i := range a {
		if !equalStrings(a[i].Parts, b[i].Parts) {
			t.Fatalf("share %d differs between two derived splits of the same input", i+1)
		}
	}
	other := bytes.Clone(data)
	other[len(other)-1] ^= 1
	higherK, err := SplitDataDerived(bbqr.TypeCBOR, data, 3, 4)
	if err != nil {
		t.Fatal(err)
	}
	otherData, err := SplitDataDerived(bbqr.TypeCBOR, other, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		series []bbqr.Series
	}{
		{"threshold 3", higherK},
		{"one data bit", otherData},
	} {
		for i := range a {
			ref, got := parseShare(t, a[i]), parseShare(t, tc.series[i])
			if got.Tag == ref.Tag {
				t.Fatalf("%s: share %d keeps the tag %04x", tc.name, i+1, ref.Tag)
			}
			if bytes.Equal(got.data, ref.data) {
				t.Fatalf("%s: share %d keeps its y values", tc.name, i+1)
			}
		}
	}
}

// TestDerivedPrefix: n is not an input to the derived stream, so a set
// issued at n=5 extends the n=3 set share for share, and a share cut
// under the larger n combines with the shares of the smaller one.
func TestDerivedPrefix(t *testing.T) {
	data := []byte(descriptorText)
	three, err := SplitDataDerived(bbqr.TypeText, data, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	five, err := SplitDataDerived(bbqr.TypeText, data, 2, 5)
	if err != nil {
		t.Fatal(err)
	}
	for i := range three {
		if !equalStrings(three[i].Parts, five[i].Parts) {
			t.Fatalf("share %d of the 2-of-3 set differs from the 2-of-5 set", i+1)
		}
	}
	rec, err := RecoverData([][]byte{joinParts(t, three[0].Parts), joinParts(t, five[4].Parts)})
	if err != nil {
		t.Fatal("share 1 of the 2-of-3 set with share 5 of the 2-of-5 set:", err)
	}
	if !bytes.Equal(rec.Data, data) {
		t.Fatal("mixed set recovered different data")
	}
}

// TestDerivedTagDeterministic: the derived tag and coefficients are the
// PRF stream of SPEC.md section 3a, HMAC-SHA256 counter mode keyed by
// (k, sealed), computed here from the spec text with crypto/hmac over
// the sealed content recombined from the shares. The tag is the 2
// stream bytes after the (k-1)·len(sealed) coefficient bytes; with
// k=2, share 1's y values XOR sealed are the coefficients themselves.
func TestDerivedTagDeterministic(t *testing.T) {
	data := []byte(descriptorText)
	for _, k := range []int{2, 3} {
		series, err := SplitDataDerived(bbqr.TypeText, data, k, 3)
		if err != nil {
			t.Fatal(err)
		}
		envelopes := make([][]byte, len(series))
		for i, s := range series {
			envelopes[i] = joinParts(t, s.Parts)
		}
		sealed := sealedOf(t, envelopes, k)
		ncoef := (k - 1) * len(sealed)
		stream := specDerivedStream(k, sealed, ncoef+2)
		tag := binary.BigEndian.Uint16(stream[ncoef:])
		for i, env := range envelopes {
			sh, err := ParseShare(env)
			if err != nil {
				t.Fatal(err)
			}
			if sh.Tag != tag {
				t.Fatalf("k=%d share %d: tag %04x is not the derived stream's function of (k, sealed), %04x", k, i+1, sh.Tag, tag)
			}
		}
		if k == 2 {
			sh, err := ParseShare(envelopes[0])
			if err != nil {
				t.Fatal(err)
			}
			for j := range sealed {
				if sh.data[j]^sealed[j] != stream[j] {
					t.Fatalf("coefficient %d is %02x, the derived stream has %02x", j, sh.data[j]^sealed[j], stream[j])
				}
			}
		}
	}
}

// specDerivedStream restates SPEC.md section 3a: prk = HMAC-SHA256(key
// = "seedhammer.com/shamir derived v1", k ‖ sealed), then the
// concatenation of HMAC-SHA256(prk, u64be(i)) for i = 0, 1, ...
func specDerivedStream(k int, sealed []byte, n int) []byte {
	extract := hmac.New(sha256.New, []byte("seedhammer.com/shamir derived v1"))
	extract.Write([]byte{byte(k)})
	extract.Write(sealed)
	prk := extract.Sum(nil)
	var out []byte
	for i := uint64(0); len(out) < n; i++ {
		var ctr [8]byte
		binary.BigEndian.PutUint64(ctr[:], i)
		m := hmac.New(sha256.New, prk)
		m.Write(ctr[:])
		out = m.Sum(out)
	}
	return out[:n]
}

// TestSplitDataDerivedErrors: the derived profile validates as
// SplitData does.
func TestSplitDataDerivedErrors(t *testing.T) {
	data := []byte("hello")
	if _, err := SplitDataDerived(bbqr.TypeText, nil, 2, 3); err == nil {
		t.Fatal("empty data: expected error")
	}
	if _, err := SplitDataDerived(bbqr.TypeText, data, 1, 3); err == nil {
		t.Fatal("k=1: expected error")
	}
	if _, err := SplitDataDerived(bbqr.TypeText, data, 4, 3); err == nil {
		t.Fatal("k>n: expected error")
	}
	if _, err := SplitDataDerived('b', data, 2, 3); err == nil {
		t.Fatal("lowercase type: expected error")
	}
}

// parseShare decodes one series to its Share.
func parseShare(t *testing.T, s bbqr.Series) Share {
	t.Helper()
	sh, err := ParseShare(joinParts(t, s.Parts))
	if err != nil {
		t.Fatal(err)
	}
	return sh
}
