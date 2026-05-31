package workgroup

import (
	"context"
	"fmt"

	"github.com/ashtonian/git-mgr/internal/gitops"
)

// MemberHealth is the observed health of a single workgroup member.
type MemberHealth struct {
	Member
	Present   bool
	Branch    string
	Dirty     bool   // tracked changes staged or unstaged
	Untracked bool   // untracked files present
	Locked    bool   // a git lock file is present
	Stuck     string // "rebase" | "merge" | "cherry-pick" | ""
	Upstream  string
	Ahead     int
	Behind    int
	Issues    []string // problems worth flagging to a human
}

// Health is the aggregate health of a workgroup's members.
type Health struct {
	Spec    *Spec
	Members []MemberHealth
}

// OK reports whether every member is set up and in sync (no issues).
func (h *Health) OK() bool {
	for _, m := range h.Members {
		if len(m.Issues) > 0 {
			return false
		}
	}
	return true
}

// ComputeHealth inspects every member of the spec. All checks are local and
// read-only (no network); it assumes remote-tracking refs are already fetched.
func ComputeHealth(ctx context.Context, spec *Spec) *Health {
	h := &Health{Spec: spec}
	for _, m := range spec.Members {
		h.Members = append(h.Members, memberHealth(ctx, spec, m))
	}
	return h
}

func memberHealth(ctx context.Context, spec *Spec, m Member) MemberHealth {
	mh := MemberHealth{Member: m}
	if !gitops.HasGitDir(m.Path) {
		mh.Issues = append(mh.Issues, "missing (not created)")
		return mh
	}
	mh.Present = true

	if locked, _ := gitops.IsLocked(m.Path); locked {
		mh.Locked = true
		mh.Issues = append(mh.Issues, "git lock present")
	}

	rs, err := gitops.GetStatus(ctx, m.Path)
	if err != nil {
		mh.Issues = append(mh.Issues, "status error: "+err.Error())
		return mh
	}
	mh.Branch = rs.CurrentBranch
	mh.Dirty = rs.HasStaged || rs.HasUnstaged
	mh.Untracked = rs.HasUntracked

	switch {
	case rs.InRebase:
		mh.Stuck = "rebase"
	case rs.InMerge:
		mh.Stuck = "merge"
	case rs.InCherryPick:
		mh.Stuck = "cherry-pick"
	}
	if mh.Stuck != "" {
		mh.Issues = append(mh.Issues, mh.Stuck+" in progress")
	}

	// A worktree member is expected to sit on its assigned branch.
	if spec.Kind == "worktree" && m.Branch != "" && rs.CurrentBranch != m.Branch {
		mh.Issues = append(mh.Issues, fmt.Sprintf("on branch %q, expected %q", rs.CurrentBranch, m.Branch))
	}

	if up := gitops.Upstream(ctx, m.Path); up != "" {
		mh.Upstream = up
		if ahead, behind, err := gitops.AheadBehind(ctx, m.Path, up); err == nil {
			mh.Ahead, mh.Behind = ahead, behind
			if behind > 0 {
				mh.Issues = append(mh.Issues, fmt.Sprintf("behind %s by %d", up, behind))
			}
		}
	}
	return mh
}
