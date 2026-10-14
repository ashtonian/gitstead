package config

import (
	"fmt"
	"strings"
	"time"
)

// HooksConfig holds the commands gitten runs, each with /bin/sh -c, in a
// checkout it has just made: post_clone in a repo's new clone, post_create
// in a new workspace copy of it. They come only from config, never from a
// repository.
type HooksConfig struct {
	PostClone  []string `koanf:"post_clone"  yaml:"post_clone,omitempty"`
	PostCreate []string `koanf:"post_create" yaml:"post_create,omitempty"`
	// Timeout bounds each command; 0 means none.
	Timeout time.Duration `koanf:"timeout" yaml:"timeout" default:"15m" validate:"min=0"`
}

// HooksOverride sets hooks for an account or repos. A nil list is unset; an
// empty one runs nothing.
type HooksOverride struct {
	PostClone  *[]string `koanf:"post_clone"  yaml:"post_clone,omitempty"`
	PostCreate *[]string `koanf:"post_create" yaml:"post_create,omitempty"`
}

// RepoHooks are the hooks a repo resolves to.
type RepoHooks struct {
	PostClone  []string `yaml:"post_clone"`
	PostCreate []string `yaml:"post_create"`
}

func (o *HooksOverride) apply(h *RepoHooks) {
	if o == nil {
		return
	}
	if o.PostClone != nil {
		h.PostClone = *o.PostClone
	}
	if o.PostCreate != nil {
		h.PostCreate = *o.PostCreate
	}
}

// HooksFor resolves a repo's hooks: the top-level ones, then its account's,
// then any override's, each replacing a list it sets.
func (c *Config) HooksFor(repoKey, ruleName string) RepoHooks {
	h := RepoHooks{PostClone: c.Hooks.PostClone, PostCreate: c.Hooks.PostCreate}
	if a := c.AccountFor(repoKey, ruleName); a != nil {
		a.Hooks.apply(&h)
	}
	for _, o := range c.Overrides {
		if matchesOverride(repoKey, o.Repos) {
			o.Hooks.apply(&h)
		}
	}
	return h
}

// WorkspaceHooks are a workspace's own hooks: its post_create commands run in
// each new copy after the copy's repo's. A nil list is unset.
type WorkspaceHooks struct {
	PostCreate *[]string `koanf:"post_create" yaml:"post_create,omitempty"`
}

// PostCreate is the workspace's own post_create commands. w must have its
// defaults.
func (w Workspace) PostCreate() []string {
	if w.Hooks.PostCreate == nil {
		return nil
	}
	return *w.Hooks.PostCreate
}

// validateHooks rejects empty commands wherever hooks are set.
func validateHooks(cfg *Config) error {
	check := func(where, event string, commands []string) error {
		for i, c := range commands {
			if strings.TrimSpace(c) == "" {
				return fmt.Errorf("%s: hooks.%s[%d] is empty", where, event, i)
			}
		}
		return nil
	}
	checkOverride := func(where string, o *HooksOverride) error {
		if o == nil {
			return nil
		}
		if o.PostClone != nil {
			if err := check(where, "post_clone", *o.PostClone); err != nil {
				return err
			}
		}
		if o.PostCreate != nil {
			return check(where, "post_create", *o.PostCreate)
		}
		return nil
	}
	if err := check("config", "post_clone", cfg.Hooks.PostClone); err != nil {
		return err
	}
	if err := check("config", "post_create", cfg.Hooks.PostCreate); err != nil {
		return err
	}
	for _, a := range cfg.Accounts {
		if err := checkOverride(fmt.Sprintf("account %q", a.Name), a.Hooks); err != nil {
			return err
		}
	}
	for _, o := range cfg.Overrides {
		if err := checkOverride(fmt.Sprintf("override %v", o.Repos), o.Hooks); err != nil {
			return err
		}
	}
	if p := cfg.WorkspaceDefaults.Hooks.PostCreate; p != nil {
		if err := check("workspace_defaults", "post_create", *p); err != nil {
			return err
		}
	}
	for _, w := range cfg.Workspaces {
		if p := w.Hooks.PostCreate; p != nil {
			if err := check(fmt.Sprintf("workspace %q", w.Name), "post_create", *p); err != nil {
				return err
			}
		}
	}
	return nil
}
