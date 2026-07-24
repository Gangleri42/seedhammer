// Command svgplate converts an SVG drawing or a rich-text document
// into a seedhammer.com:curves payload the SeedHammer engraves, and
// validates it against the firmware's own caps. Two front-ends feed
// one pipeline: -text reads a markdown subset (headers, italic, pipe
// tables); otherwise the input is an SVG.
//
// Usage:
//
//	svgplate [flags] input.svg
//	svgplate -text [flags] doc.md
//
// The payload is validated by curves.Parse and the shared caps, so a
// clean run engraves exactly what the gauge report describes.
package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"seedhammer.com/curves"
)

func main() {
	var (
		text    = flag.Bool("text", false, "read the input as a rich-text markdown subset, not SVG")
		out     = flag.String("o", "", "payload output path (default: input with .curves)")
		preview = flag.String("preview", "", "also render the payload to this PNG")
		height  = flag.Float64("height", 0, "target height in mm (SVG; 0 fits to the plate)")
		rotate  = flag.Float64("rotate", 0, "rotate the drawing by this many degrees (SVG)")
		pos     = flag.String("pos", "center", "placement: center, or x,y mm of the top-left")
		body    = flag.Float64("size", 4, "body text height in mm (rich text)")
		side    = flag.Int("previewpx", 1024, "preview size in pixels")
	)
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	in := flag.Arg(0)
	src, err := os.ReadFile(in)
	if err != nil {
		die(err)
	}

	isText := *text || strings.EqualFold(filepath.Ext(in), ".md")
	var segs []fseg
	if isText {
		segs, err = renderMarkdown(string(src), *body)
	} else {
		var raw []fseg
		raw, err = extractSVG(src)
		if err == nil {
			pl, perr := placementOf(*height, *rotate, *pos)
			if perr != nil {
				die(perr)
			}
			segs = layoutOnPlate(raw, pl)
		}
	}
	// A rich-text miss is a warning, not a stop: the drawing minus the
	// bad runes may still be worth engraving. SVG parse errors are fatal.
	var warn error
	if err != nil {
		if isText && segs != nil {
			warn = err
		} else {
			die(err)
		}
	}

	payload, d, r, verr := finish(segs)
	report(in, payload, r, warn, verr)

	if *out == "" {
		*out = strings.TrimSuffix(in, filepath.Ext(in)) + ".curves"
	}
	if verr == nil {
		if err := os.WriteFile(*out, payload, 0o644); err != nil {
			die(err)
		}
		fmt.Fprintf(os.Stderr, "wrote %s\n", *out)
		if *preview != "" {
			if err := writePreview(*preview, d, *side); err != nil {
				die(err)
			}
			fmt.Fprintf(os.Stderr, "wrote %s\n", *preview)
		}
	}
	if verr != nil {
		os.Exit(1)
	}
}

func placementOf(height, rotate float64, pos string) (placement, error) {
	pl := placement{heightMM: height, rotate: rotate, posX: math.NaN(), posY: math.NaN()}
	if pos != "" && pos != "center" {
		var x, y float64
		if _, err := fmt.Sscanf(strings.ReplaceAll(pos, " ", ""), "%f,%f", &x, &y); err != nil {
			return pl, fmt.Errorf("bad -pos %q, want center or x,y", pos)
		}
		pl.posX, pl.posY = x, y
	}
	return pl, nil
}

// report prints the gauge table: every cost against its cap, so the
// user sees the wall whether or not the drawing cleared it.
func report(name string, payload []byte, r curves.Report, warn, verr error) {
	w := os.Stderr
	fmt.Fprintf(w, "%s\n", name)
	mm := float64(sh2.Millimeter)
	gauge := func(label string, val, cap int) {
		flag := "  "
		if val > cap {
			flag = "!!"
		}
		fmt.Fprintf(w, "  %s %-16s %8d / %-8d (%3.0f%%)\n", flag, label, val, cap, 100*float64(val)/float64(cap))
	}
	fmt.Fprintf(w, "  gauges:\n")
	gauge("payload bytes", len(payload), payloadByteCap)
	gauge("strokes", r.Strokes, curves.MaxStrokes)
	gauge("knots", r.Knots, curves.MaxKnots)
	gauge("knots/stroke", r.MaxStrokeKnots, curves.MaxStrokeKnots)
	gauge("seconds", r.Seconds, curves.MaxMinutes*60)
	if !r.Bounds.Empty() {
		fmt.Fprintf(w, "     %-16s %6.1f x %-6.1f mm   at (%.1f, %.1f)\n", "size",
			float64(r.Bounds.Dx())/mm, float64(r.Bounds.Dy())/mm,
			float64(r.Bounds.Min.X)/mm, float64(r.Bounds.Min.Y)/mm)
	}
	if r.Seconds > 0 {
		fmt.Fprintf(w, "     %-16s %d:%02d\n", "engrave time", r.Seconds/60, r.Seconds%60)
	}
	if warn != nil {
		fmt.Fprintf(w, "  warning: %v\n", warn)
	}
	if verr != nil {
		fmt.Fprintf(w, "  REJECTED: %v\n", verr)
	} else {
		fmt.Fprintf(w, "  OK: fits the plate and every cap\n")
	}
}

func die(err error) {
	fmt.Fprintf(os.Stderr, "svgplate: %v\n", err)
	os.Exit(1)
}
