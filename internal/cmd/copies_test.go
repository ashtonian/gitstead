package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ashtonian/gitten/internal/config"
	"github.com/ashtonian/gitten/internal/gitops"
	"github.com/ashtonian/gitten/internal/state"
)

func TestRecordCopies(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	backup := &gitops.BackupConfig{}
	clean := &gitops.RepoStatus{CurrentBranch: "main"}
	repoAt := func(path string) copyJob { return copyJob{repoKey: "h/o/r", path: path, backup: backup} }

	tests := []struct {
		name  string
		repo  state.RepoState
		out   copyOutcome
		check func(t *testing.T, r *state.RepoState)
	}{
		{
			name: "a fetch that works clears the last one's error",
			repo: state.RepoState{LocalPath: "/r", Status: "error", LastFetchError: "timeout"},
			out:  copyOutcome{job: repoAt("/r"), fetched: true, status: clean, at: now},
			check: func(t *testing.T, r *state.RepoState) {
				if r.Status != "clean" || r.LastFetchError != "" || !r.LastFetch.Equal(now) || r.CurrentBranch != "main" {
					t.Errorf("repo = %+v", r)
				}
			},
		},
		{
			name: "a failing fetch shows as an error",
			repo: state.RepoState{LocalPath: "/r", Status: "clean"},
			out:  copyOutcome{job: repoAt("/r"), fetched: true, fetchErr: errors.New("timeout"), status: clean, at: now},
			check: func(t *testing.T, r *state.RepoState) {
				if r.Status != "error" || r.LastFetchError != "timeout" || !r.LastFetch.IsZero() {
					t.Errorf("repo = %+v", r)
				}
			},
		},
		{
			name: "git's lock taken is retried next run, not an error",
			repo: state.RepoState{LocalPath: "/r", Status: "clean"},
			out:  copyOutcome{job: repoAt("/r"), fetched: true, fetchErr: fmt.Errorf("fetch: %w", gitops.ErrLocked), status: clean, at: now},
			check: func(t *testing.T, r *state.RepoState) {
				if r.Status != "clean" || r.LastFetchError != "" {
					t.Errorf("repo = %+v", r)
				}
			},
		},
		{
			name: "a backup moves LastBackup",
			repo: state.RepoState{LocalPath: "/r", BackupStatus: state.BackupStatus{LastBackup: now.Add(-time.Hour), LastBackupError: "disk full"}},
			out:  copyOutcome{job: repoAt("/r"), backup: &gitops.BackupResult{ID: "new"}, at: now},
			check: func(t *testing.T, r *state.RepoState) {
				if !r.LastBackup.Equal(now) || r.LastBackupError != "" {
					t.Errorf("repo = %+v", r)
				}
			},
		},
		{
			name: "a failed backup keeps the last good one",
			repo: state.RepoState{LocalPath: "/r", BackupStatus: state.BackupStatus{LastBackup: now.Add(-time.Hour)}},
			out:  copyOutcome{job: repoAt("/r"), backup: &gitops.BackupResult{}, backupErr: errors.New("disk full"), at: now},
			check: func(t *testing.T, r *state.RepoState) {
				if !r.LastBackup.Equal(now.Add(-time.Hour)) || r.LastBackupError != "disk full" {
					t.Errorf("repo = %+v", r)
				}
			},
		},
		{
			name: "a repo moved since the run read the state is left alone",
			repo: state.RepoState{LocalPath: "/moved", Status: "clean"},
			out:  copyOutcome{job: repoAt("/r"), fetched: true, fetchErr: errors.New("gone"), at: now},
			check: func(t *testing.T, r *state.RepoState) {
				if r.Status != "clean" || r.LastFetchError != "" {
					t.Errorf("repo = %+v", r)
				}
			},
		},
		{
			name: "stale stays stale",
			repo: state.RepoState{LocalPath: "/r", Status: "stale"},
			out:  copyOutcome{job: repoAt("/r"), status: clean, at: now},
			check: func(t *testing.T, r *state.RepoState) {
				if r.Status != "stale" {
					t.Errorf("repo = %+v", r)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := tt.repo
			st := &state.State{Repos: map[string]*state.RepoState{"h/o/r": &repo}}
			recordCopies(st, []copyOutcome{tt.out})
			tt.check(t, st.Repos["h/o/r"])
		})
	}
}

func TestRecordCopies_WorkspaceCopy(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	st := &state.State{Workspaces: map[string]*state.WorkspaceState{
		"app": {Name: "app", Replicas: []state.ReplicaState{
			{Agent: "a1", Path: "/w/a1", Copies: []state.CopyState{{Path: "/w/a1"}}},
			{Agent: "a2", Path: "/w/a2", Copies: []state.CopyState{{Path: "/w/a2"}}},
		}},
	}}
	job := func(agent, path string) copyJob {
		return copyJob{workspace: "app", agent: agent, path: path, backup: &gitops.BackupConfig{}}
	}
	recordCopies(st, []copyOutcome{
		{job: job("a1", "/w/a1"), backup: &gitops.BackupResult{ID: "b1"}, at: now},
		{job: job("a2", "/w/elsewhere"), backup: &gitops.BackupResult{ID: "b2"}, at: now},
	})
	rs := st.Workspaces["app"].Replicas
	if !rs[0].Copies[0].LastBackup.Equal(now) {
		t.Errorf("a1's copy = %+v", rs[0].Copies[0])
	}
	if !rs[1].Copies[0].LastBackup.IsZero() {
		t.Errorf("a copy moved meanwhile was recorded: %+v", rs[1].Copies[0])
	}
}

func TestSyncJobs(t *testing.T) {
	root := t.TempDir()
	dir := func(name string) string {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Join(p, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	app, lib, agent, clone := dir("app"), dir("lib"), dir("agent"), dir("clone")
	cfg := config.Defaults()
	st := &state.State{
		Repos: map[string]*state.RepoState{
			"h/o/app": {LocalPath: app, Cloned: true, Tags: []string{"work"}},
			"h/o/lib": {LocalPath: lib, Cloned: true},
			"h/o/old": {LocalPath: dir("old"), Cloned: true, Status: "stale"},
		},
		Workspaces: map[string]*state.WorkspaceState{
			"fleet": {Name: "fleet", Isolation: "worktree", Replicas: []state.ReplicaState{
				{Agent: "agent", Path: agent, Copies: []state.CopyState{{Repo: "h/o/lib", Path: agent}}},
				{Agent: "app", Path: app, Copies: []state.CopyState{{Repo: "h/o/app", Path: app}}}, // adopted from the tracked clone
			}},
			"clones": {Name: "clones", Isolation: "clone", Replicas: []state.ReplicaState{
				{Agent: "a1", Path: clone, Copies: []state.CopyState{{Repo: "h/o/lib", Path: clone}}},
			}},
		},
	}
	labels := func(c *SyncCmd) []string {
		pass, err := selection{c.Repos, c.Tag, c.Set}.predicate(cfg)
		if err != nil {
			t.Fatal(err)
		}
		jobs, _ := c.jobs(cfg, st, pass, "laptop-1a2b3c")
		var out []string
		for _, j := range jobs {
			label := j.label
			if j.fetch {
				label += "+fetch"
			}
			if j.refresh != "" {
				label += "+refresh"
			}
			if j.offsite != nil {
				label += "+offsite"
			}
			out = append(out, label)
		}
		return out
	}

	if got := strings.Join(labels(&SyncCmd{}), " "); got != "h/o/app+fetch h/o/lib+fetch h/o/old clones/a1+refresh fleet/agent" {
		t.Errorf("jobs = %s; want each working copy once, the stale repo backed up but not fetched, clones refreshed", got)
	}
	if got := strings.Join(labels(&SyncCmd{Tag: "work"}), " "); got != "h/o/app+fetch" {
		t.Errorf("jobs for --tag work = %s; copies of unselected repos should be left out", got)
	}
	if got := strings.Join(labels(&SyncCmd{NoBackup: true}), " "); got != "h/o/app+fetch h/o/lib+fetch h/o/old clones/a1+refresh" {
		t.Errorf("jobs for --no-backup = %s; only clones have more to do than a backup", got)
	}
	if got := strings.Join(labels(&SyncCmd{NoBackup: true, NoFetch: true}), " "); got != "h/o/app h/o/lib h/o/old" {
		t.Errorf("jobs for --no-backup --no-fetch = %s", got)
	}

	// remote_sync applies to copies by the repo they copy: an override that
	// turns it off for lib keeps lib's copy local too, unless its workspace
	// turns it on.
	off := false
	cfg.RemoteSync.Enabled = true
	cfg.Overrides = []config.Override{{Repos: []string{"h/o/lib"}, RemoteSync: &config.RemoteSyncOverride{Enabled: &off}}}
	if got := strings.Join(labels(&SyncCmd{}), " "); got != "h/o/app+fetch+offsite h/o/lib+fetch h/o/old clones/a1+refresh fleet/agent" {
		t.Errorf("jobs with remote_sync = %s", got)
	}
	on := true
	cfg.Workspaces = []config.Workspace{{Name: "fleet", Repos: []string{"h/o/lib"}, RemoteSync: &config.RemoteSyncOverride{Enabled: &on}}}
	if got := strings.Join(labels(&SyncCmd{}), " "); got != "h/o/app+fetch+offsite h/o/lib+fetch h/o/old clones/a1+refresh fleet/agent+offsite" {
		t.Errorf("jobs with the workspace's remote_sync = %s", got)
	}
}

// A fetch runs at least once, whatever retries says: it is never reported
// done without having run.
func TestFetchWithRetry_RunsOnce(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", filepath.Join(t.TempDir(), "gone.git")}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := fetchWithRetry(context.Background(), repo, "", config.SyncConfig{Retries: -1}); err == nil {
		t.Error("a fetch from a missing remote reported success")
	}
}
