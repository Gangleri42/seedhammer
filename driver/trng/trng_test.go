package trng

import (
	"bytes"
	"errors"
	"testing"
)

// The reader serves one entropy holding register at a time, so the
// arithmetic around the buffer boundary is what a caller depends on:
// no byte reused, none skipped, and a refill exactly when the previous
// register runs out.
func TestReaderRefills(t *testing.T) {
	fills := 0
	fill = func(ehr *[ehrBytes]byte) error {
		for i := range ehr {
			// Distinct across fills so a reused or skipped byte shows.
			ehr[i] = byte(fills*ehrBytes + i)
		}
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
	if fills != 3 {
		t.Errorf("%d refills for %d bytes, want 3", fills, len(got))
	}
	want := make([]byte, len(got))
	for i := range want {
		want[i] = byte(i)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("bytes served out of order or reused:\n got %v\nwant %v", got, want)
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
