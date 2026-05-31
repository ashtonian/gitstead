// Package config loads, validates, and resolves git-mgr's layered configuration:
// struct-tag defaults, then the YAML config file, then GIT_MGR_* environment
// variables. It also applies per-repo overrides and SSH-key resolution.
package config
