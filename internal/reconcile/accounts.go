package reconcile

import (
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ashtonian/gitstead/internal/config"
	"github.com/ashtonian/gitstead/internal/pathtemplate"
)

// AccountGitConfig is the git config account a gives every repo it covers:
// its identity and signing settings, and the SSH key it signs in with.
func AccountGitConfig(a *config.Account) map[string]string {
	cfg := AccountConfig(a)
	if a != nil && a.SSHKey != "" {
		cfg[keySSHCommand] = SSHCommand(config.ExpandHome(a.SSHKey))
	}
	return cfg
}

// AccountFolders are the folders config places the repos of account a in:
// for each of its hosts and owners, the leading part of their repos' paths
// that doesn't depend on a repo's name -- nor, for an account covering a
// whole host, on its owner. An account whose repos' paths start with such a
// part -- under layout name, every repo is directly in base_dir -- has none
// for that host or owner.
func AccountFolders(cfg *config.Config, a *config.Account) []string {
	const anyOwner, anyName = "\x00owner", "\x00name"
	folders := map[string]bool{}
	for _, host := range a.Hosts {
		owners := a.Owners
		if len(owners) == 0 {
			owners = []string{anyOwner}
		}
		for _, owner := range owners {
			repoCfg := cfg.ResolveForRepo(host + "/" + owner + "/" + anyName)
			rel, err := pathtemplate.Resolve(repoCfg.PathTemplate, pathtemplate.RepoData{
				Host: host, Owner: owner, Name: anyName, FullName: owner + "/" + anyName,
			})
			if err != nil {
				continue
			}
			var fixed []string
			for _, part := range strings.Split(rel, string(filepath.Separator)) {
				if strings.Contains(part, "\x00") {
					break
				}
				fixed = append(fixed, part)
			}
			if len(fixed) == 0 {
				continue
			}
			folders[filepath.Join(append([]string{config.ExpandHome(repoCfg.BaseDir)}, fixed...)...)] = true
		}
	}
	return slices.Sorted(maps.Keys(folders))
}
