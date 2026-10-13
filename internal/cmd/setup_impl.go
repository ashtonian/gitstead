package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/huh/v2"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/discovery"
	"github.com/ashtonian/git-mgr/internal/scheduler"
	"github.com/ashtonian/git-mgr/internal/setup"
)

// SetupCmd walks through setting git-mgr up, or changing an existing setup.
type SetupCmd struct {
	Yes         bool   `help:"Accept the detected and recommended answers without asking."`
	Answers     string `help:"Read the answers from this YAML file instead of asking." type:"path"`
	SaveAnswers string `help:"Also write the answers to this file, to repeat this setup elsewhere." name:"save-answers" type:"path"`
	DryRun      bool   `help:"Show the config changes and checks; change nothing." name:"dry-run"`
	Offline     bool   `help:"Skip network checks: ssh -T and provider API calls."`

	noSchedule bool // set by bootstrap, which installs the agent itself
}

func (c *SetupCmd) Run(ctx *Context) error {
	interactive := !c.Yes && c.Answers == ""
	if interactive && (ctx.JSON || !term.IsTerminal(int(os.Stdin.Fd()))) {
		return errors.New("setup asks questions, so it needs a terminal; use --yes to accept the recommended answers, or --answers <file>")
	}
	home := setup.HomeDir()
	configPath := ctx.ConfigPath()
	existing, err := existingConfig(configPath)
	if err != nil {
		return err
	}
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	tracked := map[string]bool{}
	for _, r := range st.Repos {
		tracked[r.LocalPath] = true
	}

	fmt.Println("Looking at this machine: gh accounts, SSH keys and config, git identities, existing clones...")
	det := (&setup.Detector{Home: home, Run: setup.ExecRunner, RunCombined: setup.ExecCombinedRunner, Probe: !c.Offline}).Detect(ctx.GoCtx())

	var answers setup.Answers
	switch {
	case c.Answers != "":
		if answers, err = readAnswers(c.Answers); err != nil {
			return err
		}
	case interactive:
		if answers, err = askSetup(ctx, det, setup.Defaults(det, existing, home), tracked, home, !c.Offline); err != nil {
			if errors.Is(err, huh.ErrUserAborted) {
				fmt.Println("Setup cancelled; nothing changed.")
				return nil
			}
			return err
		}
	default:
		answers = setup.Defaults(det, existing, home)
		for _, clone := range setup.AdoptCandidates(det, answers, tracked, home) {
			if setup.UnderBaseDir(clone, answers, home) {
				answers.Adopt = append(answers.Adopt, clone.Path)
			}
		}
	}

	answers.Adopt = slices.DeleteFunc(answers.Adopt, func(p string) bool { return tracked[p] })
	if c.noSchedule {
		answers.Schedule = false
	}

	doc, err := config.OpenDocument(configPath)
	if err != nil {
		return err
	}
	before, _ := os.ReadFile(configPath)
	if err := setup.Render(doc, answers); err != nil {
		return err
	}
	after, err := doc.Bytes()
	if err != nil {
		return err
	}
	candidate, err := loadCandidate(after)
	if err != nil {
		return fmt.Errorf("the answers make an invalid config: %w", err)
	}

	printSetupReview(configPath, before, after, answers)
	checks := (setup.Checker{Probe: !c.Offline, RunCombined: setup.ExecCombinedRunner}).Check(ctx.GoCtx(), candidate)
	failed := printChecks(checks)
	// What setup did, as --json prints it: the config it wrote, or would.
	result := struct {
		Config  string        `json:"config"`
		Written bool          `json:"written"`
		Text    string        `json:"text"`
		Checks  []doctorCheck `json:"checks"`
		// Schedule is install-agent's result, when setup installed one.
		Schedule json.RawMessage `json:"schedule,omitempty"`
	}{Config: configPath, Text: string(after), Checks: []doctorCheck{}}
	for _, ch := range checks {
		result.Checks = append(result.Checks, doctorCheck{Level: string(ch.Status), Message: ch.Message})
	}
	if c.DryRun {
		fmt.Println("\nDry run: nothing written.")
		return ctx.finish(result, nil)
	}
	if interactive {
		write := true
		title := "Write this config and continue?"
		if failed > 0 {
			title = fmt.Sprintf("%d check(s) failed. Write this config anyway?", failed)
		}
		if err := huh.NewForm(huh.NewGroup(huh.NewConfirm().Title(title).Value(&write))).Run(); err != nil || !write {
			fmt.Println("Nothing written.")
			return nil
		}
	}

	if err := doc.Save(); err != nil {
		return err
	}
	result.Written = true
	fmt.Printf("Wrote %s\n", tildify(configPath))
	if c.SaveAnswers != "" {
		if err := writeAnswers(config.ExpandHome(c.SaveAnswers), answers); err != nil {
			return err
		}
		fmt.Printf("Answers saved to %s; repeat this setup with `git-mgr setup --answers %s`.\n", c.SaveAnswers, c.SaveAnswers)
	}

	schedule, err := afterSetup(ctx, candidate, answers)
	result.Schedule = schedule
	if err != nil {
		return ctx.finish(result, err)
	}
	fmt.Println("\nNext: `git-mgr plan` shows what will be cloned and changed; `git-mgr apply` does it.")
	if answers.Workspaces != nil {
		fmt.Println("Then give a repo its agents: `git-mgr ws create <name> --repo <repo> --save`, and open them with `git-mgr ws open <name>`.")
	}
	return ctx.finish(result, outcome(failed, len(checks), "setup checks failed"))
}

