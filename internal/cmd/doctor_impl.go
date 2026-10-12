package cmd

import (
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"os/user"
	"slices"
	"strings"
	"time"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/launch"
	"github.com/ashtonian/git-mgr/internal/scheduler"
	"github.com/ashtonian/git-mgr/internal/setup"
	"github.com/ashtonian/git-mgr/internal/state"
)

// DoctorCmd diagnoses the health of the git-mgr setup: tooling, auth, config,
// and whether tracked repos still exist on disk and point where state says.
// It is offline (no provider API calls), changes nothing, and exits non-zero
// if any check fails. `git-mgr plan` and `apply` fix what config can.
type DoctorCmd struct {
	// Scheduled is set when doctor checks a schedule's environment from
	// inside it; the schedules aren't checked again from there.
	Scheduled bool `hidden:"" help:"Run as a schedule's environment check."`
}

// stuckLockAfter is how long doctor waits for the state lock before calling
// its holder stuck; a write holds it for milliseconds.
const stuckLockAfter = 5 * time.Second

type doctorReport struct{ warns, fails int }

func (d *doctorReport) ok(msg string)   { fmt.Printf("  ok   %s\n", msg) }
func (d *doctorReport) info(msg string) { fmt.Printf("  --   %s\n", msg) }
func (d *doctorReport) warn(msg string) { d.warns++; fmt.Printf("  WARN %s\n", msg) }
func (d *doctorReport) fail(msg string) { d.fails++; fmt.Printf("  FAIL %s\n", msg) }

func (c *DoctorCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		fmt.Printf("  FAIL config: %v\n", err)
		return fmt.Errorf("config invalid")
	}

	st, err := ctx.LoadState()
	if err != nil {
		return err
	}

	d := &doctorReport{}

	// --- Tooling ---
	if path, lerr := exec.LookPath("git"); lerr != nil {
		d.fail("git not found in PATH")
	} else if ver, verr := exec.Command(path, "--version").Output(); verr == nil {
		d.ok(strings.TrimSpace(string(ver)))
	} else {
		d.ok("git found: " + path)
	}

	d.ok("config valid: " + config.ExpandHome(ctx.Config))

	// --- base_dir ---
	if fi, serr := os.Stat(cfg.BaseDir); serr == nil && fi.IsDir() {
		if dirWritable(cfg.BaseDir) {
			d.ok("base_dir writable: " + cfg.BaseDir)
		} else {
			d.warn("base_dir not writable: " + cfg.BaseDir)
		}
	} else {
		d.info("base_dir will be created on first clone: " + cfg.BaseDir)
	}

	// --- Auth: provider tokens, SSH keys, signing (offline) ---
	for _, c := range (setup.Checker{}).Check(ctx.GoCtx(), cfg) {
		switch c.Status {
		case setup.Pass:
			d.ok(c.Message)
		case setup.Info:
			d.info(c.Message)
		case setup.Warn:
			d.warn(c.Message)
		default:
			d.fail(c.Message)
		}
	}

	// --- Accounts: identity and what each covers ---
	covered := map[string]int{}
	for key, repo := range st.Repos {
		if a := cfg.AccountFor(key, repo.DiscoveredBy); a != nil {
			covered[a.Name]++
		}
	}
	for _, a := range cfg.Accounts {
		identity := "inherits your global git identity"
		if a.Git.Name != "" || a.Git.Email != "" {
			identity = fmt.Sprintf("commits as %s <%s>", orInherited(a.Git.Name), orInherited(a.Git.Email))
		}
		msg := fmt.Sprintf("account %q: %s; covers %d tracked repo(s)", a.Name, identity, covered[a.Name])
		if covered[a.Name] == 0 && len(st.Repos) > 0 {
			d.warn(msg + " -- check its hosts and owners")
		} else {
			d.ok(msg)
		}
	}

	// --- Locks ---
	if release, lerr := st.RunLock("sync"); lerr != nil {
		d.info("a sync is running")
	} else {
		release()
	}
	// A write holds the state lock for milliseconds; held longer, a run is
	// stuck and every command will wait lock_timeout and fail.
	if lerr := st.Lock(stuckLockAfter); lerr != nil {
		d.warn(fmt.Sprintf("state lock: %v -- a git-mgr process may be stuck; find it with lsof", lerr))
	} else {
		st.ReleaseLock()
		d.ok("state lock free")
	}

	if !c.Scheduled {
		checkSchedules(ctx, st, d)
	}

	// --- Tracked repos: present on disk, origin still maps to the key ---
	cloned, missing := 0, 0
	for key, repo := range st.Repos {
		if !repo.Cloned {
			continue
		}
		cloned++
		if !gitops.HasGitDir(repo.LocalPath) {
			missing++
			d.fail(fmt.Sprintf("repo %s: not on disk (%s); `git-mgr apply` clones it again, `git-mgr remove %s` stops tracking it", key, repo.LocalPath, key))
			continue
		}
		if id, perr := gitops.ParseRemoteURL(ctx.GoCtx(), repo.LocalPath); perr == nil {
			if got := id.Key(); got != key {
				d.warn(fmt.Sprintf("repo %s: origin now points to %s", key, got))
			}
		}
	}
	if cloned > 0 && missing == 0 {
		d.ok(fmt.Sprintf("%d tracked repos present on disk", cloned))
	}
	checkWorkspaces(ctx, st, d)
	checkLaunchers(cfg, d)

	fmt.Printf("\n%d problem(s), %d warning(s)\n", d.fails, d.warns)
	if d.fails > 0 {
		return fmt.Errorf("doctor found %d problem(s)", d.fails)
	}
	return nil
}

