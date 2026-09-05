package gui

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcutil/v2/hdkeychain"
	"github.com/btcsuite/btcd/chaincfg/v2"
	"seedhammer.com/bip32"
	"seedhammer.com/bip380"
	"seedhammer.com/bip39"
	"seedhammer.com/codex32"
)

func TestScan(t *testing.T) {
	c32, err := codex32.New("MS12NAMEA320ZYXWVUTSRQPNMLKJHGFEDCAXRPP870HKKQRM")
	if err != nil {
		t.Fatal(err)
	}
	b39, err := bip39.ParseMnemonic("legal winner thank year wave sausage worth useful legal winner thank yellow")
	if err != nil {
		t.Fatal(err)
	}
	b39x24, err := bip39.ParseMnemonic("attack pizza motion avocado network gather crop fresh patrol unusual wild holiday candy pony ranch winter theme error hybrid van cereal salon goddess expire")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		Name    string
		Encoded string
		Err     error
		Content any
	}{
		{
			Name:    "Unknown Format",
			Encoded: "unsupported rune: \u00e9",
			Err:     errScanUnknownFormat,
		},
		{
			Name:    "Plain Text",
			Encoded: "IN CASE OF FIRE\n\nBREAK GLASS",
			Content: plainText("IN CASE OF FIRE\n\nBREAK GLASS"),
		},
		{
			Name:    "Plain Text CRLF",
			Encoded: "LINE 1\r\nLINE 2",
			Content: plainText("LINE 1\nLINE 2"),
		},
		{
			Name:    "Plain Text Canonicalized",
			Encoded: "LINE 1  \n\nLINE 2  \n \n\n",
			Content: plainText("LINE 1\n\nLINE 2"),
		},
		{
			Name:    "Whitespace Only",
			Encoded: " \n \n",
			Err:     errScanUnknownFormat,
		},
		{
			Name:    "Too Long",
			Encoded: strings.Repeat("TOOLONG", 8000),
			Err:     errScanOverflow,
		},
		{
			Name:    "Codex32",
			Encoded: c32.String(),
			Content: c32,
		},
		{
			Name:    "BIP39",
			Encoded: b39.String(),
			Content: b39,
		},
		{
			Name:    "SeedQR",
			Encoded: "101920151790203919831533203119191019201517902040",
			Content: b39,
		},
		{
			// The SeedSigner documentation's own 24-word vector.
			Name:    "SeedQR 24 Words",
			Encoded: "011513251154012711900771041507421289190620080870026613431420201617920614089619290300152408010643",
			Content: b39x24,
		},
		{
			Name:    "SeedQR Trailing Newline",
			Encoded: "101920151790203919831533203119191019201517902040\r\n",
			Content: b39,
		},
		{
			// The last word moved by one: the text plate, as a
			// misspelled seed phrase would be.
			Name:    "SeedQR Bad Checksum",
			Encoded: "101920151790203919831533203119191019201517902041",
			Content: plainText("101920151790203919831533203119191019201517902041"),
		},
		{
			// Four words are never a mnemonic, and the CompactSeedQR
			// reading of the same sixteen bytes must not apply.
			Name:    "Sixteen Digits",
			Encoded: "1234567812345678",
			Content: plainText("1234567812345678"),
		},
		{
			Name:    "Thirty-two Digits",
			Encoded: "12345678123456781234567812345678",
			Content: plainText("12345678123456781234567812345678"),
		},
		{
			Name:    "Command",
			Encoded: "command: sudo-make-me-a-sandwich!",
			Content: debugCommand{"sudo-make-me-a-sandwich!"},
		},
		{
			Name:    "Descriptor",
			Encoded: "wpkh([dc567276/48h/0h/0h/2h]xpub6DiYrfRwNnjeX4vHsWMajJVFKrbEEnu8gAW9vDuQzgTWEsEHE16sGWeXXUV1LBWQE1yCTmeprSNcqZ3W74hqVdgDbtYHUv3eM4W2TEUhpan/0/*)#ap6v6zth",
			Content: &bip380.Descriptor{
				Script:    bip380.P2WPKH,
				Threshold: 1,
				Type:      bip380.Singlesig,
				Keys: []bip380.Key{{
					Network:           &chaincfg.MainNetParams,
					MasterFingerprint: 0xdc567276,
					DerivationPath:    bip32.Path{hdkeychain.HardenedKeyStart + 48, hdkeychain.HardenedKeyStart, hdkeychain.HardenedKeyStart, hdkeychain.HardenedKeyStart + 2},
					Children: []bip380.Derivation{
						{Index: 0x0},
						{Type: bip380.WildcardDerivation},
					},
					KeyData:           []uint8{0x2, 0x1c, 0xb, 0x47, 0x9e, 0xcf, 0x6e, 0x67, 0x71, 0x3d, 0xdf, 0xc, 0x43, 0xb6, 0x34, 0x59, 0x2f, 0x51, 0xc0, 0x37, 0xb6, 0xf9, 0x51, 0xfb, 0x1d, 0xc6, 0x36, 0x1a, 0x98, 0xb1, 0xe5, 0x73, 0x5e},
					ChainCode:         []uint8{0x6b, 0x3a, 0x4c, 0xfb, 0x6a, 0x45, 0xf6, 0x30, 0x5e, 0xfe, 0x6e, 0xe, 0x97, 0x6b, 0x5d, 0x26, 0xba, 0x27, 0xf7, 0xc3, 0x44, 0xd7, 0xfc, 0x7a, 0xbe, 0xf7, 0xbe, 0x2d, 0x6, 0xd5, 0x2d, 0xfd},
					ParentFingerprint: 0x18f8c2e7,
				}},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			buf := new(bytes.Buffer)
			buf.WriteString(test.Encoded)
			s := new(scanner)
			for {
				got, err := s.Scan(buf)
				if err != nil || test.Err != nil {
					if err == errScanInProgress {
						continue
					}
					if !errors.Is(err, test.Err) {
						t.Fatalf("scanner failed: %v", err)
					}
				}
				if want := test.Content; !reflect.DeepEqual(got, want) {
					t.Errorf("scanner decoded\n%#v\nexpected\n%#v", got, want)
				}
				break
			}
		})
	}
}
