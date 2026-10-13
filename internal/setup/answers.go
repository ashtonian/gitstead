package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/pathtemplate"
	"github.com/ashtonian/git-mgr/internal/repourl"
)

// Answers are every choice setup makes. They double as the --answers file,
// so one machine's setup can be repeated on another.
type Answers struct {
	BaseDir  string    `yaml:"base_dir"`
	Layout   string    `yaml:"layout"` // a config.Layouts preset, or "custom" to keep path_template
	Accounts []Account `yaml:"accounts"`
	// Adopt lists existing clones to track where they are.
	Adopt []string `yaml:"adopt,omitempty"`

	Backups  bool `yaml:"backups"`
	Schedule bool `yaml:"schedule"` // install the background sync agent
	// Linger keeps a Linux schedule running while you are logged out
	// (loginctl enable-linger).
	Linger     bool `yaml:"linger,omitempty"`
	RemoteSync bool `yaml:"remote_sync"`
	// Update fast-forwards clean clones that are behind during sync
	// (sync.update: ff-only-clean).
	Update        bool   `yaml:"update"`
	ReconcileAuto string `yaml:"reconcile_auto"`
	Cleanup       string `yaml:"cleanup"`
	// Workspaces sets up agents' workspaces; nil leaves them as they are.
	Workspaces *Workspaces `yaml:"workspaces,omitempty"`
}

// Workspaces are the answers for running coding agents in workspaces: the
// agent profiles, where workspaces live, how many agents each gets, and the
// launcher that opens them. Workspaces themselves are created later, for
// repos that are tracked by then.
type Workspaces struct {
	Root     string                `yaml:"root"`
	Agents   int                   `yaml:"agents"`
	Profiles []config.AgentProfile `yaml:"profiles"`
	// Agent is the profile agents run unless a workspace names another.
	Agent    string `yaml:"agent,omitempty"`
	Launcher string `yaml:"launcher"`
}

// Account is one account's answers. Setup writes it as an account, a provider
// of the same name using the account's token, and a discovery rule per
// tracked owner.
type Account struct {
	Name         string             `yaml:"name"`
	Type         string             `yaml:"type"` // github | gitlab | gitea
	Host         string             `yaml:"host"`
	URL          string             `yaml:"url,omitempty"` // API base of a self-hosted provider
	Login        string             `yaml:"login,omitempty"`
	TokenCommand string             `yaml:"token_command,omitempty"`
	TokenEnv     string             `yaml:"token_env,omitempty"`
	Track        []Track            `yaml:"track"`
	SSHKey       string             `yaml:"ssh_key,omitempty"`
	Protocol     string             `yaml:"protocol"` // ssh | https
	Git          config.GitIdentity `yaml:"git,omitempty"`
	Signing      config.Signing     `yaml:"signing,omitempty"`
	BaseDir      string             `yaml:"base_dir,omitempty"` // this account's repos go here instead
}

// Track is an owner whose repos an account tracks.
type Track struct {
	Owner string `yaml:"owner"`
	Kind  string `yaml:"kind"` // self | org | group
}

// Owners are the owners an account covers: the ones it tracks.
func (a Account) Owners() []string {
	var owners []string
	for _, t := range a.Track {
		if !slices.Contains(owners, t.Owner) {
			owners = append(owners, t.Owner)
		}
	}
	return owners
}

// Tracks reports whether the account tracks repos of owner.
func (a Account) Tracks(owner string) bool {
	for _, o := range a.Owners() {
		if strings.EqualFold(o, owner) || strings.HasPrefix(strings.ToLower(owner), strings.ToLower(o)+"/") {
			return true
		}
	}
	return false
}

// Validate checks answers before they are rendered.
func (a Answers) Validate() error {
	if w := a.Workspaces; w != nil {
		if err := w.validate(); err != nil {
			return fmt.Errorf("workspaces: %w", err)
		}
	}
	if a.BaseDir == "" {
		return fmt.Errorf("base_dir is empty")
	}
	if _, ok := config.Layouts[a.Layout]; !ok && a.Layout != "custom" {
		return fmt.Errorf("layout %q is not one of owner/name, host/owner/name, name or custom", a.Layout)
	}
	names := map[string]bool{}
	for _, acct := range a.Accounts {
		switch {
		case acct.Name == "":
			return fmt.Errorf("an account has no name")
		case names[acct.Name]:
			return fmt.Errorf("two accounts are named %q", acct.Name)
		case acct.Host == "":
			return fmt.Errorf("account %q has no host", acct.Name)
		case acct.Type != "github" && acct.Type != "gitlab" && acct.Type != "gitea":
			return fmt.Errorf("account %q: type %q is not github, gitlab or gitea", acct.Name, acct.Type)
		case acct.Protocol != "ssh" && acct.Protocol != "https":
			return fmt.Errorf("account %q: protocol %q is not ssh or https", acct.Name, acct.Protocol)
		}
		names[acct.Name] = true
		for _, t := range acct.Track {
			if t.Kind != "self" && t.Kind != "org" && t.Kind != "group" {
				return fmt.Errorf("account %q: owner %q has kind %q, not self, org or group", acct.Name, t.Owner, t.Kind)
			}
		}
	}
	return nil
}

