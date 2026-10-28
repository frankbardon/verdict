package dialect

import (
	"math/big"
	"reflect"
	"sort"
)

// VERDICT DIVERGENCE: this file replaces upstream's count, min, max, sum,
// product, mean, stddev and median bindings, and adds mode, which upstream
// lacked. See NOTICE.md.

// aggregateNames are the DMN 1.5 §10.3.4.4 Table 75 list aggregates Aggregate
// implements, in the order the table lists them.
var aggregateNames = []string{
	"count", "min", "max", "sum", "product", "mean", "median", "stddev", "mode",
}

// Aggregate applies the §10.3.4.4 list aggregate name — count, min, max, sum,
// product, mean, median, stddev or mode — to list. ok is false only when name
// is not one of them; an argument outside the function's domain is a null
// result, never an error (§10.3.2.13.1).
//
// It is the one implementation of these functions: the FEEL built-ins call
// it, and so may a decision table's COLLECT aggregation, which §10.3.2.10
// defines through the same built-ins.
//
// Empty lists follow Table 75: count is 0, mode the empty list, every other
// aggregate null. A list holding a null or a non-number is outside the domain
// of the arithmetic aggregates (sum, product, mean, median, stddev, mode), so
// they are null; count counts every item, null included; min and max follow
// extremum. Interpretation: Table 75 is silent on null items, and the TCK
// fixes only product([1,2,null,4]) = null (0094).
func Aggregate(name string, list []any) (result any, ok bool) {
	switch name {
	case "count":
		return NewNumberFromInt64(int64(len(list))), true
	case "min":
		return extremum(list, 1), true
	case "max":
		return extremum(list, -1), true
	}
	var fold func([]*Number) any
	switch name {
	case "sum":
		fold = sumOf
	case "product":
		fold = productOf
	case "mean":
		fold = meanOf
	case "median":
		fold = medianOf
	case "stddev":
		fold = stddevOf
	case "mode":
		fold = modeOf
	default:
		return nil, false
	}
	nums, isNumbers := numberItems(list)
	if !isNumbers {
		return null, true
	}
	return fold(nums), true
}

// aggregateBuiltin is the FEEL binding of the aggregate name.
func aggregateBuiltin(name string) func(map[string]any) (any, error) {
	return func(args map[string]any) (any, error) {
		items, ok := aggregateItems(args)
		if !ok {
			return null, nil
		}
		r, _ := Aggregate(name, items)
		return r, nil
	}
}

// aggregateItems binds the `list` parameter of an aggregate: Table 75 gives
// each the domain "list" or "argument list of one or more" items. A
// positional call collects its arguments as the list, unless it passed one
// list; a named `list:` that is not a list is a singleton list
// (§10.3.2.9.4). ok is false — the call is null — for a missing or null
// list, including a positional call whose one argument is null.
//
// The binding cannot tell `count(null)` from `count(list: [null])`, so the
// latter is null too rather than 1.
func aggregateItems(args map[string]any) (items []any, ok bool) {
	v, present := args["list"]
	if !present || isNullValue(v) {
		return nil, false
	}
	items, isList := anyList(v)
	if !isList {
		return []any{v}, true
	}
	if len(items) == 1 {
		if inner, nested := anyList(items[0]); nested {
			return inner, true
		}
		if isNullValue(items[0]) {
			return nil, false
		}
	}
	return items, true
}

// anyList is v as a list of items. Besides []any it accepts any Go slice:
// no built-in returns a typed slice any more (get entries was the last), but
// a host value bound without FromGo still can.
func anyList(v any) ([]any, bool) {
	if l, ok := v.([]any); ok {
		return l, true
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil, false
	}
	l := make([]any, rv.Len())
	for i := range l {
		l[i] = rv.Index(i).Interface()
	}
	return l, true
}

// numberItems is list as numbers, or false if any item is not a number.
func numberItems(list []any) ([]*Number, bool) {
	nums := make([]*Number, len(list))
	for i, item := range list {
		n, ok := item.(*Number)
		if !ok || n == nil {
			return nil, false
		}
		nums[i] = n
	}
	return nums, true
}

// sumOf is sum(list): null for an empty list (Table 75, `sum([]) = null`).
func sumOf(nums []*Number) any {
	if len(nums) == 0 {
		return null
	}
	sum := Zero
	for _, n := range nums {
		sum = sum.Add(n)
	}
	return sum
}

// productOf is product(list): null for an empty list (TCK 0094-002).
func productOf(nums []*Number) any {
	if len(nums) == 0 {
		return null
	}
	p := N(1)
	for _, n := range nums {
		p = p.Mul(n)
	}
	return p
}

// meanOf is mean(list), divided at the working precision as `/` is: null for
// an empty list.
func meanOf(nums []*Number) any {
	if len(nums) == 0 {
		return null
	}
	return sumOf(nums).(*Number).Div(NewNumberFromInt64(int64(len(nums))))
}

// medianOf is median(list): the middle item of the sorted list, or the mean
// of the two middle items of an even-length one; null for an empty list
// (TCK 0061-007; `median([6,1,2,3]) = 2.5`, 0061-006).
func medianOf(nums []*Number) any {
	if len(nums) == 0 {
		return null
	}
	sorted := sortedNumbers(nums)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return sorted[mid-1].Add(sorted[mid]).Div(N(2))
}

// stddevOf is stddev(list), the sample standard deviation at 34 significant
// digits (Table 75,
// `stddev([2,4,7,5]) = 2.081665999466132735282297706979931`): null for fewer
// than two items (`stddev([47]) = null`, TCK 0063-007/008).
func stddevOf(nums []*Number) any {
	if len(nums) < 2 {
		return null
	}
	mean := meanOf(nums).(*Number)
	ss := Zero
	for _, n := range nums {
		d := n.Sub(mean)
		ss = ss.Add(d.Mul(d))
	}
	variance := ss.Div(NewNumberFromInt64(int64(len(nums) - 1)))
	// Rounded to decimal128's 34 significant digits (§10.3.2.3.1), as
	// Verdict's sqrt is: an irrational root has no exact value to keep.
	root := new(big.Float).SetPrec(Prec).Sqrt(variance.v)
	return NewNumber(root.Text('g', 34))
}

// modeOf is mode(list): the most frequent items, ascending; the empty list
// for an empty list (Table 75, `mode([6,1,9,6,1]) = [1,6]`, `mode([]) = []`).
func modeOf(nums []*Number) any {
	out := []any{}
	sorted := sortedNumbers(nums)
	best := 0
	for i := 0; i < len(sorted); {
		j := i + 1
		for j < len(sorted) && sorted[j].Cmp(sorted[i]) == 0 {
			j++
		}
		switch run := j - i; {
		case run > best:
			best = run
			out = []any{sorted[i]}
		case run == best:
			out = append(out, sorted[i])
		}
		i = j
	}
	return out
}

// sortedNumbers is an ascending copy of nums.
func sortedNumbers(nums []*Number) []*Number {
	sorted := append([]*Number(nil), nums...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Cmp(sorted[j]) < 0 })
	return sorted
}
