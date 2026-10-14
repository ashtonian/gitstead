package cmd

import (
	"fmt"
	"sort"

	"github.com/ashtonian/gitstead/internal/config"
	"github.com/ashtonian/gitstead/internal/repourl"
	"github.com/ashtonian/gitstead/internal/state"
)

// selection narrows tracked repos by a glob over keys, a tag, and/or a named
// set. The three filters all narrow (AND): a repo must satisfy each one given.
type selection struct {
	repos string // glob over repo keys (repourl.MatchKey)
	tag   string
	set   string // name of a config.RepoSet
}

// empty reports whether no selection filter is set (i.e. "all repos").
func (s selection) empty() bool { return s.repos == "" && s.tag == "" && s.set == "" }

// predicate compiles sel into a per-repo membership test. A named set's Repos
// globs and Tags are unioned; the set and the inline glob/tag filters then all
// narrow (AND). Commands build this once and call it in their repo loop instead
// of re-implementing the filter.
func (sel selection) predicate(cfg *config.Config) (func(key string, repo *state.RepoState) bool, error) {
	var set *config.RepoSet
	if sel.set != "" {
		s, ok := cfg.FindSet(sel.set)
		if !ok {
			return nil, fmt.Errorf("unknown set %q (define it under `sets:` in config)", sel.set)
		}
		set = s
	}
	return func(key string, repo *state.RepoState) bool {
		if set != nil && !set.Matches(cfg.MemberOf(key, repo.DiscoveredBy, repo.HasTag, repo.IsArchived)) {
			return false
		}
		if sel.repos != "" {
			if !repourl.MatchKey(sel.repos, key) {
				return false
			}
		}
		if sel.tag != "" && !repo.HasTag(sel.tag) {
			return false
		}
		return true
	}, nil
}

// selectRepos returns the sorted keys of tracked repos matching sel.
func selectRepos(cfg *config.Config, st *state.State, sel selection) ([]string, error) {
	pass, err := sel.predicate(cfg)
	if err != nil {
		return nil, err
	}
	var keys []string
	for key, repo := range st.Repos {
		if pass(key, repo) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys, nil
}
