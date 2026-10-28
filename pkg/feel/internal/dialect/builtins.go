package dialect

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mitchellh/mapstructure"
)

func toFEELIndex(idx int) int {
	return idx + 1
}

func fromFEELIndex(idx int) int {
	return idx - 1
}

// getContextMap takes any input type and puts each value in the output map.
// If the input is a struct it will use the json tag if available, else the field name will remain as defined on the struct
// If the input is a map, each key value of the map will be copied to the output map
func getContextMap(input any, output map[string]any) error {
	if output == nil {
		return errors.New("output map cannot be nil")
	}

	v := reflect.ValueOf(input)
	if !v.IsValid() {
		return errors.New("input is invalid")
	}

	// Dereference pointer if needed
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return errors.New("input is a nil pointer")
		}
		v = v.Elem()
	}

	if v.Kind() == reflect.Map {
		// Add all key-value pairs from the input map to the output map
		for iter := v.MapRange(); iter.Next(); {
			key := iter.Key()
			value := iter.Value()
			if keyStr, ok := key.Interface().(string); ok {
				output[keyStr] = value.Interface()
			}
		}
	}
	if v.Kind() == reflect.Struct {

		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			fieldType := t.Field(i)
			if fieldType.PkgPath != "" {
				continue // skip unexported fields
			}

			fieldVal := v.Field(i)
			if !fieldVal.CanInterface() {
				continue // skip if value can't be interfaced
			}

			// Use json tag if available
			jsonTag := fieldType.Tag.Get("json")
			if jsonTag == "-" {
				continue // skip fields explicitly ignored
			}

			key := fieldType.Name
			if jsonTag != "" {
				// Handle tag options like "name,omitempty"
				if commaIdx := strings.Index(jsonTag, ","); commaIdx != -1 {
					key = jsonTag[:commaIdx]
				} else {
					key = jsonTag
				}
			}

			output[key] = fieldVal.Interface()
		}
	}

	return nil
}

func decodeKWArgs(input map[string]any, output any) error {
	config := &mapstructure.DecoderConfig{
		Metadata: nil,
		TagName:  "json",
		Result:   &output,
	}
	decoder, err := mapstructure.NewDecoder(config)
	if err != nil {
		return err
	}
	return decoder.Decode(input)
}

func extractList(args map[string]any, argName string) ([]any, error) {
	if v, ok := args[argName]; ok {
		if listV, ok := v.([]any); ok {
			if len(listV) == 1 {
				elem := listV[0]
				rv := reflect.ValueOf(elem)
				if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
					result := make([]any, rv.Len())
					for i := 0; i < rv.Len(); i++ {
						result[i] = rv.Index(i).Interface()
					}
					return result, nil
				}
			}
			return listV, nil
		} else {
			return nil, NewErrTypeMismatch("list")
		}
	} else {
		return nil, NewErrKeyNotFound(argName)
	}
}

// booleanItems binds the `list` parameter of all/any (and the fork's
// and(list)/or(list)): DMN 1.5 §10.3.4.4 gives them the domain "list of
// Boolean items or argument list of one or more Boolean items". A positional
// call collects its arguments as the list, unless it passed one list; a named
// `list:` that is not a list is a singleton list (§10.3.2.9.4). ok is false
// — the call is null — for a missing or null list, or an item that is neither
// a boolean nor null.
//
// VERDICT DIVERGENCE: the domain is checked before any item decides the
// result, so `all([false, 123])` is null, not false. Interpretation: the TCK
// (0059, 0060) only puts a non-boolean beside undecisive items; §10.3.4 makes
// a parameter outside its domain null whatever else the list holds.
func booleanItems(args map[string]any) (items []any, ok bool) {
	v, present := args["list"]
	if !present || isNullValue(v) {
		return nil, false
	}
	items, isList := v.([]any)
	switch {
	case !isList:
		items = []any{v}
	case len(items) == 1:
		if inner, nested := items[0].([]any); nested {
			items = inner
		}
	}
	for _, item := range items {
		if isNullValue(item) {
			continue
		}
		if _, isBool := item.(bool); !isBool {
			return nil, false
		}
	}
	return items, true
}

