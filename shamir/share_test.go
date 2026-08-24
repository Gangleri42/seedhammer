package shamir

import (
	"bytes"
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
			if rec.Corrupt != 0 {
				t.Fatalf("clean recovery named share %d corrupt", rec.Corrupt)
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
	if rec.Corrupt != 2 {
		t.Fatalf("corrupt share named %d, want 2", rec.Corrupt)
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
	// Now at 3 shares of a 2-of-3 whose corrupt share is the spare:
	// the first combination is clean and recovery succeeds without a
	// retry. Remove the corrupt share's peers instead: a fresh set
	// with only corrupt data must fail.
	if rec, err := s.Recover(); err != nil {
		t.Fatal("expected recovery from the clean subset:", err)
	} else if rec.Corrupt != 0 {
		t.Fatalf("clean recovery named share %d corrupt", rec.Corrupt)
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
