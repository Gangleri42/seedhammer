package bip39

import (
	"errors"
	"io"
)

// ErrInvalidPrefix reports a prefix that is not exactly one word short
// of a mnemonic, or that contains a word outside the wordlist.
var ErrInvalidPrefix = errors.New("bip39: invalid mnemonic prefix")

// lastWordBits reports how the final word of a mnemonic of prefixLen+1
// words splits into checksum bits and free entropy bits. The two always
// sum to wordBits: an n-word mnemonic carries n/3 checksum bits, all of
// them in the last word.
func lastWordBits(prefixLen int) (checkBits, freeBits int, ok bool) {
	n := prefixLen + 1
	if n < 12 || n > 24 || n%3 != 0 {
		return 0, 0, false
	}
	checkBits = n / 3
	return checkBits, wordBits - checkBits, true
}

// maxNoProgress bounds the empty reads tolerated from an entropy source
// before it is called broken. One byte is wanted, so any healthy reader
// answers on the first pass.
const maxNoProgress = 64

// LastWords returns every word that completes prefix into a mnemonic
// with a valid checksum, ordered by ascending word index. prefix must be
// one word short of a mnemonic length; otherwise the result is nil.
//
// The free entropy bits of the final word are what distinguish the
// completions, so the count is 2^freeBits: 128 words for an 11-word
// prefix, 8 for a 23-word prefix. Element i is the completion whose free
// bits are i, and because the free bits are the high bits of the word,
// the slice comes out sorted.
func LastWords(prefix Mnemonic) []Word {
	checkBits, freeBits, ok := lastWordBits(len(prefix))
	if !ok || !prefix.wordsInRange() {
		return nil
	}
	m := make(Mnemonic, len(prefix)+1)
	copy(m, prefix)
	words := make([]Word, 1<<freeBits)
	for free := range words {
		words[free] = completeWord(m, Word(free), checkBits)
	}
	return words
}

// PickLastWord completes prefix with a uniformly random valid word,
// taking the free entropy bits from rng.
//
// The bits are masked, never reduced modulo a candidate count: the count
// of valid completions is 2^freeBits, so masking off the surplus bits of
// the drawn byte is already a uniform draw over the candidates. rng is a
// parameter so the device TRNG, crypto/rand and a recorded stream all
// drive the same code.
func PickLastWord(prefix Mnemonic, rng io.Reader) (Word, error) {
	checkBits, freeBits, ok := lastWordBits(len(prefix))
	if !ok || !prefix.wordsInRange() {
		return -1, ErrInvalidPrefix
	}
	// freeBits is wordBits-n/3, at most 7 for the shortest mnemonic, so
	// one byte always covers it.
	var buf [1]byte
	// Not io.ReadFull: its loop runs while a reader returns (0, nil), so
	// one that never errors and never delivers spins forever. The device
	// TRNG cannot do that, but Rand is exported and writable so an
	// out-of-tree Platform can install its own, and one waiting on a
	// browser crypto API would hang inside the frame loop instead of
	// reaching the caller's error screen.
	for tries := 0; ; tries++ {
		n, err := rng.Read(buf[:])
		if n > 0 {
			break
		}
		if err != nil {
			return -1, err
		}
		if tries >= maxNoProgress {
			return -1, io.ErrNoProgress
		}
	}
	free := Word(buf[0]) & (1<<freeBits - 1)
	m := make(Mnemonic, len(prefix)+1)
	copy(m, prefix)
	return completeWord(m, free, checkBits), nil
}

// completeWord returns the word that both carries free as its entropy
// bits and gives m a valid checksum. It overwrites the last word of m.
func completeWord(m Mnemonic, free Word, checkBits int) Word {
	// The low checkBits of the last word are the checksum, which
	// splitMnemonic discards, so any value works for the round trip.
	m[len(m)-1] = free << checkBits
	ent, _ := splitMnemonic(m)
	return ChecksumWord(ent)
}

func (m Mnemonic) wordsInRange() bool {
	for _, w := range m {
		if !w.valid() {
			return false
		}
	}
	return true
}
