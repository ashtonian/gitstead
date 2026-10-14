// Package config loads, validates, and resolves gitten's layered configuration:
// struct-tag defaults, then the YAML config file, then GITTEN_* environment
// variables. It also applies per-repo overrides and SSH-key resolution.
package config
