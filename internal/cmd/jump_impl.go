package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"charm.land/huh/v2"
	"golang.org/x/term"

	"github.com/ashtonian/git-mgr/internal/config"
	"github.com/ashtonian/git-mgr/internal/jump"
	"github.com/ashtonian/git-mgr/internal/repourl"
	"github.com/ashtonian/git-mgr/internal/shellutil"
)

// targetFlags pick a place git-mgr knows from a short query.
type targetFlags struct {
	Query []string `arg:"" help:"Part of a repo, workspace or agent name: app, acme/api, app 2." predictor:"target"`
	Kind  string   `help:"Only consider this kind: repo, workspace, agent or copy." enum:",repo,workspace,agent,copy" default:""`
}

// resolve finds the one target the query means. When several fit equally
// well it asks which, on a terminal, else lists them.
func (f targetFlags) resolve(ctx *Context) (jump.Target, error) {
	st, err := ctx.LoadState()
	if err != nil {
		return jump.Target{}, err
	}
	query := strings.Join(f.Query, " ")
	best := jump.Best(jump.Find(jump.OfKind(jump.Targets(st), f.Kind), query))
	switch {
	case len(best) == 0:
		return jump.Target{}, fmt.Errorf("nothing matches %q; `git-mgr status` and `git-mgr ws ls` list what git-mgr knows", query)
	case len(best) == 1:
		return best[0].Target, nil
	}
	if ctx.JSON || !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stderr.Fd())) {
		var labels []string
		for _, m := range best {
			labels = append(labels, m.Label)
		}
		return jump.Target{}, fmt.Errorf("%q matches %s; give more of the name", query, strings.Join(labels, ", "))
	}
	var opts []huh.Option[int]
	for i, m := range best {
		opts = append(opts, huh.NewOption(fmt.Sprintf("%s  %s", m.Label, tildify(m.Path)), i))
	}
	chosen := 0
	// The picker draws on stderr: stdout carries the answer, often into $(...).
	err = huh.NewForm(huh.NewGroup(huh.NewSelect[int]().Title("Which one?").Options(opts...).Value(&chosen))).
		WithOutput(os.Stderr).Run()
	if err != nil {
		return jump.Target{}, err
	}
	return best[chosen].Target, nil
}

// PathCmd prints the path of a place git-mgr knows.
type PathCmd struct {
	targetFlags
}

func (c *PathCmd) Run(ctx *Context) error {
	t, err := c.resolve(ctx)
	if err != nil {
		return err
	}
	if ctx.JSON {
		return ctx.Emit(targetJSON(t))
	}
	fmt.Println(t.Path)
	return nil
}

// targetJSON is a place git-mgr knows, as path --json prints it.
func targetJSON(t jump.Target) map[string]string {
	return map[string]string{"label": t.Label, "kind": t.Kind, "path": t.Path, "repo": t.Repo}
}

// CdCmd changes the shell's directory to a place git-mgr knows. A program
// can't change its shell's directory, so the function shell-init prints does
// it; without that function this prints the path, and says how to set it up.
type CdCmd struct {
	targetFlags
}

func (c *CdCmd) Run(ctx *Context) error {
	t, err := c.resolve(ctx)
	if err != nil {
		return err
	}
	if ctx.JSON {
		return ctx.Emit(targetJSON(t))
	}
	fmt.Println(t.Path)
	if term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Fprintf(os.Stderr, "git-mgr can't change your shell's directory itself; add `eval \"$(git-mgr shell-init %s)\"` to your shell's rc file and `git-mgr cd` will.\n", shellName())
	}
	return nil
}

// OpenCmd opens a place git-mgr knows in your editor, or its repo's page in
// a browser.
type OpenCmd struct {
	targetFlags
	Web bool `help:"Open the repo's page on its host in a browser instead."`
}

func (c *OpenCmd) Run(ctx *Context) error {
	if ctx.JSON {
		return errNoJSON("open, which hands the folder to your editor or browser,")
	}
	t, err := c.resolve(ctx)
	if err != nil {
		return err
	}
	if c.Web {
		return c.openWeb(ctx, t)
	}
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	editor := editorFor(cfg)
	if editor == "" {
		return errors.New("no editor: set editor in config, or $VISUAL or $EDITOR")
	}
	return runEditor(editor, t.Path, t.Path)
}

