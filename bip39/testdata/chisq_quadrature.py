import math
# Independent chi-square survival by high-resolution Simpson quadrature
# in log-space pdf. p(x) = x^(k/2-1) e^(-x/2) / (2^(k/2) Gamma(k/2))
def logpdf(x,k):
    if x<=0: return -math.inf
    return (k/2-1)*math.log(x) - x/2 - (k/2)*math.log(2) - math.lgamma(k/2)
def sf(x0,k,N=2000001):
    # integrate to a far tail: mean k, sd sqrt(2k); go 40 sd past max(x0,k)
    hi=max(x0,k)+60*math.sqrt(2*k)+200
    h=(hi-x0)/N
    s=0.0
    for i in range(N+1):
        x=x0+i*h
        w=1 if i in (0,N) else (4 if i%2 else 2)
        s+=w*math.exp(logpdf(x,k))
    return s*h/3
for x,k in [(3.841458820694124,1),(5.991464547107979,2),(18.307038053275146,10),
            (14.067140449340167,7),(2.0,2),(20.0,2),
            (154.30152420585646,128),(2145.965833413664,2047),
            (128.0,128),(2047.0,2047),(100.0,128),(2200.0,2047)]:
    print(k, x, "%.12g"%sf(x,k))
