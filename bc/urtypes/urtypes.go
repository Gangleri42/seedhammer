// Package urtypes implements decoders for UR types specified in [BCR-2020-006].
//
// [BCR-2020-006]: https://github.com/BlockchainCommons/Research/blob/master/papers/bcr-2020-006-urtypes.md
package urtypes

import (
	"errors"
	"fmt"
	"math"
	"reflect"

	"github.com/btcsuite/btcd/btcutil/v2/hdkeychain"
	"github.com/btcsuite/btcd/chaincfg/v2"
	"github.com/fxamacker/cbor/v2"
	"seedhammer.com/bip32"
	"seedhammer.com/bip380"
)

// EncodeDescriptor encodes d in the format described by
// [BCR-2020-010].
//
// [BCR-2020-010]: https://github.com/BlockchainCommons/Research/blob/master/papers/bcr-2020-010-output-desc.md
func EncodeDescriptor(o *bip380.Descriptor) []byte {
	var v any
	switch o.Type {
	case bip380.SortedMulti:
		m := struct {
			Threshold int        `cbor:"1,keyasint,omitempty"`
			Keys      []cbor.Tag `cbor:"2,keyasint"`
		}{
			Threshold: o.Threshold,
		}
		for _, k := range o.Keys {
			m.Keys = append(m.Keys, cbor.Tag{
				Number:  tagHDKey,
				Content: hdkeyFor(k),
			})
		}
		v = cbor.Tag{
			Number:  uint64(tagSortedMulti),
			Content: m,
		}
	case bip380.Singlesig:
		v = cbor.Tag{
			Number:  tagHDKey,
			Content: hdkeyFor(o.Keys[0]),
		}
	default:
		panic("invalid type")
	}
	var tags []uint64
	switch o.Script {
	case bip380.P2SH:
		tags = []uint64{tagSH}
	case bip380.P2SH_P2WSH:
		tags = []uint64{tagSH, tagWSH}
	case bip380.P2SH_P2WPKH:
		tags = []uint64{tagSH, tagWPKH}
	case bip380.P2PKH:
		tags = []uint64{tagP2PKH}
	case bip380.P2WSH:
		tags = []uint64{tagWSH}
	case bip380.P2WPKH:
		tags = []uint64{tagWPKH}
	case bip380.P2TR:
		tags = []uint64{tagTR}
	default:
		panic("invalid type")
	}
	for i := len(tags) - 1; i >= 0; i-- {
		v = cbor.Tag{
			Number:  tags[i],
			Content: v,
		}
	}
	enc, err := encMode.Marshal(v)
	if err != nil {
		panic(err)
	}
	return enc
}

// Encode the key in the format described by [BCR-2020-007].
//
// [BCR-2020-007]: https://github.com/BlockchainCommons/Research/blob/master/papers/bcr-2020-007-hdkey.md
func hdkeyFor(k bip380.Key) hdKey {
	// A key whose children hold a receive/change pair encodes with no
	// children at all. BIP389's <0;1> means alternatives, and
	// [BCR-2020-007] predates it: its child-index-range means a
	// contiguous span, so the pair has no faithful encoding here, and
	// no importer maps a range back — Sparrow ignores the components,
	// BlueWallet renders them as a stray */* tail on the vault key's
	// derivation path. Multisig exchange runs on origin-only keys,
	// the childless shape the split plates have always carried and
	// wallets rebuild the standard branches from.
	var children []any
	if !hasMultipath(k.Children) {
		for _, c := range k.Children {
			switch c.Type {
			case bip380.ChildDerivation:
				children = append(children, c.Index, c.Hardened)
			case bip380.WildcardDerivation:
				children = append(children, []any{}, c.Hardened)
			}
		}
	}
	network := mainnet
	if k.Network == &chaincfg.TestNet3Params {
		network = testnet
	}
	return hdKey{
		UseInfo: useInfo{
			Network: network,
		},
		KeyData:           k.KeyData,
		ChainCode:         k.ChainCode,
		ParentFingerprint: k.ParentFingerprint,
		Origin: keyPath{
			Fingerprint: k.MasterFingerprint,
			// No need to store the depth when the derivation path is
			// present, and the goldens pin the emitted bytes: a
			// non-zero depth here would change every engraved UR QR.
			Depth:      0,
			Components: pathComponents(k.DerivationPath),
		},
		Children: keyPath{
			Components: children,
		},
	}
}

