package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/reconcile"
	"github.com/ashtonian/git-mgr/internal/state"
	"github.com/ashtonian/git-mgr/internal/workspace"
)

// changesJSON is what the plans a command applied did: each change made,
// failed or skipped, what was changed by hand and left alone, and what
// config asks for that wasn't done.
type changesJSON struct {
	Changes []changeJSON        `json:"changes"`
	Drift   []reconcile.Drift   `json:"drift,omitempty"`
	Blocked []reconcile.Blocked `json:"blocked,omitempty"`
	// Errors are failures outside any plan, such as a path adopt couldn't
	// read.
	Errors []targetError `json:"errors,omitempty"`
}

// changeJSON is one change: the action as plan --json prints it, and how it
// went.
type changeJSON struct {
	reconcile.Action
	OK      bool   `json:"ok"`
	Skipped bool   `json:"skipped,omitempty"`
	Info    string `json:"info,omitempty"`
	Error   string `json:"error,omitempty"`
}

// noteChanges logs what applying plan did and keeps it for --json.
func (c *Context) noteChanges(plan *reconcile.Plan, results []reconcile.Result) {
	logResults(c, results)
	if c.changes.Changes == nil {
		c.changes.Changes = []changeJSON{}
	}
	for _, r := range results {
		ch := changeJSON{Action: r.Action, OK: r.Err == nil, Info: r.Info}
		switch {
		case errors.Is(r.Err, reconcile.ErrSkipped):
			ch.Skipped = true
		case r.Err != nil:
			ch.Error = r.Err.Error()
		}
		c.changes.Changes = append(c.changes.Changes, ch)
	}
	if plan != nil {
		c.changes.Drift = append(c.changes.Drift, plan.Drift...)
		c.changes.Blocked = append(c.changes.Blocked, plan.Blocked...)
	}
}

// noteError keeps a failure outside any plan for --json.
func (c *Context) noteError(target string, err error) {
	c.changes.Errors = append(c.changes.Errors, targetError{target, err.Error()})
}

// finishChanges ends a command that applies plans: under --json it prints
// what they did, then returns err.
func (c *Context) finishChanges(err error) error {
	if c.changes.Changes == nil {
		c.changes.Changes = []changeJSON{}
	}
	return c.finish(c.changes, err)
}

