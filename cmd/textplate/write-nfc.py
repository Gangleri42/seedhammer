#!/usr/bin/env python3
"""
write-nfc — write a text plate composition to the SeedHammer using a
USB NFC reader (e.g. ACR122U via nfcpy).

By default the composition is written as an NDEF Text record: each
line of the payload becomes a plate line, engraved at the largest
text size whose grid holds the composition.

The alternatives write a seedhammer.com:curves record instead, which
the firmware dispatches on a mode field. With --curves-text the plate
text rides the curves envelope and the firmware renders it from its
own font, same engraving as a Text record but one input format. With
--curves the composition is vectorized into path geometry, engraving
the strokes directly; use this for graphics, not dense text (path
geometry is far larger than the text).

Compose with the plate editor (index.html) or any text editor. The
supported charset, grid dimensions, glyph geometry and payload
parameters are read from the glyphs.js next to this script,
generated from the firmware sources by
"go run seedhammer.com/cmd/textplate".

With --raw the payload is written as a Text record without the plate
grid checks: the firmware's scanner decides what it is (descriptor,
codex32, seed phrase, free text). The default mode gates on the
plate grid, wrapping overlong lines like the firmware's text fit;
structured one-line records pass that gate too, but the firmware
parses them as records, not text plates, so the reported plate size
does not apply to them. Prefer --raw for anything that is not plate
text.

Usage:
    write-nfc.py plate.txt            # or - for stdin
    write-nfc.py --curves-text plate.txt
    write-nfc.py --curves plate.txt
    write-nfc.py --raw descriptor.txt
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
    # The firmware's anchored fit (gui.fitText): every line caps the
    # fit at the largest ladder size whose columns hold it unwrapped,
    # the smallest cap is the ceiling, and the chosen size is the
    # largest at or under the ceiling whose grid holds the composition
    # with overlong lines wrapped. A line longer than every grid caps
    # nothing and wraps at whatever size wins.
    ceiling = 0
    for line in lines:
        for i, size in enumerate(sh["sizes"]):
            if len(line) <= size["cols"]:
                ceiling = max(ceiling, i)
                break
    for size in sh["sizes"][ceiling:]:
        wrapped = sum(max(1, -(-len(line) // size["cols"])) for line in lines)
        if wrapped <= size["rows"]:
            return size, wrapped > len(lines)
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
    """Retired: version 2 path bodies are binary (dictionary-compressed),
    which this script does not emit. Vectorized text comes from
    cmd/svgplate now; the text modes here cover the grid plate."""
    sys.exit("--curves retired: the version 2 path body is binary. "
             "Use 'go run seedhammer.com/cmd/svgplate -text' to vectorize, "
             "or --curves-text for the firmware-rendered text mode.")


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
    as_curves_text = "--curves-text" in args
    as_raw = "--raw" in args
    args = [a for a in args if a not in ("--curves", "--curves-text", "--raw")]
    if len(args) != 1 or (as_curves + as_curves_text + as_raw) > 1:
        sys.exit(__doc__.strip())
    src = sys.stdin if args[0] == "-" else open(args[0], encoding="utf-8")
    with src:
        lines = canonical(src.read())
    if as_raw:
        # No grid gate: the record carries whatever the firmware's
        # scanner can make of it.
        write([ndef.TextRecord("\n".join(lines), language="en")])
        return
    sh = font_data()
    size, wrapped = validate(lines, sh)
    if wrapped:
        # The size only applies if the firmware engraves the payload
        # as a text plate; a one-line record (descriptor, key) is
        # parsed as a record instead.
        print(
            f"{max(len(l) for l in lines)}x{len(lines)} characters, "
            f"wraps at {size['mm']}mm if engraved as plate text",
            file=sys.stderr,
        )
    else:
        print(
            f"{max(len(l) for l in lines)}x{len(lines)} characters, "
            f"engraves at {size['mm']}mm",
            file=sys.stderr,
        )
    ext = "urn:nfc:ext:" + sh["recordType"]
    if as_curves:
        payload = compile_curves(lines, size, sh)
        records = [ndef.Record(ext, "", payload)]
    elif as_curves_text:
        # Text through the curves envelope: the firmware lays it out
        # and renders it from its own font, same as a Text record.
        body = f"{sh['version']} text\n" + "\n".join(lines)
        records = [ndef.Record(ext, "", body.encode())]
    else:
        records = [ndef.TextRecord("\n".join(lines), language="en")]
    write(records)


if __name__ == "__main__":
    main()
