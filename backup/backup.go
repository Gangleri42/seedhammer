// package backup implements the SeedHammer backup scheme.
package backup

import (
	"fmt"
	"image"
	"math"
	"strings"

	qr "github.com/seedhammer/kortschak-qr"
	"seedhammer.com/engrave"
	"seedhammer.com/font/vector"
)

type Seed struct {
	Size              PlateSize
	Title             string
	Mnemonic          []string
	ShortestWord      int
	LongestWord       int
	QR                *qr.Code
	MasterFingerprint uint32
	Font              *vector.Face
}

type SeedString struct {
	Size              PlateSize
	Title             string
	Seed              string
	MasterFingerprint uint32
	Font              *vector.Face
}

type Text struct {
	Size       PlateSize
	Paragraphs []Paragraph
	Font       *vector.Face
	// FontSize is the text size in millimeters. If zero, it defaults
	// to plateFontSizeUR (3.8mm).
	FontSize float32
}

type Paragraph struct {
	Text    string
	QR      *qr.Code
	QRScale int
}

const MaxTitleLen = 18

const outerMargin = 3
const innerMargin = 10

// plateSize is the width of every plate and the side of the square
// one in millimeters.
const plateSize = 85

// PlateSize enumerates the physical plate formats the machine takes.
// The zero value is the square plate.
type PlateSize int

const (
	SquarePlate PlateSize = iota
	SmallPlate
)

// Dims returns the plate dimensions in millimeters. Every plate
// shares the same width; the small plate is the square one with the
// top 30mm removed, its bottom and side edges staying put in the
// machine.
func (p PlateSize) Dims() image.Point {
	switch p {
	case SquarePlate:
		return image.Pt(plateSize, plateSize)
	case SmallPlate:
		return image.Pt(plateSize, 55)
	}
	panic("unreachable")
}

// dims returns the plate dimensions in machine units.
func (p PlateSize) dims(params engrave.Params) image.Point {
	d := p.Dims()
	return image.Point{X: params.I(d.X), Y: params.I(d.Y)}
}

// FontSizes is the descending ladder of plate text sizes in
// millimeters, tried largest-first until an engraving fits its plate.
// The device auto-fit (gui) and the editor grid data (cmd/textplate,
// which bakes it into glyphs.js) both read this one slice, so the
// firmware and the composition tools can never disagree on which sizes
// exist. Keep it sorted descending: the fit loops take the first match.
var FontSizes = []float32{6.0, 5.0, 4.4, 3.8, 3.4, 3.0}

// CharsPerLine returns the number of fixed-width characters that fit
// on one plate line at the given text size in millimeters.
func CharsPerLine(params engrave.Params, fnt *vector.Face, fontMM float32) int {
	width := params.F(plateSize) - 2*params.I(outerMargin)
	return width / fixedCharWidth(fnt, params.F(fontMM))
}

// LinesPerPlate returns the number of text lines that fit the plate
// at the given text size in millimeters. Together with CharsPerLine
// it defines the character grid composition tools rely on. Width is
// the same on every plate, so only the line count varies with size.
func LinesPerPlate(params engrave.Params, plate PlateSize, fontMM float32) int {
	height := params.I(plate.Dims().Y) - 2*params.I(outerMargin)
	return height / params.F(fontMM)
}

// fixedCharWidth returns the character advance at fontSize machine
// units, assuming the font is fixed width.
func fixedCharWidth(fnt *vector.Face, fontSize int) int {
	w, _, ok := fnt.Decode('W')
	if !ok {
		panic("W not in font")
	}
	return int(float32(w*fontSize) / float32(fnt.Metrics().Height))
}

func TitleString(face *vector.Face, s string) string {
	s = strings.ToUpper(s)
	res := ""
	for _, r := range s {
		if _, _, valid := face.Decode(r); valid {
			res += string(r)
		}
		if len(res) == MaxTitleLen {
			break
		}
	}
	return res
}

