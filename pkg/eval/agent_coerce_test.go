package eval

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/frankbardon/verdict/pkg/dmn/model"
	"github.com/frankbardon/verdict/pkg/feel"
)

// describe renders a coerced value as "<FEEL type>:<lexical form>", recursing
// into lists and contexts with sorted keys, so a case pins both the kind the
// coercion produced and how it prints.
func describe(v any) string {
	switch x := v.(type) {
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = describe(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = k + ": " + describe(x[k])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case string:
		return fmt.Sprintf("string:%q", x)
	}
	if feel.IsNull(v) {
		return "null"
	}
	return feel.TypeName(v) + ":" + fmt.Sprint(v)
}

type point struct {
	X int     `json:"x"`
	Y float64 `json:"y"`
}

// TestCoerceToTypeCharacterization pins what coerceToType makes of every shape
// of Go answer a bridge can hand back, for every declared type path. It was
// written against the ToGo(FromGo(v)) round-trip coercion before it was
// removed (#69); the rows marked "Intended change" are the outputs that
// removal changed on purpose, and every other row held.
func TestCoerceToTypeCharacterization(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Skipf("no time zone database: %v", err)
	}
	parisTime := time.Date(2024, 7, 1, 9, 30, 0, 0, paris)
	utcTime := time.Date(2024, 7, 1, 9, 30, 0, 0, time.UTC)
	fixedTime := time.Date(2024, 7, 1, 9, 30, 0, 0, time.FixedZone("", 2*3600))

	num := model.TypeSpec{TypeRef: "number"}
	str := model.TypeSpec{TypeRef: "string"}
	boo := model.TypeSpec{TypeRef: "boolean"}
	date := model.TypeSpec{TypeRef: "date"}
	tim := model.TypeSpec{TypeRef: "time"}
	dt := model.TypeSpec{TypeRef: "dateTime"}
	dtd := model.TypeSpec{TypeRef: "dayTimeDuration"}
	ymd := model.TypeSpec{TypeRef: "yearMonthDuration"}
	anyT := model.TypeSpec{}
	custom := model.TypeSpec{TypeRef: "tRisk"}
	enum := model.TypeSpec{Enumeration: []string{"low", "1.5", "3", "true", "2024-07-01T09:30:00@Europe/Paris", "PT1H"}}
	list := func(s model.TypeSpec) model.TypeSpec { s.Collection = true; return s }
	obj := model.TypeSpec{Components: map[string]model.TypeSpec{"x": num, "y": num}}
	objX := model.TypeSpec{Components: map[string]model.TypeSpec{"x": num}}
	nested := model.TypeSpec{Components: map[string]model.TypeSpec{
		"p":    obj,
		"tags": list(str),
		"at":   dt,
	}}

	cases := []struct {
		name string
		v    any
		spec model.TypeSpec
		want string
	}{
		// Numbers.
		{"number/int", 42, num, "number:42"},
		{"number/int64", int64(-7), num, "number:-7"},
		{"number/uint8", uint8(9), num, "number:9"},
		{"number/float64", 2.5, num, "number:2.5"},
		{"number/float32", float32(0.5), num, "number:0.5"},
		{"number/json", json.Number("1.10"), num, "number:1.1"},
		{"number/string", " 3.25 ", num, "number:3.25"},
		{"number/bad string", "many", num, `error: "many" is not a number`},
		{"number/bool", true, num, "error: expected a number, got a boolean"},
		{"number/nil", nil, num, "error: expected a number, got null"},
		{"number/time", utcTime, num, "error: expected a number, got time.Time"},
		{"number/list", []any{1}, num, "error: expected a number, got []interface {}"},

		// Strings.
		{"string/string", "hi", str, `string:"hi"`},
		{"string/int", 1, str, "error: expected a string, got int"},
		{"string/nil", nil, str, "error: expected a string, got <nil>"},
		{"string/time", utcTime, str, "error: expected a string, got time.Time"},

		// Booleans.
		{"boolean/true", true, boo, "boolean:true"},
		{"boolean/yes", " Yes ", boo, "boolean:true"},
		{"boolean/no", "no", boo, "boolean:false"},
		{"boolean/bad string", "maybe", boo, `error: "maybe" is not a boolean`},
		{"boolean/int", 1, boo, "error: expected a boolean, got int"},
		{"boolean/nil", nil, boo, "error: expected a boolean, got <nil>"},

		// Temporals.
		{"date/string", "2024-07-01", date, "date:2024-07-01"},
		{"date/bad string", "July", date, "error: fail to parse temporal value"},
		{"date/time.Time", utcTime, date, "error: expected date, got time.Time"},
		{"time/string", "09:30:00Z", tim, "time:09:30:00Z"},
		{"time/time.Time", utcTime, tim, "error: expected time, got time.Time"},
		{"dateTime/string", "2024-07-01T09:30:00", dt, "date and time:2024-07-01T09:30:00"},
		{"dateTime/zoned string", "2024-07-01T09:30:00@Europe/Paris", dt, "date and time:2024-07-01T09:30:00@Europe/Paris"},
		{"dateTime/utc time", utcTime, dt, "date and time:2024-07-01T09:30:00Z"},
		{"dateTime/paris time", parisTime, dt, "date and time:2024-07-01T09:30:00@Europe/Paris"},
		{"dateTime/fixed offset time", fixedTime, dt, "date and time:2024-07-01T09:30:00+02:00"},
		{"dateTime/int", 5, dt, "error: expected date and time, got int"},
		{"dateTime/nil", nil, dt, "error: expected date and time, got <nil>"},
		{"dayTime/string", "P1DT2H", dtd, "duration:P1DT2H"},
		{"dayTime/go duration", 90 * time.Minute, dtd, "error: expected days and time duration, got time.Duration"},
		{"dayTime/year-month string", "P1Y", dtd, "duration:P1Y"},
		{"yearMonth/string", "P1Y2M", ymd, "duration:P1Y2M"},
		{"yearMonth/go duration", time.Hour, ymd, "error: expected years and months duration, got time.Duration"},

		// Untyped and user-defined types pass the value through FromGo.
		{"any/int", 3, anyT, "number:3"},
		{"any/nil", nil, anyT, "null"},
		{"any/time", parisTime, anyT, "date and time:2024-07-01T09:30:00@Europe/Paris"},
		{"any/struct", point{X: 1, Y: 2.5}, anyT, "{x: number:1, y: number:2.5}"},
		{"any/typed slice", []int{1, 2}, anyT, "[number:1, number:2]"},
		{"custom/map", map[string]any{"a": 1}, custom, "{a: number:1}"},

		// Enumerations.
		{"enum/exact", "low", enum, `string:"low"`},
		{"enum/case", "LOW", enum, `string:"low"`},
		{"enum/spaces", "  low ", enum, `string:"low"`},
		{"enum/outside", "high risk", enum, `error: "high risk" is not one of low, 1.5, 3, true, 2024-07-01T09:30:00@Europe/Paris, PT1H`},
		{"enum/int", 3, enum, `string:"3"`},
		{"enum/float", 1.5, enum, `string:"1.5"`},
		{"enum/json number", json.Number("1.50"), enum, `string:"1.5"`},
		{"enum/bool", true, enum, `string:"true"`},
		{"enum/paris time", parisTime, enum, `string:"2024-07-01T09:30:00@Europe/Paris"`},
		{"enum/go duration", time.Hour, enum, `string:"PT1H"`},
		{"enum/small float", 0.1, enum, `error: "0.1" is not one of low, 1.5, 3, true, 2024-07-01T09:30:00@Europe/Paris, PT1H`},
		{"enum/large float", 1e21, enum, `error: "1e+21" is not one of low, 1.5, 3, true, 2024-07-01T09:30:00@Europe/Paris, PT1H`},
		{"enum/nil", nil, enum, `error: "<nil>" is not one of low, 1.5, 3, true, 2024-07-01T09:30:00@Europe/Paris, PT1H`},

		// Lists.
		{"list/any slice", []any{1, "2", 3.5}, list(num), "[number:1, number:2, number:3.5]"},
		{"list/int slice", []int{1, 2}, list(num), "[number:1, number:2]"},
		{"list/string slice", []string{"a", "b"}, list(str), `[string:"a", string:"b"]`},
		{"list/string slice as numbers", []string{"1", "2"}, list(num), "[number:1, number:2]"},
		// Intended change (#69): the exact decimal is kept, no longer narrowed
		// through float64 (was 1.100000000000000088817841970012523).
		{"list/json numbers", []json.Number{"1.10", "2"}, list(num), `[number:1.1, number:2]`},
		{"list/bool slice", []bool{true, false}, list(boo), "[boolean:true, boolean:false]"},
		{"list/array", [2]int{4, 5}, list(num), "[number:4, number:5]"},
		{"list/time slice", []time.Time{utcTime, parisTime}, list(dt), "[date and time:2024-07-01T09:30:00Z, date and time:2024-07-01T09:30:00@Europe/Paris]"},
		// Intended change (#69): rejected, as a top-level time.Time is for a
		// string (was accepted as its lexical string).
		{"list/time slice as strings", []time.Time{utcTime}, list(str), `error: element 1: expected a string, got *dialect.FEELDatetime`},
		// Intended change (#69): message text only (was "fail to parse
		// temporal value").
		{"list/time slice as dates", []time.Time{utcTime}, list(date), "error: element 1: expected date, got *dialect.FEELDatetime"},
		// Intended change (#69): rejected, as a top-level time.Duration is
		// (was accepted through its lexical string).
		{"list/duration slice", []time.Duration{time.Hour}, list(dtd), "error: element 1: expected days and time duration, got *dialect.FEELDuration"},
		{"list/enum slice", []string{"LOW", "low"}, list(enum), `[string:"low", string:"low"]`},
		{"list/int slice as enum", []int{3}, list(enum), `[string:"3"]`},
		{"list/bad element", []any{1, "x"}, list(num), `error: element 2: "x" is not a number`},
		{"list/empty typed", []int{}, list(num), "[]"},
		{"list/nil", nil, list(num), "error: expected a list, got <nil>"},
		{"list/scalar", 3, list(num), "error: expected a list, got int"},
		{"list/string", "[1,2]", list(num), "error: expected a list, got string"},
		{"list/nested slice", [][]int{{1}, {2, 3}}, list(num), "error: element 1: expected a number, got []interface {}"},
		{"list/untyped", []int{1, 2}, list(anyT), "[number:1, number:2]"},
		{"list/struct slice", []point{{X: 1, Y: 2}}, list(obj), "[{x: number:1, y: number:2}]"},

		// Contexts.
		{"object/map", map[string]any{"x": 1, "y": "2"}, obj, "{x: number:1, y: number:2}"},
		{"object/json string", `{"x": 1, "y": 2.5}`, obj, "{x: number:1, y: number:2.5}"},
		{"object/bad json string", `{"x": `, objX, "error: expected an object with fields x, got string"},
		{"object/typed map", map[string]int{"x": 1, "y": 2}, obj, "{x: number:1, y: number:2}"},
		{"object/struct", point{X: 3, Y: 4.5}, obj, "{x: number:3, y: number:4.5}"},
		{"object/struct pointer", &point{X: 3, Y: 4.5}, obj, "{x: number:3, y: number:4.5}"},
		{"object/nil pointer", (*point)(nil), objX, "error: expected an object with fields x, got *eval.point"},
		{"object/missing field", map[string]any{"x": 1}, obj, `error: missing field "y"`},
		{"object/extra field dropped", map[string]any{"x": 1, "y": 2, "z": 3}, obj, "{x: number:1, y: number:2}"},
		{"object/nil", nil, objX, "error: expected an object with fields x, got <nil>"},
		{"object/scalar", 7, objX, "error: expected an object with fields x, got int"},
		{"object/list", []any{1}, objX, "error: expected an object with fields x, got []interface {}"},
		{"object/multi-field scalar", 7, obj, "error: expected an object with fields x, y, got int"},
		{"object/multi-field nil", nil, nested, "error: expected an object with fields at, p, tags, got <nil>"},
		{"object/multi-field all missing", map[string]any{}, obj, `error: missing field "x"`},
		{"object/multi-field first invalid", map[string]any{"x": "a", "y": "b"}, obj, `error: field "x": "a" is not a number`},
		{"object/nested", map[string]any{
			"p":    map[string]int{"x": 1, "y": 2},
			"tags": []string{"a"},
			"at":   parisTime,
		}, nested, `{at: date and time:2024-07-01T09:30:00@Europe/Paris, p: {x: number:1, y: number:2}, tags: [string:"a"]}`},
		{"object/nested typed", map[string]any{
			"p":    point{X: 1, Y: 2},
			"tags": []any{"a", "b"},
			"at":   "2024-07-01T09:30:00Z",
		}, nested, `{at: date and time:2024-07-01T09:30:00Z, p: {x: number:1, y: number:2}, tags: [string:"a", string:"b"]}`},
		// Intended change (#69): message text only (was the lexical string
		// "... is not a number").
		{"object/map of times", map[string]time.Time{"x": utcTime}, objX, `error: field "x": expected a number, got *dialect.FEELDatetime`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := coerceToType(c.v, c.spec)
			var desc string
			if err != nil {
				desc = "error: " + err.Error()
			} else {
				desc = describe(got)
			}
			if desc != c.want {
				t.Errorf("coerceToType(%#v) = %s, want %s", c.v, desc, c.want)
			}
		})
	}
}