// afterSetup adopts the chosen clones and installs the background agent,
// returning the agent's result under --json.
func afterSetup(ctx *Context, cfg *config.Config, a setup.Answers) (json.RawMessage, error) {
	if len(a.Adopt) > 0 {
		st, err := ctx.LoadState()
		if err != nil {
			return nil, err
		}
		failed := 0
		for _, path := range a.Adopt {
			if err := (&AdoptCmd{}).adoptSingle(ctx, cfg, st, path); err != nil {
				fmt.Fprintf(os.Stderr, "  %s: %v\n", tildify(path), err)
				failed++
			}
		}
		if err := outcome(failed, len(a.Adopt), "clones could not be adopted"); err != nil {
			return nil, err
		}
	}
	if a.Schedule {
		fmt.Println("\nInstalling the background sync agent:")
		agent := &InstallAgentCmd{Interval: 15 * time.Minute, Log: "~/.local/share/git-mgr/git-mgr.log", Linger: a.Linger}
		doc, err := ctx.captured(agent.Run)
		if err != nil {
			return doc, fmt.Errorf("installing the background agent: %w", err)
		}
		return doc, nil
	}
	return nil, nil
}

// existingConfig loads the config at path, or nil if there is none yet.
func existingConfig(path string) (*config.Config, error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, fmt.Errorf("%s is invalid; fix it or move it aside before running setup: %w", path, err)
	}
	return cfg, nil
}

// loadCandidate loads a rendered config without writing it in place.
func loadCandidate(data []byte) (*config.Config, error) {
	dir, err := os.MkdirTemp("", "git-mgr-setup-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return nil, err
	}
	return config.Load(path)
}

func readAnswers(path string) (setup.Answers, error) {
	var a setup.Answers
	data, err := os.ReadFile(config.ExpandHome(path))
	if err != nil {
		return a, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&a); err != nil {
		return a, fmt.Errorf("reading answers %s: %w", path, err)
	}
	return a, a.Validate()
}

