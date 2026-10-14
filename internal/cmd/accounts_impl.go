package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/pathutil"
	"github.com/ashtonian/git-mgr/internal/reconcile"
)

// AccountsCmd groups the accounts subcommands.
type AccountsCmd struct {
	List      AccountsListCmd      `cmd:"" name:"ls" help:"List accounts: what they cover, their identity, key and signing, and the folders their repos are in."`
	Gitconfig AccountsGitconfigCmd `cmd:"" help:"Make each account's identity your git's own for repos under its folders, with includeIf: print it, or --write it, or --install it."`
}

// accountJSON is one account as accounts ls and gitconfig print it.
type accountJSON struct {
	Name    string            `json:"name"`
	Hosts   []string          `json:"hosts"`
	Owners  []string          `json:"owners,omitempty"`
	SSHKey  string            `json:"ssh_key,omitempty"`
	Email   string            `json:"email,omitempty"`
	Signing string            `json:"signing"`
	Folders []string          `json:"folders,omitempty"`
	Config  map[string]string `json:"config,omitempty"` // what it sets in git config
	File    string            `json:"file,omitempty"`   // gitconfig: where it is written
	Skipped string            `json:"skipped,omitempty"`
}

func describeAccount(cfg *config.Config, a *config.Account) accountJSON {
	return accountJSON{
		Name: a.Name, Hosts: a.Hosts, Owners: a.Owners, SSHKey: a.SSHKey, Email: a.Git.Email,
		Signing: dashIfEmpty(a.Signing.Mode), Folders: reconcile.AccountFolders(cfg, a), Config: reconcile.AccountGitConfig(a),
	}
}

// AccountsListCmd lists accounts.
type AccountsListCmd struct{}

