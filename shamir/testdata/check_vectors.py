#!/usr/bin/env python3
"""Check shamir/testdata/vectors.json against shamir/SPEC.md.

Independent implementation from SPEC.md, with BBQr.md for the
transport: its own GF(256), hmac and hashlib for the hedge, the
derived stream and the digest, zlib only to inflate. It shares no
code with the Go package, so a vector both agree on pins the format
itself and no single program's reading of it.

Generator class: rebuild sealed from type_byte, payload_hex and the
digest, split it under the vector's profile (randomized: the recorded
rand stream through the hedge; derived: the stream keyed by k and
sealed, no rand_hex), compare every envelope, then size and encode
each envelope as a base 32 BBQr series and compare the part strings.

Receiver class: for every k-subset of the shares, with shares and
parts shuffled, join, parse, interpolate at x=0, verify the digest,
inflate when bit 7 of T is set (checking that compression shrank the
data) and compare with data_hex.

Run from the repo root:

    python3 shamir/testdata/check_vectors.py

One PASS or FAIL line per vector, with the first mismatch; a
malformed vector fails on its own line and the run goes on; exit
status 1 on any FAIL. Development tool, not run in CI, mirroring
bbqr/testdata/gen_vectors.py. Standard library only.
"""
import base64
import binascii
import hashlib
import hmac
import itertools
import json
import os
import random
import sys
import zlib

HEDGE_KEY = b"seedhammer.com/shamir hedge v0"
DERIVED_KEY = b"seedhammer.com/shamir derived v1"
SHARE_TYPE = "M"
HEADER_LEN = 8
MIN_VERSION = 5
MAX_VERSION = 40
INFLATE_LIMIT = 1 << 20
ALNUM_CHARSET = set("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ$%*+-./:")

# Alphanumeric capacity per QR version at error correction level L,
# ISO/IEC 18004 table 7; the "Chars" column of the size table in
# BBQr.md lists the same numbers for the versions it shows.
ALNUM_CAPACITY = [
    None, 25, 47, 77, 114, 154, 195, 224, 279, 335, 395,
    468, 535, 619, 667, 758, 854, 938, 1046, 1153, 1249,
    1352, 1460, 1588, 1704, 1853, 1990, 2132, 2223, 2369, 2520,
    2677, 2840, 3009, 3183, 3351, 3537, 3729, 3927, 4087, 4296,
]


class Mismatch(Exception):
    pass


# GF(256) with the Rijndael polynomial 0x11b (SPEC section 3).

def gf_mul(a, b):
    r = 0
    while b:
        if b & 1:
            r ^= a
        a <<= 1
        if a & 0x100:
            a ^= 0x11B
        b >>= 1
    return r


def gf_inv(a):
    r = 1
    for _ in range(254):
        r = gf_mul(r, a)
    return r


GF_INV = [0] + [gf_inv(a) for a in range(1, 256)]


def gf_div(a, b):
    return gf_mul(a, GF_INV[b])


# Sealed content (SPEC section 2).

def sealed_digest(k, t, payload):
    return hashlib.sha256(bytes([k, t]) + payload).digest()[:4]


def inflate(payload):
    """Raw DEFLATE under the 1 KiB window of SPEC section 1 (wbits=10)
    and the receiver's size cap."""
    d = zlib.decompressobj(-10)
    try:
        data = d.decompress(payload, INFLATE_LIMIT)
    except zlib.error as e:
        raise Mismatch(f"payload does not inflate: {e}")
    if not d.eof or d.unconsumed_tail or d.unused_data:
        raise Mismatch("payload is not one complete DEFLATE stream")
    return data


# Split (SPEC section 3) and the generator profiles (section 3a).

def prf_stream(key, data, length):
    """HMAC-SHA256 counter mode: prk = HMAC(key, data), then the blocks
    HMAC(prk, u64be(0)), HMAC(prk, u64be(1)), ... truncated to
    length."""
    prk = hmac.new(key, data, hashlib.sha256).digest()
    out = bytearray()
    counter = 0
    while len(out) < length:
        out += hmac.new(prk, counter.to_bytes(8, "big"), hashlib.sha256).digest()
        counter += 1
    return bytes(out[:length])


def randomized_stream(sealed, k, rand):
    """Coefficients and tag of the randomized profile: the recorded
    rand stream, its coefficient bytes XORed with the hedge keyed by
    sealed, its last 2 bytes the tag as recorded."""
    ncoef = len(sealed) * (k - 1)
    if len(rand) != ncoef + 2:
        raise Mismatch(f"rand stream is {len(rand)} bytes, the split consumes {ncoef + 2}")
    hedge = prf_stream(HEDGE_KEY, sealed, ncoef)
    coef = bytes(r ^ h for r, h in zip(rand[:ncoef], hedge))
    return coef, rand[ncoef:]