func (w Workspaces) validate() error {
	if w.Root == "" {
		return fmt.Errorf("root is empty")
	}
	if w.Agents < 1 || w.Agents > 64 {
		return fmt.Errorf("agents is %d, not 1 to 64", w.Agents)
	}
	names := map[string]bool{}
	for _, p := range w.Profiles {
		if p.Name == "" || strings.TrimSpace(p.Command) == "" {
			return fmt.Errorf("an agent profile needs a name and a command")
		}
		if names[p.Name] {
			return fmt.Errorf("two agent profiles are named %q", p.Name)
		}
		names[p.Name] = true
	}
	if w.Agent != "" && !names[w.Agent] {
		return fmt.Errorf("agent %q isn't one of the profiles", w.Agent)
	}
	if !slices.Contains(config.BuiltinLaunchers, w.Launcher) {
		return fmt.Errorf("launcher %q is not one of %s", w.Launcher, strings.Join(config.BuiltinLaunchers, ", "))
	}
	return nil
}

// Defaults proposes answers: the existing config's choices where there is
// one, otherwise what was detected, with the recommended features on.
func Defaults(det Detected, existing *config.Config, home string) Answers {
	a := Answers{
		BaseDir: "~/repos", Layout: "host/owner/name",
		Backups: true, Schedule: true, Linger: true, ReconcileAuto: "safe", Cleanup: "mark_stale",
	}
	if existing != nil {
		a.BaseDir = tildePath(existing.BaseDir, home)
		a.Layout = layoutOf(existing)
		a.Backups, a.RemoteSync = existing.Backup.Enabled, existing.RemoteSync.Enabled
		a.Update = existing.Sync.Update == "ff-only-clean"
		a.ReconcileAuto, a.Cleanup = existing.Reconcile.Auto, existing.Cleanup.Policy
		a.Schedule = false // re-running setup doesn't reinstall the agent unless asked
		a.Accounts = existingAccounts(existing)
	} else if det.Layout.Matches > 0 {
		a.BaseDir, a.Layout = tildePath(det.Layout.BaseDir, home), det.Layout.Layout
	}
	if len(a.Accounts) == 0 {
		a.Accounts = detectedAccounts(det)
	}
	a.Workspaces = defaultWorkspaces(det, existing, home)
	return a
}

// defaultWorkspaces proposes workspace answers: the existing config's when it
// sets workspaces up, else on when an agent was found on PATH.
func defaultWorkspaces(det Detected, existing *config.Config, home string) *Workspaces {
	if existing != nil && (len(existing.Agents) > 0 || len(existing.Workspaces) > 0) {
		d := existing.WorkspaceDefaults
		return &Workspaces{
			Root: tildePath(d.Root, home), Agents: d.Agents, Profiles: existing.Agents,
			Agent: d.Agent, Launcher: existing.Launchers.Default,
		}
	}
	if len(det.Agents) == 0 {
		return nil
	}
	return &Workspaces{Root: "~/workspaces", Agents: 2, Profiles: det.Agents, Launcher: "auto"}
}

func detectedAccounts(det Detected) []Account {
	perLogin := map[string]int{}
	for _, gh := range det.GH {
		perLogin[gh.Login]++
	}
	var accounts []Account
	for _, gh := range det.GH {
		acct := Account{
			Name: gh.Login, Type: "github", Host: gh.Host, Login: gh.Login,
			TokenCommand: fmt.Sprintf("gh auth token --hostname %s --user %s", gh.Host, gh.Login),
			Track:        []Track{{Owner: gh.Login, Kind: "self"}},
			SSHKey:       det.KeyFor(gh.Host, gh.Login),
			Protocol:     "https",
		}
		if perLogin[gh.Login] > 1 {
			acct.Name = gh.Login + "-" + strings.Split(gh.Host, ".")[0]
		}
		if gh.Host != "github.com" {
			acct.URL = "https://" + gh.Host
		}
		if acct.SSHKey != "" || gh.GitProtocol == "ssh" {
			acct.Protocol = "ssh"
		}
		accounts = append(accounts, acct)
	}
	return accounts
}

