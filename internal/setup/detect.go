// Package setup is the logic behind `gitten setup`: it detects what this
// machine already has (gh accounts, SSH keys and host aliases, git
// identities, existing clones), proposes answers from it, renders the answers
// into the config file in place, and checks the result before it is written.
// The interactive questions live in the cmd package; everything here runs
// without a terminal.
package setup

import (
	"bufio"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ashtonian/gitten/internal/agent"
	"github.com/ashtonian/gitten/internal/config"
	"github.com/ashtonian/gitten/internal/pathtemplate"
	"github.com/ashtonian/gitten/internal/repourl"
)

// GHAccount is an account the gh CLI is logged in to.
type GHAccount struct {
	Host, Login string
	Active      bool
	Scopes      string // comma-separated OAuth scopes
	GitProtocol string // what gh uses for git: ssh or https
	// TokenSource is where gh has the token: "keyring" or "oauth_token"
	// for a login it stores, or the environment variable it was given.
	TokenSource string
}

// TokenEnv is the environment variable holding the account's token, when
// gh has it from one rather than a login it stores.
func (a GHAccount) TokenEnv() string {
	if a.TokenSource != "" && a.TokenSource == strings.ToUpper(a.TokenSource) {
		return a.TokenSource
	}
	return ""
}

// SSHHost is a Host block in ~/.ssh/config.
type SSHHost struct {
	Alias, HostName, User, IdentityFile string
}

// SSHKey is a private key in ~/.ssh with its public half beside it.
type SSHKey struct {
	Path, Comment string
	// Logins maps a host to the account this key authenticates as there,
	// found by `ssh -T`.
	Logins map[string]string
}

// Identity is a git commit identity configured on this machine.
type Identity struct {
	Name, Email string
	Where       string // "global", or the includeIf condition that applies it
}

// Clone is a git working copy found on disk.
type Clone struct {
	Path, Key, Origin, Root string
}

// LayoutGuess is the base dir and layout most existing clones follow.
type LayoutGuess struct {
	BaseDir, Layout string
	Matches         int
}

// Detected is what Detect found.
type Detected struct {
	GH         []GHAccount
	SSHHosts   []SSHHost
	SSHKeys    []SSHKey
	Identities []Identity
	Clones     []Clone
	Layout     LayoutGuess
	// Agents are the known coding agents found on PATH.
	Agents []config.AgentProfile
	// Launchers are the launchers installed: tmux, iterm, muxy.
	Launchers []string

	// ShellRC is your shell's startup file; nil for a shell gitten doesn't
	// integrate with. ShellIntegrated reports that it loads gitten's shell
	// integration already.
	ShellRC         *ShellRC
	ShellIntegrated bool
	// GitIncludes reports that your global git config includes the file
	// `gitten accounts gitconfig` writes.
	GitIncludes bool
	// Schedule is the background sync setup installs, when one is; the
	// caller fills it in from state.
	Schedule *InstalledSchedule
}

// InstalledSchedule is a background sync already installed.
type InstalledSchedule struct {
	Interval time.Duration
}

// Runner runs a command and returns its standard output.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// ExecRunner runs commands for real, each limited to 10 seconds, and returns
// their standard output.
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return quietCommand(ctx, name, args...).Output()
}

// ExecCombinedRunner is ExecRunner returning standard output and error
// together, for commands such as ssh that answer on standard error.
func ExecCombinedRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return quietCommand(ctx, name, args...).CombinedOutput()
}

// quietCommand builds a command that never prompts.
func quietCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GH_PROMPT_DISABLED=1")
	return cmd
}

// Detector finds what a machine already has.
type Detector struct {
	Home string
	// GOOS and Getenv describe the machine for the shell's startup file;
	// runtime.GOOS and os.Getenv when empty.
	GOOS   string
	Getenv func(string) string
	// AccountsInclude is the file `gitten accounts gitconfig` writes, to
	// look for in the global git config's include.path.
	AccountsInclude string
	Run             Runner
	// RunCombined runs network probes, whose answers come on standard error.
	RunCombined Runner
	// Roots are where to look for existing clones; DefaultRoots when empty.
	Roots []string
	// Probe allows network checks: `ssh -T` to learn which account each key is.
	Probe bool
	// LookPath finds a program on PATH; exec.LookPath when nil.
	LookPath func(string) (string, error)
	// Exists reports whether a path exists, for apps; os.Stat when nil.
	Exists func(string) bool
}

