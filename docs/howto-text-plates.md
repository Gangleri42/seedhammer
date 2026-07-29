# Engrave a free-form text plate

Write a plain NFC Text record to the machine and it engraves the text as a
plate: one record line per plate line, set at the largest size that fits. No
app pairing, no cable, no file format. This is a fork feature; upstream
firmware rejects anything that is not a descriptor, seed phrase, or codex32
share. (use NFC Tools APP, add text --> send, done. preview renders on screen)

## What you need

- A SeedHammer II running the [fork firmware](../README.md#firmware-downloads).
- Something that writes NFC Text records: a phone app (NFC Tools works),
  [Studio's plate composer](https://gangleri42.github.io/studio/textplate/),
  or [`write-nfc.py`](../cmd/textplate/write-nfc.py) with a USB reader.

## Compose

The plate font covers the 95 printable ASCII characters. The grid depends on
the text size, and the firmware picks the largest size whose grid holds your
composition:

| size (mm) | grid (cols x rows) |
|-----------|--------------------|
| 6.0       | 22 x 13            |
| 5.0       | 26 x 15            |
| 4.4       | 30 x 17            |
| 3.8       | 34 x 20            |
| 3.4       | 38 x 23            |
| 3.0       | 44 x 26            |

Two rules worth knowing:

- **Lines you size deliberately stay put.** A line that fits some grid
  unwrapped pins the fit to that size or smaller; a longer line then wraps
  instead of quietly reflowing your whole layout into a smaller font. Fill a
  26-column line and the plate engraves at 5.0 mm or below, whatever else the
  text does.
- **Whitespace is canonicalized.** CRLF becomes LF, trailing spaces are
  stripped from every line, trailing blank lines are dropped. What remains is
  exactly what engraves.

Don't start the first line with `command: `; that prefix is the firmware's
debug channel and will not engrave.

## Write it

**Phone.** In NFC Tools: Write, Add a record, Text, write or paste your text,
then press Write and hold the phone against the machine's reader. The machine
scans whenever it shows the start screen.

**Studio.** The [composer](https://gangleri42.github.io/studio/textplate/)
previews the grid, the wrap, and the size as you type, using glyph geometry
generated from this repo's font. Send works over Web NFC on Android, or
through the [desktop bridge](../cmd/nfc-bridge/README.md).

**CLI.** With `nfcpy` and `ndeflib` installed and a USB reader attached:

```sh
python3 cmd/textplate/write-nfc.py plate.txt     # a file, or - for stdin
echo "IN CASE OF FIRE" | python3 cmd/textplate/write-nfc.py -
```

The script validates the charset and grid before writing and prints the size
the plate will engrave at. `--raw` skips the plate checks and writes the text
record as-is; use it for descriptors, keys, and anything else the firmware
parses as a structured record rather than plate text.

## On the machine

The machine plans the plate behind a progress screen, then shows the layout
confirm: the composed lines at the fitted size, wraps and all, with plate
dimensions and engraving duration. What you approve is the plate itself, not
a display-width rendering of the text.

If the text looks like a damaged backup (a descriptor fragment, a codex32
string, a seed phrase with a bad word), the confirm screen says so. Intact
backups never land here; the structured parsers catch them first. The warning
is a nudge to re-check the source, not a block: confirm and it engraves as
plain text.
