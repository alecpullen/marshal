package config

import "reflect"

// deepCopyConfig returns a deep copy of cfg so that mutations to the original
// do not affect the copy. This is used to build the independent Default and
// User snapshots in LoadLayers; without it, those snapshots share the map and
// slice values with the merged config and with each other, which causes
// layer-provenance comparisons and project save logic to see the wrong values.
func deepCopyConfig(cfg Config) Config {
	return deepCopyValue(reflect.ValueOf(cfg)).Interface().(Config)
}

func deepCopyValue(v reflect.Value) reflect.Value {
	if !v.IsValid() {
		return v
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		cp := reflect.New(v.Type().Elem())
		cp.Elem().Set(deepCopyValue(v.Elem()))
		return cp
	case reflect.Interface:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		cp := reflect.New(v.Type()).Elem()
		cp.Set(deepCopyValue(v.Elem()))
		return cp
	case reflect.Map:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		cp := reflect.MakeMap(v.Type())
		for _, key := range v.MapKeys() {
			cp.SetMapIndex(key, deepCopyValue(v.MapIndex(key)))
		}
		return cp
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		cp := reflect.MakeSlice(v.Type(), v.Len(), v.Cap())
		for i := 0; i < v.Len(); i++ {
			cp.Index(i).Set(deepCopyValue(v.Index(i)))
		}
		return cp
	case reflect.Array:
		cp := reflect.New(v.Type()).Elem()
		for i := 0; i < v.Len(); i++ {
			cp.Index(i).Set(deepCopyValue(v.Index(i)))
		}
		return cp
	case reflect.Struct:
		cp := reflect.New(v.Type()).Elem()
		for i := 0; i < v.NumField(); i++ {
			field := v.Field(i)
			if cp.Field(i).CanSet() {
				cp.Field(i).Set(deepCopyValue(field))
			}
		}
		return cp
	default:
		return v
	}
}
