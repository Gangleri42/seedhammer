# Back up your SeedHammer II boot key as 24 words

Companion to [`howto-bootkey-and-signing.md`](howto-bootkey-and-signing.md), which
mints the key this document backs up.

Your boot key is a 32-byte secp256k1 private key in a single file. Its hash is fused
into your board's OTP, and OTP is one-way. Lose the file and you keep a board that
boots only firmware you can no longer produce. There is no reset, no recovery slot,
no support path. The file is the whole story.

## Before you start

- Your key, `my-key.pem`, minted in the signing how-to.
- A SeedHammer II and blank plates.
- The repo checkout and Go.
- For the NFC steps (4 and 5), the nfcpy stack: `./install.sh send`
  sets it up in `~/.nfc-venv`. `sh2key` finds the venv by itself;
  step 5's direct `python3` call needs it activated first
  (`. ~/.nfc-venv/bin/activate`).
- Somewhere private. Words go on a screen during this.

## Why words rather than hex

A secp256k1 private key is a 256-bit scalar. BIP39 256-bit entropy is also 256 bits.
The sizes match exactly, so the key encodes as a 24-word mnemonic with nothing left
over and nothing invented.

That buys three things. The machine already engraves 24-word seed plates, so the
layout, the font and the plate format are all solved. The mnemonic carries an 8-bit
checksum, so a mis-struck or mis-read word is caught instead of silently producing a
key that signs nothing. And words survive damage better than hex: BIP39 words are
unique in their first four letters, so a partly corroded word is often still readable,
where a smudged hex digit is gone.

Every valid secp256k1 key is smaller than the curve order, which is itself below
2^256. So every key that exists encodes as 24 words. This direction never fails.

## What you are about to do

Convert the key to 24 words, prove the words rebuild the key **before** cutting any
metal, then engrave two plates:

| Plate | Contents | Secret? |
| --- | --- | --- |
| 1 | The 24 words | Yes. Treat it as a wallet seed. |
| 2 | Restore instructions | No. Copy it freely. |

Keeping them separate is deliberate. Plate 2 is useless to a thief and priceless to
whoever restores this in five years, so it can be duplicated and stored where plate 1
must never go.

## The one thing that will mislead whoever restores this

Anyone who finds 24 BIP39 words will assume a wallet seed and run BIP32 derivation on
it. That produces the wrong key, silently.

Here the entropy **is** the private key. No PBKDF2, no seed derivation, no passphrase,
no derivation path. Words decode to 32 bytes, and those 32 bytes are the scalar.

This is why plate 2 exists. Do not skip it.

## Step 1: Confirm what you are holding

```sh
openssl pkey -in my-key.pem -noout -text_pub | grep -E 'ASN1 OID|Public-Key'
```

Expect `Public-Key: (256 bit)` and `ASN1 OID: secp256k1`. Anything else means this
procedure does not apply to your file.

Deliberately `-text_pub`, not `-text`. The plain `-text` form prints the private
scalar to your terminal, into scrollback, and into any session log. You do not need
to see it at any point in this procedure.

## Step 2: Convert the key to 24 words

```sh
go run seedhammer.com/cmd/sh2key backup my-key.pem
```

It prints the words to the terminal and nothing else anywhere. It does not write them
to disk unless you ask with `-o`, and it refuses to run if stdout is not a terminal,
so a stray pipe cannot capture them.

Note the public key fingerprint it prints alongside. That value is public, it is what
is fused into your OTP, and it is how every later verification step works.

## Step 3: Prove the words rebuild the key

This is the step people skip and regret.

```sh
go run seedhammer.com/cmd/sh2key restore -o /tmp/check.pem
cmp /tmp/check.pem my-key.pem && echo IDENTICAL
shred -u /tmp/check.pem
```

Type the words in by hand from what you are about to engrave. Do not paste them.
Copy-paste proves the clipboard works; typing proves the words you are about to
commit to steel are the words that rebuild your key.

`IDENTICAL` means the PEM was reproduced byte for byte. If it does not print, stop
and go back to step 2. Do not engrave a mnemonic that has not round-tripped.

## Step 4: Engrave plate 1, the words

Input to the machine is NFC. Send the mnemonic as plain text and the device
recognises it as a BIP39 seed and offers the seed-plate flow:

```sh
go run seedhammer.com/cmd/sh2key backup my-key.pem -nfc
```

Then follow the on-screen flow and engrave.