// dirWritable reports whether a directory accepts new files.
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".git-mgr-doctor-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

func orInherited(v string) string {
	if v == "" {
		return "(inherited)"
	}
	return v
}

// checkSchedules checks each installed schedule is in place and that its
// environment -- PATH and agent captured at install, nothing else -- can do
// what a sync needs, by running doctor there.
func checkSchedules(ctx *Context, st *state.State, d *doctorReport) {
	if len(st.Schedules) == 0 {
		d.info("no background schedule installed (`git-mgr install-agent`)")
		return
	}
	host, err := newHost(ctx)
	if err != nil {
		d.warn(fmt.Sprintf("schedules: %v", err))
		return
	}
	for _, name := range slices.Sorted(maps.Keys(st.Schedules)) {
		s := scheduleOf(st.Schedules[name])
		where := fmt.Sprintf("schedule %q (%s, every %s)", s.Name, s.Kind, s.Every())
		installed, err := host.Installed(s)
		switch {
		case err != nil:
			d.warn(fmt.Sprintf("%s: %v", where, err))
			continue
		case !installed:
			d.fail(fmt.Sprintf("%s is recorded but no longer installed; `git-mgr install-agent --name %s` puts it back", where, s.Name))
			continue
		}
		if info, err := os.Stat(s.Bin); err != nil || info.Mode()&0o111 == 0 {
			d.fail(fmt.Sprintf("%s runs %s, which is missing; `git-mgr install-agent --name %s` again from the git-mgr you use", where, s.Bin, s.Name))
			continue
		}
		if s.Kind == scheduler.Systemd {
			if u, err := user.Current(); err == nil {
				if lingering, err := host.Lingering(u.Username); err == nil && !lingering {
					d.warn(fmt.Sprintf("%s stops when you log out; `loginctl enable-linger %s` keeps it running", where, u.Username))
				}
			}
		}
		fails, warns, err := doctorAsScheduled(ctx, s)
		switch {
		case err != nil:
			d.fail(fmt.Sprintf("%s: running doctor in its environment: %v", where, err))
		case len(fails) > 0:
			d.fail(fmt.Sprintf("%s would fail; in its environment: %s", where, strings.Join(fails, "; ")))
		case len(warns) > 0:
			d.warn(fmt.Sprintf("%s: in its environment: %s", where, strings.Join(warns, "; ")))
		default:
			d.ok(where + ": its environment passes doctor")
		}
	}
}

// doctorAsScheduled runs the schedule's binary as `doctor` with only the
// environment the scheduler gives it, and returns the problems it reports.
func doctorAsScheduled(ctx *Context, s scheduler.Schedule) (fails, warns []string, err error) {
	runCtx, cancel := context.WithTimeout(ctx.GoCtx(), 2*time.Minute)
	defer cancel()
	args := append(slices.Clone(s.Global), "doctor", "--scheduled")
	cmd := exec.CommandContext(runCtx, s.Bin, args...)
	home, _ := os.UserHomeDir()
	env := map[string]string{"HOME": home, "USER": os.Getenv("USER"), "LOGNAME": os.Getenv("LOGNAME")}
	maps.Copy(env, s.Env)
	// A LaunchAgent gets the login session's ssh agent from launchd.
	if _, ok := env["SSH_AUTH_SOCK"]; !ok && s.Kind == scheduler.Launchd && !s.Daemon {
		if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
			env["SSH_AUTH_SOCK"] = sock
		}
	}
	if _, ok := env["PATH"]; !ok {
		env["PATH"] = "/usr/bin:/bin:/usr/sbin:/sbin" // what launchd, systemd and cron default to
	}
	for _, k := range slices.Sorted(maps.Keys(env)) {
		cmd.Env = append(cmd.Env, k+"="+env[k])
	}
	out, runErr := cmd.CombinedOutput()
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "  FAIL "):
			fails = append(fails, strings.TrimPrefix(line, "  FAIL "))
		case strings.HasPrefix(line, "  WARN "):
			warns = append(warns, strings.TrimPrefix(line, "  WARN "))
		}
	}
	if runErr != nil && len(fails) == 0 {
		return nil, nil, fmt.Errorf("%w: %s", runErr, strings.TrimSpace(string(out)))
	}
	return fails, warns, nil
}

