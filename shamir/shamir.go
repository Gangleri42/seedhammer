// Package shamir implements Shamir's secret sharing over GF(256) with
// a BBQr transport for the shares: any data is compressed, sealed with
// its BBQr file type and an integrity digest, split into a threshold
// number of shares, and each share is carried by its own type M BBQr
// series. Two generator profiles produce the same wire format and the
// same receiver handles both. Under the randomized profile
// (SplitData), the polynomial coefficients come from a cryptographic
// random source and fewer shares than the threshold reveal nothing
// about the data beyond its sealed length, in the
// information-theoretic sense. Under the derived profile
// (SplitDataDerived), the whole set is a function of the threshold and
// the sealed content, so the same data splits to the same shares on
// every generator that seals the same bytes; privacy is then
// computational, and the
// profile is for data whose entropy defeats guessing, such as a wallet
// descriptor. See SPEC.md for the format and the security argument.
//
// [BBQr]: https://github.com/coinkite/BBQr
package shamir

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
)

// Split splits secret into n shares, any k of which recover it, under
// the randomized profile. The shares are the raw (x, y) form: one
// index byte followed by one value byte per secret byte.
//
// For each secret byte s, Split samples a random polynomial
// p(X) = s + c1·X + ... + c[k-1]·X^(k-1) over GF(256) and evaluates it
// at x = 1..n, so every share byte is uniformly distributed and
// independent of s unless k shares are combined. rand must therefore
// be a cryptographic random source; on the device it is the TRNG.
// Coefficient bytes are hedged (see SPEC.md): XORed with an
// HMAC-SHA256 stream keyed by secret, so a failed rand degrades
// privacy to computational instead of revealing the secret from any
// single share.
//
// The point x=0 is never issued: it would be the secret itself.
func Split(secret []byte, k, n int, rand io.Reader) ([][]byte, error) {
	if len(secret) == 0 {
		return nil, errors.New("shamir: nothing to split")
	}
	if k < 1 || n > 255 || k > n {
		return nil, fmt.Errorf("shamir: invalid threshold %d of %d", k, n)
	}
	return split(secret, k, n, newHedge(secret, rand))
}

// split is the core of both profiles. For each secret byte it reads
// the k-1 higher coefficients from coeffs, in ascending degree, and
// evaluates the polynomial at x = 1..n. The callers validate the
// arguments.
func split(secret []byte, k, n int, coeffs io.Reader) ([][]byte, error) {
	shares := make([][]byte, n)
	for i := range shares {
		shares[i] = make([]byte, 1+len(secret))
		shares[i][0] = byte(i + 1)
	}
	poly := make([]byte, k)
	for j, s := range secret {
		poly[0] = s
		if _, err := io.ReadFull(coeffs, poly[1:]); err != nil {
			return nil, fmt.Errorf("shamir: reading coefficients: %w", err)
		}
		for _, share := range shares {
			// Horner evaluation at the share's x.
			x := share[0]
			y := poly[k-1]
			for m := k - 2; m >= 0; m-- {
				y = mul(y, x) ^ poly[m]
			}
			share[1+j] = y
		}
	}
	return shares, nil
}

// Combine recovers a secret from shares produced by Split. Combining
// fewer shares than the threshold does not fail: it silently yields a
// wrong result, which is why transports (such as the share envelopes
// of this package) must add integrity. All shares must have the same
// length and distinct, nonzero indices.
func Combine(shares [][]byte) ([]byte, error) {
	if len(shares) == 0 {
		return nil, errors.New("shamir: no shares")
	}
	n := len(shares[0])
	if n < 2 {
		return nil, errors.New("shamir: empty share")
	}
	seen := make(map[byte]bool, len(shares))
	for _, s := range shares {
		if len(s) != n {
			return nil, errors.New("shamir: shares of different lengths")
		}
		if s[0] == 0 {
			return nil, errors.New("shamir: share index 0 is invalid")
		}
		if seen[s[0]] {
			return nil, fmt.Errorf("shamir: duplicate share index %d", s[0])
		}
		seen[s[0]] = true
	}
	return interpolate(shares, 0), nil
}

// interpolate evaluates, byte by byte, the polynomial through the
// shares' (x, y) points at x. The shares must satisfy Combine's
// checks; Combine is the case x=0. The polynomial passes through
// every point it was built from, so a held share lies off it exactly
// when its bytes were changed after the split.
func interpolate(shares [][]byte, x byte) []byte {
	// Lagrange basis at x. Subtraction is addition in characteristic
	// 2, so the coefficient of share i is
	//
	//	λ_i(x) = Π_{m≠i} (x ⊕ x_m) / (x_i ⊕ x_m)
	lambda := make([]byte, len(shares))
	for i, si := range shares {
		l := byte(1)
		for m, sm := range shares {
			if m == i {
				continue
			}
			l = div(mul(l, x^sm[0]), si[0]^sm[0])
		}
		lambda[i] = l
	}
	out := make([]byte, len(shares[0])-1)
	for j := range out {
		y := byte(0)
		for i, s := range shares {
			y ^= mul(lambda[i], s[1+j])
		}
		out[j] = y
	}
	return out
}

// prf is an HMAC-SHA256 counter-mode stream (SP 800-108 counter mode;
// the exact derivations are in SPEC.md): prk = HMAC-SHA256(label,
// input) and block i = HMAC-SHA256(prk, u64be(i)). Both generator
// profiles read their coefficients through one. The hedge of the
// randomized profile (label v0, keyed by the split secret) XORs the
// stream into the bytes of the random source: with a working source
// the XOR stays uniform and independent of the secret, and with a
// failed one the coefficients degrade to the PRF stream, so a share
// verifies a guessed secret instead of revealing the secret outright.
// The derived profile (label v1, keyed by the threshold and the sealed
// content) reads the stream itself, coefficients first and then the
// tag.
type prf struct {
	rand  io.Reader // XORed into the stream; nil reads the stream alone
	mac   hash.Hash
	block [sha256.Size]byte
	n     uint64
	off   int
}

func newPRF(label string, input []byte, rand io.Reader) *prf {
	extract := hmac.New(sha256.New, []byte(label))
	extract.Write(input)
	return &prf{
		rand: rand,
		mac:  hmac.New(sha256.New, extract.Sum(nil)),
		off:  sha256.Size,
	}
}

// newHedge is the randomized profile's coefficient stream: rand,
// hedged by the PRF keyed by secret.
func newHedge(secret []byte, rand io.Reader) *prf {
	return newPRF("seedhammer.com/shamir hedge v0", secret, rand)
}

// newDerived is the derived profile's stream, coefficients then tag:
// the PRF keyed by the threshold byte followed by the sealed content.
func newDerived(k int, sealed []byte) *prf {
	input := make([]byte, 0, 1+len(sealed))
	input = append(input, byte(k))
	input = append(input, sealed...)
	return newPRF("seedhammer.com/shamir derived v1", input, nil)
}

func (s *prf) Read(p []byte) (int, error) {
	n := len(p)
	var err error
	if s.rand != nil {
		n, err = s.rand.Read(p)
	} else {
		clear(p) // XORing into zeros yields the stream itself
	}
	for i := range p[:n] {
		if s.off == len(s.block) {
			var ctr [8]byte
			binary.BigEndian.PutUint64(ctr[:], s.n)
			s.mac.Reset()
			s.mac.Write(ctr[:])
			s.mac.Sum(s.block[:0])
			s.n++
			s.off = 0
		}
		p[i] ^= s.block[s.off]
		s.off++
	}
	return n, err
}
