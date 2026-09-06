package shamir

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"testing"

	"seedhammer.com/bbqr"
)

var update = flag.Bool("update", false, "update testdata/vectors.json")

// shareVector is one split: the data and its file type, the scheme,
// the exact random stream consumed, the sealed type byte and payload,
// and the resulting share envelopes with their BBQr series. Feeding
// rand_hex through a bytes.Reader must reproduce the envelopes and
// parts exactly, in any implementation. type_byte and payload_hex
// are recorded because DEFLATE output is not unique: an independent
// generator starts from the sealed payload, and validates the
// compression by inflating it back to data_hex.
//
// testdata/check_vectors.py is that independent generator and
// receiver, written from SPEC.md and BBQr.md (with the ISO 18004
// alphanumeric capacity table), standard library only, sharing no
// code with this package:
//
//	python3 shamir/testdata/check_vectors.py
type shareVector struct {
	Name       string `json:"name"`
	FileType   string `json:"file_type"`
	DataHex    string `json:"data_hex"`
	K          int    `json:"k"`
	N          int    `json:"n"`
	RandHex    string `json:"rand_hex"`
	TypeByte   string `json:"type_byte"`
	PayloadHex string `json:"payload_hex"`
	Shares     []struct {
		EnvelopeHex string   `json:"envelope_hex"`
		Parts       []string `json:"parts"`
	} `json:"shares"`
}

// descriptorText is a 2-of-3 P2WSH descriptor over three real
// origin-tagged xpubs (the gui fixture cosigners), the U-typed input
// of the descriptor-3-of-5 vector. The keys are ascending by their
// 33-byte key data, the order a machine's plates follow, so the two
// descriptor vectors describe the plates a machine cuts.
const descriptorText = "wsh(sortedmulti(2," +
	"[c20d0c81/48h/0h/0h/2h]xpub6Dyvg74MADonsv1hPvMFKNtHPyvuSZ3mc8c7A6CLhD21ef6qSfbqgqWHFfjtV8H7Vz9YSKdeXq6n2NkvE5GapUmZJnsvn5p1pfQwV6aTmXd/<0;1>/*," +
	"[9a6a2580/48h/0h/0h/2h]xpub6EeqK2JLwngrHJEQ4X4iqrySZV9qU3TgwMgf6NStLZa37AfNiHTtTE9ji1F9YQDLArJMLy8sw3Q2samVj5VQQjaaUHr5z2Hz57NWHJCfh31/<0;1>/*," +
	"[2a77e0a6/48h/0h/0h/2h]xpub6F8WgTkiV8iDPFG1Kv4sNrcBNMMgKK4cjfxjdZWvR3kChfbt3L2dJF7xmCHBMGMmxjyzwgjdFkh9UN3623YpsmqN1KwZGR45Y3ANLQQX87u/<0;1>/*" +
	"))"

// descriptorCBORHex is the crypto-output CBOR (urtypes.EncodeDescriptor)
// of descriptorText, the C-typed input of the descriptor-cbor-2-of-3
// vector: 320 bytes, three hdkeys carrying key data, chain code,
// origin path and parent fingerprint, no children. Generated once so
// that this package does not import bip380 or urtypes;
// cmd/bbqr's TestShamirVectorInputsValid checks it against both.
const descriptorCBORHex = "d90191d90197a201020283" +
	"d9012fa40358210252e3c39bc033e7fcaa788e336aa7200d70b19e8bdb035156760d2922f067583004582003e41fe2807dd4361e63f1741a4d7c6d82cbea9a924cf22f79d23d8b873f332a06d90130a201881830f500f500f502f5021ac20d0c81081a3d0a2c0c" +
	"d9012fa403582102f2d0286e4dcbd23ab09f4ad78d6531946119aa3d99720f626647fc29fdbdd2070458200dfb8b816f34448df8719a3d33aa63fb56b36a81dcc2e7320bf83faec9c19d6406d90130a201881830f500f500f502f5021a9a6a2580081a984e30e7" +
	"d9012fa403582103b3f7e0119b2843cd8f4c56dc1fcb0ac51bd63a652f507ba14581b79a72bfc8f3045820c4bccf13ee362a8ef437898087069870fd62ea227e77714a1d756383f31be9a106d90130a201881830f500f500f502f5021a2a77e0a6081ad93b5676"

