package engrave

import (
	"math"

	"seedhammer.com/bezier"
)

// dubinsBridge appends the shortest bounded-curvature path from exit
// with heading thOut to entry with heading thIn, turning radius r,
// sampled at close to spacing-length chords. The needle-up bridges of
// flying transitions use it for hairpin and reversal junctions where
// a direct tangent bridge would bend below the curvature envelope.
// The construction self-checks its endpoint and heading closure and
// returns nil when no candidate closes.
func dubinsBridge(dst []bezier.Point, exit bezier.Point, thOut float64, entry bezier.Point, thIn float64, r, spacing float64) []bezier.Point {
	dx := float64(entry.X - exit.X)
	dy := float64(entry.Y - exit.Y)
	D := math.Hypot(dx, dy)
	d := D / r
	phi := math.Atan2(dy, dx)
	alpha := mod2pi(thOut - phi)
	beta := mod2pi(thIn - phi)
	sa, ca := math.Sincos(alpha)
	sb, cb := math.Sincos(beta)
	cab := math.Cos(alpha - beta)

	// Candidate words, best-first by total length. Arc params t, q
	// (and p for the CCC words) are in radians; p is a straight
	// length in radius units for the CSC words.
	type word struct {
		t, p, q float64
		m       [3]byte
	}
	var cands []word
	add := func(t, p, q float64, m [3]byte) {
		if math.IsNaN(t) || math.IsNaN(p) || math.IsNaN(q) || t < 0 || p < 0 || q < 0 {
			return
		}
		cands = append(cands, word{t, p, q, m})
	}
	if psq := 2 + d*d - 2*cab + 2*d*(sa-sb); psq >= 0 {
		tmp := math.Atan2(cb-ca, d+sa-sb)
		add(mod2pi(-alpha+tmp), math.Sqrt(psq), mod2pi(beta-tmp), [3]byte{'L', 'S', 'L'})
	}
	if psq := 2 + d*d - 2*cab + 2*d*(sb-sa); psq >= 0 {
		tmp := math.Atan2(ca-cb, d-sa+sb)
		add(mod2pi(alpha-tmp), math.Sqrt(psq), mod2pi(-beta+tmp), [3]byte{'R', 'S', 'R'})
	}
	if psq := -2 + d*d + 2*cab + 2*d*(sa+sb); psq >= 0 {
		p := math.Sqrt(psq)
		tmp := math.Atan2(-ca-cb, d+sa+sb) - math.Atan2(-2, p)
		add(mod2pi(-alpha+tmp), p, mod2pi(-mod2pi(beta)+tmp), [3]byte{'L', 'S', 'R'})
	}
	if psq := -2 + d*d + 2*cab - 2*d*(sa+sb); psq >= 0 {
		p := math.Sqrt(psq)
		tmp := math.Atan2(ca+cb, d-sa-sb) - math.Atan2(2, p)
		add(mod2pi(alpha-tmp), p, mod2pi(beta-tmp), [3]byte{'R', 'S', 'L'})
	}
	if tmp := (6 - d*d + 2*cab + 2*d*(sa-sb)) / 8; math.Abs(tmp) <= 1 {
		p := mod2pi(2*math.Pi - math.Acos(tmp))
		t := mod2pi(alpha - math.Atan2(ca-cb, d-sa+sb) + p/2)
		add(t, p, mod2pi(alpha-beta-t+p), [3]byte{'R', 'L', 'R'})
	}
	if tmp := (6 - d*d + 2*cab + 2*d*(sb-sa)) / 8; math.Abs(tmp) <= 1 {
		p := mod2pi(2*math.Pi - math.Acos(tmp))
		t := mod2pi(-alpha - math.Atan2(ca-cb, d+sa-sb) + p/2)
		add(t, p, mod2pi(mod2pi(beta)-alpha-t+p), [3]byte{'L', 'R', 'L'})
	}
	// Best-first: try candidates by length until one closes.
	for len(cands) > 0 {
		bi, bl := -1, math.Inf(1)
		for i, c := range cands {
			l := c.t + c.q
			if c.m[1] == 'S' {
				l += c.p
			} else {
				l += c.p
			}
			if l < bl {
				bi, bl = i, l
			}
		}
		c := cands[bi]
		cands = append(cands[:bi], cands[bi+1:]...)
		if bl*r > D+8*r {
			continue
		}
		if out, ok := sampleDubins(dst, exit, thOut, entry, thIn, r, spacing, c.t, c.p, c.q, c.m); ok {
			return out
		}
	}
	return nil
}

func sampleDubins(dst []bezier.Point, exit bezier.Point, thOut float64, entry bezier.Point, thIn float64, r, spacing float64, t, p, q float64, m [3]byte) ([]bezier.Point, bool) {
	x, y, th := float64(exit.X), float64(exit.Y), thOut
	n0 := len(dst)
	emit := func() {
		dst = append(dst, bezier.Point{X: int(math.Round(x)), Y: int(math.Round(y))})
	}
	seg := func(kind byte, amount float64) {
		switch kind {
		case 'S':
			length := amount * r
			steps := int(math.Ceil(length / spacing))
			for i := 0; i < steps; i++ {
				ds := min(spacing, length-float64(i)*spacing)
				x += math.Cos(th) * ds
				y += math.Sin(th) * ds
				emit()
			}
		default:
			sign := 1.0
			if kind == 'R' {
				sign = -1
			}
			steps := int(math.Ceil(amount * r / spacing))
			for i := 0; i < steps; i++ {
				da := min(spacing/r, amount-float64(i)*spacing/r)
				// chord of the arc step
				mid := th + sign*da/2
				chord := 2 * r * math.Sin(da/2)
				x += math.Cos(mid) * chord
				y += math.Sin(mid) * chord
				th = mod2pi(th + sign*da)
				emit()
			}
		}
	}
	seg(m[0], t)
	seg(m[1], p)
	seg(m[2], q)
	// Closure self-check: a mis-transcribed candidate must fail here,
	// degrading to "does not fly", never to bad geometry.
	ex, ey := float64(entry.X), float64(entry.Y)
	if math.Hypot(x-ex, y-ey) > spacing/2+1 {
		return dst[:n0], false
	}
	if a := math.Abs(mod2pi(th-thIn+math.Pi) - math.Pi); a > 0.06 {
		return dst[:n0], false
	}
	// Snap the tail onto the entry point.
	if n := len(dst); n > n0 {
		dst[n-1] = entry
	}
	return dst, true
}

func mod2pi(x float64) float64 {
	x = math.Mod(x, 2*math.Pi)
	if x < 0 {
		x += 2 * math.Pi
	}
	return x
}
