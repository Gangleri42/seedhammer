package nonstandard

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"seedhammer.com/bip380"
)

func TestDescriptors(t *testing.T) {
	tests := []struct {
		name    string
		encoded string
		desc    string
	}{
		{
			"Test Multisig 2-of-3",
			`{
				"label": "Test Multisig 2-of-3",
				"blockheight": 481824,
				"descriptor": "wsh(sortedmulti(2,[dc567276/48h/0h/0h/2h]xpub6DiYrfRwNnjeX4vHsWMajJVFKrbEEnu8gAW9vDuQzgTWEsEHE16sGWeXXUV1LBWQE1yCTmeprSNcqZ3W74hqVdgDbtYHUv3eM4W2TEUhpan/0/*,[f245ae38/48h/0h/0h/2h]xpub6DnT4E1fT8VxuAZW29avMjr5i99aYTHBp9d7fiLnpL5t4JEprQqPMbTw7k7rh5tZZ2F5g8PJpssqrZoebzBChaiJrmEvWwUTEMAbHsY39Ge/0/*,[c5d87297/48h/0h/0h/2h]xpub6DjrnfAyuonMaboEb3ZQZzhQ2ZEgaKV2r64BFmqymZqJqviLTe1JzMr2X2RfQF892RH7MyYUbcy77R7pPu1P71xoj8cDUMNhAMGYzKR4noZ/0/*))#hfwurrvt",
				"devices": [{"type": "other", "label": "Test Multisig 2-of-3 Cosigner 1"}, {"type": "other", "label": "Test Multisig 2-of-3 Cosigner 2"}, {"type": "other", "label": "Test Multisig 2-of-3 Cosigner 3"}]
			}`,
			"wsh(sortedmulti(2,[dc567276/48h/0h/0h/2h]xpub6DiYrfRwNnjeX4vHsWMajJVFKrbEEnu8gAW9vDuQzgTWEsEHE16sGWeXXUV1LBWQE1yCTmeprSNcqZ3W74hqVdgDbtYHUv3eM4W2TEUhpan/0/*,[f245ae38/48h/0h/0h/2h]xpub6DnT4E1fT8VxuAZW29avMjr5i99aYTHBp9d7fiLnpL5t4JEprQqPMbTw7k7rh5tZZ2F5g8PJpssqrZoebzBChaiJrmEvWwUTEMAbHsY39Ge/0/*,[c5d87297/48h/0h/0h/2h]xpub6DjrnfAyuonMaboEb3ZQZzhQ2ZEgaKV2r64BFmqymZqJqviLTe1JzMr2X2RfQF892RH7MyYUbcy77R7pPu1P71xoj8cDUMNhAMGYzKR4noZ/0/*))#hfwurrvt",
		},
		{
			"sh",
			`# BlueWallet Multisig setup file
# this file contains only public keys and is safe to
# distribute among cosigners
#
Name: sh
Policy: 2 of 3
Derivation: m/48'/0'/0'/2'
Format: P2WSH

5A0804E3: xpub6F148LnjUhGrHfEN6Pa8VkwF8L6FJqYALxAkuHfacfVhMLVY4MRuUVMxr9pguAv67DHx1YFxqoKN8s4QfZtD9sR2xRCffTqi9E8FiFLAYk8

DD4FADEE: xpub6DnediUuY8Pcc6Fej8Yt2ZntPCyFdpbHBkNV7EawesRMbc6i9MKKMhKEv4JMMzwDJckaV4czBvNdc6ikwLiZqdUqMd5ZKQGYaQT4cXMeVjf

9BACD5C0: xpub6EefrCrMAduhNwnsHb3dAs8DYZSw4f63WyR6DaEByUHjwvPDdhczj15FyBBG4tbEJtf4vRKTv1ng5SPPnWv1Pve1f15EJfiBY5oYDN6VLEC
`,
			"wsh(sortedmulti(2,[5A0804E3/48'/0'/0'/2']xpub6F148LnjUhGrHfEN6Pa8VkwF8L6FJqYALxAkuHfacfVhMLVY4MRuUVMxr9pguAv67DHx1YFxqoKN8s4QfZtD9sR2xRCffTqi9E8FiFLAYk8,[DD4FADEE/48'/0'/0'/2']xpub6DnediUuY8Pcc6Fej8Yt2ZntPCyFdpbHBkNV7EawesRMbc6i9MKKMhKEv4JMMzwDJckaV4czBvNdc6ikwLiZqdUqMd5ZKQGYaQT4cXMeVjf,[9BACD5C0/48'/0'/0'/2']xpub6EefrCrMAduhNwnsHb3dAs8DYZSw4f63WyR6DaEByUHjwvPDdhczj15FyBBG4tbEJtf4vRKTv1ng5SPPnWv1Pve1f15EJfiBY5oYDN6VLEC))",
		},
		{
			// Wallet with Zpub keys.
			"V2",
			`# BlueWallet Multisig setup file
Name: V2
Policy: 2 of 3
Derivation: m/48'/0'/0'/2'
Format: P2WSH

79E1C26F: Zpub753vSk6B5CuYmJBvgBQYmBUghHoApQHtgJWthN7WmrJsaRaCGuQFguZTXdJxCL2rUbFdsVcLuT9ASoKGtRtug3A6SZmhfaMzYH5yc11Da3h

FC68BCE8: Zpub74vSYSU12tQqbxYb7YYwUSHq8bUVSe3iKxG8JHmuLjEu1K3ZjjgH1refsgdUhxR4WttV1NFQzJnZZtueannW6Mau9QXs58wLWvh3ftfkk97

347BCBE3: Zpub74bnCwDLdCa7ytzd2unjhLL842fv4RocsHbRBcpP8Nv2DGp6eCzZfJesd55YvYv1TkVrsyCNSV8HcoHcHpmm1GvmhuYmschCbYcTR1orqKB
`,
			"wsh(sortedmulti(2,[79E1C26F/48'/0'/0'/2']Zpub753vSk6B5CuYmJBvgBQYmBUghHoApQHtgJWthN7WmrJsaRaCGuQFguZTXdJxCL2rUbFdsVcLuT9ASoKGtRtug3A6SZmhfaMzYH5yc11Da3h,[FC68BCE8/48'/0'/0'/2']Zpub74vSYSU12tQqbxYb7YYwUSHq8bUVSe3iKxG8JHmuLjEu1K3ZjjgH1refsgdUhxR4WttV1NFQzJnZZtueannW6Mau9QXs58wLWvh3ftfkk97,[347BCBE3/48'/0'/0'/2']Zpub74bnCwDLdCa7ytzd2unjhLL842fv4RocsHbRBcpP8Nv2DGp6eCzZfJesd55YvYv1TkVrsyCNSV8HcoHcHpmm1GvmhuYmschCbYcTR1orqKB))",
		},
		{
			"test",
			`Name: test
Policy: 2 of 3
Format: P2WSH

Derivation: m/48'/0'/0'/2'
dc567276: xpub6DiYrfRwNnjeX4vHsWMajJVFKrbEEnu8gAW9vDuQzgTWEsEHE16sGWeXXUV1LBWQE1yCTmeprSNcqZ3W74hqVdgDbtYHUv3eM4W2TEUhpan

Derivation: m/48'/0'/0'/2'
f245ae38: xpub6DnT4E1fT8VxuAZW29avMjr5i99aYTHBp9d7fiLnpL5t4JEprQqPMbTw7k7rh5tZZ2F5g8PJpssqrZoebzBChaiJrmEvWwUTEMAbHsY39Ge

Derivation: m/48'/0'/0'/2'
c5d87297: xpub6DjrnfAyuonMaboEb3ZQZzhQ2ZEgaKV2r64BFmqymZqJqviLTe1JzMr2X2RfQF892RH7MyYUbcy77R7pPu1P71xoj8cDUMNhAMGYzKR4noZ
`,
			"wsh(sortedmulti(2,[dc567276/48'/0'/0'/2']xpub6DiYrfRwNnjeX4vHsWMajJVFKrbEEnu8gAW9vDuQzgTWEsEHE16sGWeXXUV1LBWQE1yCTmeprSNcqZ3W74hqVdgDbtYHUv3eM4W2TEUhpan,[f245ae38/48'/0'/0'/2']xpub6DnT4E1fT8VxuAZW29avMjr5i99aYTHBp9d7fiLnpL5t4JEprQqPMbTw7k7rh5tZZ2F5g8PJpssqrZoebzBChaiJrmEvWwUTEMAbHsY39Ge,[c5d87297/48'/0'/0'/2']xpub6DjrnfAyuonMaboEb3ZQZzhQ2ZEgaKV2r64BFmqymZqJqviLTe1JzMr2X2RfQF892RH7MyYUbcy77R7pPu1P71xoj8cDUMNhAMGYzKR4noZ))",
		},
		{
			"",
			"[4bbaa801/84'/0'/0']zpub6qpFgGWoG7bKmDDMvmwHBvg6inZAb2KF2Vg8h4fKJ2ickSZ71PsMmRg1FyRWAS6PqPCSzd5CB6PHixx64k6q5svZNZd9bEoCWJuMSkSRzJx",
			"wpkh([4bbaa801/84'/0'/0']xpub6C9j4wAxxkWN4cq8G4N2mkV6NrGGhnLFCGdh8GsYY1xreEveW5YEXJMjDZWLAcnZ26xqVft5FmgBxPixdMGoVQZMdtEJRRADxrn4facoGnx)",
		},
		{
			"",
			"zpub6qpFgGWoG7bKmDDMvmwHBvg6inZAb2KF2Vg8h4fKJ2ickSZ71PsMmRg1FyRWAS6PqPCSzd5CB6PHixx64k6q5svZNZd9bEoCWJuMSkSRzJx",
			"wpkh([00000000/84'/0'/0']xpub6C9j4wAxxkWN4cq8G4N2mkV6NrGGhnLFCGdh8GsYY1xreEveW5YEXJMjDZWLAcnZ26xqVft5FmgBxPixdMGoVQZMdtEJRRADxrn4facoGnx)",
		},
		{
			"",
			"xpub6C9j4wAxxkWN4cq8G4N2mkV6NrGGhnLFCGdh8GsYY1xreEveW5YEXJMjDZWLAcnZ26xqVft5FmgBxPixdMGoVQZMdtEJRRADxrn4facoGnx",
			"pkh(xpub6C9j4wAxxkWN4cq8G4N2mkV6NrGGhnLFCGdh8GsYY1xreEveW5YEXJMjDZWLAcnZ26xqVft5FmgBxPixdMGoVQZMdtEJRRADxrn4facoGnx)",
		},
	}
	for _, test := range tests {
		got, err := OutputDescriptor([]byte(test.encoded))
		if err != nil {
			t.Fatalf("failed to parse:\n%q\nerror: %v", test.encoded, err)
		}
		want, err := bip380.Parse(test.desc)
		if err != nil {
			t.Fatalf("failed to parse reference:\n%q\nerror: %v", test.desc, err)
		}
		want.Title = test.name
		// want is parsed from canonical descriptor text, whose
		// checksum presence need not match the scanned format's.
		want.NoChecksum = got.NoChecksum
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q\ndecoded to\n%#v\nexpected\n%#v\n", test.encoded, got, want)
		}
	}
}

