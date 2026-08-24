// Package deflate compresses to raw DEFLATE (RFC 1951, no zlib
// wrapper) under the window constraint BBQr's Zlib encoding fixes:
// every back-reference stays within a 1 KiB window (wbits=10), so the
// leanest decoders accept the stream. Compress picks the smallest of
// three compliant encodings of the input: greedy LZ77 in a single
// fixed-Huffman block, a literal-only fixed-Huffman block (no
// back-references at all), or a stored block.
//
// Decompress accepts any raw DEFLATE stream, window size
// notwithstanding, and is a thin wrapper over compress/flate.
package deflate

import (
	"bytes"
	"compress/flate"
	"fmt"
	"io"
)

// Window is the largest back-reference distance the compressor emits,
// matching the 1 KiB history a wbits=10 decoder keeps.
const Window = 1024

const (
	minMatch = 3
	maxMatch = 258
	maxChain = 64
	// hashBits sizes the matcher's chain table; 8k entries of chain
	// heads balance match quality against the device heap.
	hashBits  = 13
	hashEmpty = 0 // hash table stores position+1 so zero reads as empty
)

// Compress returns data as a raw DEFLATE stream with all
// back-references within Window bytes. The candidate encodings are
// greedy LZ77 in a fixed-Huffman block, a literal-only fixed-Huffman
// block, and a stored block; the smallest wins. Everything is built
// in this package over fixed tables, so firmware never pays for the
// standard library compressor's window and hash allocations.
func Compress(data []byte) []byte {
	best := compressFixed(data, true)
	if lit := compressFixed(data, false); len(lit) < len(best) {
		best = lit
	}
	if len(data) <= 65535 {
		if s := storedBlock(data); len(s) < len(best) {
			best = s
		}
	}
	return best
}

// Decompress decompresses a raw DEFLATE stream. A positive limit caps
// the decompressed size in bytes.
func Decompress(data []byte, limit int64) ([]byte, error) {
	r := flate.NewReader(bytes.NewReader(data))
	defer r.Close()
	var raw []byte
	var err error
	if limit > 0 {
		raw, err = io.ReadAll(io.LimitReader(r, limit+1))
		if int64(len(raw)) > limit {
			return nil, fmt.Errorf("deflate: payload exceeds %d byte limit", limit)
		}
	} else {
		raw, err = io.ReadAll(r)
	}
	if err != nil {
		return nil, fmt.Errorf("deflate: %w", err)
	}
	return raw, nil
}

// bitWriter packs bits LSB-first, as DEFLATE orders them. Huffman
// codes are the exception: they are written most significant bit
// first (RFC 1951 section 3.1.1).
type bitWriter struct {
	out   []byte
	acc   uint32
	nbits uint
}

func (w *bitWriter) bits(v uint32, n uint) {
	w.acc |= v << w.nbits
	w.nbits += n
	for w.nbits >= 8 {
		w.out = append(w.out, byte(w.acc))
		w.acc >>= 8
		w.nbits -= 8
	}
}

// code writes an n-bit Huffman code most significant bit first.
func (w *bitWriter) code(v uint32, n uint) {
	w.bits(reverse(v, n), n)
}

func (w *bitWriter) finish() []byte {
	if w.nbits > 0 {
		w.out = append(w.out, byte(w.acc))
	}
	return w.out
}

func reverse(v uint32, n uint) uint32 {
	r := uint32(0)
	for i := uint(0); i < n; i++ {
		r = r<<1 | (v>>i)&1
	}
	return r
}

// fixedLiteralCode returns the fixed-Huffman code and width for a
// literal or the end-of-block symbol, per RFC 1951 section 3.2.6.
func fixedLiteralCode(sym int) (code uint32, bits uint) {
	switch {
	case sym < 144:
		return 0x30 + uint32(sym), 8
	case sym < 256:
		return 0x190 + uint32(sym-144), 9
	default: // 256 end of block
		return 0, 7
	}
}

// lengthTable is the RFC 1951 section 3.2.5 length table: base length
// and extra bit count per length symbol 257..285.
var lengthTable = [29]struct {
	base int
	eb   uint
}{
	{3, 0}, {4, 0}, {5, 0}, {6, 0}, {7, 0}, {8, 0}, {9, 0}, {10, 0}, // 257-264
	{11, 1}, {13, 1}, {15, 1}, {17, 1}, // 265-268
	{19, 2}, {23, 2}, {27, 2}, {31, 2}, // 269-272
	{35, 3}, {43, 3}, {51, 3}, {59, 3}, // 273-276
	{67, 4}, {83, 4}, {99, 4}, {115, 4}, // 277-280
	{131, 5}, {163, 5}, {195, 5}, {227, 5}, // 281-284
	{258, 0}, // 285
}

// lengthCode maps a match length (3..258) to its length symbol
// (257..285), the extra bits and their value.
func lengthCode(length int) (sym int, extraBits uint, extra uint32) {
	for i := len(lengthTable) - 1; i >= 0; i-- {
		if t := lengthTable[i]; length >= t.base {
			return 257 + i, t.eb, uint32(length - t.base)
		}
	}
	panic("deflate: length below table")
}

