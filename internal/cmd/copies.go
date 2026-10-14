package cmd

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/ashtonian/gitten/internal/config"
	"github.com/ashtonian/gitten/internal/ctxutil"
	"github.com/ashtonian/gitten/internal/gitops"
	"github.com/ashtonian/gitten/internal/state"
)

// copyJob is one working copy a run backs up and fetches: a tracked repo or
// a workspace copy.
type copyJob struct {
	label     string // the repo key, or workspace/agent[/folder]
	repoKey   string // "" for a workspace copy
	workspace string // "" for a repo
	agent     string
	path      string

	backup        *gitops.BackupConfig // nil skips the backup
	backupTimeout time.Duration        // bounds the backup and the status read
	fetch         bool
	// update fast-forwards the clone after a successful fetch (sync.update).
	update bool
	// refresh is the path of the repo's clone a workspace clone is brought
	// up to date from, without the network, in place of a fetch.
	refresh string
	sshKey  string
	sync    config.SyncConfig // fetch retries; timeout_per_repo bounds the fetch and the offsite push
	offsite *offsiteSpec      // nil keeps everything local
}

// copyOutcome is what a run did to one copy.
type copyOutcome struct {
	job       copyJob
	backup    *gitops.BackupResult
	backupErr error
	fetched   bool // a fetch was attempted
	fetchErr  error
	// updated reports a fast-forward; updateSkipped why there was none when
	// the clone was behind.
	updated       bool
	updateSkipped string
	updateErr     error
	// offsite is what the offsite push left, nil when it didn't run;
	// offsitePlan is what it did, or would do on a dry run.
	offsite     *gitops.OffsiteRecord
	offsitePlan gitops.OffsitePlan
	offsiteErr  error
	// offsiteSubs is what taking each checked-out submodule offsite did, by
	// path; nil when they weren't listed.
	offsiteSubs map[string]offsitePush
	status      *gitops.RepoStatus
	at          time.Time
}

// runCopies backs up and fetches each copy, at most limit at a time. It
// holds no lock: the outcomes are recorded afterwards with recordCopies.
func runCopies(ctx context.Context, log *slog.Logger, jobs []copyJob, limit int) []copyOutcome {
	outcomes := make([]copyOutcome, len(jobs))
	var g errgroup.Group
	g.SetLimit(max(limit, 1))
	for i, job := range jobs {
		g.Go(func() error {
			outcomes[i] = runCopy(ctx, log, job)
			return nil
		})
	}
	_ = g.Wait()
	return outcomes
}

func runCopy(ctx context.Context, log *slog.Logger, job copyJob) copyOutcome {
	o := copyOutcome{job: job}
	// Back up first, on its own deadline, so a slow fetch can't starve it.
	if job.backup != nil {
		bctx, cancel := ctxutil.WithTimeout(ctx, job.backupTimeout)
		o.backup, o.backupErr = gitops.Backup(bctx, job.path, *job.backup)
		o.backupErr = ctxutil.TimedOut(bctx, o.backupErr, "backup", job.backupTimeout, "sync.backup_timeout")
		cancel()
		if o.backup != nil && o.backup.Dirty() {
			log.Debug("backup", "copy", job.label, "outcome", o.backup.Outcome.String(), "id", o.backup.ID)
		}
		if o.backup != nil && len(o.backup.Skipped) > 0 {
			log.Warn("untracked paths left out of backup", "copy", job.label, "count", len(o.backup.Skipped), "paths", skippedSummary(o.backup.Skipped))
		}
	}
	switch {
	case job.fetch:
		fctx, cancel := ctxutil.WithTimeout(ctx, job.sync.TimeoutPerRepo)
		o.fetched, o.fetchErr = true, fetchWithRetry(fctx, job.path, job.sshKey, job.sync)
		o.fetchErr = ctxutil.TimedOut(fctx, o.fetchErr, "fetch", job.sync.TimeoutPerRepo, "sync.timeout_per_repo")
		cancel()
		if job.update && o.fetchErr == nil {
			uctx, cancel := ctxutil.WithTimeout(ctx, job.sync.TimeoutPerRepo)
			o.updated, o.updateSkipped, o.updateErr = gitops.FastForward(uctx, job.path)
			o.updateErr = ctxutil.TimedOut(uctx, o.updateErr, "fast-forward", job.sync.TimeoutPerRepo, "sync.timeout_per_repo")
			cancel()
		}
	case job.refresh != "":
		fctx, cancel := ctxutil.WithTimeout(ctx, job.sync.TimeoutPerRepo)
		o.fetched, o.fetchErr = true, gitops.RefreshFromSource(fctx, job.path, job.refresh)
		o.fetchErr = ctxutil.TimedOut(fctx, o.fetchErr, "refresh from the repo's clone", job.sync.TimeoutPerRepo, "sync.timeout_per_repo")
		cancel()
	}
	// Offsite after the fetch, which tells which commits a remote already has.
	if job.offsite != nil {
		octx, cancel := ctxutil.WithTimeout(ctx, job.sync.TimeoutPerRepo)
		o.pushOffsite(octx, job)
		o.offsiteErr = ctxutil.TimedOut(octx, o.offsiteErr, "offsite push", job.sync.TimeoutPerRepo, "sync.timeout_per_repo")
		cancel()
	}
	if job.repoKey != "" {
		sctx, cancel := ctxutil.WithTimeout(ctx, job.backupTimeout)
		if rs, err := gitops.GetStatus(sctx, job.path); err == nil {
			o.status = rs
		}
		cancel()
	}
	o.at = time.Now()
	return o
}

