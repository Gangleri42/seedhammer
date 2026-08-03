package main

import (
	"bytes"
	"encoding/asn1"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"seedhammer.com/picobin"
	"seedhammer.com/uf2"
)

// Signing runs entirely in-process: the picosign flow (embed the
// public key, hash, sign, embed the signature) against the in-tree
// picobin and uf2 packages, ECDSA via the decred library. No openssl,
// no xxd, and the input file is never modified: every run signs a
// fresh copy so the pristine unsigned image survives for next time.

const defaultKeyPath = "sh2-bootkey.pem"

// resolveKeyPath finds the ceremony key. An explicit -key value must
// exist. The default keeps the convention name first for
// compatibility, then discovers key PEMs in the working directory:
// exactly one wins regardless of its name, and several is the user's
// choice to make, never a silent pick.
// errNoKeyHere reports a working directory holding no key to adopt.
// provision is allowed to mint past this one and past a missing named
// file, and past nothing else: an ambiguous directory must stop it,
// because it is the only command that burns a write-once fuse.
var errNoKeyHere = errors.New("no PEM here parses as a secp256k1 private key; mint one with 'sh2key mint' or name yours with -key")

func resolveKeyPath(flagVal string) (string, error) {
	if flagVal != defaultKeyPath && flagVal != "" {
		if _, err := os.Stat(flagVal); err != nil {
			return "", err
		}
		return flagVal, nil
	}
	if _, err := os.Stat(defaultKeyPath); err == nil {
		return defaultKeyPath, nil
	}
	cands := keyCandidates()
	switch len(cands) {
	case 1:
		return cands[0], nil
	case 0:
		return "", errNoKeyHere
	default:
		return "", fmt.Errorf("several keys here: %s; name one with -key", strings.Join(cands, ", "))
	}
}

// keyCandidates lists the PEMs in the working directory that parse
// as secp256k1 private keys, the only files worth offering as boot
// keys; certificates and public keys filter themselves out.
func keyCandidates() []string {
	matches, _ := filepath.Glob("*.pem")
	var out []string
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		if _, err := parseKeyPEM(data); err == nil {
			out = append(out, m)
		}
	}
	return out
}

// uf2Info is the preflight parse of a firmware image.
type uf2Info struct {
	firmware  []byte
	startAddr uint32
	img       *picobin.Image
	sigZero   bool
	pubKey    []byte // 64-byte X||Y as embedded, nil when unsigned
}

