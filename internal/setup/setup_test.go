package setup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ashtonian/gitten/internal/config"
)

// fakeRunner answers commands from a table keyed by the joined command line.
func fakeRunner(answers map[string]string) Runner {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		line := name + " " + strings.Join(args, " ")
		for prefix, out := range answers {
			if strings.HasPrefix(line, prefix) {
				return []byte(out), nil
			}
		}
		return nil, fmt.Errorf("no fake for %q", line)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fakeHome builds a home directory with an ssh config, keys and clones.
func fakeHome(t *testing.T) (home string, run Runner) {
	home = t.TempDir()
	write(t, filepath.Join(home, ".ssh", "config"), `Host github-work
  HostName github.com
  IdentityFile ~/.ssh/id_work
Include config.d/*
Host *
  AddKeysToAgent yes
`)
	write(t, filepath.Join(home, ".ssh", "config.d", "lab"), "Host lab gitlab-alt\n  HostName gitlab.com\n  User git\n")
	for _, k := range []string{"id_work", "id_personal"} {
		write(t, filepath.Join(home, ".ssh", k), "private")
		write(t, filepath.Join(home, ".ssh", k+".pub"), "ssh-ed25519 AAAA "+k+"@laptop")
	}
	write(t, filepath.Join(home, ".ssh", "known_hosts"), "")

	answers := map[string]string{
		"gh auth status --json hosts": `{"hosts":{"github.com":[
			{"state":"success","active":true,"host":"github.com","login":"me","scopes":"repo","gitProtocol":"https","tokenSource":"GITHUB_TOKEN"},
			{"state":"success","active":false,"host":"github.com","login":"me","scopes":"repo","gitProtocol":"https","tokenSource":"keyring"},
			{"state":"success","active":false,"host":"github.com","login":"me-at-acme","scopes":"repo, read:org","gitProtocol":"ssh","tokenSource":"keyring"}]}}`,
		"git config --global --get user.name":                                               "Me",
		"git config --global --get user.email":                                              "me@home",
		"git config --global --get-regexp":                                                  "includeif.gitdir:~/work/.path ~/.gitconfig-work",
		"git config --file " + filepath.Join(home, ".gitconfig-work") + " --get user.email": "me@acme.com",
	}
	for _, clone := range []struct{ rel, origin string }{
		{"projects/me/dots", "git@github.com:me/dots.git"},
		{"projects/acme/api", "git@github.com:acme/api.git"},
		{"projects/scratch", "https://github.com/someone/scratch.git"},
		{"projects/tool", "https://github.com/other/tool.git"},
		{"projects/lib", "https://github.com/other/lib.git"},
	} {
		dir := filepath.Join(home, clone.rel)
		write(t, filepath.Join(dir, ".git", "HEAD"), "ref: refs/heads/main\n")
		answers["git -C "+dir+" config --get remote.origin.url"] = clone.origin
	}
	return home, fakeRunner(answers)
}

func TestDetect(t *testing.T) {
	home, run := fakeHome(t)
	probe := fakeRunner(map[string]string{
		"ssh -T -o BatchMode=yes -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o ConnectTimeout=5 -i " + filepath.Join(home, ".ssh", "id_work") + " git@github.com": "Hi me-at-acme! You've successfully authenticated, but GitHub does not provide shell access.",
	})
	det := (&Detector{Home: home, Run: run, RunCombined: probe, Probe: true, LookPath: onPath("codex", "tmux"), Exists: exists("/Applications/Muxy.app")}).Detect(context.Background())

	// An env-var token gh lists beside a stored login gives way to it.
	if got := fmt.Sprint(det.GH); got != "[{github.com me false repo https keyring} {github.com me-at-acme false repo, read:org ssh keyring}]" {
		t.Errorf("gh accounts = %s", got)
	}
	if got := fmt.Sprint(det.SSHHosts); got != fmt.Sprintf("[{github-work github.com  %s} {lab gitlab.com git } {gitlab-alt gitlab.com git }]", filepath.Join(home, ".ssh", "id_work")) {
		t.Errorf("ssh hosts = %s", got)
	}
	if len(det.SSHKeys) != 2 || det.SSHKeys[1].Comment != "id_work@laptop" {
		t.Errorf("ssh keys = %+v", det.SSHKeys)
	}
	if det.KeyFor("github.com", "me-at-acme") != filepath.Join(home, ".ssh", "id_work") || det.KeyFor("github.com", "me") != "" {
		t.Errorf("key logins = %+v", det.SSHKeys)
	}
	if got := fmt.Sprint(det.Identities); got != "[{Me me@home global} { me@acme.com gitdir:~/work/}]" {
		t.Errorf("identities = %s", got)
	}
	if len(det.Clones) != 5 {
		t.Errorf("clones = %+v", det.Clones)
	}
	// Three flat clones of other people's projects outnumber the two in
	// owner/name, but your own repo decides.
	if det.Layout != (LayoutGuess{BaseDir: filepath.Join(home, "projects"), Layout: "owner/name", Matches: 1}) {
		t.Errorf("layout guess = %+v", det.Layout)
	}
	if len(det.Agents) != 1 || det.Agents[0].Name != "codex" || fmt.Sprint(det.Launchers) != "[tmux muxy]" {
		t.Errorf("agents %+v, launchers %v", det.Agents, det.Launchers)
	}
}

// onPath is a LookPath that finds only names.
func onPath(names ...string) func(string) (string, error) {
	return func(name string) (string, error) {
		for _, n := range names {
			if n == name {
				return "/usr/bin/" + name, nil
			}
		}
		return "", fmt.Errorf("%s not found", name)
	}
}

func exists(paths ...string) func(string) bool {
	return func(p string) bool {
		for _, x := range paths {
			if x == p {
				return true
			}
		}
		return false
	}
}

func TestDefaults_FromDetection(t *testing.T) {
	home, run := fakeHome(t)
	det := (&Detector{Home: home, Run: run, LookPath: onPath(), Exists: exists()}).Detect(context.Background())
	det.SSHKeys[1].Logins = map[string]string{"github.com": "me-at-acme"}
	a := Defaults(det, nil, home)
	if a.Workspaces != nil {
		t.Errorf("workspaces are on with no agent found: %+v", a.Workspaces)
	}
	det.Agents = []config.AgentProfile{{Name: "claude", Command: "claude"}}
	if w := Defaults(det, nil, home).Workspaces; w == nil || w.Root != "~/workspaces" || w.Agents != 2 || w.Profiles[0].Name != "claude" || w.Launcher != "auto" {
		t.Errorf("with claude found, workspaces = %+v", w)
	}

	if a.BaseDir != "~/projects" || a.Layout != "owner/name" {
		t.Errorf("layout = %s %s", a.BaseDir, a.Layout)
	}
	if !a.Backups || !a.Schedule || a.RemoteSync || a.ReconcileAuto != "safe" {
		t.Errorf("features = %+v", a)
	}
	want := []Account{
		{Name: "me", Type: "github", Host: "github.com", Login: "me",
			TokenCommand: "gh auth token --hostname github.com --user me",
			Track:        []Track{{Owner: "me", Kind: "self"}}, Protocol: "https"},
		{Name: "me-at-acme", Type: "github", Host: "github.com", Login: "me-at-acme",
			TokenCommand: "gh auth token --hostname github.com --user me-at-acme",
			Track:        []Track{{Owner: "me-at-acme", Kind: "self"}}, SSHKey: filepath.Join(home, ".ssh", "id_work"), Protocol: "ssh"},
	}
	if !reflect.DeepEqual(a.Accounts, want) {
		t.Errorf("accounts =\n%+v\nwant\n%+v", a.Accounts, want)
	}

	// Clones discovery will find where they are aren't offered for adoption.
	got := AdoptCandidates(det, a, nil, home)
	var paths []string
	for _, c := range got {
		paths = append(paths, strings.TrimPrefix(c.Path, home+"/"))
	}
	if strings.Join(paths, " ") != "projects/acme/api projects/lib projects/scratch projects/tool" {
		t.Errorf("adopt candidates = %v", paths)
	}
}

func sampleAnswers() Answers {
	return Answers{
		BaseDir: "~/projects", Layout: "owner/name", Backups: true, ReconcileAuto: "safe", Cleanup: "mark_stale",
		Accounts: []Account{{
			Name: "work", Type: "github", Host: "github.com", Login: "me-at-acme",
			TokenCommand: "gh auth token --hostname github.com --user me-at-acme",
			Track: []Track{{Owner: "me-at-acme", Kind: "self"}, {Owner: "acme", Kind: "org", Names: []string{"billing", "payments"}, Skip: []string{"billing-sandbox"}},
				{Owner: "acme-labs", Kind: "org", Languages: []string{"Go"}, Topics: []string{"service"}, NamePattern: "^svc-"}},
			SSHKey: "~/.ssh/id_work", Protocol: "ssh",
			Git:     config.GitIdentity{Name: "Me", Email: "me@acme.com"},
			Signing: config.Signing{Mode: "off"},
			BaseDir: "~/work",
		}},
	}
}

func render(t *testing.T, path string, a Answers) string {
	t.Helper()
	doc, err := config.OpenDocument(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Render(doc, a); err != nil {
		t.Fatal(err)
	}
	if err := doc.Save(t.Context()); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	return string(data)
}

func TestRender(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	write(t, path, "# keep me\npath_template: \"{{.Name}}\"\nrepos:\n  - url: git@github.com:x/y.git # mine\n")
	a := sampleAnswers()
	first := render(t, path, a)
	if second := render(t, path, a); second != first {
		t.Fatalf("rendering twice changed the file:\n%s\nthen\n%s", first, second)
	}
	for _, want := range []string{"# keep me", "url: git@github.com:x/y.git # mine", "layout: owner/name"} {
		if !strings.Contains(first, want) {
			t.Errorf("missing %q:\n%s", want, first)
		}
	}
	if strings.Contains(first, "path_template") {
		t.Errorf("path_template should give way to layout:\n%s", first)
	}

	cfg, err := config.Load(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if a := cfg.AccountFor("github.com/acme/api", "work/acme"); a == nil || a.Git.Email != "me@acme.com" {
		t.Errorf("account for acme/api = %+v", a)
	}
	if got := cfg.ResolveForRepo("github.com/acme/api").BaseDir; got != config.ExpandHome("~/work") {
		t.Errorf("acme repos base_dir = %s", got)
	}
	var rules []string
	for _, r := range cfg.Discovery {
		rules = append(rules, fmt.Sprintf("%s:%s%s%v", r.Name, r.Org, r.User, r.Affiliation))
	}
	if strings.Join(rules, " ") != "work:[owner] work/acme:acme[] work/acme-labs:acme-labs[]" {
		t.Errorf("rules = %v", rules)
	}

	// Untracking an org removes its rule and nothing else.
	a.Accounts[0].Track = a.Accounts[0].Track[:2]
	third := render(t, path, a)
	if strings.Contains(third, "acme-labs") || !strings.Contains(third, "work/acme") {
		t.Errorf("after untracking acme-labs:\n%s", third)
	}
}

// A re-run starts from what setup wrote.
func TestDefaults_ReadBackWhatRenderWrote(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	want := sampleAnswers()
	render(t, path, want)
	cfg, err := config.Load(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	got := Defaults(Detected{}, cfg, "/nonexistent-home")
	want.Accounts[0].BaseDir = "" // kept in the override, not read back
	for i, rule := range []string{"work", "work/acme", "work/acme-labs"} {
		want.Accounts[0].Track[i].Rule = rule // each owner's rule, by name
	}
	if !reflect.DeepEqual(got.Accounts, want.Accounts) {
		t.Errorf("accounts read back =\n%+v\nwant\n%+v", got.Accounts, want.Accounts)
	}
	if got.Layout != "owner/name" || got.Schedule {
		t.Errorf("re-run answers = %+v", got)
	}

	// sync.update round-trips too.
	want.Update = true
	render(t, path, want)
	if cfg, err = config.Load(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	if cfg.Sync.Update != "ff-only-clean" || !Defaults(Detected{}, cfg, "/nonexistent-home").Update {
		t.Errorf("sync.update = %q", cfg.Sync.Update)
	}
}

// Rules added after setup -- a search, another user's repos, a second rule
// for an organization it tracks -- survive a re-run: setup edits the rules
// it manages, each by its own name, and leaves the rest as they are.
func TestRender_KeepsRulesItDoesNotManage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	render(t, path, sampleAnswers())
	doc, err := config.OpenDocument(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []config.DiscoveryRule{
		{Name: "acme-ledger", Provider: "work", Org: "acme", Names: []string{"ledger"}},
		{Name: "search", Provider: "work", Query: "org:acme payments in:readme"},
		{Name: "someone", Provider: "work", User: "someone"},
	} {
		if err := doc.Upsert("discovery", config.Named(r.Name), r); err != nil {
			t.Fatal(err)
		}
	}
	if err := doc.Save(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}

	a := Defaults(Detected{}, cfg, "/nonexistent-home")
	var tracks []string
	for _, tr := range a.Accounts[0].Track {
		tracks = append(tracks, tr.Owner+":"+tr.Rule+":"+strings.Join(tr.Names, ","))
	}
	if got := strings.Join(tracks, " "); got != "me-at-acme:work: acme:work/acme:billing,payments acme-labs:work/acme-labs: acme:acme-ledger:ledger" {
		t.Errorf("tracks read back = %s", got)
	}

	// Untrack acme-labs and re-render: its rule goes, the rest stay.
	a.Accounts[0].Track = slices.DeleteFunc(a.Accounts[0].Track, func(tr Track) bool { return tr.Owner == "acme-labs" })
	render(t, path, a)
	if cfg, err = config.Load(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	var rules []string
	for _, r := range cfg.Discovery {
		rules = append(rules, r.Name)
	}
	if got := strings.Join(rules, " "); got != "work work/acme acme-ledger search someone" {
		t.Errorf("rules after the re-run = %s", got)
	}
}

func TestGuessLayout_DeliberateTreesOutweighFlatClones(t *testing.T) {
	root := "/home/me/projects"
	clone := func(rel, origin string) Clone {
		return Clone{Path: filepath.Join(root, rel), Origin: origin, Root: root}
	}
	clones := []Clone{
		clone("a", "git@github.com:me/a.git"), clone("b", "git@github.com:me/b.git"), clone("c", "git@github.com:me/c.git"),
		clone("me/d", "git@github.com:me/d.git"), clone("me/e", "git@github.com:me/e.git"),
	}
	if got := guessLayout(clones, []string{"me"}); got.Layout != "owner/name" || got.Matches != 2 {
		t.Errorf("guess = %+v; two owner/name clones should outweigh three flat ones", got)
	}
}

func TestRender_Workspaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	write(t, path, "agents:\n  - name: mine\n    command: mine --fast # keep\n")
	a := sampleAnswers()
	a.Workspaces = &Workspaces{Root: "~/agents", Agents: 4, Launcher: "tmux", Agent: "claude", Profiles: []config.AgentProfile{
		{Name: "claude", Command: "claude-wrapper", Resume: "claude-wrapper --continue"}, {Name: "codex", Command: "codex"},
	}}
	first := render(t, path, a)
	if second := render(t, path, a); second != first {
		t.Fatalf("rendering twice changed the file:\n%s\nthen\n%s", first, second)
	}
	cfg, err := config.Load(t.Context(), path)
	if err != nil {
		t.Fatalf("%v\n%s", err, first)
	}
	d := cfg.WorkspaceDefaults
	if d.Root != config.ExpandHome("~/agents") || d.Agents != 4 || d.Agent != "claude" || cfg.Launchers.Default != "tmux" {
		t.Errorf("workspace_defaults = %+v, launcher %s", d, cfg.Launchers.Default)
	}
	if len(cfg.Agents) != 3 || cfg.FindAgent("mine") == nil || cfg.FindAgent("claude").Resume != "claude-wrapper --continue" {
		t.Errorf("agents = %+v; a profile setup doesn't manage is kept", cfg.Agents)
	}

	// A re-run starts from what setup wrote.
	got := Defaults(Detected{}, cfg, "/nonexistent-home").Workspaces
	if got == nil || got.Agents != 4 || got.Launcher != "tmux" || got.Agent != "claude" || len(got.Profiles) != 3 {
		t.Errorf("workspaces read back = %+v", got)
	}

	a.Workspaces.Agent = "nope"
	if err := a.Validate(); err == nil {
		t.Error("an agent that isn't a profile validated")
	}
	a.Workspaces.Agent, a.Workspaces.Launcher = "", "konsole"
	if err := a.Validate(); err == nil {
		t.Error("an unknown launcher validated")
	}
}
