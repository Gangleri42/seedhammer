package gui

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"unicode/utf8"

	"seedhammer.com/bbqr"
	"seedhammer.com/bc/urtypes"
	"seedhammer.com/bip380"
	"seedhammer.com/bip39"
	"seedhammer.com/codex32"
	"seedhammer.com/curves"
	"seedhammer.com/font/sh"
	"seedhammer.com/nip19"
	"seedhammer.com/nonstandard"
	"seedhammer.com/shamir"
)

// recordTyper is implemented by NFC readers that surface the type of
// the NDEF record they deliver, such as [poller.Poller].
type recordTyper interface {
	RecordType() []byte
}

type scanner struct {
	buf      []byte
	n        int
	overflow bool

	// Multi-record BBQr assembly: series collects the parts of one
	// series, shares the share envelopes of one split session, and
	// detail is the progress label reported with errScanProgress.
	series bbqr.Decoder
	shares shamir.Set
	detail string
}

const (
	// scanSeriesLimit caps one decoded BBQr payload and the data
	// recovered from a share set; scanShareLimit caps one share
	// envelope, so a split recovery's retained memory is bounded by
	// the share count times this cap. Neither limits the format, only
	// what the machine holds in RAM.
	scanSeriesLimit = 16 * 1024
	scanShareLimit  = 8 * 1024
	// scanSetLimit caps the bytes a share set may retain in total:
	// the threshold announced by a share times its envelope size.
	// Legit sets stay far below it; a hostile threshold byte cannot
	// make the scanner clone envelopes until the heap dies.
	scanSetLimit = 64 * 1024
	// maxUnwrapDepth bounds nesting of a BBQr series inside another's
	// payload.
	maxUnwrapDepth = 8
)

var (
	errScanInProgress    = errors.New("scan: in progress")
	errScanOverflow      = errors.New("scan: buffer overflow")
	errScanUnknownFormat = errors.New("scan: unknown format")
	errScanCompoundNostr = errors.New("scan: nostr compound entity")
	errScanProgress      = errors.New("scan: bbqr part collected")
)

func (s *scanner) Scan(r io.Reader) (any, error) {
	if cap(s.buf) == 0 {
		s.buf = make([]byte, 32*1024)
	}
	nn, err := r.Read(s.buf[s.n:])
	s.n += nn
	s.overflow = s.overflow || s.n == len(s.buf)
	if s.overflow {
		// Discard the rest of the content.
		s.n = 0
		if err != nil {
			// The oversized record's stream has ended (io.EOF) or the
			// poller failed; either way the next record starts clean.
			s.overflow = false
		}
		return nil, errScanOverflow
	}
	switch err {
	case io.EOF:
	case nil:
		// Report progress.
		return nil, errScanInProgress
	default:
		log.Printf("nfc poller: %v", err)
		s.n = 0
		return nil, err
	}

	buf := s.buf[:s.n]
	s.n = 0
	if len(buf) == 0 {
		return nil, nil
	}
	// Typed records dispatch on their NDEF record type; only untyped
	// text goes through the content-sniffing cascade below.
	if rt, ok := r.(recordTyper); ok {
		if bytes.Equal(rt.RecordType(), []byte(curves.RecordType)) {
			return curvesPayload(bytes.Clone(buf)), nil
		}
	}
	// The provisioning channel inherited from upstream, dispatched ahead
	// of every parser and deliberately ungated: see the commands in
	// gui.go. On a provisioned board lock-boot is a no-op plus a reboot,
	// because each of its three steps early-returns on an already-set
	// value. It stays out of sniff so a BBQr payload cannot reach it.
	const cmdPrefix = "command: "
	if bytes.HasPrefix(buf, []byte(cmdPrefix)) {
		cmd := debugCommand{string(buf[len(cmdPrefix):])}
		return cmd, nil
	}
	return s.sniff(buf, 0)
}

// sniff runs the content cascade on a complete payload, whether
// scanned directly or unwrapped from a BBQr series by scanPayload.
// depth bounds BBQr nesting.
func (s *scanner) sniff(buf []byte, depth int) (any, error) {
	if depth < maxUnwrapDepth {
		if _, err := bbqr.ParseHeader(string(buf)); err == nil {
			return s.scanPart(string(buf), depth)
		}
	}
	return sniffContent(buf)
}

// scanPart feeds one BBQr part into the series accumulator. A complete
// series is unwrapped by scanPayload; anything else reports progress.
func (s *scanner) scanPart(part string, depth int) (any, error) {
	if s.series.Limit == 0 {
		s.series.Limit = scanSeriesLimit
	}
	if err := s.series.Add(part); err != nil {
		if errors.Is(err, bbqr.ErrLimit) {
			// No rescan can shrink the series; retrying into a fresh
			// decoder would loop forever, resetting progress each
			// round.
			s.series = bbqr.Decoder{Limit: scanSeriesLimit}
			return nil, err
		}
		// A part that does not fit the series being collected starts
		// a new one; the partial series is dropped. Equal-shape series
		// carry identical headers, so a conflicting duplicate is
		// indistinguishable from a switch to a new series and resets
		// too: a corrupt record costs one extra delivery pass instead
		// of wedging the scan.
		s.series = bbqr.Decoder{Limit: scanSeriesLimit}
		if err := s.series.Add(part); err != nil {
			return nil, err
		}
	}
	if !s.series.Complete() {
		have, total := s.series.Progress()
		s.detail = fmt.Sprintf("PART %d OF %d", have, total)
		return nil, errScanProgress
	}
	typ, payload, err := s.series.Result()
	s.series = bbqr.Decoder{}
	if err != nil {
		return nil, err
	}
	return s.scanPayload(typ, payload, depth)
}

