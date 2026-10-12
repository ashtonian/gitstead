package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/state"
)

func TestRecordCopies(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	backup := &gitops.BackupConfig{}
	clean := &gitops.RepoStatus{HeadCommit: "abc", CurrentBranch: "main"}
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
				if r.Status != "clean" || r.LastFetchError != "" || !r.LastFetch.Equal(now) || r.HeadCommit != "abc" {
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
			repo: state.RepoState{LocalPath: "/r", BackupStatus: state.BackupStatus{LastBackupID: "old"}},
			out:  copyOutcome{job: repoAt("/r"), backup: &gitops.BackupResult{ID: "new"}, at: now},
			check: func(t *testing.T, r *state.RepoState) {
				if r.LastBackupID != "new" || !r.LastBackup.Equal(now) || r.LastBackupError != "" {
					t.Errorf("repo = %+v", r)
				}
			},
		},
		{
			name: "a failed backup keeps the last good one",
			repo: state.RepoState{LocalPath: "/r", BackupStatus: state.BackupStatus{LastBackupID: "old", LastBackup: now.Add(-time.Hour)}},
			out:  copyOutcome{job: repoAt("/r"), backup: &gitops.BackupResult{}, backupErr: errors.New("disk full"), at: now},
			check: func(t *testing.T, r *state.RepoState) {
				if r.LastBackupID != "old" || !r.LastBackup.Equal(now.Add(-time.Hour)) || r.LastBackupError != "disk full" {
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

func TestRecordCopies_Member(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	st := &state.State{Workgroups: map[string]*state.WorkgroupState{
		"fleet": {Members: []state.WorkgroupMemberState{{Name: "a1", Path: "/w/a1"}, {Name: "a2", Path: "/w/a2"}}},
	}}
	job := func(name, path string) copyJob {
		return copyJob{workgroup: "fleet", member: name, path: path, backup: &gitops.BackupConfig{}}
	}
	recordCopies(st, []copyOutcome{
		{job: job("a1", "/w/a1"), backup: &gitops.BackupResult{ID: "b1"}, at: now},
		{job: job("a2", "/w/elsewhere"), backup: &gitops.BackupResult{ID: "b2"}, at: now},
	})
	m := st.Workgroups["fleet"].Members
	if m[0].LastBackupID != "b1" || !m[0].LastBackup.Equal(now) {
		t.Errorf("member a1 = %+v", m[0])
	}
	if m[1].LastBackupID != "" {
		t.Errorf("a member moved meanwhile was recorded: %+v", m[1])
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
	app, lib, agent := dir("app"), dir("lib"), dir("agent")
	cfg := config.Defaults()
	st := &state.State{
		Repos: map[string]*state.RepoState{
			"h/o/app": {LocalPath: app, Cloned: true, Tags: []string{"work"}},
			"h/o/lib": {LocalPath: lib, Cloned: true},
			"h/o/old": {LocalPath: dir("old"), Cloned: true, Status: "stale"},
		},
		Workgroups: map[string]*state.WorkgroupState{"fleet": {Members: []state.WorkgroupMemberState{
			{Name: "agent", Path: agent, RepoKey: "h/o/lib"},
			{Name: "app", Path: app, RepoKey: "h/o/app"}, // adopted from the tracked clone
		}}},
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
			if j.offsite != nil {
				label += "+offsite"
			}
			out = append(out, label)
		}
		return out
	}

	if got := strings.Join(labels(&SyncCmd{}), " "); got != "h/o/app h/o/lib fleet/agent" {
		t.Errorf("jobs = %s; want each working copy once, stale repos left out", got)
	}
	if got := strings.Join(labels(&SyncCmd{Tag: "work"}), " "); got != "h/o/app" {
		t.Errorf("jobs for --tag work = %s; members of unselected repos should be left out", got)
	}
	if got := strings.Join(labels(&SyncCmd{NoBackup: true}), " "); got != "h/o/app h/o/lib" {
		t.Errorf("jobs for --no-backup = %s; members are only backed up", got)
	}

	// remote_sync applies to members by the repo they copy: an override that
	// turns it off for lib keeps lib's member local too.
	off := false
	cfg.RemoteSync.Enabled = true
	cfg.Overrides = []config.Override{{Repos: []string{"h/o/lib"}, RemoteSync: &config.RemoteSyncOverride{Enabled: &off}}}
	if got := strings.Join(labels(&SyncCmd{}), " "); got != "h/o/app+offsite h/o/lib fleet/agent" {
		t.Errorf("jobs with remote_sync = %s", got)
	}
}
