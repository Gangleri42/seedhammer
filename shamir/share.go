package shamir

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"

	"seedhammer.com/bbqr"
)

// A share envelope is the payload of one share's BBQr series (file
// type M). The wire format is:
//
//	tag                2 bytes, common to the shares of one split:
//	                   random under the randomized profile, a
//	                   fingerprint of (k, sealed) under the derived one
//	index              1 byte, x coordinate of this share, 1..255
//	threshold          1 byte, k, at least 2
//	share data         y values of the sealed content, one byte each
//
// The sealed content, reconstructed only by combining a threshold of
// shares, is
//
//	type               1 byte; bits 0-6: the BBQr file type of the
//	                   data, bit 7: the payload is DEFLATE-compressed
//	payload            the data, DEFLATE-compressed when that shrinks it
//	digest             4 bytes: SHA-256(threshold ‖ type ‖ payload)[0:4]
//
// Everything about the data, its type and compression flag included,
// is inside the shared secret: fewer than a threshold of shares reveal
// nothing but the threshold, the tag and the sealed length. The digest
// binds the public threshold byte and detects corruption after
// reconstruction; it is not a MAC (see SPEC.md).
const (
	prefixLen = 4 // tag(2) + index(1) + threshold(1)
	// digestLen is the length of the integrity digest inside the
	// sealed content.
	digestLen = 4
	// sealedOverhead is the sealed bytes beside the payload: the type
	// byte and the digest.
	sealedOverhead = 1 + digestLen
	// flagDeflated marks sealed payloads that are DEFLATE-compressed,
	// in bit 7 of the type byte.
	flagDeflated = 0x80
)

// DefaultLimit is the default maximum recovered data size accepted by
// a Set.
const DefaultLimit = 1 << 20

// ErrForeignShare reports a share belonging to a different split than
// the set being collected. Receivers use it to start a fresh set
// instead of failing the scan.
var ErrForeignShare = errors.New("shamir: share from a different split")

// ErrCorrupt reports that a corrupt share blocks recovery: no
// combination of the held shares passed the integrity digest, or the
// verifying combinations disagree on which shares are clean
// (ErrAmbiguous). Receivers keep the set and ask for another share,
// which lets Recover get past the corrupt one and name it.
var ErrCorrupt = errors.New("shamir: corrupt share")

// ErrAmbiguous reports that the held shares verify under two different
// polynomials with equally many agreeing shares, so the corrupt shares
// cannot be told from the clean ones. It wraps ErrCorrupt because the
// remedy is the same: one more clean share breaks the tie.
var ErrAmbiguous = fmt.Errorf("%w: cannot tell which", ErrCorrupt)

// attributionCap bounds the threshold-sized combinations Recover
// enumerates to attribute corruption: the enumeration runs while
// C(held, k) is at most this many.
const attributionCap = 1024

// Share is a parsed share envelope.
type Share struct {
	Threshold int    // k: shares needed to recover
	Index     int    // x coordinate of this share, 1..255
	Tag       uint16 // identifier common to the shares of one split

	data []byte // share y values, len == sealedOverhead + payload length
}

// ParseShare parses and validates one share envelope, the payload of a
// complete type M BBQr series. The envelope does not self-identify:
// transports dispatch on the series file type.
func ParseShare(payload []byte) (Share, error) {
	var sh Share
	if len(payload) < prefixLen+sealedOverhead+1 {
		return sh, errors.New("shamir: share too short")
	}
	x, k := int(payload[2]), int(payload[3])
	if k < 2 {
		return sh, fmt.Errorf("shamir: invalid threshold %d", k)
	}
	if x < 1 {
		return sh, errors.New("shamir: invalid share index 0")
	}
	sh.Tag = binary.BigEndian.Uint16(payload[0:2])
	sh.Index, sh.Threshold = x, k
	// Copy: callers may reuse the payload buffer (scanners do).
	sh.data = bytes.Clone(payload[prefixLen:])
	return sh, nil
}

// marshal returns the wire form of the share.
func (sh Share) marshal() []byte {
	out := make([]byte, prefixLen+len(sh.data))
	binary.BigEndian.PutUint16(out[0:2], sh.Tag)
	out[2], out[3] = byte(sh.Index), byte(sh.Threshold)
	copy(out[prefixLen:], sh.data)
	return out
}

