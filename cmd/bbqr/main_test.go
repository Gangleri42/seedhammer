package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"seedhammer.com/bbqr"
	"seedhammer.com/bc/urtypes"
	"seedhammer.com/bip380"
	"seedhammer.com/shamir"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	data := []byte("the quick brown fox jumps over the lazy dog")
	var parts, stderr bytes.Buffer
	if err := run(strings.NewReader(string(data)), &parts, &stderr, []string{"encode", "-type", "U"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	stderr.Reset()
	if err := run(&parts, &out, &stderr, []string{"decode"}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), data) {
		t.Fatalf("round trip mismatch: %q", out.Bytes())
	}
}

func TestSplitCombineRoundTrip(t *testing.T) {
	data := []byte("abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about")
	var parts bytes.Buffer
	if err := run(strings.NewReader(string(data)), &parts, &bytes.Buffer{}, []string{"split", "-k", "2", "-n", "4"}); err != nil {
		t.Fatal(err)
	}
	groups := strings.Split(strings.TrimSpace(parts.String()), "\n\n")
	if len(groups) != 4 {
		t.Fatalf("split produced %d series, want 4", len(groups))
	}
	// Any 2 of the 4 shares recover.
	for _, pick := range [][2]int{{0, 1}, {0, 3}, {1, 2}, {2, 3}} {
		in := groups[pick[0]] + "\n\n" + groups[pick[1]] + "\n"
		var out, stderr bytes.Buffer
		if err := run(strings.NewReader(in), &out, &stderr, []string{"combine"}); err != nil {
			t.Fatalf("combine shares %v: %v", pick, err)
		}
		if !bytes.Equal(out.Bytes(), data) {
			t.Fatalf("combine shares %v: mismatch", pick)
		}
	}
}

func TestCombineIncomplete(t *testing.T) {
	data := []byte("some secret material for the test")
	var parts bytes.Buffer
	if err := run(strings.NewReader(string(data)), &parts, &bytes.Buffer{}, []string{"split", "-k", "3", "-n", "5"}); err != nil {
		t.Fatal(err)
	}
	groups := strings.Split(strings.TrimSpace(parts.String()), "\n\n")
	in := groups[0] + "\n\n" + groups[1] + "\n"
	var out bytes.Buffer
	if err := run(strings.NewReader(in), &out, &bytes.Buffer{}, []string{"combine"}); err == nil {
		t.Fatal("combine with 2 of 3 required shares: expected error")
	}
}

// TestCombineCorruptWarning: with spares held, combine recovers past
// corrupt shares and names every one of them on stderr, so the holder
// knows which plates to re-cut.
func TestCombineCorruptWarning(t *testing.T) {
	data := []byte("some secret material for the test")
	var parts bytes.Buffer
	if err := run(strings.NewReader(string(data)), &parts, &bytes.Buffer{}, []string{"split", "-k", "3", "-n", "5"}); err != nil {
		t.Fatal(err)
	}
	groups := strings.Split(strings.TrimSpace(parts.String()), "\n\n")
	// corrupt flips one share byte of series i, a different byte per
	// series so that two corrupt shares cannot cancel each other, and
	// re-encodes it as valid parts, the way a mis-cut plate would
	// still scan.
	corrupt := func(group string, i int) string {
		_, payload, err := bbqr.Join(strings.Split(group, "\n"))
		if err != nil {
			t.Fatal(err)
		}
		payload[len(payload)-1-i] ^= 0xFF
		s, err := bbqr.Split(payload, bbqr.TypeShamir, bbqr.SplitOptions{Encoding: bbqr.EncBase32})
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(s.Parts, "\n")
	}
	for _, tc := range []struct {
		bad  []int // 0-based series positions; share index is position+1
		want string
	}{
		{[]int{3}, "warning: share 4 is corrupt; recovered from the others\n"},
		{[]int{0, 3}, "warning: shares 1, 4 are corrupt; recovered from the others\n"},
	} {
		held := make([]string, len(groups))
		for i, g := range groups {
			held[i] = g
			if slices.Contains(tc.bad, i) {
				held[i] = corrupt(g, i)
			}
		}
		in := strings.Join(held, "\n\n") + "\n"
		var out, stderr bytes.Buffer
		if err := run(strings.NewReader(in), &out, &stderr, []string{"combine"}); err != nil {
			t.Fatalf("combine with corrupt %v: %v", tc.bad, err)
		}
		if !bytes.Equal(out.Bytes(), data) {
			t.Fatalf("combine with corrupt %v: mismatch", tc.bad)
		}
		if !strings.HasSuffix(stderr.String(), tc.want) {
			t.Fatalf("combine with corrupt %v: stderr %q, want it to end with %q", tc.bad, stderr.String(), tc.want)
		}
	}
}

