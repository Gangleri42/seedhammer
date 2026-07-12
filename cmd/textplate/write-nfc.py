#!/usr/bin/env python3
"""
write-nfc — write a text plate composition to the SeedHammer using a
USB NFC reader (e.g. ACR122U via nfcpy).

By default the composition is written as an NDEF Text record: each
line of the payload becomes a plate line, engraved at the largest
text size whose grid holds the composition. With --curves the
composition is compiled to a seedhammer.com:curves vector record
instead, engraving the same strokes through the firmware's curves
pipeline. Compose with the plate editor (index.html) or any text
editor.

The supported charset, grid dimensions, glyph geometry and payload
parameters are read from the glyphs.js next to this script,
generated from the firmware sources by
"go run seedhammer.com/cmd/textplate".

Usage:
    write-nfc.py plate.txt            # or - for stdin
    write-nfc.py --curves plate.txt
    echo "IN CASE OF FIRE" | write-nfc.py -

Requires: pip install nfcpy ndeflib
"""

import json
import pathlib
import re
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


def validate(lines: list[str], sh: dict) -> dict:
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
            return size
    largest = sh["sizes"][-1]
    sys.exit(
        f"does not fit any plate size: {cols}x{len(lines)}, "
        f"largest grid is {largest['cols']}x{largest['rows']}"
    )


def translate(d: str, dx: int, dy: int) -> str:
    """Offset every coordinate of glyph path data."""
    out = []
    x = True
    for tok in re.finditer(r"[MC]|-?\d+", d):
        t = tok.group(0)
        if t in ("M", "C"):
            out.append(t)
            x = True
            continue
        v = int(t) + (dx if x else dy)
        if out and out[-1] not in ("M", "C"):
            out.append(" ")
        out.append(str(v))
        x = not x
    return "".join(out)


def compile_curves(lines: list[str], size: dict, sh: dict) -> bytes:
    """Compile a composition to a seedhammer.com:curves payload, with
    glyphs laid out on the same grid the firmware engraves."""
    units_per_mm = int(sh["height"] / size["mm"] + 0.5)
    stroke_width = int(sh["strokeMM"] * units_per_mm + 0.5)
    margin = sh["marginMM"] * units_per_mm
    parts = [f"{sh['version']} {units_per_mm} {stroke_width}"]
    for row, line in enumerate(lines):
        for col, ch in enumerate(line):
            d = sh["glyphs"].get(ch, "")
            if not d:
                continue
            dx = margin + col * sh["advance"]
            dy = margin + row * sh["height"]
            parts.append(translate(d, dx, dy))
    payload = "\n".join(parts).encode()
    if len(payload) > sh["payloadCap"]:
        sys.exit(f"payload is {len(payload)} bytes, over the {sh['payloadCap']} byte cap")
    return payload


def write(records: list) -> None:
    result = {}

    def on_connect(tag):
        if tag.ndef is None:
            result["error"] = "target is not NDEF-formatted"
            return True
        if not tag.ndef.is_writeable:
            result["error"] = "target is read-only"
            return True
        try:
            tag.ndef.records = records
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
    args = sys.argv[1:]
    as_curves = "--curves" in args
    args = [a for a in args if a != "--curves"]
    if len(args) != 1:
        sys.exit(__doc__.strip())
    src = sys.stdin if args[0] == "-" else open(args[0], encoding="utf-8")
    with src:
        lines = canonical(src.read())
    sh = font_data()
    size = validate(lines, sh)
    print(
        f"{max(len(l) for l in lines)}x{len(lines)} characters, "
        f"engraves at {size['mm']}mm",
        file=sys.stderr,
    )
    if as_curves:
        payload = compile_curves(lines, size, sh)
        records = [ndef.Record("urn:nfc:ext:" + sh["recordType"], "", payload)]
    else:
        records = [ndef.TextRecord("\n".join(lines), language="en")]
    write(records)


if __name__ == "__main__":
    main()
