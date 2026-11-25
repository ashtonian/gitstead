package workspace

import (
	"cmp"
	"context"
	"fmt"

	"github.com/ashtonian/gitstead/internal/gitops"
)

// Baseline is what a copy's checked-out branch is compared with: ws status
// and ws check measure the copy against it, and ws pull brings the copy up
// to it.
type Baseline struct {
	// Upstream is the branch's upstream; "" when it has none.
	Upstream string
	// Ref is what the copy is compared with, named as the copy resolves it:
	// Upstream, else what the branch started from. "" when there is nothing
	// to compare with.
	Ref string
}

// Baseline returns what copy t is compared with. An agent's branch usually
// has no upstream until it is published, so without one it is compared with
// what it was branched from: the workspace's base ref, resolved as new
// branches resolve it, else its repo's default branch (see
// gitops.DefaultBase). A linked worktree sees its repo's refs; a clone, its
// own view of origin, which a refresh from the repo's clone updates. A shared
// folder's branch is nobody's to have started from, and a repo that names no
// default branch has nothing to compare with.
func (s *Spec) Baseline(ctx context.Context, t Located) Baseline {
	b := Baseline{Upstream: gitops.Upstream(ctx, t.Path)}
	switch {
	case b.Upstream != "":
		b.Ref = b.Upstream
	case s.Shared():
	case s.BaseRef != "":
		b.Ref = gitops.NameBase(ctx, t.Path, s.BaseRef)
	default:
		// A copy whose repo gitstead can't find names its own default branch.
		b.Ref = gitops.DefaultBase(ctx, cmp.Or(SourceOf(ctx, t.Copy), t.Path))
	}
	return b
}

// UpdateToBase brings the copy at path, whose branch has no upstream, up to
// ref, what its branch started from (see Baseline), as the copy last
// fetched it: a fast-forward, or with rebase a rebase onto it. It returns
// what it did, or with skipped why it left the copy as it was: diverged from
// ref without rebase, a change in the way, git busy in it, a rebase that
// stopped.
func UpdateToBase(ctx context.Context, path, ref string, rebase bool) (out string, skipped bool, err error) {
	if ref == "" {
		return "it has no upstream, and nothing it started from to compare with", true, nil
	}
	update, did := gitops.FastForwardTo, "fast-forwarded to "
	if rebase {
		update, did = gitops.Rebase, "rebased onto "
	}
	updated, reason, err := update(ctx, path, ref)
	switch {
	case err != nil:
		return "", false, fmt.Errorf("bringing it up to %s: %w", ref, err)
	case reason != "":
		return reason, true, nil
	case !updated:
		return "up to date with " + ref, false, nil
	}
	return did + ref, false, nil
}
