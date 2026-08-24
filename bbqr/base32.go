package bbqr

import (
	"errors"
	"fmt"
	"strings"
)

// b32alphabet is the RFC 4648 Base32 alphabet. The padding character
// is never used: the standard requires every part to decode to a whole
// number of bytes.
const b32alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"

// b32encode encodes src to unpadded Base32.
func b32encode(src []byte) string {
	var sb strings.Builder
	sb.Grow((len(src)*8 + 4) / 5)
	var acc uint32
	var nbits uint
	for _, b := range src {
		acc = acc<<8 | uint32(b)
		nbits += 8
		for nbits >= 5 {
			nbits -= 5
			sb.WriteByte(b32alphabet[(acc>>nbits)&31])
		}
	}
	if nbits > 0 {
		sb.WriteByte(b32alphabet[(acc<<(5-nbits))&31])
	}
	return sb.String()
}

// b32decode decodes unpadded Base32. Lengths modulo 8 that cannot
// arise from encoding a whole number of bytes are rejected, as the
// standard requires.
func b32decode(s string) ([]byte, error) {
	switch len(s) % 8 {
	case 1, 3, 6:
		return nil, errors.New("bbqr: base32 part cannot decode to whole bytes")
	}
	out := make([]byte, 0, len(s)*5/8)
	var acc uint32
	var nbits uint
	for i := 0; i < len(s); i++ {
		v, ok := b32val(s[i])
		if !ok {
			return nil, fmt.Errorf("bbqr: invalid base32 character %q", s[i])
		}
		acc = acc<<5 | uint32(v)
		nbits += 5
		if nbits >= 8 {
			nbits -= 8
			out = append(out, byte(acc>>nbits))
		}
	}
	return out, nil
}

func b32val(c byte) (byte, bool) {
	switch {
	case c >= 'A' && c <= 'Z':
		return c - 'A', true
	case c >= '2' && c <= '7':
		return c - '2' + 26, true
	}
	return 0, false
}
