# Engrave an SVG drawing or rich text

The fork engraves arbitrary vector content: a logo, a diagram, a formatted
document. You produce a `seedhammer.com:curves` payload on a computer, send it
over NFC, and the machine re-plans it for its own kinematics before showing a
preview.

## The browser way: Studio

[SeedHammer Studio](https://gangleri42.github.io/studio/) runs in the browser.
Draw on the canvas, place and edit text, import an SVG, or write markdown in
the rich-text pane; the preview uses the same fonts and speed model the machine
does (firmware code compiled to WebAssembly). Send writes over Web NFC on
Android; on desktop, the [NFC bridge](../cmd/nfc-bridge/README.md) routes
Send to a USB NFC reader (an [ACR122U](../README.md#host-setup);
`./install.sh send` installs the bridge as a user service).

## The CLI way: svgplate

`cmd/svgplate` converts an SVG, or markdown with `-text`, into a validated
payload. `./install.sh send` builds it as `./svgplate` at the repo root;
the `go run` form always works:

```sh
go run seedhammer.com/cmd/svgplate logo.svg
go run seedhammer.com/cmd/svgplate -height 40 -rotate 90 -preview check.png logo.svg
go run seedhammer.com/cmd/svgplate -text -size 4 notes.md
```

Useful flags: `-height` scales the drawing to a target height in mm (0 fits
the plate), `-pos center|x,y` places it, `-preview out.png` renders what the
machine will show, `-size` sets the rich-text body height, `-o` names the
output (default: the input renamed to `.curves`).

The repo carries the demo pair this page's figures use, a drawing and a
note: `gui/testdata/hammer.svg` and `gui/testdata/note.md`. Convert them as
above to reproduce their payloads byte for byte.

Every run prints the gauge table, each cost against its cap:

```
logo.svg
  gauges:
     payload bytes        7411 / 32703    ( 23%)
     strokes               181
     knots                3662 (max 217/stroke)
     seconds               513 / 3600     ( 14%)
     size               40.0 x 40.0   mm   at (22.5, 22.5)
     engrave time       8:33
  OK: fits the plate and every cap
```

`OK` means the machine will accept it: the validation is the same `curves`
package the firmware runs. `REJECTED` names the cap you hit.

To send the payload without Studio, POST it to the running bridge and tap the
machine against the reader:

```sh
curl -s http://127.0.0.1:8787/bridge/send \
  -H 'Content-Type: application/json' \
  -d "{\"payloadB64\":\"$(base64 -w0 logo.curves)\"}"
```

or skip the bridge and write it straight from the reader (venv active):

```sh
python3 cmd/textplate/write-nfc.py --curves logo.curves
```

## The rich-text subset

Markdown, deliberately small: five header levels (`#` through `#####`),
`*italic*` as a 12-degree oblique, `_underline_`, and GFM pipe tables. There
is no bold. A fixed 0.3 mm needle cannot vary stroke weight, which is also why
underline stands in for strong emphasis. Body size defaults to 4 mm.

## What fits

The square plate is 85 x 85 mm and the small plate 85 x 55 mm (held in
the clamp by the printable [jaw](../hardware/small-plate-jaw/)), both with a
3 mm margin. By default (`-plate ask`) the converter fits the square box and
the device measures the drawing, offering every plate it fits. Pass
`-plate small` or `-plate square` to lay out for that plate and stamp it into
the payload header: the device then engraves that plate without asking.
Payloads cap just under 32 KB
(the NDEF file limit). Filled shapes engrave as outline contours; there is
no hatch fill. Repeated shapes cost fewer payload bytes: rich text and
stamped shapes deduplicate through the payload dictionary.

## On the machine

Scanning and planning run behind a progress screen while a live preview fills
in, and the engrave screen keeps that preview: the rendered drawing, its size,
and the engraving duration. Back cancels. Hold the button to engrave, and the
drawing stays up with the duration counting down. The middle button hides the
content if you would rather not leave it on screen while the machine runs.

![The demo drawing held on the engrave screen](images/svg-01-drawing-preview.png)

A rich-text payload arrives the same way, composed lines, table rules and
all:

![The demo note held on the engrave screen](images/svg-02-richtext-preview.png)

Both as engraved, rendered from the same planned strokes the machine cuts:

![The demo drawing as engraved](images/plate-svg-drawing.png)

![The demo note as engraved](images/plate-richtext.png)