## Step 5: Engrave plate 2, the instructions

Plate 2 is a text plate. It carries no secret, so unlike plate 1 it is safe to
generate, store and re-send at any time.

```sh
go run seedhammer.com/cmd/sh2key backup my-key.pem -instructions -o restore.txt
python3 cmd/textplate/write-nfc.py restore.txt
```

`write-nfc.py` validates the text against the plate grid and reports the size
before writing; the machine then confirms the same layout on screen.

The generated text fits a single plate at 5mm, the largest size the machine offers
that holds it. It reads:

```
SH2 BOOTKEY RESTORE
24 BIP39 WORDS ARE THE
secp256k1 PRIVATE KEY
NOT A WALLET SEED
NO BIP32, NO PASSPHRASE
1 WORDS -> 264 BITS
2 DROP LAST 8 = CKSUM
3 THE 32 BYTES ARE THE
  PRIVATE SCALAR
4 WRAP AS SEC1 EC PEM
CHECK SHA256 PUBKEY XY
STARTS <first 16 hex>
MUST MATCH OTP BOOTKEY
SIGNING: SEE REPO DOCS
```

The fingerprint line is filled in from your own key. It deliberately names no signing
command: tools change, steel does not, and the repo docs stay current.

## Step 6: Verify the plates, not the files

Read plate 1 back with your own eyes, off the steel, and type it in:

```sh
go run seedhammer.com/cmd/sh2key restore -verify <your-fingerprint>
```

Use the fingerprint from step 2. The tool derives the public key from what you typed
and compares. A match means the engraving is correct and the backup is real.

The mnemonic should now exist in exactly two places: your steel, and the PEM you
started with.

## Handling

Plate 1 grants firmware-signing authority for every board whose OTP carries this key's
hash. Someone holding it can build and sign firmware those boards will boot. Store it
the way you would store a wallet seed for a comparable amount of money, and note that
to anyone who finds it, that is exactly what it will look like.

Engraving Plate 2 on the back of Plate 1 gives up most of the benefit of splitting them.

## Optional: the boot key as a Nostr identity

The boot key scalar is a valid Nostr secret key: same curve, same range.

```sh
go run seedhammer.com/cmd/sh2key nsec my-key.pem        # print nsec1 and npub1
go run seedhammer.com/cmd/sh2key nsec my-key.pem -nfc   # engrave, per the Nostr how-to
```

Name the consequence before you use this: deriving and using the nsec makes
the boot key double as a Nostr identity. One secret, two protocols. Whoever
holds either form can both sign firmware for the fused boards and act as that
Nostr key, and plate 1 then backs up both. If you would not store your Nostr
identity and your firmware-signing authority in the same vault, derive a
separate Nostr key instead ([`howto-nostr-keys.md`](howto-nostr-keys.md)).

## Restoring

```sh
go run seedhammer.com/cmd/sh2key restore -o my-key.pem
```

Type the 24 words from plate 1. The tool completes words from four letters, offers
only checksum-valid words for the final position, and refuses to write a PEM whose
fingerprint does not match if you pass `-verify`.

Then rejoin the signing flow in
[`howto-bootkey-and-signing.md`](howto-bootkey-and-signing.md) at step 4.

## Troubleshooting

**Checksum fails and you are sure you read the plate correctly.** One word is wrong.
Run `sh2key restore -repair -verify <fingerprint>`. Checksum alone leaves roughly 190
candidates, far too many to guess, but your public key fingerprint is public and
unique, so the tool can test every single-word substitution against it and name the
error. This works only because you engraved the fingerprint on plate 2.

**A word is illegible on the plate.** Enter it as `?`. With the fingerprint, one fully
unknown word is recoverable by search.

**The device does not offer the seed flow after the NFC tap.** Confirm the payload
arrived as plain text. See the NFC notes in the signing howto.

**`restore` writes a PEM but the board rejects the firmware.** The key is not the one
this board was fused with. Compare your fingerprint against the board's OTP rows
before re-flashing anything, and read the boot-key section of the signing howto.

## References

- [`howto-bootkey-and-signing.md`](howto-bootkey-and-signing.md), the ceremony that
  mints the key
- [BIP39](https://github.com/bitcoin/bips/blob/master/bip-0039.mediawiki)
- [SeedQR](https://github.com/SeedSigner/seedsigner/blob/dev/docs/seed_qr/README.md),
  implemented in-tree at `seedqr/seedqr.go`
