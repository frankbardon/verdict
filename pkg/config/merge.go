package config

import "reflect"

// merge overlays the non-zero fields of src onto dst.
//
// YAML unmarshalling cannot distinguish "absent" from "explicitly zero" for a
// plain scalar, so the config types use pointers wherever a false or empty
// value is meaningful (`parallel_independent_decisions`, `mcp_enabled`) and
// this merge treats every other zero value as "not set". That keeps a partial
// config file additive over the documented defaults instead of silently
// resetting everything it does not mention.
func merge(dst *Config, src Config) {
	mergeValue(reflect.ValueOf(dst).Elem(), reflect.ValueOf(src))
}

func mergeValue(dst, src reflect.Value) {
	switch dst.Kind() {
	case reflect.Struct:
		for i := 0; i < dst.NumField(); i++ {
			if !dst.Field(i).CanSet() {
				continue
			}
			mergeValue(dst.Field(i), src.Field(i))
		}
	case reflect.Ptr, reflect.Slice, reflect.Map:
		// A pointer, slice or map that was present in the document replaces the
		// default outright; the caller opted into the whole value.
		if !src.IsNil() {
			dst.Set(src)
		}
	default:
		if !src.IsZero() {
			dst.Set(src)
		}
	}
}

// isZero reports whether a config block carries no settings at all, which is
// how Parse decides whether a document nested its settings under `verdict:`.
func isZero(c Config) bool { return reflect.ValueOf(c).IsZero() }
