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

// ToGo converts a FEEL value into the plain Go value Verdict hands to the
// outside world: Result.Outputs, every trace node, an agent's Request.Inputs,
// and so every JSON surface built on them (MCP, Twirp, the CLI, Nexus). It is
// the one boundary conversion, so every one of those surfaces shows a value
// the same way.
//
// Numbers become float64. Every temporal and both duration kinds become their
// FEEL lexical string, so the value says what kind it is and keeps its
// zone-ness: a date is "2024-03-05", a time "10:00:00" (or "10:00:00Z",
// "10:00:00+02:00"), a date and time "2024-03-05T10:00:00" when zoneless and
// "...Z", "...+02:00" or "...@Europe/Paris" when zoned, and a duration
// "P1DT2H" or "P1Y2M". Each string parses back to the same FEEL value through
// date(), time(), date and time() or duration(). Null becomes nil, and contexts
// and lists are converted element-wise.
//
// FEEL numbers carry more precision than a float64 can hold. ToGo is the
// interop boundary, not the arithmetic one — every calculation happens in full
// precision inside the evaluator and only the final value is narrowed here.
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
		if x == nil {
			return nil
		}
		return x.String()
	case *pfeel.FEELTime:
		if x == nil {
			return nil
		}
		return x.String()
	case *pfeel.FEELDatetime:
		if x == nil {
			return nil
		}
		return x.String()
	case *pfeel.FEELDuration:
		if x == nil {
			return nil
		}
		return x.String()
	case map[string]any:
		return contextToGo(x)
	case pfeel.Scope:
		// A context built by the evaluator (a context literal, a boxed
		// context) is the fork's named map type; it is the same FEEL context
		// and must not reach the boundary unconverted.
		return contextToGo(x)
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

func contextToGo(x map[string]any) map[string]any {
	out := make(map[string]any, len(x))
	for k, e := range x {
		out[k] = ToGo(e)
	}
	return out
}

// KeyOf renders v as a string that identifies it as a FEEL value: two values
// get the same key only if they are the same kind and print the same lexical
// form. Every scalar is tagged with its kind, so a string never meets a number
// or a temporal that prints alike, a date never meets the midnight date and
// time of that day, and a zoneless date and time never meets the same wall
// clock at UTC (its lexical form has no `Z`). Numbers use their exact decimal
// form. Values with no deterministic rendering (functions, unknown Go types)
// report ok=false, so a caller using the key as a cache key can decline to
// cache rather than risk a wrong hit.
//
// Two equal FEEL values can still get different keys (`1` and `1.0`, the same
// instant at two offsets); that costs a cache miss, never a wrong answer.
func KeyOf(v any) (string, bool) {
	var b strings.Builder
	if !writeKey(&b, v) {
		return "", false
	}
	return b.String(), true
}

func writeKey(b *strings.Builder, v any) bool {
	quote := func(tag, s string) {
		b.WriteString(tag)
		b.WriteByte(':')
		b.WriteString(fmt.Sprintf("%q", s))
	}
	switch x := v.(type) {
	case nil, *pfeel.NullValue:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		quote("s", x)
	case *pfeel.Number:
		if x == nil {
			b.WriteString("null")
			return true
		}
		quote("n", x.String())
	case *pfeel.FEELDate:
		if x == nil {
			return false
		}
		quote("date", x.String())
	case *pfeel.FEELTime:
		if x == nil {
			return false
		}
		quote("time", x.String())
	case *pfeel.FEELDatetime:
		if x == nil {
			return false
		}
		quote("dt", x.String())
	case *pfeel.FEELDuration:
		if x == nil {
			return false
		}
		if x.IsYearsMonths() {
			quote("ymd", x.String())
		} else {
			quote("dtd", x.String())
		}
	case *pfeel.RangeValue:
		if x == nil {
			return false
		}
		b.WriteString("range(")
		if x.StartOpen {
			b.WriteByte('(')
		} else {
			b.WriteByte('[')
		}
		if !writeKey(b, x.Start) {
			return false
		}
		b.WriteString("..")
		if !writeKey(b, x.End) {
			return false
		}
		if x.EndOpen {
			b.WriteByte(')')
		} else {
			b.WriteByte(']')
		}
		b.WriteByte(')')
	case pfeel.Scope:
		// The fork keeps contexts as map[string]any (issue #67); a Scope here
		// is a leak, but it is the same context and keys the same way.
		return writeKey(b, map[string]any(x))
	case map[string]any:
		names := make([]string, 0, len(x))
		for k := range x {
			names = append(names, k)
		}
		sort.Strings(names)
		b.WriteByte('{')
		for i, k := range names {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(fmt.Sprintf("%q", k))
			b.WriteByte(':')
			if !writeKey(b, x[k]) {
				return false
			}
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			if !writeKey(b, e) {
				return false
			}
		}
		b.WriteByte(']')
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64,
		float32, float64, json.Number, time.Time, time.Duration:
		// A plain Go scalar is keyed as the FEEL value it converts to.
		return writeKey(b, FromGo(x))
	default:
		// Functions and anything else have no rendering the key can trust.
		return false
	}
	return true
}

