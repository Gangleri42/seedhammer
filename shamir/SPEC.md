# BBQr file type M: Shamir shares

A threshold extension to [BBQr](https://github.com/coinkite/BBQr): any
data is compressed, sealed with its file type and an integrity digest,
split into a k-of-n Shamir scheme, and each share is carried by its
own independent BBQr series of file type `M`. Any set of fewer than k
shares reveals nothing about the data beyond the metadata listed in
the Security section (the threshold, a random tag, and the exact
sealed length), in the information-theoretic sense. BBQr's framing is
unchanged: transport, encodings and chunking work exactly as for any
other file type, and the recovered result is an ordinary (file type,
data) pair that re-enters BBQr dispatch.

The file type `M`, as in m-of-n, is currently reserved upstream; this
document is the proposal for its assignment ('S' is out: Coldcard key
teleport already rides BBQr with de facto codes R, S and E). The
extension convention it uses,
claim one type char, define a fixed payload prefix, recover to an
inner (type, data) pair, is reusable by future extensions. Decoders
without the extension reject a type `M` series as unknown instead of
presenting a single share to the user as if it were their data.

This document is the specification. The Go package beside it
(`seedhammer.com/shamir`) is the reference implementation, and
`testdata/vectors.json` holds cross-implementation test vectors.

## Encoding pipeline

```
data, file type F
  │  1. compress if it shrinks
  ▼
payload  = deflate(data)   if shorter, else data
  │  2. seal type and digest with the payload
  ▼
sealed   = T ‖ payload ‖ digest
           T      = F, with bit 7 set iff compressed
           digest = SHA-256(k ‖ T ‖ payload)[0:4]
  │  3. Shamir split over GF(256)
  ▼
shares   = y values of sealed at x = 1..n
  │  4. envelope
  ▼
tag ‖ x ‖ k ‖ y values, the payload of one type M BBQr series
```

### 1. Compression

Raw DEFLATE (no zlib wrapper), as for BBQr's `Z` encoding. Compression
happens before splitting because shares are indistinguishable from
random and do not compress. The DEFLATE stream must stay within the
reach of a 1 KiB-window decoder, matching BBQr's `wbits=10`
constraint: every back-reference stays within 1024 bytes (a
Huffman-only stream satisfies this vacuously).

### 2. Sealed content

The split input is `T ‖ payload ‖ digest`. `T` carries the data's
ordinary BBQr file type in bits 0-6 (`A`..`Z`) and the compression
flag in bit 7, so nothing about the data, not even whether it
compressed, is visible below the threshold, and recovery yields a
typed result. `digest` is the first 4 bytes of the SHA-256 over the
threshold byte k, `T` and the payload.

Shamir's scheme has no error detection: a corrupt share yields a
plausible but wrong secret. The digest detects that, disambiguates
mixed-up share sets, and, by binding k and `T`, makes a tampered
threshold byte or sealed type fail like any other corruption. Because
the digest sits inside the shared secret, holders of fewer than k
shares learn nothing from it and gain no way to verify a guessed
secret against it.

The digest is not a message authentication code. Lagrange combination
is linear, so a shareholder who also knows the original data can craft
a substitute share that steers a recovery they participate in to data
of their choice, digest included. The scheme detects accidents, not
insiders; recoveries that must resist a hostile shareholder need an
out-of-band check of the result.

### 3. Split

For each byte `s` of `sealed`, sample a random polynomial over GF(256)
(the Rijndael field, polynomial `0x11b`):

```
p(X) = s + c1·X + ... + c[k-1]·X^(k-1)
```

with `c1..c[k-1]` uniform random bytes, and evaluate it at `x = 1..n`,
with `2 <= k <= n <= 255`. Share `i` is `p(x_i)` per byte. The point
x=0 is never issued; it would be the secret itself. A 1-of-n split is
invalid: its shares would be plain copies, which vanilla BBQr series
of the data's own type express honestly.

Coefficient bytes are hedged against a failed random source: each byte
of the random stream is XORed with the matching byte of a PRF stream
keyed by the sealed content,

```
prk       = HMAC-SHA256(key = "seedhammer.com/shamir hedge v0", sealed)
stream    = HMAC-SHA256(prk, u64be(0)) ‖ HMAC-SHA256(prk, u64be(1)) ‖ ...
```

(SP 800-108 counter mode). With a working source the XOR leaves the
coefficients uniform and independent of the data; with a failed one
they degrade to the PRF stream instead of to known values, so a share
verifies a guessed secret rather than revealing the secret outright.

The split consumes the random stream in order: the k-1 coefficients of
each sealed byte in order, then 2 bytes of share tag, which is not
hedged (it is public metadata; see Security). Fixing the stream
reproduces a split exactly; the test vectors record the raw stream,
before hedging.

### 4. Envelope and transport

```
offset  field       size
0       tag         2       random, big endian, common to one split
2       index       1       x coordinate of this share, 1..255
3       threshold   1       k, at least 2
4       share data  ...     1 + len(payload) + 4 y values of sealed
```

Fixed overhead is 9 bytes per share: the 4-byte prefix plus the sealed
type byte and digest. The envelope rides one BBQr series of file type
`M` in base 32 encoding (`2`); shares are uniformly random, so the `Z`
encoding could never shrink them. Constraints a parser must enforce:
`k >= 2`; `index >= 1`; share data at least 6 bytes. Within a set, all
shares must agree on tag, k and length, and have distinct indices.

All shares of one split have equal envelope length, hence identical
BBQr part counts and QR versions: a split renders as a visually
uniform family of codes. A 32-byte secret split 2-of-4 fits a single
version 5 QR per share.

The textual form of a share is its BBQr part string(s), verbatim,
including the 8-character header; a share transcribed by hand
re-enters as those strings. Receivers dispatch on the series file
type: only type `M` payloads are share envelopes, and the envelope
does not self-identify.

## Decoding pipeline

Join each share's BBQr series in any order (vanilla BBQr). Group
envelopes by tag, threshold and length; collect until k distinct
indices are held. The threshold on every share tells the collector
when to stop; the total issued count n is deliberately not on the
wire, since recovery does not need it. Reconstruct `sealed` by
Lagrange interpolation at x=0 over GF(256):

```
sealed_byte = Σ_i y_i · Π_{m≠i} x_m / (x_m ⊕ x_i)
```

Verify the digest, then, if bit 7 of `T` is set, inflate the payload
under a size cap. The digest covers the compressed payload, so only
verified content is ever inflated.

With more than k shares held and a failed digest, exclude each member
of the failed combination in turn for a spare and retry: at most k
further attempts. One corrupt share among spares is thereby survived,
and its index is known and should be reported, so the holder learns
which plate to re-make instead of only that recovery failed.

## Security

- Privacy below the threshold is information-theoretic, scoped by the
  public metadata: any k-1 shares are consistent with every value of
  every sealed byte (Shamir's scheme; see the bijection test in the
  reference implementation). Visible to anyone holding any number of
  shares: k, the split tag, and the exact sealed length. Padding is
  not applied; data whose length must not leak needs padding before
  splitting. The file type and the compression flag are sealed.
- The random source must be cryptographic. On SeedHammer it is the
  RP2350 TRNG. The coefficient hedge (section 3) bounds the damage of
  a failed source: privacy degrades from information-theoretic to
  computational instead of collapsing, at the price that a holder of
  one share can then verify a guessed secret.
- The tag is drawn from the raw source, never hedged and never derived
  from the data: a data-derived tag would hand any single-share holder
  a guess-verification oracle, and a hedged one would be a public
  function of the secret under exactly the failure the hedge exists
  for. Under a failed source, tags collide across splits and set
  separation falls back to the digest.
- The digest provides integrity against accidents only; section 2
  states the insider-forgery caveat. False accept probability is
  2^-32 per combination attempt.
- A compressed payload can inflate to many times its share size.
  Receivers bound the decompressed size before use; the reference
  implementation caps it at 1 MiB by default (`Set.Limit`). Memory
  constrained receivers also bound the retained share bytes before
  collecting: the threshold announced by the first share times the
  envelope size is the set's eventual footprint.
- Generators must verify a split before committing it to its medium by
  recovering through the receiving path from subsets covering every
  share. Engraved steel is write-once; the reference implementation
  refuses to emit shares that do not round-trip.
- The reference implementation is not constant time (table-driven
  GF(256), data-dependent branches) and does not zeroize secret
  material. Its intended deployment is a single-user air-gapped
  device whose output is a plaintext plate; co-resident attackers are
  out of scope.

## Conformance and test vectors

`testdata/vectors.json` lists, per vector: the data and its file type,
k and n, the exact random byte stream consumed, and the resulting
envelopes with their BBQr parts.

Generator conformance: from (file type, data, k, n, rand stream),
reproduce the envelopes byte for byte. This deliberately pins the
hedge and the stream consumption order, both invisible on the wire,
because vectors are the only way to catch a silently skipped hedge.
The BBQr parts in the vectors additionally pin the reference part
splitting (base 32, minimum version 5); a generator with different
part sizing is interoperable but must reproduce at least the
envelopes.

Receiver conformance: recover the (file type, data) pair from every
k-subset of a vector's shares, in any order of shares and parts;
reject any set below k as incomplete; enforce the parser constraints
of section 4.