// TestCombineAmbiguous: two corrupt shares whose errors cancel in the
// first combination leave two readings tied when each has three
// shares behind it; combine reports that plainly instead of naming
// shares it cannot tell apart.
func TestCombineAmbiguous(t *testing.T) {
	data := []byte("some secret material for the test")
	var parts bytes.Buffer
	if err := run(strings.NewReader(string(data)), &parts, &bytes.Buffer{}, []string{"split", "-k", "3", "-n", "6"}); err != nil {
		t.Fatal(err)
	}
	groups := strings.Split(strings.TrimSpace(parts.String()), "\n\n")
	payloads := make([][]byte, len(groups))
	for i, g := range groups {
		_, payload, err := bbqr.Join(strings.Split(g, "\n"))
		if err != nil {
			t.Fatal(err)
		}
		payloads[i] = payload
	}
	// Errors e1 on share 1 and e2 on share 2 in one payload byte,
	// with e2 found by brute force so that the combination of shares
	// 1, 2, 3 still yields the true sealed content.
	const pos = 4 + 3
	clean, err := shamir.Combine(rawPoints(payloads[:3]))
	if err != nil {
		t.Fatal(err)
	}
	payloads[0][pos] ^= 0x5a
	found := false
	for e2 := 1; e2 < 256 && !found; e2++ {
		payloads[1][pos] ^= byte(e2)
		wrong, err := shamir.Combine(rawPoints(payloads[:3]))
		if err != nil {
			t.Fatal(err)
		}
		found = bytes.Equal(wrong, clean)
		if !found {
			payloads[1][pos] ^= byte(e2)
		}
	}
	if !found {
		t.Fatal("no cancelling error found")
	}
	held := make([]string, 0, 5)
	for _, p := range payloads[:5] {
		s, err := bbqr.Split(p, bbqr.TypeShamir, bbqr.SplitOptions{Encoding: bbqr.EncBase32})
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, strings.Join(s.Parts, "\n"))
	}
	var out, stderr bytes.Buffer
	err = run(strings.NewReader(strings.Join(held, "\n\n")+"\n"), &out, &stderr, []string{"combine"})
	if err == nil || !strings.HasPrefix(err.Error(), "cannot tell which shares are corrupt: ") {
		t.Fatalf("combine with a tie: got %v, want the ambiguity report", err)
	}
	if out.Len() != 0 {
		t.Fatalf("combine with a tie wrote %d bytes", out.Len())
	}
}

// rawPoints returns the envelopes in the (x, y values) form Combine
// takes.
func rawPoints(envelopes [][]byte) [][]byte {
	raw := make([][]byte, len(envelopes))
	for i, env := range envelopes {
		raw[i] = append([]byte{env[2]}, env[4:]...)
	}
	return raw
}

