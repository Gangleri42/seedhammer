package shamir

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"seedhammer.com/bbqr"
)

// A share envelope is the payload of one share's BBQr series (file
// type M). The wire format is:
//
//	tag                2 bytes, random, common to the shares of one split
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

// Share is a parsed share envelope.
type Share struct {
	Threshold int    // k: shares needed to recover
	Index     int    // x coordinate of this share, 1..255
	Tag       uint16 // random identifier common to the shares of one split

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
	// Corrupt is the index of the corrupt share a retry excluded to
	// recover, zero when the first combination verified. With spare
	// shares beyond the threshold, a single corrupt share is thereby
	// survived and named.
	Corrupt int
}

// SplitData splits data of the given BBQr file type into n shares, any
// k of which recover it, and encodes each share as its own type M BBQr
// series in base 32 encoding. The data is DEFLATE-compressed when
// compression shrinks it, sealed with its type and an integrity
// digest, and only then split, so shares carry no compressible
// structure and reveal neither type nor flag below the threshold. rand
// must be a cryptographic random source; on the device it is the TRNG.
//
// rand supplies, in order: the k-1 random polynomial coefficients per
// sealed byte (see Split), then 2 bytes of share tag. Fixing a rand
// stream therefore reproduces a split exactly, which is how the test
// vectors in SPEC.md are defined.
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
	raw, err := Split(inner, k, n, rand)
	if err != nil {
		return nil, err
	}
	var tag [2]byte
	if _, err := io.ReadFull(rand, tag[:]); err != nil {
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

// Set collects the shares of one split session, in any order, for
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
// other split sessions or with different parameters are rejected.
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

// Recover combines shares into the original data. With spare shares
// beyond the threshold, one corrupt share is survived: each member of
// the failed combination is excluded in turn for a spare, and the
// excluded index is reported in Recovered.Corrupt when that recovers.
// The integrity digest decides which result is correct.
func (s *Set) Recover() (Recovered, error) {
	if !s.Complete() {
		return Recovered{}, fmt.Errorf("shamir: %d of %d required shares", len(s.shares), s.k)
	}
	idx := make([]int, s.k)
	for i := range idx {
		idx[i] = i
	}
	rec, err := s.recoverSubset(idx)
	if err == nil || len(s.shares) <= s.k {
		return rec, err
	}
	for j := range s.k {
		sub := append(append(idx[:j:j], idx[j+1:]...), s.k)
		if rec, err2 := s.recoverSubset(sub); err2 == nil {
			rec.Corrupt = s.shares[j].Index
			return rec, nil
		}
	}
	return Recovered{}, err
}

// recoverSubset combines the shares at the given indices, verifies the
// integrity digest and unseals the content.
func (s *Set) recoverSubset(idx []int) (Recovered, error) {
	raw := make([][]byte, len(idx))
	for i, j := range idx {
		raw[i] = make([]byte, 1+len(s.shares[j].data))
		raw[i][0] = byte(s.shares[j].Index)
		copy(raw[i][1:], s.shares[j].data)
	}
	inner, err := Combine(raw)
	if err != nil {
		return Recovered{}, err
	}
	typ := inner[0]
	payload := inner[1 : len(inner)-digestLen]
	if !bytes.Equal(digest(s.k, typ, payload), inner[len(inner)-digestLen:]) {
		return Recovered{}, errors.New("shamir: integrity digest mismatch")
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
