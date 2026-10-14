package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashtonian/gitten/internal/config"
)

func TestDocument_EditsInPlaceKeepingComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := `# my repos
base_dir: ~/code # where they live
path_template: "{{.Name}}"
accounts:
  # the work one
  - name: work
    hosts: [github.com] # only github
    ssh_key: ~/.ssh/old
  - name: other
    hosts: [gitlab.com]
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := config.OpenDocument(path)
	if err != nil {
		t.Fatal(err)
	}
	must(t, doc.Set("base_dir", "~/projects"))
	must(t, doc.Set("layout", "owner/name"))
	doc.Delete("path_template")
	must(t, doc.Set("backup.enabled", false))
	must(t, doc.Upsert("accounts", config.Named("work"), config.Account{Name: "work", Hosts: []string{"github.com"}, SSHKey: "~/.ssh/id_work"}))
	must(t, doc.Upsert("accounts", config.Named("new"), config.Account{Name: "new", Hosts: []string{"example.com"}}))
	must(t, doc.Save(t.Context()))

	data, _ := os.ReadFile(path)
	got := string(data)
	for _, want := range []string{
		"# my repos", "base_dir: ~/projects # where they live", "layout: owner/name",
		"# the work one", "hosts: [github.com] # only github", "ssh_key: ~/.ssh/id_work",
		"- name: other", "- name: new", "backup:\n    enabled: false",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "path_template") || strings.Contains(got, "~/.ssh/old") {
		t.Errorf("stale values left in:\n%s", got)
	}
	if strings.Index(got, "name: work") > strings.Index(got, "name: other") {
		t.Errorf("an upsert must replace the entry in place:\n%s", got)
	}
	cfg, err := config.Load(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PathTemplate != "{{.Owner}}/{{.Name}}" || cfg.Backup.Enabled {
		t.Errorf("loaded %q backup=%v", cfg.PathTemplate, cfg.Backup.Enabled)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
