package workspace

import (
	"context"
	"fmt"

	"github.com/ashtonian/git-mgr/internal/gitops"
)

// Targets lists what commands act on in a workspace: every copy, or with
// shared isolation the shared folder, once.
func (s *Spec) Targets() []Located {
	if !s.Shared() {
		return s.Copies()
	}
	var out []Located
	seen := map[string]bool{}
	for _, r := range s.Replicas {
		if !seen[r.Path] {
			seen[r.Path] = true
			out = append(out, Located{Workspace: s.Name, Copy: Copy{Path: r.Path}})
		}
	}
	return out
}

// CopyHealth is what a copy's working tree looks like now.
type CopyHealth struct {
	Located
	Present   bool
	Branch    string
	Dirty     bool   // tracked changes, staged or not
	Untracked bool   // untracked files
	Locked    bool   // a git lock file is present
	Stuck     string // rebase, merge or cherry-pick under way
	Upstream  string
	Ahead     int
	Behind    int
	Issues    []string // what a person should look at
}

// State is the copy's state in a word: missing, rebase, merge, cherry-pick,
// dirty, untracked or clean.
func (h CopyHealth) State() string {
	switch {
	case !h.Present:
		return "missing"
	case h.Stuck != "":
		return h.Stuck
	case h.Dirty:
		return "dirty"
	case h.Untracked:
		return "untracked"
	default:
		return "clean"
	}
}

// OK reports whether every copy is set up and up to date.
func OK(hs []CopyHealth) bool {
	return Flagged(hs) == 0
}

// Flagged counts the copies with issues.
func Flagged(hs []CopyHealth) int {
	n := 0
	for _, h := range hs {
		if len(h.Issues) > 0 {
			n++
		}
	}
	return n
}

// Health inspects targets, locally: it reads only what was last fetched.
func Health(ctx context.Context, spec *Spec, targets []Located) []CopyHealth {
	out := make([]CopyHealth, 0, len(targets))
	for _, t := range targets {
		out = append(out, copyHealth(ctx, spec, t))
	}
	return out
}

func copyHealth(ctx context.Context, spec *Spec, t Located) CopyHealth {
	h := CopyHealth{Located: t}
	if spec.Shared() {
		h.Present = exists(t.Path)
		if !h.Present {
			h.Issues = append(h.Issues, "missing")
		}
		if !gitops.HasGitDir(t.Path) {
			return h
		}
	} else if !gitops.HasGitDir(t.Path) {
		h.Issues = append(h.Issues, "missing (not created)")
		return h
	}
	h.Present = true

	if locked, _ := gitops.IsLocked(ctx, t.Path); locked {
		h.Locked = true
		h.Issues = append(h.Issues, "git lock present")
	}
	rs, err := gitops.GetStatus(ctx, t.Path)
	if err != nil {
		h.Issues = append(h.Issues, "status error: "+err.Error())
		return h
	}
	h.Branch = rs.CurrentBranch
	h.Dirty = rs.HasStaged || rs.HasUnstaged
	h.Untracked = rs.HasUntracked
	switch {
	case rs.InRebase:
		h.Stuck = "rebase"
	case rs.InMerge:
		h.Stuck = "merge"
	case rs.InCherryPick:
		h.Stuck = "cherry-pick"
	}
	if h.Stuck != "" {
		h.Issues = append(h.Issues, h.Stuck+" in progress")
	}
	if t.Branch != "" && rs.CurrentBranch != t.Branch {
		h.Issues = append(h.Issues, fmt.Sprintf("on branch %q, expected %q", rs.CurrentBranch, t.Branch))
	}

	// An agent's branch usually has no upstream until it is published, so
	// without one it is compared with what it was branched from.
	h.Upstream = gitops.Upstream(ctx, t.Path)
	compare := h.Upstream
	if compare == "" && !spec.Shared() {
		compare = spec.BaseRef
		if compare == "" {
			compare = "origin/HEAD"
		}
	}
	if compare != "" {
		if ahead, behind, err := gitops.AheadBehind(ctx, t.Path, compare); err == nil {
			h.Ahead, h.Behind = ahead, behind
			if behind > 0 {
				h.Issues = append(h.Issues, fmt.Sprintf("behind %s by %d", compare, behind))
			}
		}
	}
	return h
}