func TestDecodeShareHint(t *testing.T) {
	var parts bytes.Buffer
	if err := run(strings.NewReader("hint me"), &parts, &bytes.Buffer{}, []string{"split", "-k", "2", "-n", "2"}); err != nil {
		t.Fatal(err)
	}
	group := strings.SplitN(strings.TrimSpace(parts.String()), "\n\n", 2)[0] + "\n"
	var out, stderr bytes.Buffer
	if err := run(strings.NewReader(group), &out, &stderr, []string{"decode"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "shamir share") {
		t.Fatalf("expected share hint, got %q", stderr.String())
	}
}

func TestUsageError(t *testing.T) {
	if err := run(nil, nil, &bytes.Buffer{}, nil); err != errUsage {
		t.Fatalf("got %v, want errUsage", err)
	}
	if err := run(nil, nil, &bytes.Buffer{}, []string{"bogus"}); err == nil {
		t.Fatal("unknown command: expected error")
	}
}

// TestCombineDescriptor: the machine's share plates seal the
// descriptor's crypto-output CBOR; -descriptor prints it back as
// descriptor text, the form a wallet loads.
func TestCombineDescriptor(t *testing.T) {
	const want = "wsh(sortedmulti(2,xpub661MyMwAqRbcFtXgS5sYJABqqG9YLmC4Q1Rdap9gSE8NqtwybGhePY2gZ29ESFjqJoCu1Rupje8YtGqsefD265TMg7usUDFdp6W1EGMcet8,xpub661MyMwAqRbcFW31YEwpkMuc5THy2PSt5bDMsktWQcFF8syAmRUapSCGu8ED9W6oDMSgv6Zz8idoc4a6mr8BDzTJY47LJhkJ8UB7WEGuduB/0/*))"
	desc, err := bip380.Parse(want)
	if err != nil {
		t.Fatal(err)
	}
	cbor := urtypes.EncodeDescriptor(desc)
	var parts bytes.Buffer
	if err := run(bytes.NewReader(cbor), &parts, &bytes.Buffer{}, []string{"split", "-k", "2", "-n", "3", "-type", "C"}); err != nil {
		t.Fatal(err)
	}
	groups := strings.Split(strings.TrimSpace(parts.String()), "\n\n")
	in := groups[0] + "\n\n" + groups[2] + "\n"
	var out, stderr bytes.Buffer
	if err := run(strings.NewReader(in), &out, &stderr, []string{"combine", "-descriptor"}); err != nil {
		t.Fatal(err)
	}
	// The CBOR normalizes key metadata, so the text round-trips
	// through its parse: same wallet, canonical serialization.
	back, err := bip380.Parse(strings.TrimSpace(out.String()))
	if err != nil {
		t.Fatalf("recovered text does not parse: %v", err)
	}
	if !bytes.Equal(urtypes.EncodeDescriptor(back), cbor) {
		t.Fatalf("recovered %q, want the wallet of %q", out.String(), want)
	}
}

// TestSubcommandHelp: -h prints usage and succeeds instead of exiting
// with an error.
func TestSubcommandHelp(t *testing.T) {
	for _, cmd := range []string{"encode", "decode", "split", "combine"} {
		if err := run(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, []string{cmd, "-h"}); err != nil {
			t.Errorf("%s -h: %v", cmd, err)
		}
	}
}

// TestSplitDerived: split -derived reproduces run for run, the default
// randomized profile does not, and the derived shares combine.
func TestSplitDerived(t *testing.T) {
	desc, err := bip380.Parse("wsh(sortedmulti(2,xpub661MyMwAqRbcFtXgS5sYJABqqG9YLmC4Q1Rdap9gSE8NqtwybGhePY2gZ29ESFjqJoCu1Rupje8YtGqsefD265TMg7usUDFdp6W1EGMcet8,xpub661MyMwAqRbcFW31YEwpkMuc5THy2PSt5bDMsktWQcFF8syAmRUapSCGu8ED9W6oDMSgv6Zz8idoc4a6mr8BDzTJY47LJhkJ8UB7WEGuduB/0/*))")
	if err != nil {
		t.Fatal(err)
	}
	cbor := urtypes.EncodeDescriptor(desc)
	split := func(args ...string) string {
		var parts bytes.Buffer
		if err := run(bytes.NewReader(cbor), &parts, &bytes.Buffer{}, append([]string{"split", "-k", "2", "-n", "3", "-type", "C"}, args...)); err != nil {
			t.Fatal(err)
		}
		return parts.String()
	}
	a, b := split("-derived"), split("-derived")
	if a != b {
		t.Fatal("two derived splits of the same input differ")
	}
	if c, d := split(), split(); c == d {
		t.Fatal("two randomized splits of the same input agree")
	}
	groups := strings.Split(strings.TrimSpace(a), "\n\n")
	if len(groups) != 3 {
		t.Fatalf("derived split produced %d series, want 3", len(groups))
	}
	var out bytes.Buffer
	if err := run(strings.NewReader(groups[1]+"\n\n"+groups[2]+"\n"), &out, &bytes.Buffer{}, []string{"combine"}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), cbor) {
		t.Fatal("derived shares combine to different data")
	}
}
