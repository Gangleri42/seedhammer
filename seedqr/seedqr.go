// Package seedqr encodes and decodes [SeedQR] and CompactSeedQR formats.
//
// [SeedQR]: https://github.com/SeedSigner/seedsigner/blob/dev/docs/seed_qr/README.md
package seedqr

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"seedhammer.com/bip39"
)

// Parse decodes a payload in either format. CompactSeedQR is raw
// entropy with no checksum of its own, so any 16 or 32 bytes decode as
// a mnemonic: Parse suits a payload known to be a QR's content, such as
// a file. A sniffer classifying untyped bytes uses [ParseDigits].
func Parse(qr []byte) (bip39.Mnemonic, bool) {
	if seed, ok := ParseDigits(qr); ok {
		return seed, true
	}
	return parseCompactSeedQR(qr)
}

// QR encodes a bip39 menmonic into the SeedQR format.
// It panics if m is invalid.
func QR(m bip39.Mnemonic) []byte {
	if !m.Valid() {
		panic("invalid mnemonic")
	}
	var qr bytes.Buffer
	for _, w := range m {
		fmt.Fprintf(&qr, "%04d", w)
	}
	return qr.Bytes()
}

// CompactQR encodes a bip39 mnemonic into the CompactSeedQR format.
// It panics if m is invalid.
func CompactQR(m bip39.Mnemonic) []byte {
	if !m.Valid() {
		panic("invalid mnemonic")
	}
	return m.Entropy()
}

// Shaped reports whether qr has the shape of the SeedQR digit form:
// four decimal digits per word, 12 to 24 words, nothing else. It is the
// gate before [ParseDigits] allocates, and lets a caller tell a SeedQR
// with a bad checksum from text that never was one.
func Shaped(qr []byte) bool {
	if n := len(qr); n%4 != 0 || n < 4*12 || n > 4*24 {
		return false
	}
	for _, c := range qr {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// ParseDigits decodes the SeedQR digit form alone: every word index as
// four decimal digits, no separators, the checksum word included. The
// bytes are taken as they are; a caller expecting surrounding
// whitespace trims first.
func ParseDigits(qr []byte) (bip39.Mnemonic, bool) {
	if !Shaped(qr) {
		return nil, false
	}
	m := make(bip39.Mnemonic, len(qr)/4)
	for i := range m {
		var word bip39.Word
		for _, c := range qr[i*4 : (i+1)*4] {
			word = word*10 + bip39.Word(c-'0')
		}
		m[i] = word
	}
	if !m.Valid() {
		return nil, false
	}
	return m, true
}

func parseCompactSeedQR(qr []byte) (bip39.Mnemonic, bool) {
	switch len(qr) {
	case 128 / 8, 256 / 8:
	default:
		return nil, false
	}
	bits := len(qr) * 8
	checksum := bits / 32
	n := (bits + checksum) / 11
	var buf strings.Builder
	for _, b := range qr {
		buf.WriteString(fmt.Sprintf("%.8b", b))
	}
	for range checksum {
		buf.WriteRune('0')
	}
	bitstream := buf.String()
	m := make(bip39.Mnemonic, n)
	for i := range m {
		w, err := strconv.ParseUint(bitstream[i*11:(i+1)*11], 2, 16)
		if err != nil {
			return nil, false
		}
		m[i] = bip39.Word(w)
	}
	return m.FixChecksum(), true
}
