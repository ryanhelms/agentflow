SHELL := /usr/bin/env bash
.DEFAULT_GOAL := build

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
PKG     := github.com/nscaledev/devflow/internal/version
LDFLAGS := -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT) -X $(PKG).Date=$(DATE)

SHELL_SCRIPTS := $(wildcard bin/*.sh) $(wildcard scripts/*.sh) .githooks/commit-msg .githooks/pre-push .githooks/claude-pre-bash \
	templates/repo/.devflow/bin/devflow \
	templates/repo/.githooks/commit-msg \
	adapters/claude-code/bin/launch

.PHONY: agent-docs e2e build test vet lint shellcheck actionlint check snapshot install-hooks clean

build: ## Build ./devflow for this machine
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o devflow ./cmd/devflow

test: ## Run unit tests with the race detector
	go test -race -count=1 ./...

vet:
	go vet ./...

lint: ## golangci-lint (v2)
	golangci-lint run ./...

shellcheck:
	shellcheck -x $(SHELL_SCRIPTS)

actionlint:
	actionlint

agent-docs: ## CLAUDE.md files must only contain @AGENTS.md
	scripts/check-agent-docs.sh

check: vet test lint shellcheck actionlint agent-docs ## Everything CI runs

snapshot: ## Local multi-platform build, no publish
	goreleaser release --snapshot --clean

e2e: ## Drive the Claude Code plugin end to end with claude -p (uses your Claude account)
	scripts/e2e-claude.sh

install-hooks: build ## Check this repo's commits with the devflow built from this checkout
	git config core.hooksPath .githooks

clean:
	rm -rf dist devflow devflow.exe coverage.out
