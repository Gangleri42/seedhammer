package gui

import (
	"bytes"
	"errors"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"seedhammer.com/bbqr"
	"seedhammer.com/bip380"
	"seedhammer.com/bip39"
	"seedhammer.com/shamir"
)

// scanRecord delivers one NFC record and returns the scanner's verdict
// for it: the loop drains the record's chunks until Scan settles.
func scanRecord(t *testing.T, s *scanner, record string) (any, error) {
	t.Helper()
	buf := new(bytes.Buffer)
	buf.WriteString(record)
	for {
		obj, err := s.Scan(buf)
		if err == errScanInProgress {
			continue
		}
		return obj, err
	}
}

func TestScanBBQrSingle(t *testing.T) {
	m, err := bip39.ParseMnemonic("legal winner thank year wave sausage worth useful legal winner thank yellow")
	if err != nil {
		t.Fatal(err)
	}
	ser, err := bbqr.Split([]byte(m.String()), bbqr.TypeText, bbqr.SplitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	s := new(scanner)
	for _, part := range ser.Parts {
		obj, err := scanRecord(t, s, part)
		if err != nil {
			t.Fatal(err)
		}
		if obj != nil {
			got, ok := obj.(bip39.Mnemonic)
			if !ok {
				t.Fatalf("got %T, want bip39.Mnemonic", obj)
			}
			if !reflect.DeepEqual(got, m) {
				t.Fatal("mnemonic mismatch")
			}
			return
		}
	}
	t.Fatal("series completed without an object")
}

func TestScanBBQrMultiPart(t *testing.T) {
	// Incompressible in the engraveable charset: random letters.
	rng := rand.New(rand.NewSource(9))
	var sb strings.Builder
	for sb.Len() < 1000 {
		sb.WriteByte('A' + byte(rng.Intn(26)))
	}
	text := sb.String()
	ser, err := bbqr.Split([]byte(text), bbqr.TypeText, bbqr.SplitOptions{Encoding: bbqr.EncBase32, MaxVersion: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(ser.Parts) < 3 {
		t.Fatalf("test needs several parts, got %d", len(ser.Parts))
	}
	s := new(scanner)
	// Feed parts out of order: progress must track and the final part
	// must unwrap to the text.
	order := rand.New(rand.NewSource(1)).Perm(len(ser.Parts))
	for i, idx := range order {
		obj, err := scanRecord(t, s, ser.Parts[idx])
		if i < len(order)-1 {
			if !errors.Is(err, errScanProgress) {
				t.Fatalf("part %d: got %v, want progress", i, err)
			}
			if s.detail == "" {
				t.Fatal("no progress detail")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := obj.(plainText); !ok || string(got) != text {
			t.Fatalf("got %v %v, want the text", obj, err)
		}
	}
}

func TestScanBBQrShares(t *testing.T) {
	m, err := bip39.ParseMnemonic("legal winner thank year wave sausage worth useful legal winner thank yellow")
	if err != nil {
		t.Fatal(err)
	}
	series, err := shamir.SplitData(bbqr.TypeText, []byte(m.String()), 2, 3, rand.New(rand.NewSource(2)))
	if err != nil {
		t.Fatal(err)
	}
	s := new(scanner)
	// Feed share 2's series, then share 0's, part by part.
	for _, share := range []int{2, 0} {
		for _, part := range series[share].Parts {
			obj, err := scanRecord(t, s, part)
			if obj == nil {
				if !errors.Is(err, errScanProgress) {
					t.Fatalf("want progress, got %v", err)
				}
				continue
			}
			got, ok := obj.(bip39.Mnemonic)
			if !ok || !reflect.DeepEqual(got, m) {
				t.Fatalf("recovered %v (%T), want the mnemonic", obj, obj)
			}
			return
		}
	}
	t.Fatal("two shares did not recover")
}

// TestScanBBQrSeriesReset: a part from a different series mid-way
// drops the partial one and starts over.
func TestScanBBQrSeriesReset(t *testing.T) {
	textA := strings.Repeat("AAAA ", 199) + "AAAA"
	textB := strings.Repeat("BBBB ", 199) + "BBBB"
	a, err := bbqr.Split([]byte(textA), bbqr.TypeText, bbqr.SplitOptions{Encoding: bbqr.EncBase32, MaxVersion: 5})
	if err != nil {
		t.Fatal(err)
	}
	b, err := bbqr.Split([]byte(textB), bbqr.TypeText, bbqr.SplitOptions{Encoding: bbqr.EncBase32, MaxVersion: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Parts) < 2 || len(b.Parts) < 2 {
		t.Fatal("test needs multi-part series")
	}
	s := new(scanner)
	if _, err := scanRecord(t, s, a.Parts[0]); !errors.Is(err, errScanProgress) {
		t.Fatalf("part of A: %v", err)
	}
	// Switch to B: the partial A is dropped.
	for i, part := range b.Parts {
		obj, err := scanRecord(t, s, part)
		if i < len(b.Parts)-1 {
			if !errors.Is(err, errScanProgress) {
				t.Fatalf("part %d of B: %v", i, err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := obj.(plainText); !ok || string(got) != textB {
			t.Fatalf("got %v, want B's text", obj)
		}
	}
}

// TestScanBBQrNested: a series whose payload is itself a (single part)
// series unwraps twice.
func TestScanBBQrNested(t *testing.T) {
	inner, err := bbqr.Split([]byte("INNER MOST TEXT"), bbqr.TypeText, bbqr.SplitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	outer, err := bbqr.Split([]byte(inner.Parts[0]), bbqr.TypeBinary, bbqr.SplitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	s := new(scanner)
	for _, part := range outer.Parts {
		obj, err := scanRecord(t, s, part)
		if obj == nil {
			if !errors.Is(err, errScanProgress) {
				t.Fatalf("want progress, got %v", err)
			}
			continue
		}
		if got, ok := obj.(plainText); !ok || string(got) != "INNER MOST TEXT" {
			t.Fatalf("got %v, want the nested text", obj)
		}
		return
	}
	t.Fatal("nested series produced no object")
}

// TestScanBBQrTextLookalike: text that opens with "B$" but fails the
// header rules is still plain text.
func TestScanBBQrTextLookalike(t *testing.T) {
	s := new(scanner)
	obj, err := scanRecord(t, s, "B$ NOT A QR AT ALL")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := obj.(plainText); !ok || string(got) != "B$ NOT A QR AT ALL" {
		t.Fatalf("got %v, want plain text", obj)
	}
}

// TestScanBBQrShareDescriptor recovers the machine's own share plates:
// the exact part strings fitShares engraves, fed back through the
// scanner, must land a descriptor object, or the split how-to's
// recovery promise is false.
func TestScanBBQrShareDescriptor(t *testing.T) {
	desc := testMultisig(t, 2, 3)
	_, plans, err := fitShares(engraverParams, desc, nil)
	if err != nil {
		t.Fatal(err)
	}
	sp := plans[0]
	s := new(scanner)
	for _, share := range []int{2, 0} {
		_, qrTexts, err := sp.plateContent(desc, share)
		if err != nil {
			t.Fatal(err)
		}
		for _, part := range qrTexts {
			obj, err := scanRecord(t, s, part)
			if obj == nil {
				if !errors.Is(err, errScanProgress) {
					t.Fatalf("want progress, got %v", err)
				}
				continue
			}
			got, ok := obj.(*bip380.Descriptor)
			if !ok {
				t.Fatalf("recovered %T, want a descriptor", obj)
			}
			if got.Encode() != desc.Encode() {
				t.Fatal("recovered descriptor differs")
			}
			return
		}
	}
	t.Fatal("a quorum of share plates did not recover the descriptor")
}

// TestScanBBQrShareConflict: a corrupted duplicate of a held share
// must be rejected without discarding the set, so a clean rescan
// still completes it.
func TestScanBBQrShareConflict(t *testing.T) {
	m, _ := bip39.ParseMnemonic("legal winner thank year wave sausage worth useful legal winner thank yellow")
	series, err := shamir.SplitData(bbqr.TypeText, []byte(m.String()), 2, 3, rand.New(rand.NewSource(3)))
	if err != nil {
		t.Fatal(err)
	}
	s := new(scanner)
	if _, err := scanRecord(t, s, series[0].Parts[0]); !errors.Is(err, errScanProgress) {
		t.Fatalf("share 1: %v", err)
	}
	// Corrupt share 1's envelope body, re-encoded as a valid part.
	_, payload, err := bbqr.Join(series[0].Parts)
	if err != nil {
		t.Fatal(err)
	}
	payload[len(payload)-1] ^= 0xFF
	bad, err := bbqr.Split(payload, bbqr.TypeShamir, bbqr.SplitOptions{Encoding: bbqr.EncBase32})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scanRecord(t, s, bad.Parts[0]); err == nil || errors.Is(err, errScanProgress) {
		t.Fatalf("conflicting share accepted: %v", err)
	}
	// The held set survived: the second legit share completes it.
	obj, err := scanRecord(t, s, series[1].Parts[0])
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := obj.(bip39.Mnemonic); !ok || !reflect.DeepEqual(got, m) {
		t.Fatalf("recovered %v (%T), want the mnemonic", obj, obj)
	}
}

// TestScanBBQrSeriesTooLarge: a series over the decoded-size cap must
// fail the scan instead of silently resetting and looping forever.
func TestScanBBQrSeriesTooLarge(t *testing.T) {
	big, err := bbqr.Split(make([]byte, 20*1024), bbqr.TypeBinary, bbqr.SplitOptions{Encoding: bbqr.EncBase32, MaxVersion: 20})
	if err != nil {
		t.Fatal(err)
	}
	s := new(scanner)
	sawLimit := false
	for pass := 0; pass < 2 && !sawLimit; pass++ {
		for _, part := range big.Parts {
			_, err := scanRecord(t, s, part)
			if errors.Is(err, bbqr.ErrLimit) {
				sawLimit = true
				break
			}
			if !errors.Is(err, errScanProgress) {
				t.Fatalf("unexpected verdict: %v", err)
			}
		}
	}
	if !sawLimit {
		t.Fatal("oversized series never surfaced the size limit")
	}
}
