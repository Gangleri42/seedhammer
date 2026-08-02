// Package trng reads the hardware true random number generator built
// into the rp2350.
//
// The block conditions and health-checks its own output: a von Neumann
// decorrelator, a repetition check (CRNGT) and an autocorrelation test
// are all enabled out of reset, and the block reports through RNG_ISR
// what they found. Nothing here whitens or stretches the entropy, and
// nothing here falls back to a weaker source when the hardware reports
// a failure — a caller that needs entropy for key material must see the
// error rather than a plausible substitute.
//
// The alternative on this chip is machine.GetRNG, a ring oscillator
// sampled through an 8-bit LFSR, which TinyGo documents as unsuitable
// for cryptographic use.
package trng

import "errors"

// ErrUnavailable reports a build with no hardware entropy source. Only
// the rp2350 target has one.
var ErrUnavailable = errors.New("trng: no hardware entropy source on this target")

// ErrUnhealthy reports that the block's own entropy checks rejected
// every attempt. The reader yields no bytes rather than degraded ones.
var ErrUnhealthy = errors.New("trng: entropy checks failed")

// ehrBytes is the width of the entropy holding register, 192 bits.
const ehrBytes = 24

// Reader draws bytes from the hardware generator. It is not safe for
// concurrent use.
//
// Reads are served from one entropy holding register at a time, so a
// caller wanting a few bits pays for a refill only once every 24 bytes.
// A refill blocks for on the order of ten milliseconds at the sample
// count this driver uses, and the datasheet warns the tail can run far
// longer, so do not call this from a frame loop.
type Reader struct {
	buf [ehrBytes]byte
	// n counts bytes already handed out of buf.
	n int
	// started reports whether buf holds a generation's output.
	started bool
}

// Read implements io.Reader. It returns ErrUnavailable off-device and
// ErrUnhealthy when the block's entropy checks reject every attempt.
func (r *Reader) Read(p []byte) (int, error) {
	if fill == nil {
		return 0, ErrUnavailable
	}
	for i := range p {
		if !r.started || r.n == len(r.buf) {
			if err := fill(&r.buf); err != nil {
				return i, err
			}
			r.started = true
			r.n = 0
		}
		p[i] = r.buf[r.n]
		r.n++
	}
	return len(p), nil
}

// Health reports the block's cumulative autocorrelation statistics,
// which is the hardware's own opinion of its entropy source. Both
// counters saturate; a fails count that climbs with trys is the signal
// to investigate. It returns false off-device.
func Health() (fails, trys uint32, ok bool) {
	if health == nil {
		return 0, 0, false
	}
	f, t := health()
	return f, t, true
}

// fill loads one entropy holding register, retrying past the recoverable
// health failures. The rp2350 build installs it.
var fill func(ehr *[ehrBytes]byte) error

// health reads the autocorrelation fail/try counters. The rp2350 build
// installs it.
var health func() (fails, trys uint32)
