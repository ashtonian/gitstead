package cmd

import (
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
	doc := gitconfigJSON{File: filepath.Join(config.ConfigDir(), "accounts.gitconfig"), Accounts: []accountJSON{}}

	claims := map[string][]string{}
	for i := range cfg.Accounts {
		a := describeAccount(cfg, &cfg.Accounts[i])
		switch {
		case len(a.Config) == 0:
			a.Skipped = "it sets nothing: no identity, signing or SSH key"
		case len(a.Folders) == 0:
			a.Skipped = "its repos have no folder of their own (under layout name, every repo is directly in base_dir)"
		default:
			a.File = filepath.Join(dir, pathutil.FileName(a.Name)+".gitconfig")
			for _, f := range a.Folders {
				claims[f] = append(claims[f], a.Name)
			}
		}
		doc.Accounts = append(doc.Accounts, a)
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
	var text strings.Builder
	text.WriteString("# Written by git-mgr accounts gitconfig: each account's git config for repos\n# under its folders. Change accounts in git-mgr's config and run it again.\n")
	for _, inc := range includes {
		fmt.Fprintf(&text, "[includeIf \"gitdir:%s/\"]\n\tpath = %s\n", inc.folder, inc.file)
	}
	doc.Text = text.String()
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
		if a.File != "" {
			if err := writeGitConfigFile(ctx, a.File, a.Config); err != nil {
				return fmt.Errorf("account %s: %w", a.Name, err)
			}
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

// writeGitConfigFile replaces the git config file at path with settings,
// written by git, so its quoting is git's.
func writeGitConfigFile(ctx *Context, path string, settings map[string]string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	for _, k := range slices.Sorted(maps.Keys(settings)) {
		if _, err := gitops.Git(ctx.GoCtx(), filepath.Dir(path), "config", "--file", tmp.Name(), k, settings[k]); err != nil {
			return err
		}
	}
	data, err := os.ReadFile(tmp.Name())
	if err != nil {
		return err
	}
	return pathutil.WriteFileAtomic(path, data, 0o600)
}