// allItems is all(list): false if any item is false, else true if every item
// is true (so for an empty list), else null — a null item is unknown, never
// falsy. DMN 1.5 §10.3.4.4: `all([false,null,true]) = false`, `all([]) =
// true`, `all(0) = null`.
func allItems(args map[string]any) (any, error) {
	items, ok := booleanItems(args)
	if !ok {
		return null, nil
	}
	unknown := false
	for _, item := range items {
		switch item {
		case false:
			return false, nil
		case true:
		default:
			unknown = true
		}
	}
	if unknown {
		return null, nil
	}
	return true, nil
}

// anyItems is any(list): true if any item is true, else false if every item
// is false (so for an empty list), else null. DMN 1.5 §10.3.4.4:
// `any([false,null,true]) = true`, `any([]) = false`, `any(0) = null`.
func anyItems(args map[string]any) (any, error) {
	items, ok := booleanItems(args)
	if !ok {
		return null, nil
	}
	unknown := false
	for _, item := range items {
		switch item {
		case true:
			return true, nil
		case false:
		default:
			unknown = true
		}
	}
	if unknown {
		return null, nil
	}
	return false, nil
}

// separatedNumber rewrites s, written with the given separators, as a plain
// numeric literal: number(from, grouping separator, decimal separator), DMN
// 1.5 §10.3.4.1 Table 72. grouping is " ", "," or "." and decimal "." or ",",
// and they must differ; either may be null — no grouping separator, and the
// default decimal point "." — and anything else is outside the domain.
//
// VERDICT DIVERGENCE: upstream had only number(from). Table 72 does not say
// what a null separator means; the fork reads it as the one-argument form
// does, so number("1.000", ".", null) is null (grouping and decimal clash).
func separatedNumber(s string, grouping, decimal any) (string, bool) {
	g, d := "", "."
	switch v := grouping.(type) {
	case *NullValue, nil:
	case string:
		if v != " " && v != "," && v != "." {
			return "", false
		}
		g = v
	default:
		return "", false
	}
	switch v := decimal.(type) {
	case *NullValue, nil:
	case string:
		if v != "." && v != "," {
			return "", false
		}
		d = v
	default:
		return "", false
	}
	if g == d {
		return "", false
	}
	if g != "" {
		s = strings.ReplaceAll(s, g, "")
	}
	if d != "." && strings.Contains(s, ".") {
		return "", false
	}
	return strings.ReplaceAll(s, d, "."), true
}

// numericLiteral is DMN 1.5 grammar rule 35, the domain of number(from).
// Table 72's footnote cites "grammar rule 37", which is digits; and rule 35's
// parentheses do not close, so the exponent is read as applying to either
// mantissa form.
var numericLiteral = regexp.MustCompile(`^-?(\d+(\.\d+)?|\.\d+)([eE][+-]?\d+)?$`)