// Recovered is the result of recombining a share set.
type Recovered struct {
	// FileType is the BBQr file type of Data, sealed with it at the
	// split.
	FileType byte
	Data     []byte
	// Corrupt lists the index (x) of every held share whose bytes lie
	// off the recovered polynomial, ascending; nil when every held
	// share agrees. With spare shares beyond the threshold, corrupt
	// shares are survived wherever they sit in the set and named by
	// maximal agreement (see Recover): exact for one corrupt share,
	// and for several when the clean shares outnumber every wrong
	// reading's support.
	Corrupt []int
}

// SplitData splits data of the given BBQr file type into n shares, any
// k of which recover it, under the randomized generator profile of
// SPEC.md, and encodes each share as its own type M BBQr series in
// base 32 encoding. The data is DEFLATE-compressed when compression
// shrinks it, sealed with its type and an integrity digest, and only
// then split, so shares carry no compressible structure and reveal
// neither type nor flag below the threshold. rand must be a
// cryptographic random source; on the device it is the TRNG. Privacy
// below the threshold is information-theoretic, so the profile suits
// data of any entropy; SplitDataDerived is the profile for shares
// that must be reproducible.
//
// rand supplies, in order: the k-1 random polynomial coefficients per
// sealed byte (see Split), then 2 bytes of share tag. Fixing a rand
// stream therefore reproduces a split exactly, which is how the
// randomized test vectors in SPEC.md are defined.
//
// A 1-of-n split is rejected: its shares would be plain copies of the
// data, which vanilla BBQr series of the data's own type express
// honestly.
//
// Before returning, the shares are verified by recovering the data
// through the receiving path from threshold-sized subsets covering
// every share. Steel is write-once; an encoder defect surfaces here,
// not on a recovery attempt years later.
func SplitData(fileType byte, data []byte, k, n int, rand io.Reader) ([]bbqr.Series, error) {
	return splitData(fileType, data, k, n, func(sealed []byte) (coeffs, tag io.Reader) {
		return newHedge(sealed, rand), rand
	})
}

// SplitDataDerived splits data under the derived generator profile of
// SPEC.md: the same validation, compression, sealing, envelope, BBQr
// encoding and self-verification as SplitData, with the polynomial
// coefficients and the tag drawn from an HMAC-SHA256 stream keyed by
// the threshold and the sealed content. No random source takes part.
// The whole set is a function of (k, data): splitting the same data at
// the same threshold yields identical
// shares, and a set issued at a larger n extends a smaller one share
// for share, so a lost share is cut again from the data. The tag is a
// 16-bit fingerprint of (k, sealed): a re-split whose tag differs from
// the shares in hand has different sealed content.
//
// Privacy below the threshold is computational: every share is a
// deterministic function of the sealed content, so a holder of one share can
// verify a guessed data value against it. The profile is for data
// whose min-entropy defeats guessing, such as a wallet descriptor
// whose extended public keys each carry a 256-bit chain code. A
// mnemonic, a short text or a PIN does not qualify and must use
// SplitData.
func SplitDataDerived(fileType byte, data []byte, k, n int) ([]bbqr.Series, error) {
	return splitData(fileType, data, k, n, func(sealed []byte) (coeffs, tag io.Reader) {
		stream := newDerived(k, sealed)
		return stream, stream
	})
}

// splitData is the core shared by both profiles: validation,
// compression, sealing, the split, the envelopes and the
// self-verification. source builds the profile's streams from the
// sealed content: coeffs yields the k-1 coefficients of each sealed
// byte in order, then tag yields the 2 tag bytes.
func splitData(fileType byte, data []byte, k, n int, source func(sealed []byte) (coeffs, tag io.Reader)) ([]bbqr.Series, error) {
	if fileType < 'A' || fileType > 'Z' {
		return nil, fmt.Errorf("shamir: invalid file type %q", fileType)
	}
	if len(data) == 0 {
		return nil, errors.New("shamir: nothing to split")
	}
	if k < 2 || n > 255 || k > n {
		return nil, fmt.Errorf("shamir: invalid threshold %d of %d", k, n)
	}
	payload, typ := data, fileType
	if cmp := deflate(data); len(cmp) < len(data) {
		payload, typ = cmp, fileType|flagDeflated
	}
	inner := make([]byte, 0, sealedOverhead+len(payload))
	inner = append(inner, typ)
	inner = append(inner, payload...)
	inner = append(inner, digest(k, typ, payload)...)
	coeffs, tagSource := source(inner)
	raw, err := split(inner, k, n, coeffs)
	if err != nil {
		return nil, err
	}
	var tag [2]byte
	if _, err := io.ReadFull(tagSource, tag[:]); err != nil {
		return nil, fmt.Errorf("shamir: reading share tag: %w", err)
	}
	series := make([]bbqr.Series, n)
	for i := range series {
		sh := Share{
			Threshold: k,
			Index:     i + 1,
			Tag:       binary.BigEndian.Uint16(tag[:]),
			data:      raw[i][1:], // the x coordinate lives in the envelope
		}
		s, err := bbqr.Split(sh.marshal(), bbqr.TypeShamir, bbqr.SplitOptions{
			// Shares are incompressible; Zlib could only fall back.
			Encoding: bbqr.EncBase32,
		})
		if err != nil {
			return nil, err
		}
		series[i] = s
	}
	if err := verifySplit(fileType, data, k, series); err != nil {
		return nil, err
	}
	return series, nil
}

