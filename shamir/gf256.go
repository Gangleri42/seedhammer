package shamir

// This file implements GF(256), the field with 256 elements, using the
// Rijndael polynomial x^8+x^4+x^3+x+1 (binary 1_0001_1011, 0x11b). It
// is the field of AES and of SLIP-39, which makes review and
// cross-implementation testing easier than an exotic choice.
//
// Multiplication uses log/exp tables: for nonzero a and b,
// a·b = exp[(log[a]+log[b]) mod 255]. Any primitive element generates
// an isomorphism between the multiplicative group and Z/255, so the
// choice of generator does not affect the products computed; init
// simply finds one and verifies it is primitive.

const poly = 0x11b

// exp is doubled so that exp[log[a]+log[b]] never needs a modular
// reduction: the sum of two logs is at most 2*254 < 511.
var exp [512]byte
var log [256]byte

func init() {
	// Find a primitive element by trial: g is a generator if its
	// powers cycle through all 255 nonzero elements.
	for g := 2; g < 256; g++ {
		var seen [256]bool
		x := 1
		primitive := true
		for i := 0; i < 255; i++ {
			if seen[x] {
				primitive = false
				break
			}
			seen[x] = true
			exp[i] = byte(x)
			log[x] = byte(i)
			x = mulSlow(byte(x), byte(g))
		}
		if primitive && x == 1 {
			copy(exp[255:], exp[:255])
			return
		}
	}
	panic("shamir: no primitive element in GF(256)")
}

// mulSlow multiplies in GF(256) with the Russian peasant algorithm. It
// serves the table setup and the tests; the hot paths use the tables.
func mulSlow(a, b byte) int {
	r := 0
	aa, bb := int(a), int(b)
	for bb > 0 {
		if bb&1 != 0 {
			r ^= aa
		}
		aa <<= 1
		if aa&0x100 != 0 {
			aa ^= poly
		}
		bb >>= 1
	}
	return r
}

func mul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return exp[uint(log[a])+uint(log[b])]
}

// div computes a/b. Dividing by zero is a programming error.
func div(a, b byte) byte {
	if b == 0 {
		panic("shamir: division by zero")
	}
	if a == 0 {
		return 0
	}
	// exp is doubled, so the +255 shift avoids a modular reduction.
	return exp[uint(log[a])+255-uint(log[b])]
}
