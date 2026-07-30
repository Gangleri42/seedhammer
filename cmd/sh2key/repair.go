package main

import (
	"crypto/sha256"
	"runtime"
	"sync"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"seedhammer.com/bip39"
)

// The checksum alone cannot locate a wrong word: measured over
// single-word corruptions, roughly 190 substitutions still pass it.
// The public key fingerprint resolves the search uniquely, and that
// fingerprint is public: fused in OTP and engraved on plate 2.

type repairHit struct {
	pos      int        // 0-based word position
	from, to bip39.Word // from is -1 when the word was entered as '?'
}

// packMnemonic packs 24 words into 264 bits: 32 entropy bytes
// followed by the checksum byte. It is the hot path of the repair
// search, replacing the big.Int arithmetic in package bip39.
func packMnemonic(words []bip39.Word, out *[33]byte) {
	*out = [33]byte{}
	bit := 0
	for _, w := range words {
		for k := 10; k >= 0; k-- {
			if w>>k&1 != 0 {
				out[bit>>3] |= 0x80 >> (bit & 7)
			}
			bit++
		}
	}
}

// checkCandidate reports whether words form a checksum-valid mnemonic
// whose key matches the fingerprint prefix, reusing buf.
func checkCandidate(words []bip39.Word, want []byte, buf *[33]byte) bool {
	packMnemonic(words, buf)
	sum := sha256.Sum256(buf[:32])
	if sum[0] != buf[32] {
		return false
	}
	var s secp256k1.ModNScalar
	if overflow := s.SetByteSlice(buf[:32]); overflow || s.IsZero() {
		return false
	}
	priv := secp256k1.NewPrivateKey(&s)
	return fingerprintMatches(want, fingerprint(priv))
}

// searchOne tries every single-word substitution at the given
// positions and returns those matching the fingerprint. A position
// whose word is -1 (unknown) tries all 2048 candidates.
func searchOne(words []bip39.Word, positions []int, want []byte) [][]repairHit {
	var solutions [][]repairHit
	cand := make([]bip39.Word, 24)
	copy(cand, words)
	var buf [33]byte
	for _, pos := range positions {
		orig := words[pos]
		for w := bip39.Word(0); w < bip39.NumWords; w++ {
			if w == orig {
				continue
			}
			cand[pos] = w
			if checkCandidate(cand, want, &buf) {
				solutions = append(solutions, []repairHit{{pos: pos, from: orig, to: w}})
			}
		}
		cand[pos] = orig
	}
	return solutions
}

// searchTwo tries substitutions at every pair of positions, in
// parallel. With both positions free this is roughly 2048^2 checksum
// evaluations per pair and, across all 276 pairs, a few million key
// derivations: minutes of work, which is why it sits behind an
// explicit flag.
func searchTwo(words []bip39.Word, pairs [][2]int, want []byte, progress func(done, total int)) [][]repairHit {
	jobs := make(chan [2]int)
	var mu sync.Mutex
	var solutions [][]repairHit
	done := 0
	var wg sync.WaitGroup
	for range max(1, runtime.NumCPU()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cand := make([]bip39.Word, 24)
			var buf [33]byte
			for p := range jobs {
				copy(cand, words)
				i, j := p[0], p[1]
				var hits [][]repairHit
				for wi := bip39.Word(0); wi < bip39.NumWords; wi++ {
					if wi == words[i] {
						continue
					}
					cand[i] = wi
					for wj := bip39.Word(0); wj < bip39.NumWords; wj++ {
						if wj == words[j] {
							continue
						}
						cand[j] = wj
						if checkCandidate(cand, want, &buf) {
							hits = append(hits, []repairHit{
								{pos: i, from: words[i], to: wi},
								{pos: j, from: words[j], to: wj},
							})
						}
					}
					cand[j] = words[j]
				}
				mu.Lock()
				solutions = append(solutions, hits...)
				done++
				if progress != nil {
					progress(done, len(pairs))
				}
				mu.Unlock()
			}
		}()
	}
	for _, p := range pairs {
		jobs <- p
	}
	close(jobs)
	wg.Wait()
	return solutions
}

// allPairs enumerates the 276 position pairs of a 24-word mnemonic.
func allPairs() [][2]int {
	var out [][2]int
	for i := 0; i < 24; i++ {
		for j := i + 1; j < 24; j++ {
			out = append(out, [2]int{i, j})
		}
	}
	return out
}