// Equal reports whether a = b is true in FEEL (DMN 1.5 §10.3.2.15, Tables 52
// and 53). It is the fork's `=` with a null verdict folded to false, so it
// cannot disagree with an `=` in an expression: 1 and 1.00 are equal, as are
// two temporals denoting the same instant and PT60M and PT1H, while values of
// different kinds are not. Two nulls are equal. The ANY hit policy, PRIORITY
// and OUTPUT ORDER ranking, `duplicate values` and the analyser use it.
func Equal(a, b any) bool {
	eq, _ := equal3(a, b)
	return eq
}

// equal3 is FEEL's three-valued `=`: known is false when the comparison is
// null — operands of different kinds, or a list or context pair that is.
func equal3(a, b any) (eq, known bool) {
	switch r := pfeel.Equal(a, b).(type) {
	case bool:
		return r, true
	default:
		return false, false
	}
}

// Compare orders two FEEL values as FEEL's `<` does, reporting ok=false when
// FEEL defines no order between them. Numbers, strings, durations of one kind,
// times, and dates with date and times are ordered (DMN 1.5 Table 54);
// booleans, lists, contexts, ranges and functions are not, nor is any pair of
// different kinds or a null operand (§10.3.2.15). This is the ordering FEEL's
// min and max use, so COLLECT MIN/MAX agree with them, and the analyser picks
// its witnesses by it too.
func Compare(a, b any) (int, bool) {
	if IsNull(a) || IsNull(b) {
		return 0, false
	}
	if at, ok := a.(time.Time); ok {
		// A raw Go time is not a FEEL value, but callers outside the
		// evaluator may still hand one over; order two of them by instant.
		if bt, ok := b.(time.Time); ok {
			return at.Compare(bt), true
		}
		return 0, false
	}
	return pfeel.OrderValues(a, b)
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
	case map[string]any, pfeel.Scope:
		return "context"
	case []any:
		return "list"
	}
	return fmt.Sprintf("%T", v)
}

// datetimeFromGo builds a FEEL date-and-time from a Go time. The embedded
// library keeps its temporal fields unexported and offers only string parsing,
// so the conversion round-trips through the ISO-8601 form it accepts. The
// fraction of a second is kept to the nanosecond, as a FEEL date and time holds
// it, so a sub-second input is not silently truncated. The result is always
// zoned: a time.Time has no zoneless form.
//
// A time whose Location is a real IANA zone keeps it as a zone id
// (`...@Europe/Paris`, DMN 1.5 §10.3.2.3.5 and Table 72), so `.timezone` reads
// it back; any other time carries its offset (`Z`, `+01:00`). See ianaZone for
// what counts as real.
func datetimeFromGo(t time.Time) any {
	const local = "2006-01-02T15:04:05.999999999"
	s := t.Format(local + "-07:00")
	if id := ianaZone(t); id != "" {
		s = t.Format(local) + "@" + id
	}
	dt, err := pfeel.ParseDatetime(s)
	if err != nil {
		return Null
	}
	return dt
}

// ianaZone is the IANA zone id t's Location names, or "" when writing t with
// that id would not give back t's instant and offset. A Location's name is
// whatever its constructor was given, so the name alone proves nothing:
//
//   - "UTC" stays the offset form, so the common case prints `Z`, not `@UTC`;
//     "Local" names the host's zone, which a decision must not depend on (the
//     FEEL parser rejects it too); "" is an unnamed fixed zone.
//   - The name must load from the time zone database, which rejects names such
//     as `+01:00` given to time.FixedZone.
//   - t's wall clock placed in the loaded zone must be t's instant. That
//     rejects a fixed zone borrowing a real zone's name (FixedZone("CET", 3600)
//     in July, when real CET is +02:00), and the one occurrence of a wall
//     clock time repeated when DST ends that the zone id would not name (the
//     FEEL parser places `local@zone` with time.Date, as this check does, so
//     the id reads as the other occurrence) — there only the offset is
//     lossless.
func ianaZone(t time.Time) string {
	name := t.Location().String()
	switch name {
	case "", "UTC", "Local":
		return ""
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return ""
	}
	y, mo, d := t.Date()
	h, mi, sec := t.Clock()
	if !time.Date(y, mo, d, h, mi, sec, t.Nanosecond(), loc).Equal(t) {
		return ""
	}
	return name
}

// Aggregate applies the DMN 1.5 §10.3.4.4 list aggregate name (count, min,
// max, sum, product, mean, median, stddev or mode) to a list of FEEL values,
// exactly as the FEEL built-in of that name does: a list outside the
// function's domain gives Null, never an error. ok is false only for an
// unknown name. It is the door a decision table's COLLECT aggregation, which
// §10.3.2.10 defines through these built-ins, uses into them.
func Aggregate(name string, list []any) (result any, ok bool) {
	return pfeel.Aggregate(name, list)
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
