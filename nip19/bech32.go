package nip19

import "strings"

// alphabet is the bech32 (BIP-173) symbol table; the index of a character
// is its 5-bit value.
const alphabet = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

// generator is the bech32 checksum-polynomial coefficient table.
var generator = [...]uint32{
	0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3,
}

// decode returns the HRP and the 8-bit payload of a bech32 string after
// verifying the 6-symbol checksum. Mixed-case input is rejected per
// BIP-173; upper-case input is folded to lower-case before lookup.
func decode(s string) (string, []byte, error) {
	if len(s) < 8 || len(s) > 90 {
		return "", nil, errInvalidLength
	}
	hasLower, hasUpper := false, false
	for i := range len(s) {
		switch c := s[i]; {
		case 'a' <= c && c <= 'z':
			hasLower = true
		case 'A' <= c && c <= 'Z':
			hasUpper = true
		}
	}
	if hasLower && hasUpper {
		return "", nil, errInvalidCase
	}
	if hasUpper {
		s = strings.ToLower(s)
	}
	sep := strings.LastIndexByte(s, '1')
	// HRP must be at least one char; body must hold at least the
	// 6-symbol checksum.
	if sep < 1 || sep+7 > len(s) {
		return "", nil, errInvalidLength
	}
	hrp := s[:sep]
	body := s[sep+1:]
	for i := range len(hrp) {
		// HRP must be printable ASCII in [33, 126].
		if c := hrp[i]; c < 33 || c > 126 {
			return "", nil, errInvalidCharacter
		}
	}
	values := make([]byte, len(body))
	for i := range len(body) {
		idx := strings.IndexByte(alphabet, body[i])
		if idx < 0 {
			return "", nil, errInvalidCharacter
		}
		values[i] = byte(idx)
	}
	if !verifyChecksum(hrp, values) {
		return "", nil, errInvalidChecksum
	}
	out, err := convertBits(values[:len(values)-6], 5, 8, false)
	if err != nil {
		return "", nil, err
	}
	return hrp, out, nil
}

// encode produces the bech32 string for a HRP and an 8-bit payload.
// The caller is responsible for choosing an appropriate HRP.
func encode(hrp string, data []byte) string {
	d, _ := convertBits(data, 8, 5, true)
	d = append(d, checksum(hrp, d)...)
	var b strings.Builder
	b.Grow(len(hrp) + 1 + len(d))
	b.WriteString(hrp)
	b.WriteByte('1')
	for _, v := range d {
		b.WriteByte(alphabet[v])
	}
	return b.String()
}

// polymod is the BIP-173 polynomial residue function over a 5-bit
// symbol sequence.
func polymod(values []byte) uint32 {
	chk := uint32(1)
	for _, v := range values {
		top := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ uint32(v)
		for i := range 5 {
			if (top>>i)&1 != 0 {
				chk ^= generator[i]
			}
		}
	}
	return chk
}

// hrpExpand splits the HRP into the high-bits / separator / low-bits
// form folded into the checksum.
func hrpExpand(hrp string) []byte {
	out := make([]byte, len(hrp)*2+1)
	for i := range len(hrp) {
		out[i] = hrp[i] >> 5
	}
	out[len(hrp)] = 0
	for i := range len(hrp) {
		out[len(hrp)+1+i] = hrp[i] & 31
	}
	return out
}

func verifyChecksum(hrp string, body []byte) bool {
	return polymod(append(hrpExpand(hrp), body...)) == 1
}

// checksum returns the 6-symbol bech32 checksum for an HRP and a
// 5-bit body.
func checksum(hrp string, body []byte) []byte {
	enc := append(hrpExpand(hrp), body...)
	enc = append(enc, 0, 0, 0, 0, 0, 0)
	mod := polymod(enc) ^ 1
	out := make([]byte, 6)
	for i := range 6 {
		out[i] = byte(mod>>uint(5*(5-i))) & 31
	}
	return out
}

// convertBits regroups a symbol stream from one bit-width to another.
// With pad=false the input must be a whole multiple of the output
// width; trailing bits that are not all zero are rejected.
func convertBits(data []byte, from, to uint, pad bool) ([]byte, error) {
	var acc uint32
	var bits uint
	out := make([]byte, 0, (len(data)*int(from)+int(to)-1)/int(to))
	maxv := uint32(1)<<to - 1
	for _, v := range data {
		if uint32(v)>>from != 0 {
			return nil, errInvalidCharacter
		}
		acc = acc<<from | uint32(v)
		bits += from
		for bits >= to {
			bits -= to
			out = append(out, byte(acc>>bits&maxv))
		}
	}
	if pad {
		if bits > 0 {
			out = append(out, byte(acc<<(to-bits)&maxv))
		}
		return out, nil
	}
	// Reject trailing data that doesn't fit cleanly into the output width.
	if bits >= from || (acc<<(to-bits))&maxv != 0 {
		return nil, errInvalidCharacter
	}
	return out, nil
}
