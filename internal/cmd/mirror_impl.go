package cmd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/ctxutil"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/pathutil"
	"github.com/ashtonian/git-mgr/internal/repourl"
	"github.com/ashtonian/git-mgr/internal/state"
)

// MirrorCmd makes or updates the mirrors of the repos mirroring is on for.
type MirrorCmd struct {
	Repos string `help:"Only repos matching this glob." predictor:"repo"`
	Tag   string `help:"Only repos with this tag." predictor:"tag"`
	Set   string `help:"Only repos in this named set (from config 'sets:')." predictor:"set"`
	Force bool   `help:"Update every selected mirror, however recently it was."`
}

func (c *MirrorCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	pass, err := selection{c.Repos, c.Tag, c.Set}.predicate(cfg)
	if err != nil {
		return err
	}
	jobs, recent := mirrorJobs(cfg, st, pass, c.Force)
	if len(jobs) == 0 && recent == 0 {
		fmt.Println("No repo to mirror: turn mirror.enabled on, or mirror some with accounts[].mirror or overrides[].mirror.")
		return ctx.finish(map[string]any{"mirrors": []mirrorJSON{}, "skipped_recent": 0}, nil)
	}
	outcomes := runMirrors(ctx.GoCtx(), jobs, cfg.Concurrency)
	if err := recordMirrors(cfg, st, outcomes); err != nil {
		return err
	}
	failed := 0
	for _, o := range outcomes {
		switch {
		case o.err != nil:
			failed++
			fmt.Fprintf(os.Stderr, "  x %s: %v\n", o.job.key, o.err)
		case !ctx.Quiet && o.created:
			fmt.Printf("  + %s: mirrored at %s\n", o.job.key, tildify(o.job.path))
		case !ctx.Quiet:
			fmt.Printf("  ~ %s: updated\n", o.job.key)
		}
	}
	if !ctx.Quiet {
		fmt.Printf("Mirrors: %d updated, %d skipped (within mirror.interval; --force updates them), %d failed\n", len(outcomes)-failed, recent, failed)
	}
	logEvent(ctx, "mirror", "", fmt.Sprintf("updated=%d failed=%d skipped_recent=%d", len(outcomes)-failed, failed, recent))
	return ctx.finish(map[string]any{"mirrors": mirrorsJSON(outcomes), "skipped_recent": recent},
		outcome(failed, len(outcomes), "mirrors failed"))
}

// mirrorJob is one repo's mirror to make or update.
type mirrorJob struct {
	key, url string
	// path is where config puts the mirror; from is where it was, when the
	// repo has been renamed since.
	path, from string
	opts       gitops.MirrorOptions
	timeout    time.Duration
}

type mirrorOutcome struct {
	job     mirrorJob
	created bool
	err     error
}

// mirrorJobs lists the mirrors due: of each selected repo mirroring is on
// for, last updated more than mirror.interval ago or failing (any, with
// force). A stale repo's upstream is gone, so its mirror is left as it was.
func mirrorJobs(cfg *config.Config, st *state.State, pass func(string, *state.RepoState) bool, force bool) (jobs []mirrorJob, recent int) {
	for _, key := range slices.Sorted(maps.Keys(st.Repos)) {
		repo := st.Repos[key]
		m := cfg.MirrorFor(key, repo.DiscoveredBy)
		if !m.Enabled || !pass(key, repo) || repo.CloneURL == "" || repo.Status == "stale" {
			continue
		}
		if !force && repo.LastMirrorError == "" && !repo.LastMirror.IsZero() && time.Since(repo.LastMirror) < cfg.Mirror.Interval {
			recent++
			continue
		}
		job := mirrorJob{
			key: key, url: repo.CloneURL, path: cfg.MirrorPath(key), from: repo.MirrorPath,
			opts: gitops.MirrorOptions{LFS: m.LFS, KeepRewritten: cfg.Mirror.KeepRewritten}, timeout: cfg.Sync.CloneTimeout,
		}
		if !repourl.IsHTTP(repo.CloneURL) {
			job.opts.SSHKey = cfg.ResolveSSHKey(key, repo.DiscoveredBy)
		}
		jobs = append(jobs, job)
	}
	return jobs, recent
}

// runMirrors runs the jobs, limit at once.
func runMirrors(ctx context.Context, jobs []mirrorJob, limit int) []mirrorOutcome {
	outcomes := make([]mirrorOutcome, len(jobs))
	var g errgroup.Group
	g.SetLimit(max(limit, 1))
	for i, job := range jobs {
		g.Go(func() error {
			outcomes[i] = runMirror(ctx, job)
			return nil
		})
	}
	_ = g.Wait()
	return outcomes
}

func runMirror(ctx context.Context, job mirrorJob) mirrorOutcome {
	o := mirrorOutcome{job: job}
	// A repo renamed since keeps its mirror, moved to its new name.
	if job.from != "" && job.from != job.path && exists(job.from) && !exists(job.path) {
		if err := pathutil.Move(job.from, job.path); err != nil {
			o.err = fmt.Errorf("moving its mirror from %s: %w", job.from, err)
			return o
		}
	}
	mctx, cancel := ctxutil.WithTimeout(ctx, job.timeout)
	defer cancel()
	o.created, o.err = gitops.Mirror(mctx, job.url, job.path, job.opts)
	o.err = ctxutil.TimedOut(mctx, o.err, "mirror", job.timeout, "sync.clone_timeout")
	return o
}

// recordMirrors saves what the mirror runs did.
func recordMirrors(cfg *config.Config, st *state.State, outcomes []mirrorOutcome) error {
	if len(outcomes) == 0 {
		return nil
	}
	return st.Update(cfg.LockTimeout, func(st *state.State) error {
		for _, o := range outcomes {
			repo := st.Repos[o.job.key]
			if repo == nil {
				continue
			}
			if exists(o.job.path) {
				repo.MirrorPath = o.job.path
			}
			if o.err != nil {
				repo.LastMirrorError = o.err.Error()
				continue
			}
			repo.LastMirror, repo.LastMirrorError = time.Now(), ""
		}
		return nil
	})
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return !errors.Is(err, fs.ErrNotExist)
}