func hasMultipath(children []bip380.Derivation) bool {
	for _, c := range children {
		if c.Type == bip380.RangeDerivation {
			return true
		}
	}
	return false
}

func pathComponents(p bip32.Path) []any {
	var comp []any
	for _, c := range p {
		hard := c >= hdkeychain.HardenedKeyStart
		if hard {
			c -= hdkeychain.HardenedKeyStart
		}
		comp = append(comp, c, hard)
	}
	return comp
}

// EncodeKey encodes k in the format described by [BCR-2020-007].
//
// [BCR-2020-007]: https://github.com/BlockchainCommons/Research/blob/master/papers/bcr-2020-007-hdkey.md
func EncodeKey(k bip380.Key) []byte {
	b, err := encMode.Marshal(hdkeyFor(k))
	if err != nil {
		// Always valid by construction.
		panic(err)
	}
	return b
}

type seed struct {
	Payload []byte `cbor:"1,keyasint"`
}

type multi struct {
	Threshold int               `cbor:"1,keyasint"`
	Keys      []cbor.RawMessage `cbor:"2,keyasint"`
}

// account is the CBOR representation of a crypto-account.
type account struct {
	MasterFingerprint uint32            `cbor:"1,keyasint,omitempty"`
	OutputDescriptors []cbor.RawMessage `cbor:"2,keyasint,omitempty"`
}

// hdKey is the CBOR representation of a crypto-hdkey.
type hdKey struct {
	IsMaster          bool    `cbor:"1,keyasint,omitempty"`
	IsPrivate         bool    `cbor:"2,keyasint,omitempty"`
	KeyData           []byte  `cbor:"3,keyasint"`
	ChainCode         []byte  `cbor:"4,keyasint,omitempty"`
	UseInfo           useInfo `cbor:"5,keyasint,omitempty"`
	Origin            keyPath `cbor:"6,keyasint,omitempty"`
	Children          keyPath `cbor:"7,keyasint,omitempty"`
	ParentFingerprint uint32  `cbor:"8,keyasint,omitempty"`
}

type useInfo struct {
	Type    uint32 `cbor:"1,keyasint,omitempty"`
	Network int    `cbor:"2,keyasint,omitempty"`
}

type keyPath struct {
	Components  []any  `cbor:"1,keyasint,omitempty"`
	Fingerprint uint32 `cbor:"2,keyasint,omitempty"`
	Depth       uint8  `cbor:"3,keyasint,omitempty"`
}

const (
	tagHDKey   = 303
	tagKeyPath = 304
	tagUseInfo = 305

	tagSH    = 400
	tagWSH   = 401
	tagP2PKH = 403
	tagWPKH  = 404
	tagTR    = 409

	tagMulti       = 406
	tagSortedMulti = 407
)

var encMode cbor.EncMode
var decMode cbor.DecMode

func init() {
	tags := cbor.NewTagSet()
	if err := tags.Add(cbor.TagOptions{DecTag: cbor.DecTagOptional}, reflect.TypeFor[hdKey](), tagHDKey); err != nil {
		panic(err)
	}
	if err := tags.Add(cbor.TagOptions{DecTag: cbor.DecTagOptional, EncTag: cbor.EncTagRequired}, reflect.TypeFor[keyPath](), tagKeyPath); err != nil {
		panic(err)
	}
	if err := tags.Add(cbor.TagOptions{DecTag: cbor.DecTagOptional, EncTag: cbor.EncTagRequired}, reflect.TypeFor[useInfo](), tagUseInfo); err != nil {
		panic(err)
	}
	em, err := cbor.CoreDetEncOptions().EncModeWithTags(tags)
	if err != nil {
		panic(err)
	}
	encMode = em
	dm, err := cbor.DecOptions{}.DecModeWithTags(tags)
	if err != nil {
		panic(err)
	}
	decMode = dm
}

