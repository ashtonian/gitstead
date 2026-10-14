package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ashtonian/gitstead/internal/config"
)

func TestDetectShellRC(t *testing.T) {
	home := "/home/me"
	tests := []struct {
		name, shell, goos string
		env               map[string]string
		want              ShellRC
		ok                bool
	}{
		{name: "zsh", shell: "/bin/zsh", goos: "darwin", want: ShellRC{"zsh", "/home/me/.zshrc"}, ok: true},
		{name: "zsh with ZDOTDIR", shell: "/usr/bin/zsh", goos: "linux", env: map[string]string{"ZDOTDIR": "/home/me/.config/zsh"},
			want: ShellRC{"zsh", "/home/me/.config/zsh/.zshrc"}, ok: true},
		{name: "bash on Linux", shell: "/bin/bash", goos: "linux", want: ShellRC{"bash", "/home/me/.bashrc"}, ok: true},
		{name: "bash on macOS", shell: "/bin/bash", goos: "darwin", want: ShellRC{"bash", "/home/me/.bash_profile"}, ok: true},
		{name: "fish", shell: "/opt/homebrew/bin/fish", goos: "darwin", want: ShellRC{"fish", "/home/me/.config/fish/config.fish"}, ok: true},
		{name: "another shell", shell: "/bin/tcsh", goos: "linux"},
		{name: "no shell", goos: "linux"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(k string) string {
				if k == "SHELL" {
					return tt.shell
				}
				return tt.env[k]
			}
			got, ok := DetectShellRC(home, tt.goos, getenv)
			if ok != tt.ok || got != tt.want {
				t.Errorf("DetectShellRC = %+v, %v; want %+v, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

// Setup's line goes in once: appended, then replaced in place, with the rest
// of the file kept.
func TestShellRC_Install(t *testing.T) {
	rc := ShellRC{Shell: "zsh", Path: filepath.Join(t.TempDir(), ".zshrc")}
	if integrated, err := rc.Integrated(); err != nil || integrated {
		t.Fatalf("a missing file: Integrated = %v, %v", integrated, err)
	}
	if err := os.WriteFile(rc.Path, []byte("export EDITOR=vim"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := rc.Install(); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(rc.Path)
	text := string(data)
	if !strings.HasPrefix(text, "export EDITOR=vim\n\n") || strings.Count(text, rc.Line()) != 1 || strings.Count(text, shellBlockStart) != 1 {
		t.Errorf("startup file:\n%s", text)
	}
	if info, _ := os.Stat(rc.Path); info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want the file's own 0600", info.Mode().Perm())
	}
	if integrated, _ := rc.Integrated(); !integrated {
		t.Error("Integrated = false after Install")
	}

	// A line of setup's from before is replaced where it is.
	stale := shellBlockStart + "\nold line\n" + shellBlockEnd + "\n"
	if err := os.WriteFile(rc.Path, []byte("a\n"+stale+"b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rc.Install(); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(rc.Path)
	if want := "a\n" + shellBlockStart + "\n" + rc.Line() + "\n" + shellBlockEnd + "\nb\n"; string(data) != want {
		t.Errorf("startup file:\n%s\nwant\n%s", data, want)
	}
}

// A startup file that links into a dotfiles repository is changed there, and
// stays a link.
func TestShellRC_InstallFollowsALink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles", "zshrc")
	write(t, target, "# mine\n")
	link := filepath.Join(dir, ".zshrc")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := (ShellRC{Shell: "zsh", Path: link}).Install(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is no longer a link: %v", link, err)
	}
	if data, _ := os.ReadFile(target); !strings.Contains(string(data), "gitstead shell-init zsh") {
		t.Errorf("the link's target:\n%s", data)
	}
}

// A missing startup file is made, in a directory made for it.
func TestShellRC_InstallMakesTheFile(t *testing.T) {
	rc := ShellRC{Shell: "fish", Path: filepath.Join(t.TempDir(), "fish", "config.fish")}
	if err := rc.Install(); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(rc.Path); !strings.Contains(string(data), "gitstead shell-init fish | source") {
		t.Errorf("config.fish:\n%s", data)
	}
}

// What setup does beyond the config starts at what the machine has: the
// shell integration unless your shell has it, git includes for several
// accounts of their own, cloning when there are accounts, and the background
// sync as installed.
func TestDefaults_BeyondTheConfig(t *testing.T) {
	rc := &ShellRC{Shell: "zsh", Path: "/home/me/.zshrc"}
	two := []GHAccount{{Host: "github.com", Login: "me"}, {Host: "github.com", Login: "work"}}
	key := []SSHKey{{Path: "/home/me/.ssh/id_work", Logins: map[string]string{"github.com": "work"}}}
	existing := &config.Config{BaseDir: "/home/me/repos", Layout: "host/owner/name"}
	tests := []struct {
		name                             string
		det                              Detected
		existing                         *config.Config
		shell, includes, clone, schedule bool
		interval                         string
	}{
		{name: "a fresh machine", det: Detected{ShellRC: rc, GH: two[:1]}, shell: true, clone: true, schedule: true, interval: "15m"},
		{name: "shell integrated already", det: Detected{ShellRC: rc, ShellIntegrated: true}, schedule: true, interval: "15m"},
		{name: "a shell gitstead doesn't know", det: Detected{}, schedule: true, interval: "15m"},
		{name: "two accounts, one with a key of its own", det: Detected{GH: two, SSHKeys: key}, includes: true, clone: true, schedule: true, interval: "15m"},
		{name: "two accounts with nothing of their own", det: Detected{GH: two}, clone: true, schedule: true, interval: "15m"},
		{name: "included already", det: Detected{GitIncludes: true, GH: two[:1]}, includes: true, clone: true, schedule: true, interval: "15m"},
		{name: "re-run, no schedule", det: Detected{}, existing: existing, interval: "15m"},
		{name: "re-run, schedule installed", det: Detected{Schedule: &InstalledSchedule{Interval: time.Hour}}, existing: existing, schedule: true, interval: "1h"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := Defaults(tt.det, tt.existing, "/home/me")
			if a.Shell != tt.shell || a.GitIncludes != tt.includes || a.Clone != tt.clone || a.Schedule != tt.schedule || a.ScheduleInterval != tt.interval {
				t.Errorf("shell %v, git includes %v, clone %v, schedule %v every %s", a.Shell, a.GitIncludes, a.Clone, a.Schedule, a.ScheduleInterval)
			}
		})
	}
}

// A gh login whose token comes from the environment reads it from there:
// `gh auth token --user` knows only the logins gh stores.
func TestDefaults_EnvTokenLogin(t *testing.T) {
	det := Detected{GH: []GHAccount{
		{Host: "github.com", Login: "me", TokenSource: "GITHUB_TOKEN"},
		{Host: "github.com", Login: "work", TokenSource: "keyring"},
	}}
	accts := Defaults(det, nil, "/home/me").Accounts
	if accts[0].TokenEnv != "GITHUB_TOKEN" || accts[0].TokenCommand != "" {
		t.Errorf("env login: %+v", accts[0])
	}
	if accts[1].TokenEnv != "" || accts[1].TokenCommand != "gh auth token --hostname github.com --user work" {
		t.Errorf("stored login: %+v", accts[1])
	}
}

// A token in an environment variable the background sync doesn't get is
// warned about; one it gets isn't.
func TestScheduleChecks(t *testing.T) {
	cfg := &config.Config{Providers: []config.Provider{
		{Name: "me", Type: "github", TokenEnv: "GITHUB_TOKEN"},
		{Name: "lab", Type: "gitlab", TokenCommand: "glab auth token"},
	}}
	checks := ScheduleChecks(cfg, func(string) string { return "" })
	if len(checks) != 1 || checks[0].Status != Warn || !strings.Contains(checks[0].Message, "$GITHUB_TOKEN") || !strings.Contains(checks[0].Message, "gh auth login") {
		t.Errorf("checks = %+v", checks)
	}
}

func TestValidate_ScheduleInterval(t *testing.T) {
	for interval, ok := range map[string]bool{"": true, "30m": true, "2h": true, "7m": false, "soon": false} {
		a := sampleAnswers()
		a.ScheduleInterval = interval
		if err := a.Validate(); (err == nil) != ok {
			t.Errorf("schedule_interval %q: Validate = %v", interval, err)
		}
	}
}
