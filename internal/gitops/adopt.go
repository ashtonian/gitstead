package gitops

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// RepoIdentity represents parsed identity from a git remote URL.
type RepoIdentity struct {
	Host     string // "github.com"
	Owner    string // "ashtonian"
	Name     string // "git-mgr"
	FullName string // "ashtonian/git-mgr"
	SSHURL   string // "git@github.com:ashtonian/git-mgr.git"
}

// ParseRemoteURL parses a repo's origin remote URL into identity components.
func ParseRemoteURL(ctx context.Context, repoPath string) (*RepoIdentity, error) {
	remoteURL, err := Git(ctx, repoPath, "remote", "get-url", "origin")
	if err != nil {
		return nil, fmt.Errorf("getting origin URL: %w", err)
	}
	return ParseURL(remoteURL)
}

// ParseURL parses a git URL (SSH or HTTPS) into identity components.
func ParseURL(rawURL string) (*RepoIdentity, error) {
	rawURL = strings.TrimSpace(rawURL)

	// SSH format: git@github.com:owner/repo.git
	if strings.HasPrefix(rawURL, "git@") {
		return parseSSHURL(rawURL)
	}

	// HTTPS format: https://github.com/owner/repo.git
	if strings.HasPrefix(rawURL, "https://") || strings.HasPrefix(rawURL, "http://") {
		return parseHTTPSURL(rawURL)
	}

	return nil, fmt.Errorf("unsupported URL format: %s", rawURL)
}

func parseSSHURL(sshURL string) (*RepoIdentity, error) {
	// git@github.com:owner/repo.git
	parts := strings.SplitN(sshURL, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid SSH URL: %s", sshURL)
	}

	host := strings.TrimPrefix(parts[0], "git@")
	path := strings.TrimSuffix(parts[1], ".git")

	pathParts := strings.SplitN(path, "/", 2)
	if len(pathParts) != 2 {
		return nil, fmt.Errorf("invalid SSH URL path: %s", path)
	}

	return &RepoIdentity{
		Host:     host,
		Owner:    pathParts[0],
		Name:     pathParts[1],
		FullName: path,
		SSHURL:   sshURL,
	}, nil
}

func parseHTTPSURL(httpsURL string) (*RepoIdentity, error) {
	u, err := url.Parse(httpsURL)
	if err != nil {
		return nil, fmt.Errorf("parsing HTTPS URL: %w", err)
	}

	path := strings.TrimPrefix(u.Path, "/")
	path = strings.TrimSuffix(path, ".git")

	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid HTTPS URL path: %s", path)
	}

	return &RepoIdentity{
		Host:     u.Host,
		Owner:    parts[0],
		Name:     parts[1],
		FullName: path,
		SSHURL:   fmt.Sprintf("git@%s:%s.git", u.Host, path),
	}, nil
}
