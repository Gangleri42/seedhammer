package backup

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"seedhammer.com/engrave"
	"seedhammer.com/font/constant"
	"seedhammer.com/nip19"
)

func nostrKey(t testing.TB, hrp, h string) nip19.Key {
	t.Helper()
	b, err := hex.DecodeString(h)
	if err != nil {
		t.Fatal(err)
	}
	k := nip19.Key{HRP: hrp}
	copy(k.Data[:], b)
	return k
}

// Golden splines are stored as varint-packed deltas (see
// internal/golden). Regenerate with `go test -update`; visualise with
// `go test -dump <dir>` which writes per-test SVG previews.
func TestNsecGolden(t *testing.T) {
	k := nostrKey(t, nip19.HRPSec, "67dea2ed018072d675f5415ecfaed7d2597555e202d85b3d65ea4e58d2d92ffa")
	p, err := EngraveNsec(params, Nsec{
		Title: "nostr nsec",
		Key:   k,
		Font:  constant.Font,
	})
	if err != nil {
		t.Fatal(err)
	}
	compareGolden(t, "nostr-nsec", p)
}

func TestNpubGolden(t *testing.T) {
	k := nostrKey(t, nip19.HRPPub, "3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d")
	p, err := EngraveNpub(params, Npub{
		Title: "nostr npub",
		Key:   k,
		Font:  constant.Font,
	})
	if err != nil {
		t.Fatal(err)
	}
	compareGolden(t, "nostr-npub", p)
}

func TestEngraveNsecRejectsNpub(t *testing.T) {
	pub := nostrKey(t, nip19.HRPPub, "3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d")
	_, err := EngraveNsec(params, Nsec{Key: pub, Font: constant.Font})
	if !errors.Is(err, errNotNsec) {
		t.Fatalf("EngraveNsec(npub): err = %v, want errNotNsec", err)
	}
}

func TestEngraveNpubRejectsNsec(t *testing.T) {
	sec := nostrKey(t, nip19.HRPSec, "67dea2ed018072d675f5415ecfaed7d2597555e202d85b3d65ea4e58d2d92ffa")
	_, err := EngraveNpub(params, Npub{Key: sec, Font: constant.Font})
	if !errors.Is(err, errNotNpub) {
		t.Fatalf("EngraveNpub(nsec): err = %v, want errNotNpub", err)
	}
}

// TestConstantNsecTiming guards the security-critical invariant that
// EngraveNsec emits the same motion profile regardless of the secret
// bytes. A regression here means the side-channel mitigation has
// broken; see also the FuzzConstantNsec target below.
func TestConstantNsecTiming(t *testing.T) {
	hexes := []string{
		"67dea2ed018072d675f5415ecfaed7d2597555e202d85b3d65ea4e58d2d92ffa",
		"0000000000000000000000000000000000000000000000000000000000000001",
		"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		"0101010101010101010101010101010101010101010101010101010101010101",
		"7f7f7f7f7f7f7f7f7f7f7f7f7f7f7f7f7f7f7f7f7f7f7f7f7f7f7f7f7f7f7f7f",
		"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210",
	}
	var ref engrave.Profile
	for i, h := range hexes {
		k := nostrKey(t, nip19.HRPSec, h)
		p, err := EngraveNsec(params, Nsec{Key: k, Font: constant.Font})
		if err != nil {
			t.Fatalf("%s: %v", h, err)
		}
		prof := engrave.ProfileSpline(engrave.PlanEngraving(params.StepperConfig, p))
		if i == 0 {
			ref = prof
			continue
		}
		if !prof.Equal(ref) {
			t.Errorf("nsec %s has profile\n%+v\nexpected\n%+v", h, prof, ref)
		}
	}
}

// FuzzConstantNsec walks the 32-byte secret-key space looking for any
// input that perturbs the engraving's motion profile.
func FuzzConstantNsec(f *testing.F) {
	f.Add([]byte{
		0x67, 0xde, 0xa2, 0xed, 0x01, 0x80, 0x72, 0xd6,
		0x75, 0xf5, 0x41, 0x5e, 0xcf, 0xae, 0xd7, 0xd2,
		0x59, 0x75, 0x55, 0xe2, 0x02, 0xd8, 0x5b, 0x3d,
		0x65, 0xea, 0x4e, 0x58, 0xd2, 0xd9, 0x2f, 0xfa,
	})
	f.Add(make([]byte, nip19.KeyLen))
	ref, err := nsecProfile(make([]byte, nip19.KeyLen))
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) != nip19.KeyLen {
			return
		}
		got, err := nsecProfile(raw)
		if err != nil {
			t.Fatalf("raw=%x: %v", raw, err)
		}
		if !got.Equal(ref) {
			t.Errorf("raw=%x: profile diverges from reference", raw)
		}
	})
}

func nsecProfile(raw []byte) (engrave.Profile, error) {
	if len(raw) != nip19.KeyLen {
		return engrave.Profile{}, errors.New("nsecProfile: expected 32-byte key")
	}
	k := nip19.Key{HRP: nip19.HRPSec}
	copy(k.Data[:], raw)
	p, err := EngraveNsec(params, Nsec{Key: k, Font: constant.Font})
	if err != nil {
		return engrave.Profile{}, err
	}
	return engrave.ProfileSpline(engrave.PlanEngraving(params.StepperConfig, p)), nil
}

// TestConstantFontCoversBech32 documents the assumption that every rune
// in an uppercased Nostr key is present in the constant-stroke font.
// Without this guarantee EngraveSeedString panics with "unsupported
// rune"; failing here at test time is preferable.
func TestConstantFontCoversBech32(t *testing.T) {
	const alphabet = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"
	for _, r := range "NSEC1NPUB1" + strings.ToUpper(alphabet) {
		if _, _, ok := constant.Font.Decode(r); !ok {
			t.Errorf("constant.Font lacks rune %q", r)
		}
	}
}
