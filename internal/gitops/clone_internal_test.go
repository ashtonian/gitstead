package gitops

import (
	"os"
	"path/filepath"
	"testing"
)

// Whatever appears at the destination while a clone is made -- a file, a
// link, another run's clone -- is kept, and the clone isn't published; an
// empty directory is only a placeholder and gives way.
func TestStaged_KeepsWhatAppearsAtTheDestination(t *testing.T) {
	tests := []struct {
		name    string
		appear  func(t *testing.T, dest string)
		publish bool
	}{
		{"nothing", func(t *testing.T, dest string) {}, true},
		{"an empty directory", func(t *testing.T, dest string) { mkdir(t, dest) }, true},
		{"a file", func(t *testing.T, dest string) { write(t, dest, "valuable") }, false},
		{"a symlink", func(t *testing.T, dest string) {
			if err := os.Symlink(t.TempDir(), dest); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"another clone", func(t *testing.T, dest string) {
			mkdir(t, filepath.Join(dest, ".git"))
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "repo")
			err := staged(dest, func(staging string) error {
				write(t, filepath.Join(staging, "cloned"), "clone")
				tt.appear(t, dest)
				return nil
			})
			if published := err == nil; published != tt.publish {
				t.Fatalf("staged = %v, want published %v", err, tt.publish)
			}
			_, clonedErr := os.Stat(filepath.Join(dest, "cloned"))
			if (clonedErr == nil) != tt.publish {
				t.Errorf("clone at destination = %v, want %v", clonedErr == nil, tt.publish)
			}
			if tt.name == "a file" {
				if got, _ := os.ReadFile(dest); string(got) != "valuable" {
					t.Errorf("the file that appeared now holds %q", got)
				}
			}
			entries, _ := os.ReadDir(filepath.Dir(dest))
			if len(entries) != 1 {
				t.Errorf("staging left behind in %s: %v", filepath.Dir(dest), entries)
			}
		})
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
