//go:build tinygo && rp2350

package trng

import (
	"device/rp"
	"encoding/binary"
	"time"
)

// RNG_ISR bits, RP2350 datasheet table 1263.
const (
	isrEHRValid = 1 << 0
	// isrAutocorrErr latches when the autocorrelation test has failed
	// four times running. The block then stops until it is reset, and
	// the bit cannot be cleared through RNG_ICR.
	isrAutocorrErr = 1 << 1
	// isrCRNGTErr reports two consecutive equal 16-bit blocks.
	isrCRNGTErr = 1 << 2
	// isrVNErr reports 32 consecutive identical bits.
	isrVNErr = 1 << 3

	isrRecoverable = isrCRNGTErr | isrVNErr
	// The bits that mean a run has finished, one way or another.
	isrTerminal = isrEHRValid | isrAutocorrErr | isrRecoverable
)

// sampleCount is the rng_clk cycles between ring oscillator samples.
// Measured on the bench, not taken from the datasheet: at 100 the
// conditioned output carries temperature-drifting bit-alternation
// structure (min-entropy 6.0 to 6.8 bits per byte against the 7.88 an
// honest source reads), at 400 it passes the full statistical battery
// across hours of thermal drift, and at 800 it overshoots into mild
// repetition bias. The clean zone is a window the sampling beat can
// wander in, not a plateau, so hold this at the measured point.
// Forty milliseconds per generation is nothing on a screen the user
// just pressed a button on.
const sampleCount = 400

// roscChain selects the ring oscillator's inverter chain length. The
// datasheet gives no lengths for the four settings and recommends 0
// or 1, but on this silicon chain 1 starves under the health checks
// (nearly every generation rejected until the driver fails closed),
// so 0 it stays.
const roscChain = 0

// attempts bounds the retries across recoverable health failures. Each
// costs a generation, so the expected worst case is well under a
// second per raw generation. The hard ceiling per raw generation is
// the reset deadline plus attempts x (run deadline + reset deadline),
// about 8.5 seconds at runTimeout, and a served block draws two raw
// generations, so a block that never asserts any terminal status bit
// can hold the caller for about 17 seconds before it gets
// ErrUnhealthy rather than weaker entropy. Reaching that needs
// hardware that answers nothing.
const attempts = 8

// runTimeout bounds one generation and one reset release. It is twelve
// times the expected forty milliseconds at sampleCount. The datasheet
// warns the tail can run "in excess of 100 times the average", which
// at this sampling rate would be four seconds; a healthy run that slow
// is cut short here and costs one attempt, failing closed rather than
// hanging the screen, which is the trade this constant makes.
const runTimeout = 500 * time.Millisecond

func init() {
	fill = fillEHR
	health = autocorrStats
}

// reset returns the block to a known configuration and reports
// whether it came back. The bootrom leaves the block out of reset
// with every health check bypassed in TRNG_DEBUG_CONTROL and does not
// clear that on its way out, so inheriting the register state would
// silently disable the checks this package relies on. The RESET_DONE
// spin carries the same deadline discipline as waitRun: a block that
// never leaves reset reports false instead of hanging the screen,
// and every caller fails closed rather than configure registers on a
// block still in reset.
func reset() bool {
	rp.RESETS.SetRESET_TRNG(1)
	rp.RESETS.SetRESET_TRNG(0)
	deadline := time.Now().Add(runTimeout)
	for rp.RESETS.GetRESET_DONE_TRNG() == 0 {
		if time.Now().After(deadline) {
			return false
		}
	}
	rp.TRNG.RND_SOURCE_ENABLE.Set(0)
	rp.TRNG.TRNG_DEBUG_CONTROL.Set(0)
	rp.TRNG.RNG_IMR.Set(0xffffffff)
	rp.TRNG.RNG_ICR.Set(0xffffffff)
	rp.TRNG.SAMPLE_CNT1.Set(sampleCount)
	rp.TRNG.TRNG_CONFIG.Set(roscChain)
	return true
}

// fillEHR runs generations until one passes the block's entropy checks,
// and copies its 192 bits into ehr.
func fillEHR(ehr *[ehrBytes]byte) error {
	if !reset() {
		return ErrUnhealthy
	}
	// The block answers only after its reset release completes. A
	// reset() that reported false has left it in reset, and what a
	// register write does to a block in that state is not something to
	// rest on, so the disable on the way out is skipped after such a
	// failure: there is nothing running to disable.
	up := true
	defer func() {
		if up {
			rp.TRNG.RND_SOURCE_ENABLE.Set(0)
		}
	}()
	for range attempts {
		rp.TRNG.RNG_ICR.Set(0xffffffff)
		rp.TRNG.RND_SOURCE_ENABLE.Set(1)
		isr := waitRun()
		switch {
		case isr&isrAutocorrErr != 0:
			// Unrecoverable in software: only a reset restarts the
			// block, so start it over. The chain length is not varied
			// between attempts; if a die is found that latches at
			// setting 0, alternating TRNG_CONFIG here is the lever the
			// datasheet points at.
			rp.TRNG.RND_SOURCE_ENABLE.Set(0)
			if !reset() {
				up = false
				return ErrUnhealthy
			}
			continue
		case isr&isrRecoverable != 0:
			// A failed check presents an all-zero EHR, so there is
			// nothing to salvage. Clear and run again.
			rp.TRNG.RNG_ICR.Set(isrRecoverable)
			continue
		case isr&isrEHRValid == 0:
			// Timed out, or a state the datasheet does not describe.
			// Start the block over rather than read a stale bank.
			if !reset() {
				up = false
				return ErrUnhealthy
			}
			continue
		}
		// Read every word: the last one clears the bank and re-arms
		// sampling. A partial read leaves the block wedged.
		var words [6]uint32
		words[0] = rp.TRNG.EHR_DATA0.Get()
		words[1] = rp.TRNG.EHR_DATA1.Get()
		words[2] = rp.TRNG.EHR_DATA2.Get()
		words[3] = rp.TRNG.EHR_DATA3.Get()
		words[4] = rp.TRNG.EHR_DATA4.Get()
		words[5] = rp.TRNG.EHR_DATA5.Get()
		var zero uint32
		for i, w := range words {
			binary.LittleEndian.PutUint32(ehr[i*4:], w)
			zero |= w
		}
		if zero == 0 {
			// An all-zero register bank is how the block reports a
			// discarded generation, never 192 bits of entropy.
			continue
		}
		return nil
	}
	return ErrUnhealthy
}

// waitRun spins until the block reports a finished run, successful or
// not, and returns the interrupt status it saw. It returns 0 if the
// block never answered.
//
// Polling TRNG_BUSY here would race. The block does not assert busy the
// instant the source is enabled, so a read landing in that window sees
// an idle block, reports a run that never happened, and burns a retry
// without ever having waited.
func waitRun() uint32 {
	deadline := time.Now().Add(runTimeout)
	for {
		if isr := rp.TRNG.RNG_ISR.Get(); isr&isrTerminal != 0 {
			return isr
		}
		if time.Now().After(deadline) {
			return 0
		}
	}
}

func autocorrStats() (fails, trys uint32) {
	v := rp.TRNG.AUTOCORR_STATISTIC.Get()
	return (v >> 14) & 0xff, v & 0x3fff
}
