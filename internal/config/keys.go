package config

import (
	"fmt"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// checkKeys fails on a key in the config file that is not a setting. The
// loader ignores unknown keys, so a misspelt or renamed one would otherwise
// silently fall back to its default.
func checkKeys(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc any
	if yaml.Unmarshal(data, &doc) != nil || doc == nil {
		return nil // the loader reports parse errors
	}
	if err := walkKeys(doc, reflect.TypeFor[Config](), ""); err != nil {
		return fmt.Errorf("config %s: %w", path, err)
	}
	return nil
}

var durationType = reflect.TypeFor[time.Duration]()

func walkKeys(v any, t reflect.Type, at string) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		if t == durationType {
			return nil
		}
		m, ok := v.(map[string]any)
		if !ok {
			return nil // a type mismatch is the loader's to report
		}
		fields := settings(t)
		for _, key := range slices.Sorted(maps.Keys(m)) {
			f, ok := fields[key]
			if !ok {
				return unknownKey(join(at, key), key, fields)
			}
			if err := walkKeys(m[key], f.Type, join(at, key)); err != nil {
				return err
			}
		}
	case reflect.Slice:
		items, ok := v.([]any)
		if !ok {
			return nil
		}
		for i, item := range items {
			if err := walkKeys(item, t.Elem(), fmt.Sprintf("%s[%d]", at, i)); err != nil {
				return err
			}
		}
	}
	// Maps (agents) take any key; scalars have none.
	return nil
}

// settings maps a struct's koanf keys to its fields, embedded structs
// included.
func settings(t reflect.Type) map[string]reflect.StructField {
	out := map[string]reflect.StructField{}
	for i := range t.NumField() {
		f := t.Field(i)
		tag, _, _ := strings.Cut(f.Tag.Get("koanf"), ",")
		switch {
		case tag == "-":
		case tag != "":
			out[tag] = f
		case f.Anonymous:
			for k, v := range settings(f.Type) {
				out[k] = v
			}
		}
	}
	return out
}

func unknownKey(path, key string, fields map[string]reflect.StructField) error {
	best, bestDist := "", 3 // suggest only close matches
	for name := range fields {
		if d := editDistance(key, name); d < bestDist || d == bestDist && name < best {
			best, bestDist = name, d
		}
	}
	if best != "" {
		return fmt.Errorf("%q is not a setting (did you mean %q?)", path, best)
	}
	return fmt.Errorf("%q is not a setting; see config.example.yaml", path)
}

func join(at, key string) string {
	if at == "" {
		return key
	}
	return at + "." + key
}

// editDistance is the Levenshtein distance between a and b.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}