// DefaultRoots are the usual places people keep clones.
func DefaultRoots(home string) []string {
	var roots []string
	for _, d := range []string{"src", "code", "projects", "repos", "work", "dev", "git", "go/src"} {
		roots = append(roots, filepath.Join(home, d))
	}
	return roots
}

// Detect gathers everything it can; each source that fails is just empty.
func (d *Detector) Detect(ctx context.Context) Detected {
	var det Detected
	det.GH = d.ghAccounts(ctx)
	det.SSHHosts = parseSSHConfig(filepath.Join(d.Home, ".ssh", "config"), d.Home, 0)
	det.SSHKeys = sshKeys(filepath.Join(d.Home, ".ssh"))
	det.Identities = d.identities(ctx)
	roots := d.Roots
	if len(roots) == 0 {
		roots = DefaultRoots(d.Home)
		if out, err := d.Run(ctx, "git", "config", "--global", "--get", "ghq.root"); err == nil {
			roots = append(roots, expandFrom(strings.TrimSpace(string(out)), d.Home))
		}
	}
	det.Clones = d.clones(ctx, roots)
	var logins []string
	for _, a := range det.GH {
		logins = append(logins, a.Login)
	}
	det.Layout = guessLayout(det.Clones, logins)
	det.Agents, det.Launchers = d.agentTools()
	det.ShellRC, det.ShellIntegrated = d.shellRC()
	det.GitIncludes = d.gitIncludes(ctx)
	if d.Probe {
		d.probeKeys(ctx, det.SSHKeys, det.GH)
	}
	return det
}

func (d *Detector) shellRC() (*ShellRC, bool) {
	goos, getenv := d.GOOS, d.Getenv
	if goos == "" {
		goos = runtime.GOOS
	}
	if getenv == nil {
		getenv = os.Getenv
	}
	rc, ok := DetectShellRC(d.Home, goos, getenv)
	if !ok {
		return nil, false
	}
	integrated, _ := rc.Integrated()
	return &rc, integrated
}

func (d *Detector) gitIncludes(ctx context.Context) bool {
	if d.AccountsInclude == "" {
		return false
	}
	out, err := d.Run(ctx, "git", "config", "--global", "--get-all", "include.path")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if expandFrom(strings.TrimSpace(line), d.Home) == d.AccountsInclude {
			return true
		}
	}
	return false
}

func (d *Detector) ghAccounts(ctx context.Context) []GHAccount {
	out, err := d.Run(ctx, "gh", "auth", "status", "--json", "hosts")
	if err != nil {
		return nil
	}
	var status struct {
		Hosts map[string][]struct {
			Host, Login, Scopes, GitProtocol, State, TokenSource string
			Active                                               bool
		} `json:"hosts"`
	}
	if json.Unmarshal(out, &status) != nil {
		return nil
	}
	var accounts []GHAccount
	seen := map[string]int{} // host/login -> index in accounts
	for _, entries := range status.Hosts {
		for _, e := range entries {
			if e.State != "success" || e.Login == "" {
				continue
			}
			a := GHAccount{Host: e.Host, Login: e.Login, Active: e.Active, Scopes: e.Scopes, GitProtocol: e.GitProtocol, TokenSource: e.TokenSource}
			// gh lists an env-var token beside the stored one for the same
			// login; one account each, the stored login if there is one,
			// since a scheduled sync has no shell environment.
			if i, ok := seen[e.Host+"/"+e.Login]; ok {
				if accounts[i].TokenEnv() != "" && a.TokenEnv() == "" {
					accounts[i] = a
				}
				continue
			}
			seen[e.Host+"/"+e.Login] = len(accounts)
			accounts = append(accounts, a)
		}
	}
	sort.Slice(accounts, func(i, j int) bool {
		if accounts[i].Host != accounts[j].Host {
			return accounts[i].Host < accounts[j].Host
		}
		return accounts[i].Login < accounts[j].Login
	})
	return accounts
}

