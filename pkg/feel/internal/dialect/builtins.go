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

	// `is defined` is not a DMN built-in; it is the Camunda FEEL extension
	// upstream ported, with the semantics Camunda documented up to 8.2 (FEEL
	// Scala 1.16): "A value is defined if it exists, and it is an instance of
	// one of the FEEL data types including null" — so a name, entry,
	// property or list element holding null is defined, one that does not
	// exist is not. See definedLookup.
	prelude.Bind("is defined", NewMacro(func(intp *Interpreter, args map[string]Node, varArgs []Node) (any, error) {
		_, found, err := definedLookup(intp, args["value"])
		if err != nil {
			return false, err
		}
		return found, nil
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

	// VERDICT DIVERGENCE: upstream raised on a null list, skipped items that
	// were not numbers, returned 0 for sum([]) and 1 for product([]), panicked
	// on mean([]), divided stddev by n and median's even case picked the wrong
	// pair, and had no mode. DMN 1.5 §10.3.4.4 Table 75; see Aggregate.
	for _, name := range aggregateNames {
		prelude.Bind(name, NewNativeFunc(aggregateBuiltin(name)).Vararg("list"))
	}

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

	// VERDICT DIVERGENCE: upstream named the second parameter `predicates`,
	// raised on a list argument that was not a list, on a second argument
	// that was not a user-defined function, and ("eval call argument size
	// mismatch") on a function that did not take two arguments. DMN 1.5
	// §10.3.4.9 Table 80: sort(list, precedes), precedes a boolean function
	// of 2 arguments; §10.3.4 makes a parameter outside its domain null. So a
	// null list, a non-function or a function of any other arity is null; a
	// non-list value is a singleton list (§10.3.2.9.4, as listArg); any
	// function value — a FunDef or a native one such as a BKM — is called
	// through CallFunction. An error raised inside the function's body
	// propagates. Only a boolean true means "before": upstream read the
	// result through boolValue, so `1` or `"x"` counted as true; a
	// non-boolean or null result is "not before", like false, as
	// `list replace` reads a non-boolean match result as no match. The sort
	// is stable, so a predicate whose result is never true leaves the list as
	// it was (the spec fixes no order then).
	prelude.Bind("sort", NewMacro(func(intp *Interpreter, args map[string]Node, varargs []Node) (any, error) {
		vlist, err := args["list"].Eval(intp)
		if err != nil {
			return nil, err
		}
		vpred, err := args["precedes"].Eval(intp)
		if err != nil {
			return nil, err
		}
		list, ok := listArg(vlist)
		if !ok {
			return null, nil
		}
		switch p := vpred.(type) {
		case *FunDef:
			if len(p.Args) != 2 {
				return null, nil
			}
		case *NativeFun:
		default:
			return null, nil
		}

		newList := append([]any{}, list...)
		var sortErr error
		misfit := false
		sort.SliceStable(newList, func(i, j int) bool {
			if sortErr != nil || misfit {
				return false
			}
			r, ok, err := CallFunction(intp, vpred, newList[i], newList[j])
			if err != nil {
				sortErr = err
				return false
			}
			if !ok {
				misfit = true
				return false
			}
			before, isBool := r.(bool)
			return isBool && before
		})
		if sortErr != nil {
			return nil, sortErr
		}
		if misfit {
			return null, nil
		}
		return newList, nil
	}).Required("list", "precedes"))

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
// a context `{a: 1, b: "x"}`. Strings inside a list or context are written
// as FEEL string literals (quoteString), so they read back as the same
// string; temporals and durations print in their lexical form, unquoted, as
// a top-level one does; ranges print with their endpoints. A context's
// entries print in key order: the fork holds contexts as Go maps, which keep
// none of their own. A key that is not a FEEL name is quoted the same way.
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
		quoteString(b, x)
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
				quoteString(b, k)
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

// isFEELName reports whether a context key can be written unquoted and read
// back as the same key: one or more words separated by single spaces, each
// of which this fork's scanner reads as one name token. That is the
// unambiguous subset of DMN 1.5 §10.3.1.2 grammar rules 27-30 that the
// scanner admits: a keyword (`true`, `and`, `for`) is not a name, a run of
// spaces would read back as one, and a word the scanner splits or reads as
// something else (`?` alone is the unary-test input; `a-b` and `a.b` are
// operators; `1x` starts with a digit) would not parse. Names such as `é`,
// `a?` and `Größe` print bare. Any other key is written as a string literal,
// which always reads back.
func isFEELName(k string) bool {
	if k == "" {
		return false
	}
	for _, word := range strings.Split(k, " ") {
		if word == "" {
			return false
		}
		tokens, err := NewScanner(word).Tokens()
		if err != nil || len(tokens) != 1 || tokens[0].Kind != TokenName || tokens[0].Value != word {
			return false
		}
	}
	return true
}

// definedLookup evaluates node for `is defined` and reports whether what it
// selects exists. Upstream answered from the "key not found" and "index
// error" a missing entry or index raised; both are null now (DMN 1.5
// §10.3.2.5-6), so existence is asked structurally, recursing through chains
// of names, paths and indexes (`{a: [1]}.a[5]`, `xs[5].a`, `{a: {b: 1}}.a.c`):
//
//   - a name exists when it is bound;
//   - `e.name` exists when e does and selectProperty finds the entry or
//     property on it — a path over a list, or on null, is defined;
//   - `e1[e2]` exists when e1 does and indexInto finds the element or string
//     key — an index the list does not reach is undefined, a null element
//     defined, and a filter (a list) defined;
//   - anything else is defined when it evaluates without error.
func definedLookup(intp *Interpreter, node Node) (val any, found bool, err error) {
	switch n := node.(type) {
	case *Var:
		v, ok := intp.Resolve(n.Name)
		return v, ok, nil
	case *DotOp:
		left, found, err := definedLookup(intp, n.Left)
		if err != nil || !found {
			return null, false, err
		}
		if list, isList := left.([]any); isList {
			v, err := n.project(intp, list)
			return v, err == nil, err
		}
		if isNullValue(left) {
			return null, true, nil
		}
		v, found := selectProperty(left, n.Attr)
		return v, found, nil
	case *Binop:
		if n.Op == "[]" {
			left, found, err := definedLookup(intp, n.Left)
			if err != nil || !found {
				return null, false, err
			}
			return n.indexInto(intp, left)
		}
	}
	v, err := node.Eval(intp)
	return v, err == nil, err
}
