package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/discovery"
	"github.com/ashtonian/git-mgr/internal/gitops"
)

// DoctorCmd diagnoses the health of the git-mgr setup: tooling, auth, config,
// and whether tracked repos still exist on disk and point where state says.
// It is offline (no provider API calls), changes nothing, and exits non-zero
// if any check fails. `git-mgr plan` and `apply` fix what config can.
type DoctorCmd struct{}

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

	// --- Auth: provider API tokens and configured SSH keys ---
	sshKeyConfigured := false
	for _, p := range cfg.Providers {
		if discovery.ResolveToken(p) == "" {
			d.warn(fmt.Sprintf("provider %q: no API token resolved (set token_command, token_env, or token)", p.Name))
		} else {
			d.ok(fmt.Sprintf("provider %q: API token resolved", p.Name))
		}
		if p.SSHKey != "" {
			sshKeyConfigured = true
			if key := config.ExpandHome(p.SSHKey); fileExists(key) {
				d.ok(fmt.Sprintf("provider %q: ssh_key present (%s)", p.Name, key))
			} else {
				d.fail(fmt.Sprintf("provider %q: ssh_key not found (%s)", p.Name, key))
			}
		}
	}
	if !sshKeyConfigured {
		d.info("no ssh_key configured; SSH clones use your default agent/keys (~/.ssh)")
	}

	// --- State lock ---
	if lerr := st.AcquireLock(); lerr != nil {
		d.warn("state lock is held (another git-mgr running?)")
	} else {
		st.ReleaseLock()
		d.ok("state lock free")
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
