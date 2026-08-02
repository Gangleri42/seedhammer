package bip39

import (
	"bytes"
	crand "crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var (
	soakTime = flag.Duration("bip39.soak", 500*time.Millisecond, "wall clock per distribution experiment; four experiments run")
	soakMax  = flag.Uint64("bip39.draws", 0, "stop each experiment after this many draws (0: run until -bip39.soak elapses)")
	soakRNG  = flag.String("bip39.rng", "crypto", `entropy under test: "crypto", "prng:<seed>", or "file:<path>" for a captured device TRNG stream`)
	soakSeed = flag.Int64("bip39.seed", 0, "seed for the prefix schedule; 0 draws one and logs it")
	// A p-value this small is a bias, not a bad day: with four
	// experiments and a handful of statistics each, the run-level false
	// alarm rate stays under 1e-4.
	soakAlpha = flag.Float64("bip39.alpha", 1e-6, "p-value below which a statistic fails the soak")
	// Each trial brute-forces all 2048 words through Mnemonic.Valid, so
	// this dominates the package test time. 300 catches any regression
	// at once; the 3000 that proved the enumeration is a flag away.
	enumTrials = flag.Int("bip39.trials", 300, "random prefixes per length in the enumeration cross-check")
)

func envDuration(name string, def time.Duration) time.Duration {
	if v := os.Getenv(name); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

// randomPrefix fills p with words drawn from rng. Masking 11 bits off a
// uniform 32-bit value is itself uniform over the wordlist.
func randomPrefix(p Mnemonic, rng *rand.ChaCha8) {
	for i := range p {
		p[i] = Word(rng.Uint64() & (1<<wordBits - 1))
	}
}

func seededChaCha(parts ...uint64) *rand.ChaCha8 {
	var seed [32]byte
	for i, v := range parts {
		if i*8+8 > len(seed) {
			break
		}
		for b := range 8 {
			seed[i*8+b] = byte(v >> (8 * b))
		}
	}
	return rand.NewChaCha8(seed)
}

// TestLastWordsEnumeration is the gate on every statistic below: it
// brute-forces all 2048 words through the package's own checksum
// validator and demands LastWords return exactly the words that pass.
func TestLastWordsEnumeration(t *testing.T) {
	rng := seededChaCha(0x5EEDBEEF12345678, 1)
	trials := *enumTrials
	if testing.Short() {
		trials = 100
	}
	for _, prefixLen := range []int{11, 14, 17, 20, 23} {
		wantCount := 1 << (wordBits - (prefixLen+1)/3)
		t.Run(fmt.Sprintf("%dwords", prefixLen+1), func(t *testing.T) {
			prefix := make(Mnemonic, prefixLen)
			full := make(Mnemonic, prefixLen+1)
			var counts []int
			for trial := range trials {
				randomPrefix(prefix, rng)
				got := LastWords(prefix)
				// Brute force: every word in the list, checked by
				// Mnemonic.Valid, which is what the firmware trusts.
				var want []Word
				copy(full, prefix)
				for w := Word(0); w < NumWords; w++ {
					full[len(full)-1] = w
					if full.Valid() {
						want = append(want, w)
					}
				}
				if !slices.Equal(got, want) {
					t.Fatalf("trial %d, prefix %v:\nLastWords = %v (%d)\nbrute force = %v (%d)",
						trial, prefix, got, len(got), want, len(want))
				}
				if len(got) != wantCount {
					t.Fatalf("trial %d: %d valid completions, want %d", trial, len(got), wantCount)
				}
				if !slices.IsSorted(got) {
					t.Fatalf("trial %d: LastWords not sorted: %v", trial, got)
				}
				// The candidate index must be recoverable from the word:
				// the soak relies on it to bin draws in O(1).
				checkBits := (prefixLen + 1) / 3
				for i, w := range got {
					if int(w>>checkBits) != i {
						t.Fatalf("trial %d: candidate %d is word %d, whose free bits are %d", trial, i, w, w>>checkBits)
					}
				}
				counts = append(counts, len(got))
			}
			lo, hi := slices.Min(counts), slices.Max(counts)
			t.Logf("%d prefixes: valid completions per prefix min=%d max=%d want=%d", trials, lo, hi, wantCount)
		})
	}
}

// TestLastWordsCoversSpecVectors anchors the enumeration to BIP39
// itself rather than to this package: every official test vector's real
// last word has to appear among the completions of its own prefix.
func TestLastWordsCoversSpecVectors(t *testing.T) {
	checked := 0
	for _, v := range testVectors {
		m, err := ParseMnemonic(v.mnemonic)
		if err != nil {
			t.Fatal(err)
		}
		got := LastWords(m[:len(m)-1])
		if !slices.Contains(got, m[len(m)-1]) {
			t.Errorf("%q: last word %q missing from the %d completions", v.mnemonic, LabelFor(m[len(m)-1]), len(got))
		}
		checked++
	}
	t.Logf("%d BIP39 spec vectors, every last word enumerated", checked)
}

// TestPickLastWordExhaustsByteRange proves the mask is unbiased: feeding
// every one of the 256 byte values yields each candidate the same number
// of times, which a modulo reduction of a non-power-of-two range could
// not do.
func TestPickLastWordExhaustsByteRange(t *testing.T) {
	rng := seededChaCha(0x5EEDBEEF12345678, 2)
	for _, prefixLen := range []int{11, 14, 17, 20, 23} {
		prefix := make(Mnemonic, prefixLen)
		randomPrefix(prefix, rng)
		cands := LastWords(prefix)
		counts := make([]int, len(cands))
		for b := range 256 {
			w, err := PickLastWord(prefix, bytes.NewReader([]byte{byte(b)}))
			if err != nil {
				t.Fatalf("byte %d: %v", b, err)
			}
			i := slices.Index(cands, w)
			if i < 0 {
				t.Fatalf("byte %d: word %d is not a valid completion", b, w)
			}
			counts[i]++
		}
		want := 256 / len(cands)
		for i, c := range counts {
			if c != want {
				t.Errorf("%d words: candidate %d drawn %d times over the byte range, want %d", prefixLen+1, i, c, want)
			}
		}
	}
}

func TestPickLastWordErrors(t *testing.T) {
	good := make(Mnemonic, 11)
	if _, err := PickLastWord(good, bytes.NewReader(nil)); !errors.Is(err, io.EOF) {
		t.Errorf("empty rng: err = %v, want io.EOF", err)
	}
	for _, bad := range []Mnemonic{
		make(Mnemonic, 10), make(Mnemonic, 12), make(Mnemonic, 24), {}, {Word(2048), 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
	} {
		if _, err := PickLastWord(bad, crand.Reader); !errors.Is(err, ErrInvalidPrefix) {
			t.Errorf("prefix of %d words: err = %v, want ErrInvalidPrefix", len(bad), err)
		}
		if got := LastWords(bad); got != nil {
			t.Errorf("LastWords(%d words) = %v, want nil", len(bad), got)
		}
	}
}

// entropySource hands each worker a reader over the entropy under test.
// A "file:" source is one shared stream, so a captured device sequence is
// consumed exactly once and in order.
type entropySource struct {
	spec string
	// reset runs before each experiment, so a finite capture can drive
	// every one of them instead of draining into the first.
	reset  func() error
	reader func(worker int) io.Reader
	closer func()
}

type lockedReader struct {
	mu sync.Mutex
	r  io.Reader
}

func (l *lockedReader) Read(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return io.ReadFull(l.r, p)
}

func newEntropySource(spec string) (*entropySource, error) {
	noop := func() error { return nil }
	switch {
	case spec == "crypto":
		return &entropySource{
			spec: spec, reset: noop,
			reader: func(int) io.Reader { return crand.Reader },
			closer: func() {},
		}, nil
	case strings.HasPrefix(spec, "prng:"):
		seed, err := strconv.ParseInt(strings.TrimPrefix(spec, "prng:"), 0, 64)
		if err != nil {
			return nil, err
		}
		return &entropySource{
			spec: spec, reset: noop,
			reader: func(w int) io.Reader { return seededChaCha(uint64(seed), uint64(w), 0xE47_0B7E) },
			closer: func() {},
		}, nil
	case strings.HasPrefix(spec, "file:"):
		f, err := os.Open(strings.TrimPrefix(spec, "file:"))
		if err != nil {
			return nil, err
		}
		shared := &lockedReader{r: f}
		return &entropySource{
			spec: spec,
			reset: func() error {
				shared.mu.Lock()
				defer shared.mu.Unlock()
				_, err := f.Seek(0, io.SeekStart)
				return err
			},
			reader: func(int) io.Reader { return shared },
			closer: func() { f.Close() },
		}, nil
	}
	return nil, fmt.Errorf("unknown entropy source %q", spec)
}

// tally is one worker's accumulators. Everything is per worker so the
// hot loop never touches shared memory; the serial statistics are
// therefore taken within a worker's own stream and pooled afterwards.
type tally struct {
	cand        []uint64
	word        []uint64
	bitOnes     []uint64
	draws       uint64
	ones        uint64
	bits        uint64
	transitions uint64
	bitPairs    uint64
	sx, sy      float64
	sxx, syy    float64
	sxy         float64
	candPairs   uint64
	checked     uint64
	invalid     uint64
	prefixes    uint64
	lastBit     int
	lastCand    int
	_           [64]byte // keep workers off each other's cache lines
}

func newTally(bins int) *tally {
	return &tally{
		cand:     make([]uint64, bins),
		word:     make([]uint64, int(NumWords)),
		bitOnes:  make([]uint64, bitsFor(bins)),
		lastBit:  -1,
		lastCand: -1,
	}
}

func bitsFor(bins int) int {
	n := 0
	for 1<<n < bins {
		n++
	}
	return n
}

func (t *tally) add(cand int, w Word, freeBits int) {
	t.draws++
	t.cand[cand]++
	t.word[w]++
	for i := freeBits - 1; i >= 0; i-- {
		b := (cand >> i) & 1
		t.bits++
		if b == 1 {
			t.ones++
			t.bitOnes[freeBits-1-i]++
		}
		if t.lastBit >= 0 {
			t.bitPairs++
			if b != t.lastBit {
				t.transitions++
			}
		}
		t.lastBit = b
	}
	if t.lastCand >= 0 {
		x, y := float64(t.lastCand), float64(cand)
		t.sx += x
		t.sy += y
		t.sxx += x * x
		t.syy += y * y
		t.sxy += x * y
		t.candPairs++
	}
	t.lastCand = cand
}

func (t *tally) merge(o *tally) {
	for i := range t.cand {
		t.cand[i] += o.cand[i]
	}
	for i := range t.word {
		t.word[i] += o.word[i]
	}
	for i := range t.bitOnes {
		t.bitOnes[i] += o.bitOnes[i]
	}
	t.draws += o.draws
	t.ones += o.ones
	t.bits += o.bits
	t.transitions += o.transitions
	t.bitPairs += o.bitPairs
	t.sx += o.sx
	t.sy += o.sy
	t.sxx += o.sxx
	t.syy += o.syy
	t.sxy += o.sxy
	t.candPairs += o.candPairs
	t.checked += o.checked
	t.invalid += o.invalid
	t.prefixes += o.prefixes
}

// TestPickLastWordDistribution is the soak. It draws last words until
// the deadline and tests the result for uniformity, bit balance, serial
// structure and min-entropy, with the prefix both pinned and redrawn on
// every draw so that a prefix-dependent bug cannot hide behind one
// lucky prefix.
func TestPickLastWordDistribution(t *testing.T) {
	if testing.Short() {
		t.Skip("distribution soak skipped under -short")
	}
	dur := envDuration("SH_BIP39_SOAK", *soakTime)
	seed := *soakSeed
	if seed == 0 {
		seed = time.Now().UnixNano()
	}
	src, err := newEntropySource(*soakRNG)
	if err != nil {
		t.Fatal(err)
	}
	defer src.closer()
	t.Logf("entropy source %q, prefix seed %d (reproduce with -bip39.seed=%d), %v per experiment, GOMAXPROCS=%d",
		src.spec, seed, seed, dur, runtime.GOMAXPROCS(0))
	for _, prefixLen := range []int{11, 23} {
		for _, fresh := range []bool{false, true} {
			name := fmt.Sprintf("%dwords/fixedprefix", prefixLen+1)
			if fresh {
				name = fmt.Sprintf("%dwords/freshprefix", prefixLen+1)
			}
			t.Run(name, func(t *testing.T) {
				soak(t, src, seed, prefixLen, fresh, dur)
			})
		}
	}
}

func soak(t *testing.T, src *entropySource, seed int64, prefixLen int, fresh bool, dur time.Duration) {
	checkBits, freeBits, ok := lastWordBits(prefixLen)
	if !ok {
		t.Fatalf("bad prefix length %d", prefixLen)
	}
	if err := src.reset(); err != nil {
		t.Fatalf("rewinding %s: %v", src.spec, err)
	}
	bins := 1 << freeBits
	workers := runtime.GOMAXPROCS(0)
	var stop atomic.Bool
	var readErr atomic.Value
	tallies := make([]*tally, workers)
	start := time.Now()
	timer := time.AfterFunc(dur, func() { stop.Store(true) })
	defer timer.Stop()

	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ta := newTally(bins)
			tallies[w] = ta
			rng := src.reader(w)
			prng := seededChaCha(uint64(seed), uint64(w), uint64(prefixLen))
			prefix := make(Mnemonic, prefixLen)
			randomPrefix(prefix, prng)
			ta.prefixes = 1
			var cands []Word
			if !fresh {
				cands = LastWords(prefix)
			}
			full := make(Mnemonic, prefixLen+1)
			budget := uint64(math.MaxUint64)
			if *soakMax > 0 {
				budget = *soakMax / uint64(workers)
			}
			for ta.draws < budget {
				if ta.draws%4096 == 0 && stop.Load() {
					return
				}
				if fresh {
					randomPrefix(prefix, prng)
					ta.prefixes++
				}
				word, err := PickLastWord(prefix, rng)
				if err != nil {
					readErr.Store(err)
					stop.Store(true)
					return
				}
				cand := int(word >> checkBits)
				if cand < 0 || cand >= bins {
					t.Errorf("word %d has free bits %d, outside 0..%d", word, cand, bins-1)
					stop.Store(true)
					return
				}
				if !fresh {
					// Full membership check, every draw: free.
					ta.checked++
					if cands[cand] != word {
						ta.invalid++
					}
				} else if ta.draws%1024 == 0 {
					// Fresh prefixes make the full enumeration too
					// costly per draw, so validate a sample through
					// Mnemonic.Valid instead.
					copy(full, prefix)
					full[prefixLen] = word
					ta.checked++
					if !full.Valid() {
						ta.invalid++
					}
				}
				ta.add(cand, word, freeBits)
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	if err := readErr.Load(); err != nil {
		if e, _ := err.(error); !errors.Is(e, io.EOF) && !errors.Is(e, io.ErrUnexpectedEOF) {
			t.Fatalf("entropy source failed: %v", err)
		}
		t.Logf("entropy source exhausted after %v", elapsed)
	}
	total := newTally(bins)
	for _, ta := range tallies {
		if ta != nil {
			total.merge(ta)
		}
	}
	if total.draws == 0 {
		t.Fatal("no draws")
	}
	report(t, total, freeBits, fresh, elapsed)
}

func report(t *testing.T, ta *tally, freeBits int, fresh bool, elapsed time.Duration) {
	alpha := *soakAlpha
	bad := func(name string, p float64, format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		if p < alpha {
			t.Errorf("FAIL %s: %s", name, line)
		} else {
			t.Logf("  %s: %s", name, line)
		}
	}
	rate := float64(ta.draws) / elapsed.Seconds()
	t.Logf("draws=%d prefixes=%d elapsed=%v rate=%.0f draws/s (%.2f M/s)",
		ta.draws, ta.prefixes, elapsed.Round(time.Millisecond), rate, rate/1e6)
	if ta.invalid > 0 {
		t.Errorf("FAIL validity: %d of %d checked draws were not valid completions", ta.invalid, ta.checked)
	} else {
		t.Logf("  validity: %d of %d draws checked against the checksum, all valid", ta.checked, ta.draws)
	}

	lo, hi := slices.Min(ta.cand), slices.Max(ta.cand)
	mean := float64(ta.draws) / float64(len(ta.cand))
	stat, df, exp := chiSquare(ta.cand)
	p := chiSquareSF(stat, df)
	t.Logf("  candidate counts: bins=%d min=%d max=%d mean=%.1f spread=%.3f%% of mean",
		len(ta.cand), lo, hi, mean, 100*float64(hi-lo)/mean)
	bad("chi2 candidate index", p, "H0 uniform over %d candidates: X2=%.3f df=%d expected/bin=%.1f p=%.4g",
		len(ta.cand), stat, df, exp, p)

	if fresh {
		// With a fresh prefix per draw the checksum bits come from a
		// fresh SHA-256 output, so the resulting word should cover the
		// whole list uniformly. The null therefore also assumes SHA-256
		// behaves as a random function on these inputs.
		wstat, wdf, wexp := chiSquare(ta.word)
		wp := chiSquareSF(wstat, wdf)
		wlo, whi := slices.Min(ta.word), slices.Max(ta.word)
		t.Logf("  word counts: bins=%d min=%d max=%d", len(ta.word), wlo, whi)
		bad("chi2 word index", wp, "H0 uniform over %d words: X2=%.3f df=%d expected/bin=%.1f p=%.4g",
			len(ta.word), wstat, wdf, wexp, wp)
	} else {
		nz := 0
		for _, c := range ta.word {
			if c > 0 {
				nz++
			}
		}
		t.Logf("  word index: %d distinct words reached (a relabelling of the candidate index for a fixed prefix)", nz)
	}

	// Monobit: H0 says each free bit is an independent fair coin.
	zeros := ta.bits - ta.ones
	z := (float64(ta.ones) - float64(ta.bits)/2) / (0.5 * math.Sqrt(float64(ta.bits)))
	bad("monobit", twoSidedNormalP(z), "H0 P(bit=1)=1/2: ones=%d zeros=%d of %d bits, deviation=%+.6f%% z=%+.4f p=%.4g",
		ta.ones, zeros, ta.bits, 100*(float64(ta.ones)/float64(ta.bits)-0.5), z, twoSidedNormalP(z))

	// Per position, so a single stuck or skewed bit lane cannot hide in
	// the aggregate.
	var posStat float64
	perPos := make([]string, len(ta.bitOnes))
	nPer := ta.draws
	for i, ones := range ta.bitOnes {
		zi := (float64(ones) - float64(nPer)/2) / (0.5 * math.Sqrt(float64(nPer)))
		posStat += zi * zi
		perPos[i] = fmt.Sprintf("b%d:%+.2f", i, zi)
	}
	pp := chiSquareSF(posStat, len(ta.bitOnes))
	bad("bit balance per position", pp, "H0 every one of the %d free-bit lanes is fair: X2=%.3f df=%d p=%.4g z=[%s]",
		freeBits, posStat, len(ta.bitOnes), pp, strings.Join(perPos, " "))

	// Runs over the bit stream: H0 says adjacent bits are independent,
	// so a transition happens with probability 2p(1-p).
	pi := float64(ta.ones) / float64(ta.bits)
	pt := 2 * pi * (1 - pi)
	expT := float64(ta.bitPairs) * pt
	zr := (float64(ta.transitions) - expT) / math.Sqrt(float64(ta.bitPairs)*pt*(1-pt))
	bad("runs", twoSidedNormalP(zr), "H0 adjacent bits independent: transitions=%d expected=%.1f runs=%d z=%+.4f p=%.4g",
		ta.transitions, expT, ta.transitions+1, zr, twoSidedNormalP(zr))

	// Lag-1 correlation of the candidate index: H0 says consecutive
	// draws are independent, so r=0.
	n := float64(ta.candPairs)
	num := n*ta.sxy - ta.sx*ta.sy
	den := math.Sqrt((n*ta.sxx - ta.sx*ta.sx) * (n*ta.syy - ta.sy*ta.sy))
	r := 0.0
	if den > 0 {
		r = num / den
	}
	zc := r * math.Sqrt(n)
	bad("serial correlation lag-1", twoSidedNormalP(zc), "H0 r=0 over %d consecutive pairs: r=%+.6f z=%+.4f p=%.4g",
		ta.candPairs, r, zc, twoSidedNormalP(zc))

	// Min-entropy, NIST SP 800-90B most-common-value estimator.
	hCand, puCand := mcvMinEntropy(ta.cand)
	bitCounts := []uint64{ta.bits - ta.ones, ta.ones}
	hBit, puBit := mcvMinEntropy(bitCounts)
	tol := mcvTolerance(len(ta.cand), ta.draws)
	t.Logf("  min-entropy (MCV, 99%% bound): %.5f bits/draw of %d ideal (p_upper=%.8f, tolerance %.5f); %.6f bits/bit (p_upper=%.8f)",
		hCand, freeBits, puCand, tol, hBit, puBit)
	if hCand < float64(freeBits)-tol {
		t.Errorf("FAIL min-entropy: %.5f bits per draw, ideal %d, tolerance %.5f", hCand, freeBits, tol)
	}

	// One greppable line per experiment.
	t.Logf("SUMMARY freebits=%d fresh=%v draws=%d rate=%.0f chi2=%.3f df=%d p=%.4g monobit_p=%.4g runs_p=%.4g serial_p=%.4g minent=%.5f",
		freeBits, fresh, ta.draws, rate, stat, df, p, twoSidedNormalP(z), twoSidedNormalP(zr), twoSidedNormalP(zc), hCand)
}

// biasedDraw returns a candidate index from a distribution where bin 0
// carries (1+d)/bins and the rest share the deficit evenly. Redirecting
// a uniform draw to bin 0 with probability d/(bins-1) produces exactly
// that, which is the alternative the power analysis in
// testdata/power.py is written against.
func biasedDraw(rng *rand.Rand, bins int, d float64) int {
	k := rng.IntN(bins)
	if rng.Float64() < d/float64(bins-1) {
		k = 0
	}
	return k
}

// TestSoakDetectsBias is the harness's own control. A soak that always
// reports "uniform" is indistinguishable from a soak that cannot see
// bias at all, so a deliberately skewed source has to fail the same
// statistic the real one passes. The second subtest goes further and
// measures the rejection rate at the sample size the power analysis
// claims gives 95% power, which is what turns the claimed sensitivity
// into a measured one.
func TestSoakDetectsBias(t *testing.T) {
	prefix := make(Mnemonic, 11)
	randomPrefix(prefix, seededChaCha(0x5EEDBEEF12345678, 3))
	cands := LastWords(prefix)
	t.Run("obvious", func(t *testing.T) {
		const draws = 400000
		for _, bias := range []float64{2.56, 0.64} {
			ta := newTally(len(cands))
			rng := rand.New(rand.NewPCG(1, 2))
			for range draws {
				v := biasedDraw(rng, len(cands), bias)
				ta.add(v, cands[v], 7)
			}
			stat, df, _ := chiSquare(ta.cand)
			p := chiSquareSF(stat, df)
			t.Logf("one bin %.0f%% heavy over %d draws: X2=%.1f df=%d p=%.4g", 100*bias, draws, stat, df, p)
			if p > *soakAlpha {
				t.Errorf("chi-square did not see a %.0f%% bias: p=%.4g", 100*bias, p)
			}
		}
	})
	t.Run("calibration", func(t *testing.T) {
		if testing.Short() {
			t.Skip("power calibration skipped under -short")
		}
		// testdata/power.py: for 8 bins at alpha=1e-6, 95% power against
		// a 1% one-bin bias needs lambda=56.731, i.e. n = lambda*df/d^2.
		const bins, d, replicates = 8, 0.01, 40
		const draws = 3971170
		rejected := 0
		rng := rand.New(rand.NewPCG(7, 11))
		for range replicates {
			counts := make([]uint64, bins)
			for range draws {
				counts[biasedDraw(rng, bins, d)]++
			}
			stat, df, _ := chiSquare(counts)
			if chiSquareSF(stat, df) < *soakAlpha {
				rejected++
			}
		}
		t.Logf("%d replicates of %d draws with a %.1f%% one-bin bias: rejected %d (%.0f%%), predicted 95%%",
			replicates, draws, 100*d, rejected, 100*float64(rejected)/replicates)
		// Binomial noise on 40 replicates at p=0.95 has sd 1.4, so a
		// correct power model lands well above 33.
		if rejected < 33 {
			t.Errorf("rejected %d of %d replicates, power model predicts about 38", rejected, replicates)
		}
	})
}

func BenchmarkPickLastWord(b *testing.B) {
	for _, prefixLen := range []int{11, 23} {
		prefix := make(Mnemonic, prefixLen)
		randomPrefix(prefix, seededChaCha(1, 2))
		b.Run(fmt.Sprintf("%dwords/crypto", prefixLen+1), func(b *testing.B) {
			for b.Loop() {
				if _, err := PickLastWord(prefix, crand.Reader); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("%dwords/chacha", prefixLen+1), func(b *testing.B) {
			rng := seededChaCha(3, 4)
			for b.Loop() {
				if _, err := PickLastWord(prefix, rng); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
	b.Run("LastWords/12words", func(b *testing.B) {
		prefix := make(Mnemonic, 11)
		randomPrefix(prefix, seededChaCha(5, 6))
		for b.Loop() {
			LastWords(prefix)
		}
	})
}

// stalledReader never errors and never delivers, which io.ReadFull spins
// on. gui.Rand is exported and writable, so a reader like this can be
// installed from outside this repository.
type stalledReader struct{ calls int }

func (r *stalledReader) Read(p []byte) (int, error) { r.calls++; return 0, nil }

func TestPickLastWordGivesUpOnAStalledReader(t *testing.T) {
	prefix := make(Mnemonic, 23)
	done := make(chan error, 1)
	r := new(stalledReader)
	go func() {
		_, err := PickLastWord(prefix, r)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, io.ErrNoProgress) {
			t.Errorf("got %v, want io.ErrNoProgress", err)
		}
		if r.calls > maxNoProgress+1 {
			t.Errorf("read %d times, want at most %d", r.calls, maxNoProgress+1)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PickLastWord hung on a reader that returns (0, nil)")
	}
}
