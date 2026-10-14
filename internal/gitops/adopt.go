package gitops

import (
	"context"
	"fmt"

	"github.com/ashtonian/gitten/internal/repourl"
)

// ParseRemoteURL parses a repo's origin remote URL into identity components.
func ParseRemoteURL(ctx context.Context, repoPath string) (*repourl.Identity, error) {
	remoteURL, err := Git(ctx, repoPath, "remote", "get-url", "origin")
	if err != nil {
		return nil, fmt.Errorf("getting origin URL: %w", err)
	}
	return repourl.Parse(remoteURL)
}