func writeAnswers(path string, a setup.Answers) error {
	data, err := yaml.Marshal(a)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func printSetupReview(path string, before, after []byte, a setup.Answers) {
	fmt.Printf("\nChanges to %s:\n", tildify(path))
	if d := configDiff(before, after); d != "" {
		fmt.Println(d)
	} else {
		fmt.Println("  (none)")
	}
	if len(a.Adopt) > 0 {
		fmt.Printf("\nTrack %d existing clone(s) where they are:\n", len(a.Adopt))
		for _, p := range a.Adopt {
			fmt.Printf("  + %s\n", tildify(p))
		}
	}
	if a.Schedule {
		fmt.Println("\nInstall the background agent: `git-mgr sync` every 15 minutes.")
	}
}

func printChecks(checks []setup.Check) (failed int) {
	if len(checks) == 0 {
		return 0
	}
	fmt.Println("\nChecks:")
	for _, c := range checks {
		label := map[setup.Status]string{setup.Pass: "ok  ", setup.Info: "--  ", setup.Warn: "WARN", setup.Fail: "FAIL"}[c.Status]
		fmt.Printf("  %s %s\n", label, tildify(c.Message))
		if c.Status == setup.Fail {
			failed++
		}
	}
	return failed
}

// configDiff renders the change from before to after as a unified diff,
// using git so no diff library is needed.
func configDiff(before, after []byte) string {
	dir, err := os.MkdirTemp("", "git-mgr-diff-")
	if err != nil {
		return ""
	}
	defer os.RemoveAll(dir)
	a, b := filepath.Join(dir, "now"), filepath.Join(dir, "after")
	_ = os.WriteFile(a, before, 0o600)
	_ = os.WriteFile(b, after, 0o600)
	out, _ := exec.Command("git", "--no-pager", "diff", "--no-index", "--no-color", "--", a, b).Output()
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "@@") {
			return strings.Join(lines[i:], "\n")
		}
	}
	return ""
}

// askSetup asks the setup questions, starting from defaults.
func askSetup(ctx *Context, det setup.Detected, a setup.Answers, tracked map[string]bool, home string, online bool) (setup.Answers, error) {
	if err := askLayout(det, &a, home); err != nil {
		return a, err
	}
	if err := askAccounts(ctx.GoCtx(), det, &a, online); err != nil {
		return a, err
	}
	if err := askFeatures(&a); err != nil {
		return a, err
	}
	if err := askWorkspaces(det, &a); err != nil {
		return a, err
	}
	if a.Schedule && scheduler.DefaultKind() == scheduler.Systemd {
		err := huh.NewConfirm().Title("Keep syncing while you are logged out?").
			Description("Enables lingering (loginctl enable-linger); without it the timer stops at logout. Recommended.").
			Value(&a.Linger).Run()
		if err != nil {
			return a, err
		}
	}
	return a, askAdopt(det, &a, tracked, home)
}

func askLayout(det setup.Detected, a *setup.Answers, home string) error {
	found := fmt.Sprintf("Found %d gh %s, %d SSH %s, %d git %s and %d %s.",
		len(det.GH), plural(len(det.GH), "account", "accounts"), len(det.SSHKeys), plural(len(det.SSHKeys), "key", "keys"),
		len(det.Identities), plural(len(det.Identities), "identity", "identities"), len(det.Clones), plural(len(det.Clones), "clone", "clones"))
	if det.Layout.Matches > 0 {
		found += fmt.Sprintf("\n%d of your clones already sit in %s as %s.", det.Layout.Matches, tildify(det.Layout.BaseDir), det.Layout.Layout)
	}
	layouts := []huh.Option[string]{
		huh.NewOption("owner/name       e.g. acme/api", "owner/name"),
		huh.NewOption("host/owner/name  e.g. github.com/acme/api", "host/owner/name"),
		huh.NewOption("name             e.g. api (two repos with one name can't both live there)", "name"),
	}
	if a.Layout == "custom" {
		layouts = append(layouts, huh.NewOption("keep my path_template", "custom"))
	}
	return huh.NewForm(huh.NewGroup(
		huh.NewNote().Title("git-mgr setup").Description(found+"\n\nEach answer starts at what was found; nothing is written until you confirm the review."),
		huh.NewInput().Title("Where should repos live?").Value(&a.BaseDir).Validate(nonEmpty),
		huh.NewSelect[string]().Title("How are they laid out under it?").Options(layouts...).Value(&a.Layout),
	)).Run()
}

