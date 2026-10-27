package dialect

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strings"

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
	// non-null from, with string(null) = null as its example.
	prelude.Bind("string", NewNativeFunc(func(kwargs map[string]any) (any, error) {
		v := kwargs["from"]
		if _, isNull := v.(*NullValue); isNull || v == nil {
			return null, nil
		}
		return fmt.Sprintf("%s", v), nil
	}).Required("from"))

	// VERDICT DIVERGENCE: number yields null for a string that is not a
	// numeric literal and for an argument it cannot read, instead of 0 (the
	// big.Float that SetString left untouched) or an error. DMN 1.5 §10.3.4.1
	// Table 72: from must conform to the numeric-literal grammar, and §10.3.4
	// makes a parameter outside its domain null.
	prelude.Bind("number", NewNativeFunc(func(kwargs map[string]any) (any, error) {
		from := kwargs["from"]
		if _, ok := kwargs["decimal separator"]; ok {
			s, ok := from.(string)
			if !ok {
				return null, nil
			}
			if from, ok = separatedNumber(s, kwargs["grouping separator"], kwargs["decimal separator"]); !ok {
				return null, nil
			}
		}
		if s, ok := from.(string); ok && !numericLiteral.MatchString(s) {
			return null, nil
		}
		n, err := ParseNumberWithErr(from)
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
	prelude.Bind("string length", wrapTyped(func(s string) (int, error) {
		return len(s), nil
	}).Required("string"))

	prelude.Bind("substring", NewNativeFunc(func(kwargs map[string]any) (any, error) {
		type substringArgs struct {
			Str      string  `json:"string"`
			StartPos *Number `json:"start position"`
			Length   *Number `json:"length,omitempty"`
		}

		args := substringArgs{}
		if err := decodeKWArgs(kwargs, &args); err != nil {
			return nil, err
		}
		startPos := fromFEELIndex(args.StartPos.Int())
		if startPos >= len(args.Str) {
			return "", nil
		}
		endPos := len(args.Str)
		if args.Length != nil {
			endPos = startPos + int(args.Length.Int64())
			if endPos >= len(args.Str) {
				endPos = len(args.Str)
			}
		}
		subs := args.Str[startPos:endPos]
		return subs, nil
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

	prelude.Bind("all", NewNativeFunc(func(args map[string]any) (any, error) {
		list, err := extractList(args, "list")
		if err != nil {
			return nil, err
		}
		for _, v := range list {
			if !boolValue(v) {
				return false, nil
			}
		}
		return true, nil
	}).Vararg("list"))

	prelude.Bind("and", NewNativeFunc(func(args map[string]any) (any, error) {
		list, err := extractList(args, "list")
		if err != nil {
			return nil, err
		}
		for _, v := range list {
			if !boolValue(v) {
				return false, nil
			}
		}
		return true, nil
	}).Vararg("list"))

	prelude.Bind("any", NewNativeFunc(func(args map[string]any) (any, error) {
		list, err := extractList(args, "list")
		if err != nil {
			return nil, err
		}
		for _, v := range list {
			if boolValue(v) {
				return true, nil
			}
		}
		return false, nil
	}).Vararg("list"))

	prelude.Bind("or", NewNativeFunc(func(args map[string]any) (any, error) {
		list, err := extractList(args, "list")
		if err != nil {
			return nil, err
		}
		for _, v := range list {
			if boolValue(v) {
				return true, nil
			}
		}
		return false, nil
	}).Vararg("list"))

	prelude.Bind("sublist", NewNativeFunc(func(kwargs map[string]any) (any, error) {
		type sublistArgs struct {
			List     []any   `json:"list"`
			StartPos *Number `json:"start position"`
			Length   *Number `json:"length,omitempty"`
		}

		args := sublistArgs{}
		if err := decodeKWArgs(kwargs, &args); err != nil {
			return nil, err
		}
		startPos := fromFEELIndex(args.StartPos.Int())
		if startPos >= len(args.List) {
			return "", nil
		}
		endPos := len(args.List)
		if args.Length != nil {
			endPos = startPos + int(args.Length.Int64())
			if endPos >= len(args.List) {
				endPos = len(args.List)
			}
		}
		subs := args.List[startPos:endPos]
		return subs, nil
	}).Required("list", "start position").Optional("length"))

	prelude.Bind("append", NewNativeFunc(func(kwargs map[string]any) (any, error) {
		type appendArgs struct {
			List  []any `json:"list"`
			Items []any `json:"items"`
		}
		args := appendArgs{}
		if err := decodeKWArgs(kwargs, &args); err != nil {
			return nil, err
		}
		return append(args.List, args.Items...), nil
	}).Required("list").Vararg("items"))

	prelude.Bind("concatenate", NewNativeFunc(func(kwargs map[string]any) (any, error) {
		type concatArgs struct {
			Lists [][]any `json:"lists"`
		}
		args := concatArgs{}
		if err := decodeKWArgs(kwargs, &args); err != nil {
			return nil, err
		}
		results := make([]any, 0)
		for _, list := range args.Lists {
			results = append(results, list...)
		}
		return results, nil
	}).Vararg("lists"))

	prelude.Bind("insert before", wrapTyped(func(list []any, pos *Number, newItem any) ([]any, error) {
		// The position starts at the index 1. The last position is -1
		position := fromFEELIndex(pos.Int())
		if position > len(list) {
			position = len(list)
		}
		// make a copy of the original list
		var tmpList []any
		tmpList = append(tmpList, list[:position]...)

		//
		newList := append(tmpList, newItem)
		newList = append(newList[:], list[position:]...)
		return newList, nil
	}).Required("list", "position", "newItem"))

	prelude.Bind("remove", wrapTyped(func(list []any, pos *Number) ([]any, error) {
		// The position starts at the index 1. The last position is -1
		position := fromFEELIndex(pos.Int())
		if position > len(list) {
			position = len(list)
		}
		// make a copy of the original list
		var tmpList []any
		tmpList = append(tmpList, list[:position]...)
		//
		newList := append(tmpList, list[(position+1):]...)
		return newList, nil
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

	prelude.Bind("union", NewNativeFunc(func(kwargs map[string]any) (any, error) {
		type unionArgs struct {
			Lists [][]any `json:"lists"`
		}
		args := unionArgs{}
		if err := decodeKWArgs(kwargs, &args); err != nil {
			return nil, err
		}
		elemSet := make([]any, 0)
		for _, list := range args.Lists {
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

	prelude.Bind("string join", NewNativeFunc(func(kwargs map[string]any) (any, error) {
		type joinArgs struct {
			List      []any  `json:"list"`
			Delimiter string `json:"delimiter,omitempty"`
			Prefix    string `json:"prefix,omitempty"`
			Suffix    string `json:"suffix,omitempty"`
		}
		args := joinArgs{}
		if err := decodeKWArgs(kwargs, &args); err != nil {
			return nil, err
		}
		strArray := make([]string, 0)
		for _, v := range args.List {
			if s, ok := v.(string); ok {
				strArray = append(strArray, s)
			} else if _, ok := v.(*NullValue); !ok {
				return null, nil
			}
		}
		joined := fmt.Sprintf("%s%s%s", args.Prefix, strings.Join(strArray, args.Delimiter), args.Suffix)
		return joined, nil
	}).Required("list").Optional("delimiter", "prefix", "suffix"))

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
