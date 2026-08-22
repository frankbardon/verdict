package feel

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	pfeel "github.com/frankbardon/verdict/pkg/feel/internal/dialect"
)

// Null is the FEEL null value. It is distinct from a missing binding: a name
// bound to Null exists and evaluates to null, whereas an unbound name raises an
// evaluation error.
var Null = &pfeel.NullValue{}

// IsNull reports whether v is the FEEL null in either its FEEL or Go spelling.
func IsNull(v any) bool {
	if v == nil {
		return true
	}
	_, ok := v.(*pfeel.NullValue)
	return ok
}

// FromGo converts a plain Go value into the representation the embedded
// evaluator expects. Numbers become arbitrary-precision FEEL numbers, times
// become FEEL date-and-times, durations become FEEL day/time durations, and
// maps and slices are converted element-wise. Values that are already FEEL
// values pass through untouched.
//
// Structs and named map/slice types are converted through their reflected
// shape rather than through JSON, so a time.Time nested inside a struct keeps
// its type instead of degrading to a string.
func FromGo(v any) any {
	switch x := v.(type) {
	case nil:
		return Null
	case *pfeel.NullValue, *pfeel.Number, *pfeel.FEELDate, *pfeel.FEELTime, *pfeel.FEELDatetime, *pfeel.FEELDuration:
		return x
	case bool, string:
		return x
	case int:
		return pfeel.NewNumberFromInt64(int64(x))
	case int8:
		return pfeel.NewNumberFromInt64(int64(x))
	case int16:
		return pfeel.NewNumberFromInt64(int64(x))
	case int32:
		return pfeel.NewNumberFromInt64(int64(x))
	case int64:
		return pfeel.NewNumberFromInt64(x)
	case uint:
		return pfeel.NewNumberFromInt64(int64(x))
	case uint8:
		return pfeel.NewNumberFromInt64(int64(x))
	case uint16:
		return pfeel.NewNumberFromInt64(int64(x))
	case uint32:
		return pfeel.NewNumberFromInt64(int64(x))
	case uint64:
		return pfeel.NewNumberFromInt64(int64(x))
	case float32:
		return pfeel.NewNumberFromFloat(float64(x))
	case float64:
		return pfeel.NewNumberFromFloat(x)
	case json.Number:
		return pfeel.NewNumber(x.String())
	case time.Time:
		return datetimeFromGo(x)
	case time.Duration:
		return pfeel.NewFEELDuration(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = FromGo(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = FromGo(e)
		}
		return out
	}
	return fromGoReflect(v)
}

func fromGoReflect(v any) any {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface:
		if rv.IsNil() {
			return Null
		}
		return FromGo(rv.Elem().Interface())
	case reflect.Slice, reflect.Array:
		out := make([]any, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out[i] = FromGo(rv.Index(i).Interface())
		}
		return out
	case reflect.Map:
		out := make(map[string]any, rv.Len())
		for _, k := range rv.MapKeys() {
			out[fmt.Sprint(k.Interface())] = FromGo(rv.MapIndex(k).Interface())
		}
		return out
	case reflect.Struct:
		out := make(map[string]any, rv.NumField())
		t := rv.Type()
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name := f.Name
			if tag := f.Tag.Get("json"); tag != "" && tag != "-" {
				if comma := strings.IndexByte(tag, ','); comma >= 0 {
					tag = tag[:comma]
				}
				if tag != "" {
					name = tag
				}
			}
			out[name] = FromGo(rv.Field(i).Interface())
		}
		return out
	}
	// Anything else (channels, funcs) is passed through; the evaluator will
	// report a type mismatch if it is actually used.
	return v
}

// ToGo converts a FEEL value back into plain Go: numbers become float64, FEEL
// temporals become time.Time or time.Duration, null becomes nil, and contexts
// and lists are converted element-wise.
//
// FEEL numbers carry more precision than a float64 can hold. ToGo is the
// interop boundary, not the arithmetic one — every calculation happens in full
// precision inside the evaluator and only the final value is narrowed here.
// Use ToGoExact when the extra digits matter.
func ToGo(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case *pfeel.NullValue:
		return nil
	case *pfeel.Number:
		if x == nil {
			return nil
		}
		return x.Float64()
	case *pfeel.FEELDate:
		return x.Date()
	case *pfeel.FEELTime:
		return x.Time()
	case *pfeel.FEELDatetime:
		return x.Time()
	case *pfeel.FEELDuration:
		return durationToGo(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = ToGo(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = ToGo(e)
		}
		return out
	default:
		return v
	}
}

// ToGoExact is ToGo except that numbers are rendered as their exact decimal
// string. Trace serialisation uses it when configured for full fidelity.
func ToGoExact(v any) any {
	if n, ok := v.(*pfeel.Number); ok && n != nil {
		return n.String()
	}
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = ToGoExact(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = ToGoExact(e)
		}
		return out
	}
	return ToGo(v)
}