func installBuiltinFunctions(prelude *Prelude) {
	// conversion functions
	// VERDICT DIVERGENCE: string(null) is null rather than the Go rendering of
	// the null value ("&{}"). DMN 1.5 §10.3.4.1 Table 72: string(from) takes a
	// non-null from, with string(null) = null as its example. Every other
	// value is rendered in FEEL form (see feelString) rather than through
	// fmt's %s, which printed `%!s(bool=true)` and `[1 2]`.
	prelude.Bind("string", NewNativeFunc(func(kwargs map[string]any) (any, error) {
		v := kwargs["from"]
		if _, isNull := v.(*NullValue); isNull || v == nil {
			return null, nil
		}
		if s, ok := v.(string); ok {
			return s, nil
		}
		return feelString(v), nil
	}).Required("from"))

	// VERDICT DIVERGENCE: number yields null for a string that is not a
	// numeric literal and for an argument it cannot read, instead of 0 (the
	// big.Float that SetString left untouched) or an error. DMN 1.5 §10.3.4.1
	// Table 72: from must conform to the numeric-literal grammar, and §10.3.4
	// makes a parameter outside its domain null. A from that is not a string
	// — number(5), number(true) — is null too, in both overloads; upstream
	// passed a number through.
	prelude.Bind("number", NewNativeFunc(func(kwargs map[string]any) (any, error) {
		s, ok := kwargs["from"].(string)
		if !ok {
			return null, nil
		}
		if _, ok := kwargs["decimal separator"]; ok {
			if s, ok = separatedNumber(s, kwargs["grouping separator"], kwargs["decimal separator"]); !ok {
				return null, nil
			}
		}
		if !numericLiteral.MatchString(s) {
			return null, nil
		}
		n, err := ParseNumberWithErr(s)
		if err != nil {
			return null, nil
		}
		return n, nil
	}).Overloads([]string{"from"}, []string{"from", "grouping separator", "decimal separator"}))

	// boolean functions
	// VERDICT DIVERGENCE: upstream negated boolValue(v), so `not(null)` and
	// `not(0)` were true. DMN 1.5 §10.3.2.15 Table 51 negates a boolean and
	// makes everything else null.
	prelude.Bind("not", NewNativeFunc(func(args map[string]any) (any, error) {
		if b, ok := args["from"].(bool); ok {
			return !b, nil
		}
		return null, nil
	}).Required("from"))

	prelude.Bind("is defined", NewMacro(func(intp *Interpreter, args map[string]Node, varArgs []Node) (any, error) {
		if varNode, ok := args["value"].(*Var); ok {
			if _, ok := intp.Resolve(varNode.Name); !ok {
				return false, nil
			}
		} else {
			_, err := args["value"].Eval(intp)
			if err != nil {
				var evalErr *EvalError
				if errors.As(err, &evalErr) {
					if evalErr.Short == "key not found" || evalErr.Short == "index error" {
						return false, nil
					}
				}
				return false, err
			}
		}
		// TODO: more condition tests
		return true, nil
	}).Required("value"))

	// string functions
	// VERDICT DIVERGENCE: upstream returned len(s), the number of UTF-8
	// bytes. DMN 1.5 §10.3.4.3 Table 73: string length is the number of
	// characters, and a character is a code point (footnote 1), so
	// string length("🐎ab") is 3.
	prelude.Bind("string length", wrapTyped(func(s string) (int, error) {
		return utf8.RuneCountInString(s), nil
	}).Required("string"))

	// VERDICT DIVERGENCE: upstream decoded its arguments through mapstructure,
	// raising on a null or non-string string or a non-number position;
	// indexed bytes rather than characters; and read a negative start
	// position as a byte index before the string. DMN 1.5 §10.3.4.3 Table
	// 73, footnote 1: start position is a non-zero integer in [-L..L] (a
	// negative one counts from the end), length an integer in [1..E] where E
	// is the number of characters from the start position to the end, and a
	// position is a character (code point); §10.3.4 makes anything outside
	// that null.
	prelude.Bind("substring", NewNativeFunc(func(kwargs map[string]any) (any, error) {
		str, ok := kwargs["string"].(string)
		if !ok {
			return null, nil
		}
		runes := []rune(str)
		start, ok := position(kwargs["start position"], len(runes))
		if !ok {
			return null, nil
		}
		end := len(runes)
		if l, present := kwargs["length"]; present {
			n, ok := intArg(l)
			if !ok || n < 1 || n > len(runes)-start {
				return null, nil
			}
			end = start + n
		}
		return string(runes[start:end]), nil
	}).Required("string", "start position").Optional("length"))

	prelude.Bind("upper case", wrapTyped(func(s string) (string, error) {
		return strings.ToUpper(s), nil
	}).Required("string"))

	prelude.Bind("lower case", wrapTyped(func(s string) (string, error) {
		return strings.ToLower(s), nil
	}).Required("string"))

	prelude.Bind("contains", wrapTyped(func(s string, match string) (bool, error) {
		return strings.Contains(s, match), nil
	}).Required("string", "match"))

	prelude.Bind("starts with", wrapTyped(func(s string, match string) (bool, error) {
		return strings.HasPrefix(s, match), nil
	}).Required("string", "match"))

	prelude.Bind("ends with", wrapTyped(func(s string, match string) (bool, error) {
		return strings.HasSuffix(s, match), nil
	}).Required("string", "match"))

	// list functions
	prelude.Bind("list contains", wrapTyped(func(list []any, elem any) (bool, error) {
		for _, entry := range list {
			if valuesEqual(entry, elem) {
				return true, nil
			}
		}
		return false, nil
	}).Required("list", "element"))

	prelude.Bind("count", NewNativeFunc(func(args map[string]any) (any, error) {
		list, err := extractList(args, "list")
		if err != nil {
			return nil, err
		}
		return len(list), nil
	}).Vararg("list"))

	prelude.Bind("min", NewNativeFunc(func(args map[string]any) (any, error) {
		list, err := extractList(args, "list")
		if err != nil {
			return nil, err
		}
		return extremum(list, 1), nil
	}).Vararg("list"))

	prelude.Bind("max", NewNativeFunc(func(args map[string]any) (any, error) {
		list, err := extractList(args, "list")
		if err != nil {
			return nil, err
		}
		return extremum(list, -1), nil
	}).Vararg("list"))

	prelude.Bind("sum", NewNativeFunc(func(args map[string]any) (any, error) {
		list, err := extractList(args, "list")
		if err != nil {
			return nil, err
		}
		sum := Zero
		for _, entry := range list {
			if numEntry, ok := entry.(*Number); ok {
				sum = sum.Add(numEntry)
			}
		}
		return sum, nil
	}).Vararg("list"))

	prelude.Bind("product", NewNativeFunc(func(args map[string]any) (any, error) {
		list, err := extractList(args, "list")
		if err != nil {
			return nil, err
		}
		sum := N(1)
		for _, entry := range list {
			if numEntry, ok := entry.(*Number); ok {
				sum = sum.Mul(numEntry)
			}
		}
		return sum, nil
	}).Vararg("list"))

	prelude.Bind("mean", NewNativeFunc(func(args map[string]any) (any, error) {
		list, err := extractList(args, "list")
		if err != nil {
			return nil, err
		}
		sum := Zero
		cnt := 0
		for _, entry := range list {
			if numEntry, ok := entry.(*Number); ok {
				sum = sum.Add(numEntry)
				cnt++
			}
		}
		r := sum.FloatDiv(N(cnt))
		return r, nil
	}).Vararg("list"))

	prelude.Bind("stddev", NewNativeFunc(func(args map[string]any) (any, error) {
		list, err := extractList(args, "list")
		if err != nil {
			return nil, err
		}
		sum := 0.0
		cnt := 0
		for _, entry := range list {
			if numEntry, ok := entry.(*Number); ok {
				//sum = sum.Add(numEntry)
				sum += numEntry.Float64()
				cnt++
			}
		}
		if cnt == 0 {
			return 0, nil
		}

		avg := sum / float64(cnt)

		dev := 0.0
		for _, entry := range list {
			if numEntry, ok := entry.(*Number); ok {
				n := numEntry.Float64()
				dev += (n - avg) * (n - avg)
			}
		}

		return math.Sqrt(dev / float64(cnt)), nil
	}).Vararg("list"))

	prelude.Bind("median", NewNativeFunc(func(args map[string]any) (any, error) {
		list, err := extractList(args, "list")
		if err != nil {
			return nil, err
		}
		var numberList []*Number

		for _, entry := range list {
			if numEntry, ok := entry.(*Number); ok {
				numberList = append(numberList, numEntry)
			}
		}
		if len(numberList) == 0 {
			return nil, nil
		} else if len(numberList) == 1 {
			return numberList[0], nil
		}

		sort.Slice(numberList, func(i, j int) bool {
			return numberList[i].Compare(*numberList[j]) == -1
		})

		if len(numberList)%2 == 1 {
			return numberList[len(numberList)/2], nil
		} else {
			medPos := (len(numberList) / 2)
			return numberList[medPos+1].Add(numberList[medPos]).Mul(N("0.5")), nil
		}
	}).Vararg("list"))

	// VERDICT DIVERGENCE: upstream coerced each item through boolValue, so
	// `all([true, null])` was false and `any([123])` true, and raised on a
	// null list. See allItems/anyItems.
	prelude.Bind("all", NewNativeFunc(allItems).Vararg("list"))
	prelude.Bind("and", NewNativeFunc(allItems).Vararg("list"))
	prelude.Bind("any", NewNativeFunc(anyItems).Vararg("list"))
	prelude.Bind("or", NewNativeFunc(anyItems).Vararg("list"))

	// VERDICT DIVERGENCE: upstream decoded its arguments through mapstructure
	// (raising on a null list or position), returned "" for a start past the
	// end and sliced from a negative index (a panic). DMN 1.5 §10.3.4.4 Table
	// 76: a list (a non-list item is the singleton list §10.3.2.9.4 converts
	// it to), a start position counted from 1 or, if negative, from the end,
	// and an optional length; §10.3.4 makes a position or length outside the
	// list null.
	prelude.Bind("sublist", NewNativeFunc(func(kwargs map[string]any) (any, error) {
		list, ok := listArg(kwargs["list"])
		if !ok {
			return null, nil
		}
		start, ok := position(kwargs["start position"], len(list))
		if !ok {
			return null, nil
		}
		end := len(list)
		if l, present := kwargs["length"]; present {
			n, ok := intArg(l)
			if !ok || n < 0 || n > len(list)-start {
				return null, nil
			}
			end = start + n
		}
		return append([]any{}, list[start:end]...), nil
	}).Required("list", "start position").Optional("length"))

	// VERDICT DIVERGENCE: upstream decoded its arguments through mapstructure,
	// raising on a null list. DMN 1.5 §10.3.4.4: list is a list (a non-list
	// item converted to a singleton list, §10.3.2.9.4; null outside the
	// domain), and each item is "any element including null".
	prelude.Bind("append", NewNativeFunc(func(kwargs map[string]any) (any, error) {
		list, ok := listArg(kwargs["list"])
		if !ok {
			return null, nil
		}
		out := append([]any{}, list...)
		switch items := kwargs["items"].(type) {
		case nil:
		case []any:
			out = append(out, items...)
		default:
			out = append(out, items)
		}
		return out, nil
	}).Required("list").Vararg("items"))

	// VERDICT DIVERGENCE: upstream decoded its arguments through mapstructure,
	// raising on a null argument. DMN 1.5 §10.3.4.4: each argument is a list
	// (a non-list item converted to a singleton list, §10.3.2.9.4); a null one
	// is outside the domain, so the result is null.
	prelude.Bind("concatenate", NewNativeFunc(func(kwargs map[string]any) (any, error) {
		lists, ok := listsArg(kwargs["lists"])
		if !ok {
			return null, nil
		}
		results := make([]any, 0)
		for _, list := range lists {
			results = append(results, list...)
		}
		return results, nil
	}).Vararg("lists"))

	// VERDICT DIVERGENCE: upstream sliced at the position unchecked, so a
	// position of 0 or below -1 panicked and a negative one inserted at the
	// wrong place, and clamped a position past the end to an append. DMN 1.5
	// §10.3.4.4: position counts from 1, or from the end if negative; one
	// outside the list is outside the domain, so the result is null. newItem
	// is "any element including null".
	prelude.Bind("insert before", wrapTyped(func(list []any, pos *Number, newItem any) (any, error) {
		idx, ok := position(pos, len(list))
		if !ok {
			return null, nil
		}
		newList := append([]any{}, list[:idx]...)
		newList = append(newList, newItem)
		return append(newList, list[idx:]...), nil
	}).Required("list", "position", "newItem"))

	// VERDICT DIVERGENCE: as insert before — upstream panicked on a position
	// of 0 or a negative one, and on one past the end.
	prelude.Bind("remove", wrapTyped(func(list []any, pos *Number) (any, error) {
		idx, ok := position(pos, len(list))
		if !ok {
			return null, nil
		}
		newList := append([]any{}, list[:idx]...)
		return append(newList, list[idx+1:]...), nil
	}).Required("list", "position"))

	prelude.Bind("reverse", wrapTyped(func(list []any) ([]any, error) {
		var reversed []any
		for i := len(list) - 1; i >= 0; i-- {
			reversed = append(reversed, list[i])
		}
		return reversed, nil
	}).Required("list"))

	prelude.Bind("index of", wrapTyped(func(list []any, match any) ([]any, error) {
		matched := make([]any, 0)
		for i, elem := range list {
			if valuesEqual(elem, match) {
				matched = append(matched, N(toFEELIndex(i)))
			}
		}
		return matched, nil
	}).Required("list", "match"))

	// VERDICT DIVERGENCE: as concatenate — a null argument is null, a non-list
	// one a singleton list, where upstream raised.
	prelude.Bind("union", NewNativeFunc(func(kwargs map[string]any) (any, error) {
		lists, ok := listsArg(kwargs["lists"])
		if !ok {
			return null, nil
		}
		elemSet := make([]any, 0)
		for _, list := range lists {
			for _, elem := range list {
				found := false
				for _, setElem := range elemSet {
					if valuesEqual(elem, setElem) {
						found = true
						break
					}
				}
				if !found {
					elemSet = append(elemSet, elem)
				}
			}
		}
		return elemSet, nil
	}).Vararg("lists"))

	prelude.Bind("distinct values", wrapTyped(func(list []any) ([]any, error) {
		elemSet := make([]any, 0)
		for _, elem := range list {
			found := false
			for _, setElem := range elemSet {
				if valuesEqual(elem, setElem) {
					found = true
					break
				}
			}
			if !found {
				elemSet = append(elemSet, elem)
			}
		}
		return elemSet, nil
	}).Required("list"))

	prelude.Bind("flatten", wrapTyped(func(list []any) ([]any, error) {
		flattened := make([]any, 0)
		var flattenInterface func(v any)
		flattenInterface = func(v any) {
			if arr, ok := v.([]any); ok {
				for _, a := range arr {
					flattenInterface(a)
				}
			} else {
				flattened = append(flattened, v)
			}
		}
		for _, elem := range list {
			flattenInterface(elem)
		}
		return flattened, nil
	}).Required("list"))

	prelude.Bind("sort", NewMacro(func(intp *Interpreter, args map[string]Node, varargs []Node) (any, error) {
		vlist, err := args["list"].Eval(intp)
		if err != nil {
			return nil, err
		}
		list, ok := vlist.([]any)
		if !ok {
			//return nil, NewEvalError(-4080, "the first argument is not list")
			return nil, NewErrTypeMismatch("list")
		}

		vpred, err := args["predicates"].Eval(intp)
		if err != nil {
			return nil, err
		}
		predicates, ok := vpred.(*FunDef)
		if !ok {
			//return nil, NewEvalError(-4080, "the second argument is not function")
			return nil, NewErrTypeMismatch("function")
		}

		newList := append([]any{}, list...)
		var sortErr error
		sort.Slice(newList, func(i, j int) bool {
			r, err := predicates.EvalCall(intp, []any{newList[i], newList[j]})
			if err != nil {
				//panic(err)
				sortErr = err
				return false
			}
			return boolValue(r)
		})
		if sortErr != nil {
			return nil, sortErr
		}
		return newList, nil
	}).Required("list", "predicates"))

	// VERDICT DIVERGENCE: upstream decoded its arguments through mapstructure,
	// so a null delimiter raised, and it took two further optional arguments
	// (prefix, suffix) DMN does not define. DMN 1.5 §10.3.4.3 Table 73:
	// string join(list) and string join(list, delimiter); null elements are
	// ignored, a null delimiter joins without a separator, and §10.3.4 makes
	// a non-string element or delimiter, a null list, or a third argument,
	// null (TCK 1140).
	prelude.Bind("string join", NewNativeFunc(func(kwargs map[string]any) (any, error) {
		list, ok := listArg(kwargs["list"])
		if !ok {
			return null, nil
		}
		delimiter := ""
		switch d := kwargs["delimiter"].(type) {
		case nil, *NullValue:
		case string:
			delimiter = d
		default:
			return null, nil
		}
		strArray := make([]string, 0, len(list))
		for _, v := range list {
			if s, ok := v.(string); ok {
				strArray = append(strArray, s)
			} else if !isNullValue(v) {
				return null, nil
			}
		}
		return strings.Join(strArray, delimiter), nil
	}).Required("list").Optional("delimiter"))

}

