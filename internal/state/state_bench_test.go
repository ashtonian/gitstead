package state

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// scaleState writes a state with n repos shaped like real ones.
func scaleState(b *testing.B, n int) string {
	path := filepath.Join(b.TempDir(), "state.yaml")
	s, _ := Load(path)
	now := time.Now()
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("github.com/owner%d/repo-name-%d", i%50, i)
		s.Repos[key] = &RepoState{
			Host: "github.com", Owner: fmt.Sprintf("owner%d", i%50), Name: fmt.Sprintf("repo-name-%d", i),
			FullName: fmt.Sprintf("owner%d/repo-name-%d", i%50, i), ProviderID: fmt.Sprint(100000 + i),
			CloneURL: "git@github.com:" + key + ".git", LocalPath: "/Users/me/projects/" + key, CopyID: "0123456789abcdef",
			Cloned: true, DiscoveredBy: "work", Tags: []string{"work", "go"},
			ManagedConfig: map[string]string{"core.sshcommand": "ssh -i '/Users/me/.ssh/id_work' -o IdentitiesOnly=yes", "user.email": "me@acme.com"},
			Status:        "clean", CurrentBranch: "main", LastFetch: now, BackupStatus: BackupStatus{LastBackup: now},
			OffsiteStatus: OffsiteStatus{OffsiteRemote: "origin", OffsiteNamespace: "refs/gitten/mac-1a2b3c/0123456789abcdef", OffsiteWIP: []string{"20261012T181037.050Z"}, LastOffsite: now},
		}
	}
	if err := s.Save(); err != nil {
		b.Fatal(err)
	}
	return path
}

// BenchmarkUpdate measures one locked state update -- re-read, change,
// write -- as the number of tracked repos grows.
func BenchmarkUpdate(b *testing.B) {
	for _, n := range []int{100, 1000, 5000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			path := scaleState(b, n)
			s, _ := Load(path)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := s.Update(time.Second, func(st *State) error { st.LastSync = time.Now(); return nil }); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