// digest is the sealed integrity digest: the first 4 bytes of the
// SHA-256 over the threshold, the type byte and the payload. Binding
// the public threshold makes a tampered threshold byte fail like any
// other corruption.
func digest(k int, typ byte, payload []byte) []byte {
	h := sha256.New()
	h.Write([]byte{byte(k), typ})
	h.Write(payload)
	return h.Sum(nil)[:digestLen]
}

// verifySplit recovers the split through the full receiving path,
// BBQr decode included, from threshold-sized share subsets chosen so
// every share participates in at least one recovery.
func verifySplit(fileType byte, data []byte, k int, series []bbqr.Series) error {
	n := len(series)
	for start := 0; start < n; start += k {
		s := Set{Limit: len(data)}
		for i := range k {
			_, payload, err := bbqr.Join(series[(start+i)%n].Parts)
			if err != nil {
				return fmt.Errorf("shamir: split verification: %w", err)
			}
			if err := s.Add(payload); err != nil {
				return fmt.Errorf("shamir: split verification: %w", err)
			}
		}
		rec, err := s.Recover()
		if err != nil {
			return fmt.Errorf("shamir: split verification: %w", err)
		}
		if rec.FileType != fileType || !bytes.Equal(rec.Data, data) {
			return errors.New("shamir: split verification: recovered data differs")
		}
	}
	return nil
}

// Set collects the shares of one split, in any order, for
// recovery. The zero value is ready to use.
type Set struct {
	// Limit caps the recovered data size in bytes; zero means
	// DefaultLimit. The cap guards memory constrained receivers: a
	// compressed payload can inflate to many times its share size.
	Limit int

	k      int
	tag    uint16
	length int
	shares []Share
}

// Add consumes one share envelope, the payload of a complete type M
// BBQr series. Adding an identical share twice is a no-op; shares from
// other splits or with different parameters are rejected.
func (s *Set) Add(payload []byte) error {
	sh, err := ParseShare(payload)
	if err != nil {
		return err
	}
	if len(s.shares) == 0 {
		s.k, s.tag, s.length = sh.Threshold, sh.Tag, len(sh.data)
	} else {
		if sh.Tag != s.tag {
			return ErrForeignShare
		}
		if sh.Threshold != s.k {
			return fmt.Errorf("shamir: share threshold %d, want %d", sh.Threshold, s.k)
		}
		if len(sh.data) != s.length {
			return errors.New("shamir: inconsistent share")
		}
	}
	for _, prev := range s.shares {
		if prev.Index == sh.Index {
			if !bytes.Equal(prev.data, sh.data) {
				return fmt.Errorf("shamir: conflicting copies of share %d", sh.Index)
			}
			return nil
		}
	}
	s.shares = append(s.shares, sh)
	return nil
}

// Progress reports how many distinct shares the set holds and how many
// are needed to recover. Need is zero before the first share.
func (s *Set) Progress() (have, need int) {
	return len(s.shares), s.k
}

// Complete reports whether the set holds enough shares to recover.
func (s *Set) Complete() bool {
	return len(s.shares) >= s.k && s.k > 0
}

