#!/usr/bin/env python3
"""Generate bbqr/testdata/vectors.json from the Coinkite reference.

Development tool only; not run in CI. Requires the reference
implementation (https://github.com/coinkite/BBQr, python/) and its
pyqrcode dependency:

    python3 -m venv venv && venv/bin/pip install pyqrcode
    BBQR_REF=/path/to/BBQr/python venv/bin/python gen_vectors.py > vectors.json
"""
import json
import os
import sys

sys.path.insert(0, os.environ.get("BBQR_REF", "."))

from bbqr.split import split_qrs  # noqa: E402
from bbqr.utils import version_to_chars  # noqa: E402

HERE = os.path.dirname(os.path.abspath(__file__))
TEST_DATA = os.path.join(os.environ.get("BBQR_REF", "."), "..", "test_data")

vectors = []


def add(name, data, file_type, encoding=None, **opts):
    full = dict(min_version=5, max_version=40, min_split=1, max_split=1295)
    full.update(opts)
    ver, parts = split_qrs(data, file_type, encoding=encoding, **full)
    vectors.append({
        "name": name,
        "data_hex": data.hex(),
        "file_type": file_type,
        "encoding_opt": encoding or "",
        "opts": full,
        "version": ver,
        "encoding": parts[0][2],
        "parts": parts,
    })


def add_file(name, path, file_type, encoding=None, **opts):
    with open(path, "rb") as f:
        add(name, f.read(), file_type, encoding, **opts)


# Reference test files: compressible PSBTs and transactions exercise Z,
# incompressible tails exercise the base32 fallback. The two largest
# files are only encoded in the default mode to keep vectors.json
# small; their hex and base32 splits differ only in size.
for fn, ft in [
    ("1in2out.psbt", "P"),
    ("devils-txn.txn", "T"),
    ("nfc-result.txn", "T"),
    ("last.txn", "T"),
    ("1in10out.psbt", "P"),
    ("1in100out.psbt", "P"),
]:
    path = os.path.join(TEST_DATA, fn)
    add_file(fn + "-auto", path, ft)
    add_file(fn + "-hex", path, ft, encoding="H")
    add_file(fn + "-base32", path, ft, encoding="2")
for fn, ft in [("1in1000out.psbt", "P"), ("signed.txn", "T")]:
    add_file(fn + "-auto", os.path.join(TEST_DATA, fn), ft)

# Synthetic edge inputs.
add("one-byte", b"\x42", "B")
add("text-auto", b"The quick brown fox jumps over the lazy dog. " * 20, "U")
add("text-zlib", b"The quick brown fox jumps over the lazy dog. " * 20, "U", encoding="Z")

# Deterministic pseudorandom (incompressible) inputs, including sizes
# at QR version split boundaries for version 5 (146 base32 chars per
# part, 144 after alignment).
import random
rng = random.Random(42).randbytes(2048)
add("random-32", rng[:32], "B")
add("random-91", rng[:91], "B")   # largest single part at min version 5
add("random-92", rng[:92], "B")   # spills at version 5, fits one version 6
add("random-2048", rng, "B")      # multiple parts at any version

# Option variations.
add_file("1in20out-minsplit3", os.path.join(TEST_DATA, "1in20out.psbt"), "P", min_split=3)
add_file("1in20out-maxver10", os.path.join(TEST_DATA, "1in20out.psbt"), "P", max_version=10)
add_file("1in20out-minver20", os.path.join(TEST_DATA, "1in20out.psbt"), "P", min_version=20)

print(json.dumps({
    "version_chars": [version_to_chars(v) for v in range(1, 41)],
    "vectors": vectors,
}, indent=1))