func askAccounts(ctx context.Context, det setup.Detected, a *setup.Answers, online bool) error {
	if len(a.Accounts) > 0 {
		var keep []string
		var opts []huh.Option[string]
		for _, acct := range a.Accounts {
			opts = append(opts, huh.NewOption(fmt.Sprintf("%s on %s", orDefault(acct.Login, acct.Name), acct.Host), acct.Name).Selected(true))
			keep = append(keep, acct.Name)
		}
		if err := huh.NewForm(huh.NewGroup(huh.NewMultiSelect[string]().
			Title("Which accounts should git-mgr manage?").
			Description("Each one gets its own token, SSH key and commit identity in the repos it covers.").
			Options(opts...).Value(&keep))).Run(); err != nil {
			return err
		}
		a.Accounts = slices.DeleteFunc(a.Accounts, func(acct setup.Account) bool { return !slices.Contains(keep, acct.Name) })
	}
	for {
		add := false
		description := "For GitLab, Gitea/Forgejo, or a GitHub account gh isn't logged in to."
		if len(a.Accounts) == 0 {
			description = "None found. Without an account git-mgr still manages the clones you adopt; add one to discover repos from a provider."
		}
		if err := huh.NewForm(huh.NewGroup(huh.NewConfirm().
			Title("Add an account by hand?").
			Description(description).
			Value(&add))).Run(); err != nil {
			return err
		}
		if !add {
			break
		}
		acct, err := askNewAccount()
		if err != nil {
			return err
		}
		a.Accounts = append(a.Accounts, acct)
	}
	for i := range a.Accounts {
		if err := askAccount(ctx, det, &a.Accounts[i], online); err != nil {
			return err
		}
	}
	return nil
}

func askNewAccount() (setup.Account, error) {
	acct := setup.Account{Type: "github", Host: "github.com", Protocol: "ssh"}
	var token string
	err := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().Title("Provider").Options(
			huh.NewOption("GitHub or GitHub Enterprise", "github"),
			huh.NewOption("GitLab", "gitlab"),
			huh.NewOption("Gitea or Forgejo", "gitea")).Value(&acct.Type),
		huh.NewInput().Title("Host").Description("github.com, gitlab.com, or your server's name").Value(&acct.Host).Validate(nonEmpty),
		huh.NewInput().Title("Your login there").Value(&acct.Login).Validate(nonEmpty),
		huh.NewInput().Title("API token").
			Description("An environment variable holding it (MY_TOKEN), or a command that prints it (op read ..., glab auth token ...).").
			Value(&token).Validate(nonEmpty),
	)).Run()
	acct.Name = acct.Login
	if acct.Host != "github.com" && acct.Host != "gitlab.com" {
		acct.URL = "https://" + acct.Host
	}
	if strings.ContainsAny(token, " /") {
		acct.TokenCommand = token
	} else {
		acct.TokenEnv = token
	}
	acct.Track = []setup.Track{{Owner: acct.Login, Kind: "self"}}
	return acct, err
}

