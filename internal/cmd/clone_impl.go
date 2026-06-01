package cmd

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/ashtonian/git-mgr/internal/gitops"
	"github.com/ashtonian/git-mgr/internal/pathtemplate"
	"github.com/ashtonian/git-mgr/internal/state"
)

func (c *CloneCmd) Run(ctx *Context) error {
	cfg, err := ctx.LoadConfig()
	if err != nil {
		return err
	}
	st, unlock, err := lockedState(ctx)
	if err != nil {
		return err
	}
	defer unlock()

	identity, err := gitops.ParseURL(c.URL)
	if err != nil {
		return fmt.Errorf("parsing URL: %w", err)
	}

	key := identity.Host + "/" + identity.FullName

	if _, exists := st.Repos[key]; exists {
		return fmt.Errorf("repo %s is already tracked", key)
	}

	// Determine clone path, honoring any per-repo override (base_dir/path_template).
	repoCfg := cfg.ResolveForRepo(key)
	var destPath string
	if c.Path != "" {
		destPath = c.Path
	} else {
		relPath, err := pathtemplate.Resolve(repoCfg.PathTemplate, pathtemplate.RepoData{
			Host:     identity.Host,
			Owner:    identity.Owner,
			Name:     identity.Name,
			FullName: identity.FullName,
		})
		if err != nil {
			return fmt.Errorf("resolving path: %w", err)
		}
		destPath = filepath.Join(repoCfg.BaseDir, relPath)
	}

	// Clone exactly the URL the user gave (preserving scheme/port), and use an SSH
	// key only for SSH transports -- https auth flows through git's credential
	// helper.
	cloneURL, cloneKey := c.URL, ""
	if !isHTTPURL(c.URL) {
		cloneKey = cfg.ResolveSSHKey(key, "")
	}

	fmt.Printf("Cloning %s ...\n", key)
	if err := gitops.Clone(ctx.GoCtx(), cloneURL, destPath, cloneKey); err != nil {
		return err
	}

	var tags []string
	if c.Tag != "" {
		tags = strings.Split(c.Tag, ",")
	}

	now := time.Now()
	st.Repos[key] = &state.RepoState{
		CloneURL:       cloneURL,
		Host:           identity.Host,
		Owner:          identity.Owner,
		Name:           identity.Name,
		FullName:       identity.FullName,
		Provider:       identity.Host,
		LocalPath:      destPath,
		Cloned:         true,
		Tags:           tags,
		Status:         "clean",
		ManuallyAdded:  true,
		FirstSeen:      now,
		LastDiscovered: now,
	}

	if err := st.Save(); err != nil {
		return fmt.Errorf("saving state: %w", err)
	}
	logEvent(ctx, "clone", key, cloneURL)

	fmt.Printf("Cloned to %s\n", destPath)
	if len(tags) > 0 {
		fmt.Printf("Tags: %v\n", tags)
	}
	return nil
}
