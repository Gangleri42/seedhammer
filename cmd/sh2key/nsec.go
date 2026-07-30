package main

import (
	"errors"
	"io"
	"os"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"seedhammer.com/nip19"
)

// nostrKeys wraps the scalar as its NIP-19 pair.
func nostrKeys(priv *secp256k1.PrivateKey) (sec, pub nip19.Key, err error) {
	sec = nip19.Key{HRP: nip19.HRPSec}
	copy(sec.Data[:], priv.Serialize())
	pub, err = nip19.NpubFrom(sec)
	return sec, pub, err
}

// The boot key scalar is a valid Nostr secret key: same curve, same
// [1, n-1] range. Deriving and using the nsec makes the boot key
// double as a Nostr identity: one secret, two protocols. Whoever
// holds either form can both sign firmware for the fused boards and
// act as that Nostr key; the words plate then backs up both.

func cmdNsec(stdout io.Writer, args []string) error {
	fs := newFlagSet("nsec", "nsec <key.pem> [-nfc]")
	nfc := fs.Bool("nfc", false, "send the nsec to the engraver; the device derives the npub itself")
	lead, rest := popArg(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	keyPath, err := oneArg(fs, lead)
	if err != nil {
		return err
	}
	if keyPath == "" {
		return errors.New("nsec: name the key file, e.g. sh2key nsec sh2-bootkey.pem")
	}
	priv, err := loadKeyFile(keyPath)
	if err != nil {
		return err
	}
	sec, pub, err := nostrKeys(priv)
	if err != nil {
		return err
	}
	if *nfc {
		u := newUI(os.Stderr)
		u.printf("  sending the nsec; the device offers the Nostr flow (NSEC plate, then NPUB)\n")
		if err := sendNFC(u, nfcRaw, []byte(sec.Bech32()+"\n")); err != nil {
			return err
		}
		u.printf("  npub  %s\n", pub.Bech32())
		u.printf("  %s\n", u.warn("one secret, two protocols: this Nostr identity IS the boot key"))
		return nil
	}
	if !isTTYWriter(stdout) {
		return errors.New("stdout is not a terminal, the nsec would be captured by the pipe; use -nfc to engrave it instead")
	}
	u := newUI(stdout)
	u.printf("\n  nsec  %s\n", u.bold(sec.Bech32()))
	u.printf("  npub  %s\n\n", pub.Bech32())
	u.printf("  %s\n", u.warn("one secret, two protocols: whoever holds this nsec can sign firmware"))
	u.printf("  %s\n\n", u.warn("for the fused boards, and the boot key words plate restores this nsec"))
	return nil
}