func askAccount(ctx context.Context, det setup.Detected, acct *setup.Account, online bool) error {
	title := fmt.Sprintf("Account %s on %s", orDefault(acct.Login, acct.Name), acct.Host)
	if err := huh.NewForm(huh.NewGroup(
		huh.NewNote().Title(title),
		huh.NewInput().Title("Name for this account in git-mgr").Value(&acct.Name).Validate(nonEmpty),
	)).Run(); err != nil {
		return err
	}
	if err := askTrack(ctx, acct, online); err != nil {
		return err
	}

	keyChoice := acct.SSHKey
	if acct.Protocol == "https" {
		keyChoice = "https"
	}
	keyOpts := []huh.Option[string]{huh.NewOption("ssh with your agent or default keys", "")}
	for _, k := range det.SSHKeys {
		label := tildify(k.Path)
		if login := k.Logins[acct.Host]; login != "" {
			label += "  (signs in as " + login + ")"
		} else if k.Comment != "" {
			label += "  (" + k.Comment + ")"
		}
		keyOpts = append(keyOpts, huh.NewOption(label, k.Path))
	}
	keyOpts = append(keyOpts, huh.NewOption("https, through git's credential helper", "https"))

	identity := identityKey(acct.Git)
	idOpts := []huh.Option[string]{huh.NewOption("your global git identity", "")}
	for _, id := range det.Identities {
		idOpts = append(idOpts, huh.NewOption(fmt.Sprintf("%s <%s>  (%s)", id.Name, id.Email, id.Where), identityKey(config.GitIdentity{Name: id.Name, Email: id.Email})))
	}
	if identity != "" && !slices.ContainsFunc(idOpts, func(o huh.Option[string]) bool { return o.Value == identity }) {
		idOpts = append(idOpts, huh.NewOption(fmt.Sprintf("%s <%s>", acct.Git.Name, acct.Git.Email), identity))
	}
	idOpts = append(idOpts, huh.NewOption("enter another", "other"))

	signing := orDefault(acct.Signing.Mode, "inherit")
	if err := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().Title("How should git reach "+acct.Host+"?").Options(keyOpts...).Value(&keyChoice),
		huh.NewSelect[string]().Title("Commit as").Description("Written into each repo the account covers, so you and your agents commit as this account.").Options(idOpts...).Value(&identity),
		huh.NewSelect[string]().Title("Signing").Options(
			huh.NewOption("leave it to your global git config", "inherit"),
			huh.NewOption("sign every commit and tag", "required"),
			huh.NewOption("never sign in these repos (agents never wait for a passphrase)", "off")).Value(&signing),
		huh.NewInput().Title("Put this account's repos somewhere else? (optional)").Placeholder("e.g. ~/work").Value(&acct.BaseDir),
	)).Run(); err != nil {
		return err
	}

	acct.SSHKey, acct.Protocol = keyChoice, "ssh"
	if keyChoice == "https" {
		acct.SSHKey, acct.Protocol = "", "https"
	}
	acct.SSHKey = tildify(acct.SSHKey)
	switch identity {
	case "":
		acct.Git = config.GitIdentity{}
	case "other":
		if err := huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Commit name").Value(&acct.Git.Name),
			huh.NewInput().Title("Commit email").Value(&acct.Git.Email).Validate(nonEmpty),
		)).Run(); err != nil {
			return err
		}
	default:
		name, email, _ := strings.Cut(identity, "\x00")
		acct.Git = config.GitIdentity{Name: name, Email: email}
	}

	acct.Signing.Mode = signing
	if signing == "required" {
		if acct.Signing.Key == "" && acct.SSHKey != "" {
			acct.Signing.Key = acct.SSHKey + ".pub"
		}
		if err := huh.NewForm(huh.NewGroup(huh.NewInput().
			Title("Signing key").
			Description("An SSH public key (.pub or key::...) signs with SSH; a GPG key ID signs with GPG.").
			Value(&acct.Signing.Key).Validate(nonEmpty))).Run(); err != nil {
			return err
		}
	} else if signing == "inherit" {
		acct.Signing = config.Signing{}
	}
	return nil
}

// askTrack chooses which owners' repos an account tracks, from the
// provider's list when it can be fetched.
func askTrack(ctx context.Context, acct *setup.Account, online bool) error {
	var owners []discovery.Owner
	if online {
		pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		owners, _ = discovery.ListOwners(pctx, config.Provider{Name: acct.Name, Type: acct.Type, URL: acct.URL,
			TokenCommand: acct.TokenCommand, TokenEnv: acct.TokenEnv}, config.Defaults().Sync.APITimeout)
		cancel()
	}
	if len(owners) == 0 {
		list := strings.Join(acct.Owners(), ", ")
		if err := huh.NewForm(huh.NewGroup(huh.NewInput().
			Title("Whose repos should this account track?").
			Description("Your login for your own repos, plus any organizations or groups, comma-separated.").
			Value(&list).Validate(nonEmpty))).Run(); err != nil {
			return err
		}
		acct.Track = nil
		for _, o := range strings.Split(list, ",") {
			if o = strings.TrimSpace(o); o == "" {
				continue
			}
			kind := "org"
			if acct.Type == "gitlab" {
				kind = "group"
			}
			if strings.EqualFold(o, acct.Login) || acct.Login == "" && len(acct.Track) == 0 {
				kind = "self"
			}
			acct.Track = append(acct.Track, setup.Track{Owner: o, Kind: kind})
		}
		return nil
	}

	var opts []huh.Option[string]
	var chosen []string
	for _, o := range owners {
		label := o.Login + "  (organization)"
		if o.Kind == "self" {
			label = o.Login + "  (your own repos, private ones included)"
			if acct.Login == "" {
				acct.Login = o.Login
			}
		} else if o.Kind == "group" {
			label = o.Login + "  (group)"
		}
		selected := acct.Tracks(o.Login) || o.Kind == "self" && len(acct.Track) == 0
		opts = append(opts, huh.NewOption(label, o.Login).Selected(selected))
		if selected {
			chosen = append(chosen, o.Login)
		}
	}
	if err := huh.NewForm(huh.NewGroup(huh.NewMultiSelect[string]().
		Title("Whose repos should this account track?").
		Description("Each one becomes a discovery rule; untick one later to stop tracking it.").
		Options(opts...).Filterable(true).Value(&chosen))).Run(); err != nil {
		return err
	}
	acct.Track = nil
	for _, o := range owners {
		if slices.Contains(chosen, o.Login) {
			acct.Track = append(acct.Track, setup.Track{Owner: o.Login, Kind: o.Kind})
		}
	}
	return nil
}

