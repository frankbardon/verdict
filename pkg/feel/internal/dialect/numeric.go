package dialect

// spec on FEEL's number is https://kiegroup.github.io/dmn-feel-handbook/#number
import (
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"strings"
)

const (
	Prec = 34 * 8
)

var (
	ErrParseNumber = errors.New("fail to parse number")
)

type Number struct {
	v *big.Float
}

func NewNumber(strn string) *Number {
	v := new(big.Float)
	v.SetPrec(Prec).SetString(strn)
	return &Number{v: v}
}

func NewNumberFromInt64(input int64) *Number {
	v := new(big.Float)
	v.SetPrec(Prec).SetInt64(input)
	return &Number{v: v}
}

func NewNumberFromFloat(input float64) *Number {
	v := new(big.Float)
	v.SetPrec(Prec).SetFloat64(input)
	return &Number{v: v}
}

func ParseNumberWithErr(v interface{}) (*Number, error) {
	switch vv := v.(type) {
	case int:
		return NewNumberFromInt64(int64(vv)), nil
	case int64:
		return NewNumberFromInt64(vv), nil
	case float64:
		return NewNumberFromFloat(vv), nil
	case string:
		return NewNumber(vv), nil
	case *Number:
		return vv, nil
	default:
		return nil, ErrParseNumber
	}
}

func N(v interface{}) *Number {
	n, err := ParseNumberWithErr(v)
	if err != nil {
		panic(err)
	}
	return n
}

func (n Number) Int64() int64 {
	i64v, _ := n.v.Int64()
	return i64v
}

func (n Number) Int() int {
	return int(n.Int64())
}

func (n Number) Float64() float64 {
	f64v, _ := n.v.Float64()
	return f64v
}

func (n *Number) Add(other *Number) *Number {
	newv := new(big.Float)
	newv.SetPrec(Prec).Add(n.v, other.v)
	return &Number{v: newv}
}

func (n *Number) Sub(other *Number) *Number {
	newv := new(big.Float)
	newv.SetPrec(Prec).Sub(n.v, other.v)
	return &Number{v: newv}
}

func (n *Number) Mul(other *Number) *Number {
	newv := new(big.Float)
	newv.SetPrec(Prec).Mul(n.v, other.v)
	return &Number{v: newv}
}

func (n *Number) Cmp(other *Number) int {
	return n.v.Cmp(other.v)
}

func (n *Number) IntDiv(other *Number) *Number {
	newv := new(big.Int)
	a, _ := n.v.Int(nil)
	b, _ := other.v.Int(nil)
	newv.Div(a, b)
	newf := new(big.Float)
	newf.SetPrec(Prec).SetInt(newv)
	return &Number{v: newf}
}

// Div divides at the evaluator's working precision. It is the implementation
// behind FEEL's `/`; callers must reject a zero divisor first.
func (n *Number) Div(other *Number) *Number {
	newf := new(big.Float).SetPrec(Prec)
	newf.Quo(n.v, other.v)
	return &Number{v: newf}
}

// maxExactExponent bounds the integer exponents Pow computes exactly. Beyond it
// the result is computed through float64, which is finite only for results a
// float64 can hold — so `10 ** 1000000000` is null rather than a long stall.
const maxExactExponent = 1 << 16

// Pow raises n to the power other. It reports false when the result is not a
// finite number: a zero base with a negative exponent, a negative base with a
// fractional exponent, or an overflow. (VERDICT DIVERGENCE: upstream had no
// exponentiation; see NOTICE.md.)
func (n *Number) Pow(other *Number) (*Number, bool) {
	if other.v.IsInt() {
		if e, acc := other.v.Int64(); acc == big.Exact && e >= -maxExactExponent && e <= maxExactExponent {
			neg := e < 0
			if neg {
				e = -e
				if n.IsZero() {
					return nil, false
				}
			}
			result := new(big.Float).SetPrec(Prec).SetInt64(1)
			base := new(big.Float).SetPrec(Prec).Set(n.v)
			for ; e > 0; e >>= 1 {
				if e&1 == 1 {
					result.Mul(result, base)
				}
				base.Mul(base, base)
			}
			if neg {
				result.Quo(new(big.Float).SetPrec(Prec).SetInt64(1), result)
			}
			if result.IsInf() {
				return nil, false
			}
			return &Number{v: result}, true
		}
	}
	a, _ := n.v.Float64()
	b, _ := other.v.Float64()
	f := math.Pow(a, b)
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return nil, false
	}
	return NewNumberFromFloat(f), true
}

// rat is the number as an exact rational, read through its decimal text to 60
// significant digits. VERDICT DIVERGENCE: a decimal literal such as 0.1 is held
// in binary at Prec bits, so its big.Float is a hair off 1/10; rounding to 60
// digits (well inside Prec's ~81) recovers the decimal the model wrote, which
// is what duration scaling multiplies by (DMN 1.5 §10.3.2.15, Table 59).
func (n *Number) rat() *big.Rat {
	r, ok := new(big.Rat).SetString(n.v.Text('g', 60))
	if !ok {
		return new(big.Rat)
	}
	return r
}

// IsZero reports whether the number is exactly zero.
func (n *Number) IsZero() bool { return n.v.Sign() == 0 }

func (n *Number) FloatDiv(other *Number) *Number {
	a, _ := n.v.Float64()
	b, _ := other.v.Float64()
	newf := new(big.Float)
	newf.SetPrec(Prec).SetFloat64(a / b)
	return &Number{v: newf}
}

func (n *Number) IntMod(other *Number) *Number {
	newv := new(big.Int)
	a, _ := n.v.Int(nil)
	b, _ := other.v.Int(nil)
	newv.Mod(a, b)
	newf := new(big.Float)
	newf.SetPrec(Prec).SetInt(newv)
	return &Number{v: newf}
}

func (n Number) Equal(other Number) bool {
	return n.Compare(other) == 0
}

func (n Number) Compare(other Number) int {
	return n.v.Cmp(other.v)
}

func (n Number) String() string {
	s := n.v.Text('f', 18)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	return s
}

func (n Number) MarshalJSON() ([]byte, error) {
	if f32v, acc := n.v.Float32(); acc == big.Exact {
		return json.Marshal(f32v)
	}
	return json.Marshal(n.String())
}

var Zero = N(0)
