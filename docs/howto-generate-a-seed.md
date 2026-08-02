# Generate a seed phrase with nothing but the machine

Draw your own words by hand, type them in, and let the machine compute the last
one. No computer, no phone, no second signing device.

## Why the last word is different

The final word carries a checksum over all the others, so it cannot be drawn
like the rest: of the 2048 words, only **128** complete a 12-word phrase and
only **8** complete a 24-word one. Draw that word yourself and the phrase is
almost certainly invalid.

Computing it is arithmetic over the words you already have, not a secret.

## Where the words come from

[SeedSticks](https://seedsticks.org/) is the recommended source: a bag of 1024
double-sided sticks carrying the whole BIP39 list, two words per stick, drawn
by hand. It is worth using because the randomness is a physical process you can
watch from start to finish, with nothing to trust and nothing to verify
afterwards.

[SeedPills](https://github.com/SeedSigner/SeedPills) is the same thing printed
at home: a script generates a model for OpenSCAD, and a full set is likewise
1024 two-sided pieces. Dice against a printed conversion table work too, and so
do coin flips, at eleven flips per word.

Because a piece carries two words, drawing one is two decisions and only the
first is made in the bag. **Take the face that lands up.** Do not turn it over
and keep the word you prefer. The piece is worth 10 bits and the face is worth
the eleventh, so choosing the face yourself throws away a bit per word: 230
across 23 draws instead of 253. Still far beyond anyone's reach, and still free
not to lose. Neither project spells this out, so it is on you.

Draw **11 words** for a 12-word phrase, or **23** for a 24-word one. How you
draw them is the next section, and it is the part that matters.

## The draw is the whole secret

Everything else in this document is housekeeping. The security of the phrase is
decided in the seconds you spend with your hand in the bag, and nothing
downstream can add to it or repair it.

**Mix before every draw, not once at the start.** Tiles you handled recently sit
where you left them. A bag stirred once and then dipped into twenty-three times
produces draws that are related to each other, which is exactly what you are
trying to avoid.

**Draw blind.** No looking into the bag. No feeling around for a shape or an
edge, no sorting through to the bottom, no taking one from the middle because
the top ones "were just in there". Close your fingers on the first tile they
touch and pull it out.

**Never reject a draw.** Not because the word repeats, not because you dislike
it, not because it feels wrong for a wallet. Rejecting a tile is choosing, and
choosing is the one thing that actually destroys this.

Repeats are normal, not a signal: draw 23 words and there is about a **12%**
chance at least one appears twice (2.7% for 11 words). A phrase with a repeat is
as strong as any other.

**Put the tile back.** This one is worth being honest about: skipping it costs
you 0.36 bits out of 253, so it is not what will hurt you. Follow it anyway,
because it keeps every draw identical to every other and it removes the excuse
to re-draw a word you have already seen.

### How much margin you have

A clean blind draw is 11 bits per word, 253 bits across 23 words. Mechanical
imperfection — a bag not stirred quite enough, tiles worn unevenly — shaves some
of that off, and the margin is enormous: even losing *half* your entropy leaves
126 bits, which nobody is searching.

What has no margin is substituting choice for chance. Words you picked, or
draws you filtered, do not come from 2048 possibilities each; they come from
whatever your mind offers up, which is a small set an attacker can grind through
directly. That failure does not shave bits off the total, it moves the phrase
into a searchable space no matter how many words it has.

So: imperfect mixing is survivable. Deciding anything is not.

### Then check what you typed

The keyboard cannot accept a word that does not exist, so typos and half-words
are not a risk. What it cannot know is *which* word you drew. Enter ADDRESS where
your tile says ADDICT, or two words out of order, and you get a valid phrase for
a different wallet — restoring would almost always catch that, but generating
cannot, because the checksum is computed from what you entered. Read the list
against your tiles before confirming; order counts as much as spelling.

Draw somewhere unobserved, and put the tiles away before anyone comes in. Write
down the machine's word alongside your own. After engraving, read the plate back
against the screen, then destroy the paper.

## On the machine

1. The start screen reads **Backup Wallet**. Press the checkmark, then choose
   **12 WORDS** or **24 WORDS** under *Choose number of words*.
2. Type your drawn words in order. Letters that cannot lead to a word go dim,
   and once the letters resolve to exactly one word a checkmark appears; press
   it to accept the word and move to the next.
3. On the final word, with nothing typed, the screen offers a random last word.
   Take the middle button. Typing a word instead hides the offer; clearing the
   box brings it back.
4. A confirmation appears before anything is drawn. Hold the bottom button for
   a second to go ahead; the top button backs out.
5. The machine draws a word and shows it. The middle button draws a different
   one, the top button backs out, the bottom button accepts.
6. Check every word against your draw, then confirm to engrave.

## Set up the watch-only wallet

Once the seed plate is done, the machine offers the wallet descriptor: choose an
address type, or back out if you do not want one.

Segwit (`wpkh`, m/84'/0'/0') is understood by every wallet in circulation.
Taproot (`tr`, m/86'/0'/0') is newer, with better privacy and fees, and slightly
narrower support. Nested segwit (m/49') and legacy (m/44') are offered too, for
a seed destined for a system that only speaks an older standard. All four derive
from the same seed, so choosing one now does not rule out the others later.

Each type carries its standard path, which is the whole answer for a wallet you
are creating here. **ADVANCED** is for one that already exists somewhere else: it
lets you pick the address type and then edit the derivation path, prefilled with
the standard one, for a wallet set up at a second account or a different depth.
Check the path and fingerprint on screen against that wallet; the machine has no
way to check them for you.

The descriptor appears as a QR code with its type, master fingerprint and
derivation path beside it. Scan the code into a wallet on your phone or desktop
and you have a watch-only wallet: it can show balances, generate receive
addresses and build transactions, but it cannot spend, because the seed never
left the machine. Check the fingerprint on screen against what the wallet
reports after import; they must match.

The bottom button engraves the descriptor as a plate of its own, which is worth
doing for a wallet you intend to keep — restoring from a seed is easier when you
also know which address type it was set up with.

**A descriptor is not a seed, but it is not nothing.** It cannot move funds. It
does reveal every address the wallet will ever use, so anyone holding it can see
your whole balance and transaction history forever. Treat it as private
information, just not as a key.

## What the machine's randomness is worth

Honestly: very little, and that is the point.

In a 24-word phrase your 23 drawn words fix **253 bits**. The machine
contributes **3**, for 256 in total. In a 12-word phrase it is 121 from you and
7 from the machine, for 128.

Now suppose the machine's generator is bad — biased, broken, or backdoored. The
worst case is that its 3 bits are fully predictable, which leaves an attacker
facing your 253. Nobody brute-forces 253 bits. The randomness of the last word
is close to irrelevant to the strength of your seed.

That is the whole argument for generating a phrase this way. **The machine
touches three bits of your secret and cannot influence the other 253.** A device
that generates all 24 words holds your entire wallet inside its random number
generator, and a flaw there is total and undetectable. Here the blast radius is
three bits, whatever the hardware does.

For what it is worth, those bits come from the RP2350's hardware generator,
which conditions and health-checks its own output in silicon; if the checks
fail, the machine reports it and draws nothing rather than falling back on
something weaker. That is a reasonable design, but the reason you can relax
about it is the arithmetic above, not the datasheet.

The same arithmetic cuts the other way, and this is the real risk: **the machine
cannot rescue a bad draw.** Eleven words chosen because they were memorable, or
drawn from a bag you did not mix, produce a weak seed no matter what the machine
adds to the end of it. Every bit of security in the phrase is a bit you
produced.
