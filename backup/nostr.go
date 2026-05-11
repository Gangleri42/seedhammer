package backup

import (
	"errors"
	"fmt"

	"seedhammer.com/engrave"
	"seedhammer.com/font/vector"
	"seedhammer.com/nip19"
)

// Nsec is a Nostr secret-key engraving plate. Font must be a
// constant-stroke face; the timing invariant relies on it.
type Nsec struct {
	Title string
	Key   nip19.Key
	Font  *vector.Face
}

// Npub is a Nostr public-key engraving plate. The public key is not
// secret, so the engraving is not constant-time.
type Npub struct {
	Title string
	Key   nip19.Key
	Font  *vector.Face
}

var (
	errNotNsec = errors.New("not an nsec key")
	errNotNpub = errors.New("not an npub key")
)

// EngraveNsec engraves the secret-key plate. The layout reuses the
// single-bech32-string form used by codex32 because it already engraves
// in constant time with a centered QR sidecar.
func EngraveNsec(params engrave.Params, plate Nsec) (engrave.Engraving, error) {
	if plate.Key.HRP != nip19.HRPSec {
		return nil, fmt.Errorf("backup: %w", errNotNsec)
	}
	return EngraveSeedString(params, SeedString{
		Title: plate.Title,
		Seed:  plate.Key.Bech32(),
		Font:  plate.Font,
	})
}

// EngraveNpub engraves the public-key plate. The layout reuses the
// single-bech32-string form (column of 10-char groups, centred QR
// sidecar) used by nsec and codex32 so npub and nsec plates pair
// visually.
func EngraveNpub(params engrave.Params, plate Npub) (engrave.Engraving, error) {
	if plate.Key.HRP != nip19.HRPPub {
		return nil, fmt.Errorf("backup: %w", errNotNpub)
	}
	return EngraveSeedString(params, SeedString{
		Title: plate.Title,
		Seed:  plate.Key.Bech32(),
		Font:  plate.Font,
	})
}
