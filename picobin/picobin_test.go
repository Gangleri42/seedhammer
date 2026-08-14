package picobin

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"io"
	"slices"
	"testing"
)

//go:embed testdata/signed.bin.gz
var signedGZ []byte

//go:embed testdata/hashed.bin.gz
var hashedGZ []byte

var (
	signedImage = mustGunzip(signedGZ)
	hashedImage = mustGunzip(hashedGZ)
)

func TestSignature(t *testing.T) {
	img := bytes.NewReader(signedImage)
	finfo, err := NewImage(img)
	if err != nil {
		t.Fatal(err)
	}
	pkey, sig, err := finfo.Signature()
	if err != nil {
		t.Fatal(err)
	}
	wantSig := unhex("28510e83c2ab21039c023c4c10967405d6efd7ec15bcd8ed92b92bd029a9da4721404e4bd1424bb2fbc1faf102dd63d1797f54be4c872c53c1a63a6ad4305281")
	wantPubKey := unhex("e4894ee23471084e88852dea63f6d8bad35ef6db802f0cf2946cfa67572fd49eb65f5ac02c35534bc45159783cd3a7403eea91e55f482e35e446a0e7089de6ff")
	if !slices.Equal(sig, wantSig) || !slices.Equal(pkey, wantPubKey) {
		t.Errorf("signature mismatch: got\npublic key: %x\nsignature %x\nexpected\npublic key %x\nsignature %x", pkey, sig, wantPubKey, wantSig)
	}
}

func TestSign(t *testing.T) {
	newKey := bytes.Repeat([]byte{0xde, 0xad}, 32)
	newSig := bytes.Repeat([]byte{0xbe, 0xef}, 32)
	for _, img := range [][]byte{signedImage, hashedImage} {
		img, err := NewImage(bytes.NewReader(img))
		if err != nil {
			t.Fatal(err)
		}
		resigned := new(bytes.Buffer)
		if err := img.Sign(resigned, newKey, newSig); err != nil {
			t.Fatal(err)
		}
		r := bytes.NewReader(resigned.Bytes())
		finfo, err := NewImage(r)
		if err != nil {
			t.Fatal(err)
		}
		pkey, sig, err := finfo.Signature()
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(sig, newSig) || !slices.Equal(pkey, newKey) {
			t.Errorf("signature mismatch: got\npublic key: %x\nsignature %x\nexpected\npublic key %x\nsignature %x", pkey, sig, newKey, newSig)
		}
	}
}

func TestHashData(t *testing.T) {
	img := bytes.NewReader(signedImage)
	finfo, err := NewImage(img)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := finfo.HashData(img, 0x10000000)
	if err != nil {
		t.Fatal(err)
	}
	wantHash := unhex("15cf016da39866e8d1c0dff1aaa29fd0429876f9b55a290c1fc6fce819783557")
	if !slices.Equal(hash[:], wantHash) {
		t.Errorf("hash mismatch: got\n%x\nexpected\n%x", hash, wantHash)
	}
}

func TestHash(t *testing.T) {
	img := bytes.NewReader(hashedImage)
	finfo, err := NewImage(img)
	if err != nil {
		t.Fatal(err)
	}
	h, err := finfo.Hash()
	if err != nil {
		t.Fatal(err)
	}
	wantHash := unhex("0ef02dfd453b87629fb168b35f76ad6095e40a50a7c2650cd09fa27424a92bd7")
	if !slices.Equal(h, wantHash) {
		t.Errorf("got hash %x, expected %x", h, wantHash)
	}
}

// loadMapEntry is one raw LOAD_MAP entry for buildLoadMapImage.
// storage is a file offset; 0 means a zero-storage entry.
type loadMapEntry struct {
	storage uint32
	runtime uint32
	size    uint32
}

const (
	testPayloadLen = 256
	testImageAddr  = 0x10000000
)

// buildLoadMapImage constructs a minimal single-block image in the
// same encoding as the signed testdata image (relative LOAD_MAP,
// 1-byte-size items, 2-byte-size LAST item): payload bytes first, then
// header, LOAD_MAP, HASH_DEF, LAST, link and footer. Payload bytes
// stay in 0x40..0x7f so the header magic cannot appear early.
func buildLoadMapImage(entries []loadMapEntry) []byte {
	var img bytes.Buffer
	for i := range testPayloadLen {
		img.WriteByte(byte(0x40 | i&0x3f))
	}
	w32 := func(v uint32) {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], v)
		img.Write(b[:])
	}
	n := uint32(len(entries))
	loadMapOff := uint32(testPayloadLen) + 4
	w32(0xffffded3)        // block header
	w32(0x06 | (1+3*n)<<8) // LOAD_MAP item, relative
	for _, e := range entries {
		storage := e.storage
		if storage != 0 {
			// Relative encoding: raw word + loadMapOff = file offset,
			// in wrapping uint32 arithmetic like the real image.
			storage = e.storage - loadMapOff
		}
		w32(storage)
		w32(e.runtime)
		w32(e.size)
	}
	w32(0x47 | 2<<8 | 0x01<<24) // HASH_DEF, sha256
	w32(1 + (1 + 3*n) + 2)      // block words covered by the hash
	total := (1 + 3*n) + 2
	w32(0xFF | total<<8) // LAST item, 2-byte size form
	w32(0)               // link: single-block loop
	w32(0xab123579)      // footer
	return img.Bytes()
}

// A zero-storage LOAD_MAP entry hashes its own size word in place of
// storage bytes, per the datasheet rule picotool and the boot ROM
// implement. The two-zero shape pins the indexing: hashing entry 0's
// size word for every zero-storage entry collapses entries past index
// 0 onto the wrong word, and the boot ROM then rejects the digest.
func TestHashDataZeroStorageEntries(t *testing.T) {
	entries := []loadMapEntry{
		{storage: 0, runtime: 0x20000000, size: 0x00013370},
		{storage: 0, runtime: 0x20040000, size: 0x00026600},
		{storage: 4, runtime: testImageAddr, size: testPayloadLen - 4},
	}
	img := buildLoadMapImage(entries)
	pb, err := NewImage(bytes.NewReader(img))
	if err != nil {
		t.Fatal(err)
	}
	got, err := pb.HashData(bytes.NewReader(img), testImageAddr)
	if err != nil {
		t.Fatal(err)
	}
	// Compose the expectation explicitly, entry by entry.
	h := sha256.New()
	word := func(v uint32) {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], v)
		h.Write(b[:])
	}
	word(entries[0].size)           // zero-storage: its own size word
	word(entries[1].size)           // zero-storage: its own size word
	h.Write(img[4:testPayloadLen])  // the stored segment
	blockWords := 1 + (1 + 3*3) + 2 // header, LOAD_MAP, HASH_DEF
	h.Write(img[testPayloadLen : testPayloadLen+4*blockWords])
	if want := h.Sum(nil); !bytes.Equal(got, want) {
		t.Errorf("digest %x, want %x", got, want)
	}
}

func unhex(h string) []byte {
	v, err := hex.DecodeString(h)
	if err != nil {
		panic(err)
	}
	return v
}

func mustGunzip(f []byte) []byte {
	r, err := gzip.NewReader(bytes.NewReader(f))
	if err != nil {
		panic(err)
	}
	d, err := io.ReadAll(r)
	if err != nil {
		panic(err)
	}
	return d
}