// titleError reports the first rune of the uppercased title the face
// cannot engrave. The plate closures cut the title mid-walk, where a
// missing glyph is a panic in engrave.String; erring before the first
// command leaves the caller a session instead.
func titleError(face *vector.Face, title string) error {
	for _, r := range strings.ToUpper(title) {
		if _, _, ok := face.Decode(r); !ok {
			return fmt.Errorf("backup: the title font cannot engrave %q", r)
		}
	}
	return nil
}

func EngraveSeed(params engrave.Params, plate Seed) (engrave.Engraving, error) {
	if err := titleError(plate.Font, plate.Title); err != nil {
		return nil, err
	}
	var qrc *engrave.ConstantQRCmd
	if plate.QR != nil {
		var err error
		qrc, err = engrave.ConstantQR(plate.QR)
		if err != nil {
			return nil, err
		}
	}
	side := frontSideSeed(params, plate, qrc)
	return side, nil
}

func EngraveSeedString(params engrave.Params, plate SeedString) (engrave.Engraving, error) {
	if err := titleError(plate.Font, plate.Title); err != nil {
		return nil, err
	}
	seed := strings.ToUpper(plate.Seed)
	qrc, err := qr.Encode(seed, qr.M)
	if err != nil {
		return nil, err
	}
	qrCmd, err := engrave.ConstantQR(qrc)
	if err != nil {
		return nil, err
	}
	side := engraveSeedString(params, plate, qrCmd)
	return side, nil
}

const plateFontSize = 4.1
const plateFontSizeUR = 3.8
const plateSmallFontSize = 3.

const groupLen = 10

func engraveSeedString(params engrave.Params, plate SeedString, qrc *engrave.ConstantQRCmd) engrave.Engraving {
	pfs := params.F(plateFontSize)
	constant := engrave.NewConstantStringer(plate.Font, params, pfs)
	return func(yield func(engrave.Command) bool) {
		plateDims := plate.Size.dims(params)
		t := engrave.NewTransform(yield)

		const (
			maxCol1 = 16
			maxCol2 = 4
			qrScale = 3
		)
		seed := strings.ToUpper(plate.Seed)
		ngroups := (len(seed) + groupLen - 1) / groupLen
		endCol1 := min(ngroups, maxCol1)
		qrsz := qrc.Size * params.StrokeWidth * qrScale
		col1Height := max(qrsz, pfs*endCol1)

		// Engrave version, mfp and page.
		innerMargin := params.I(innerMargin)
		metaMargin := params.I(4)
		if plate.MasterFingerprint != 0 {
			mfp := fmt.Sprintf("%.8X", plate.MasterFingerprint)
			offy := (plateDims.Y-col1Height)/2 - metaMargin
			mfpStr := engrave.String(plate.Font, params.F(plateSmallFontSize), mfp).SourceOrder()
			mfpszX, mfpszY := mfpStr.Measure()
			t.Offset((plateDims.X-mfpszX)/2, offy-mfpszY)
			mfpStr.Engrave(t.Yield)
		}

		// Engrave column 1.
		off := t.Offset(innerMargin, (plateDims.Y-col1Height)/2)
		stringColumn(off, constant, plate.Font, pfs, seed, 0, endCol1)

		// Engrave (top of) column 2.
		endCol2 := min(ngroups, endCol1+maxCol2)
		off = t.Offset(params.I(44), (plateDims.Y-col1Height)/2)
		stringColumn(off, constant, plate.Font, pfs, seed, endCol1, endCol2)

		// Engrave seed QR.
		qrCmd := qrc.Engrave(params.StepperConfig, params.StrokeWidth, qrScale)
		t.Offset(params.I(60)-qrsz/2, (plateDims.Y-qrsz)/2)
		qrCmd(t.Yield)

		{
			// Engrave bottom of column 2.
			height := (ngroups - endCol2) * pfs
			off := t.Offset(params.I(44), (plateDims.Y+col1Height)/2-height)
			stringColumn(off, constant, plate.Font, pfs, seed, endCol2, ngroups)
		}

		// Engrave title.
		title := strings.ToUpper(plate.Title)
		{
			offy := (plateDims.Y+col1Height)/2 + metaMargin
			title := engrave.String(plate.Font, params.F(plateSmallFontSize), title).SourceOrder()
			titleWidth, _ := title.Measure()
			t.Offset((plateDims.X-titleWidth)/2, offy)
			title.Engrave(t.Yield)
		}
	}
}

