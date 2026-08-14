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
// The datasheet's own words about its recommended 20-25 are that the
// setting "increases the chance of NIST test-failing results", and that
// larger counts such as 100 "significantly reduce, but do not eliminate"
// entropy check failures. Ten milliseconds per generation instead of two
// is nothing on a screen the user just pressed a button on, and every
// failed check costs a whole retry.
const sampleCount = 100

// roscChain selects the ring oscillator's inverter chain length. The
// datasheet gives no lengths for the four settings and recommends 0 or 1.
const roscChain = 0

// attempts bounds the retries across recoverable health failures. Each
// costs a generation, so the expected worst case is under a second; the
// hard ceiling is attempts x runTimeout, four seconds, and reaching it
// needs hardware that never asserts any terminal status bit. Past it the
// caller gets ErrUnhealthy rather than weaker entropy.
const attempts = 8

// runTimeout bounds one generation. The datasheet warns the tail can run
// "in excess of 100 times the average", so this is deliberately far
// above the expected ten milliseconds; it exists to keep a wedged block
// from hanging the screen, not to cut a slow run short.
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
	defer rp.TRNG.RND_SOURCE_ENABLE.Set(0)
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
