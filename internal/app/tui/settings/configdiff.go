package settings

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"marshal/internal/app/config"
)

type diffLine struct {
	Prefix string
	Path   string
	Detail string
}

var secretFieldNames = map[string]bool{"APIKey": true, "SearchKey": true}

func configDiff(before, after config.Config) []diffLine {
	var lines []diffLine
	diffValue("", reflect.ValueOf(before), reflect.ValueOf(after), &lines)
	return lines
}

func diffValue(path string, b, a reflect.Value, lines *[]diffLine) {
	b = deref(b)
	a = deref(a)
	if !b.IsValid() && !a.IsValid() {
		return
	}
	if !b.IsValid() {
		*lines = append(*lines, diffLine{Prefix: "+", Path: path, Detail: ": " + fmtScalar(path, a)})
		return
	}
	if !a.IsValid() {
		*lines = append(*lines, diffLine{Prefix: "-", Path: path, Detail: ": " + fmtScalar(path, b)})
		return
	}
	if b.Kind() != a.Kind() {
		*lines = append(*lines, diffLine{Prefix: "~", Path: path, Detail: ": " + fmtScalar(path, b) + " → " + fmtScalar(path, a)})
		return
	}
	switch b.Kind() {
	case reflect.Struct:
		t := b.Type()
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			diffValue(joinPath(path, f.Name), b.Field(i), a.Field(i), lines)
		}
	case reflect.Map:
		diffMap(path, b, a, lines)
	case reflect.Slice:
		diffSlice(path, b, a, lines)
	default:
		if !reflect.DeepEqual(b.Interface(), a.Interface()) {
			*lines = append(*lines, diffLine{Prefix: "~", Path: path, Detail: ": " + fmtScalar(path, b) + " → " + fmtScalar(path, a)})
		}
	}
}

func diffMap(path string, b, a reflect.Value, lines *[]diffLine) {
	seen := map[string]bool{}
	for _, k := range b.MapKeys() {
		seen[k.String()] = true
	}
	for _, k := range a.MapKeys() {
		seen[k.String()] = true
	}
	sorted := make([]string, 0, len(seen))
	for k := range seen {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, k := range sorted {
		bv := b.MapIndex(reflect.ValueOf(k))
		av := a.MapIndex(reflect.ValueOf(k))
		diffValue(joinPath(path, k), bv, av, lines)
	}
}

func diffSlice(path string, b, a reflect.Value, lines *[]diffLine) {
	n := b.Len()
	if a.Len() > n {
		n = a.Len()
	}
	for i := 0; i < n; i++ {
		fp := fmt.Sprintf("%s[%d]", path, i)
		var bv, av reflect.Value
		if i < b.Len() {
			bv = b.Index(i)
		}
		if i < a.Len() {
			av = a.Index(i)
		}
		diffValue(fp, bv, av, lines)
	}
}

func deref(v reflect.Value) reflect.Value {
	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return reflect.Value{}
		}
		v = v.Elem()
	}
	return v
}

func joinPath(base, seg string) string {
	if base == "" {
		return seg
	}
	return base + "." + seg
}

func fmtScalar(path string, v reflect.Value) string {
	s := fmt.Sprintf("%v", v.Interface())
	if isSecretPath(path) {
		return maskKey(s)
	}
	return s
}

func isSecretPath(path string) bool {
	parts := strings.Split(path, ".")
	if len(parts) == 0 {
		return false
	}
	return secretFieldNames[parts[len(parts)-1]]
}