// checkLaunchers checks the launchers config uses are installed. It stays
// offline, so a fleet's host and iTerm's Automation permission (which macOS
// asks for the first time) aren't tried.
func checkLaunchers(cfg *config.Config, d *doctorReport) {
	used := map[string]bool{}
	if cfg.Launchers.Default != "auto" {
		used[cfg.Launchers.Default] = true
	}
	for _, f := range cfg.Fleets {
		if f.Launcher != "" && f.Launcher != "auto" {
			used[f.Launcher] = true
		}
		if f.Host != "" {
			d.info(fmt.Sprintf("fleet %q opens on %s: git-mgr there plans it, so it needs this version and the same workspaces", f.Name, f.Host))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(used)) {
		switch name {
		case "tmux":
			if _, err := exec.LookPath("tmux"); err != nil {
				d.fail("launcher tmux: tmux isn't on PATH")
			} else {
				d.ok("launcher tmux: installed")
			}
		case "iterm":
			if _, err := os.Stat("/Applications/iTerm.app"); err != nil {
				d.fail("launcher iterm: iTerm isn't in /Applications")
			} else {
				d.ok("launcher iterm: installed (macOS asks for Automation permission the first time)")
			}
		case "muxy":
			cli := cfg.Launchers.Muxy.Command
			if cli == "" {
				cli = "muxy"
				if _, err := exec.LookPath(cli); err != nil {
					cli = "/Applications/Muxy.app/Contents/MacOS/muxy"
				}
			}
			if out, err := exec.Command(cli, "--version").Output(); err != nil {
				d.fail(fmt.Sprintf("launcher muxy: %s doesn't run: %v", cli, err))
			} else {
				d.ok("launcher muxy: " + strings.TrimSpace(string(out)))
			}
		default:
			if _, err := exec.LookPath(launch.PluginPrefix + name); err != nil {
				d.fail(fmt.Sprintf("launcher %s: no %s%s on PATH", name, launch.PluginPrefix, name))
			} else {
				d.ok(fmt.Sprintf("launcher %s: %s%s found", name, launch.PluginPrefix, name))
			}
		}
	}
}

// checkWorkspaces checks every recorded copy is on disk, and that the clones
// reference clones borrow objects from keep git gc from pruning them.
func checkWorkspaces(ctx *Context, st *state.State, d *doctorReport) {
	copies, missing := 0, 0
	borrowed := map[string]bool{}
	for _, name := range slices.Sorted(maps.Keys(st.Workspaces)) {
		ws := st.Workspaces[name]
		for _, r := range ws.Replicas {
			for _, c := range r.Copies {
				copies++
				if !gitops.HasGitDir(c.Path) {
					missing++
					d.fail(fmt.Sprintf("workspace %s/%s: copy missing (%s); `git-mgr ws ensure %s` makes it again", name, r.Agent, c.Path, name))
					continue
				}
				if ws.Isolation == config.IsolationReferenceClone {
					if repo := st.Repos[c.Repo]; repo != nil {
						borrowed[repo.LocalPath] = true
					}
				}
			}
		}
	}
	if copies > 0 && missing == 0 {
		d.ok(fmt.Sprintf("%d workspace copies present on disk", copies))
	}
	for _, src := range slices.Sorted(maps.Keys(borrowed)) {
		if v, err := gitops.Git(ctx.GoCtx(), src, "config", "--get", "gc.pruneExpire"); err != nil || v != "never" {
			d.fail(fmt.Sprintf("%s: reference clones borrow its objects, but gc.pruneExpire isn't never, so git gc there can break them; run `git -C %s config gc.pruneExpire never`", src, src))
		}
	}
}