// editorFor is the editor to open things in: config's editor, else $VISUAL,
// else $EDITOR; "" when none is set. cfg may be nil.
func editorFor(cfg *config.Config) string {
	editor := ""
	if cfg != nil {
		editor = cfg.Editor
	}
	for _, v := range []string{"VISUAL", "EDITOR"} {
		if editor == "" {
			editor = os.Getenv(v)
		}
	}
	return editor
}

// runEditor runs editor on path, from dir, on this terminal. The editor is a
// shell command, so "code -n" works; the path goes in as an argument of its
// own, never read by the shell.
func runEditor(editor, path, dir string) error {
	cmd := exec.Command("/bin/sh", "-c", editor+` "$1"`, "git-mgr-edit", path)
	cmd.Dir = dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", editor, err)
	}
	return nil
}

func (c *OpenCmd) openWeb(ctx *Context, t jump.Target) error {
	st, err := ctx.LoadState()
	if err != nil {
		return err
	}
	repo := st.Repos[t.Repo]
	if repo == nil || repo.Host == "" || repo.FullName == "" {
		return fmt.Errorf("%s isn't one tracked repo, so it has no page to open", t.Label)
	}
	url := "https://" + repo.Host + "/" + repo.FullName
	if repourl.HasCredentials(url) {
		return fmt.Errorf("%s's URL can't be opened", t.Label)
	}
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	if err := exec.Command(opener, url).Run(); err != nil {
		return fmt.Errorf("%s %s: %w", opener, url, err)
	}
	return nil
}

// ShellInitCmd prints the shell integration: a git-mgr function whose cd
// changes the shell's directory, and which hands everything else to the
// program, and tab completion of git-mgr's commands, flags and arguments.
type ShellInitCmd struct {
	Shell string `arg:"" help:"Your shell: bash, zsh or fish." enum:"bash,zsh,fish"`
}

func (c *ShellInitCmd) Run(ctx *Context) error {
	if ctx.JSON {
		return errNoJSON("shell-init, which prints shell code,")
	}
	// Completion runs this binary, by its path, with the command line in
	// COMP_LINE (see kongplete.Complete in main).
	bin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding the git-mgr binary for completion: %w", err)
	}
	bin = shellutil.Quote(bin)
	switch c.Shell {
	case "fish":
		fmt.Printf(`# git-mgr shell integration: git-mgr cd <query> changes directory.
function git-mgr
    if test (count $argv) -gt 0; and test $argv[1] = cd
        set -l dir (command git-mgr path $argv[2..-1]); and cd $dir
    else
        command git-mgr $argv
    end
end
# Tab completion.
function __complete_git_mgr
    set -lx COMP_LINE (commandline -cp)
    test -z (commandline -ct)
    and set COMP_LINE "$COMP_LINE "
    %s
end
complete -f -c git-mgr -a "(__complete_git_mgr)"
`, bin)
	default:
		compinit := ""
		if c.Shell == "zsh" {
			// zsh runs bash completion through bashcompinit, which needs
			// the completion system (compinit) loaded first.
			compinit = "(( $+functions[compdef] )) || { autoload -U +X compinit && compinit }\nautoload -U +X bashcompinit && bashcompinit\n"
		}
		fmt.Printf(`# git-mgr shell integration: git-mgr cd <query> changes directory.
git-mgr() {
  if [ "$1" = cd ]; then
    shift
    local dir
    dir=$(command git-mgr path "$@") && [ -n "$dir" ] && cd -- "$dir"
  else
    command git-mgr "$@"
  fi
}
# Tab completion.
%scomplete -o default -C %s git-mgr
`, compinit, bin)
	}
	return nil
}

// shellName is the user's shell's name, for instructions.
func shellName() string {
	sh := os.Getenv("SHELL")
	if i := strings.LastIndex(sh, "/"); i >= 0 {
		sh = sh[i+1:]
	}
	switch sh {
	case "bash", "zsh", "fish":
		return sh
	}
	return "zsh"
}
