package pricing

import (
	"errors"
	"math/big"
	"strings"
)

// Limits of ParseDecimal.
const (
	maxFractionDigits = 30
	maxDigits         = 60
)

// Errors of ParseDecimal. None quotes the input.
var (
	errDecimalSyntax = errors.New("pricing: not a decimal number: expected digits with an optional fraction")
	errDecimalSize   = errors.New("pricing: decimal number out of range: at most 60 digits and 30 fraction digits")
)

// Decimal is an exact decimal number: an unscaled integer and a scale (the number of fraction
// digits). Values are immutable and always canonical: the scale is never negative and has no
// trailing zero. The zero value is the number 0.
type Decimal struct {
	unscaled *big.Int // nil means zero
	scale    int
}

// ParseDecimal parses -?digits(.digits)?: no exponent, no "+", no NaN, no infinity, no spaces,
// no leading zeros, at most 30 fraction digits and 60 digits in all. The error never quotes the
// input.
func ParseDecimal(s string) (Decimal, error) {
	rest := s
	neg := false
	if strings.HasPrefix(rest, "-") {
		neg = true
		rest = rest[1:]
	}
	intPart, frac, hasPoint := strings.Cut(rest, ".")
	if !allDigits(intPart) || (hasPoint && !allDigits(frac)) {
		return Decimal{}, errDecimalSyntax
	}
	if len(intPart) > 1 && intPart[0] == '0' {
		return Decimal{}, errDecimalSyntax
	}
	digits := len(intPart) + len(frac)
	if intPart == "0" {
		digits--
	}
	if len(frac) > maxFractionDigits || digits > maxDigits {
		return Decimal{}, errDecimalSize
	}
	n, ok := new(big.Int).SetString(intPart+frac, 10)
	if !ok {
		return Decimal{}, errDecimalSyntax
	}
	if neg {
		n.Neg(n)
	}
	return normalize(n, len(frac)), nil
}

// allDigits reports whether s is a non-empty run of ASCII digits.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

var (
	bigTen = big.NewInt(10)
	bigOne = big.NewInt(1)
)

// pow10 returns 10^n for n >= 0.
func pow10(n int) *big.Int {
	return new(big.Int).Exp(bigTen, big.NewInt(int64(n)), nil)
}

// normalize builds the canonical value of n / 10^scale. It takes ownership of n.
func normalize(n *big.Int, scale int) Decimal {
	if n.Sign() == 0 {
		return Decimal{}
	}
	r := new(big.Int)
	q := new(big.Int)
	for scale > 0 {
		q.QuoRem(n, bigTen, r)
		if r.Sign() != 0 {
			break
		}
		n.Set(q)
		scale--
	}
	return Decimal{unscaled: n, scale: scale}
}

// num returns the unscaled integer, never nil and never shared for writing.
func (d Decimal) num() *big.Int {
	if d.unscaled == nil {
		return new(big.Int)
	}
	return d.unscaled
}

// String is the canonical text: no exponent, no trailing zeros in the fraction, "0" for zero.
func (d Decimal) String() string {
	if d.unscaled == nil || d.unscaled.Sign() == 0 {
		return "0"
	}
	neg := d.unscaled.Sign() < 0
	digits := new(big.Int).Abs(d.unscaled).String()
	if d.scale > 0 {
		if d.scale >= len(digits) {
			digits = strings.Repeat("0", d.scale-len(digits)+1) + digits
		}
		p := len(digits) - d.scale
		digits = digits[:p] + "." + digits[p:]
	}
	if neg {
		return "-" + digits
	}
	return digits
}

// align returns the unscaled integers of d and e at their common scale.
func align(d, e Decimal) (*big.Int, *big.Int, int) {
	a, b := d.num(), e.num()
	switch {
	case d.scale < e.scale:
		a = new(big.Int).Mul(a, pow10(e.scale-d.scale))
		return a, b, e.scale
	case d.scale > e.scale:
		b = new(big.Int).Mul(b, pow10(d.scale-e.scale))
	}
	return a, b, d.scale
}

// Add returns d + e.
func (d Decimal) Add(e Decimal) Decimal {
	a, b, scale := align(d, e)
	return normalize(new(big.Int).Add(a, b), scale)
}

// MulInt returns d * n.
func (d Decimal) MulInt(n int64) Decimal {
	return normalize(new(big.Int).Mul(d.num(), big.NewInt(n)), d.scale)
}

// DivPow10 returns d / 10^n exactly; n must not be negative.
func (d Decimal) DivPow10(n int) Decimal {
	if n < 0 {
		n = 0
	}
	return normalize(new(big.Int).Set(d.num()), d.scale+n)
}

// Round rounds to the given number of fraction digits, half away from zero. It is meant for
// display: a stored amount is exact.
func (d Decimal) Round(places int) Decimal {
	if places < 0 {
		places = 0
	}
	if d.scale <= places {
		return d
	}
	factor := pow10(d.scale - places)
	q, r := new(big.Int).QuoRem(new(big.Int).Abs(d.num()), factor, new(big.Int))
	if r.Lsh(r, 1).Cmp(factor) >= 0 {
		q.Add(q, bigOne)
	}
	if d.num().Sign() < 0 {
		q.Neg(q)
	}
	return normalize(q, places)
}

// Cmp compares d and e: -1, 0 or 1.
func (d Decimal) Cmp(e Decimal) int {
	a, b, _ := align(d, e)
	return a.Cmp(b)
}

// Sign returns -1, 0 or 1.
func (d Decimal) Sign() int {
	if d.unscaled == nil {
		return 0
	}
	return d.unscaled.Sign()
}

// MarshalText returns the canonical text.
func (d Decimal) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// UnmarshalText parses the text with the rules of ParseDecimal.
func (d *Decimal) UnmarshalText(b []byte) error {
	v, err := ParseDecimal(string(b))
	if err != nil {
		return err
	}
	*d = v
	return nil
}
