package setup

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ashtonian/gitstead/internal/config"
	"github.com/ashtonian/gitstead/internal/discovery"
	"github.com/ashtonian/gitstead/internal/gitops"
	"github.com/ashtonian/gitstead/internal/reconcile"
	"github.com/ashtonian/gitstead/internal/scheduler"
)

// Status is how a check went.
type Status string

// How a check can go: Pass and Info are fine, Warn needs a look, Fail
// stops what depends on it.
const (
	Pass Status = "ok"
	Info Status = "info"
	Warn Status = "warn"
	Fail Status = "fail"
)

// Check is one verified piece of a config.
type Check struct {
	Status  Status
	Message string
}

// Checker verifies a config's access: tokens, SSH keys, signing. Without
// Probe it stays offline; with it, it calls each provider's API and asks
// each host which account a key authenticates as.
type Checker struct {
	Probe       bool
	RunCombined Runner
}

// Check verifies cfg.
func (c Checker) Check(ctx context.Context, cfg *config.Config) []Check {
	var checks []Check
	add := func(s Status, format string, args ...any) {
		checks = append(checks, Check{s, fmt.Sprintf(format, args...)})
	}

	for _, p := range cfg.Providers {
		tok, err := discovery.ResolveToken(p)
		switch {
		case err != nil:
			add(Fail, "%v", err)
			continue
		case tok == "":
			add(Warn, "provider %q: no API token; only public repos are visible", p.Name)
			continue
		case !c.Probe:
			add(Pass, "provider %q: API token resolved", p.Name)
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		owners, err := discovery.ListOwners(pctx, p, cfg.Sync.APITimeout)
		cancel()
		if err != nil || len(owners) == 0 {
			add(Fail, "provider %q: the API rejected the token: %v", p.Name, err)
			continue
		}
		add(Pass, "provider %q: token works as %s", p.Name, owners[0].Login)
	}

	keys := sshKeyRefs(cfg)
	for _, k := range keys {
		if _, err := os.Stat(k.path); err != nil {
			add(Fail, "%s: ssh_key not found (%s)", k.where, k.path)
		} else {
			add(Pass, "%s: ssh_key present (%s)", k.where, k.path)
		}
	}
	if len(keys) == 0 {
		add(Info, "no ssh_key configured; SSH clones use your default agent/keys (~/.ssh)")
	}

	for i := range cfg.Accounts {
		a := &cfg.Accounts[i]
		if c.Probe && a.SSHKey != "" {
			for _, host := range a.Hosts {
				checks = append(checks, c.probeKey(ctx, a, host))
			}
		}
		switch a.Signing.Mode {
		case "required":
			if err := gitops.TestSign(ctx, reconcile.AccountConfig(a)); err != nil {
				add(Fail, "account %q: signing is required but a test signature failed: %v", a.Name, err)
			} else {
				add(Pass, "account %q: signs commits and tags (%s) without a terminal", a.Name, a.Signing.SigningFormat())
			}
		case "off":
			add(Pass, "account %q: signing turned off in its repos", a.Name)
		}
	}
	return checks
}

func (c Checker) probeKey(ctx context.Context, a *config.Account, host string) Check {
	key := config.ExpandHome(a.SSHKey)
	out, _ := c.RunCombined(ctx, "ssh", "-T", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=5", "-i", key, "git@"+host)
	m := sshGreeting.FindSubmatch(out)
	switch {
	case m == nil:
		return Check{Warn, fmt.Sprintf("account %q: couldn't confirm %s on %s: %s", a.Name, key, host, firstLine(out))}
	case len(a.Owners) > 0 && !strings.EqualFold(string(m[1]), a.Owners[0]):
		return Check{Warn, fmt.Sprintf("account %q: %s authenticates as %s on %s, not %s", a.Name, key, m[1], host, a.Owners[0])}
	default:
		return Check{Pass, fmt.Sprintf("account %q: %s authenticates as %s on %s", a.Name, key, m[1], host)}
	}
}

// ScheduleChecks warns about what a background sync, which runs without
// your shell's environment, won't have: a token in an environment variable
// it doesn't set.
func ScheduleChecks(cfg *config.Config, getenv func(string) string) []Check {
	captured, _ := scheduler.CaptureEnv(getenv)
	var checks []Check
	for _, p := range cfg.Providers {
		if p.TokenEnv == "" || captured[p.TokenEnv] != "" {
			continue
		}
		fix := "use a token_command that reads it from a store, such as a password manager's CLI"
		if config.NormalizeProviderType(p.Type) == "github" {
			fix = "`gh auth login` stores it, so `gh auth token` can read it; run setup again afterwards"
		}
		checks = append(checks, Check{Warn, fmt.Sprintf("provider %q reads its token from $%s, which the background sync won't have: %s",
			p.Name, p.TokenEnv, fix)})
	}
	return checks
}

type sshKeyRef struct{ where, path string }

// sshKeyRefs lists every ssh_key config sets, once per place.
func sshKeyRefs(cfg *config.Config) []sshKeyRef {
	var keys []sshKeyRef
	add := func(where, key string) {
		if key != "" {
			keys = append(keys, sshKeyRef{where, config.ExpandHome(key)})
		}
	}
	for _, a := range cfg.Accounts {
		add(fmt.Sprintf("account %q", a.Name), a.SSHKey)
	}
	for _, p := range cfg.Providers {
		add(fmt.Sprintf("provider %q", p.Name), p.SSHKey)
	}
	for _, r := range cfg.Discovery {
		add(fmt.Sprintf("rule %q", r.Name), r.SSHKey)
	}
	for _, mr := range cfg.Repos {
		add("repos entry "+mr.URL, mr.SSHKey)
	}
	for _, o := range cfg.Overrides {
		add(fmt.Sprintf("override %v", o.Repos), o.SSHKey)
	}
	return keys
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return "no answer"
	}
	return s
}
