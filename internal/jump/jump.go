// Package jump finds what a short query means among the places gitstead
// knows: tracked repos, workspaces, agents' replicas and their copies. It
// backs `gitstead path`, `cd` and `open`.
package jump

import (
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ashtonian/gitstead/internal/state"
)

// Kinds of target.
const (
	KindRepo      = "repo"
	KindWorkspace = "workspace"
	KindReplica   = "agent"
	KindCopy      = "copy"
)

// Target is a place a query can mean.
type Target struct {
	Label string // how people name it: a repo key, workspace, workspace/agent[/folder]
	Path  string
	Kind  string
	// Repo is the tracked repo it is, or a copy of; "" for workspaces and
	// replicas of several repos.
	Repo string
	// words are what a query is matched against, lower-case.
	words []string
}

// Targets lists every place st knows, repos first, each once by path.
func Targets(st *state.State) []Target {
	var out []Target
	seen := map[string]bool{}
	add := func(t Target, words ...string) {
		if t.Path == "" || seen[t.Path] {
			return
		}
		seen[t.Path] = true
		for _, w := range append(words, t.Label, filepath.Base(t.Path)) {
			if w != "" && !slices.Contains(t.words, strings.ToLower(w)) {
				t.words = append(t.words, strings.ToLower(w))
			}
		}
		out = append(out, t)
	}
	for _, key := range slices.Sorted(maps.Keys(st.Repos)) {
		r := st.Repos[key]
		add(Target{Label: key, Path: r.LocalPath, Kind: KindRepo, Repo: key}, r.Name, r.FullName)
	}
	for _, name := range slices.Sorted(maps.Keys(st.Workspaces)) {
		ws := st.Workspaces[name]
		add(Target{Label: name, Path: ws.Dir, Kind: KindWorkspace}, name)
		for _, r := range ws.Replicas {
			label := name + "/" + r.Agent
			repo := ""
			if len(r.Copies) == 1 && r.Copies[0].Path == r.Path {
				repo = r.Copies[0].Repo
			}
			add(Target{Label: label, Path: r.Path, Kind: KindReplica, Repo: repo}, r.Agent)
			for _, c := range r.Copies {
				if c.Path == r.Path {
					continue
				}
				rel, err := filepath.Rel(r.Path, c.Path)
				if err != nil {
					rel = filepath.Base(c.Path)
				}
				add(Target{Label: label + "/" + filepath.ToSlash(rel), Path: c.Path, Kind: KindCopy, Repo: c.Repo})
			}
		}
	}
	return out
}

// Match is a target and how well a query fits it.
type Match struct {
	Target
	Score int
}

// Find returns the targets the query fits, best first. A query of several
// words must fit them all; each word fits a target best by its whole name,
// then a path suffix of one (acme/api in github.com/acme/api), a prefix, a
// substring, and last its letters in order.
func Find(targets []Target, query string) []Match {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return nil
	}
	var out []Match
	for _, t := range targets {
		total := 0
		for _, term := range terms {
			best := 0
			for _, w := range t.words {
				best = max(best, score(w, term))
			}
			if best == 0 {
				total = 0
				break
			}
			total += best
		}
		if total > 0 {
			out = append(out, Match{t, total})
		}
	}
	slices.SortStableFunc(out, func(a, b Match) int {
		switch {
		case a.Score != b.Score:
			return b.Score - a.Score
		case kindRank[a.Kind] != kindRank[b.Kind]:
			return kindRank[a.Kind] - kindRank[b.Kind]
		case len(a.Label) != len(b.Label):
			return len(a.Label) - len(b.Label)
		default:
			return strings.Compare(a.Label, b.Label)
		}
	})
	return out
}

// kindRank orders equally good matches: a repo by its name before a
// workspace of the same name, and agents' folders after both.
var kindRank = map[string]int{KindRepo: 0, KindWorkspace: 1, KindReplica: 2, KindCopy: 3}

// Best returns the best matches: those sharing the top score and kind. A
// workspace named after its repo doesn't make the repo ambiguous; two repos
// of one name do.
func Best(matches []Match) []Match {
	if len(matches) == 0 {
		return nil
	}
	n := 1
	for n < len(matches) && matches[n].Score == matches[0].Score && matches[n].Kind == matches[0].Kind {
		n++
	}
	return matches[:n]
}

// OfKind keeps the targets of kind; all of them when kind is "".
func OfKind(targets []Target, kind string) []Target {
	if kind == "" {
		return targets
	}
	return slices.DeleteFunc(slices.Clone(targets), func(t Target) bool { return t.Kind != kind })
}

// score rates how well term fits word: 0 when it doesn't.
func score(word, term string) int {
	switch {
	case word == term:
		return 100
	case strings.HasSuffix(word, "/"+term):
		return 90
	case strings.HasPrefix(word, term):
		return 80
	case strings.Contains(word, term):
		return 60
	}
	// Its letters in order, closer together fitting better.
	gaps, i := 0, 0
	for j := 0; j < len(word) && i < len(term); j++ {
		if word[j] == term[i] {
			i++
		} else if i > 0 {
			gaps++
		}
	}
	if i < len(term) {
		return 0
	}
	return max(1, 40-gaps)
}