// parseSSHConfig reads Host blocks, following Include directives.
func parseSSHConfig(path, home string, depth int) []SSHHost {
	f, err := os.Open(path)
	if err != nil || depth > 4 {
		return nil
	}
	defer f.Close()
	var hosts []SSHHost
	var current []int // indexes into hosts of the Host block being read
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, _ := strings.Cut(strings.ReplaceAll(line, "=", " "), " ")
		value = strings.Trim(strings.TrimSpace(value), `"`)
		switch strings.ToLower(key) {
		case "include":
			for _, pattern := range strings.Fields(value) {
				pattern = expandFrom(pattern, home)
				if !filepath.IsAbs(pattern) {
					pattern = filepath.Join(home, ".ssh", pattern)
				}
				matches, _ := filepath.Glob(pattern)
				for _, m := range matches {
					hosts = append(hosts, parseSSHConfig(m, home, depth+1)...)
				}
			}
		case "host":
			current = nil
			for _, alias := range strings.Fields(value) {
				if strings.ContainsAny(alias, "*?!") {
					continue
				}
				hosts = append(hosts, SSHHost{Alias: alias, HostName: alias})
				current = append(current, len(hosts)-1)
			}
		case "match":
			current = nil
		case "hostname":
			for _, i := range current {
				hosts[i].HostName = value
			}
		case "user":
			for _, i := range current {
				hosts[i].User = value
			}
		case "identityfile":
			for _, i := range current {
				if hosts[i].IdentityFile == "" {
					hosts[i].IdentityFile = expandFrom(value, home)
				}
			}
		}
	}
	return hosts
}

// sshKeys lists private keys that have a .pub beside them.
func sshKeys(dir string) []SSHKey {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var keys []SSHKey
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".pub") {
			continue
		}
		private := filepath.Join(dir, strings.TrimSuffix(name, ".pub"))
		if info, err := os.Stat(private); err != nil || !info.Mode().IsRegular() {
			continue
		}
		key := SSHKey{Path: private}
		if pub, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			if fields := strings.Fields(string(pub)); len(fields) >= 3 {
				key.Comment = strings.Join(fields[2:], " ")
			}
		}
		keys = append(keys, key)
	}
	return keys
}

func (d *Detector) identities(ctx context.Context) []Identity {
	var ids []Identity
	get := func(args ...string) string {
		out, err := d.Run(ctx, "git", append([]string{"config"}, args...)...)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	if name, email := get("--global", "--get", "user.name"), get("--global", "--get", "user.email"); name != "" || email != "" {
		ids = append(ids, Identity{Name: name, Email: email, Where: "global"})
	}
	out, err := d.Run(ctx, "git", "config", "--global", "--get-regexp", `^includeif\..*\.path$`)
	if err != nil {
		return ids
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		key, path, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		condition := strings.TrimSuffix(strings.TrimPrefix(key, "includeif."), ".path")
		file := expandFrom(path, d.Home)
		if !filepath.IsAbs(file) {
			file = filepath.Join(d.Home, file)
		}
		name, email := get("--file", file, "--get", "user.name"), get("--file", file, "--get", "user.email")
		if name != "" || email != "" {
			ids = append(ids, Identity{Name: name, Email: email, Where: condition})
		}
	}
	return ids
}

// clones finds git working copies up to four levels under each root.
func (d *Detector) clones(ctx context.Context, roots []string) []Clone {
	var paths []struct{ path, root string }
	for _, root := range roots {
		root = filepath.Clean(root)
		_ = filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
			if err != nil || !e.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			if strings.Count(rel, string(filepath.Separator)) >= 4 {
				return filepath.SkipDir
			}
			if name := e.Name(); p != root && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor") {
				return filepath.SkipDir
			}
			if _, err := os.Lstat(filepath.Join(p, ".git")); err == nil {
				paths = append(paths, struct{ path, root string }{p, root})
				return filepath.SkipDir
			}
			return nil
		})
	}

	clones := make([]Clone, len(paths))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, p := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out, err := d.Run(ctx, "git", "-C", p.path, "config", "--get", "remote.origin.url")
			origin := strings.TrimSpace(string(out))
			if err != nil || origin == "" || repourl.HasCredentials(origin) {
				return
			}
			if id, err := repourl.Parse(origin); err == nil {
				clones[i] = Clone{Path: p.path, Key: id.Key(), Origin: origin, Root: p.root}
			}
		}()
	}
	wg.Wait()
	return slices.DeleteFunc(clones, func(c Clone) bool { return c.Path == "" })
}