func askFeatures(a *setup.Answers) error {
	return huh.NewForm(huh.NewGroup(
		huh.NewNote().Title("Features").Description("Recommended answers are preselected; everything else stays off until you turn it on."),
		huh.NewConfirm().Title("Back up uncommitted work during sync?").
			Description("Snapshots that never touch your working tree or the index lock. Recommended.").Value(&a.Backups),
		huh.NewConfirm().Title("Run sync in the background every 15 minutes?").
			Description("Installs a launchd agent, systemd timer or cron entry. Recommended.").Value(&a.Schedule),
		huh.NewConfirm().Title("Push backup refs to each repo's remote?").
			Description("Offsite copies of your backups under refs/git-mgr/*; visible to anyone who can read the remote.").Value(&a.RemoteSync),
		huh.NewSelect[string]().Title("When config changes, what may sync change by itself?").Options(
			huh.NewOption("everything except moving or deleting directories (recommended)", "safe"),
			huh.NewOption("everything, moves included", "all"),
			huh.NewOption("nothing; I'll run git-mgr apply", "none")).Value(&a.ReconcileAuto),
		huh.NewSelect[string]().Title("When a repo disappears upstream").Options(
			huh.NewOption("mark it stale and keep it (recommended)", "mark_stale"),
			huh.NewOption("stop tracking it after 30 days; keep the files", "remove"),
			huh.NewOption("after 30 days, stop tracking it and move its clone into the archive", "archive"),
			huh.NewOption("after 30 days, archive its unique work and delete it", "remove_and_delete"),
			huh.NewOption("ignore it", "ignore")).Value(&a.Cleanup),
	)).Run()
}

