"""Power analysis for the last-word distribution soak.

Answers two questions with the numbers the soak actually achieved:
  * how many draws are needed to see a one-bin relative bias of a given
    size, at 95% power and the soak's own alpha, and
  * what bias the soak as run could already have seen.

Alternative: one candidate carries probability (1+d)/B, the other B-1
share the deficit evenly. The chi-square noncentrality is then
    lambda = n d^2 / (B-1)
and power is computed with the Patnaik chi-square approximation to the
noncentral chi-square.
"""

import math

def _gser(a, x):
    ap, dl, s = a, 1.0 / a, 1.0 / a
    for _ in range(10000):
        ap += 1
        dl *= x / ap
        s += dl
        if abs(dl) < abs(s) * 1e-16:
            break
    return s * math.exp(-x + a * math.log(x) - math.lgamma(a))

def _gcf(a, x):
    tiny = 1e-300
    b, c, d = x + 1 - a, 1 / tiny, 1 / (x + 1 - a)
    h = d
    for i in range(1, 10000):
        an = -i * (i - a)
        b += 2
        d = an * d + b
        if abs(d) < tiny:
            d = tiny
        c = b + an / c
        if abs(c) < tiny:
            c = tiny
        d = 1 / d
        dl = d * c
        h *= dl
        if abs(dl - 1) < 1e-16:
            break
    return math.exp(-x + a * math.log(x) - math.lgamma(a)) * h

def gammaq(a, x):
    if x <= 0:
        return 1.0
    return 1 - _gser(a, x) if x < a + 1 else _gcf(a, x)

def chi2sf(x, df):
    return gammaq(df / 2, x / 2)

def chi2crit(df, alpha):
    lo, hi = 0.0, float(df)
    while chi2sf(hi, df) > alpha:
        hi *= 2
    for _ in range(300):
        mid = (lo + hi) / 2
        if chi2sf(mid, df) > alpha:
            lo = mid
        else:
            hi = mid
    return (lo + hi) / 2

def power(lam, df, crit):
    """Patnaik: noncentral chi2(df, lam) ~ c * chi2(f)."""
    if lam <= 0:
        return chi2sf(crit, df)
    c = (df + 2 * lam) / (df + lam)
    f = (df + lam) ** 2 / (df + 2 * lam)
    return chi2sf(crit / c, f)

def lambda_for_power(df, crit, target=0.95):
    lo, hi = 0.0, 10.0
    while power(hi, df, crit) < target:
        hi *= 2
    for _ in range(300):
        mid = (lo + hi) / 2
        if power(mid, df, crit) < target:
            lo = mid
        else:
            hi = mid
    return (lo + hi) / 2

if __name__ == "__main__":
    # Self-check against the independent quadrature values.
    for x, df, want in [(3.841458820694124, 1, 0.05),
                        (154.30152420585646, 128, 0.0565865675595),
                        (2145.965833413664, 2047, 0.0626563090986)]:
        got = chi2sf(x, df)
        assert abs(got - want) < 1e-9, (x, df, got, want)
    print("chi2sf self-check ok")

    alpha = 1e-6
    import sys
    # bins, draws achieved, draws/sec, label
    cases = [(int(a), int(b), float(c), d) for a, b, c, d in
             (arg.split(",") for arg in sys.argv[1:])]
    for bins, n_run, rate, label in cases:
        df = bins - 1
        crit = chi2crit(df, alpha)
        lam95 = lambda_for_power(df, crit)
        print(f"\n{label}: bins={bins} df={df} alpha={alpha:g} "
              f"crit={crit:.3f} lambda(95% power)={lam95:.3f}")
        for d in (0.001, 0.01, 0.05):
            n = lam95 * df / d**2
            print(f"  one-bin relative bias {d*100:>5.2f}%: "
                  f"n={n:.4g} draws = {n/rate:.4g} s = {n/rate/3600:.4g} h "
                  f"at {rate:.0f} draws/s")
        d_run = math.sqrt(lam95 * df / n_run)
        print(f"  soak as run (n={n_run}): detects a one-bin relative bias of "
              f">= {d_run*100:.4f}% at 95% power")