func frontSideSeed(params engrave.Params, plate Seed, qrc *engrave.ConstantQRCmd) engrave.Engraving {
	return func(yield func(engrave.Command) bool) {
		plateDims := plate.Size.dims(params)
		t := engrave.NewTransform(yield)
		pfs := params.F(plateFontSize)
		constant := engrave.NewConstantStringer(plate.Font, params, pfs)

		const (
			maxCol1 = 16
			maxCol2 = 4
		)
		endCol1 := min(maxCol1, len(plate.Mnemonic))
		col1Height := pfs * endCol1

		// Engrave master fingerprint.
		innerMargin := params.I(innerMargin)
		metaMargin := params.I(4)
		if plate.MasterFingerprint != 0 {
			mfp := fmt.Sprintf("%.8X", plate.MasterFingerprint)
			mfpStr := engrave.String(plate.Font, params.F(plateSmallFontSize), mfp).SourceOrder()
			mfpszX, mfpszY := mfpStr.Measure()
			switch plate.Size {
			case SmallPlate:
				// No headroom above the column on the small plate: the
				// fingerprint reads up the left edge, as on the v1
				// SH01 plates.
				margin := params.I(outerMargin)
				off := t.Offset(margin, (plateDims.Y+mfpszX)/2).Rotate(-math.Pi / 2)
				mfpStr.Engrave(off.Yield)
			default:
				offy := (plateDims.Y-col1Height)/2 - metaMargin
				t.Offset((plateDims.X-mfpszX)/2, offy-mfpszY)
				mfpStr.Engrave(t.Yield)
			}
		}

		// Engrave column 1.
		off := t.Offset(innerMargin, (plateDims.Y-col1Height)/2)
		wordColumn(off, constant, plate.Font, pfs, plate.Mnemonic, plate.ShortestWord, plate.LongestWord, 0, endCol1)

		// Engrave (top of) column 2.
		endCol2 := min(endCol1+maxCol2, len(plate.Mnemonic))
		off = t.Offset(params.I(44), (plateDims.Y-col1Height)/2)
		wordColumn(off, constant, plate.Font, pfs, plate.Mnemonic, plate.ShortestWord, plate.LongestWord, endCol1, endCol2)

		// Engrave seed QR.
		if qrc != nil {
			const qrScale = 3
			qrCmd := qrc.Engrave(params.StepperConfig, params.StrokeWidth, qrScale)
			qrsz := qrc.Size * params.StrokeWidth * qrScale
			t.Offset(params.I(60)-qrsz/2, (plateDims.Y-qrsz)/2)
			qrCmd(t.Yield)
		}

		{
			// Engrave bottom of column 2.
			height := (len(plate.Mnemonic) - endCol2) * pfs
			off := t.Offset(params.I(44), (plateDims.Y+col1Height)/2-height)
			wordColumn(off, constant, plate.Font, pfs, plate.Mnemonic, plate.ShortestWord, plate.LongestWord, endCol2, len(plate.Mnemonic))
		}

		// Engrave title.
		title := strings.ToUpper(plate.Title)
		{
			titleStr := engrave.String(plate.Font, params.F(plateSmallFontSize), title).SourceOrder()
			titleWidth, titleHeight := titleStr.Measure()
			switch plate.Size {
			case SmallPlate:
				// Up the right edge, mirroring the fingerprint.
				margin := params.I(outerMargin)
				off := t.Offset(plateDims.X-margin-titleHeight, (plateDims.Y+titleWidth)/2).Rotate(-math.Pi / 2)
				titleStr.Engrave(off.Yield)
			default:
				offy := (plateDims.Y+col1Height)/2 + metaMargin
				t.Offset((plateDims.X-titleWidth)/2, offy)
				titleStr.Engrave(t.Yield)
			}
		}
	}
}