func Parse(typ string, enc []byte) (any, error) {
	switch typ {
	case "crypto-seed":
		var s seed
		if err := decMode.Unmarshal(enc, &s); err != nil {
			return nil, fmt.Errorf("ur: %s: %w", typ, err)
		}
		return s, nil
	case "crypto-account":
		// Limited support for crypto-account: unpack a single output
		// descriptor and treat it like crypto-output.
		var acc account
		if err := decMode.Unmarshal(enc, &acc); err != nil {
			return nil, fmt.Errorf("ur: crypto-account: %w", err)
		}
		if len(acc.OutputDescriptors) != 1 {
			return nil, fmt.Errorf("ur: crypto-account: zero or multiple crypto-outputs")
		}
		enc = acc.OutputDescriptors[0]
		desc, err := parseOutputDescriptor(decMode, acc.OutputDescriptors[0])
		if err != nil {
			return nil, fmt.Errorf("ur: crypto-account: %w", err)
		}
		if !desc.Script.Singlesig() {
			return nil, fmt.Errorf("ur: crypto-account: invalid single-sig script: %s", desc.Script)
		}
		return desc, nil
	case "crypto-output":
		desc, err := parseOutputDescriptor(decMode, enc)
		if err != nil {
			return nil, fmt.Errorf("ur: crypto-output: %w", err)
		}
		return desc, nil
	case "crypto-hdkey":
		key, err := parseHDKey(enc)
		if err != nil {
			return nil, fmt.Errorf("ur: crypto-hdkey: %w", err)
		}
		return key, nil
	case "bytes":
		var content []byte
		if err := decMode.Unmarshal(enc, &content); err != nil {
			return nil, fmt.Errorf("ur: bytes decoding failed: %w", err)
		}
		return content, nil
	default:
		return nil, fmt.Errorf("ur: unknown type %q", typ)
	}
}

const mainnet = 0
const testnet = 1

func parseHDKey(enc []byte) (bip380.Key, error) {
	var k hdKey
	if err := decMode.Unmarshal(enc, &k); err != nil {
		return bip380.Key{}, fmt.Errorf("ur: crypto-hdkey decoding failed: %w", err)
	}
	const cointypeBTC = 0
	if k.UseInfo.Type != cointypeBTC {
		return bip380.Key{}, fmt.Errorf("ur: crypto-hdkey key has unsupported coin type %d", k.UseInfo.Type)
	}
	children, err := parseKeypath(k.Children.Components)
	if err != nil {
		return bip380.Key{}, err
	}
	for _, c := range children {
		// The same line the text parser holds: a hardened child
		// cannot be derived from a public key, and deriving its
		// unhardened sibling would be a different wallet.
		if c.Hardened {
			return bip380.Key{}, errors.New("ur: hardened child derivation cannot be derived from a public key")
		}
	}
	if len(k.KeyData) != 33 {
		return bip380.Key{}, fmt.Errorf("ur: crypto-hdkey key is %d bytes, expected 33", len(k.KeyData))
	}
	if len(k.ChainCode) != 32 {
		return bip380.Key{}, fmt.Errorf("ur: crypto-hdkey chain code is %d bytes, expected 32", len(k.ChainCode))
	}
	var net *chaincfg.Params
	switch n := k.UseInfo.Network; n {
	case mainnet:
		net = &chaincfg.MainNetParams
	case testnet:
		net = &chaincfg.TestNet3Params
	default:
		return bip380.Key{}, fmt.Errorf("ur: unknown coininfo network %d", n)
	}
	comps, err := parseKeypath(k.Origin.Components)
	if err != nil {
		return bip380.Key{}, err
	}
	var devPath bip32.Path
	for _, d := range comps {
		if d.Type != bip380.ChildDerivation {
			return bip380.Key{}, fmt.Errorf("ur: wildcards or ranges not allowed in origin path")
		}
		idx := d.Index
		if d.Hardened {
			idx += hdkeychain.HardenedKeyStart
		}
		devPath = append(devPath, idx)
	}
	// BIP32 serializes depth as one byte, so no extended key at a
	// deeper origin can exist; the text parser bounds it too.
	if len(devPath) > 255 {
		return bip380.Key{}, fmt.Errorf("ur: origin path has %d elements; BIP32 depth is one byte", len(devPath))
	}
	depth := k.Origin.Depth
	if depth != 0 && int(depth) != len(devPath) {
		return bip380.Key{}, fmt.Errorf("ur: origin depth is %d but expected %d", depth, len(devPath))
	}
	return bip380.Key{
		Network:           net,
		MasterFingerprint: k.Origin.Fingerprint,
		DerivationPath:    devPath,
		Children:          children,
		KeyData:           k.KeyData,
		ChainCode:         k.ChainCode,
		ParentFingerprint: k.ParentFingerprint,
	}, nil
}

