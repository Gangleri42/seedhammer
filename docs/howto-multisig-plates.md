# Split a multisig descriptor across its cosigner plates

Scan a multisig setup and the machine offers to cut one plate per cosigner
instead of a single descriptor plate. Any signing quorum of those plates
recovers the full descriptor: for a 2-of-3, any two plates. A cosigner who
holds enough seed plates to spend also holds enough steel to reconstruct the
wallet, with no descriptor plate as a separate single point of loss.

The split is Shamir's secret sharing over a BBQr transport, specified in
[shamir/SPEC.md](../shamir/SPEC.md): below the quorum the shares reveal
nothing about the descriptor, mathematically, no matter how many sit in a
stranger's drawer. What a lost plate does reveal is its engraved pairing
header: plate number, threshold, cosigner fingerprint, wallet title. That
names whose wallet the plate belongs to, not what is in it.

## What you need

- A SeedHammer II running the [fork firmware](../README.md#firmware-downloads).
- The multisig setup, delivered over NFC: tap a Coldcard's export directly,
  or write the descriptor string as a text record with
  [`write-nfc.py --raw`](../cmd/textplate/write-nfc.py) or from
  [Studio](https://gangleri42.github.io/studio/). A wallet
  [built on the machine itself](howto-multisig-wallet.md) arrives at the
  same split screens without scanning anything.
- One blank plate per cosigner, plus spares for extra copies. Engraving the
  share on the back of each cosigner's seed plate works and keeps the pair
  physically inseparable.

## Any quorum

Every quorum from 2-of-2 up splits into one share per cosigner: the scheme
is exact for any threshold, so there is no table of supported shapes. All
shares of a set are the same size, a little over the compressed descriptor,
and the set engraves at one matched text size and QR scale, chosen so the
largest plate fits. Very wide wallets eventually outgrow the plate (around
8-of-13 with plain xpubs); the machine says so before anything moves.

A 1-of-n wallet offers N FULL COPIES instead: a 1-of-n split is just
copies, and the plain descriptor plate states that honestly while engraving
less.

A share plate as engraved, the pairing header and the BBQr part text
wrapped around its code:

![Cosigner 1's share plate of a 2-of-3](images/plate-share-1of3.png)

## On the machine

1. **Scan.** Hold the tag or phone against the reader while the start screen
   shows. The descriptor screen lists the wallet title, quorum, and script
   type; check them against your setup and press the checkmark. A Notice
   line appears when nothing verified the transcription: descriptor text
   that arrived without its checksum, a BlueWallet export, a bare account
   key. The machine accepts it anyway; compare it with the wallet before
   engraving. A descriptor the machine refuses (a quorum that cannot spend,
   a hardened child after an xpub) lands in the text flow with the reason
   named on its hold gate.

   ![The descriptor screen before the split choice](images/msw-14-descriptor.png)

2. **Choose the split.** A multisig offers ONE PLATE (the single descriptor
   plate, exactly as before) and SPLIT: N PLATES. The lead line states the
   recovery rule, e.g. "Any 2 of 3 plates recover". The split then offers
   its plate styles the way the single plate does, listing what fits:
   TEXT + QR (part text wrapped around the code), TEXT ONLY (the pure
   hand-transcription plate: header plus the part strings as engraved
   text), and QR ONLY (header plus bare codes, the densest plate). One
   choice per set; every plate of the run uses it.

   ![One plate or the split](images/msw-15-split-choice.png)
   On the ONE PLATE path the machine also asks for the plate size when the
   descriptor fits the small 85 x 55 mm format (which sits in the clamp on
   the printable [jaw](../hardware/small-plate-jaw/)); the variant list is
   then the small plate's own, so TEXT + QR may drop away. Splits and full copies
   stay on the square plate.
3. **Per plate.** Each plate opens on a gate titled "Plate k of N" with the
   cosigner fingerprint it belongs to. ENGRAVE PLATE plans the layout and
   hands over to the engrave screen, which shows the share as it will be
   cut (the header, the wrapped text and the code) with its dimensions and
   duration. Insert the blank (or the cosigner's seed plate, back side up),
   close the lock, hold the button. SKIP leaves a cosigner without a share:
   the issued plates still recover at the same threshold, there are simply
   fewer of them, and the machine warns before finishing a session whose
   plate count fell below the threshold itself.
4. **Copies.** After each finished plate: NEXT PLATE (or DONE on the last)
   moves on, ANOTHER COPY routes back through the gate to cut the same share
   again. Copies of the same plate are identical and interchangeable.
5. **One session, one set.** The split draws fresh randomness every time,
   so only plates cut in the same session combine; the shared session tag
   in the header is the marker. An aborted set is not resumed, it is
   re-cut: split again and engrave every plate, and scrap the orphans of
   the earlier tag. Before any plate is offered, the machine recovers the
   descriptor from its own freshly cut shares in memory and refuses the
   session if that fails.

Share plates carry no secrets in their codes, so the middle button's hide
toggle matters less here than on a seed plate; it is there on every plate
for the times you would rather the room did not read the screen.

## Keep the pairing straight

**Share k belongs to cosigner k.** The header on every plate names its plate
number, the recovery rule (ANY 2), its cosigner fingerprint, and the split
session tag (#5C71): plates of one set share one tag. Keep each share with
that cosigner's seed plate. Recovery needs a quorum of *different* shares:
two copies of the same share count once, so a swapped pair of plates can
turn a valid quorum of cosigners into an insufficient set of steel. The
full-copy fallback has no such rule; those plates are all the same.

## Recover into a wallet

The share format is this project's proposed extension to BBQr, so today no
phone wallet reads the plates directly; the recovery paths are the machine
and any computer, and the descriptor they output then enters wallets the
usual ways.

- **On the machine.** Deliver each plate's code content over NFC (a
  phone NFC app, `write-nfc.py`, or Studio), one plate per tap. The
  progress line counts SHARE 1 OF 2; at the quorum the descriptor
  screen opens as if the wallet had been scanned whole, ready to
  re-engrave or to build plates.
- **On a computer.** Collect each plate's code into a file, one line
  per QR, a blank line between plates, and run
  `bbqr combine -descriptor` from [cmd/bbqr](../cmd/bbqr). It prints
  the wallet descriptor as text and, given a spare plate beyond the
  quorum, survives one corrupt share and names which plate is bad.

The engraved part text, on the TEXT + QR and TEXT ONLY styles, is the
exact QR content, character for character, so hand transcription
substitutes for a camera; TEXT ONLY fits every quorum the machine can
split, so camera-free recovery is a choice of plate style, not a size
accident. QR ONLY plates read back with any phone's scanner. Either way
a misread fails loudly: the shares carry an integrity check that a
wrong or damaged plate cannot pass unnoticed.

## Fine print

- The wire format is [specified](../shamir/SPEC.md) and pinned by test
  vectors in this repo: plates cut today remain decodable by tomorrow's
  firmware, or by anyone implementing the spec.
- Share codes engrave at level L error correction and the set's one QR scale,
  like the single descriptor plate.
- The single-plate flow is unchanged and remains the right choice when one
  plate in a safe place serves the whole quorum.