func TestDecoder(t *testing.T) {
	parts := []string{
		"p1of3 abc",
		"p2of3 def",
		"p3of3 g",
	}
	var d Decoder
	for _, p := range parts {
		if err := d.Add(p); err != nil {
			t.Fatal(err)
		}
	}
	if p := d.Progress(); p != 1. {
		t.Errorf("decoder progress %f, want 1.", p)
	}
	got := string(d.Result())
	if want := "abcdefg"; got != want {
		t.Errorf("decoded %q, want %q", got, want)
	}
}

// The pMofN header sizes the parts table; a hostile count must not.
func TestDecoderHostileCounts(t *testing.T) {
	for _, p := range []string{"p1of99999999 x", "p0of3 x", "p4of3 x", "p-1of3 x"} {
		var d Decoder
		if err := d.Add(p); err == nil {
			t.Errorf("accepted %q", p)
		}
	}
}

func TestElectrumSeed(t *testing.T) {
	phrase := "head orient raw shoulder size fancy front cycle lamp giant camera jacket"
	if !ElectrumSeed(phrase) {
		t.Fatal("failed to detect Electrum seed")
	}
	if !ElectrumSeed(strings.ToUpper(phrase)) {
		t.Fatal("failed to detect upper-case Electrum seed")
	}
}