// Recover combines shares into the original data. The first threshold
// of shares is combined and its digest verified; when every other held
// share lies on that polynomial too, the set is clean and that is the
// result. Otherwise a corrupt share is present, and Recover attributes
// it by maximal agreement: every threshold-sized combination of the
// held shares is combined, the ones whose digest verifies are grouped
// by polynomial, the polynomial with the most agreeing held shares
// wins, and every share off it is named in Recovered.Corrupt. Two
// polynomials tied for the most agreement are ErrAmbiguous; no
// verifying combination at all is ErrCorrupt. The digest alone cannot
// settle attribution: it vouches for the polynomial's value at x=0
// only, and two corrupt members whose errors cancel there verify with
// a wrong polynomial, which would blame the clean spares.
//
// The enumeration runs while C(held, k) <= 1024: at most that many
// combinations, each O(k·len) to verify, plus one O(held·k·len)
// agreement pass per distinct verifying polynomial. Above the cap,
// Recover settles for one verifying combination, the first threshold
// or, when that fails, the first threshold with each member swapped
// for each spare in turn (at most k·(held-k) further attempts), and
// names the shares off its polynomial; that attribution is exact for
// one corrupt share and unverified beyond. SPEC.md states the limit.
func (s *Set) Recover() (Recovered, error) {
	if !s.Complete() {
		return Recovered{}, fmt.Errorf("shamir: %d of %d required shares", len(s.shares), s.k)
	}
	idx := make([]int, s.k)
	for i := range idx {
		idx[i] = i
	}
	first, err := s.read(idx)
	if err != nil && !errors.Is(err, ErrCorrupt) {
		// A limit, deflate or type failure is a property of the sealed
		// content, not of one share; no other combination cures it.
		return Recovered{}, err
	}
	if err == nil && first.count == len(s.shares) {
		return first.rec, nil
	}
	if subsetsWithin(len(s.shares), s.k, attributionCap) {
		var known []reading
		if err == nil {
			known = append(known, first)
		}
		return s.attribute(known, idx)
	}
	if err == nil {
		return s.outvote(first)
	}
	for j := range s.k {
		for sp := s.k; sp < len(s.shares); sp++ {
			idx[j] = sp
			r, err := s.read(idx)
			switch {
			case err == nil:
				return s.outvote(r)
			case !errors.Is(err, ErrCorrupt):
				return Recovered{}, err
			}
		}
		idx[j] = j
	}
	// Disjoint windows of threshold positions after the first: with c
	// corrupt shares one of the first c+1 windows is clean whenever
	// held >= (c+1)·k, where the single swaps above all failed.
	for start := s.k; start+s.k <= len(s.shares); start += s.k {
		for i := range idx {
			idx[i] = start + i
		}
		r, err := s.read(idx)
		switch {
		case err == nil:
			return s.outvote(r)
		case !errors.Is(err, ErrCorrupt):
			return Recovered{}, err
		}
	}
	return Recovered{}, err
}

// outvote guards a reading found past the enumeration cap. A polynomial
// that at most half of the held shares agree with may be a cancelling
// pair of corrupt members outvoting the clean shares, so one
// combination drawn from its dissenters is read and the reading with
// the larger agreement wins; equal agreement is ErrAmbiguous.
func (s *Set) outvote(r reading) (Recovered, error) {
	if 2*r.count > len(s.shares) {
		return r.result(s), nil
	}
	idx := make([]int, 0, s.k)
	for i, agree := range r.agree {
		if !agree {
			idx = append(idx, i)
			if len(idx) == s.k {
				break
			}
		}
	}
	if len(idx) < s.k {
		return r.result(s), nil
	}
	alt, err := s.read(idx)
	switch {
	case err == nil && alt.count > r.count:
		return alt.result(s), nil
	case err == nil && alt.count == r.count:
		return Recovered{}, ErrAmbiguous
	case err != nil && !errors.Is(err, ErrCorrupt):
		return Recovered{}, err
	}
	return r.result(s), nil
}

// A reading is one verifying combination's polynomial: the content it
// unseals and which held shares lie on it.
type reading struct {
	rec   Recovered
	agree []bool // by position in Set.shares
	count int    // shares agreeing, the combination's own included
}

// read combines the shares at the given positions, verifies the digest
// and unseals the content, then evaluates the polynomial at every
// other held share's x to record which shares agree with it.
func (s *Set) read(idx []int) (reading, error) {
	raw := s.points(idx)
	rec, err := s.unseal(raw)
	if err != nil {
		return reading{}, err
	}
	r := reading{rec: rec, agree: make([]bool, len(s.shares))}
	for i, sh := range s.shares {
		if slices.Contains(idx, i) || bytes.Equal(interpolate(raw, byte(sh.Index)), sh.data) {
			r.agree[i] = true
			r.count++
		}
	}
	return r, nil
}

