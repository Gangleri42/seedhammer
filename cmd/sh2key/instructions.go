package main

import (
	"fmt"

	"seedhammer.com/curves"
	"seedhammer.com/engrave"
	"seedhammer.com/richtext"
)

// The payload contract shared with cmd/svgplate: the coarse 0.1mm
// payload grid and the needle's stroke width in those units. The body
// size is the largest at which the full document fits the plate; the
// 4.5mm headers (title, sections, fingerprint) carry the legibility.
const (
	instructionsBodyMM = 3.0
	payloadUnitsPerMM  = 10
	payloadStroke      = 3
)

// instructionsTemplate is the plate-2 restore document, rich-text
// markdown rendered by the firmware's own font. Line breaks are hard:
// the renderer does not reflow, and every line is width-checked by
// the validation pass.
const instructionsTemplate = `## SH2 BOOT KEY RESTORE

The 24 words on the words plate are a
secp256k1 private key, encoded as BIP39.
_Not a wallet seed:_ no BIP32 derivation,
no passphrase, no seed generation.

## Decode

1. 24 words decode to 264 bits.
2. Drop the last 8 bits, the checksum.
3. The remaining 32 bytes are the scalar.
4. Wrap it as a SEC1 EC private key PEM.

## Verify

SHA-256 of the public key, 64 bytes
X then Y, must begin with

## %s
and match the boot key hash in the
board's OTP.
`

// instructionsMarkdown fills the restore document with the key's
// fingerprint prefix, grouped in fours for transcription; -verify
// accepts the spacing verbatim.
func instructionsMarkdown(fpHex string) string {
	fp := fpHex[0:4] + " " + fpHex[4:8] + " " + fpHex[8:12] + " " + fpHex[12:16]
	return fmt.Sprintf(instructionsTemplate, fp)
}

// instructionsPayload renders the document and encodes it as a curves
// payload, then parses and validates the emitted bytes exactly as the
// firmware will: a font or template change that overflows the plate
// fails here, not on the machine.
func instructionsPayload(src string) ([]byte, error) {
	groups, err := richtext.Render(src, instructionsBodyMM)
	if err != nil {
		return nil, err
	}
	payload, err := curves.EncodeGroups(payloadUnitsPerMM, payloadStroke, richtext.Groups(groups, payloadUnitsPerMM))
	if err != nil {
		return nil, err
	}
	d, err := curves.Parse(payload, engrave.SH2Params)
	if err != nil {
		return nil, err
	}
	if _, err := d.Validate(engrave.SH2Params); err != nil {
		return nil, err
	}
	return payload, nil
}