// durationToGo narrows a FEEL duration to a time.Duration. Years-and-months
// durations have no exact nanosecond length, so they are returned as their
// canonical ISO-8601 string rather than a wrong number.
func durationToGo(d *pfeel.FEELDuration) any {
	if d == nil {
		return nil
	}
	if d.Years != 0 || d.Months != 0 {
		return d.String()
	}
	total := time.Duration(d.Days)*24*time.Hour +
		time.Duration(d.Hours)*time.Hour +
		time.Duration(d.Minutes)*time.Minute +
		time.Duration(d.Seconds)*time.Second
	if d.Neg {
		total = -total
	}
	return total
}

// Equal compares two FEEL values structurally. It is used by the ANY hit policy
// to decide whether multiple matching rules agree, and by trace deduplication.
func Equal(a, b any) bool {
	if IsNull(a) || IsNull(b) {
		return IsNull(a) && IsNull(b)
	}
	an, aok := a.(*pfeel.Number)
	bn, bok := b.(*pfeel.Number)
	if aok && bok {
		return an.Cmp(bn) == 0
	}
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			ov, ok := bv[k]
			if !ok || !Equal(v, ov) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !Equal(av[i], bv[i]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(a, b)
}

// Compare orders two FEEL values, reporting ok=false when they are not
// comparable. Numbers, strings, booleans and temporals are ordered; contexts
// and functions are not. COLLECT MIN/MAX and OUTPUT ORDER depend on it.
func Compare(a, b any) (int, bool) {
	an, aok := a.(*pfeel.Number)
	bn, bok := b.(*pfeel.Number)
	if aok && bok {
		return an.Cmp(bn), true
	}
	as, aok := a.(string)
	bs, bok := b.(string)
	if aok && bok {
		return strings.Compare(as, bs), true
	}
	ab, aok := a.(bool)
	bb, bok := b.(bool)
	if aok && bok {
		switch {
		case ab == bb:
			return 0, true
		case !ab:
			return -1, true
		default:
			return 1, true
		}
	}
	at, aok := timeOf(a)
	bt, bok := timeOf(b)
	if aok && bok {
		switch {
		case at.Before(bt):
			return -1, true
		case at.After(bt):
			return 1, true
		default:
			return 0, true
		}
	}
	return 0, false
}

func timeOf(v any) (time.Time, bool) {
	switch x := v.(type) {
	case *pfeel.FEELDate:
		return x.Date(), true
	case *pfeel.FEELTime:
		return x.Time(), true
	case *pfeel.FEELDatetime:
		return x.Time(), true
	case time.Time:
		return x, true
	}
	return time.Time{}, false
}

// SortValues sorts a list of FEEL values in ascending order, leaving values it
// cannot compare in their original relative position.
func SortValues(vs []any) {
	sort.SliceStable(vs, func(i, j int) bool {
		c, ok := Compare(vs[i], vs[j])
		return ok && c < 0
	})
}

// TypeName reports the FEEL type name of a value, for type-violation messages.
func TypeName(v any) string {
	switch v.(type) {
	case nil, *pfeel.NullValue:
		return "null"
	case *pfeel.Number:
		return "number"
	case string:
		return "string"
	case bool:
		return "boolean"
	case *pfeel.FEELDate:
		return "date"
	case *pfeel.FEELTime:
		return "time"
	case *pfeel.FEELDatetime:
		return "date and time"
	case *pfeel.FEELDuration:
		return "duration"
	case map[string]any:
		return "context"
	case []any:
		return "list"
	}
	return fmt.Sprintf("%T", v)
}

// datetimeFromGo builds a FEEL date-and-time from a Go time. The embedded
// library keeps its temporal fields unexported and offers only string parsing,
// so the conversion round-trips through the offset-bearing ISO-8601 form it
// accepts. Sub-second precision is not representable in that form and is
// dropped, which matches DMN's own second-resolution literal grammar.
func datetimeFromGo(t time.Time) any {
	dt, err := pfeel.ParseDatetime(t.Format("2006-01-02T15:04:05-07:00"))
	if err != nil {
		return Null
	}
	return dt
}

// Add sums two FEEL values. It exists for the COLLECT SUM aggregator, which
// needs arithmetic outside an expression context.
func Add(a, b any) (any, error) {
	an, aok := a.(*pfeel.Number)
	bn, bok := b.(*pfeel.Number)
	if !aok || !bok {
		return nil, fmt.Errorf("cannot add %s and %s", TypeName(a), TypeName(b))
	}
	return an.Add(bn), nil
}

// ParseTemporal parses a temporal literal into the requested FEEL type. It
// backs agent-response coercion, where a model answers a date-typed decision
// with a string.
func ParseTemporal(s, want string) (any, error) {
	switch want {
	case "date":
		return pfeel.ParseDate(s)
	case "time":
		return pfeel.ParseTime(s)
	case "date and time":
		return pfeel.ParseDatetime(s)
	case "days and time duration", "years and months duration":
		return pfeel.ParseDuration(s)
	default:
		v, err := pfeel.ParseTemporalValue(s)
		if err != nil {
			return nil, fmt.Errorf("%q is not a %s", s, want)
		}
		return v, nil
	}
}
