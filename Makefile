SHELL := /usr/bin/env bash
.DEFAULT_GOAL := build

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
PKG     := github.com/ryanhelms/agentflow/internal/version
LDFLAGS := -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT) -X $(PKG).Date=$(DATE)

SHELL_SCRIPTS := install.sh $(wildcard bin/*.sh) $(wildcard scripts/*.sh) .githooks/commit-msg .githooks/pre-push .githooks/claude-pre-bash \
	templates/repo/.agentflow/bin/agentflow \
	templates/repo/.githooks/commit-msg templates/repo/.githooks/pre-commit templates/repo/.githooks/pre-push \
	adapters/claude-code/bin/launch adapters/claude-code/bin/agentflow

.PHONY: agent-docs e2e build test vet lint shellcheck actionlint check snapshot install-hooks clean

build: ## Build ./agentflow for this machine
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o agentflow ./cmd/agentflow

test: ## Run unit tests with the race detector
	go test -race -count=1 ./...

vet:
	go vet ./...

lint: ## golangci-lint (v2)
	golangci-lint run ./...

shellcheck:
	shellcheck -x $(SHELL_SCRIPTS)

actionlint:
	scripts/lint-workflows.sh

agent-docs: ## CLAUDE.md files must only contain @AGENTS.md
	scripts/check-agent-docs.sh

check: vet test lint shellcheck actionlint agent-docs ## Everything CI runs locally
	bash scripts/test-install-sh.sh
	bash scripts/test-workflow-trust.sh
	@if command -v pwsh >/dev/null; then pwsh -NoProfile -File scripts/test-install-ps1.ps1; fi

snapshot: ## Local multi-platform build, no publish
	goreleaser release --snapshot --clean

e2e: ## Drive the Claude Code plugin end to end with claude -p (uses your Claude account)
	scripts/e2e-claude.sh

install-hooks: build ## Check this repo's commits with the agentflow built from this checkout
	git config core.hooksPath .githooks

clean:
	rm -rf dist agentflow agentflow.exe coverage.out
