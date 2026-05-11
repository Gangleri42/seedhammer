package nip19

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// vectors lifted from the NIP-19 specification; the npub/nsec strings
// are the canonical bech32 forms of the corresponding 32-byte hex keys.
var vectors = []struct {
	s   string
	hrp string
	hex string
}{
	{
		"npub180cvv07tjdrrgpa0j7j7tmnyl2yr6yr7l8j4s3evf6u64th6gkwsyjh6w6",
		HRPPub,
		"3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d",
	},
	{
		"nsec1vl029mgpspedva04g90vltkh6fvh240zqtv9k0t9af8935ke9laqsnlfe5",
		HRPSec,
		"67dea2ed018072d675f5415ecfaed7d2597555e202d85b3d65ea4e58d2d92ffa",
	},
}

func TestParseKey(t *testing.T) {
	for _, v := range vectors {
		k, err := ParseKey(v.s)
		if err != nil {
			t.Fatalf("ParseKey(%q): %v", v.s, err)
		}
		if k.HRP != v.hrp {
			t.Errorf("ParseKey(%q): hrp = %q, want %q", v.s, k.HRP, v.hrp)
		}
		got := hex.EncodeToString(k.Data[:])
		if got != v.hex {
			t.Errorf("ParseKey(%q): data = %s, want %s", v.s, got, v.hex)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	for _, v := range vectors {
		k, err := ParseKey(v.s)
		if err != nil {
			t.Fatalf("ParseKey(%q): %v", v.s, err)
		}
		if got := k.Bech32(); got != v.s {
			t.Errorf("round trip: got %q, want %q", got, v.s)
		}
	}
}

func TestBech32FromBytes(t *testing.T) {
	// Directly-constructed Keys must produce the canonical form via Bech32().
	for _, v := range vectors {
		b, err := hex.DecodeString(v.hex)
		if err != nil {
			t.Fatal(err)
		}
		k := Key{HRP: v.hrp}
		copy(k.Data[:], b)
		if got := k.Bech32(); got != v.s {
			t.Errorf("Bech32(): got %q, want %q", got, v.s)
		}
	}
}

func TestUpperCase(t *testing.T) {
	// BIP-173 allows all-uppercase as an alternate canonical form.
	for _, v := range vectors {
		k, err := ParseKey(strings.ToUpper(v.s))
		if err != nil {
			t.Fatalf("uppercase ParseKey: %v", err)
		}
		if k.HRP != v.hrp {
			t.Errorf("uppercase: hrp = %q, want %q", k.HRP, v.hrp)
		}
	}
}

func TestRejectMixedCase(t *testing.T) {
	mixed := vectors[0].s[:5] + strings.ToUpper(vectors[0].s[5:])
	_, err := ParseKey(mixed)
	if !errors.Is(err, errInvalidCase) {
		t.Fatalf("mixed case: err = %v, want errInvalidCase", err)
	}
}

func TestRejectBadChecksum(t *testing.T) {
	// Flip the last symbol to break the checksum without changing length
	// or alphabet membership.
	s := vectors[0].s
	last := s[len(s)-1]
	var flip byte
	for i := range len(alphabet) {
		if alphabet[i] != last {
			flip = alphabet[i]
			break
		}
	}
	bad := s[:len(s)-1] + string(flip)
	_, err := ParseKey(bad)
	if !errors.Is(err, errInvalidChecksum) {
		t.Fatalf("bad checksum: err = %v, want errInvalidChecksum", err)
	}
}

func TestRejectCompound(t *testing.T) {
	// Synthetic compound HRP with a structurally-valid envelope is enough
	// to exercise the compound-rejection path because the HRP check runs
	// before any length check on the payload.
	cases := []string{"nprofile", "nevent", "naddr", "nrelay"}
	for _, hrp := range cases {
		// Encode 32 zero bytes under the compound HRP — produces a
		// bech32-valid string whose body has the right shape; rejection
		// is driven by the HRP, not the body.
		s := encode(hrp, make([]byte, 32))
		_, err := ParseKey(s)
		if !errors.Is(err, errCompoundEntity) {
			t.Errorf("hrp %q: err = %v, want errCompoundEntity", hrp, err)
		}
	}
	if !IsCompound("nprofile1abc") {
		t.Errorf("IsCompound failed to recognise nprofile1...")
	}
	if IsCompound("npub1abc") {
		t.Errorf("IsCompound mis-tagged npub as compound")
	}
}

func TestRejectUnknownHRP(t *testing.T) {
	s := encode("nfoo", make([]byte, 32))
	_, err := ParseKey(s)
	if !errors.Is(err, errInvalidHRP) {
		t.Fatalf("unknown hrp: err = %v, want errInvalidHRP", err)
	}
}

func TestRejectWrongLength(t *testing.T) {
	// 16-byte payload under the npub HRP — bech32-valid but wrong length.
	s := encode(HRPPub, make([]byte, 16))
	_, err := ParseKey(s)
	if !errors.Is(err, errInvalidLength) {
		t.Fatalf("short payload: err = %v, want errInvalidLength", err)
	}
}

func TestNpubFrom(t *testing.T) {
	// NIP-19 spec vector: nsec1vl029mg… → derived x-only pubkey.
	// Private key 67dea2ed018072d675f5415ecfaed7d2597555e202d85b3d65ea4e58d2d92ffa
	// → x-only pubkey 7e7e9c42a91bfef19fa929e5fda1b72e0ebc1a4c1141673e2794234d86addf4e.
	const (
		nsecStr = "nsec1vl029mgpspedva04g90vltkh6fvh240zqtv9k0t9af8935ke9laqsnlfe5"
		wantHex = "7e7e9c42a91bfef19fa929e5fda1b72e0ebc1a4c1141673e2794234d86addf4e"
	)
	nsec, err := ParseKey(nsecStr)
	if err != nil {
		t.Fatal(err)
	}
	npub, err := NpubFrom(nsec)
	if err != nil {
		t.Fatal(err)
	}
	if npub.HRP != HRPPub {
		t.Errorf("npub hrp = %q, want %q", npub.HRP, HRPPub)
	}
	got := hex.EncodeToString(npub.Data[:])
	if got != wantHex {
		t.Errorf("npub data = %s, want %s", got, wantHex)
	}
}

func TestNpubFromRejectsNpub(t *testing.T) {
	npub, err := ParseKey("npub180cvv07tjdrrgpa0j7j7tmnyl2yr6yr7l8j4s3evf6u64th6gkwsyjh6w6")
	if err != nil {
		t.Fatal(err)
	}
	_, err = NpubFrom(npub)
	if !errors.Is(err, errNotNsec) {
		t.Fatalf("NpubFrom(npub): err = %v, want errNotNsec", err)
	}
}
