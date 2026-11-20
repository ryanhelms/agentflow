// Package adapters embeds per-client adapter files used by `devflow install`.
package adapters

import "embed"

// FS holds every adapter directory, including dotfiles.
//
//go:embed all:agents-md all:claude-code all:codex all:cursor all:gemini all:grok all:vscode
var FS embed.FS
