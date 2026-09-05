package shamir

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"math/rand"
	"os"
	"testing"

	"seedhammer.com/bbqr"
)

var update = flag.Bool("update", false, "update testdata/vectors.json")

// shareVector is one split: the data and its file type, the scheme,
// the exact random stream consumed, and the resulting share envelopes
// with their BBQr series. Feeding rand_hex through a bytes.Reader must
// reproduce the envelopes and parts exactly, in any implementation.
type shareVector struct {
	Name     string `json:"name"`
	FileType string `json:"file_type"`
	DataHex  string `json:"data_hex"`
	K        int    `json:"k"`
	N        int    `json:"n"`
	RandHex  string `json:"rand_hex"`
	Shares   []struct {
		EnvelopeHex string   `json:"envelope_hex"`
		Parts       []string `json:"parts"`
	} `json:"shares"`
}

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
		{"descriptor-3-of-5", bbqr.TypeText, []byte("wsh(sortedmulti(2,xpub661MyMwAqRbcFtXgS5sYJABqqG9YLmC4Q1Rdap9gSE8NqtwybGhePY2gZ29ESFjqJoCu1Rupje8YtGqsefD265TMg7usUDFdp6W1EGMcet8,xpub661MyMwAqRbcG8ZahFF5MEb6vFprAZECfcKZqczRkdLZvBNEQ5x2NQcBzHUaNn5mBAhGHbQNTGg8VgNqXCaCLfR9Yj2FhZCCBSvzkgbDx2/0/*))"), 3, 5, 102},
		{"binary-2-of-2", bbqr.TypeBinary, []byte{0x42}, 2, 2, 103},
		{"compressible-2-of-3", bbqr.TypeText, bytes.Repeat([]byte("seedhammer "), 100), 2, 3, 104},
		{"random-16-of-16", bbqr.TypeBinary, rngBytes(t, 64), 16, 16, 105},
	}
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
			for _, s := range series {
				_, payload, err := bbqr.Join(s.Parts)
				if err != nil {
					t.Fatal(err)
				}
				v.Shares = append(v.Shares, struct {
					EnvelopeHex string   `json:"envelope_hex"`
					Parts       []string `json:"parts"`
				}{hex.EncodeToString(payload), s.Parts})
			}
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
