package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ErrWorkgroupExists is returned by SaveWorkgroup when the named workgroup is
// already present in the config file and overwrite was not requested.
var ErrWorkgroupExists = errors.New("workgroup already declared in config")

// SaveWorkgroup appends wg to the `workgroups:` sequence in the config file at
// path, preserving existing content and comments via a yaml.Node round-trip. If
// a workgroup with the same name already exists it returns ErrWorkgroupExists
// unless overwrite is true, in which case the existing entry is replaced.
//
// When the config file does not exist it is created from defaults first (the
// same shape `git-mgr init` writes). The written file is re-loaded and validated
// before returning, so a successful call never leaves an invalid config.
func SaveWorkgroup(path string, wg Workgroup, overwrite bool) error {
	path = ExpandHome(path)

	root, err := loadOrInitDocument(path)
	if err != nil {
		return err
	}
	// root is a DocumentNode; its first content node is the top-level mapping.
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("config %s is not a YAML mapping", path)
	}
	doc := root.Content[0]

	seq := findOrCreateSeq(doc, "workgroups")

	// Marshal the workgroup to a node so its yaml tags drive the output.
	var wgNode yaml.Node
	if err := wgNode.Encode(wg); err != nil {
		return fmt.Errorf("encoding workgroup: %w", err)
	}

	if idx := indexOfNamedWorkgroup(seq, wg.Name); idx >= 0 {
		if !overwrite {
			return fmt.Errorf("%w: %q (use --force to overwrite)", ErrWorkgroupExists, wg.Name)
		}
		seq.Content[idx] = &wgNode
	} else {
		seq.Content = append(seq.Content, &wgNode)
	}

	if err := writeDocumentAtomic(path, root); err != nil {
		return err
	}

	// Re-load + validate so --save never persists something Load would reject.
	if _, err := Load(path); err != nil {
		return fmt.Errorf("config invalid after saving workgroup: %w", err)
	}
	return nil
}

// loadOrInitDocument reads path into a YAML document node, or builds one from
// defaults when the file is absent.
func loadOrInitDocument(path string) (*yaml.Node, error) {
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		var root yaml.Node
		if err := yaml.Unmarshal(data, &root); err != nil {
			return nil, fmt.Errorf("parsing config %s: %w", path, err)
		}
		if root.Kind == 0 { // empty file -> start a fresh mapping
			return newEmptyDocument(), nil
		}
		return &root, nil
	case os.IsNotExist(err):
		out, mErr := yaml.Marshal(Defaults())
		if mErr != nil {
			return nil, mErr
		}
		var root yaml.Node
		if uErr := yaml.Unmarshal(out, &root); uErr != nil {
			return nil, uErr
		}
		return &root, nil
	default:
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}
}

func newEmptyDocument() *yaml.Node {
	return &yaml.Node{
		Kind:    yaml.DocumentNode,
		Content: []*yaml.Node{{Kind: yaml.MappingNode}},
	}
}

// findOrCreateSeq returns the sequence node for the given top-level mapping key,
// creating an empty one (key + sequence) if it is absent.
func findOrCreateSeq(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			v := mapping.Content[i+1]
			if v.Kind != yaml.SequenceNode {
				v.Kind = yaml.SequenceNode
				v.Tag = "!!seq"
				v.Content = nil
			}
			return v
		}
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	seqNode := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	mapping.Content = append(mapping.Content, keyNode, seqNode)
	return seqNode
}

// indexOfNamedWorkgroup returns the index in seq of the workgroup mapping whose
// "name" scalar equals name, or -1.
func indexOfNamedWorkgroup(seq *yaml.Node, name string) int {
	for i, item := range seq.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(item.Content); j += 2 {
			if item.Content[j].Value == "name" && item.Content[j+1].Value == name {
				return i
			}
		}
	}
	return -1
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