func parseOutputDescriptor(mode cbor.DecMode, enc []byte) (*bip380.Descriptor, error) {
	var tags []uint64
	for {
		var raw cbor.RawTag
		if err := mode.Unmarshal(enc, &raw); err != nil {
			break
		}
		tags = append(tags, raw.Number)
		enc = raw.Content
	}
	if len(tags) == 0 {
		return nil, errors.New("ur: missing descriptor tag")
	}
	desc := new(bip380.Descriptor)
	first := tags[0]
	tags = tags[1:]
	switch first {
	case tagSH:
		desc.Script = bip380.P2SH
		if len(tags) == 0 {
			break
		}
		switch tags[0] {
		case tagWSH:
			desc.Script = bip380.P2SH_P2WSH
			tags = tags[1:]
		case tagWPKH:
			desc.Script = bip380.P2SH_P2WPKH
			tags = tags[1:]
		}
	case tagP2PKH:
		desc.Script = bip380.P2PKH
	case tagTR:
		desc.Script = bip380.P2TR
	case tagWSH:
		desc.Script = bip380.P2WSH
	case tagWPKH:
		desc.Script = bip380.P2WPKH
	default:
		return nil, fmt.Errorf("ur: unknown script type tag: %d", first)
	}
	if len(tags) == 0 {
		return nil, errors.New("ur: missing descriptor script tag")
	}
	funcNumber := tags[0]
	tags = tags[1:]
	if len(tags) > 0 {
		return nil, errors.New("ur: extra tags")
	}
	switch funcNumber {
	case tagHDKey: // singlesig
		desc.Type = bip380.Singlesig
		k, err := parseHDKey(enc)
		if err != nil {
			return nil, err
		}
		desc.Threshold = 1
		desc.Keys = append(desc.Keys, k)
	case tagSortedMulti:
		desc.Type = bip380.SortedMulti
		var m multi
		if err := mode.Unmarshal(enc, &m); err != nil {
			return nil, err
		}
		desc.Threshold = m.Threshold
		for _, k := range m.Keys {
			keyDesc, err := parseHDKey([]byte(k))
			if err != nil {
				return nil, err
			}
			desc.Keys = append(desc.Keys, keyDesc)
		}
	default:
		return desc, fmt.Errorf("unknown script function tag: %d", funcNumber)
	}
	// The same quorum arithmetic the text parser enforces.
	if err := desc.Validate(); err != nil {
		return nil, fmt.Errorf("ur: %w", err)
	}
	return desc, nil
}

func parseKeypath(comp []any) ([]bip380.Derivation, error) {
	if len(comp)%2 == 1 {
		return nil, errors.New("odd number of components")
	}
	var path []bip380.Derivation
	for i := 0; i < len(comp); i += 2 {
		d, h := comp[i], comp[i+1]
		var deriv bip380.Derivation
		switch d := d.(type) {
		case uint64:
			if d > math.MaxUint32 {
				return nil, errors.New("child index out of range")
			}
			deriv = bip380.Derivation{
				Type:  bip380.ChildDerivation,
				Index: uint32(d),
			}
		case []any:
			switch len(d) {
			case 0:
				deriv = bip380.Derivation{
					Type: bip380.WildcardDerivation,
				}
			case 2:
				start, ok1 := d[0].(uint64)
				end, ok2 := d[1].(uint64)
				if !ok1 || !ok2 || start > math.MaxUint32 || end > math.MaxUint32 {
					return nil, errors.New("invalid range derivation")
				}
				deriv = bip380.Derivation{
					Type:  bip380.RangeDerivation,
					Index: uint32(start),
					End:   uint32(end),
				}
			default:
				return nil, errors.New("invalid wildcard derivation")
			}
		default:
			return nil, errors.New("unknown component type")
		}
		hardened, ok := h.(bool)
		if !ok {
			return nil, errors.New("invalid hardened flag")
		}
		deriv.Hardened = hardened
		path = append(path, deriv)
	}
	return path, nil
}