// contains reports whether every share of the combination idx lies on
// the reading's polynomial. k points fix a polynomial of degree below
// k, so such a combination reads the same polynomial again.
func (r reading) contains(idx []int) bool {
	for _, i := range idx {
		if !r.agree[i] {
			return false
		}
	}
	return true
}

// result is the reading's recovered content with every held share off
// its polynomial named in Corrupt, ascending.
func (r reading) result(s *Set) Recovered {
	rec := r.rec
	for i, ok := range r.agree {
		if !ok {
			rec.Corrupt = append(rec.Corrupt, s.shares[i].Index)
		}
	}
	slices.Sort(rec.Corrupt)
	return rec
}

// attribute enumerates the threshold-sized combinations of the held
// shares after idx, the first one, whose reading (if it verified) is
// in known. Combinations inside a known reading's agreement set are
// skipped, since they read the same polynomial; every other verifying
// combination is a distinct polynomial. The reading with the most
// agreeing shares wins, with its dissenters named; a tie for the most
// is ErrAmbiguous and no reading at all is ErrCorrupt.
func (s *Set) attribute(known []reading, idx []int) (Recovered, error) {
	for nextCombination(idx, len(s.shares)) {
		if slices.ContainsFunc(known, func(r reading) bool { return r.contains(idx) }) {
			continue
		}
		r, err := s.read(idx)
		switch {
		case err == nil:
			known = append(known, r)
		case !errors.Is(err, ErrCorrupt):
			return Recovered{}, err
		}
	}
	if len(known) == 0 {
		return Recovered{}, ErrCorrupt
	}
	best, tie := known[0], false
	for _, r := range known[1:] {
		switch {
		case r.count > best.count:
			best, tie = r, false
		case r.count == best.count:
			tie = true
		}
	}
	if tie {
		return Recovered{}, ErrAmbiguous
	}
	return best.result(s), nil
}

// nextCombination advances idx, an ascending combination of positions
// below n, to the next one in lexicographic order and reports whether
// there was one.
func nextCombination(idx []int, n int) bool {
	k := len(idx)
	i := k - 1
	for i >= 0 && idx[i] == n-k+i {
		i--
	}
	if i < 0 {
		return false
	}
	idx[i]++
	for j := i + 1; j < k; j++ {
		idx[j] = idx[j-1] + 1
	}
	return true
}

// subsetsWithin reports whether C(n, k) <= limit. The running product
// is C(n-k+i, i) at step i, an exact integer, and stops at the limit,
// so it never overflows.
func subsetsWithin(n, k, limit int) bool {
	if k > n-k {
		k = n - k
	}
	c := 1
	for i := 1; i <= k; i++ {
		c = c * (n - k + i) / i
		if c > limit {
			return false
		}
	}
	return true
}

// points returns the shares at the given positions in the raw (x, y)
// form Combine takes.
func (s *Set) points(idx []int) [][]byte {
	raw := make([][]byte, len(idx))
	for i, j := range idx {
		raw[i] = make([]byte, 1+len(s.shares[j].data))
		raw[i][0] = byte(s.shares[j].Index)
		copy(raw[i][1:], s.shares[j].data)
	}
	return raw
}

// unseal combines the raw shares, verifies the integrity digest and
// unseals the content.
func (s *Set) unseal(raw [][]byte) (Recovered, error) {
	inner, err := Combine(raw)
	if err != nil {
		return Recovered{}, err
	}
	typ := inner[0]
	payload := inner[1 : len(inner)-digestLen]
	if !bytes.Equal(digest(s.k, typ, payload), inner[len(inner)-digestLen:]) {
		return Recovered{}, ErrCorrupt
	}
	rec := Recovered{FileType: typ &^ flagDeflated, Data: payload}
	if rec.FileType < 'A' || rec.FileType > 'Z' {
		return Recovered{}, fmt.Errorf("shamir: invalid recovered file type %q", rec.FileType)
	}
	// The digest covers the compressed payload, so only verified
	// content is inflated, under the size cap.
	if typ&flagDeflated != 0 {
		if rec.Data, err = inflate(payload, int64(s.limit())); err != nil {
			return Recovered{}, err
		}
	}
	return rec, nil
}

func (s *Set) limit() int {
	if s.Limit == 0 {
		return DefaultLimit
	}
	return s.Limit
}

// RecoverData is the one-shot form of Set: collect every payload, then
// recover.
func RecoverData(payloads [][]byte) (Recovered, error) {
	var s Set
	for _, p := range payloads {
		if err := s.Add(p); err != nil {
			return Recovered{}, err
		}
	}
	return s.Recover()
}
