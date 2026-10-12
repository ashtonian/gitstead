package config

import (
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// ErrWorkspaceExists is returned by SaveWorkspace when the named workspace is
// already declared in the config file and overwrite was not requested.
var ErrWorkspaceExists = errors.New("workspace already declared in config")

// SaveWorkspace appends w to the `workspaces:` sequence in the config file at
// path, preserving existing content and comments via a yaml.Node round-trip. If
// a workspace with the same name already exists it returns ErrWorkspaceExists
// unless overwrite is true, in which case the existing entry is replaced.
//
// When the config file does not exist it is created holding just the
// workspace. The written file is re-loaded and validated before returning, so
// a successful call never leaves an invalid config.
func SaveWorkspace(path string, w Workspace, overwrite bool) error {
	doc, err := OpenDocument(path)
	if err != nil {
		return err
	}
	if doc.Has("workspaces", Named(w.Name)) && !overwrite {
		return fmt.Errorf("%w: %q (use --force to overwrite)", ErrWorkspaceExists, w.Name)
	}
	if err := doc.Upsert("workspaces", Named(w.Name), w); err != nil {
		return err
	}
	if err := doc.Save(); err != nil {
		return fmt.Errorf("saving workspace: %w", err)
	}
	return nil
}

// loadOrInitDocument reads path into a YAML document node, or starts an
// empty one when the file is absent: defaults apply on load, so a new config
// holds only what was chosen.
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
		return newEmptyDocument(), nil
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
