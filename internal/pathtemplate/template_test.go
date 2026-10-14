package pathtemplate_test

import (
	"testing"

	"github.com/ashtonian/gitten/internal/pathtemplate"
)

func TestResolve(t *testing.T) {
	tests := []struct {
		tmpl string
		data pathtemplate.RepoData
		want string
	}{
		{
			"{{.Host}}/{{.Owner}}/{{.Name}}",
			pathtemplate.RepoData{Host: "github.com", Owner: "ashtonian", Name: "gitten", FullName: "ashtonian/gitten"},
			"github.com/ashtonian/gitten",
		},
		{
			"{{.Name}}",
			pathtemplate.RepoData{Host: "github.com", Owner: "your-login", Name: "dotfiles"},
			"dotfiles",
		},
		{
			"{{.Owner}}/{{.Name}}",
			pathtemplate.RepoData{Owner: "company", Name: "api"},
			"company/api",
		},
	}

	for _, tt := range tests {
		t.Run(tt.tmpl, func(t *testing.T) {
			got, err := pathtemplate.Resolve(tt.tmpl, tt.data)
			if err != nil {
				t.Fatalf("Resolve(%q) failed: %v", tt.tmpl, err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolve_UnsafePath(t *testing.T) {
	_, err := pathtemplate.Resolve("../../etc/passwd", pathtemplate.RepoData{})
	if err == nil {
		t.Error("expected error for path traversal")
	}
}

func TestResolve_InvalidTemplate(t *testing.T) {
	_, err := pathtemplate.Resolve("{{.Invalid", pathtemplate.RepoData{})
	if err == nil {
		t.Error("expected error for invalid template")
	}
}
