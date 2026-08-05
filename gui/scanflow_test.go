package gui

import (
	"errors"
	"io"
	"testing"

	"seedhammer.com/bip39"
)

// testNFC feeds queued payloads to the scan worker, one NDEF record
// per queue entry: the whole payload returns in a single Read with
// io.EOF marking the record end, the shape the poller presents. The
// platform hands out a fresh poller per NFCReader call, so the queue
// outlives any one connection; Close ends only its own connection,
// and it must unblock a pending Read (a no-op Close deadlocks the
// flow's cleanup).
type testNFC struct {
	payloads chan []byte
}

func newTestNFC() *testNFC {
	return &testNFC{
		payloads: make(chan []byte, 4),
	}
}

func (r *testNFC) conn() io.ReadCloser {
	return &testNFCConn{src: r, done: make(chan struct{})}
}

type testNFCConn struct {
	src  *testNFC
	done chan struct{}
}

var errNFCClosed = errors.New("testNFC: closed")

func (r *testNFCConn) Read(p []byte) (int, error) {
	// A close outranks a queued payload: a payload sent for the NEXT
	// connection must not race into a closing one.
	select {
	case <-r.done:
		return 0, errNFCClosed
	default:
	}
	select {
	case payload := <-r.src.payloads:
		if len(payload) > len(p) {
			panic("testNFC: payload larger than the scan buffer")
		}
		n := copy(p, payload)
		return n, io.EOF
	case <-r.done:
		return 0, errNFCClosed
	}
}

func (r *testNFCConn) Close() error {
	select {
	case <-r.done:
	default:
		close(r.done)
	}
	return nil
}

// scanFlowHarness drives scanFlow with an injected reader.
func scanFlowHarness(t *testing.T, accept func(any) bool, script func(ctx *Context, nfc *testNFC, await func(string))) (any, bool) {
	t.Helper()
	p := newPlatform()
	nfc := newTestNFC()
	p.nfc = nfc
	ctx := NewContext(p)
	var got any
	var ok bool
	frame, quit := runUI(ctx, func() {
		got, ok = scanFlow(ctx, &descriptorTheme, "Cosigner 1 of 2", "Tap the cosigner's seed", "Not a seed phrase", accept)
	})
	defer quit()
	script(ctx, nfc, func(marker string) {
		t.Helper()
		awaitUI(t, frame, marker)
	})
	for range 10000 {
		if _, more := frame(); !more {
			return got, ok
		}
	}
	t.Fatal("scanFlow did not end")
	return got, ok
}

func TestScanFlowAcceptsSeed(t *testing.T) {
	acceptSeed := func(obj any) bool {
		_, is := obj.(bip39.Mnemonic)
		return is
	}
	got, ok := scanFlowHarness(t, acceptSeed, func(ctx *Context, nfc *testNFC, await func(string)) {
		await("Tap the cosigner's seed")
		// A text payload is decodable but not what this step takes:
		// the flow must reject it and keep listening.
		nfc.payloads <- []byte("hello plate")
		await("Not a seed phrase")
		nfc.payloads <- []byte("oil oil oil oil oil oil oil oil oil oil oil oil")
	})
	if !ok {
		t.Fatal("scanFlow rejected the seed")
	}
	m, is := got.(bip39.Mnemonic)
	if !is || len(m) != 12 {
		t.Fatalf("scanFlow returned %T, want a 12-word mnemonic", got)
	}
}

func TestScanFlowBack(t *testing.T) {
	_, ok := scanFlowHarness(t, func(any) bool { return true }, func(ctx *Context, nfc *testNFC, await func(string)) {
		await("Tap the cosigner's seed")
		click(&ctx.Router, Button1)
	})
	if ok {
		t.Fatal("backing out reported a payload")
	}
}
