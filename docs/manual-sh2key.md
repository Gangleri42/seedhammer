# sh2key

`sh2key` manages the boot key of a SeedHammer II. One binary, two
faces. Launched bare on a terminal it is a full-screen interactive
tool: key and board on one screen, every job behind one keystroke.
Given a command it is a plain CLI for scripts and the howtos. Both
faces run the same code underneath, so the safety rules are identical:
key files are never clobbered, every fuse write is gated on a fresh
readback, and consent to anything irreversible is typed, not clicked.

The ceremonies it automates are documented by hand in
[`howto-bootkey-and-signing.md`](howto-bootkey-and-signing.md) and
[`howto-bootkey-backup.md`](howto-bootkey-backup.md). Read them to
know what the tool does before trusting it.

## Running it

From the repo checkout:

```sh
go run seedhammer.com/cmd/sh2key            # the interactive tool
go run seedhammer.com/cmd/sh2key <command>  # the scripting face
```

The paper commands (backup, restore, nsec, mint) need nothing else.
The device commands (status, flash, provision, enable-secure-boot)
need `picotool` 2.0 or later, which `nix develop` provides in this
repo's shell; `sign` needs no device and no picotool. Sending plates
to the machine (`-nfc`, or the engrave keys in the tool) runs
`cmd/textplate/write-nfc.py`, which needs `python3` with `nfcpy` and
`ndeflib` and a USB NFC reader.

First run on Linux: `sh2key setup-udev`, once. It shows and installs
the single udev rule that lets picotool open the board without sudo;
the interactive tool offers the same install right on the board panel
(`i`) when it detects the permission failure. That is the whole host
setup.

## The interactive tool

The home screen shows three panels. The key panel names the local PEM
and its fingerprint, plus any provisioning records. The board panel
shows what the last OTP scan saw: population, secure boot, the four
slots and what each holds, and one verdict line relating this key to
this board. It scans once when the tool opens, and after that only
when asked (`r`), never on a timer. The action list adapts to the
state: with no key, mint and restore lead; actions that cannot run
stay visible with the reason they cannot.

Keys everywhere: arrows move, `enter` selects, `esc` goes back (and
quits from home), `q` quits, `ctrl-l` repaints. Digits pick list rows
directly. Signing and flashing head the action list; the paper
commands and the ceremony follow.

Device steps run synchronously and picotool takes seconds: while one
is running, the footer names it, and the transcript of the step builds
up on screen as it goes.

**Backup** shows the 24 words as the plate grid, the fingerprint with
its plate-2 prefix in bold, and three actions: `w` engraves the words
(the machine offers the seed-plate flow), `i` engraves the
instructions plate, `f` saves the words to a 0600 file.