def derived_stream(sealed, k):
    """Coefficients and tag of the derived profile: the PRF stream
    keyed by k (one byte) and sealed, consumed as the randomized
    profile consumes rand, with no hedge."""
    ncoef = len(sealed) * (k - 1)
    stream = prf_stream(DERIVED_KEY, bytes([k]) + sealed, ncoef + 2)
    return stream[:ncoef], stream[ncoef:]


def split(sealed, k, n, coef, tag):
    """Envelopes for x = 1..n: the k-1 coefficients per sealed byte in
    ascending degree from coef, then the tag."""
    envelopes = []
    for x in range(1, n + 1):
        y = bytearray()
        for i, s in enumerate(sealed):
            v = s
            xp = 1
            for c in coef[i * (k - 1):(i + 1) * (k - 1)]:
                xp = gf_mul(xp, x)
                v ^= gf_mul(c, xp)
            y.append(v)
        envelopes.append(tag + bytes([x, k]) + bytes(y))
    return envelopes


# BBQr transport (BBQr.md; SPEC section 4 fixes base 32 and version 5).

def base36(n):
    digits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"
    return digits[n // 36] + digits[n % 36]


def b32(data):
    return base64.b32encode(data).decode("ascii").rstrip("=")


def unb32(s):
    try:
        return base64.b32decode(s + "=" * (-len(s) % 8))
    except binascii.Error:
        raise Mismatch("part is not base 32")


def size_parts(nbytes):
    """Fewest parts, then lowest version, minimum version 5. A part
    holds the version's alphanumeric capacity minus the header,
    rounded down to a multiple of 8 characters (5 bytes), and every
    part but the last is full."""
    best = None
    for version in range(MIN_VERSION, MAX_VERSION + 1):
        chars = ALNUM_CAPACITY[version] - HEADER_LEN
        per_part = chars // 8 * 5
        need = -(-nbytes // per_part)
        if best is None or need < best[0]:
            best = (need, version, per_part)
    return best


def bbqr_parts(envelope):
    need, version, per_part = size_parts(len(envelope))
    parts = []
    for i in range(need):
        chunk = envelope[i * per_part:(i + 1) * per_part]
        part = "B$2" + SHARE_TYPE + base36(need) + base36(i) + b32(chunk)
        if len(part) > ALNUM_CAPACITY[version]:
            raise Mismatch(f"part {i} of {len(part)} chars exceeds version {version}")
        parts.append(part)
    return parts


def join(parts):
    """Vanilla BBQr join of one series, parts in any order."""
    total = file_type = None
    pieces = {}
    for p in parts:
        if not set(p) <= ALNUM_CHARSET:
            raise Mismatch("part uses characters outside the alphanumeric set")
        if p[:3] != "B$2":
            raise Mismatch("part is not base 32 BBQr: " + p[:8])
        ft, tot, idx = p[3], int(p[4:6], 36), int(p[6:8], 36)
        if total is None:
            total, file_type = tot, ft
        elif (tot, ft) != (total, file_type):
            raise Mismatch("parts disagree on total or file type")
        if idx >= total or idx in pieces:
            raise Mismatch(f"part index {idx} out of range or repeated")
        pieces[idx] = unb32(p[8:])
    if len(pieces) != total:
        raise Mismatch(f"{len(pieces)} of {total} parts")
    return file_type, b"".join(pieces[i] for i in range(total))


# Receiver (SPEC section 4 constraints, Decoding pipeline).

def parse_envelope(env):
    if len(env) < 4 + 6:
        raise Mismatch("envelope shorter than 10 bytes")
    tag, x, k = env[:2], env[2], env[3]
    if k < 2:
        raise Mismatch("threshold below 2")
    if x < 1:
        raise Mismatch("index 0")
    return tag, x, k, env[4:]


def interpolate0(points):
    """Lagrange interpolation at x=0, points are (x, y bytes)."""
    length = len(points[0][1])
    out = bytearray(length)
    for i, (xi, yi) in enumerate(points):
        basis = 1
        for m, (xm, _) in enumerate(points):
            if m != i:
                basis = gf_mul(basis, gf_div(xm, xm ^ xi))
        for b in range(length):
            out[b] ^= gf_mul(yi[b], basis)
    return bytes(out)


def recover(envelopes):
    parsed = [parse_envelope(e) for e in envelopes]
    if len({(tag, k, len(y)) for tag, _, k, y in parsed}) != 1:
        raise Mismatch("shares disagree on tag, threshold or length")
    xs = [x for _, x, _, _ in parsed]
    if len(set(xs)) != len(xs):
        raise Mismatch("repeated share index")
    k = parsed[0][2]
    if len(parsed) < k:
        raise Mismatch(f"{len(parsed)} shares below threshold {k}")
    sealed = interpolate0([(x, y) for _, x, _, y in parsed[:k]])
    t, payload, digest = sealed[0], sealed[1:-4], sealed[-4:]
    if digest != sealed_digest(k, t, payload):
        raise Mismatch("digest mismatch")
    if t & 0x80:
        data = inflate(payload)
        if len(payload) >= len(data):
            raise Mismatch(f"compressed payload of {len(payload)} bytes does not shrink {len(data)}")
    else:
        data = payload
    return chr(t & 0x7F), data


def first_diff(a, b):
    for i, (x, y) in enumerate(zip(a, b)):
        if x != y:
            return i
    return min(len(a), len(b))


def check(v):
    data = bytes.fromhex(v["data_hex"])
    k, n = v["k"], v["n"]
    profile = v["profile"]
    t = bytes.fromhex(v["type_byte"])[0]
    payload = bytes.fromhex(v["payload_hex"])
    if chr(t & 0x7F) != v["file_type"]:
        raise Mismatch(f"type byte {t:02x} does not carry file type {v['file_type']}")
    if t & 0x80:
        if inflate(payload) != data:
            raise Mismatch("payload does not inflate to data_hex")
        if len(payload) >= len(data):
            raise Mismatch(f"compressed payload of {len(payload)} bytes does not shrink {len(data)}")
    elif payload != data:
        raise Mismatch("uncompressed payload differs from data_hex")

    # Generator class.
    sealed = bytes([t]) + payload + sealed_digest(k, t, payload)
    if profile == "randomized":
        coef, tag = randomized_stream(sealed, k, bytes.fromhex(v["rand_hex"]))
    elif profile == "derived":
        if "rand_hex" in v:
            raise Mismatch("derived vector records a rand stream")
        coef, tag = derived_stream(sealed, k)
    else:
        raise Mismatch(f"unknown profile {profile!r}")
    envelopes = split(sealed, k, n, coef, tag)
    if len(v["shares"]) != n:
        raise Mismatch(f"{len(v['shares'])} shares recorded for n={n}")
    for i, (env, share) in enumerate(zip(envelopes, v["shares"]), 1):
        want = bytes.fromhex(share["envelope_hex"])
        if env != want:
            raise Mismatch(f"share {i} envelope differs at byte {first_diff(env, want)}")
        parts = bbqr_parts(env)
        if parts != share["parts"]:
            got = [len(p) for p in parts]
            want = [len(p) for p in share["parts"]]
            if got != want:
                raise Mismatch(f"share {i} parts differ: {len(parts)} parts of {got} chars, want {len(share['parts'])} of {want}")
            p = next(j for j, (a, b) in enumerate(zip(parts, share["parts"])) if a != b)
            raise Mismatch(f"share {i} part {p} differs at character {first_diff(parts[p], share['parts'][p])}")

    # Receiver class.
    rng = random.Random(v["name"])
    for subset in itertools.combinations(range(n), k):
        order = list(subset)
        rng.shuffle(order)
        envs = []
        for i in order:
            parts = list(v["shares"][i]["parts"])
            rng.shuffle(parts)
            ft, env = join(parts)
            if ft != SHARE_TYPE:
                raise Mismatch(f"share {i + 1} series has file type {ft}")
            envs.append(env)
        ft, got = recover(envs)
        if ft != v["file_type"] or got != data:
            raise Mismatch(f"subset {[i + 1 for i in order]} recovers wrong content")
        if len(subset) > 1:
            envs.pop()
            try:
                recover(envs)
            except Mismatch:
                continue
            raise Mismatch(f"{k - 1} shares recovered without error")

    # A receiver that skipped the digest would pass every vector above;
    # one corrupted envelope among a threshold must fail on the digest.
    envs = [bytes.fromhex(s["envelope_hex"]) for s in v["shares"][:k]]
    bad = bytearray(envs[0])
    bad[-1] ^= 0xFF
    try:
        recover([bytes(bad)] + envs[1:])
    except Mismatch as e:
        if "digest" not in str(e):
            raise Mismatch(f"corrupt share failed for the wrong reason: {e}")
    else:
        raise Mismatch("corrupt share recovered without a digest failure")


def main():
    path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "vectors.json")
    with open(path) as f:
        vectors = json.load(f)["vectors"]
    failed = False
    for v in vectors:
        name = v.get("name", "?")
        try:
            check(v)
            print("PASS", name)
        except Mismatch as e:
            failed = True
            print("FAIL", name + ":", e)
        except (ValueError, IndexError, TypeError) as e:
            # A hex field that does not decode, a header digit outside
            # base 36, or a field of the wrong shape: the vector is
            # malformed.
            failed = True
            print("FAIL", name + ": malformed vector:", e)
        except KeyError as e:
            failed = True
            print("FAIL", name + ": missing field", e)
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
