#!/usr/bin/env python3
"""
write-nfc — write a text plate composition as an NDEF Text record
using a USB NFC reader (e.g. ACR122U via nfcpy).

The SeedHammer firmware accepts plain Well-Known Text records: each
line of the payload becomes a plate line, engraved at the largest
text size whose grid holds the composition. Compose with the plate
editor (docs/index.html) or any text editor.

The supported charset and grid dimensions are read from the
glyphs.js next to this script, generated from the firmware sources
by "go run seedhammer.com/cmd/textplate".

Usage:
    write-nfc.py plate.txt          # or - for stdin
    echo "IN CASE OF FIRE" | write-nfc.py -

Requires: pip install nfcpy ndeflib
"""

import json
import pathlib
import sys
import time

import ndef
import nfc

TAP_TIMEOUT_S = 30


def font_data():
    js = pathlib.Path(__file__).resolve().parent / "glyphs.js"
    try:
        raw = js.read_text()
    except OSError as e:
        sys.exit(f"cannot read {js}: {e}")
    return json.loads(raw[raw.index("{") : raw.rindex(";")])


def canonical(text: str) -> list[str]:
    """Match the firmware's parsePlainText canonicalization: CRLF/CR to
    LF, strip trailing spaces per line, drop trailing blank lines."""
    text = text.replace("\r\n", "\n").replace("\r", "\n")
    lines = [l.rstrip(" ") for l in text.split("\n")]
    while lines and lines[-1] == "":
        lines.pop()
    return lines


def validate(lines: list[str], sh: dict) -> float:
    charset = set(sh["glyphs"])
    bad = sorted({ch for line in lines for ch in line if ch not in charset})
    if bad:
        sys.exit("not engravable: " + " ".join(repr(c) for c in bad))
    if not any(line.strip() for line in lines):
        sys.exit("nothing to engrave")
    if lines[0].startswith("command: "):
        sys.exit('the firmware reads a leading "command: " as a debug command')
    cols = max(len(line) for line in lines)
    for size in sh["sizes"]:
        if cols <= size["cols"] and len(lines) <= size["rows"]:
            return size["mm"]
    largest = sh["sizes"][-1]
    sys.exit(
        f"does not fit any plate size: {cols}x{len(lines)}, "
        f"largest grid is {largest['cols']}x{largest['rows']}"
    )


def write(text: str) -> None:
    result = {}

    def on_connect(tag):
        if tag.ndef is None:
            result["error"] = "target is not NDEF-formatted"
            return True
        if not tag.ndef.is_writeable:
            result["error"] = "target is read-only"
            return True
        try:
            tag.ndef.records = [ndef.TextRecord(text, language="en")]
        except Exception as e:
            result["error"] = f"write failed: {e}"
            return True
        result["bytes"] = tag.ndef.length
        return True

    with nfc.ContactlessFrontend("usb") as clf:
        print("hold a tag or the SeedHammer against the reader...", file=sys.stderr)
        deadline = time.monotonic() + TAP_TIMEOUT_S
        tag = clf.connect(
            rdwr={"on-connect": on_connect},
            terminate=lambda: time.monotonic() > deadline,
        )
        if tag is None and not result:
            sys.exit("no target detected")
    if "error" in result:
        sys.exit(result["error"])
    print(f"written {result['bytes']} bytes", file=sys.stderr)


def main():
    if len(sys.argv) != 2:
        sys.exit(__doc__.strip())
    src = sys.stdin if sys.argv[1] == "-" else open(sys.argv[1], encoding="utf-8")
    with src:
        lines = canonical(src.read())
    mm = validate(lines, font_data())
    print(
        f"{max(len(l) for l in lines)}x{len(lines)} characters, "
        f"engraves at {mm}mm",
        file=sys.stderr,
    )
    write("\n".join(lines))


if __name__ == "__main__":
    main()
