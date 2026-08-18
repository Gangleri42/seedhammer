// Package poller implements a NFC device poller for accepting
// data from either tags or writers.
package poller

import (
	"bufio"
	"io"
	"time"

	"seedhammer.com/nfc/ndef"
	"seedhammer.com/nfc/type2"
	"seedhammer.com/nfc/type4"
	"seedhammer.com/nfc/type5"
)

type Device interface {
	Close() error
	Interrupt()
	Detect() (bool, error)
	SetProtocol(prot Protocol) error
	Sleep() error
	ReadCapacity() int
	io.ReadWriter
}

type Poller struct {
	d       Device
	bufr    *bufio.Reader
	emu     *type4.Tag
	reading chan struct{}
	// r is the active reader.
	r *ndef.RecordReader
}

type Protocol int

const (
	ISO14443a Protocol = iota
	ISO15693
)

func New(d Device) *Poller {
	return &Poller{
		d:       d,
		bufr:    bufio.NewReaderSize(nil, 256),
		emu:     type4.NewTag(d),
		reading: make(chan struct{}, 1),
	}
}

func (p *Poller) Read(buf []byte) (int, error) {
	p.reading <- struct{}{}
	defer func() {
		<-p.reading
	}()
	for {
		if p.r != nil {
			n, err := p.r.Read(buf)
			if err != nil {
				if err != io.EOF || n == 0 {
					p.r = nil
				}
			}
			return n, err
		}
		active, err := p.d.Detect()
		if err != nil {
			return 0, err
		}
		var r io.Reader
		if active {
			// Reset the tag emulator when the
			// external field is off.
			p.emu.Reset()

			r, err = p.poll()
			if err != nil {
				return 0, err
			}
			if r == nil {
				continue
			}
			p.bufr.Reset(r)
			r = ndef.NewMessageReader(p.bufr)
		} else {
			p.bufr.Reset(p.emu)
			r = p.bufr
		}
		p.r = ndef.NewRecordReader(r)
	}
}

// RecordType returns the type of the NDEF record currently being
// read, if any. It must only be called from the same goroutine as
// Read.
func (p *Poller) RecordType() []byte {
	if p.r == nil {
		return nil
	}
	return p.r.RecordType()
}

// goodbyeTimeout bounds how long Close keeps the transport alive for a
// writer's trailing frames. A record ends at its declared length, one
// frame before the writer's closing NLEN commit, so the commit is
// still in flight when the flow that consumed the record returns and
// closes the poller; switching off then leaves it unacknowledged and
// the writer reports the delivered write as failed. The commit follows
// within a few milliseconds and the DESELECT shortly after; the
// deadline only bounds a writer that walks away without one.
const goodbyeTimeout = 300 * time.Millisecond

func (p *Poller) Close() error {
	select {
	case p.reading <- struct{}{}:
	default:
		p.d.Interrupt()
		p.reading <- struct{}{}
	}
	p.goodbye()
	err := p.d.Close()
	// Release the token: a scan goroutine that raced past its stop
	// signal into Read blocks on the token, and holding it here would
	// park that goroutine forever with its stop waiting on the join.
	// The device is closed, so the read fails and the loop exits.
	<-p.reading
	return err
}

// goodbye sees a writer's conversation out before the device switches
// off: it keeps servicing the tag emulator, discarding data, until the
// writer ends with DESELECT, the transport reports the field gone, or
// the deadline passes. Frames go through the emulator alone, never the
// record layer, so the next session's framing sees none of this.
func (p *Poller) goodbye() {
	if p.emu.Idle() {
		return
	}
	deadline := time.Now().Add(goodbyeTimeout)
	// The driver's own read timeouts stretch to seconds; the timer
	// interrupts a wait that would outlive the deadline.
	t := time.AfterFunc(goodbyeTimeout, p.d.Interrupt)
	defer t.Stop()
	var discard [128]byte
	for time.Now().Before(deadline) {
		_, err := p.emu.Read(discard[:])
		switch {
		case err == nil:
		case err == io.EOF && !p.emu.Idle():
			// A completed write was acknowledged mid-conversation,
			// or the driver reported the field down. The writer may
			// still have its DESELECT to send: keep listening.
		default:
			// DESELECT (the emulator is idle again), or a transport
			// error. Either way the conversation is over.
			return
		}
	}
}

// poll attempts to select a tag, trying each protocol in turn.
func (p *Poller) poll() (io.Reader, error) {
	if err := p.d.SetProtocol(ISO15693); err != nil {
		return nil, err
	}
	tag15693, err := type5.NewReader(p.d, p.d.ReadCapacity())
	if err == nil {
		return tag15693, nil
	}
	if err := p.d.SetProtocol(ISO14443a); err != nil {
		return nil, err
	}
	tag14443, err := type2.NewReader(p.d)
	if err != nil {
		// Ignore read errors.
		return nil, nil
	}
	return tag14443, nil
}
