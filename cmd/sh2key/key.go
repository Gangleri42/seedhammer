package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"seedhammer.com/bip39"
)

// The boot key is a plain secp256k1 scalar. Its public key hash is
// what the RP2350 boot ROM compares against a fused BOOTKEY slot:
// SHA-256 over the uncompressed 64-byte X||Y form, without the 0x04
// prefix (picobin SIGNATURE item layout, cmd/picosign X/Y assembly).

var (
	oidSecp256k1   = asn1.ObjectIdentifier{1, 3, 132, 0, 10}
	oidECPublicKey = asn1.ObjectIdentifier{1, 2, 840, 10045, 2, 1}
)

// curveName resolves the OIDs a stray PEM plausibly carries, so a
// rejection can name what was found instead of shrugging.
func curveName(oid asn1.ObjectIdentifier) string {
	names := map[string]string{
		"1.3.132.0.10":         "secp256k1",
		"1.2.840.10045.3.1.7":  "prime256v1 (NIST P-256)",
		"1.3.132.0.34":         "secp384r1 (NIST P-384)",
		"1.3.132.0.35":         "secp521r1 (NIST P-521)",
		"1.3.132.0.33":         "secp224r1 (NIST P-224)",
		"1.2.840.10045.3.1.1":  "prime192v1 (NIST P-192)",
		"1.3.36.3.3.2.8.1.1.7": "brainpoolP256r1",
	}
	if n, ok := names[oid.String()]; ok {
		return n
	}
	return oid.String()
}

// ecPrivateKey is the SEC 1 / RFC 5915 ECPrivateKey structure, the
// exact form `openssl ecparam -genkey` writes.
type ecPrivateKey struct {
	Version       int
	PrivateKey    []byte
	NamedCurveOID asn1.ObjectIdentifier `asn1:"optional,explicit,tag:0"`
	PublicKey     asn1.BitString        `asn1:"optional,explicit,tag:1"`
}

// pkcs8 is the outer PrivateKeyInfo envelope of a PKCS#8 PEM. Its
// PrivateKey octet string holds a nested ecPrivateKey.
type pkcs8 struct {
	Version int
	Algo    struct {
		Algorithm  asn1.ObjectIdentifier
		Parameters asn1.RawValue `asn1:"optional"`
	}
	PrivateKey []byte
}

// keyFromScalar range-checks a 32-byte scalar and wraps it as a
// private key. A value of zero or at or above the curve order cannot
// come from a real key; the odds of hitting one from random input are
// about 2^-128, which means it never happens by accident and must
// still be rejected explicitly.
func keyFromScalar(b []byte) (*secp256k1.PrivateKey, error) {
	if len(b) != 32 {
		return nil, fmt.Errorf("key scalar is %d bytes, need 32", len(b))
	}
	var s secp256k1.ModNScalar
	if overflow := s.SetByteSlice(b); overflow {
		return nil, errors.New("scalar is at or above the secp256k1 curve order; not a valid private key")
	}
	if s.IsZero() {
		return nil, errors.New("scalar is zero; not a valid private key")
	}
	return secp256k1.NewPrivateKey(&s), nil
}

// parseKeyPEM parses a secp256k1 private key from PEM data. It
// accepts the SEC1 form the howto ceremony mints and the unencrypted
// PKCS#8 form other tools emit, skips EC PARAMETERS blocks, and
// rejects everything else by name.
func parseKeyPEM(data []byte) (*secp256k1.PrivateKey, error) {
	var seen []string
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			break
		}
		switch block.Type {
		case "EC PARAMETERS":
			// openssl ecparam without -noout prepends this; harmless.
			continue
		case "EC PRIVATE KEY":
			if _, ok := block.Headers["DEK-Info"]; ok {
				return nil, errors.New("the key is encrypted (legacy DEK-Info PEM); decrypt it with openssl first, this tool never prompts for passphrases")
			}
			return parseSEC1(block.Bytes, nil)
		case "ENCRYPTED PRIVATE KEY":
			return nil, errors.New("the key is encrypted (PKCS#8); decrypt it with openssl first, this tool never prompts for passphrases")
		case "PRIVATE KEY":
			var p8 pkcs8
			if rest, err := asn1.Unmarshal(block.Bytes, &p8); err != nil {
				return nil, fmt.Errorf("invalid PKCS#8 structure: %w", err)
			} else if len(rest) > 0 {
				return nil, errors.New("invalid PKCS#8 structure: trailing data")
			}
			if !p8.Algo.Algorithm.Equal(oidECPublicKey) {
				return nil, fmt.Errorf("not an EC key: PKCS#8 algorithm is %v", p8.Algo.Algorithm)
			}
			var oid asn1.ObjectIdentifier
			if len(p8.Algo.Parameters.FullBytes) > 0 {
				if _, err := asn1.Unmarshal(p8.Algo.Parameters.FullBytes, &oid); err != nil {
					return nil, fmt.Errorf("invalid PKCS#8 curve parameters: %w", err)
				}
			}
			return parseSEC1(p8.PrivateKey, oid)
		default:
			seen = append(seen, block.Type)
		}
	}
	if len(seen) > 0 {
		return nil, fmt.Errorf("no private key found in PEM input (saw %s)", strings.Join(seen, ", "))
	}
	return nil, errors.New("no PEM data found")
}

