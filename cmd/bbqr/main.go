// Command bbqr encodes and decodes BBQr series and splits and
// combines Shamir share backups carried by BBQr.
//
// Usage:
//
//	bbqr encode [-type B] [-enc auto|H|2|Z] [-minver N] [-maxver N]
//	            [-minsplit N] [-maxsplit N] [-png dir] [input]
//	bbqr decode [-limit N] [parts]
//	bbqr split -k K -n N [-type B] [-png dir] [input]
//	bbqr combine [-limit N] [-descriptor] [parts]
//
// Parts are one QR content per line; share series are separated by
// blank lines. Data is binary from stdin or the input file, and the
// recovered data goes to stdout. With -png, each part is also written
// as a QR image at error correction level L, the alphanumeric mode
// chosen automatically by the QR library.
package main

import (
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	qr "github.com/seedhammer/kortschak-qr"

	"seedhammer.com/bbqr"
	"seedhammer.com/bc/urtypes"
	"seedhammer.com/bip380"
	"seedhammer.com/shamir"
)

func main() {
	err := run(os.Stdin, os.Stdout, os.Stderr, os.Args[1:])
	switch err {
	case nil:
	case errUsage:
		os.Exit(2)
	default:
		fmt.Fprintf(os.Stderr, "bbqr: %v\n", err)
		os.Exit(1)
	}
}

func run(stdin io.Reader, stdout, stderr io.Writer, args []string) error {
	if len(args) == 0 {
		usage(stderr)
		return errUsage
	}
	cmd, args := args[0], args[1:]
	switch cmd {
	case "encode":
		return cmdEncode(stdin, stdout, args)
	case "decode":
		return cmdDecode(stdin, stdout, stderr, args)
	case "split":
		return cmdSplit(stdin, stdout, args)
	case "combine":
		return cmdCombine(stdin, stdout, stderr, args)
	}
	usage(stderr)
	return fmt.Errorf("unknown command %q", cmd)
}

var errUsage = errors.New("bad usage")

func usage(w io.Writer) {
	fmt.Fprintln(w, `usage:
  bbqr encode [-type B] [-enc auto|H|2|Z] [-minver N] [-maxver N] [-minsplit N] [-maxsplit N] [-png dir] [input]
  bbqr decode [-limit N] [parts]
  bbqr split -k K -n N [-type B] [-png dir] [input]
  bbqr combine [-limit N] [-descriptor] [parts]

parts are one QR content per line; share series are separated by a
blank line. data is read from stdin or the input file; recovered data
is written to stdout.`)
}

func cmdEncode(stdin io.Reader, stdout io.Writer, args []string) error {
	fs := flag.NewFlagSet("encode", flag.ContinueOnError)
	typ := fs.String("type", "B", "file type: P, T, J, C, U, B or a name: psbt, txn, json, cbor, text, binary")
	enc := fs.String("enc", "auto", "encoding: auto, H, 2 or Z")
	pngDir := fs.String("png", "", "also write one QR PNG per part into this directory")
	var opts bbqr.SplitOptions
	fs.IntVar(&opts.MinVersion, "minver", 0, "smallest QR version (default 5)")
	fs.IntVar(&opts.MaxVersion, "maxver", 0, "largest QR version (default 40)")
	fs.IntVar(&opts.MinSplit, "minsplit", 0, "smallest number of parts")
	fs.IntVar(&opts.MaxSplit, "maxsplit", 0, "largest number of parts (default 1295)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	t, err := parseType(*typ)
	if err != nil {
		return err
	}
	switch strings.ToUpper(*enc) {
	case "AUTO", "":
	case "H":
		opts.Encoding = bbqr.EncHex
	case "2":
		opts.Encoding = bbqr.EncBase32
	case "Z":
		opts.Encoding = bbqr.EncZlib
	default:
		return fmt.Errorf("bad -enc %q", *enc)
	}
	data, err := readInput(stdin, fs.Args())
	if err != nil {
		return err
	}
	s, err := bbqr.Split(data, t, opts)
	if err != nil {
		return err
	}
	for _, p := range s.Parts {
		fmt.Fprintln(stdout, p)
	}
	return writePNGs(*pngDir, "", s.Parts)
}

func cmdDecode(stdin io.Reader, stdout, stderr io.Writer, args []string) error {
	fs := flag.NewFlagSet("decode", flag.ContinueOnError)
	limit := fs.Int("limit", 1<<30, "decoded payload size cap in bytes")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	groups, err := readParts(stdin, fs.Args())
	if err != nil {
		return err
	}
	if len(groups) != 1 {
		return fmt.Errorf("decode takes exactly one series, got %d", len(groups))
	}
	d := bbqr.Decoder{Limit: *limit}
	for _, p := range groups[0] {
		if err := d.Add(p); err != nil {
			return err
		}
	}
	typ, data, err := d.Result()
	if err != nil {
		return err
	}
	if _, err := stdout.Write(data); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "type %c, %d bytes\n", typ, len(data))
	if typ == bbqr.TypeShamir {
		if sh, err := shamir.ParseShare(data); err == nil {
			fmt.Fprintf(stderr, "note: payload is a shamir share, threshold %d index %d; use combine\n",
				sh.Threshold, sh.Index)
		}
	}
	return nil
}

