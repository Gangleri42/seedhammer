// package nip19 parses NIP-19 bech32-encoded Nostr entities.
// Only the simple 32-byte keys npub and nsec are supported; the
// compound entities nprofile, nevent, naddr, and nrelay are rejected
// because their TLV bodies (relay hints, event ids) rot and are not
// meaningful to engrave on a permanent plate.
package nip19

import (
	"errors"
	"fmt"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// HRP values produced by ParseKey.
const (
	HRPPub = "npub"
	HRPSec = "nsec"
)

// KeyLen is the byte length of a NIP-19 simple-key payload.
const KeyLen = 32

// Key is a 32-byte Nostr key with its bech32 HRP.
type Key struct {
	HRP  string
	Data [KeyLen]byte
}

var (
	errInvalidHRP       = errors.New("invalid hrp")
	errInvalidChecksum  = errors.New("invalid checksum")
	errInvalidLength    = errors.New("invalid length")
	errInvalidCase      = errors.New("invalid case")
	errInvalidCharacter = errors.New("invalid character")
	errCompoundEntity   = errors.New("compound entity not supported")
	errNotNsec          = errors.New("not an nsec key")
)

// ParseKey decodes a npub1 or nsec1 string into a Key. Mixed case is
// rejected per BIP-173. nprofile, nevent, naddr, and nrelay are rejected
// even when the bech32 envelope is valid.
func ParseKey(s string) (Key, error) {
	hrp, data, err := decode(s)
	if err != nil {
		return Key{}, fmt.Errorf("nip19: %w", err)
	}
	switch hrp {
	case HRPPub, HRPSec:
	case "nprofile", "nevent", "naddr", "nrelay":
		return Key{}, fmt.Errorf("nip19: %w", errCompoundEntity)
	default:
		return Key{}, fmt.Errorf("nip19: %w", errInvalidHRP)
	}
	if len(data) != KeyLen {
		return Key{}, fmt.Errorf("nip19: %w", errInvalidLength)
	}
	k := Key{HRP: hrp}
	copy(k.Data[:], data)
	return k, nil
}

// Bech32 returns the canonical lowercase bech32 form of the Key. Named
// to not satisfy [fmt.Stringer]; that lets dead-code elimination drop
// the encoder when the firmware only parses (never re-emits) keys.
func (k Key) Bech32() string {
	return encode(k.HRP, k.Data[:])
}

// IsCompound reports whether a leading-token of a candidate string looks
// like a NIP-19 compound entity. Used by callers that want to surface a
// dedicated error message before attempting a full parse.
func IsCompound(s string) bool {
	for _, hrp := range [...]string{"nprofile", "nevent", "naddr", "nrelay"} {
		if len(s) > len(hrp) && s[:len(hrp)] == hrp && s[len(hrp)] == '1' {
			return true
		}
	}
	return false
}

// NpubFrom derives the x-only public key (BIP-340 / NIP-19 npub) from an
// nsec secret key. The result is suitable for engraving on the public
// plate paired with the secret plate.
func NpubFrom(nsec Key) (Key, error) {
	if nsec.HRP != HRPSec {
		return Key{}, fmt.Errorf("nip19: %w", errNotNsec)
	}
	priv := secp256k1.PrivKeyFromBytes(nsec.Data[:])
	pub := priv.PubKey()
	npub := Key{HRP: HRPPub}
	// X().FillBytes writes the 32-byte big-endian X coordinate, zero-padded
	// on the left if shorter — matches the picosign signing convention.
	pub.X().FillBytes(npub.Data[:])
	return npub, nil
}
