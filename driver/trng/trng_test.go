package trng

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
)

// The reader serves one conditioned block at a time, each the hash of
// two raw generations, so the arithmetic around the buffer boundary is
// what a caller depends on: no byte reused, none skipped, a refill
// exactly when the previous block runs out, and two raw fills per
// block.
func TestReaderRefills(t *testing.T) {
	fills := 0
	rawFill := func(k int) [ehrBytes]byte {
		var b [ehrBytes]byte
		for i := range b {
			// Distinct across fills so a reused or skipped byte shows.
			b[i] = byte(k*ehrBytes + i)
		}
		return b
	}
	fill = func(ehr *[ehrBytes]byte) error {
		*ehr = rawFill(fills)
		fills++
		return nil
	}
	defer func() { fill = nil }()

	var r Reader
	got := make([]byte, ehrBytes*2+ehrBytes/2)
	n, err := r.Read(got)
	if err != nil || n != len(got) {
		t.Fatalf("Read = %d, %v; want %d, nil", n, err, len(got))
	}
	if fills != 6 {
		t.Errorf("%d raw fills for %d bytes, want 6 (two per block)", fills, len(got))
	}
	var want []byte
	for blk := range 3 {
		a, b := rawFill(2*blk), rawFill(2*blk+1)
		sum := sha256.Sum256(append(a[:], b[:]...))
		want = append(want, sum[:ehrBytes]...)
	}
	if !bytes.Equal(got, want[:len(got)]) {
		t.Errorf("bytes served out of order or reused:\n got %v\nwant %v", got, want[:len(got)])
	}
}

// The construction is pinned byte-exact: SHA-256 over the two raw
// generations in draw order, truncated to the block width. A swapped
// order or a different length lands here, not on the bench.
func TestReaderConditioningConstruction(t *testing.T) {
	calls := 0
	fill = func(ehr *[ehrBytes]byte) error {
		v := byte(0x11)
		if calls == 1 {
			v = 0x22
		}
		for i := range ehr {
			ehr[i] = v
		}
		calls++
		return nil
	}
	defer func() { fill = nil }()

	var r Reader
	got := make([]byte, ehrBytes)
	if _, err := r.Read(got); err != nil {
		t.Fatal(err)
	}
	const want = "69b7c9b608fb317a9afa3ac98fa66b273221cdb7dc67f0fd"
	if hex := fmt.Sprintf("%x", got); hex != want {
		t.Errorf("conditioned block %s, want %s", hex, want)
	}
}

// A caller that asked for entropy and got an error must not be handed a
// count implying bytes it can use.
func TestReaderReportsFailure(t *testing.T) {
	boom := errors.New("boom")
	fill = func(*[ehrBytes]byte) error { return boom }
	defer func() { fill = nil }()

	var r Reader
	buf := []byte{1, 2, 3, 4}
	n, err := r.Read(buf)
	if !errors.Is(err, boom) {
		t.Errorf("Read error = %v, want %v", err, boom)
	}
	if n != 0 {
		t.Errorf("Read reported %d bytes on a failed fill, want 0", n)
	}
	if !bytes.Equal(buf, []byte{1, 2, 3, 4}) {
		t.Errorf("destination was written on a failed fill: %v", buf)
	}

	// The second raw generation failing must not serve a
	// half-conditioned block either.
	calls := 0
	fill = func(*[ehrBytes]byte) error {
		calls++
		if calls == 2 {
			return boom
		}
		return nil
	}
	var r2 Reader
	n, err = r2.Read(buf)
	if !errors.Is(err, boom) {
		t.Errorf("second-fill failure: Read error = %v, want %v", err, boom)
	}
	if n != 0 {
		t.Errorf("second-fill failure: Read reported %d bytes, want 0", n)
	}
	if !bytes.Equal(buf, []byte{1, 2, 3, 4}) {
		t.Errorf("second-fill failure wrote the destination: %v", buf)
	}
}

// Off-device, and on a build where the rp2350 file is excluded, the
// reader must say so rather than return zeros that look like entropy.
func TestReaderUnavailable(t *testing.T) {
	fill = nil
	var r Reader
	buf := []byte{9}
	n, err := r.Read(buf)
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("Read error = %v, want ErrUnavailable", err)
	}
	if n != 0 || buf[0] != 9 {
		t.Errorf("Read wrote %d bytes off-device: %v", n, buf)
	}
	if _, _, ok := Health(); ok {
		t.Error("Health reported available with no hardware")
	}
}