// copyJSON is what a run did to one tracked repo or workspace copy: its
// backup, fetch (or refresh from its repo's clone) and offsite push.
type copyJSON struct {
	Repo      string `json:"repo,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	Agent     string `json:"agent,omitempty"`
	Path      string `json:"path"`
	OK        bool   `json:"ok"`
	// Backup is "clean", "saved" or "unchanged"; "" when it wasn't backed up.
	Backup      string `json:"backup,omitempty"`
	BackupID    string `json:"backup_id,omitempty"`
	BackupError string `json:"backup_error,omitempty"`
	// Submodules maps each submodule backed up with it to its backup's ID.
	Submodules map[string]string `json:"submodules,omitempty"`
	LeftOut    []string          `json:"left_out,omitempty"` // untracked files too big to back up
	Fetched    bool              `json:"fetched,omitempty"`
	FetchError string            `json:"fetch_error,omitempty"`
	// Updated: sync.update fast-forwarded it; UpdateSkipped says why it
	// didn't, for one that was behind.
	Updated       bool   `json:"updated,omitempty"`
	UpdateSkipped string `json:"update_skipped,omitempty"`
	UpdateError   string `json:"update_error,omitempty"`
	// Offsite is what the push did: refs written or deleted; nil when it
	// didn't run.
	Offsite      []string `json:"offsite,omitempty"`
	OffsiteError string   `json:"offsite_error,omitempty"`
}

func copiesJSON(outcomes []copyOutcome) []copyJSON {
	out := make([]copyJSON, 0, len(outcomes))
	for _, o := range outcomes {
		c := copyJSON{
			Repo: o.job.repoKey, Workspace: o.job.workspace, Agent: o.job.agent, Path: o.job.path,
			OK: len(o.failed()) == 0, BackupError: errString(o.backupErr),
			Fetched: o.fetched, FetchError: errString(o.fetchErr), OffsiteError: errString(o.offsiteErr),
			Updated: o.updated, UpdateSkipped: o.updateSkipped, UpdateError: errString(o.updateErr),
		}
		if o.backup != nil {
			c.Backup, c.BackupID = o.backup.Outcome.String(), o.backup.ID
			for path, sub := range o.backup.Submodules {
				if c.Submodules == nil {
					c.Submodules = map[string]string{}
				}
				c.Submodules[path] = sub.ID
			}
			for _, s := range o.backup.Skipped {
				c.LeftOut = append(c.LeftOut, s.Path)
			}
		}
		if o.offsite != nil || len(o.offsitePlan.Push) > 0 {
			c.Offsite = append([]string{}, o.offsitePlan.Push...)
		}
		out = append(out, c)
	}
	return out
}

// mirrorJSON is what a run did to one repo's mirror.
type mirrorJSON struct {
	Repo    string `json:"repo"`
	Path    string `json:"path"`
	OK      bool   `json:"ok"`
	Created bool   `json:"created,omitempty"`
	Error   string `json:"error,omitempty"`
}

func mirrorsJSON(outcomes []mirrorOutcome) []mirrorJSON {
	out := make([]mirrorJSON, 0, len(outcomes))
	for _, o := range outcomes {
		out = append(out, mirrorJSON{Repo: o.job.key, Path: o.job.path, OK: o.err == nil, Created: o.created, Error: errString(o.err)})
	}
	return out
}

// backupJSON is one backup: what it holds and the commands that restore it,
// run in the copy.
type backupJSON struct {
	ID       string   `json:"id"`
	CopyID   string   `json:"copy_id,omitempty"`
	Ref      string   `json:"ref,omitempty"`
	Commit   string   `json:"commit,omitempty"`
	PatchDir string   `json:"patch_dir,omitempty"`
	Branch   string   `json:"branch,omitempty"`
	LeftOut  []string `json:"left_out,omitempty"`
	// Submodules are the submodules' backups restored with it, by path.
	Submodules map[string]gitops.SubmoduleBackup `json:"submodules,omitempty"`
	Restore    []string                          `json:"restore"`
}

func backupInfoJSON(b gitops.BackupInfo) backupJSON {
	j := backupJSON{ID: b.ID, CopyID: b.CopyID, Ref: b.Ref, Commit: b.Commit, PatchDir: b.PatchDir, Submodules: b.Submodules, Restore: b.RestoreCommands()}
	if b.Meta != nil {
		j.Branch = b.Meta.Branch
		for _, s := range b.Meta.Skipped {
			j.LeftOut = append(j.LeftOut, s.Path)
		}
	}
	return j
}

// restoreJSON is what restoring a backup did: printed its steps, restored it
// into a scratch directory, or applied it.
type restoreJSON struct {
	Target  string     `json:"target"`
	Path    string     `json:"path"`
	Backup  backupJSON `json:"backup"`
	Into    string     `json:"into,omitempty"`
	Applied bool       `json:"applied,omitempty"`
	Error   string     `json:"error,omitempty"`
}

// retiredJSON is what retiring copies did: the copies removed, those kept
// and why, and the archives of their unique work.
type retiredJSON struct {
	Removed  []copyRefJSON        `json:"removed"`
	Kept     []targetError        `json:"kept,omitempty"`
	Archives []state.ArchiveEntry `json:"archives,omitempty"`
}

// copyRefJSON names a workspace copy.
type copyRefJSON struct {
	Copy string `json:"copy"`
	Path string `json:"path"`
}

func (d *retiredJSON) add(spec *workspace.Spec, r workspace.Retired) {
	if d.Removed == nil {
		d.Removed = []copyRefJSON{}
	}
	for _, l := range r.Removed {
		d.Removed = append(d.Removed, copyRefJSON{l.Label(spec), l.Path})
	}
	for _, k := range r.Kept {
		d.Kept = append(d.Kept, targetError{k.Label(spec), k.Err.Error()})
	}
	d.Archives = append(d.Archives, r.Archives...)
}

// opJSON is what an operation did to one workspace copy.
type opJSON struct {
	Workspace string `json:"workspace"`
	Agent     string `json:"agent,omitempty"`
	Repo      string `json:"repo,omitempty"`
	Path      string `json:"path"`
	OK        bool   `json:"ok"`
	Skipped   bool   `json:"skipped,omitempty"`
	Output    string `json:"output,omitempty"`
	Error     string `json:"error,omitempty"`
}

func opsJSON(results []workspace.OpResult) []opJSON {
	out := make([]opJSON, 0, len(results))
	for _, r := range results {
		o := opJSON{Workspace: r.Workspace, Agent: r.Agent, Repo: r.Repo, Path: r.Path, OK: r.Err == nil, Skipped: r.Skipped, Output: r.Output}
		if r.Err != nil {
			o.Error = r.Err.Error()
		}
		out = append(out, o)
	}
	return out
}

// finish ends a command: under --json it prints doc first, then returns
// err either way, which sets the exit code.
func (c *Context) finish(doc any, err error) error {
	if c.JSON {
		if eerr := c.Emit(doc); eerr != nil {
			return eerr
		}
	}
	return err
}

// captured runs a command as a step of another: under --json its document
// is returned rather than printed, so the outer command prints one of them
// all.
func (c *Context) captured(run func(*Context) error) (json.RawMessage, error) {
	if !c.JSON {
		return nil, run(c)
	}
	var buf bytes.Buffer
	step := *c
	step.Out, step.changes = &buf, changesJSON{}
	err := run(&step)
	// A config or state loaded by the step is the outer command's too.
	c.cfg, c.st = step.cfg, step.st
	return json.RawMessage(bytes.TrimSpace(buf.Bytes())), err
}

// errNeedsYes is what a command that asks before it changes anything
// returns under --json without --yes: there is no one to ask.
func errNeedsYes(what string) error {
	return fmt.Errorf("%s asks first; with --json, pass --yes", what)
}

// targetError is a failure outside any plan, by what it was about.
type targetError struct {
	Target string `json:"target"`
	Error  string `json:"error"`
}

// errString is err's message, "" for none.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// errorJSON is the document --json prints for a command that failed before
// it printed its result, or a command line that didn't parse.
type errorJSON struct {
	Error    string `json:"error"`
	ExitCode int    `json:"exit_code"`
}

// Conclude ends a run and returns its exit code. Under --json it makes sure
// stdout holds exactly one document: the command's, or, for one that failed
// before printing it, the error.
func Conclude(c *Context, err error) int {
	code := ExitCode(err)
	if c.JSON && !c.emitted {
		var doc any = struct{}{}
		if err != nil {
			doc = errorJSON{Error: err.Error(), ExitCode: code}
		}
		_ = c.Emit(doc)
	}
	return code
}

// WantsJSON reports whether args, which didn't parse, asked for --json: then
// the parse error is the one document on stdout.
func WantsJSON(args []string) bool {
	for _, a := range args {
		switch {
		case a == "--":
			return false
		case a == "--json", a == "--json=true":
			return true
		}
	}
	return false
}

// UsageError writes the document --json prints for a command line that
// didn't parse.
func UsageError(w io.Writer, err error) {
	c := &Context{JSON: true, Out: w}
	_ = c.Emit(errorJSON{Error: err.Error(), ExitCode: ExitCode(err)})
}
