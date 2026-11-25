package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ashtonian/gitstead/internal/pathutil"
)

// Document is a config file edited in place: changes go into its YAML tree,
// so comments and the order of everything left alone survive. Its fragments
// are read alongside and never written: a change to a setting or entry one
// of them sets is refused, naming it, so a write never makes a second
// source for a fact.
type Document struct {
	path      string
	root      *yaml.Node // a DocumentNode holding the top-level mapping
	fragments []fragmentDoc
}

// fragmentDoc is a fragment of a document's config, as read when it was
// opened.
type fragmentDoc struct {
	path string
	top  *yaml.Node // its top-level mapping; nil when it holds none
}

// FragmentError is a write a Document refuses: what it would change is set
// in a fragment, which gitstead leaves to whoever owns it.
type FragmentError struct {
	// What names the setting or entry: sync.update, workspaces entry "app".
	What string
	// Fragment is the file that sets it; Config the file writes go to.
	Fragment, Config string
}

func (e *FragmentError) Error() string {
	return fmt.Sprintf("%s is set in %s, and gitstead writes only %s: change it there", e.What, e.Fragment, e.Config)
}

// OpenDocument reads the config file at path for editing, with its
// fragments. A missing file starts empty.
func OpenDocument(path string) (*Document, error) {
	path = ExpandHome(path)
	root, err := loadOrInitDocument(path)
	if err != nil {
		return nil, err
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("config %s is not a YAML mapping", path)
	}
	paths, err := fragmentPaths(path)
	if err != nil {
		return nil, err
	}
	d := &Document{path: path, root: root}
	for _, p := range paths {
		data, err := readConfigFile(p)
		if err != nil {
			return nil, err
		}
		var froot yaml.Node
		if err := yaml.Unmarshal(data, &froot); err != nil {
			return nil, fmt.Errorf("config %s does not parse: %w", p, err)
		}
		f := fragmentDoc{path: p}
		if len(froot.Content) > 0 && froot.Content[0].Kind == yaml.MappingNode {
			f.top = froot.Content[0]
		}
		d.fragments = append(d.fragments, f)
	}
	return d, nil
}

func (d *Document) top() *yaml.Node { return d.root.Content[0] }

// Set sets the value at a dotted key path, such as "backup.enabled",
// creating the mappings on the way. A key a fragment sets is refused, unless
// the fragment already gives it value.
func (d *Document) Set(key string, value any) error {
	node, err := encode(value)
	if err != nil {
		return err
	}
	if frag, have := d.fragmentSetting(key); have != nil {
		if sameValue(have, node) {
			return nil
		}
		return &FragmentError{What: key, Fragment: frag, Config: d.path}
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

// Delete removes the value at a dotted key path, if present. A key a
// fragment sets is refused: deleting it here would leave it set.
func (d *Document) Delete(key string) error {
	if frag, have := d.fragmentSetting(key); have != nil {
		return &FragmentError{What: key, Fragment: frag, Config: d.path}
	}
	m := d.top()
	parts := strings.Split(key, ".")
	for _, p := range parts[:len(parts)-1] {
		if m = mappingValue(m, p); m == nil || m.Kind != yaml.MappingNode {
			return nil
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
			return nil
		}
	}
	return nil
}

// Has reports whether the document's own top-level sequence seqKey holds an
// entry matched by match.
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
// match selects, keeping the comments on fields that remain, or appended. An
// entry match selects in a fragment is refused, unless it is value already.
func (d *Document) Upsert(seqKey string, match func(*yaml.Node) bool, value any) error {
	node, err := encode(value)
	if err != nil {
		return err
	}
	if frag, have := d.fragmentEntry(seqKey, match); have != nil {
		if sameValue(have, node) {
			return nil
		}
		return &FragmentError{What: entryName(seqKey, have), Fragment: frag, Config: d.path}
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
// selects. One a fragment holds is refused: removing it here would leave it.
func (d *Document) Remove(seqKey string, match func(*yaml.Node) bool) error {
	if frag, have := d.fragmentEntry(seqKey, match); have != nil {
		return &FragmentError{What: entryName(seqKey, have), Fragment: frag, Config: d.path}
	}
	seq := mappingValue(d.top(), seqKey)
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return nil
	}
	seq.Content = slices.DeleteFunc(seq.Content, match)
	return nil
}

// fragmentSetting is the first fragment that sets the dotted key, and its
// value there.
func (d *Document) fragmentSetting(key string) (string, *yaml.Node) {
	for _, f := range d.fragments {
		v := f.top
		for _, p := range strings.Split(key, ".") {
			if v = mappingValue(v, p); v == nil {
				break
			}
		}
		if v != nil {
			return f.path, v
		}
	}
	return "", nil
}

// fragmentEntry is the first fragment whose top-level sequence seqKey holds
// an entry match selects, and that entry.
func (d *Document) fragmentEntry(seqKey string, match func(*yaml.Node) bool) (string, *yaml.Node) {
	for _, f := range d.fragments {
		seq := mappingValue(f.top, seqKey)
		if seq == nil || seq.Kind != yaml.SequenceNode {
			continue
		}
		for _, item := range seq.Content {
			if match(item) {
				return f.path, item
			}
		}
	}
	return "", nil
}

// entryName names a sequence entry in an error: by its name, URL or account.
func entryName(seqKey string, entry *yaml.Node) string {
	for _, field := range []string{"name", "url", "account"} {
		if v := mappingValue(entry, field); v != nil && v.Kind == yaml.ScalarNode {
			return fmt.Sprintf("%s entry %q", seqKey, v.Value)
		}
	}
	return seqKey + " entry"
}

// sameValue reports whether two YAML values decode to the same thing,
// however each is written.
func sameValue(a, b *yaml.Node) bool {
	var av, bv any
	return a.Decode(&av) == nil && b.Decode(&bv) == nil && reflect.DeepEqual(av, bv)
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

// Check loads the config the document makes, with its fragments, without
// writing it.
func (d *Document) Check(ctx context.Context) (*Config, error) {
	data, err := d.Bytes()
	if err != nil {
		return nil, err
	}
	return Load(ctx, d.path, withMainData(data))
}

// Save checks the config the document makes and then writes it atomically,
// so an invalid config is never written.
func (d *Document) Save(ctx context.Context) error {
	if _, err := d.Check(ctx); err != nil {
		return fmt.Errorf("checking %s before writing it: %w", d.path, err)
	}
	return writeDocumentAtomic(d.path, d.root)
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
	if err := pathutil.WriteFileAtomic(path, out, 0o600); err != nil {
		return fmt.Errorf("replacing config: %w", err)
	}
	return nil
}
