// Command sh2key manages the boot key of a SeedHammer II: it backs the
// key up as 24 BIP39 words for a steel plate and restores it from them,
// derives the matching Nostr identity, and runs the board ceremony that
// the signing howto performs by hand: classify a board by its OTP
// state, fuse a boot-key slot, sign a firmware image and flash it.
//
// The backup commands touch no device and burn nothing. Every
// irreversible fuse write lives behind the ceremony commands, each
// gated on a fresh readback of the attached board, never on cached
// state. See docs/howto-bootkey-backup.md and
// docs/howto-bootkey-and-signing.md for the ceremonies this
// implements.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	err := run(os.Stdout, os.Stdin, os.Args[1:])
	if err == nil {
		return
	}
	if errors.Is(err, flag.ErrHelp) {
		os.Exit(0)
	}
	fmt.Fprintf(os.Stderr, "sh2key: %v\n", err)
	os.Exit(2)
}

func run(stdout io.Writer, stdin io.Reader, args []string) error {
	if len(args) == 0 || args[0][0] == '-' {
		// Bare invocation (with at most -key) opens the interactive
		// tool on a terminal; scripts and pipes get the usage. The
		// key often lives outside the checkout.
		fs := newFlagSet("sh2key", "[-key file]")
		keyFlag := fs.String("key", "", "key `file` for the interactive tool")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() > 0 {
			return fmt.Errorf("put the command first: sh2key %s [flags]", fs.Arg(0))
		}
		if fout, ok := stdout.(*os.File); ok && isTerminal(fout) {
			if fin, ok := stdin.(*os.File); ok && isTerminal(fin) {
				return runTUI(*keyFlag)
			}
		}
		usage(stdout)
		return errors.New("missing command")
	}
	cmd := args[0]
	args = args[1:]
	switch cmd {
	case "backup":
		return cmdBackup(stdout, args)
	case "restore":
		return cmdRestore(stdout, stdin, args)
	case "nsec":
		return cmdNsec(stdout, args)
	case "mint":
		return cmdMint(stdout, args)
	case "status":
		return cmdStatus(stdout, args)
	case "setup-udev":
		return cmdSetupUdev(stdout, args)
	case "sign":
		return cmdSign(stdout, args)
	case "flash":
		return cmdFlash(stdout, args)
	case "provision":
		return cmdProvision(stdout, args)
	case "enable-secure-boot":
		return cmdEnableSecureBoot(stdout, args)
	case "help", "-h", "-help", "--help":
		usage(stdout)
		return flag.ErrHelp
	default:
		return fmt.Errorf("unknown command %q (run 'sh2key help')", cmd)
	}
}

func usage(w io.Writer) {
	io.WriteString(w, `usage: sh2key [command] [flags]

Run bare on a terminal, sh2key opens its interactive tool: key and
board panels, guided backup, restore, signing and the board ceremony.
The commands below are the scripting face of the same verbs.

Back up the boot key. No device, no fuse writes:

  backup <key.pem>     the key as 24 BIP39 words, terminal only
  restore              24 typed words back to a byte-identical PEM
  nsec <key.pem>       the key as a Nostr identity (nsec1 and npub1)
  mint                 mint a new boot key, nothing else

Provision a board and sign firmware. Needs picotool for device steps:

  status               classify the attached board, write nothing
  setup-udev           one-time Linux USB access for picotool; sudo, shown first
  sign [uf2]           sign a firmware image; no device involved
  flash [uf2]          flash an image; the fuse-free default for DIY boards
  provision [uf2]      mint, fuse, sign and flash, skipping what is done
  enable-secure-boot   one-way door: a DIY board starts requiring signatures

Run 'sh2key <command> -h' for the flags of each command.
`)
}

// newFlagSet returns a flag set that reports errors instead of
// exiting, so run stays callable in-process from tests.
func newFlagSet(name, oneliner string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: sh2key %s\n", oneliner)
		fs.PrintDefaults()
	}
	return fs
}

// errStr is a constant error, for the small fixed refusals that carry
// no context of their own.
type errStr string

func (e errStr) Error() string { return string(e) }

// popArg splits off a leading non-flag argument. The howtos write
// `sh2key backup key.pem -nfc` with the positional first, and the
// flag package alone stops parsing at it; both orders work this way.
func popArg(args []string) (string, []string) {
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		return args[0], args[1:]
	}
	return "", args
}

// oneArg resolves the single positional of a command from either
// order, empty when absent.
func oneArg(fs *flag.FlagSet, lead string) (string, error) {
	switch {
	case lead == "" && fs.NArg() == 0:
		return "", nil
	case lead == "" && fs.NArg() == 1:
		return fs.Arg(0), nil
	case lead != "" && fs.NArg() == 0:
		return lead, nil
	default:
		return "", fmt.Errorf("too many arguments (%s)", strings.Join(append([]string{lead}, fs.Args()...), " "))
	}
}