// extremum is min (replaceWhen 1) or max (replaceWhen -1) over list: the item
// that the current candidate compares as replaceWhen against replaces it.
//
// VERDICT DIVERGENCE: upstream skipped any item compareInterfaces could not
// order against the current candidate, ordered false below true and a null
// item above everything, so `min([1, "a"])` and `min([1, null])` were 1,
// `max([1, null])` null and `max([true, false])` true; a list of lists or
// contexts returned its first item. DMN 1.5 §10.3.4.4 defines min and max over a list of
// comparable items, and §10.3.4 makes a built-in given a parameter outside its
// domain null: so an empty list, an item of an unordered kind (null, boolean,
// list, context, range, function — Table 54), or two items of different
// kinds, yields null.
func extremum(list []any, replaceWhen int) any {
	if len(list) == 0 {
		return null
	}
	for _, item := range list {
		if !orderedKind(item) {
			return null
		}
	}
	best := list[0]
	for _, item := range list[1:] {
		cmp, err := compareInterfaces(best, item)
		if err != nil {
			return null
		}
		if cmp == replaceWhen {
			best = item
		}
	}
	return best
}

// orderedKind reports whether v is of a kind DMN 1.5 Table 54 orders: a
// number, a string, a temporal or a duration.
func orderedKind(v any) bool {
	switch v.(type) {
	case *Number, string, *FEELDuration:
		return true
	}
	_, ok := instantOf(v)
	return ok
}

