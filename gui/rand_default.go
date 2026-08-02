//go:build !rp2350

package gui

import "crypto/rand"

// Host tests and the browser emulator have a real crypto/rand backend,
// so they default to it. This file is the reason package gui never
// names crypto/rand on rp2350: TinyGo decides per chip what backs
// crypto/rand.Reader, and on this family that backend is
// machine.GetRNG, a ring oscillator LFSR it documents as unfit for
// cryptography. rp2350 is absent from that build constraint today, so
// Reader is nil there; if a future TinyGo adds it, importing
// crypto/rand from the device build would install the LFSR as the seed
// source and the nil check in drawLastWordFlow would stop meaning
// anything. cmd/controller installs the hardware generator instead.
func init() { Rand = rand.Reader }
