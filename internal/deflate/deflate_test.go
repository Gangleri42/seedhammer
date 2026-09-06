package deflate

import (
	"bytes"
	"encoding/hex"
	"math/rand"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	text := []byte(strings.Repeat("the quick brown fox jumps over the lazy dog. ", 500))
	repetitive := bytes.Repeat([]byte{0xAA}, 70000) // longer than one stored block
	binary := make([]byte, 5000)
	for i := range binary {
		binary[i] = byte(i * i >> 3)
	}
	random := make([]byte, 100000)
	rng.Read(random)
	structured := make([]byte, 0, 20000)
	for range 200 {
		structured = append(structured, rngBytes(rng, 32)...)
		structured = append(structured, []byte("xpub6DiYrfRwNnjeX4vHsWMajJVFKrbEEnu8gAW9vDuQzgTWEsEHE16sGWeXXUV1LBWQE1yCTmeprSNcqZ3W74hqVdgDbtYHUv3eM4W2TEUhpan")...)
	}
	for i, data := range [][]byte{
		{}, {0}, {0, 0, 0}, text, repetitive, binary, random, structured,
		[]byte(strings.Repeat("abababab", 1000)),
		[]byte(strings.Repeat("a", 300)),
	} {
		got, err := Decompress(Compress(data), 0)
		if err != nil {
			t.Fatalf("input %d: %v", i, err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("input %d: round trip mismatch (%d bytes)", i, len(data))
		}
	}
}

func TestCompressBeatsRaw(t *testing.T) {
	text := strings.Repeat("seedhammer backup plate ", 500)
	got := Compress([]byte(text))
	// Fixed-Huffman literals alone would cost about the input size;
	// LZ77 in the window must do clearly better.
	if len(got) > len(text)/2 {
		t.Fatalf("compressed to %d of %d bytes", len(got), len(text))
	}
}

// TestWindowCompliance parses a compressFixed stream symbol by symbol
// with an independent fixed-Huffman decoder, asserting every distance
// stays within the 1 KiB window and within the bytes emitted so far
// (the properties a wbits=10 decoder relies on) and that the symbols
// reconstruct the input.
func TestWindowCompliance(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	inputs := [][]byte{
		[]byte(strings.Repeat("window check ", 2000)),
		bytes.Repeat([]byte{0}, 5000),
		rngBytes(rng, 3000),
	}
	for i, data := range inputs {
		out, err := parseFixed(t, compressFixed(data, true))
		if err != nil {
			t.Fatalf("input %d: %v", i, err)
		}
		if !bytes.Equal(out, data) {
			t.Fatalf("input %d: reconstructed stream differs", i)
		}
	}
}

// parseFixed decodes one fixed-Huffman DEFLATE stream, verifying the
// window rules, and returns the reconstructed bytes.
func parseFixed(t *testing.T, stream []byte) ([]byte, error) {
	t.Helper()
	r := &bitReader{data: stream}
	if bfinal := r.bits(1); bfinal != 1 {
		t.Fatal("BFINAL not set")
	}
	if btype := r.bits(2); btype != 1 {
		t.Fatalf("BTYPE %d, want fixed (1)", btype)
	}
	var out []byte
	for {
		sym := r.fixedLitLen(t)
		switch {
		case sym < 256:
			out = append(out, byte(sym))
		case sym == 256:
			return out, nil
		default:
			length := int(r.length(t, sym))
			dsym := int(r.code(5))
			dist := int(r.distance(t, dsym))
			if dist > Window {
				t.Fatalf("distance %d exceeds the 1 KiB window", dist)
			}
			if dist > len(out) {
				t.Fatalf("distance %d reaches before the stream start", dist)
			}
			for range length {
				out = append(out, out[len(out)-dist])
			}
		}
	}
}

// bitReader reads DEFLATE bits: LSB-first for plain elements,
// MSB-first for Huffman codes.
type bitReader struct {
	data []byte
	pos  int // bit position
}

func (r *bitReader) bit() uint32 {
	if r.pos/8 >= len(r.data) {
		return 0 // end padding reads as zero
	}
	b := (r.data[r.pos/8] >> (r.pos % 8)) & 1
	r.pos++
	return uint32(b)
}

// bits reads n bits LSB-first (plain data elements).
func (r *bitReader) bits(n uint) uint32 {
	v := uint32(0)
	for i := uint(0); i < n; i++ {
		v |= r.bit() << i
	}
	return v
}

// code reads an n-bit Huffman code, MSB-first.
func (r *bitReader) code(n uint) uint32 {
	v := uint32(0)
	for range n {
		v = v<<1 | r.bit()
	}
	return v
}

// fixedLitLen decodes one literal/length symbol of the fixed tree.
func (r *bitReader) fixedLitLen(t *testing.T) int {
	t.Helper()
	acc := uint32(0)
	n := uint(0)
	for n < 9 {
		acc = acc<<1 | r.bit()
		n++
		switch n {
		case 7:
			if acc <= 23 { // 0000000-0010111: 256-279
				return 256 + int(acc)
			}
		case 8:
			if acc >= 0x30 && acc <= 0xBF { // literals 0-143
				return int(acc - 0x30)
			}
			if acc >= 0xC0 && acc <= 0xC7 { // lengths 280-287
				return 280 + int(acc-0xC0)
			}
		case 9:
			if acc >= 0x190 && acc <= 0x1FF { // literals 144-255
				return 144 + int(acc-0x190)
			}
			t.Fatalf("no fixed code for 9-bit value %#x", acc)
		}
	}
	t.Fatal("unreachable")
	return 0
}

// length resolves a length symbol with its extra bits.
func (r *bitReader) length(t *testing.T, sym int) uint32 {
	t.Helper()
	i := sym - 257
	if i < 0 || i >= len(lengthTable) {
		t.Fatalf("invalid length symbol %d", sym)
	}
	e := lengthTable[i]
	return uint32(e.base) + r.bits(e.eb)
}

// distance resolves a distance symbol with its extra bits.
func (r *bitReader) distance(t *testing.T, sym int) uint32 {
	t.Helper()
	if sym >= len(distTable) {
		t.Fatalf("invalid distance symbol %d", sym)
	}
	e := distTable[sym]
	return uint32(e.base) + r.bits(e.eb)
}

func rngBytes(rng *rand.Rand, n int) []byte {
	b := make([]byte, n)
	rng.Read(b)
	return b
}

func FuzzDeflate(f *testing.F) {
	f.Add([]byte("seedhammer"))
	f.Add([]byte(strings.Repeat("AB", 100)))
	f.Fuzz(func(t *testing.T, data []byte) {
		enc := Compress(data)
		got, err := Decompress(enc, 0)
		if err != nil {
			t.Fatalf("Decompress(Compress): %v", err)
		}
		if !bytes.Equal(got, data) {
			t.Fatal("round trip mismatch")
		}
		// The fixed block alone must keep every distance in the window.
		if _, err := parseFixed(t, compressFixed(data, true)); err != nil {
			t.Fatal(err)
		}
	})
}

// TestCompressGolden pins the bitstream: derived Shamir shares of
// compressed data (shamir/SPEC.md, Conformance) reproduce only while
// Compress emits exactly these bytes for this input.
func TestCompressGolden(t *testing.T) {
	const want = "2b4e4d4dc948cccd4d2d5218658e324799a34cb29900"
	got := hex.EncodeToString(Compress([]byte(strings.Repeat("seedhammer ", 100))))
	if got != want {
		t.Fatalf("Compress bitstream changed:\n got %s\nwant %s", got, want)
	}
}