// listArg is a built-in's list argument: a list as it is, any other non-null
// value as the singleton list DMN 1.5 §10.3.2.9.4 converts it to, and null as
// outside the domain.
func listArg(v any) ([]any, bool) {
	if isNullValue(v) {
		return nil, false
	}
	if l, ok := v.([]any); ok {
		return l, true
	}
	return []any{v}, true
}

// listsArg is a variadic parameter of lists (concatenate, union): each
// argument through listArg.
func listsArg(v any) ([][]any, bool) {
	args, ok := v.([]any)
	if !ok {
		return nil, true // no arguments
	}
	out := make([][]any, len(args))
	for i, a := range args {
		if out[i], ok = listArg(a); !ok {
			return nil, false
		}
	}
	return out, true
}

// intArg is an integer-valued number argument.
func intArg(v any) (int, bool) {
	n, ok := v.(*Number)
	if !ok || n == nil || !n.v.IsInt() {
		return 0, false
	}
	i, acc := n.v.Int64()
	if acc != big.Exact || i > math.MaxInt32 || i < math.MinInt32 {
		return 0, false
	}
	return int(i), true
}

// position resolves a FEEL position over a sequence of length n — 1-based,
// or counted from the end if negative — to a 0-based index; false when it is
// not an integer, is 0, or lies outside the sequence.
func position(v any, n int) (int, bool) {
	p, ok := intArg(v)
	switch {
	case !ok || p == 0 || p > n || p < -n:
		return 0, false
	case p > 0:
		return p - 1, true
	default:
		return n + p, true
	}
}

