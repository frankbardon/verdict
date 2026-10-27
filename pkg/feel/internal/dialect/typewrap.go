package dialect

import (
	"errors"
	"fmt"
	"github.com/mitchellh/mapstructure"
	"reflect"
)

func typeIsStruct(tp reflect.Type) bool {
	return (tp.Kind() == reflect.Struct ||
		(tp.Kind() == reflect.Ptr && typeIsStruct(tp.Elem())))
}

func interfaceToValue(a interface{}, outputType reflect.Type) (reflect.Value, error) {
	output := reflect.Zero(outputType).Interface()
	config := &mapstructure.DecoderConfig{
		Metadata: nil,
		TagName:  "json",
		Result:   &output,
	}
	decoder, err := mapstructure.NewDecoder(config)
	if err != nil {
		return reflect.Value{}, err
	}
	err = decoder.Decode(a)
	if err != nil {
		return reflect.Value{}, err
	}
	return reflect.ValueOf(output), nil
}

func valueToInterface(tp reflect.Type, val reflect.Value) (interface{}, error) {
	var output interface{}
	//if typeIsStruct(tp) {
	if false {
		output = make(map[string]interface{})
	} else {
		output = reflect.Zero(tp).Interface()
	}
	config := &mapstructure.DecoderConfig{
		Metadata: nil,
		TagName:  "json",
		Result:   &output,
	}
	decoder, err := mapstructure.NewDecoder(config)
	if err != nil {
		return nil, err
	}
	err = decoder.Decode(val.Interface())
	if err != nil {
		return nil, err
	}
	return output, nil
}

// domainArg checks a built-in's argument against the Go parameter type it is
// bound to, and applies DMN 1.5 §10.3.2.9.4's conversion to a singleton list
// where the parameter is a list. ok is false when the argument is outside the
// parameter's domain: null where a value of a type is expected, a value of
// another kind, a value that does not implement an interface parameter.
//
// VERDICT DIVERGENCE: upstream handed every argument to mapstructure, which
// raised a reflection error for most mismatches ("source data must be an array
// or slice" for `list contains(5, 5)`, a decode error for `contains(null,
// "a")`) and quietly converted others (null to an empty context, so `get
// entries(null)` was []). §10.3.4: "Whenever a parameter is outside its
// domain, the result of the built-in is null"; §10.3.2.15: unless the
// singleton-list conversion applies.
func domainArg(param any, argType reflect.Type) (any, bool) {
	if argType.Kind() == reflect.Interface {
		if argType.NumMethod() == 0 {
			return param, true
		}
		if param == nil || !reflect.TypeOf(param).Implements(argType) {
			return nil, false
		}
		return param, true
	}
	if param == nil || isNullValue(param) {
		return nil, false
	}
	switch argType.Kind() {
	case reflect.String:
		_, ok := param.(string)
		return param, ok
	case reflect.Map:
		_, ok := param.(map[string]any)
		return param, ok
	case reflect.Slice:
		if _, ok := param.([]any); ok {
			return param, true
		}
		return []any{param}, true
	case reflect.Ptr:
		return param, reflect.TypeOf(param).AssignableTo(argType)
	}
	return param, true
}

func wrapTyped(tfunc interface{}) *NativeFun {
	funcType := reflect.TypeOf(tfunc)
	if funcType.Kind() != reflect.Func {
		panic("tfunc is not func type")
	}

	firstArgNum := 0
	numIn := funcType.NumIn()

	// if false {
	// 	firstArgNum = 1
	// 	// check inputs and 1st argument
	// 	if numIn < firstArgNum {
	// 		panic(errors.New("func must have 1 more arguments"))
	// 	}
	// 	firstArgType := funcType.In(0)
	// 	// TODO: using more methods to check type equal
	// 	if !(firstArgType.Kind() == reflect.Ptr && firstArgType.Elem().Name() == "Interpreter") {
	// 		panic(fmt.Sprintf("the first arg must be %s, it's %s instead", firstArgSpec.String(), firstArgType.Elem().Name()))
	// 	}
	// }

	// if numIn != len(argNames)+firstArgNum {
	// 	panic(fmt.Sprintf("arg number msmatch, %d expected, but %d given", numIn-1, len(argNames)))
	// }

	// check outputs
	if funcType.NumOut() != 2 {
		panic("func return number must be 2")
	}

	errType := funcType.Out(1)
	errInterface := reflect.TypeOf((*error)(nil)).Elem()

	if !errType.Implements(errInterface) {
		panic("second output does not implement error")
	}

	nativeFun := &NativeFun{}
	nativeFun.fn = func(args map[string]interface{}) (interface{}, error) {
		// check inputs
		if numIn > firstArgNum+len(nativeFun.requiredArgNames) {
			return nil, errors.New("no enough params size")
		}

		// params -> []reflect.Value
		fnArgs := []reflect.Value{}
		//fnArgs = append(fnArgs, reflect.ValueOf(intp))
		j := 0
		for i := firstArgNum; i < numIn; i++ {
			argType := funcType.In(i)
			argName, hasArg := nativeFun.ArgNameAt(i)
			if !hasArg {
				return nil, errors.New(fmt.Sprintf("arg not found at %d", i))
			}
			param, ok := args[argName]
			if !ok {
				return nil, errors.New(fmt.Sprintf("arg not found %s", argName))
			}
			j++

			param, ok = domainArg(param, argType)
			if !ok {
				return null, nil
			}
			argValue, err := interfaceToValue(param, argType)
			if err != nil {
				return null, nil
			}
			fnArgs = append(fnArgs, argValue)

		}

		// wrap result
		resValues := reflect.ValueOf(tfunc).Call(fnArgs)
		resType := funcType.Out(0)
		errRes := resValues[1].Interface()
		if errRes != nil {
			if err, ok := errRes.(error); ok {
				return nil, err
			} else {
				return nil, errors.New(fmt.Sprintf("error return is not error %+v", errRes))
			}
		}

		res, err := valueToInterface(
			resType, resValues[0])
		return res, err
	}

	return nativeFun
}
