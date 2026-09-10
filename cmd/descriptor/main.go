// Command descriptor prints a wallet descriptor as text. It reads the
// crypto-output CBOR the share plates carry, or descriptor text in any
// spelling, from standard input or a file, and writes the descriptor
// in the machine's canonical encoding with its checksum. It is the
// second half of a share recovery on a computer:
//
//	go run github.com/Gangleri42/BBQr/go/cmd/bbqr@v0.1.0 combine shares.txt | go run ./cmd/descriptor
package main

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"seedhammer.com/bc/urtypes"
	"seedhammer.com/bip380"
)

func main() {
	if err := run(os.Stdin, os.Stdout, os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "descriptor: %v\n", err)
		os.Exit(1)
	}
}

func run(stdin io.Reader, stdout io.Writer, args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("want at most one input file, got %d", len(args))
	}
	var in []byte
	var err error
	if len(args) == 1 {
		in, err = os.ReadFile(args[0])
	} else {
		in, err = io.ReadAll(stdin)
	}
	if err != nil {
		return err
	}
	desc, err := parse(in)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, desc.Encode())
	return err
}

// parse accepts the two forms a descriptor reaches a computer in: the
// crypto-output CBOR a share recovery yields, or descriptor text.
func parse(in []byte) (*bip380.Descriptor, error) {
	if d, err := urtypes.Parse("crypto-output", in); err == nil {
		if desc, ok := d.(*bip380.Descriptor); ok {
			return desc, nil
		}
		return nil, fmt.Errorf("crypto-output holds %T, not a wallet descriptor", d)
	}
	desc, err := bip380.Parse(string(bytes.TrimSpace(in)))
	if err != nil {
		return nil, fmt.Errorf("input is neither crypto-output CBOR nor descriptor text: %w", err)
	}
	return desc, nil
}
