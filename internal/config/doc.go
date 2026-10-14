// Package config loads, validates, and resolves gitstead's layered
// configuration: struct-tag defaults, then the YAML config file, then
// GITSTEAD_* environment variables, then the flags that set a setting
// (--concurrency).
//
// Loading is github.com/ashtonian/pkg/config's: it merges the layers, fails
// on a key that is not a setting, and validates with the struct tags and then
// Config.Validate, gitstead's rules no tag can state. Before validation gitstead
// adds the shared manifest's repos and normalizes (layout, ~ expansion, the
// data directories), so the rules judge the config commands will use.
//
// The package also resolves per-repo overrides, accounts and SSH keys, and
// edits the config file in place (Document) keeping its comments.
package config