// failed reports the copy's failures; a fetch that found git's lock taken
// is not one, it is retried next run.
func (o copyOutcome) failed() []error {
	var errs []error
	if o.backupErr != nil {
		errs = append(errs, o.backupErr)
	}
	if o.fetchErr != nil && !errors.Is(o.fetchErr, gitops.ErrLocked) {
		errs = append(errs, o.fetchErr)
	}
	if o.offsiteErr != nil {
		errs = append(errs, o.offsiteErr)
	}
	if o.updateErr != nil {
		errs = append(errs, o.updateErr)
	}
	return errs
}

// recordCopies stores outcomes in st. Run it under the state lock. A copy
// moved or untracked since the run read the state is left alone.
func recordCopies(st *state.State, outcomes []copyOutcome) {
	for _, o := range outcomes {
		if o.job.repoKey == "" {
			if c := st.CopyAt(o.job.workspace, o.job.agent, o.job.path); c != nil {
				if o.job.backup != nil {
					c.RecordBackup(o.saved(), o.backupErr, o.at)
				}
				o.recordOffsite(&c.OffsiteStatus)
			}
			continue
		}
		if repo := st.Repos[o.job.repoKey]; repo != nil && repo.LocalPath == o.job.path {
			o.recordRepo(repo)
		}
	}
}

func (o copyOutcome) recordRepo(repo *state.RepoState) {
	if o.job.backup != nil {
		repo.RecordBackup(o.saved(), o.backupErr, o.at)
	}
	o.recordOffsite(&repo.OffsiteStatus)
	if o.fetched {
		switch {
		case o.fetchErr == nil:
			repo.LastFetch, repo.LastFetchError = o.at, ""
		case errors.Is(o.fetchErr, gitops.ErrLocked):
		default:
			repo.LastFetchError = o.fetchErr.Error()
		}
	}
	if o.status != nil {
		repo.CurrentBranch = o.status.CurrentBranch
	}
	// A failing fetch shows until a fetch succeeds; stale is discovery's call.
	switch {
	case repo.Status == "stale":
	case repo.LastFetchError != "":
		repo.Status = "error"
	case o.status != nil:
		repo.Status = o.status.StatusString()
	}
}

// saved reports that a backup holds the copy's current state.
func (o copyOutcome) saved() bool { return o.backup != nil && o.backup.ID != "" }

// repoJob is the job for a tracked repo, backing up when backup is set.
func repoJob(cfg *config.Config, key string, repo *state.RepoState, backup *gitops.BackupConfig) copyJob {
	repoCfg := cfg.ResolveForRepo(key)
	return copyJob{
		label: key, repoKey: key, path: repo.LocalPath,
		backup: backup, backupTimeout: repoCfg.Sync.BackupTimeout,
		sshKey: cfg.ResolveSSHKey(key, repo.DiscoveredBy), sync: repoCfg.Sync,
	}
}

func fetchWithRetry(ctx context.Context, repoPath, sshKey string, cfg config.SyncConfig) error {
	var lastErr error
	for attempt := 0; attempt <= max(cfg.Retries, 0); attempt++ {
		if attempt > 0 {
			backoff := cfg.RetryBackoff * time.Duration(1<<uint(attempt-1))
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		lastErr = gitops.Fetch(ctx, repoPath, sshKey)
		if lastErr == nil {
			return nil
		}
		// A lock is transient; don't burn retries waiting on another process.
		if errors.Is(lastErr, gitops.ErrLocked) {
			return lastErr
		}
	}
	return lastErr
}