// askWorkspaces asks whether to run coding agents in workspaces and, if so,
// with which agents, how many per workspace, where, and opened by what.
func askWorkspaces(det setup.Detected, a *setup.Answers) error {
	on := a.Workspaces != nil
	desc := "Each agent works in a copy of its own -- a worktree of the repo on a branch of its own -- and git-mgr opens them side by side in tmux, iTerm or Muxy."
	if len(det.Agents) > 0 {
		var names []string
		for _, p := range det.Agents {
			names = append(names, p.Name)
		}
		desc += " Found " + strings.Join(names, " and ") + " on your PATH."
	}
	if err := huh.NewConfirm().Title("Run coding agents in workspaces?").Description(desc).Value(&on).Run(); err != nil {
		return err
	}
	if !on {
		a.Workspaces = nil
		return nil
	}
	w := a.Workspaces
	if w == nil {
		w = &setup.Workspaces{Root: "~/workspaces", Agents: 2, Launcher: "auto"}
	}
	known := slices.Clone(w.Profiles)
	for _, p := range det.Agents {
		if !slices.ContainsFunc(known, func(k config.AgentProfile) bool { return k.Name == p.Name }) {
			known = append(known, p)
		}
	}
	var chosen []string
	var profileOpts []huh.Option[string]
	for _, p := range known {
		selected := len(w.Profiles) == 0 || slices.ContainsFunc(w.Profiles, func(k config.AgentProfile) bool { return k.Name == p.Name })
		profileOpts = append(profileOpts, huh.NewOption(fmt.Sprintf("%s  (%s)", p.Name, p.Command), p.Name).Selected(selected))
		if selected {
			chosen = append(chosen, p.Name)
		}
	}
	agents := strconv.Itoa(w.Agents)
	launcherOpts := []huh.Option[string]{huh.NewOption("auto: tmux inside tmux, iTerm inside iTerm, else tmux (recommended)", "auto")}
	for _, l := range det.Launchers {
		launcherOpts = append(launcherOpts, huh.NewOption(l, l))
	}
	if w.Launcher != "auto" && !slices.Contains(det.Launchers, w.Launcher) {
		launcherOpts = append(launcherOpts, huh.NewOption(w.Launcher, w.Launcher))
	}
	var customName, customCommand string
	fields := []huh.Field{
		huh.NewInput().Title("Where should workspaces live?").Value(&w.Root).Validate(nonEmpty),
		huh.NewSelect[string]().Title("How many agents per workspace?").Options(huh.NewOptions("1", "2", "3", "4", "6", "8")...).Value(&agents),
	}
	if len(profileOpts) > 0 {
		fields = append(fields, huh.NewMultiSelect[string]().Title("Which agents?").Options(profileOpts...).Value(&chosen))
	}
	fields = append(fields,
		huh.NewInput().Title("Another agent command?").
			Description("Typed into an interactive shell, so a shell function or alias such as a claude wrapper works. Blank for none.").
			Value(&customCommand),
		huh.NewInput().Title("Its name").Description("How fleets and `ws open --agent` refer to it.").Value(&customName),
		huh.NewSelect[string]().Title("What opens the agents' terminals?").Options(launcherOpts...).Value(&w.Launcher),
	)
	if err := huh.NewForm(huh.NewGroup(fields...)).Run(); err != nil {
		return err
	}

	w.Profiles = slices.DeleteFunc(known, func(p config.AgentProfile) bool { return !slices.Contains(chosen, p.Name) })
	if cmd := strings.TrimSpace(customCommand); cmd != "" {
		name := strings.TrimSpace(customName)
		if name == "" {
			name = strings.Fields(cmd)[0]
		}
		w.Profiles = append(slices.DeleteFunc(w.Profiles, func(p config.AgentProfile) bool { return p.Name == name }),
			config.AgentProfile{Name: name, Command: cmd})
	}
	if len(w.Profiles) == 0 {
		return errors.New("workspaces need an agent: pick one, or give a command")
	}
	w.Agents, _ = strconv.Atoi(agents)
	if !slices.ContainsFunc(w.Profiles, func(p config.AgentProfile) bool { return p.Name == w.Agent }) {
		w.Agent = ""
	}
	if len(w.Profiles) > 1 {
		if w.Agent == "" {
			w.Agent = w.Profiles[0].Name
		}
		var opts []huh.Option[string]
		for _, p := range w.Profiles {
			opts = append(opts, huh.NewOption(p.Name, p.Name))
		}
		if err := huh.NewSelect[string]().Title("Which agent runs unless a workspace says otherwise?").Options(opts...).Value(&w.Agent).Run(); err != nil {
			return err
		}
	}
	a.Workspaces = w
	return nil
}

func askAdopt(det setup.Detected, a *setup.Answers, tracked map[string]bool, home string) error {
	candidates := setup.AdoptCandidates(det, *a, tracked, home)
	if len(candidates) == 0 {
		return nil
	}
	var opts []huh.Option[string]
	for _, c := range candidates {
		opts = append(opts, huh.NewOption(fmt.Sprintf("%s  (%s)", tildify(c.Path), c.Key), c.Path).Selected(setup.UnderBaseDir(c, *a, home)))
	}
	a.Adopt = nil
	return huh.NewForm(huh.NewGroup(huh.NewMultiSelect[string]().
		Title("Track these existing clones where they are?").
		Description("They aren't where your layout puts them, or no account tracks their owner; the ones under your base dir are preselected. Tracking fetches and backs them up; nothing moves.").
		Options(opts...).Filterable(true).Value(&a.Adopt))).Run()
}

func identityKey(id config.GitIdentity) string {
	if id.Name == "" && id.Email == "" {
		return ""
	}
	return id.Name + "\x00" + id.Email
}

func nonEmpty(s string) error {
	if strings.TrimSpace(s) == "" {
		return errors.New("required")
	}
	return nil
}
