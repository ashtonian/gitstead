package pathtemplate

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"text/template"
)

// RepoData holds template fields for path resolution.
type RepoData struct {
	Host     string // "github.com", "ghes.company.com"
	Owner    string // "ashtonian"
	Name     string // "git-mgr"
	FullName string // "ashtonian/git-mgr"
}

// Resolve applies the path template to repo data and returns the relative path.
func Resolve(tmpl string, data RepoData) (string, error) {
	t, err := template.New("path").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("invalid path template: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("executing path template: %w", err)
	}
	result := filepath.Clean(buf.String())
	if filepath.IsAbs(result) || strings.Contains(result, "..") {
		return "", fmt.Errorf("path template produced unsafe path: %s", result)
	}
	return result, nil
}
