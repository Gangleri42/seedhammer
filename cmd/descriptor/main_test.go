package main

import (
	"bytes"
	"strings"
	"testing"

	"seedhammer.com/bc/urtypes"
	"seedhammer.com/bip380"
)

const text = "wsh(sortedmulti(2,[2a77e0a6/48h/0h/0h/2h]xpub6F8WgTkiV8iDPFG1Kv4sNrcBNMMgKK4cjfxjdZWvR3kChfbt3L2dJF7xmCHBMGMmxjyzwgjdFkh9UN3623YpsmqN1KwZGR45Y3ANLQQX87u/<0;1>/*,[9a6a2580/48h/0h/0h/2h]xpub6EeqK2JLwngrHJEQ4X4iqrySZV9qU3TgwMgf6NStLZa37AfNiHTtTE9ji1F9YQDLArJMLy8sw3Q2samVj5VQQjaaUHr5z2Hz57NWHJCfh31/<0;1>/*))"

// TestCBORAndText: the CBOR a share recovery yields and the text a
// wallet exports print the same canonical descriptor.
func TestCBORAndText(t *testing.T) {
	desc, err := bip380.Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	cbor := urtypes.EncodeDescriptor(desc)
	var fromCBOR, fromText bytes.Buffer
	if err := run(bytes.NewReader(cbor), &fromCBOR, nil); err != nil {
		t.Fatal(err)
	}
	if err := run(strings.NewReader(text+"\n"), &fromText, nil); err != nil {
		t.Fatal(err)
	}
	// The CBOR carries no children, so its text is the origin-only
	// spelling; both outputs describe one wallet.
	back, err := bip380.Parse(strings.TrimSpace(fromCBOR.String()))
	if err != nil {
		t.Fatalf("output does not parse: %v", err)
	}
	if !bytes.Equal(urtypes.EncodeDescriptor(back), cbor) {
		t.Fatalf("CBOR output %q is another wallet", fromCBOR.String())
	}
	if fromText.String() != desc.Encode()+"\n" {
		t.Fatalf("text output %q, want the canonical encoding", fromText.String())
	}
}

func TestRejectsNoise(t *testing.T) {
	if err := run(strings.NewReader("not a descriptor"), &bytes.Buffer{}, nil); err == nil {
		t.Fatal("noise accepted")
	}
	if err := run(strings.NewReader(""), &bytes.Buffer{}, []string{"a", "b"}); err == nil {
		t.Fatal("two files accepted")
	}
}