**Restore** is the word entry: candidates narrow as you type, words
complete at their shortest unambiguous prefix, `enter` accepts a word
that is spelled out but continues into longer ones, `?` marks an
illegible word, and position 24 offers only the eight checksum-valid
words. The result screen verifies against the local key when one
exists, otherwise against a fingerprint you enter (the attached
board's fused slots are shown as the crib); mis-read words are found
by search and named, and `s` saves the PEM.

**Sign** lists the `.uf2` images in the working directory with what
inspection found: unsigned with a signature section, signed and by
which key, sealed twice, unreadable. It always writes
`<name>.signed.uf2` next to the input and never touches the input.

**Flash** scans the board first and then lists only images that board
would actually boot: signed by a key in one of its valid,
non-revoked slots, with a signature that verifies. Each row names the
slot that trusts it. Unsigned and foreign-signed images are left out,
with a count of what was hidden, because flashing one only drops the
board back into BOOTSEL. With secure boot off, or with no scan to
judge by, nothing is filtered.

**Provision** previews its plan from the live scan before running:
which slot, whether anything needs fusing at all, or why it must
halt. With several unsigned images `f` picks the firmware, and when
more than one pristine slot exists `s` picks the slot (only pristine
ones are selectable; the others show why not). **Enable secure
boot** is its own screen with its own gates. Both run the identical
ceremony cores as the CLI, print the same transcript inline, and
collect consent in a typed modal.

With several key PEMs in the directory, the home screen says so and
`k` opens the key picker, annotated with each key's fingerprint and,
when a board is attached, which slot it is fused in.

## The commands

Flags may come before or after the positional argument; the howtos
write `sh2key backup key.pem -nfc` and that order works.

Where several candidates exist, nothing is picked silently. Several
key PEMs in the directory (any name counts if it parses as a
secp256k1 key; `sh2-bootkey.pem` keeps priority) is an error listing
them until `-key` chooses; several unsigned images likewise until one
is named; and `provision -slot` chooses the boot-key slot. The
interactive tool turns each of these into a picker instead: `k` for
the key, `f` for the firmware, `s` for the slot.

### backup

    sh2key backup <key.pem> [-o file|-] [-instructions] [-nfc]

Prints the key as 24 BIP39 words. The words render to a terminal or
an explicitly named sink only: with stdout redirected and no `-o` or
`-nfc`, backup refuses, so a stray pipe or `tee` cannot capture the
key. `-o` writes a 0600 file (never overwrites; `-` for stdout).
`-instructions` emits the plate-2 restore text instead, public and
freely overwritable, sized for a single 5mm plate. `-nfc` sends to
the engraver: words as a seed payload, instructions as a text plate.

### restore

    sh2key restore [-qr file|-] [-o file|-] [-f] [-verify hex]
                   [-repair] [-repair2]

Rebuilds a byte-identical PEM from typed words. On a terminal the
interactive entry runs on `/dev/tty`; from a pipe it reads
whitespace-separated words (full words or unambiguous prefixes; an
ambiguous prefix is an error, never a silent pick); with `TERM=dumb`
it prompts line by line.

`-qr` consumes a SeedQR (24 groups of 4 digits) or CompactSeedQR (32
raw bytes) payload from a file or stdin; any QR reader produces the
payload, this consumes it. `-verify` requires a fingerprint match (16
to 64 hex digits, the plate-2 prefix suffices) and refuses to write
on mismatch. `-repair` finds a single mis-read word by fingerprint
search and names it; `-repair2` extends the search to two words,
which takes minutes; both need `-verify`, since without a fingerprint
the search would return about 190 candidate keys with no way to
choose. One or two words entered as `?` are recovered the same way.

Without `-o`, a terminal gets the fingerprint only (the howto's
plate-verification mode) and a pipe gets the PEM. `-o` writes 0600
and never overwrites; `-f` relaxes that for a non-key file or an
identical key, and a file holding a *different* key is refused even
then.

### nsec

    sh2key nsec <key.pem> [-nfc]

The key as a Nostr identity: prints `nsec1...` and `npub1...`
(terminal only), or `-nfc` sends the nsec and the machine derives the
npub itself and offers both plates. One secret, two protocols: see
the note in the backup howto before engraving this.

### mint

    sh2key mint [-key file]

Mints a fresh key (crypto/rand, rejection-sampled into [1, n-1]) to
`sh2-bootkey.pem` or `-key`, mode 0600, added to `.git/info/exclude`
when inside a checkout. Refuses to touch an existing file.

Mint points at `provision`, not at `backup`, on purpose: the steel
backup is engraved by the machine, and the machine engraves only once
it boots firmware this key signed. Fuse and flash first; the
post-provision summary names the backup command the moment it is
actually possible.

### status

    sh2key status [-key file]

Classifies the attached board from its OTP tuple and writes nothing:
population, secure boot, both flag registers, all four slots with
their hashes and states, and how the local key relates. The
SeedHammer manufacturer key is named when a slot holds it;
classification itself never keys on branding.

### sign

    sh2key sign [uf2] [-key file] [-o out.uf2]

Signs a firmware image entirely in-process: embed the public key,
hash, sign with the in-tree ECDSA, embed the signature. The picosign
flow, without openssl or xxd. Without an argument it globs the working
directory for exactly one unsigned image with a signature section.
Output defaults to `<input>.signed.uf2`; the input is never modified.
Images with three metadata blocks (sealed twice) are refused before
signing, and the output is verified from disk after.

### flash

    sh2key flash [uf2]

Flashes and reboots, with a preflight: on a board that enforces
signatures, an unsigned image or one signed by an untrusted key is
refused by name instead of falling silently back to BOOTSEL. On a
virgin board this is the whole story: no fuses, reversible forever.

### provision

    sh2key provision [uf2] [-key file] [-slot n]

The whole ceremony, skipping what is already done: mints the key if
missing, fuses a slot if the key is not on the board, signs fresh
from the PEM, cross-checks with picotool, flashes. Re-run against a
new build it recognises the fused key and performs zero OTP writes.
Slot writes happen only into slots reading exactly zero (or resuming
an interrupted write of this same key, a pure 0-to-1 re-assert); all
sixteen rows are read back before the valid bit is set, and the valid
bit is computed from the slot actually written, then written into every
redundant copy of `BOOT_FLAGS1` and verified there. On a locked board
consent is `y`; on a board without secure boot the reversible
`flash` is the loud default and consent means typing the chip id.
A partially fused or foreign board halts with a report.

`-slot` overrides the default lowest-pristine choice and passes the
exact same gate: a slot that is not pristine is refused by name, and
a key already fused somewhere cannot be re-aimed, because OTP is
one-way.

### enable-secure-boot

    sh2key enable-secure-boot [-key file]

Population B's separate one-way door, never part of a provision run.
Gates: the key fused and valid with all sixteen rows matching, a
`*.signed.uf2` in the directory that verifies offline against this
key, picotool agreeing, and the chip id typed back after confirming
the board actually booted the signed image. Then it burns
`CRIT1.SECURE_BOOT_ENABLE` and reads it back. Enabled over a broken
signature chain, a board is bricked forever; that is what the gates
are for.

### setup-udev

    sh2key setup-udev

One-time Linux host setup: installs
`/etc/udev/rules.d/99-picotool.rules`, a one-line rule granting your
user access to Raspberry Pi USB devices (vendor id `2e8a`), so
picotool works without sudo. It prints the exact file content and the
exact three commands first, asks, then runs them through `sudo`,
which prompts for your password itself; the tool never reads it.
Idempotent: an already-current rule means nothing to do. On macOS
there is no udev and nothing to set up.

## Files

| File | What | Written how |
| --- | --- | --- |
| `sh2-bootkey.pem` (or `my-key.pem`, or `-key`) | the private key, SEC1 PEM byte-identical to `openssl ecparam -genkey` | 0600, never clobbered |
| `sh2key.json` | provisioning record next to the key: chip id, slot, population, timestamps; no secrets | rewritten each ceremony |
| `<input>.signed.uf2` | signed firmware copy | overwritten freely |
| OTP JSON for `picotool otp load` | one temp directory per run | never next to the key |

Stale picotool byproducts in a working directory are how wrong hashes
get fused; the tool regenerates everything from the PEM every run and
reads nothing cached.

## Environment

`NO_COLOR` (any value) or `TERM=dumb` disables color; `TERM=dumb`
also swaps the raw-mode entry for line prompts. Non-UTF-8 locales get
ASCII marks. Nothing else is read from the environment.

## Exit status

0 on success, 2 on any error, matching `biptool` and `picosign`.
Errors print as `sh2key: ...` on stderr, and a failed gate always
says what was and was not written.

## What can burn fuses

Only two commands write OTP at all: `provision` (a boot-key slot's
hash rows, then its `KEY_VALID` bit) and `enable-secure-boot`
(`CRIT1.SECURE_BOOT_ENABLE`). Everything else, `status` included,
is read-only on the device. The tool never writes white-label rows
and never touches `KEY_INVALID`.

## Troubleshooting

- **"no RP2350 in BOOTSEL mode"**: unplug, hold BOOTSEL, plug,
  release after a second. OTP state is never inferred from an absent
  device.
- **Device present but cannot be opened**: USB permissions. On Linux
  run `sh2key setup-udev` once (or press `i` on the board panel);
  on macOS retry with sudo.
- **"checksum invalid" on restore**: one word is likely mis-read;
  rerun with `-repair -verify <fingerprint>` and the tool names it.
- **"parses as neither SeedQR nor CompactSeedQR"**: the payload is
  not one of the two formats; export the QR's raw content, not a
  screenshot.
- **write-nfc.py fails**: `pip install nfcpy ndeflib`, plug the USB
  reader, and hold the machine's antenna to it within 30 seconds.
- **"sealed twice"**: the image went through `picotool seal` twice;
  rebuild it. `build-firmware` output and the `*-unsigned` edge asset
  are correct inputs.
- **"KEY_VALID reads 0x… without the bit for slot n"**: the valid-bit
  write reached only part of `BOOT_FLAGS1`'s three redundant copies,
  so the majority vote still reads the slot as invalid. Rerun
  `provision` on the same slot to complete it; re-asserting a hash
  already in place flips no bits. `status` shows the copies.
