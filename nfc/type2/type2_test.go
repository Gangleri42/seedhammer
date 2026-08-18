package type2

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"testing"
)

func TestReader(t *testing.T) {
	tests := [][]byte{
		bytes.Repeat([]byte{0xde, 0xad, 0xbe, 0xef}, 100),
	}
	for _, data := range tests {
		capContainer := []byte{
			ccMagic,
			0x10, // Version.
			byte(len(data) / 8),
			0x00, // Read/write access.
		}
		tag := &Tag{
			uid: 0xdeadbeef,
			mem: append(capContainer, data...),
		}
		r, err := NewReader(tag)
		if err != nil {
			t.Fatal(err)
		}
		// Buffer reading to ensure a minimum read size.
		bufr := bufio.NewReaderSize(r, 128)
		got, err := io.ReadAll(bufr)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, data) {
			t.Errorf("\nread\n%x\nexpected\n%x", got, data)
		}
	}
}

// A tag claiming more memory than the uint8 read address can reach:
// cc[2] 0x7f claims 254 blocks, but the wire address block+ccBlock+1
// caps the reachable span at 252 blocks, the last read returning
// address 255. The reader must stop there; past it the address byte
// wraps to the low blocks and the loop never finds its terminator.
func TestReaderClaimPastAddressSpace(t *testing.T) {
	data := make([]byte, 254*blockSize)
	for i := range data {
		data[i] = byte(i)
	}
	capContainer := []byte{ccMagic, 0x10, 0x7f, 0x00}
	tag := &Tag{uid: 0xdeadbeef, mem: append(capContainer, data...)}
	r, err := NewReader(tag)
	if err != nil {
		t.Fatal(err)
	}
	bufr := bufio.NewReaderSize(r, 128)
	got, err := io.ReadAll(bufr)
	if err != nil {
		t.Fatal(err)
	}
	if want := data[:maxMemBlocks*blockSize]; !bytes.Equal(got, want) {
		t.Errorf("read %d bytes, want the %d the address byte reaches", len(got), len(want))
	}
}

// Tag implements a NFC forum type 2 tag.
type Tag struct {
	uid   uint32
	state tagState
	mem   []byte
	resp  []byte
}

type tagState int

const (
	tagIdle tagState = iota
	tagSelected
)

func (t *Tag) Read(b []byte) (int, error) {
	n := copy(b, t.resp)
	if n < len(t.resp) {
		return n, io.ErrShortBuffer
	}
	t.resp = t.resp[:0]
	return n, io.EOF
}

func (t *Tag) Write(req []byte) (int, error) {
	n := len(req)
	if len(t.resp) > 0 {
		return 0, errors.New("write: double write")
	}
	switch {
	case t.state == tagIdle && len(req) == 1 && req[0] == cmdSENS_REQ:
		t.resp = append(t.resp, 0x00, 0x44) // ATQA
	case t.state == tagIdle && len(req) == 2 && req[0] == casLevel1 && req[1] == 0x20:
		t.resp = binary.BigEndian.AppendUint32(t.resp, t.uid)
		t.resp = append(t.resp, bcc(t.resp))
	case t.state == tagIdle && len(req) == 7 && req[0] == casLevel1 && req[1] == 0x70:
		uid := binary.BigEndian.Uint32(req[2:])
		if uid != t.uid {
			return 0, fmt.Errorf("write: got uid %.4x, expected: %.4x", uid, t.uid)
		}
		const sak = 0x00
		t.resp = append(t.resp, sak)
		t.state = tagSelected
	case t.state == tagSelected && len(req) == 2 && req[0] == cmdRead:
		bno := int(req[1])
		if bno < ccBlock {
			return 0, fmt.Errorf("write: out of bounds")
		}
		bno -= ccBlock
		page := make([]byte, 16)
		start := bno * blockSize
		if start >= len(t.mem) {
			return 0, fmt.Errorf("write: out of bounds")
		}
		copy(page, t.mem[start:])
		t.resp = append(t.resp, page...)
	default:
		return 0, fmt.Errorf("write: unknown request: %x", req)
	}
	return n, nil
}
