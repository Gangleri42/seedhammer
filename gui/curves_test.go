package gui

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"io"
	"strings"
	"testing"

	"math"
	"seedhammer.com/curves"
	"seedhammer.com/svgpath"
)

// curvesTestPayload is a payload in 100 units per mm, engravable
// with the test params (0.3mm stroke = 30 units).
func curvesTestPayload(path string) []byte {
	segs, err := svgpath.ParseData(path, 0, 0, func(v float64) int { return int(math.Round(v)) })
	if err != nil {
		panic(err)
	}
	payload, err := curves.EncodePath(100, 30, segs)
	if err != nil {
		panic(err)
	}
	return payload
}

func TestScanCurvesRecord(t *testing.T) {
	// A curves-typed record is returned raw, even when its content
	// would pass the text cascade.
	const body = "IN CASE OF FIRE"
	r := &typedReader{Reader: strings.NewReader(body), typ: curves.RecordType}
	s := new(scanner)
	for {
		got, err := s.Scan(r)
		if err == errScanInProgress {
			continue
		}
		if err != nil {
			t.Fatalf("scanner failed: %v", err)
		}
		p, ok := got.(curvesPayload)
		if !ok {
			t.Fatalf("scanner decoded %#v, expected a curvesPayload", got)
		}
		if string(p) != body {
			t.Fatalf("payload %q, expected %q", p, body)
		}
		break
	}
	// Other record types fall through to the cascade.
	r = &typedReader{Reader: strings.NewReader(body), typ: "example.com:other"}
	s = new(scanner)
	for {
		got, err := s.Scan(r)
		if err == errScanInProgress {
			continue
		}
		if err != nil {
			t.Fatalf("scanner failed: %v", err)
		}
		if want := plainText(body); got != want {
			t.Fatalf("scanner decoded %#v, expected %#v", got, want)
		}
		break
	}
}

type typedReader struct {
	io.Reader
	typ string
}

func (r *typedReader) RecordType() []byte {
	return []byte(r.typ)
}

func TestCurvesTextModeMatchesTextRecord(t *testing.T) {
	// A text-mode curves payload must produce the same plate as the
	// text-record path: same canonicalization, same layout, same
	// engraving. This is the invariant the A/B bench checks.
	const body = "IN CASE OF FIRE  \n\nBREAK GLASS"
	payload := []byte("2 text\n" + body)

	mode, err := curves.Mode(payload)
	if err != nil || mode != curves.ModeText {
		t.Fatalf("Mode = %q, %v; want text", mode, err)
	}
	text, err := curves.Text(payload)
	if err != nil {
		t.Fatal(err)
	}

	viaCurves, ok := parsePlainText([]byte(text))
	if !ok {
		t.Fatal("parsePlainText rejected the text-mode body")
	}
	viaRecord, ok := parsePlainText([]byte(body))
	if !ok {
		t.Fatal("parsePlainText rejected the record body")
	}
	if viaCurves != viaRecord {
		t.Fatalf("text mode canonicalized to %q, record to %q", viaCurves, viaRecord)
	}

	pc, err := validateText(engraverParams, string(viaCurves))
	if err != nil {
		t.Fatal(err)
	}
	pr, err := validateText(engraverParams, string(viaRecord))
	if err != nil {
		t.Fatal(err)
	}
	if pc.Duration != pr.Duration {
		t.Errorf("plate duration differs: curves %d vs record %d", pc.Duration, pr.Duration)
	}
}

func TestValidateCurves(t *testing.T) {
	dims := image.Pt(480, 320)
	cs := new(CurvesScreen)
	payload := curvesTestPayload("M 1000 1000 L 4000 1000 L 4000 4000 C 4000 5000 1000 5000 1000 4000 Z")
	plate, err := validateCurves(cs, payload, engraverParams, dims, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plate.Duration == 0 {
		t.Error("validated plate has no duration")
	}
	if cs.preview == nil || cs.info == "" {
		t.Errorf("confirm screen not initialized: preview %v, info %q", cs.preview, cs.info)
	}
	set := 0
	side := cs.preview.sz.X
	// The drawing spans 10-50mm; every lit pixel must fall inside,
	// with a pixel of slack for rounding.
	lo, hi := 10*side/85-1, 50*side/85+1
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			if cs.preview.alpha(x, y) == 0 {
				continue
			}
			set++
			if x < lo || x > hi || y < lo || y > hi {
				t.Fatalf("preview pixel (%d, %d) outside the drawing bounds", x, y)
			}
		}
	}
	if set == 0 {
		t.Error("preview is empty")
	}
}

