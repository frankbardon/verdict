package dialect

import "reflect"

func contextGetByKeys(ctx map[string]any, keys []string) (any, bool) {
	for i, key := range keys {
		if i == len(keys)-1 {
			v, ok := ctx[key]
			return v, ok
		} else {
			v, ok := ctx[key]
			if !ok {
				return nil, false
			}
			if subctx, ok := v.(map[string]any); ok {
				ctx = subctx
			} else {
				return nil, false
			}
		}
	}
	return nil, false
}

// contextPutCopy is ctx with value put under the key path keys, as a new
// context: each context along the path is copied, never written in place, and
// a missing one is created. false when an existing entry along the path is not
// a context.
func contextPutCopy(ctx map[string]any, keys []string, value any) (map[string]any, bool) {
	out := make(map[string]any, len(ctx)+1)
	for k, v := range ctx {
		out[k] = v
	}
	if len(keys) == 1 {
		out[keys[0]] = value
		return out, true
	}
	var inner map[string]any
	switch sub := ctx[keys[0]].(type) {
	case nil:
		inner = map[string]any{}
	case map[string]any:
		inner = sub
	default:
		return nil, false
	}
	put, ok := contextPutCopy(inner, keys[1:], value)
	if !ok {
		return nil, false
	}
	out[keys[0]] = put
	return out, true
}

func installContextFunctions(prelude *Prelude) {
	// context/map functions
	prelude.Bind("get value", NewNativeFunc(func(kwargs map[string]any) (any, error) {
		type getvalueByKey struct {
			Context map[string]any `json:"context"`
			Key     string         `json:"key"`
		}

		type getvalueByKeys struct {
			Context map[string]any `json:"context"`
			Keys    []string       `json:"key"`
		}

		if key, ok := kwargs["key"].(string); ok {
			argsByKey := getvalueByKey{
				Context: map[string]any{},
				Key:     key,
			}
			if err := getContextMap(kwargs["context"], argsByKey.Context); err == nil {
				if v, ok := argsByKey.Context[argsByKey.Key]; ok {
					return dereferencePtr(v), nil
				} else {
					return null, nil
				}
			}
		}

		// VERDICT DIVERGENCE: a context or key path mapstructure cannot
		// decode — null, a list holding a non-string — is null rather than
		// an error (DMN 1.5 §10.3.4.10, §10.3.4); so is an empty key path.
		if isNullValue(kwargs["context"]) {
			return null, nil
		}
		argsByKeys := getvalueByKeys{}
		if err := decodeKWArgs(kwargs, &argsByKeys); err != nil || len(argsByKeys.Keys) == 0 {
			return null, nil
		}

		if v, ok := contextGetByKeys(argsByKeys.Context, argsByKeys.Keys); ok {
			return dereferencePtr(v), nil
		} else {
			return null, nil
		}
	}).Required("context", "key"))

	prelude.Bind("get entries", wrapTyped(func(ctx map[string]any) ([](map[string]any), error) {
		entries := make([](map[string]any), 0)
		for k, v := range ctx {
			ent := map[string]any{
				"key":   k,
				"value": v,
			}
			entries = append(entries, ent)
		}
		return entries, nil
	}).Required("context"))

	// VERDICT DIVERGENCE: upstream decoded its arguments through mapstructure,
	// so a null context, key or keys raised, a context built from a non-map
	// was empty, and a put under a nested key wrote into the argument's own
	// nested context — a value other expressions still held. DMN 1.5 §10.3.4.10
	// Table 81: context is a context, key a string or keys a non-empty list of
	// strings, and value "any element including null"; the result is a new
	// context, the entry overriding one with the same key. §10.3.4 makes any
	// other argument null (TCK 1146, 1147), and the fork reads a key path
	// through an existing entry that is not a context as outside the domain
	// too, where upstream returned the context unchanged.
	prelude.Bind("context put", NewNativeFunc(func(kwargs map[string]any) (any, error) {
		ctx, ok := kwargs["context"].(map[string]any)
		if !ok {
			return null, nil
		}
		var keys []string
		switch k := kwargs["key"].(type) {
		case string:
			keys = []string{k}
		case []any:
			for _, e := range k {
				s, ok := e.(string)
				if !ok {
					return null, nil
				}
				keys = append(keys, s)
			}
		}
		if len(keys) == 0 {
			return null, nil
		}
		out, ok := contextPutCopy(ctx, keys, kwargs["value"])
		if !ok {
			return null, nil
		}
		return out, nil
	}).Required("context", "key", "value"))

	// VERDICT DIVERGENCE: upstream bound its parameter as "contextx", so the
	// named form `context merge(contexts: ...)` failed, and decoded it through
	// mapstructure, so `context merge({a: 1})` raised "source data must be an
	// array or slice". DMN 1.5 §10.3.4.10 Table 81: contexts is a list of
	// contexts, later entries overriding earlier ones with the same key; a
	// lone context is the singleton list §10.3.2.9.4 converts it to, and
	// §10.3.4 makes null, or an item that is not a context, null.
	prelude.Bind("context merge", NewNativeFunc(func(kwargs map[string]any) (any, error) {
		list, ok := listArg(kwargs["contexts"])
		if !ok {
			return null, nil
		}
		merged := make(map[string]any)
		for _, item := range list {
			ctx, ok := item.(map[string]any)
			if !ok {
				return null, nil
			}
			for k, v := range ctx {
				merged[k] = v
			}
		}
		return merged, nil
	}).Required("contexts"))

}

// dereferencePtr dereference pointer.
// If the value is a pointer to any internal feel objects they will remain as is,
// any other value will be dereferenced if it is a pointer
func dereferencePtr(val any) any {
	if val == nil {
		return null
	}
	switch val.(type) {
	// Don't dereference
	case *Number, *NullValue, *FEELDate, *FEELDatetime, *FEELDuration, *FEELTime:
		return val
	}
	v := reflect.ValueOf(val)
	for v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return null
		}
		v = v.Elem()
	}
	return v.Interface()
}
