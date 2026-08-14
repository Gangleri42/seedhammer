package mjolnir2

import (
	"math/rand/v2"
	"slices"
	"testing"
)

// After a Reset the only truthful advance is zero words: the ring
// maps a remaining-count R to len(buf)-R completed words, so a stale
// TRANS_COUNT remainder from an aborted job credits words this job
// never wrote. The ring cannot tell the difference, which is why
// Device.advanceDMA is gated on a running job.
func TestRingAdvanceAfterReset(t *testing.T) {
	r := newRing(make([]uint32, 16))
	r.Write(make([]uint32, 10))
	r.AdvanceRead(16 - 6)
	r.Reset()
	if got := r.AdvanceRead(0); got != 0 {
		t.Errorf("a zero post-reset advance credited %d words", got)
	}
	// The phantom shape the gate exists for: a stale remainder
	// credits words the ring never carried.
	if got := r.AdvanceRead(5); got != 16-5 {
		t.Errorf("a stale remainder of 5 credited %d words, not the %d the arithmetic implies", got, 16-5)
	}
}

func TestRing(t *testing.T) {
	buf := make([]uint32, 1024)
	r := newRing(buf)
	want := make([]uint32, 8000)
	for i := range want {
		want[i] = uint32(i + 1)
	}
	var got []uint32
	data := want
	avail := 0
	s1, s2 := rand.Uint64(), rand.Uint64()
	rng := rand.New(rand.NewPCG(s1, s2))
	for len(got) != len(want) {
		nw := rng.IntN(len(data) + 1)
		wrote := r.Write(data[:nw])
		data = data[wrote:]
		avail += wrote
		if r.buf[r.writeIdx] != 0 {
			t.Fatalf("seed %x/%x: missing halting zero after writing %d", s1, s2, wrote)
		}
		nr := rng.IntN(avail + 1)
		for i := range nr {
			got = append(got, ringRead(r, i))
		}
		avail -= nr
		ridx := (r.readIdx + nr) % len(r.buf)
		completed := r.AdvanceRead(len(r.buf) - ridx)
		if completed != nr {
			t.Fatalf("seed %x/%x: ring reported %d reads, expected %d", s1, s2, completed, nr)
		}
	}
	if !slices.Equal(want, got) {
		t.Fatalf("seed %x/%x: data read did not match data written", s1, s2)
	}
}

func ringRead(r *ring, idx int) uint32 {
	i := (r.readIdx + idx) % len(r.buf)
	return r.buf[i]
}