// A malformed BlueWallet export must be rejected, not handed onward as a
// descriptor that panics later. Both shapes reached the firmware from an
// NFC tag: a short fingerprint panicked binary.BigEndian.Uint32 inside
// the parser, and a missing Format header left Script at its zero value,
// which bip380.Encode panics on when the operator confirms.
func TestBlueWalletRejectsMalformed(t *testing.T) {
	const xp = "xpub6DiYrfRwNnjeX4vHsWMajJVFKrbEEnu8gAW9vDuQzgTWEsEHE16sGWeXXUV1LBWQE1yCTmeprSNcqZ3W74hqVdgDbtYHUv3eM4W2TEUhpan"
	base := "Name: t\nPolicy: 1 of 1\nDerivation: m/48'/0'/0'/2'\nFormat: P2WSH\n"
	for _, fp := range []string{"", "00", "0000", "000000"} {
		if _, err := OutputDescriptor([]byte(base + fp + ": " + xp + "\n")); err == nil {
			t.Errorf("fingerprint %q: accepted, want rejected", fp)
		}
	}
	if _, err := OutputDescriptor([]byte(base + "dc567276: " + xp + "\n")); err != nil {
		t.Errorf("well-formed export rejected: %v", err)
	}
	for _, p := range []string{
		"Name: t\nPolicy: 1 of 1\nDerivation: m/48'/0'/0'/2'\ndc567276: " + xp + "\n",
		"Name: 00",
	} {
		if d, err := OutputDescriptor([]byte(p)); err == nil {
			t.Errorf("missing Format accepted as %v, want rejected", d.Script)
		}
	}
}