func parseSEC1(der []byte, outerOID asn1.ObjectIdentifier) (*secp256k1.PrivateKey, error) {
	var ec ecPrivateKey
	if rest, err := asn1.Unmarshal(der, &ec); err != nil {
		return nil, fmt.Errorf("invalid SEC1 structure: %w", err)
	} else if len(rest) > 0 {
		return nil, errors.New("invalid SEC1 structure: trailing data")
	}
	if ec.Version != 1 {
		return nil, fmt.Errorf("unsupported SEC1 version %d", ec.Version)
	}
	oid := ec.NamedCurveOID
	if len(oid) == 0 {
		oid = outerOID
	}
	switch {
	case len(oid) == 0:
		return nil, errors.New("the PEM names no curve; cannot confirm it is a secp256k1 key")
	case !oid.Equal(oidSecp256k1):
		return nil, fmt.Errorf("not a secp256k1 key: curve is %s", curveName(oid))
	}
	if len(ec.PrivateKey) > 32 {
		return nil, fmt.Errorf("key scalar is %d bytes, need at most 32", len(ec.PrivateKey))
	}
	var scalar [32]byte
	copy(scalar[32-len(ec.PrivateKey):], ec.PrivateKey)
	priv, err := keyFromScalar(scalar[:])
	if err != nil {
		return nil, err
	}
	// A PEM minted by openssl embeds the public point. Checking it
	// against the point derived from the scalar catches a corrupted
	// file before anything downstream trusts it.
	if emb := ec.PublicKey.Bytes; len(emb) > 0 {
		if len(emb) != 65 || emb[0] != 0x04 {
			return nil, errors.New("embedded public key is not in uncompressed form")
		}
		if !bytes.Equal(emb[1:], pubXY(priv.PubKey())) {
			return nil, errors.New("the embedded public key does not match the private scalar; the PEM is corrupted")
		}
	}
	return priv, nil
}

// marshalKeyPEM encodes the key exactly as `openssl ecparam -genkey
// -noout` does: SEC1 with the curve OID and the uncompressed public
// point embedded. Byte-identical output is load-bearing: the backup
// howto proves a restore with cmp(1) against the original file.
func marshalKeyPEM(priv *secp256k1.PrivateKey) []byte {
	pub := append([]byte{0x04}, pubXY(priv.PubKey())...)
	der, err := asn1.Marshal(ecPrivateKey{
		Version:       1,
		PrivateKey:    priv.Serialize(),
		NamedCurveOID: oidSecp256k1,
		PublicKey:     asn1.BitString{Bytes: pub, BitLength: len(pub) * 8},
	})
	if err != nil {
		// Marshalling a fixed-shape structure cannot fail.
		panic(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}

// pubXY returns the uncompressed 64-byte X||Y form of a public key,
// zero-padded per coordinate, without the 0x04 prefix.
func pubXY(pub *secp256k1.PublicKey) []byte {
	xy := make([]byte, 64)
	pub.X().FillBytes(xy[:32])
	pub.Y().FillBytes(xy[32:])
	return xy
}

// fingerprint is SHA-256 of the 64-byte X||Y public key: the value a
// BOOTKEY OTP slot holds, the value plate 2 tells a restorer to check.
func fingerprint(priv *secp256k1.PrivateKey) [32]byte {
	return sha256.Sum256(pubXY(priv.PubKey()))
}

func fingerprintHex(priv *secp256k1.PrivateKey) string {
	fp := fingerprint(priv)
	return hex.EncodeToString(fp[:])
}

// parseFingerprint accepts a -verify argument: 64 hex digits, or any
// unambiguous prefix of at least 16, matching what plate 2 carries.
// Spaces are stripped, so the plate's grouped form types verbatim.
func parseFingerprint(s string) ([]byte, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "")
	if n := len(s); n < 16 || n > 64 || n%2 != 0 {
		return nil, fmt.Errorf("fingerprint must be 16 to 64 hex digits (an even number), got %d", len(s))
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("fingerprint is not hex: %v", err)
	}
	return b, nil
}

func fingerprintMatches(prefix []byte, fp [32]byte) bool {
	return bytes.HasPrefix(fp[:], prefix)
}

// mnemonicFromKey encodes the 32-byte scalar as 24 BIP39 words. The
// entropy IS the private key: no PBKDF2, no BIP32 derivation, no
// passphrase. Every valid scalar is below 2^256, so this never fails.
func mnemonicFromKey(priv *secp256k1.PrivateKey) bip39.Mnemonic {
	return bip39.New(priv.Serialize())
}

// keyFromMnemonic decodes 24 words back into the private key.
func keyFromMnemonic(m bip39.Mnemonic) (*secp256k1.PrivateKey, error) {
	if len(m) != 24 {
		return nil, fmt.Errorf("got %d words; the boot key backup is 24", len(m))
	}
	if !m.Valid() {
		return nil, bip39.ErrInvalidChecksum
	}
	return keyFromScalar(m.Entropy())
}

// loadKeyFile reads and parses a private key PEM.
func loadKeyFile(path string) (*secp256k1.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	priv, err := parseKeyPEM(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return priv, nil
}
