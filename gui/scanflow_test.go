package gui

import (
	"errors"
	"fmt"
	"io"
	"testing"

	"seedhammer.com/bip380"
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

// cosignerEntryHarness drives the cosigner landing page with an
// injected reader.
func cosignerEntryHarness(t *testing.T, script func(ctx *Context, nfc *testNFC, await func(string))) (cosignerEntryAction, bool) {
	t.Helper()
	p := newPlatform()
	nfc := newTestNFC()
	p.nfc = nfc
	ctx := NewContext(p)
	var got cosignerEntryAction
	var ok bool
	frame, quit := runUI(ctx, func() {
		got, ok = cosignerEntry(ctx, &descriptorTheme, "Cosigner 1 of 3")
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
	t.Fatal("cosignerEntry did not end")
	return got, ok
}

// The landing page listens while it asks: a decodable payload of the
// wrong kind shows the reject line and keeps listening, a seed ends
// the page.
func TestCosignerEntrySeedTap(t *testing.T) {
	got, ok := cosignerEntryHarness(t, func(ctx *Context, nfc *testNFC, await func(string)) {
		await("Enter the seed words")
		nfc.payloads <- []byte("hello plate")
		await("Not a seed or cosigner key")
		nfc.payloads <- []byte(goldenSeedOil)
	})
	if !ok {
		t.Fatal("the landing page rejected the seed")
	}
	m, is := got.scan.(bip39.Mnemonic)
	if !is || len(m) != 12 {
		t.Fatalf("landing page returned %T, want a 12-word mnemonic", got.scan)
	}
}

func TestCosignerEntryKeyTap(t *testing.T) {
	golden, err := cosignerKey(goldenMnemonic(t, goldenSeedOil), "")
	if err != nil {
		t.Fatal(err)
	}
	expr := fmt.Sprintf("[%.8x%s]%s/<0;1>/*",
		golden.MasterFingerprint, golden.DerivationPath.Encode(), golden.String())
	got, ok := cosignerEntryHarness(t, func(ctx *Context, nfc *testNFC, await func(string)) {
		await("Enter the seed words")
		nfc.payloads <- []byte(expr)
	})
	if !ok {
		t.Fatal("the landing page rejected the key expression")
	}
	key, is := got.scan.(bip380.Key)
	if !is {
		t.Fatalf("landing page returned %T, want a key", got.scan)
	}
	if key.MasterFingerprint != golden.MasterFingerprint {
		t.Errorf("fingerprint %.8x, want %.8x", key.MasterFingerprint, golden.MasterFingerprint)
	}
}

func TestCosignerEntryWords(t *testing.T) {
	got, ok := cosignerEntryHarness(t, func(ctx *Context, nfc *testNFC, await func(string)) {
		await("Enter the seed words")
		click(&ctx.Router, Down)
		await("24 WORDS")
		click(&ctx.Router, Button3)
	})
	if !ok || got.words != 24 || got.scan != nil {
		t.Fatalf("choosing 24 WORDS returned %+v, %v", got, ok)
	}
}

func TestCosignerEntryBack(t *testing.T) {
	_, ok := cosignerEntryHarness(t, func(ctx *Context, nfc *testNFC, await func(string)) {
		await("Enter the seed words")
		click(&ctx.Router, Button1)
	})
	if ok {
		t.Fatal("backing out reported an action")
	}
}
