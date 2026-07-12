package ndef

import (
	"bytes"
	"encoding/hex"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestRecords(t *testing.T) {
	tests := []struct {
		message string
		records []string
	}{
		{
			// Empty record.
			"d00000",
			nil,
		},
		{
			"d1010f5402656e48656c6c6f20776f726c6421",
			[]string{"Hello world!"},
		},
		{
			// URL with ID and trailing data.
			"d9010c0155ff046578616d706c652e636f6d",
			[]string{"https://example.com"},
		},
		{
			// Multiple records.
			"91010f5402656e48656c6c6f20776f726c642151010c55046578616d706c652e636f6d",
			[]string{"Hello world!", "https://example.com"},
		},
		{
			"c1010000012c5402656e48656c6c6f2c20746869732069732061206c6f6e67204e46432074657874207265636f72642e2048656c6c6f2c20746869732069732061206c6f6e67204e46432074657874207265636f72642e2048656c6c6f2c20746869732069732061206c6f6e67204e46432074657874207265636f72642e2048656c6c6f2c20746869732069732061206c6f6e67204e46432074657874207265636f72642e2048656c6c6f2c20746869732069732061206c6f6e67204e46432074657874207265636f72642e2048656c6c6f2c20746869732069732061206c6f6e67204e46432074657874207265636f72642e2048656c6c6f2c20746869732069732061206c6f6e67204e46432074657874207265636f72642e2048656c6c6f2c20746869732069732061206c6f6e67204e46",
			[]string{"Hello, this is a long NFC text record. Hello, this is a long NFC text record. Hello, this is a long NFC text record. Hello, this is a long NFC text record. Hello, this is a long NFC text record. Hello, this is a long NFC text record. Hello, this is a long NFC text record. Hello, this is a long NF"},
		},
	}
	for _, test := range tests {
		data, err := hex.DecodeString(test.message)
		if err != nil {
			t.Fatal(err)
		}
		r := NewRecordReader(bytes.NewBuffer(data))
		var records []string
		for {
			got, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("%x failed to decode: %v", data, err)
			}
			if len(got) == 0 {
				break
			}
			records = append(records, string(got))
		}
		if !slices.Equal(records, test.records) {
			t.Fatalf("%x decoded to %s, expected %s", data, records, test.records)
		}
	}
}

func TestExternalRecords(t *testing.T) {
	type record struct {
		typ     string
		payload string
	}
	tests := []struct {
		message string
		records []record
	}{
		{
			// External type record.
			"d40d056578616d706c652e636f6d3a7468656c6c6f",
			[]record{{"example.com:t", "hello"}},
		},
		{
			// An external record with a too long type is skipped.
			"942103" + strings.Repeat("61", 33) + "616263" +
				"51010f5402656e48656c6c6f20776f726c6421",
			[]record{{"", "Hello world!"}},
		},
		{
			// External record followed by a text record.
			"940d056578616d706c652e636f6d3a7468656c6c6f" +
				"51010f5402656e48656c6c6f20776f726c6421",
			[]record{{"example.com:t", "hello"}, {"", "Hello world!"}},
		},
		{
			// A 1-byte external type is not treated as well-known.
			"d401065402656e414243",
			[]record{{"T", "\x02enABC"}},
		},
	}
	for _, test := range tests {
		data, err := hex.DecodeString(test.message)
		if err != nil {
			t.Fatal(err)
		}
		r := NewRecordReader(bytes.NewBuffer(data))
		var records []record
		for {
			got, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("%x failed to decode: %v", data, err)
			}
			if len(got) == 0 {
				break
			}
			records = append(records, record{string(r.RecordType()), string(got)})
		}
		if !slices.Equal(records, test.records) {
			t.Fatalf("%x decoded to %v, expected %v", data, records, test.records)
		}
	}
}

func TestOverlongRecordRejected(t *testing.T) {
	// A non-short record declaring a 4-byte length near uint32 max
	// must be rejected, not narrowed to a negative int that desyncs
	// the parser onto the payload bytes.
	// Flags MB|ME (0xc0), type len 1, payload len 0xffffffff, type 'T'.
	data, err := hex.DecodeString("c001ffffffff54")
	if err != nil {
		t.Fatal(err)
	}
	r := NewRecordReader(bytes.NewBuffer(data))
	if _, err := io.ReadAll(r); err == nil {
		t.Fatal("oversized record accepted, want error")
	}
}

func TestMessages(t *testing.T) {
	tests := []struct {
		tlvs    string
		content string
	}{
		{
			// With an unknown TLV key (0x01), multiple records and trailing data.
			"0103a00c340301ff00000342d1013e5402656e7369676e20776564642068617262206865617220616c6d6f2061757468206c61746920616c726520686f62622064726166206f6e636520726f7365fedeadbeef",
			"ffd1013e5402656e7369676e20776564642068617262206865617220616c6d6f2061757468206c61746920616c726520686f62622064726166206f6e636520726f7365",
		},
		{
			// Long record format with padding.
			"03ff0100c1010000012c5402656e48656c6c6f2c20746869732069732061206c6f6e67204e46432074657874207265636f72642e2048656c6c6f2c20746869732069732061206c6f6e67204e46432074657874207265636f72642e2048656c6c6f2c20746869732069732061206c6f6e67204e46432074657874207265636f72642e2048656c6c6f2c20746869732069732061206c6f6e67204e46432074657874207265636f72642e2048656c6c6f2c20746869732069732061206c6f6e67204e46432074657874207265636f72642e2048656c6c6f2c20746869732069732061206c6f6e67204e46432074657874207265636f72642e2048656c6c6f2c207468697320000000000000fe",
			"c1010000012c5402656e48656c6c6f2c20746869732069732061206c6f6e67204e46432074657874207265636f72642e2048656c6c6f2c20746869732069732061206c6f6e67204e46432074657874207265636f72642e2048656c6c6f2c20746869732069732061206c6f6e67204e46432074657874207265636f72642e2048656c6c6f2c20746869732069732061206c6f6e67204e46432074657874207265636f72642e2048656c6c6f2c20746869732069732061206c6f6e67204e46432074657874207265636f72642e2048656c6c6f2c20746869732069732061206c6f6e67204e46432074657874207265636f72642e2048656c6c6f2c207468697320",
		},
	}
	for _, test := range tests {
		data, err := hex.DecodeString(test.tlvs)
		if err != nil {
			t.Fatal(err)
		}
		want, err := hex.DecodeString(test.content)
		if err != nil {
			t.Fatal(err)
		}
		r := NewMessageReader(bytes.NewBuffer(data))
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("%x failed to decode: %v", data, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%x decoded to %s, expected %s", data, got, want)
		}
	}
}
