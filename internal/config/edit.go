package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Document is a config file edited in place: changes go into its YAML tree,
// so comments and the order of everything left alone survive.
type Document struct {
	path string
	root *yaml.Node // a DocumentNode holding the top-level mapping
}

// OpenDocument reads the config file at path for editing. A missing file
// starts empty.
func OpenDocument(path string) (*Document, error) {
	path = ExpandHome(path)
	root, err := loadOrInitDocument(path)
	if err != nil {
		return nil, err
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("config %s is not a YAML mapping", path)
	}
	return &Document{path: path, root: root}, nil
}

func (d *Document) top() *yaml.Node { return d.root.Content[0] }

// Set sets the value at a dotted key path, such as "backup.enabled",
// creating the mappings on the way.
func (d *Document) Set(key string, value any) error {
	node, err := encode(value)
	if err != nil {
		return err
	}
	m := d.top()
	parts := strings.Split(key, ".")
	for _, p := range parts[:len(parts)-1] {
		child := mappingValue(m, p)
		if child == nil || child.Kind != yaml.MappingNode {
			child = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			setMappingValue(m, p, child)
		}
		m = child
	}
	setMappingValue(m, parts[len(parts)-1], node)
	return nil
}

// Delete removes the value at a dotted key path, if present.
func (d *Document) Delete(key string) {
	m := d.top()
	parts := strings.Split(key, ".")
	for _, p := range parts[:len(parts)-1] {
		if m = mappingValue(m, p); m == nil || m.Kind != yaml.MappingNode {
			return
		}
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == parts[len(parts)-1] {
			// A comment above the removed key belongs to what follows it.
			if head := m.Content[i].HeadComment; head != "" && i+2 < len(m.Content) {
				next := m.Content[i+2]
				next.HeadComment = strings.TrimSpace(head + "\n" + next.HeadComment)
			}
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

// Has reports whether the top-level sequence seqKey holds an entry matched
// by match.
func (d *Document) Has(seqKey string, match func(*yaml.Node) bool) bool {
	seq := mappingValue(d.top(), seqKey)
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return false
	}
	for _, item := range seq.Content {
		if match(item) {
			return true
		}
	}
	return false
}

// Upsert puts value into the top-level sequence seqKey: in place of the entry
// match selects, keeping the comments on fields that remain, or appended.
func (d *Document) Upsert(seqKey string, match func(*yaml.Node) bool, value any) error {
	node, err := encode(value)
	if err != nil {
		return err
	}
	seq := findOrCreateSeq(d.top(), seqKey)
	for i, item := range seq.Content {
		if match(item) {
			seq.Content[i] = mergeMapping(item, node)
			return nil
		}
	}
	seq.Content = append(seq.Content, node)
	return nil
}

// Remove drops every entry of the top-level sequence seqKey that match
// selects.
func (d *Document) Remove(seqKey string, match func(*yaml.Node) bool) {
	seq := mappingValue(d.top(), seqKey)
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return
	}
	seq.Content = slices.DeleteFunc(seq.Content, match)
}

// Named matches a sequence entry by its name field.
func Named(name string) func(*yaml.Node) bool {
	return func(n *yaml.Node) bool {
		v := mappingValue(n, "name")
		return v != nil && v.Value == name
	}
}

// Field matches a sequence entry whose field decodes equal to want.
func Field(field string, want any) func(*yaml.Node) bool {
	return func(n *yaml.Node) bool {
		v := mappingValue(n, field)
		if v == nil {
			return false
		}
		got := reflect.New(reflect.TypeOf(want))
		return v.Decode(got.Interface()) == nil && reflect.DeepEqual(got.Elem().Interface(), want)
	}
}

// Bytes renders the edited document.
func (d *Document) Bytes() ([]byte, error) {
	return yaml.Marshal(d.root)
}

// Save writes the document atomically, then loads it back so an invalid
// config is never left in place without an error.
func (d *Document) Save() error {
	if err := writeDocumentAtomic(d.path, d.root); err != nil {
		return err
	}
	if _, err := Load(d.path); err != nil {
		return fmt.Errorf("config invalid after saving: %w", err)
	}
	return nil
}

// Path is the file the document is saved to.
func (d *Document) Path() string { return d.path }

func encode(value any) (*yaml.Node, error) {
	var n yaml.Node
	if err := n.Encode(value); err != nil {
		return nil, fmt.Errorf("encoding %T: %w", value, err)
	}
	return &n, nil
}

func mappingValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// setMappingValue sets key in mapping m, keeping the key node, and with it
// any comment, when the key exists.
func setMappingValue(m *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			keepPresentation(m.Content[i+1], value)
			m.Content[i+1] = value
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

// keepPresentation carries old's line comment over to its replacement, and
// keeps a list or mapping written inline ([a, b]) inline. An empty one ([])
// says nothing about how its contents should be written.
func keepPresentation(old, next *yaml.Node) {
	next.LineComment = old.LineComment
	if old.Kind == next.Kind && len(old.Content) > 0 && (old.Kind == yaml.SequenceNode || old.Kind == yaml.MappingNode) {
		next.Style = old.Style
	}
}

// mergeMapping makes old hold exactly next's keys and values, keeping the
// key nodes, and their comments, of keys present in both.
func mergeMapping(old, next *yaml.Node) *yaml.Node {
	if old.Kind != yaml.MappingNode || next.Kind != yaml.MappingNode {
		next.HeadComment = old.HeadComment
		return next
	}
	merged := &yaml.Node{Kind: yaml.MappingNode, Tag: old.Tag, Style: old.Style,
		HeadComment: old.HeadComment, LineComment: old.LineComment, FootComment: old.FootComment}
	for i := 0; i+1 < len(next.Content); i += 2 {
		key, value := next.Content[i], next.Content[i+1]
		for j := 0; j+1 < len(old.Content); j += 2 {
			if old.Content[j].Value == key.Value {
				key = old.Content[j]
				keepPresentation(old.Content[j+1], value)
				break
			}
		}
		merged.Content = append(merged.Content, key, value)
	}
	return merged
}

// writeDocumentAtomic marshals root and writes it to path via a temp file +
// rename, with 0600 permissions.
func writeDocumentAtomic(path string, root *yaml.Node) error {
	out, err := yaml.Marshal(root)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".git-mgr-config-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return fmt.Errorf("writing temp config: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replacing config: %w", err)
	}
	return nil
}
