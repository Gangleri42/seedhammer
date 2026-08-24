package main

import (
	"bytes"
	"strings"
	"testing"

	"seedhammer.com/bc/urtypes"
	"seedhammer.com/bip380"
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