func wordColumn(t engrave.Transform, constant *engrave.ConstantStringer, font *vector.Face, fontSize int, mnemonic []string, shortest, longest, start, end int) {
	y := 0
	for i := start; i < end; i++ {
		num := engrave.String(font, fontSize, fmt.Sprintf("%2d ", i+1)).SourceOrder()
		width, _ := num.Measure()
		w := mnemonic[i]
		word := strings.ToUpper(w)
		t.Offset(0, y)
		num.Engrave(t.Yield)
		t.Offset(width, y)
		constant.PaddedString(t.Yield, word, shortest, longest)
		y += fontSize
	}
}

func stringColumn(t engrave.Transform, constant *engrave.ConstantStringer, font *vector.Face, fontSize int, s string, start, end int) {
	y := 0
	for i := start; i < end; i++ {
		word := s[i*groupLen:]
		word = word[:min(len(word), groupLen)]
		constant.String(t.Offset(0, y).Yield, word)
		y += fontSize
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func EngraveText(params engrave.Params, plate Text) engrave.Engraving {
	return func(yield func(engrave.Command) bool) {
		t := engrave.NewTransform(yield)
		fontMM := plate.FontSize
		if fontMM == 0 {
			fontMM = plateFontSizeUR
		}
		fontSize := params.F(fontMM)
		fnt := plate.Font

		charWidth := fixedCharWidth(fnt, fontSize)
		margin := params.I(outerMargin)
		plateDims := plate.Size.dims(params)
		width := plateDims.X - 2*margin
		charPerLine := CharsPerLine(params, fnt, fontMM)
		offy := params.I(outerMargin)
		// One line command reused across the whole layout; its glyph
		// buffers grow once. A dense plate lays out thousands of
		// glyphs, and per-line allocation is what crowds the device
		// heap when a fit ladder walks many candidate layouts.
		var str engrave.StringCmd
		// Serpentine rows: a row engraves right to left when its right
		// end is nearer the previous row's exit, so the head enters
		// each row where the last one ended instead of returning
		// across the plate.
		exitX := margin
		for i, p := range plate.Paragraphs {
			qrLines := 0
			charPerQRLine := 0
			qrsz := 0
			qrBorder := params.I(2)
			var qr engrave.Engraving
			if p.QR != nil {
				qrScale := p.QRScale
				if qrScale == 0 {
					qrScale = 2
				}
				qr = engrave.QR(params.StrokeWidth, qrScale, p.QR)
				qrsz = p.QR.Size * params.StrokeWidth * qrScale
				charPerQRLine = (width - 2*qrBorder - qrsz) / charWidth
				qrLines = (qrsz + 2*qrBorder + fontSize - 1) / fontSize
			}
			lineno := 0
			txt := p.Text
			// A '\n' forces a line break; lines longer than the plate
			// width wrap.
			for len(txt) > 0 {
				seg := txt
				if i := strings.IndexByte(txt, '\n'); i >= 0 {
					seg = txt[:i]
					txt = txt[i+1:]
				} else {
					txt = ""
				}
				for {
					n := charPerLine
					if lineno < qrLines {
						n = charPerQRLine
					}
					if n < 1 {
						n = 1
					}
					if l := len(seg); n > l {
						n = l
					}
					s := seg[:n]
					seg = seg[n:]
					t.Offset(margin, offy+lineno*fontSize)
					str.Reset(fnt, fontSize, s)
					rightX := margin + len(s)*charWidth
					if s != "" && abs(exitX-rightX) < abs(exitX-margin) {
						str.Reversed()
						exitX = margin
					} else if s != "" {
						exitX = rightX
					}
					str.Engrave(t.Yield)
					lineno++
					if len(seg) == 0 {
						break
					}
				}
			}
			if qr != nil {
				qrx := plateDims.X - qrsz - margin - qrBorder
				qry := offy + (qrLines*fontSize-qrsz)/2
				if len(p.Text) == 0 {
					// Center QR.
					qrx, qry = (plateDims.X-qrsz)/2, (plateDims.Y-qrsz)/2
				}
				t.Offset(qrx, qry)
				qr(t.Yield)
			}
			offy += lineno * fontSize
			if i != len(plate.Paragraphs)-1 {
				// Space UR sections.
				offy += params.I(1)
			}
		}
	}
}
