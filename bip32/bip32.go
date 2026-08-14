// package bip32 contains helper functions for operating on bitcoin bip32
// extended keys.
package bip32

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/btcsuite/btcd/address/v2"
	"github.com/btcsuite/btcd/btcutil/v2/hdkeychain"
	"github.com/btcsuite/btcd/chaincfg/v2"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

type Path []uint32

func (p Path) String() string {
	var d strings.Builder
	d.WriteRune('m')
	for _, p := range p {
		d.WriteByte('/')
		idx := p
		if p >= hdkeychain.HardenedKeyStart {
			idx -= hdkeychain.HardenedKeyStart
		}
		d.WriteString(strconv.Itoa(int(idx)))
		if p >= hdkeychain.HardenedKeyStart {
			d.WriteByte('h')
		}
	}
	return d.String()
}

// Fingerprint is the first 4 bytes of the RIPEMD160(SHA256(pkey)).
func Fingerprint(pkey *secp256k1.PublicKey) uint32 {
	mfp := address.Hash160(pkey.SerializeCompressed())[:4]
	return binary.BigEndian.Uint32(mfp)
}

func Derive(mk *hdkeychain.ExtendedKey, path Path) (xpub *hdkeychain.ExtendedKey, err error) {
	key := mk
	for _, p := range path {
		key, err = key.Derive(p)
		if err != nil {
			return
		}
	}
	xpub, err = key.Neuter()
	return
}

func NetworkFor(xpub *hdkeychain.ExtendedKey) (*chaincfg.Params, error) {
	networks := []*chaincfg.Params{
		&chaincfg.MainNetParams,
		&chaincfg.TestNet3Params,
		&chaincfg.SimNetParams,
	}
	for _, n := range networks {
		if xpub.IsForNet(n) {
			return n, nil
		}
	}
	return nil, errors.New("unknown network")
}

func ParsePathElement(p string) (uint32, error) {
	offset := uint32(0)
	if strings.HasSuffix(p, "h") || strings.HasSuffix(p, "'") {
		offset = hdkeychain.HardenedKeyStart
		p = p[:len(p)-1]
	}
	// The element range is [0, 2^31) on every platform. Parsing with
	// the platform int accepted [2^31, 2^63) on 64-bit hosts, aliasing
	// the first half of it with the hardening offset, while the 32-bit
	// device rejected the same elements.
	idx, err := strconv.ParseUint(p, 10, 32)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) {
			return 0, fmt.Errorf("bip32: path element out of range: %q", p)
		}
		return 0, fmt.Errorf("bip32: invalid path element: %q", p)
	}
	if idx >= hdkeychain.HardenedKeyStart {
		return 0, fmt.Errorf("bip32: path element out of range: %q", p)
	}
	return uint32(idx) + offset, nil
}

func ParsePath(path string) (Path, error) {
	var res Path
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] != "m" {
		return nil, fmt.Errorf("bip32: missing m/ prefix: %q", path)
	}
	parts = parts[1:]
	// BIP32 serializes depth as one byte, so no extended key at a
	// deeper path can exist. bip380's key encoding writes
	// uint8(len(path)): unbounded, a 260-deep origin re-encodes
	// byte-identical to a genuine 4-deep key.
	if len(parts) > 255 {
		return nil, fmt.Errorf("bip32: %d path elements; BIP32 depth is one byte", len(parts))
	}
	for _, p := range parts {
		p, err := ParsePathElement(p)
		if err != nil {
			return nil, err
		}
		res = append(res, p)
	}
	return res, nil
}

func (p Path) Encode() string {
	res := new(strings.Builder)
	for _, e := range p {
		res.WriteByte('/')
		hard := e >= hdkeychain.HardenedKeyStart
		if hard {
			e -= hdkeychain.HardenedKeyStart
		}
		res.WriteString(strconv.Itoa(int(e)))
		if hard {
			res.WriteByte('h')
		}
	}
	return res.String()
}
