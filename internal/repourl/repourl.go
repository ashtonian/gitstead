// Package repourl parses git remote URLs into the host/owner/name identity
// git-mgr keys repositories by. It is pure: no git, no filesystem.
package repourl

import (
	"fmt"
	"net/url"
	"strings"
)

// Identity represents parsed identity from a git remote URL.
type Identity struct {
	Host     string // "github.com"
	Owner    string // "ashtonian"
	Name     string // "git-mgr"
	FullName string // "ashtonian/git-mgr"
	SSHURL   string // canonical "git@github.com:ashtonian/git-mgr.git"
	URL      string // the original input URL, verbatim (preserves scheme/port)
}

// Key returns the tracking key, host/owner/name.
func (id *Identity) Key() string { return id.Host + "/" + id.FullName }

// Parse parses a git remote URL into identity components. It accepts every
// form git itself does:
//   - scp-like SSH:   git@host:owner/repo.git  (or any user@)
//   - ssh:// URLs:    ssh://git@host[:port]/owner/repo.git
//   - http(s):        https://host[:port]/owner/repo.git  (userinfo/port stripped)
//   - git://:         git://host/owner/repo.git
//   - local paths:    /path/to/repo[.git], file://…, ~/repos/x.git
//
// Owner may be a multi-segment path (GitLab subgroups). For local remotes,
// which have no real host/owner, Host is "local", Owner is the parent dir, and
// Name is the repo dir — enough to form a stable tracking key.
func Parse(rawURL string) (*Identity, error) {
	rawURL = strings.TrimSpace(rawURL)
	id, err := parseURLInner(rawURL)
	if err != nil {
		return nil, err
	}
	id.URL = rawURL // preserve exactly what was given (scheme, port, host form)
	return id, nil
}

func parseURLInner(rawURL string) (*Identity, error) {
	if rawURL == "" {
		return nil, fmt.Errorf("empty remote URL")
	}

	switch {
	case strings.HasPrefix(rawURL, "ssh://"),
		strings.HasPrefix(rawURL, "http://"),
		strings.HasPrefix(rawURL, "https://"),
		strings.HasPrefix(rawURL, "git://"):
		return parseSchemeURL(rawURL)
	case strings.HasPrefix(rawURL, "file://"):
		return parseLocalPath(strings.TrimPrefix(rawURL, "file://"), rawURL)
	}

	// An unsupported scheme (ftp://, etc.) is an error, not a local path.
	if i := strings.Index(rawURL, "://"); i >= 0 {
		return nil, fmt.Errorf("unsupported URL scheme: %s", rawURL[:i])
	}

	// scp-like SSH (user@host:path) — has a colon before any slash and no scheme.
	if i := strings.Index(rawURL, ":"); i >= 0 && !strings.Contains(rawURL[:i], "/") && strings.Contains(rawURL, "@") {
		return parseSCPURL(rawURL)
	}

	// Otherwise treat it as a local filesystem path — but only if it actually
	// looks like one (absolute, ~-relative, or ./..); a bare token is rejected so
	// typos don't silently become a "local/<token>" repo.
	if strings.HasPrefix(rawURL, "/") || strings.HasPrefix(rawURL, "~") ||
		strings.HasPrefix(rawURL, "./") || strings.HasPrefix(rawURL, "../") {
		return parseLocalPath(rawURL, rawURL)
	}
	return nil, fmt.Errorf("unsupported URL format: %s", rawURL)
}

// parseSchemeURL handles ssh://, http(s)://, and git:// URLs.
func parseSchemeURL(rawURL string) (*Identity, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parsing URL %q: %w", rawURL, err)
	}
	host := u.Hostname() // drops any :port and userinfo
	if host == "" {
		return nil, fmt.Errorf("URL %q has no host", rawURL)
	}
	owner, name, err := splitOwnerRepo(u.Path)
	if err != nil {
		return nil, fmt.Errorf("URL %q: %w", rawURL, err)
	}
	return identity(host, owner, name), nil
}

// parseSCPURL handles scp-like git@host:owner/repo.git.
func parseSCPURL(rawURL string) (*Identity, error) {
	at := strings.Index(rawURL, "@")
	rest := rawURL[at+1:] // host:path
	colon := strings.Index(rest, ":")
	host := rest[:colon]
	owner, name, err := splitOwnerRepo(rest[colon+1:])
	if err != nil {
		return nil, fmt.Errorf("SSH URL %q: %w", rawURL, err)
	}
	return identity(host, owner, name), nil
}

// parseLocalPath derives an identity from a filesystem remote. There is no real
// host/owner, so Host is "local" and Owner is the immediate parent directory.
func parseLocalPath(path, _ string) (*Identity, error) {
	clean := strings.TrimRight(strings.TrimSuffix(strings.TrimSpace(path), ".git"), "/")
	if clean == "" {
		return nil, fmt.Errorf("empty local repo path")
	}
	name := clean[strings.LastIndex(clean, "/")+1:]
	if name == "" {
		return nil, fmt.Errorf("cannot derive repo name from %q", path)
	}
	owner := "local"
	if i := strings.LastIndex(clean, "/"); i > 0 {
		parent := clean[:i]
		owner = parent[strings.LastIndex(parent, "/")+1:]
	}
	id := identity("local", owner, name)
	id.SSHURL = path // keep the original path as the clone source for local remotes
	return id, nil
}

// splitOwnerRepo splits a URL path into a (possibly multi-segment) owner and the
// final repo name, trimming a leading slash and a trailing .git.
func splitOwnerRepo(p string) (owner, name string, err error) {
	p = strings.Trim(p, "/")
	p = strings.TrimSuffix(p, ".git")
	idx := strings.LastIndex(p, "/")
	if idx < 0 {
		return "", "", fmt.Errorf("path %q has no owner/repo", p)
	}
	owner, name = p[:idx], p[idx+1:]
	if owner == "" || name == "" {
		return "", "", fmt.Errorf("path %q has no owner/repo", p)
	}
	return owner, name, nil
}

// identity assembles an Identity, defaulting SSHURL to the canonical
// git@host:owner/repo.git form.
func identity(host, owner, name string) *Identity {
	full := owner + "/" + name
	return &Identity{
		Host:     host,
		Owner:    owner,
		Name:     name,
		FullName: full,
		SSHURL:   fmt.Sprintf("git@%s:%s.git", host, full),
	}
}

// IsHTTP reports whether u is an http(s) URL, whose auth goes through git's
// credential helper rather than an SSH key.
func IsHTTP(u string) bool {
	return strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://")
}
