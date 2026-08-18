package poller

import (
	"bytes"
	"io"
	"testing"
	"time"
)

// fakeDevice plays the reader side of the emulated-tag exchange: Read
// hands the tag one queued frame per call the way the driver hands one
// FIFO per call, a nil frame is the field dropping, and Interrupt ends
// a blocked Read the way the driver's cancel does.
type fakeDevice struct {
	frames chan []byte
	intr   chan struct{}

	writes [][]byte
	slept  bool
	closed bool
}

func newFakeDevice() *fakeDevice {
	return &fakeDevice{
		frames: make(chan []byte, 32),
		intr:   make(chan struct{}, 1),
	}
}

func (d *fakeDevice) Read(p []byte) (int, error) {
	select {
	case f := <-d.frames:
		if f == nil {
			return 0, io.EOF
		}
		return copy(p, f), io.EOF
	case <-d.intr:
		return 0, io.EOF
	}
}

func (d *fakeDevice) Write(p []byte) (int, error) {
	d.writes = append(d.writes, bytes.Clone(p))
	return len(p), nil
}

func (d *fakeDevice) Interrupt() {
	select {
	case d.intr <- struct{}{}:
	default:
	}
}

func (d *fakeDevice) Close() error               { d.closed = true; return nil }
func (d *fakeDevice) Detect() (bool, error)      { return false, nil }
func (d *fakeDevice) SetProtocol(Protocol) error { return nil }
func (d *fakeDevice) Sleep() error               { d.slept = true; return nil }
func (d *fakeDevice) ReadCapacity() int          { return 8192 }

// writer scripts a Type 4 writer's frames.
type writer struct {
	d  *fakeDevice
	bn byte
}

func (w *writer) frame(b ...byte) { w.d.frames <- b }

func (w *writer) iblock(apdu ...byte) {
	w.frame(append([]byte{0x02 | w.bn, 0x00}, apdu...)...)
	w.bn = 1 - w.bn
}

func (w *writer) selectApp() {
	w.iblock(0xa4, 0x04, 0x00, 0x07, 0xd2, 0x76, 0x00, 0x00, 0x85, 0x01, 0x01, 0x00)
}
func (w *writer) selectFile() { w.iblock(0xa4, 0x00, 0x0c, 0x02, 0x00, 0x01) }

func (w *writer) update(off uint16, data []byte) {
	apdu := []byte{0xd6, byte(off >> 8), byte(off), byte(len(data))}
	w.iblock(append(apdu, data...)...)
}

// record is a single well-known Text record carrying text, as a phone
// writes one.
func record(text string) []byte {
	payload := append([]byte{0x02, 'e', 'n'}, text...)
	msg := []byte{0xd1, 0x01, byte(len(payload)), 'T'}
	return append(msg, payload...)
}

// The writer's trailing frames arrive after the record completes: the
// NLEN commit, a presence check, DESELECT. Close sees them out, so the
// commit is acknowledged and the writer reports success, and only then
// switches the device off.
func TestCloseSeesTheWriterOut(t *testing.T) {
	d := newFakeDevice()
	p := New(d)
	w := &writer{d: d}

	msg := record("the last word")
	w.frame(0xe0, 0x80) // SENS_REQ; the tag answers ATS.
	w.selectApp()
	w.selectFile()
	w.update(0, []byte{0, 0}) // NLEN = 0.
	w.update(2, msg)          // The payload, one chunk.

	var got []byte
	buf := make([]byte, 256)
	for {
		n, err := p.Read(buf)
		got = append(got, buf[:n]...)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if want := "the last word"; string(got) != want {
		t.Fatalf("read %q, want %q", got, want)
	}

	// The record is consumed; the writer's close is still in flight.
	w.update(0, []byte{0, byte(len(msg))}) // The NLEN commit.
	commitAt := len(d.writes)
	w.frame(0xb2 | 1 - w.bn) // R(NAK) presence check.
	w.frame(0xc2)            // DESELECT.

	start := time.Now()
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)

	if !d.closed {
		t.Error("the device was not closed")
	}
	if elapsed >= goodbyeTimeout {
		t.Errorf("a DESELECT-terminated close took %v, the full deadline", elapsed)
	}
	var commitACK, deselect bool
	for _, wr := range d.writes[commitAt:] {
		if len(wr) == 3 && wr[0]&^0b1 == 0x02 && wr[1] == 0x90 && wr[2] == 0x00 {
			commitACK = true
		}
		if len(wr) == 1 && wr[0] == 0xc2 {
			deselect = true
		}
	}
	if !commitACK {
		t.Errorf("the NLEN commit was not acknowledged; frames after the record: %x", d.writes[commitAt:])
	}
	if !deselect {
		t.Errorf("the DESELECT was not answered; frames after the record: %x", d.writes[commitAt:])
	}
	if !d.slept {
		t.Error("the DESELECT did not put the tag to sleep")
	}
}

// A writer that walks away mid-conversation holds Close no longer than
// the deadline.
func TestCloseDeadline(t *testing.T) {
	d := newFakeDevice()
	p := New(d)
	w := &writer{d: d}

	msg := record("gone")
	w.frame(0xe0, 0x80)
	w.selectApp()
	w.selectFile()
	w.update(0, []byte{0, 0})
	w.update(2, msg)

	buf := make([]byte, 256)
	for {
		_, err := p.Read(buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}

	start := time.Now()
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if elapsed < goodbyeTimeout {
		t.Errorf("close returned in %v, before the deadline could have passed", elapsed)
	}
	if elapsed > goodbyeTimeout+700*time.Millisecond {
		t.Errorf("close took %v, well past the deadline", elapsed)
	}
	if !d.closed {
		t.Error("the device was not closed")
	}
}

// With no conversation there is nothing to see out.
func TestCloseIdle(t *testing.T) {
	d := newFakeDevice()
	p := New(d)
	start := time.Now()
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("an idle close took %v", elapsed)
	}
	if !d.closed {
		t.Error("the device was not closed")
	}
	if len(d.writes) > 0 {
		t.Errorf("an idle close transmitted %x", d.writes)
	}
}
