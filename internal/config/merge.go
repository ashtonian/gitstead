package config

import (
	"encoding"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
)

// mergeKind is how the values several config files give one key combine.
type mergeKind int

const (
	// mergeOnce is a setting: one file at most may set it.
	mergeOnce mergeKind = iota
	// mergeFields is a section: its keys merge one by one.
	mergeFields
	// mergeAppend is a list of entries: every file's, in file order.
	mergeAppend
	// mergeByName is a list of named entries: every file's, in file order,
	// each name defined in one file at most.
	mergeByName
	// mergeEntries is a map: each of its keys set in one file at most.
	mergeEntries
	// mergeMachines is machines: sections by machine name, ignoring case,
	// each merged as a section.
	mergeMachines
)

var (
	machinesType    = reflect.TypeFor[map[string]Machine]()
	textUnmarshaler = reflect.TypeFor[encoding.TextUnmarshaler]()
)

// kindOf is how values of type t merge, the one statement of the rules: a
// struct is a section; a list of structs is named entries when they have a
// name key, plain entries otherwise; any other list is one setting's value; a
// map is entries, except machines, whose sections merge.
func kindOf(t reflect.Type) mergeKind {
	t = derefType(t)
	switch {
	case t == machinesType:
		return mergeMachines
	case t.Kind() == reflect.Struct && !reflect.PointerTo(t).Implements(textUnmarshaler):
		return mergeFields
	case t.Kind() == reflect.Map:
		return mergeEntries
	case t.Kind() == reflect.Slice:
		elem := derefType(t.Elem())
		if elem.Kind() != reflect.Struct {
			return mergeOnce
		}
		if _, _, named := fieldByKey(elem, "name"); named {
			return mergeByName
		}
		return mergeAppend
	}
	return mergeOnce
}

func derefType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// fieldByKey is t's field for config key key, and the key as the field
// spells it, matched ignoring case as the loader matches keys.
func fieldByKey(t reflect.Type, key string) (reflect.StructField, string, bool) {
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("koanf"), ",")
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		if name != "-" && strings.EqualFold(name, key) {
			return f, name, true
		}
	}
	return reflect.StructField{}, "", false
}

// ConflictError is a setting, or a named entry, that two config files both
// set.
type ConflictError struct {
	// Key names what both set: sync.update, machines.desktop.base_dir,
	// accounts entry "work".
	Key string
	// Files are the file that set it first and the one that set it again.
	Files [2]string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%s is set in both %s and %s: each setting may be set in only one file", e.Key, e.Files[0], e.Files[1])
}

// source is one config file's settings, as parsed.
type source struct {
	path string
	tree map[string]any
}

// mergeSources merges the files' trees, in order, into the one tree the
// loader reads. A single file's tree is merged as it is, so one file loads as
// it always has.
func mergeSources(srcs []source) (map[string]any, error) {
	m := &merger{origins: map[string]string{}}
	root := reflect.TypeFor[Config]()
	merged := map[string]any{}
	for _, src := range srcs {
		if err := m.fields(root, "", merged, src.tree, src.path); err != nil {
			return nil, err
		}
	}
	return merged, nil
}

// merger merges trees file by file, remembering which file first set each
// key and defined each named entry.
type merger struct {
	origins map[string]string
}

// fields merges src, a section of type t at key path at, into dst.
func (m *merger) fields(t reflect.Type, at string, dst, src map[string]any, file string) error {
	t = derefType(t)
	for _, key := range slices.Sorted(maps.Keys(src)) {
		typ := reflect.TypeFor[any]() // a key no setting takes merges as one; the loader rejects it
		name := key
		if f, spelled, ok := fieldByKey(t, key); ok {
			typ, name = f.Type, spelled
		}
		old, present := dst[name]
		v, err := m.value(typ, joinKey(at, name), old, present, src[key], file)
		if err != nil {
			return err
		}
		dst[name] = v
	}
	return nil
}