func cmdSplit(stdin io.Reader, stdout io.Writer, args []string) error {
	fs := flag.NewFlagSet("split", flag.ContinueOnError)
	k := fs.Int("k", 0, "threshold: shares needed to recover")
	n := fs.Int("n", 0, "total shares to issue")
	typ := fs.String("type", "B", "file type of the split data: P, T, J, C, U, B or a name: psbt, txn, json, cbor, text, binary")
	pngDir := fs.String("png", "", "also write one QR PNG per part into this directory")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *k < 2 || *n < *k {
		return fmt.Errorf("bad threshold: want 2 <= k (%d) <= n (%d)", *k, *n)
	}
	t, err := parseType(*typ)
	if err != nil {
		return err
	}
	data, err := readInput(stdin, fs.Args())
	if err != nil {
		return err
	}
	series, err := shamir.SplitData(t, data, *k, *n, rand.Reader)
	if err != nil {
		return err
	}
	for i, s := range series {
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		for _, p := range s.Parts {
			fmt.Fprintln(stdout, p)
		}
		if err := writePNGs(*pngDir, fmt.Sprintf("share-%d-", i+1), s.Parts); err != nil {
			return err
		}
	}
	return nil
}

func cmdCombine(stdin io.Reader, stdout, stderr io.Writer, args []string) error {
	fs := flag.NewFlagSet("combine", flag.ContinueOnError)
	limit := fs.Int("limit", 1<<30, "recovered data size cap in bytes")
	descText := fs.Bool("descriptor", false, "print a recovered wallet descriptor (crypto-output CBOR) as descriptor text")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	groups, err := readParts(stdin, fs.Args())
	if err != nil {
		return err
	}
	s := shamir.Set{Limit: *limit}
	for _, g := range groups {
		typ, payload, err := bbqr.Join(g)
		if err != nil {
			return err
		}
		if typ != bbqr.TypeShamir {
			return fmt.Errorf("series has file type %c, not a share (%c)", typ, bbqr.TypeShamir)
		}
		if err := s.Add(payload); err != nil {
			return err
		}
	}
	rec, err := s.Recover()
	if errors.Is(err, shamir.ErrAmbiguous) {
		have, _ := s.Progress()
		return fmt.Errorf("cannot tell which shares are corrupt: more than one reading of the %d shares verifies with equal support; add another share", have)
	}
	if err != nil {
		return err
	}
	if *descText {
		if rec.FileType != bbqr.TypeCBOR {
			return fmt.Errorf("-descriptor: recovered type %c, not CBOR", rec.FileType)
		}
		d, err := urtypes.Parse("crypto-output", rec.Data)
		if err != nil {
			return fmt.Errorf("-descriptor: %w", err)
		}
		desc, ok := d.(*bip380.Descriptor)
		if !ok {
			return fmt.Errorf("-descriptor: recovered %T, not a wallet descriptor", d)
		}
		fmt.Fprintln(stdout, desc.Encode())
	} else if _, err := stdout.Write(rec.Data); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "recovered %d bytes, type %c\n", len(rec.Data), rec.FileType)
	if len(rec.Corrupt) > 0 {
		fmt.Fprintf(stderr, "warning: %s; recovered from the others\n", corruptWarning(rec.Corrupt))
	}
	return nil
}

// corruptWarning names every corrupt share index, in the singular for
// one: "share 4 is corrupt", "shares 1, 4 are corrupt".
func corruptWarning(idx []int) string {
	list := make([]string, len(idx))
	for i, x := range idx {
		list[i] = strconv.Itoa(x)
	}
	if len(idx) == 1 {
		return "share " + list[0] + " is corrupt"
	}
	return "shares " + strings.Join(list, ", ") + " are corrupt"
}

// readInput reads all data from the named file, or stdin with no file.
func readInput(stdin io.Reader, args []string) ([]byte, error) {
	if len(args) > 1 {
		return nil, fmt.Errorf("want at most one input file, got %d", len(args))
	}
	if len(args) == 1 {
		return os.ReadFile(args[0])
	}
	return io.ReadAll(stdin)
}

// readParts reads BBQr parts, one per line, grouped into series by
// blank lines.
func readParts(stdin io.Reader, args []string) ([][]string, error) {
	raw, err := readInput(stdin, args)
	if err != nil {
		return nil, err
	}
	var groups [][]string
	var cur []string
	for line := range strings.Lines(string(raw)) {
		line = strings.TrimSpace(line)
		if line == "" {
			if cur != nil {
				groups = append(groups, cur)
				cur = nil
			}
			continue
		}
		cur = append(cur, line)
	}
	if cur != nil {
		groups = append(groups, cur)
	}
	if len(groups) == 0 {
		return nil, fmt.Errorf("no parts found")
	}
	return groups, nil
}

// writePNGs renders each part as a QR PNG into dir, unless dir is
// empty. The QR library picks the alphanumeric mode for the BBQr
// character set on its own.
func writePNGs(dir, prefix string, parts []string) error {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for i, p := range parts {
		code, err := qr.Encode(p, qr.L)
		if err != nil {
			return err
		}
		name := filepath.Join(dir, fmt.Sprintf("%spart-%02d.png", prefix, i))
		if err := os.WriteFile(name, code.PNG(), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func parseType(s string) (byte, error) {
	if len(s) == 1 && s[0] >= 'A' && s[0] <= 'Z' {
		return s[0], nil
	}
	switch strings.ToLower(s) {
	case "psbt":
		return bbqr.TypePSBT, nil
	case "txn":
		return bbqr.TypeTxn, nil
	case "json":
		return bbqr.TypeJSON, nil
	case "cbor":
		return bbqr.TypeCBOR, nil
	case "text":
		return bbqr.TypeText, nil
	case "binary":
		return bbqr.TypeBinary, nil
	}
	return 0, fmt.Errorf("bad -type %q", s)
}