type shareVectorFile struct {
	Vectors []shareVector `json:"vectors"`
}

// specs are the vector inputs, regenerated with -update. The random
// streams come from math/rand at generation time and are frozen into
// the vectors; their provenance does not matter, only that they are
// recorded.
func specs(t *testing.T) []struct {
	name string
	typ  byte
	data []byte
	k, n int
	seed int64
} {
	return []struct {
		name string
		typ  byte
		data []byte
		k, n int
		seed int64
	}{
		{"mnemonic-2-of-4", bbqr.TypeText, []byte("abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"), 2, 4, 101},
		{"descriptor-3-of-5", bbqr.TypeText, []byte(descriptorText), 3, 5, 102},
		{"binary-2-of-2", bbqr.TypeBinary, []byte{0x42}, 2, 2, 103},
		{"compressible-2-of-3", bbqr.TypeText, bytes.Repeat([]byte("seedhammer "), 100), 2, 3, 104},
		{"random-16-of-16", bbqr.TypeBinary, rngBytes(t, 64), 16, 16, 105},
		{"descriptor-cbor-2-of-3", bbqr.TypeCBOR, mustHex(t, descriptorCBORHex), 2, 3, 106},
		// 2709-byte envelopes exceed one QR at any version, so this
		// vector pins the part sizing: two parts per share.
		{"random-2700-2-of-2", bbqr.TypeBinary, rngBytes(t, 2700), 2, 2, 107},
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// sealedOf recombines the sealed content from the first k envelopes
// with the receiver's own arithmetic, so the vectors record what
// SplitData sealed, with no re-derivation from the data.
func sealedOf(t *testing.T, envelopes [][]byte, k int) []byte {
	t.Helper()
	var points [][]byte
	for _, env := range envelopes[:k] {
		sh, err := ParseShare(env)
		if err != nil {
			t.Fatal(err)
		}
		points = append(points, append([]byte{byte(sh.Index)}, sh.data...))
	}
	sealed, err := Combine(points)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

// sealedDigest restates section 2 of SPEC.md: SHA-256(k ‖ T ‖ payload)[0:4].
func sealedDigest(k int, typ byte, payload []byte) []byte {
	h := sha256.New()
	h.Write([]byte{byte(k), typ})
	h.Write(payload)
	return h.Sum(nil)[:4]
}

// countingReader records how many bytes SplitData consumed.
type countingReader struct {
	r   *rand.Rand
	buf bytes.Buffer
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.buf.Write(p[:n])
	return n, err
}

func TestVectors(t *testing.T) {
	if *update {
		var vf shareVectorFile
		for _, spec := range specs(t) {
			r := &countingReader{r: rand.New(rand.NewSource(spec.seed))}
			series, err := SplitData(spec.typ, spec.data, spec.k, spec.n, r)
			if err != nil {
				t.Fatal(err)
			}
			v := shareVector{
				Name:     spec.name,
				FileType: string(spec.typ),
				DataHex:  hex.EncodeToString(spec.data),
				K:        spec.k,
				N:        spec.n,
				RandHex:  hex.EncodeToString(r.buf.Bytes()),
			}
			var envelopes [][]byte
			for _, s := range series {
				_, payload, err := bbqr.Join(s.Parts)
				if err != nil {
					t.Fatal(err)
				}
				envelopes = append(envelopes, payload)
				v.Shares = append(v.Shares, struct {
					EnvelopeHex string   `json:"envelope_hex"`
					Parts       []string `json:"parts"`
				}{hex.EncodeToString(payload), s.Parts})
			}
			sealed := sealedOf(t, envelopes, spec.k)
			v.TypeByte = fmt.Sprintf("%02x", sealed[0])
			v.PayloadHex = hex.EncodeToString(sealed[1 : len(sealed)-4])
			vf.Vectors = append(vf.Vectors, v)
		}
		out, err := json.MarshalIndent(vf, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile("testdata/vectors.json", append(out, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Skip("testdata/vectors.json not generated yet; run with -update")
	}
	var vf shareVectorFile
	if err := json.Unmarshal(raw, &vf); err != nil {
		t.Fatal(err)
	}
	for _, v := range vf.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			data, err := hex.DecodeString(v.DataHex)
			if err != nil {
				t.Fatal(err)
			}
			randStream, err := hex.DecodeString(v.RandHex)
			if err != nil {
				t.Fatal(err)
			}

			// Exact reproduction from the recorded rand stream.
			series, err := SplitData(v.FileType[0], data, v.K, v.N, bytes.NewReader(randStream))
			if err != nil {
				t.Fatal(err)
			}
			if len(series) != len(v.Shares) {
				t.Fatalf("got %d shares, want %d", len(series), len(v.Shares))
			}
			var payloads [][]byte
			for i, s := range series {
				want, err := hex.DecodeString(v.Shares[i].EnvelopeHex)
				if err != nil {
					t.Fatal(err)
				}
				typ, payload, err := bbqr.Join(s.Parts)
				if err != nil {
					t.Fatal(err)
				}
				if typ != bbqr.TypeShamir {
					t.Fatalf("share %d series has file type %c", i, typ)
				}
				if !bytes.Equal(payload, want) {
					t.Fatalf("share %d envelope mismatch", i)
				}
				if !equalStrings(s.Parts, v.Shares[i].Parts) {
					t.Fatalf("share %d BBQr parts mismatch", i)
				}
				sh, err := ParseShare(payload)
				if err != nil {
					t.Fatal(err)
				}
				if sh.Threshold != v.K || sh.Index != i+1 {
					t.Fatalf("share %d: threshold %d index %d", i, sh.Threshold, sh.Index)
				}
				if i > 0 && sh.Tag != mustParseTag(t, v.Shares[0].EnvelopeHex) {
					t.Fatalf("share %d tag differs from share 0", i)
				}
				payloads = append(payloads, payload)
			}

			// The sealed content SplitData produced: type byte, payload
			// and the digest of section 2 over both.
			sealed := sealedOf(t, payloads, v.K)
			typ, payload := sealed[0], sealed[1:len(sealed)-4]
			if got := fmt.Sprintf("%02x", typ); got != v.TypeByte {
				t.Fatalf("type byte %s, want %s", got, v.TypeByte)
			}
			if typ&0x7f != v.FileType[0] {
				t.Fatalf("type byte %02x does not carry file type %c", typ, v.FileType[0])
			}
			if got := hex.EncodeToString(payload); got != v.PayloadHex {
				t.Fatalf("payload mismatch")
			}
			if !bytes.Equal(sealed[len(sealed)-4:], sealedDigest(v.K, typ, payload)) {
				t.Fatalf("digest mismatch")
			}
			if typ&0x80 != 0 {
				if len(payload) >= len(data) {
					t.Fatalf("compressed payload of %d bytes does not shrink %d bytes of data", len(payload), len(data))
				}
			} else if !bytes.Equal(payload, data) {
				t.Fatalf("uncompressed payload differs from data")
			}

			// One-shot recovery from the envelopes.
			rec, err := RecoverData(payloads)
			if err != nil {
				t.Fatal(err)
			}
			if rec.FileType != v.FileType[0] || !bytes.Equal(rec.Data, data) {
				t.Fatal("RecoverData mismatch")
			}

			// Recovery from every threshold-sized subset, through the
			// full BBQr decode of each share.
			for mask := 0; mask < 1<<v.N; mask++ {
				var s Set
				for i := 0; i < v.N; i++ {
					if mask&(1<<i) == 0 {
						continue
					}
					_, payload, err := bbqr.Join(v.Shares[i].Parts)
					if err != nil {
						t.Fatal(err)
					}
					if err := s.Add(payload); err != nil {
						t.Fatal(err)
					}
				}
				if bits(mask) < v.K {
					if s.Complete() {
						t.Fatalf("subset %b complete below threshold", mask)
					}
					continue
				}
				rec, err := s.Recover()
				if err != nil || !bytes.Equal(rec.Data, data) {
					t.Fatalf("subset %b: %v", mask, err)
				}
			}
		})
	}
}

func mustParseTag(t *testing.T, envelopeHex string) uint16 {
	t.Helper()
	payload, err := hex.DecodeString(envelopeHex)
	if err != nil {
		t.Fatal(err)
	}
	sh, err := ParseShare(payload)
	if err != nil {
		t.Fatal(err)
	}
	return sh.Tag
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func bits(mask int) int {
	n := 0
	for mask != 0 {
		n += mask & 1
		mask >>= 1
	}
	return n
}