// value merges src, file's value for key path of type t, with old, what the
// files before it gave the key, if any. A null section, list or map adds
// nothing, and stays null when no file gives it more.
func (m *merger) value(t reflect.Type, path string, old any, present bool, src any, file string) (any, error) {
	kind := kindOf(t)
	if !fitsKind(kind, old) || !fitsKind(kind, src) {
		// A value in a shorthand the loader expands -- a single entry for a
		// list, key=value text for a map -- is one value: no other file may
		// set the key.
		kind = mergeOnce
	}
	if !present {
		m.origins[path] = file
	}
	if kind == mergeOnce {
		if present {
			return nil, &ConflictError{Key: path, Files: [2]string{m.origins[path], file}}
		}
		return src, nil
	}
	if kind == mergeByName {
		for _, entry := range asList(src) {
			if err := m.define(path, entry, file); err != nil {
				return nil, err
			}
		}
	}
	if src == nil {
		return old, nil
	}
	switch kind {
	case mergeAppend, mergeByName:
		if old == nil {
			return src, nil
		}
		return append(slices.Clone(asList(old)), asList(src)...), nil
	case mergeMachines:
		return m.machines(path, asMap(old), asMap(src), file)
	case mergeFields:
		out := cloneMap(old)
		return out, m.fields(t, path, out, asMap(src), file)
	}
	out := cloneMap(old)
	for _, k := range slices.Sorted(maps.Keys(asMap(src))) {
		entry := joinKey(path, k)
		if _, set := out[k]; set {
			return nil, &ConflictError{Key: entry, Files: [2]string{m.origins[entry], file}}
		}
		m.origins[entry] = file
		out[k] = asMap(src)[k]
	}
	return out, nil
}

// define records file's definition of a named entry of the list at path,
// refusing a name another file defines. Two entries of one name in one file
// are validation's to judge.
func (m *merger) define(path string, entry any, file string) error {
	var name string
	for k, v := range asMap(entry) {
		if s, ok := v.(string); ok && strings.EqualFold(k, "name") {
			name = s
		}
	}
	if name == "" {
		return nil // validation reports an entry with no name
	}
	what := fmt.Sprintf("%s entry %q", path, name)
	if first, seen := m.origins[what]; seen && first != file {
		return &ConflictError{Key: what, Files: [2]string{first, file}}
	}
	m.origins[what] = file
	return nil
}

// machines merges src's machine sections into old's. A section joins one an
// earlier file gave the same machine, its name matched ignoring case as the
// machine's own is; one file's sections stay as the file has them.
func (m *merger) machines(path string, old, src map[string]any, file string) (map[string]any, error) {
	out := cloneMap(old)
	section := reflect.TypeFor[Machine]()
	for _, name := range slices.Sorted(maps.Keys(src)) {
		into := name
		if _, exact := out[name]; !exact {
			for _, k := range slices.Sorted(maps.Keys(out)) {
				if strings.EqualFold(k, name) && m.origins[joinKey(path, k)] != file {
					into = k
					break
				}
			}
		}
		prev, present := out[into]
		v, err := m.value(section, joinKey(path, into), prev, present, src[name], file)
		if err != nil {
			return nil, err
		}
		out[into] = v
	}
	return out, nil
}

// fitsKind reports whether v has the shape kind merges: a mapping for a
// section, map or machines, a list for entries. Absent or null fits any.
func fitsKind(kind mergeKind, v any) bool {
	if v == nil {
		return true
	}
	switch kind {
	case mergeFields, mergeEntries, mergeMachines:
		_, ok := toMap(v)
		return ok
	case mergeAppend, mergeByName:
		_, ok := v.([]any)
		return ok
	}
	return true
}

// asMap is v as a mapping keyed by text, or nil.
func asMap(v any) map[string]any {
	m, _ := toMap(v)
	return m
}

func toMap(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	case map[any]any:
		out := make(map[string]any, len(m))
		for k, val := range m {
			out[fmt.Sprint(k)] = val
		}
		return out, true
	}
	return nil, false
}

// cloneMap is a mapping of its own holding v's keys, v a mapping or nil.
func cloneMap(v any) map[string]any {
	out := maps.Clone(asMap(v))
	if out == nil {
		out = map[string]any{}
	}
	return out
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

func joinKey(at, key string) string {
	if at == "" {
		return key
	}
	return at + "." + key
}
