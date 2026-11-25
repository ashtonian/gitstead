package config

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	pkgconfig "github.com/ashtonian/pkg/config"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
)

// FragmentDirName is the directory beside the config file whose YAML files
// gitstead reads as more of the config.
const FragmentDirName = "config.d"

// FragmentDir is the directory of fragments read with the config file at
// path.
func FragmentDir(path string) string {
	return filepath.Join(filepath.Dir(ExpandHome(path)), FragmentDirName)
}

// FilesFor lists the files Load reads for the config file at path, in the
// order they merge: the file itself, when it exists, then its fragments.
func FilesFor(path string) ([]string, error) {
	path = ExpandHome(path)
	var files []string
	switch _, err := os.Stat(path); {
	case err == nil:
		files = append(files, path)
	case !errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}
	fragments, err := fragmentPaths(path)
	if err != nil {
		return nil, err
	}
	return append(files, fragments...), nil
}

// fragmentPaths lists the fragments of the config file at path: the *.yaml
// and *.yml files in its config.d, hidden ones left out, by name in byte
// order (os.ReadDir's). No config.d means none; one that can't be read --
// a broken link to one included -- is an error, never a config without them.
func fragmentPaths(path string) ([]string, error) {
	dir := FragmentDir(path)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		if _, lerr := os.Lstat(dir); lerr != nil {
			return nil, nil
		}
	}
	if err != nil {
		return nil, fmt.Errorf("reading config fragments %s: %w", dir, err)
	}
	var paths []string
	for _, e := range entries {
		name := e.Name()
		ext := filepath.Ext(name)
		if strings.HasPrefix(name, ".") || (ext != ".yaml" && ext != ".yml") {
			continue
		}
		paths = append(paths, filepath.Join(dir, name))
	}
	return paths, nil
}

// readConfigFile reads a config file or fragment, following links. Anything
// but a regular file is an error: a directory, or a pipe a read would wait
// on forever.
func readConfigFile(path string) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("reading config %s: not a regular file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}
	return data, nil
}

// readSources reads the config file at path, or o's stand-in for it, and its
// fragments, and checks each on its own: it must parse, and every key in it
// must be a setting with a value of the setting's type. What only the whole
// config can show -- an account another file defines, a setting two files
// set -- waits for the merge and its validation.
func readSources(ctx context.Context, path string, o *loadOptions) ([]source, error) {
	var srcs []source
	data, err := o.mainData, error(nil)
	if !o.mainGiven {
		data, err = readConfigFile(path)
	}
	switch {
	case err == nil:
		tree, err := parseSource(ctx, path, data, false)
		if err != nil {
			return nil, err
		}
		srcs = append(srcs, source{path: path, tree: tree})
	case errors.Is(err, fs.ErrNotExist) && o.optionalFile:
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
	default:
		return nil, err
	}
	fragments, err := fragmentPaths(path)
	if err != nil {
		return nil, err
	}
	for _, p := range fragments {
		data, err := readConfigFile(p)
		if err != nil {
			return nil, err
		}
		tree, err := parseSource(ctx, p, data, true)
		if err != nil {
			return nil, err
		}
		srcs = append(srcs, source{path: p, tree: tree})
	}
	return srcs, nil
}

// parseSource parses one file's settings and checks them on their own.
func parseSource(ctx context.Context, path string, data []byte, fragment bool) (map[string]any, error) {
	where := "config " + path
	tree, err := yaml.Parser().Unmarshal(data)
	if err != nil {
		return nil, fmt.Errorf("%s does not parse: %w", where, err)
	}
	if fragment {
		for key := range tree {
			if strings.EqualFold(key, "machine") {
				return nil, invalidConfig(where, errors.New(`"machine" can't be set in a fragment, which every machine reads: choose the machine with GITSTEAD_MACHINE or --machine`))
			}
		}
	}
	// The loader's own strict decode, of this file alone, so an unknown key
	// or a value of the wrong type is reported in the file it is in.
	_, err = pkgconfig.Load[Config](ctx,
		pkgconfig.WithBaseProvider(confmap.Provider(tree, ""), nil),
		pkgconfig.WithSkipValidation(),
	)
	if err != nil {
		return nil, loadError(where, err)
	}
	return tree, nil
}
