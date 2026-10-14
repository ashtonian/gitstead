package reconcile_test

import (
	"reflect"
	"testing"

	"github.com/ashtonian/gitstead/internal/config"
	"github.com/ashtonian/gitstead/internal/reconcile"
)

// An account's folders follow the layout and any base_dir its repos get;
// a layout that puts every repo straight in base_dir leaves it none.
func TestAccountFolders(t *testing.T) {
	work := config.Account{Name: "work", Hosts: []string{"github.com"}, Owners: []string{"acme", "acme-labs"}}
	host := config.Account{Name: "ghe", Hosts: []string{"ghe.acme.com"}}
	tests := []struct {
		name     string
		cfg      config.Config
		account  config.Account
		expected []string
	}{
		{"host/owner/name", config.Config{BaseDir: "/r", PathTemplate: "{{.Host}}/{{.Owner}}/{{.Name}}"}, work,
			[]string{"/r/github.com/acme", "/r/github.com/acme-labs"}},
		{"owner/name", config.Config{BaseDir: "/r", PathTemplate: "{{.Owner}}/{{.Name}}"}, work,
			[]string{"/r/acme", "/r/acme-labs"}},
		{"name", config.Config{BaseDir: "/r", PathTemplate: "{{.Name}}"}, work, nil},
		{"a whole host", config.Config{BaseDir: "/r", PathTemplate: "{{.Host}}/{{.Owner}}/{{.Name}}"}, host,
			[]string{"/r/ghe.acme.com"}},
		{"a base_dir of its own", config.Config{BaseDir: "/r", PathTemplate: "{{.Owner}}/{{.Name}}",
			Overrides: []config.Override{{Repos: []string{"github.com/acme/*"}, BaseDir: "/work"}}}, work,
			[]string{"/r/acme-labs", "/work/acme"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reconcile.AccountFolders(&tt.cfg, &tt.account); !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("AccountFolders = %v, want %v", got, tt.expected)
			}
		})
	}
}