// A format that recognizes the shape of the input and then refuses it
// hands its reason out; only input no format claims gets the generic
// error. The confirm-screen notice and the cosigner reject line show
// what comes back here.
func TestOutputDescriptorKeepsTheReason(t *testing.T) {
	const xp = "xpub6DiYrfRwNnjeX4vHsWMajJVFKrbEEnu8gAW9vDuQzgTWEsEHE16sGWeXXUV1LBWQE1yCTmeprSNcqZ3W74hqVdgDbtYHUv3eM4W2TEUhpan"
	const xp2 = "xpub6DnT4E1fT8VxuAZW29avMjr5i99aYTHBp9d7fiLnpL5t4JEprQqPMbTw7k7rh5tZZ2F5g8PJpssqrZoebzBChaiJrmEvWwUTEMAbHsY39Ge"
	for _, tc := range []struct{ in, want string }{
		{"wsh(sortedmulti(0," + xp + "," + xp2 + "))", "quorum"},
		{"wpkh(" + xp + "/0h/1)", "hardened"},
		{"wsh(sortedmulti(2," + xp + "))#deadbeef", "checksum"},
		{"Name: t\nPolicy: 3 of 2\nDerivation: m/48'/0'/0'/2'\nFormat: P2WSH\ndc567276: " + xp + "\nf245ae38: " + xp2 + "\n", "quorum"},
	} {
		_, err := OutputDescriptor([]byte(tc.in))
		if err == nil {
			t.Errorf("%q parsed", tc.in)
			continue
		}
		if errors.Is(err, ErrUnrecognized) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: error %q, want a mention of %q", tc.in, err, tc.want)
		}
	}
	for _, in := range []string{"hello plate", "IN CASE OF FIRE", "abandon abandon about"} {
		if _, err := OutputDescriptor([]byte(in)); !errors.Is(err, ErrUnrecognized) {
			t.Errorf("%q: error %v, want ErrUnrecognized", in, err)
		}
	}
}

// Policy arithmetic through the scan entry point: 0-of-n is
// anyone-can-spend and m > n is unspendable, so neither may parse.
func TestBlueWalletRejectsBadQuorum(t *testing.T) {
	const xp = "xpub6DiYrfRwNnjeX4vHsWMajJVFKrbEEnu8gAW9vDuQzgTWEsEHE16sGWeXXUV1LBWQE1yCTmeprSNcqZ3W74hqVdgDbtYHUv3eM4W2TEUhpan"
	const xp2 = "xpub6DnT4E1fT8VxuAZW29avMjr5i99aYTHBp9d7fiLnpL5t4JEprQqPMbTw7k7rh5tZZ2F5g8PJpssqrZoebzBChaiJrmEvWwUTEMAbHsY39Ge"
	export := func(policy string) []byte {
		return []byte("Name: t\nPolicy: " + policy + "\nDerivation: m/48'/0'/0'/2'\nFormat: P2WSH\n" +
			"dc567276: " + xp + "\nf245ae38: " + xp2 + "\n")
	}
	for _, policy := range []string{"0 of 2", "-1 of 2", "3 of 2"} {
		if _, err := OutputDescriptor(export(policy)); err == nil {
			t.Errorf("policy %q accepted", policy)
		}
	}
	if _, err := OutputDescriptor(export("2 of 2")); err != nil {
		t.Errorf("a sound policy was rejected: %v", err)
	}
}