// fixedLengthCode returns the code bits of a length symbol: 257-279
// are 7-bit, 280-285 are 8-bit.
func fixedLengthCode(sym int) (code uint32, bits uint) {
	if sym < 280 {
		return uint32(sym - 256), 7
	}
	return 0xC0 + uint32(sym-280), 8
}

// distTable is the RFC 1951 section 3.2.5 distance table up to the 1
// KiB window: base distance and extra bit count per symbol 0..19.
var distTable = [20]struct {
	base int
	eb   uint
}{
	{1, 0}, {2, 0}, {3, 0}, {4, 0},
	{5, 1}, {7, 1},
	{9, 2}, {13, 2},
	{17, 3}, {25, 3},
	{33, 4}, {49, 4},
	{65, 5}, {97, 5},
	{129, 6}, {193, 6},
	{257, 7}, {385, 7},
	{513, 8}, {769, 8},
}

// distCode maps a distance (1..Window) to its 5-bit symbol, extra
// bits and value.
func distCode(dist int) (sym int, extraBits uint, extra uint32) {
	for i := len(distTable) - 1; i >= 0; i-- {
		if t := distTable[i]; dist >= t.base {
			return i, t.eb, uint32(dist - t.base)
		}
	}
	panic("deflate: distance below table")
}

// storedBlock wraps data in a single final stored block (BTYPE=00).
// LEN is 16 bits, so callers must not pass more than 65535 bytes.
func storedBlock(data []byte) []byte {
	w := bitWriter{}
	w.bits(1, 1) // BFINAL
	w.bits(0, 2) // BTYPE: stored
	w.finish()   // stored blocks align to the byte boundary
	n := len(data)
	out := append(w.out, byte(n), byte(n>>8), byte(^n), byte(^n>>8))
	return append(out, data...)
}

// hash3 hashes the three bytes at pos into hbits bits.
func hash3(data []byte, pos int, hbits uint) int32 {
	v := uint32(data[pos])<<16 | uint32(data[pos+1])<<8 | uint32(data[pos+2])
	return int32((v * 0x9E3779B1) >> (32 - hbits))
}

// compressFixed emits data as one final fixed-Huffman block (BFINAL,
// BTYPE=01). With matching set, greedy LZ77 matches inside the 1 KiB
// window replace repeats; without it the block is literal-only (no
// back-references, so any decoder window accepts it).
func compressFixed(data []byte, matching bool) []byte {
	w := bitWriter{}
	w.bits(1, 1) // BFINAL
	w.bits(1, 2) // BTYPE: fixed Huffman
	if !matching {
		// Literal-only: no matcher, and none of the matcher's tables,
		// which are the allocation a pure Huffman pass has no use for.
		for _, b := range data {
			code, n := fixedLiteralCode(int(b))
			w.code(code, n)
		}
		code, n := fixedLiteralCode(256) // end of block
		w.code(code, n)
		return w.finish()
	}
	// Greedy matcher with hash chains, per RFC 1951 section 4. head
	// hashes a 3-byte prefix to its newest position+1; prev links each
	// window slot to the previous same-hash position+1. The head table
	// is sized to the input: a plate payload never justifies the full
	// index, and the device heap is not ours to waste.
	hbits := uint(hashBits)
	for n := uint(1) << hbits; n > uint(len(data)) && hbits > 10; hbits-- {
		n >>= 1
	}
	head := make([]int32, 1<<hbits)
	var prev [Window]int32
	insert := func(pos int) {
		if pos+minMatch > len(data) {
			return
		}
		h := hash3(data, pos, hbits)
		prev[pos&(Window-1)] = head[h]
		head[h] = int32(pos + 1)
	}
	match := func(pos int) (length, dist int) {
		remain := len(data) - pos
		if remain < minMatch {
			return 0, 0
		}
		limit := min(remain, maxMatch)
		best := 0
		cand := head[hash3(data, pos, hbits)]
		for chain := 0; cand != hashEmpty && chain < maxChain; chain++ {
			c := int(cand) - 1
			d := pos - c
			if d > Window {
				break // the chain ring reaches back exactly one window
			}
			// Quick reject on the byte that would extend the best
			// match, then measure.
			if best < limit && data[c+best] == data[pos+best] {
				l := 0
				for l < limit && data[c+l] == data[pos+l] {
					l++
				}
				if l > best {
					best, dist = l, d
				}
			}
			cand = prev[c&(Window-1)]
		}
		if best < minMatch {
			return 0, 0
		}
		return best, dist
	}
	for pos := 0; pos < len(data); {
		length, dist := match(pos)
		if length == 0 {
			code, n := fixedLiteralCode(int(data[pos]))
			w.code(code, n)
			insert(pos)
			pos++
			continue
		}
		sym, eb, ev := lengthCode(length)
		code, n := fixedLengthCode(sym)
		w.code(code, n)
		if eb > 0 {
			w.bits(ev, eb)
		}
		dsym, deb, dev := distCode(dist)
		w.code(uint32(dsym), 5)
		if deb > 0 {
			w.bits(dev, deb)
		}
		for i := pos; i < pos+length; i++ {
			insert(i)
		}
		pos += length
	}
	code, n := fixedLiteralCode(256) // end of block
	w.code(code, n)
	return w.finish()
}