func (c *AccountsListCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	accounts := []accountJSON{}
	for i := range cfg.Accounts {
		accounts = append(accounts, describeAccount(cfg, &cfg.Accounts[i]))
	}
	if ctx.JSON {
		return ctx.Emit(accounts)
	}
	if len(accounts) == 0 {
		fmt.Println("No accounts configured; `git-mgr setup` adds them.")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ACCOUNT\tCOVERS\tEMAIL\tKEY\tSIGNING\tFOLDERS")
	for _, a := range accounts {
		covers := strings.Join(a.Hosts, ",")
		if len(a.Owners) > 0 {
			covers += ": " + strings.Join(a.Owners, ",")
		}
		folders := make([]string, len(a.Folders))
		for i, f := range a.Folders {
			folders[i] = tildify(f)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", a.Name, covers, dashIfEmpty(a.Email), dashIfEmpty(tildify(a.SSHKey)), a.Signing, dashIfEmpty(strings.Join(folders, ", ")))
	}
	return tw.Flush()
}

// AccountsGitconfigCmd generates includeIf config for accounts.
type AccountsGitconfigCmd struct {
	Write   bool `help:"Write the account files and the includeIf file to git-mgr's config directory."`
	Install bool `help:"Write them, and include the includeIf file from your global git config (git config --global --add include.path), if it isn't already."`
}

// gitconfigJSON is what accounts gitconfig prints.
type gitconfigJSON struct {
	File      string        `json:"file"` // the includeIf file
	Text      string        `json:"text"`
	Accounts  []accountJSON `json:"accounts"`
	Written   bool          `json:"written"`
	Installed bool          `json:"installed"`
}

// Run makes, for each account with something to set and folders of its own,
// a git config file of its identity, signing and SSH key, and one file of
// includeIf sections that applies each to repos under its folders. git-mgr
// writes the same settings into the repos it manages; this covers repos you
// clone yourself there. A folder two accounts share is left out: neither's
// identity would be right for it. Deeper folders come last, so the account
// for an owner wins over one for its whole host.
func (c *AccountsGitconfigCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	dir := filepath.Join(config.ConfigDir(), "accounts")
	doc := gitconfigJSON{File: accountsIncludeFile(), Accounts: []accountJSON{}}

	claims := map[string][]string{}
	for i := range cfg.Accounts {
		a := describeAccount(cfg, &cfg.Accounts[i])
		switch {
		case len(a.Config) == 0:
			a.Skipped = "it sets nothing: no identity, signing or SSH key"
		case len(a.Folders) == 0:
			a.Skipped = "its repos have no folder of their own (under layout name, every repo is directly in base_dir)"
		default:
			a.File = filepath.Join(dir, accountFileName(a.Name))
			for _, f := range a.Folders {
				claims[f] = append(claims[f], a.Name)
			}
		}
		doc.Accounts = append(doc.Accounts, a)
	}
	if err := checkDistinctFiles(doc.Accounts); err != nil {
		return err
	}
	type include struct{ folder, file string }
	var includes []include
	for _, a := range doc.Accounts {
		for _, f := range a.Folders {
			if a.File != "" && len(claims[f]) == 1 {
				includes = append(includes, include{f, a.File})
			}
		}
	}
	slices.SortStableFunc(includes, func(x, y include) int {
		return strings.Count(x.folder, string(filepath.Separator)) - strings.Count(y.folder, string(filepath.Separator))
	})
	entries := make([]gitConfigEntry, len(includes))
	for i, inc := range includes {
		entries[i] = gitConfigEntry{"includeIf.gitdir:" + gitdirPattern(inc.folder) + ".path", inc.file}
	}
	const header = "# Written by git-mgr accounts gitconfig: each account's git config for repos\n# under its folders. Change accounts in git-mgr's config and run it again.\n"
	if doc.Text, err = gitConfigText(ctx, header, entries); err != nil {
		return fmt.Errorf("making %s: %w", doc.File, err)
	}
	for _, f := range slices.Sorted(maps.Keys(claims)) {
		if len(claims[f]) > 1 {
			fmt.Fprintf(os.Stderr, "warning: %s is left out: accounts %s all put repos there\n", tildify(f), strings.Join(claims[f], ", "))
		}
	}

	if !c.Write && !c.Install {
		if ctx.JSON {
			return ctx.Emit(doc)
		}
		fmt.Printf("# %s\n%s", doc.File, doc.Text)
		for _, a := range doc.Accounts {
			if a.Skipped != "" {
				fmt.Printf("\n# %s: skipped, %s\n", a.Name, a.Skipped)
				continue
			}
			fmt.Printf("\n# %s\n", a.File)
			for _, k := range slices.Sorted(maps.Keys(a.Config)) {
				fmt.Printf("#   %s = %s\n", k, a.Config[k])
			}
		}
		fmt.Println("\n--write writes these files; --install also includes the first from your global git config.")
		return nil
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, a := range doc.Accounts {
		if a.File == "" {
			continue
		}
		var settings []gitConfigEntry
		for _, k := range slices.Sorted(maps.Keys(a.Config)) {
			settings = append(settings, gitConfigEntry{k, a.Config[k]})
		}
		text, err := gitConfigText(ctx, "", settings)
		if err == nil {
			err = pathutil.WriteFileAtomic(a.File, []byte(text), 0o600)
		}
		if err != nil {
			return fmt.Errorf("account %s: %w", a.Name, err)
		}
	}
	if err := pathutil.WriteFileAtomic(doc.File, []byte(doc.Text), 0o600); err != nil {
		return err
	}
	doc.Written = true
	fmt.Printf("Wrote %s and %d account file(s) in %s\n", tildify(doc.File), len(includes), tildify(dir))
	if c.Install {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		have, _ := gitops.Git(ctx.GoCtx(), home, "config", "--global", "--get-all", "include.path")
		if !slices.Contains(strings.Split(have, "\n"), doc.File) {
			if _, err := gitops.Git(ctx.GoCtx(), home, "config", "--global", "--add", "include.path", doc.File); err != nil {
				return ctx.finish(doc, fmt.Errorf("including %s from your global git config: %w", doc.File, err))
			}
			fmt.Printf("Included it from your global git config (include.path).\n")
		}
		doc.Installed = true
	} else {
		fmt.Printf("Include it from your global git config to use it:\n  git config --global --add include.path %s\n", doc.File)
	}
	return ctx.finish(doc, nil)
}

// accountsIncludeFile is the file of includeIf sections accounts gitconfig
// writes, which your global git config includes.
func accountsIncludeFile() string { return filepath.Join(config.ConfigDir(), "accounts.gitconfig") }

// accountFileName is the name of account name's git config file: the name
// made a file name, which can make two names one, so with a digest of the
// name itself, which keeps them apart -- also where case doesn't.
func accountFileName(name string) string {
	sum := sha256.Sum256([]byte(name))
	return pathutil.FileName(name) + "-" + hex.EncodeToString(sum[:4]) + ".gitconfig"
}

// checkDistinctFiles refuses accounts that would share a git config file,
// on a filesystem that ignores case too: one would overwrite the other.
func checkDistinctFiles(accounts []accountJSON) error {
	owners := map[string]string{}
	for _, a := range accounts {
		if a.File == "" {
			continue
		}
		key := strings.ToLower(a.File)
		if other, ok := owners[key]; ok {
			return fmt.Errorf("accounts %q and %q would share the git config file %s; rename one", other, a.Name, a.File)
		}
		owners[key] = a.Name
	}
	return nil
}

// gitdirPattern is the includeIf gitdir pattern for repos under folder: the
// folder with git's wildcard characters escaped, so it matches only itself,
// and a trailing slash, which matches everything under it.
func gitdirPattern(folder string) string {
	var b strings.Builder
	for _, r := range folder {
		if strings.ContainsRune(`\*?[`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('/')
	return b.String()
}

// gitConfigEntry is one setting in a git config file.
type gitConfigEntry struct{ key, value string }

// gitConfigText is a git config file holding header (comment lines) and then
// entries in order. git writes it, so its quoting and escaping are git's, and
// reads it back, which must give entries exactly.
func gitConfigText(ctx *Context, header string, entries []gitConfigEntry) (string, error) {
	tmp, err := os.CreateTemp("", "git-mgr-gitconfig-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.WriteString(header)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(tmp.Name())
	for _, e := range entries {
		if _, err := gitops.Git(ctx.GoCtx(), dir, "config", "--file", tmp.Name(), e.key, e.value); err != nil {
			return "", fmt.Errorf("setting %s: %w", e.key, err)
		}
	}
	listed, err := gitops.GitOutput(ctx.GoCtx(), dir, "config", "--file", tmp.Name(), "--list", "-z")
	if err != nil {
		return "", err
	}
	var got []gitConfigEntry
	for _, item := range strings.Split(strings.TrimSuffix(string(listed), "\x00"), "\x00") {
		if key, value, ok := strings.Cut(item, "\n"); ok {
			got = append(got, gitConfigEntry{key, value})
		}
	}
	want := make([]gitConfigEntry, len(entries))
	for i, e := range entries {
		want[i] = gitConfigEntry{canonicalGitConfigKey(e.key), e.value}
	}
	if !slices.Equal(got, want) {
		return "", fmt.Errorf("git reads back %q, not %q", got, want)
	}
	data, err := os.ReadFile(tmp.Name())
	return string(data), err
}

// canonicalGitConfigKey is key as git lists it: section and variable names
// lower-cased, a subsection as it is.
func canonicalGitConfigKey(key string) string {
	first, last := strings.Index(key, "."), strings.LastIndex(key, ".")
	if first < 0 {
		return strings.ToLower(key)
	}
	return strings.ToLower(key[:first]) + key[first:last] + strings.ToLower(key[last:])
}