// scanPayload unwraps a complete BBQr payload: a type M series joins
// the share set, any other payload is sniffed as if scanned directly.
// The dispatch is by file type alone, so a malformed share errors
// instead of falling through to the sniff as data.
func (s *scanner) scanPayload(typ byte, payload []byte, depth int) (any, error) {
	if typ == bbqr.TypeShamir {
		if s.shares.Limit == 0 {
			s.shares.Limit = scanSeriesLimit
		}
		if len(payload) > scanShareLimit {
			return nil, fmt.Errorf("scan: share exceeds %d byte limit", scanShareLimit)
		}
		if sh, err := shamir.ParseShare(payload); err != nil {
			return nil, err
		} else if sh.Threshold*len(payload) > scanSetLimit {
			return nil, fmt.Errorf("scan: share set exceeds %d byte limit", scanSetLimit)
		}
		if err := s.shares.Add(payload); err != nil {
			if !errors.Is(err, shamir.ErrForeignShare) {
				// A conflicting or corrupt record: the collected
				// shares stay valid, and a clean rescan still
				// completes the set.
				return nil, err
			}
			// A share of another split starts a new set; the
			// partial set is dropped.
			s.shares = shamir.Set{}
			if err := s.shares.Add(payload); err != nil {
				return nil, err
			}
		}
		if !s.shares.Complete() {
			have, need := s.shares.Progress()
			s.detail = fmt.Sprintf("SHARE %d OF %d", have, need)
			return nil, errScanProgress
		}
		rec, err := s.shares.Recover()
		s.shares = shamir.Set{}
		if err != nil {
			return nil, err
		}
		return s.sniff(rec.Data, depth+1)
	}
	return s.sniff(payload, depth+1)
}

// sniffContent is the content-sniffing cascade for a complete payload.
func sniffContent(buf []byte) (any, error) {
	if m, err := bip39.Parse(buf); err == nil {
		return m, nil
		// Scanning a SLIP39 share stays disabled by choice. Parsing one
		// (rs1024 checksum + share decode) means github.com/gavincarr/
		// go-slip39, which pulls three external modules against this
		// project's no-dependencies rule (the in-tree seedhammer.com/
		// slip39 package is only the keyboard wordlist, not a parser).
		// The keyboard SLIP-39 entry flow is likewise gated off in
		// newInputFlow. Revisit only if a dep-free share parser lands.
	} else if d, err := nonstandard.OutputDescriptor(buf); err == nil {
		return d, nil
	} else if d, err := urtypes.Parse("crypto-output", buf); err == nil {
		// The raw CBOR the descriptor share split seals; without
		// this arm the machine could not read its own share plates
		// back.
		if desc, ok := d.(*bip380.Descriptor); ok {
			return desc, nil
		}
		return nil, errScanUnknownFormat
	} else if s, err := codex32.New(string(buf)); err == nil {
		return s, nil
	} else if k, err := nip19.ParseKey(string(bytes.TrimSpace(buf))); err == nil {
		return k, nil
	} else if nip19.IsCompound(string(bytes.TrimSpace(buf))) {
		return nil, errScanCompoundNostr
	} else if t, ok := parsePlainText(buf); ok {
		// Intentionally kept in this fork: the untyped Text fallback and
		// curves ModeText converge on the single parsePlainText + textFlow
		// funnel, so there is one text screen and one canonicalizer — this
		// branch is the bench's cheap input channel (write-nfc.py, any
		// phone NFC app) and removing it would strictly reduce input
		// options for zero code payoff. Retire only in an
		// upstream-submission patch series. Must stay below the nip19
		// branches so nsec1/npub1 recognition wins over free text.
		return t, nil
	} else {
		return nil, errScanUnknownFormat
	}
}

type debugCommand struct {
	Command string
}

// plainText is a free-form text payload destined for a text plate.
type plainText string

// parsePlainText accepts payloads whose runes can all be engraved with
// the plate font, with '\n' separating lines. Accepted text is
// canonicalized: CRLF and CR become '\n', trailing spaces are stripped
// from every line and trailing blank lines are dropped. Payloads
// without at least one visible character are rejected.
func parsePlainText(buf []byte) (plainText, bool) {
	visible := false
	for i := 0; i < len(buf); {
		r, n := utf8.DecodeRune(buf[i:])
		i += n
		if r == '\n' || r == '\r' {
			continue
		}
		if _, _, ok := sh.Font.Decode(r); !ok {
			return "", false
		}
		visible = visible || r != ' '
	}
	if !visible {
		return "", false
	}
	// The accepted charset is ASCII; canonicalize bytewise.
	out := make([]byte, 0, len(buf))
	for i := 0; i < len(buf); i++ {
		c := buf[i]
		if c == '\r' {
			if i+1 < len(buf) && buf[i+1] == '\n' {
				continue
			}
			c = '\n'
		}
		if c == '\n' {
			for len(out) > 0 && out[len(out)-1] == ' ' {
				out = out[:len(out)-1]
			}
		}
		out = append(out, c)
	}
	for len(out) > 0 && (out[len(out)-1] == ' ' || out[len(out)-1] == '\n') {
		out = out[:len(out)-1]
	}
	return plainText(out), true
}
