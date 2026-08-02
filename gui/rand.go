package gui

import "io"

// Rand is the entropy source for the only place the firmware needs
// cryptographic randomness: choosing the final word of a seed phrase.
// driver/st25r3916 also draws from machine.GetRNG, for an NFC tag UID
// where predictability costs nothing. cmd/controller replaces this with
// the rp2350 hardware generator at startup.
//
// It is a package variable rather than a method on Platform because the
// browser emulator implements Platform in a separate repository, and
// growing that interface would break its build. rand_default.go supplies
// the off-device default and explains why the device build deliberately
// has none; on device this is nil until cmd/controller sets it, and a
// nil source is reported to the user rather than silently replaced.
var Rand io.Reader