// guessLayout finds the root and layout preset that best explain where
// existing clones are. Clones of your own repos (owned by one of logins)
// decide when there are any: they show where you put what gitten will
// manage, while clones of other projects land anywhere. A match counts for as
// many path levels as its layout has, since a flat clone is where `git clone`
// leaves a repo while an owner/name tree is built on purpose.
func guessLayout(clones []Clone, logins []string) LayoutGuess {
	own := slices.DeleteFunc(slices.Clone(clones), func(c Clone) bool {
		id, err := repourl.Parse(c.Origin)
		return err != nil || !slices.ContainsFunc(logins, func(l string) bool { return strings.EqualFold(l, id.Owner) })
	})
	if len(own) > 0 {
		clones = own
	}
	weight := map[string]int{"name": 1, "owner/name": 2, "host/owner/name": 3}
	counts := map[[2]string]int{}
	for _, c := range clones {
		id, err := repourl.Parse(c.Origin)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(c.Root, c.Path)
		if err != nil {
			continue
		}
		for layout := range weight {
			want, err := pathtemplate.Resolve(config.Layouts[layout], pathtemplate.RepoData{
				Host: id.Host, Owner: id.Owner, Name: id.Name, FullName: id.FullName,
			})
			if err == nil && filepath.Clean(want) == rel {
				counts[[2]string{c.Root, layout}]++
			}
		}
	}
	var best LayoutGuess
	bestScore := 0
	for k, n := range counts {
		score := n * weight[k[1]]
		if score > bestScore || (score == bestScore && k[0]+k[1] < best.BaseDir+best.Layout) {
			best, bestScore = LayoutGuess{BaseDir: k[0], Layout: k[1], Matches: n}, score
		}
	}
	return best
}

var sshGreeting = regexp.MustCompile(`(?i)(?:Hi |Welcome to GitLab, @|Hi there, )([A-Za-z0-9._-]+)`)

// probeKeys asks each host gh knows which account each key authenticates as.
// It uses your known_hosts strictly, never adding to it, and never prompts.
func (d *Detector) probeKeys(ctx context.Context, keys []SSHKey, accounts []GHAccount) {
	hosts := map[string]bool{"github.com": true}
	for _, a := range accounts {
		hosts[a.Host] = true
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := range keys {
		keys[i].Logins = map[string]string{}
		for host := range hosts {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// GitHub exits 1 even when it recognises the key, so the
				// greeting decides, not the exit status.
				out, _ := d.RunCombined(ctx, "ssh", "-T", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes",
					"-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=5", "-i", keys[i].Path, "git@"+host)
				if m := sshGreeting.FindSubmatch(out); m != nil {
					mu.Lock()
					keys[i].Logins[host] = string(m[1])
					mu.Unlock()
				}
			}()
		}
	}
	wg.Wait()
}

// KeyFor returns the detected key that authenticates as login on host.
func (det Detected) KeyFor(host, login string) string {
	for _, k := range det.SSHKeys {
		if strings.EqualFold(k.Logins[host], login) {
			return k.Path
		}
	}
	return ""
}

// agentTools finds the known coding agents on PATH and the launchers
// installed.
func (d *Detector) agentTools() ([]config.AgentProfile, []string) {
	lookPath, exists := d.LookPath, d.Exists
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if exists == nil {
		exists = func(p string) bool { _, err := os.Stat(p); return err == nil }
	}
	var profiles []config.AgentProfile
	for _, p := range agent.Known {
		if _, err := lookPath(p.Command); err == nil {
			profiles = append(profiles, p)
		}
	}
	var launchers []string
	if _, err := lookPath("tmux"); err == nil {
		launchers = append(launchers, "tmux")
	}
	if exists("/Applications/iTerm.app") {
		launchers = append(launchers, "iterm")
	}
	if _, err := lookPath("muxy"); err == nil || exists("/Applications/Muxy.app") {
		launchers = append(launchers, "muxy")
	}
	return profiles, launchers
}