// feelString renders a value as string() does for anything but a top-level
// string or null (DMN 1.5 §10.3.4.1 Table 72: "convert from to a string"):
// in FEEL's own lexical form, so true is `true`, a list `[1, "a", null]` and
// a context `{a: 1, b: "x"}`. Strings inside a list or context are quoted and
// escaped; temporals and durations print in their lexical form, unquoted, as
// a top-level one does; ranges print with their endpoints. A context's
// entries print in key order: the fork holds contexts as Go maps, which keep
// none of their own. A key that is not a FEEL name is quoted.
//
// VERDICT DIVERGENCE: fork-only; upstream formatted with fmt's %s.
func feelString(v any) string {
	var b strings.Builder
	writeFEEL(&b, v)
	return b.String()
}

func writeFEEL(b *strings.Builder, v any) {
	switch x := v.(type) {
	case nil, *NullValue:
		b.WriteString("null")
	case string:
		b.WriteString(strconv.Quote(x))
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case *Number:
		b.WriteString(x.String())
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			writeFEEL(b, e)
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			if isFEELName(k) {
				b.WriteString(k)
			} else {
				b.WriteString(strconv.Quote(k))
			}
			b.WriteString(": ")
			writeFEEL(b, x[k])
		}
		b.WriteByte('}')
	case *RangeValue:
		if x.StartOpen {
			b.WriteByte('(')
		} else {
			b.WriteByte('[')
		}
		writeFEEL(b, x.Start)
		b.WriteString("..")
		writeFEEL(b, x.End)
		if x.EndOpen {
			b.WriteByte(')')
		} else {
			b.WriteByte(']')
		}
	case *NativeFun, *Macro, *FunDef:
		b.WriteString("function")
	case fmt.Stringer:
		b.WriteString(x.String())
	default:
		fmt.Fprintf(b, "%v", x)
	}
}

// isFEELName reports whether a context key can be written unquoted: a FEEL
// name start character followed by name characters, with no leading or
// trailing space (DMN 1.5 §10.3.1.2 grammar rules 27-30, restricted to the
// unambiguous subset).
func isFEELName(k string) bool {
	if k == "" || strings.TrimSpace(k) != k {
		return false
	}
	for i, r := range k {
		switch {
		case r == '_' || r == '?' || unicode.IsLetter(r):
		case i > 0 && (unicode.IsDigit(r) || r == ' '):
		default:
			return false
		}
	}
	return true
}