// existingAccounts reads accounts back from a config setup wrote, so a re-run
// starts from the current choices.
func existingAccounts(cfg *config.Config) []Account {
	var accounts []Account
	for _, ca := range cfg.Accounts {
		acct := Account{Name: ca.Name, SSHKey: ca.SSHKey, Git: ca.Git, Signing: ca.Signing, Protocol: "ssh", Type: "github"}
		if len(ca.Hosts) > 0 {
			acct.Host = ca.Hosts[0]
		}
		self := false
		for _, p := range cfg.Providers {
			if p.Account != ca.Name {
				continue
			}
			acct.Type, acct.URL = config.NormalizeProviderType(p.Type), p.URL
			acct.TokenCommand, acct.TokenEnv = p.TokenCommand, p.TokenEnv
			if p.CloneProtocol != "" {
				acct.Protocol = p.CloneProtocol
			}
			for _, r := range cfg.Discovery {
				switch {
				case r.Provider != p.Name:
				case r.Org != "":
					acct.Track = append(acct.Track, Track{Owner: r.Org, Kind: "org"})
				case r.Group != "":
					acct.Track = append(acct.Track, Track{Owner: r.Group, Kind: "group"})
				case r.User != "":
					acct.Login = r.User
					self = true
				default:
					self = true
				}
			}
		}
		// The account's own login is the owner it covers that isn't an
		// organization it tracks.
		for _, o := range ca.Owners {
			if acct.Login == "" && !acct.Tracks(o) {
				acct.Login = o
			}
		}
		if self && acct.Login != "" {
			acct.Track = append([]Track{{Owner: acct.Login, Kind: "self"}}, acct.Track...)
		}
		accounts = append(accounts, acct)
	}
	return accounts
}

// AdoptCandidates are existing clones that discovery won't pick up by
// itself: untracked, and either outside every account's tracked owners or
// not where the layout would put them. Adopting tracks them where they are.
func AdoptCandidates(det Detected, a Answers, tracked map[string]bool, home string) []Clone {
	var out []Clone
	for _, c := range det.Clones {
		if tracked[c.Path] {
			continue
		}
		if covered(c, a) && c.Path == templatePath(c, a, home) {
			continue // discovery finds it and records it where it is
		}
		out = append(out, c)
	}
	return out
}

// UnderBaseDir reports whether a clone lives under the answers' base_dir,
// the ones setup adopts unless told otherwise.
func UnderBaseDir(c Clone, a Answers, home string) bool {
	rel, err := filepath.Rel(expandFrom(a.BaseDir, home), c.Path)
	return err == nil && filepath.IsLocal(rel)
}

func covered(c Clone, a Answers) bool {
	id, err := repourl.Parse(c.Origin)
	if err != nil {
		return false
	}
	for _, acct := range a.Accounts {
		if acct.Host == id.Host && acct.Tracks(id.Owner) {
			return true
		}
	}
	return false
}

func templatePath(c Clone, a Answers, home string) string {
	id, err := repourl.Parse(c.Origin)
	if err != nil {
		return ""
	}
	tmpl, ok := config.Layouts[a.Layout]
	if !ok {
		return ""
	}
	rel, err := pathtemplate.Resolve(tmpl, pathtemplate.RepoData{Host: id.Host, Owner: id.Owner, Name: id.Name, FullName: id.FullName})
	if err != nil {
		return ""
	}
	return filepath.Join(expandFrom(a.BaseDir, home), rel)
}

func layoutOf(cfg *config.Config) string {
	if cfg.Layout != "" {
		return cfg.Layout
	}
	for name, tmpl := range config.Layouts {
		if cfg.PathTemplate == tmpl {
			return name
		}
	}
	return "custom"
}

// tildePath writes a path under home as ~/..., the way people write config.
func tildePath(path, home string) string {
	if rel, err := filepath.Rel(home, path); err == nil && filepath.IsLocal(rel) {
		return filepath.Join("~", rel)
	}
	return path
}

func expandFrom(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}

// HomeDir is the user's home directory, or "" if unknown.
func HomeDir() string {
	home, _ := os.UserHomeDir()
	return home
}
