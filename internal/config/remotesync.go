package config

import (
	"fmt"
	"strings"
	"text/template"

	"github.com/ashtonian/git-mgr/internal/repourl"
)

// OffsiteNamespace renders a remote_sync namespace for one copy. The result
// is a hidden ref prefix: under refs/, outside branches, tags and
// remote-tracking refs.
func OffsiteNamespace(tmpl, machine, copyID string) (string, error) {
	ns, err := render("namespace", tmpl, struct{ Machine, Copy string }{machine, copyID})
	if err != nil {
		return "", err
	}
	ns = strings.TrimSuffix(ns, "/")
	for _, visible := range []string{"refs/heads/", "refs/tags/", "refs/remotes/"} {
		if strings.HasPrefix(ns+"/", visible) {
			return "", fmt.Errorf("remote_sync.namespace %q is under %s; WIP refs must stay hidden", tmpl, visible)
		}
	}
	if !strings.HasPrefix(ns, "refs/") || strings.ContainsAny(ns, " ~^:?*[\\") || strings.Contains(ns, "..") {
		return "", fmt.Errorf("remote_sync.namespace %q renders %q, which is not a valid ref prefix under refs/", tmpl, ns)
	}
	return ns, nil
}

// OffsiteRemote resolves remote_sync.remote for a repo: a remote name as it
// is, or a URL template filled in from the repo's identity.
func OffsiteRemote(tmpl string, id repourl.Identity) (string, error) {
	if !IsRemoteURL(tmpl) {
		return tmpl, nil
	}
	return render("remote", tmpl, id)
}

// IsRemoteURL reports whether remote_sync.remote is a URL rather than the
// name of a remote.
func IsRemoteURL(remote string) bool {
	return strings.Contains(remote, ":") || strings.Contains(remote, "{{")
}

func render(what, tmpl string, data any) (string, error) {
	t, err := template.New(what).Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("remote_sync.%s %q: %w", what, tmpl, err)
	}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", fmt.Errorf("remote_sync.%s %q: %w", what, tmpl, err)
	}
	return b.String(), nil
}

// validateRemoteSync checks remote_sync templates wherever they are set.
func validateRemoteSync(cfg *Config) error {
	check := func(where string, namespace, remote *string) error {
		if namespace != nil {
			a, err := OffsiteNamespace(*namespace, "laptop", "0000000000000001")
			if err != nil {
				return fmt.Errorf("%s: %w", where, err)
			}
			if b, _ := OffsiteNamespace(*namespace, "laptop", "0000000000000002"); a == b {
				return fmt.Errorf("%s: remote_sync.namespace %q must include {{.Copy}}, or copies overwrite each other's WIP", where, *namespace)
			}
		}
		if remote != nil {
			if _, err := OffsiteRemote(*remote, repourl.Identity{Host: "h", Owner: "o", Name: "n", FullName: "o/n"}); err != nil {
				return fmt.Errorf("%s: %w", where, err)
			}
		}
		return nil
	}
	if err := check("remote_sync", &cfg.RemoteSync.Namespace, &cfg.RemoteSync.Remote); err != nil {
		return err
	}
	for _, a := range cfg.Accounts {
		if o := a.RemoteSync; o != nil {
			if err := check(fmt.Sprintf("account %q", a.Name), o.Namespace, o.Remote); err != nil {
				return err
			}
		}
	}
	for _, o := range cfg.Overrides {
		if r := o.RemoteSync; r != nil {
			if err := check(fmt.Sprintf("override %v", o.Repos), r.Namespace, r.Remote); err != nil {
				return err
			}
		}
	}
	for _, w := range cfg.Workspaces {
		if r := w.RemoteSync; r != nil {
			if err := check(fmt.Sprintf("workspace %q", w.Name), r.Namespace, r.Remote); err != nil {
				return err
			}
		}
	}
	return nil
}