func inspectUF2(path string) (*uf2Info, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	r := uf2.NewReader(bytes.NewReader(raw), uf2.FamilyRP2350ARMSigned)
	firmware, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	img, err := picobin.NewImage(bytes.NewReader(firmware))
	if err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	info := &uf2Info{firmware: firmware, startAddr: r.StartAddr, img: img}
	if img.SignatureOffset != 0 {
		if int(img.SignatureOffset)+128 > len(firmware) {
			return nil, fmt.Errorf("%s: SIGNATURE item overruns the image", path)
		}
		keyAndSig := firmware[img.SignatureOffset : img.SignatureOffset+128]
		info.sigZero = allZero(keyAndSig[64:])
		info.pubKey = bytes.Clone(keyAndSig[:64])
	}
	return info, nil
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// checkSignable is gate G8's front half, run before any slot is
// fused: the image must carry a signature section and exactly two
// metadata blocks. Three blocks means it was sealed twice, which the
// boot ROM rejects.
func checkSignable(path string, info *uf2Info) error {
	if info.img.NumBlocks > 2 {
		return fmt.Errorf("%s has %d metadata blocks: it was sealed twice and the boot ROM rejects it; rebuild the image", path, info.img.NumBlocks)
	}
	if info.img.SignatureOffset == 0 {
		return fmt.Errorf("%s has no SIGNATURE section; build one with 'nix run .#build-firmware' or take the published *-unsigned edge asset", path)
	}
	return nil
}

// unsignedCandidates lists the signable, still-unsigned images here:
// a signature section present, its bytes zero, not double-sealed.
func unsignedCandidates() []string {
	matches, _ := filepath.Glob("*.uf2")
	sort.Strings(matches)
	var cands []string
	for _, m := range matches {
		if strings.HasSuffix(m, ".signed.uf2") {
			continue
		}
		info, err := inspectUF2(m)
		if err != nil || info.img.SignatureOffset == 0 || info.img.NumBlocks > 2 || !info.sigZero {
			continue
		}
		cands = append(cands, m)
	}
	return cands
}

// findUnsignedUF2 resolves the firmware input by glob, because the
// three sources disagree on naming: build-firmware emits
// seedhammerii-<version>.uf2, the edge release publishes
// seedhammerii-latest-unsigned.uf2, and people rename things.
func findUnsignedUF2() (string, error) {
	cands := unsignedCandidates()
	switch len(cands) {
	case 1:
		return cands[0], nil
	case 0:
		return "", errors.New("no unsigned firmware image found here; name one, build one with 'nix run .#build-firmware', or download the *-unsigned edge asset")
	default:
		return "", fmt.Errorf("several unsigned images here: %s; name one", strings.Join(cands, ", "))
	}
}

func signedName(in string) string {
	base := strings.TrimSuffix(in, ".uf2")
	base = strings.TrimSuffix(base, "-unsigned")
	base = strings.TrimSuffix(base, ".unsigned")
	return base + ".signed.uf2"
}

// signImage signs a copy of inPath into outPath and verifies the
// result from disk. The order inside is load-bearing: the digest
// covers the embedded public key, so the key goes in before the
// digest is taken.
func signImage(u *ui, priv *secp256k1.PrivateKey, inPath, outPath string) error {
	info, err := inspectUF2(inPath)
	if err != nil {
		return err
	}
	if err := checkSignable(inPath, info); err != nil {
		return err
	}
	if info.pubKey != nil && !info.sigZero {
		u.printf("  %s\n", u.dim("input is already signed; replacing the signature"))
	}

	var keyAndSig [128]byte
	copy(keyAndSig[:64], pubXY(priv.PubKey()))

	patched := bytes.Clone(info.firmware)
	copy(patched[info.img.SignatureOffset:], keyAndSig[:])
	img, err := picobin.NewImage(bytes.NewReader(patched))
	if err != nil {
		return err
	}
	digest, err := img.HashData(bytes.NewReader(patched), info.startAddr)
	if err != nil {
		return err
	}
	sig, err := ecdsaSignRaw(priv, digest)
	if err != nil {
		return err
	}
	copy(keyAndSig[64:], sig[:])

	// Copy the input and rewrite the signature in place through the
	// UF2 block structure, exactly as cmd/picosign does.
	raw, err := os.ReadFile(inPath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(outPath, raw, 0o644); err != nil {
		return err
	}
	f, err := os.OpenFile(outPath, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	rw := uf2.NewReader(f, uf2.FamilyRP2350ARMSigned)
	if _, err := io.Copy(io.Discard, io.LimitReader(rw, int64(info.img.SignatureOffset))); err != nil {
		f.Close()
		return err
	}
	if _, err := rw.Write(keyAndSig[:]); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := verifySignedImage(outPath, keyAndSig[:64]); err != nil {
		return fmt.Errorf("post-sign verification failed, do not flash %s: %w", outPath, err)
	}
	u.printf("  signed  %s %s %s\n", u.bold(outPath), u.tick(), u.dim("(signature verified from disk)"))
	return nil
}

// ecdsaSignRaw signs a digest and returns the raw 64-byte r||s form
// the picobin SIGNATURE item stores.
func ecdsaSignRaw(priv *secp256k1.PrivateKey, digest []byte) ([64]byte, error) {
	var out [64]byte
	der := ecdsa.Sign(priv, digest).Serialize()
	var parsed struct {
		R, S *big.Int
	}
	rest, err := asn1.Unmarshal(der, &parsed)
	if err != nil {
		return out, err
	}
	if len(rest) > 0 {
		return out, errors.New("trailing data in DER signature")
	}
	parsed.R.FillBytes(out[:32])
	parsed.S.FillBytes(out[32:])
	return out, nil
}

// verifySignedImage is gate G8's back half, and stronger than eyeball
// checks: it re-reads the file, recomputes the covered digest and
// verifies the ECDSA signature against the embedded public key.
func verifySignedImage(path string, wantPub []byte) error {
	info, err := inspectUF2(path)
	if err != nil {
		return err
	}
	if info.img.NumBlocks != 2 {
		return fmt.Errorf("expected exactly 2 metadata blocks, found %d", info.img.NumBlocks)
	}
	if info.img.SignatureOffset == 0 {
		return errors.New("no SIGNATURE item")
	}
	pubKey, sig, err := info.img.Signature()
	if err != nil {
		return err
	}
	if wantPub != nil && !bytes.Equal(pubKey, wantPub) {
		return errors.New("embedded public key is not the signing key")
	}
	digest, err := info.img.HashData(bytes.NewReader(info.firmware), info.startAddr)
	if err != nil {
		return err
	}
	pk, err := secp256k1.ParsePubKey(append([]byte{0x04}, pubKey...))
	if err != nil {
		return fmt.Errorf("embedded public key: %v", err)
	}
	var r, s secp256k1.ModNScalar
	if overflow := r.SetByteSlice(sig[:32]); overflow {
		return errors.New("signature r out of range")
	}
	if overflow := s.SetByteSlice(sig[32:]); overflow {
		return errors.New("signature s out of range")
	}
	if !ecdsa.NewSignature(&r, &s).Verify(digest, pk) {
		return errors.New("ECDSA verification failed")
	}
	return nil
}

func cmdSign(stdout io.Writer, args []string) error {
	fs := newFlagSet("sign", "sign [uf2] [-key file] [-o out.uf2]")
	keyFlag := fs.String("key", defaultKeyPath, "signing key `file`")
	outFlag := fs.String("o", "", "output `file` (default <input>.signed.uf2)")
	lead, rest := popArg(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	keyPath, err := resolveKeyPath(*keyFlag)
	if err != nil {
		return err
	}
	priv, err := loadKeyFile(keyPath)
	if err != nil {
		return err
	}
	in, err := oneArg(fs, lead)
	if err != nil {
		return err
	}
	if in == "" {
		in, err = findUnsignedUF2()
		if err != nil {
			return err
		}
	}
	out := *outFlag
	if out == "" {
		out = signedName(in)
	}
	if out == in {
		return fmt.Errorf("refusing to sign %s onto itself; the unsigned input must survive", in)
	}
	u := newUI(stdout)
	u.printf("  key     %s %s\n", u.bold(keyPath), u.dim("fingerprint "+fingerprintHex(priv)[:16]))
	u.printf("  input   %s\n", in)
	return signImage(u, priv, in, out)
}