func TestValidateCurvesPump(t *testing.T) {
	dims := image.Pt(480, 320)
	payload := curvesTestPayload("M 1000 1000 L 4000 1000 L 4000 4000 C 4000 5000 1000 5000 1000 4000 Z")
	cs := new(CurvesScreen)
	calls, lastDone, total := 0, -1, 0
	plate, err := validateCurves(cs, payload, engraverParams, dims, func(done, tot int) bool {
		calls++
		if done < lastDone {
			t.Fatalf("pump went backwards: %d after %d", done, lastDone)
		}
		lastDone, total = done, tot
		if cs.preview == nil {
			t.Fatal("preview not shared with the screen during the walk")
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls == 0 || total == 0 || lastDone > total {
		t.Fatalf("pump: %d calls, final %d/%d", calls, lastDone, total)
	}
	// The single-walk duration must match the two-walk gauge.
	d, err := curves.Parse(payload, engraverParams)
	if err != nil {
		t.Fatal(err)
	}
	report, err := d.Validate(engraverParams)
	if err != nil {
		t.Fatal(err)
	}
	if plate.Duration != report.DurationTicks {
		t.Errorf("fused duration %d, Validate gauges %d", plate.Duration, report.DurationTicks)
	}

	// A pump returning false abandons the scan.
	if _, err := validateCurves(new(CurvesScreen), payload, engraverParams, dims, func(done, tot int) bool {
		return false
	}); !errors.Is(err, curves.ErrCanceled) {
		t.Fatalf("cancelled scan: %v, want curves.ErrCanceled", err)
	}
}

func TestValidateCurvesErrors(t *testing.T) {
	dims := image.Pt(480, 320)

	// Inside the 3mm safety margin.
	payload := curvesTestPayload("M 100 100 L 4000 4000")
	if _, err := validateCurves(new(CurvesScreen), payload, engraverParams, dims, nil); !errors.Is(err, ErrTooLarge) {
		t.Errorf("margin violation: %v, expected ErrTooLarge", err)
	}

	// Malformed payloads surface their parse error.
	if _, err := validateCurves(new(CurvesScreen), []byte("2 100 30\nM 0 0 L 1 1"), engraverParams, dims, nil); err == nil {
		t.Error("unsupported version passed validation")
	}

	// Duration cap — the one complexity gate since the structural
	// caps retired: 450 exact-line strokes of 75mm run far over it.
	var b strings.Builder
	for i := 0; i < 450; i++ {
		y := 500 + i*16
		fmt.Fprintf(&b, "M 500 %d L 8000 %d ", y, y)
	}
	if _, err := validateCurves(new(CurvesScreen), curvesTestPayload(b.String()), engraverParams, dims, nil); err == nil || !strings.Contains(err.Error(), "minutes") {
		t.Errorf("duration cap: %v, expected a duration error", err)
	}
}

func TestScanOverflowRecovers(t *testing.T) {
	// A record larger than the buffer overflows, but the scanner must
	// accept the next record instead of latching on the overflow flag.
	s := new(scanner)
	big := bytes.NewReader(bytes.Repeat([]byte{'A'}, 40*1024))
	overflowed := false
	for {
		// Drain the oversized record to its end, as the poller feeds
		// one record until io.EOF.
		_, err := s.Scan(big)
		if errors.Is(err, errScanOverflow) {
			overflowed = true
			continue
		}
		if err == errScanInProgress {
			continue
		}
		break
	}
	if !overflowed {
		t.Fatal("oversized record did not overflow")
	}
	small := strings.NewReader("IN CASE OF FIRE")
	for {
		got, err := s.Scan(small)
		if err == errScanInProgress {
			continue
		}
		if err != nil {
			t.Fatalf("scan after overflow: %v", err)
		}
		if want := plainText("IN CASE OF FIRE"); got != want {
			t.Fatalf("scan after overflow decoded %#v, want %#v", got, want)
		}
		break
	}
}

func TestScanBufferFitsNDEFCap(t *testing.T) {
	// The scan buffer must hold any record the type4 tag advertises
	// (32768 bytes), or phone writes would overflow.
	s := new(scanner)
	payload := bytes.Repeat([]byte{'A'}, 32*1024-1)
	r := &typedReader{Reader: bytes.NewReader(payload), typ: curves.RecordType}
	for {
		got, err := s.Scan(r)
		if err == errScanInProgress {
			continue
		}
		if err != nil {
			t.Fatalf("scanner failed: %v", err)
		}
		if p := got.(curvesPayload); len(p) != len(payload) {
			t.Fatalf("payload truncated to %d bytes, expected %d", len(p), len(payload))
		}
		break
	}
}
