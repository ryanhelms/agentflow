// Package templates embeds the files `agentflow init` writes into a consuming repo.
package templates

import "embed"

// Repo holds templates/repo, including dotfiles.
//
//go:embed all:repo
var Repo embed.FS
