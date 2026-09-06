package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"seedhammer.com/bbqr"
	"seedhammer.com/bc/urtypes"
	"seedhammer.com/bip380"
)

// TestShamirVectorInputsValid: the shamir test vectors are valid
// examples of their declared types. A U vector that reads as a
// descriptor parses as one; a C vector is crypto-output CBOR of a
// wallet descriptor that re-encodes to the same bytes, the fixed
// point the device relies on when it re-encodes a recovered
// descriptor. The shamir package cannot import bip380 or urtypes, so
// the check lives here.
func TestShamirVectorInputsValid(t *testing.T) {
	raw, err := os.ReadFile("../../shamir/testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vf struct {
		Vectors []struct {
			Name     string `json:"name"`
			FileType string `json:"file_type"`
			DataHex  string `json:"data_hex"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &vf); err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, v := range vf.Vectors {
		data, err := hex.DecodeString(v.DataHex)
		if err != nil {
			t.Fatalf("%s: %v", v.Name, err)
		}
		switch {
		case v.FileType == string(bbqr.TypeText) && bytes.HasPrefix(data, []byte("wsh(")):
			if _, err := bip380.Parse(string(data)); err != nil {
				t.Errorf("%s: descriptor text does not parse: %v", v.Name, err)
			}
			checked++
		case v.FileType == string(bbqr.TypeCBOR):
			d, err := urtypes.Parse("crypto-output", data)
			if err != nil {
				t.Errorf("%s: %v", v.Name, err)
				continue
			}
			desc, ok := d.(*bip380.Descriptor)
			if !ok {
				t.Errorf("%s: crypto-output is %T, want a wallet descriptor", v.Name, d)
				continue
			}
			if !bytes.Equal(urtypes.EncodeDescriptor(desc), data) {
				t.Errorf("%s: descriptor CBOR does not re-encode to itself", v.Name)
			}
			checked++
		}
	}
	if checked < 2 {
		t.Fatalf("checked %d descriptor vectors, want the U and the C one at least", checked)
	}
}
