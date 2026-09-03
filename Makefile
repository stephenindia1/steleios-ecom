# Steleios — developer tasks.
#
# The gates here run in the same order as CI, cheapest first, so a failure is
# found locally before it is found by a pull request (CLAUDE.md rule 46).

SHELL := /bin/sh
MODULE := github.com/stephenindia1/steleios-ecom

# Version metadata is stamped into the binary and reported at startup (HLT-004).
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
REVISION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS  := -s -w \
	-X '$(MODULE)/internal/platform/config.buildVersion=$(VERSION)' \
	-X '$(MODULE)/internal/platform/config.buildRevision=$(REVISION)'

GO_BUILD := CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)"

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

# ---------------------------------------------------------------------------
# Build and run
# ---------------------------------------------------------------------------

.PHONY: build
build: ## Build all binaries into ./bin
	$(GO_BUILD) -o bin/api     ./cmd/api
	$(GO_BUILD) -o bin/worker  ./cmd/worker
	$(GO_BUILD) -o bin/migrate ./cmd/migrate

.PHONY: run
run: ## Run the API against local infrastructure
	go run ./cmd/api

.PHONY: worker
worker: ## Run the queue worker
	go run ./cmd/worker

.PHONY: up
up: ## Start local PostgreSQL and Redis
	docker compose up -d --wait

.PHONY: down
down: ## Stop local infrastructure, keeping data
	docker compose down

.PHONY: nuke
nuke: ## Stop local infrastructure and DELETE its data
	docker compose down -v

.PHONY: tools
tools: ## Start the queue inspector on :8081
	docker compose --profile tools up -d --wait asynqmon

# ---------------------------------------------------------------------------
# Database
# ---------------------------------------------------------------------------

.PHONY: migrate
migrate: ## Apply migrations
	go run ./cmd/migrate -command up

.PHONY: migrate-status
migrate-status: ## Show migration status
	go run ./cmd/migrate -command status

.PHONY: migrate-down
migrate-down: ## Roll back one migration (local and test only)
	go run ./cmd/migrate -command down

# ---------------------------------------------------------------------------
# CI gates, cheapest first (CLAUDE.md rule 46)
# ---------------------------------------------------------------------------

.PHONY: fmt
fmt: ## Format Go source
	gofmt -w ./cmd ./internal ./migrations

.PHONY: gate-1-lint
gate-1-lint: ## Gate 1 — formatting and lint
	@test -z "$$(gofmt -l ./cmd ./internal ./migrations)" || \
		{ echo "gofmt found unformatted files:"; gofmt -l ./cmd ./internal ./migrations; exit 1; }
	golangci-lint run

.PHONY: gate-2-types
gate-2-types: ## Gate 2 — type checking
	go vet ./...

.PHONY: gate-3-test
gate-3-test: ## Gate 3 — tests with the race detector
	go test -race -count=1 ./...

.PHONY: gate-4-security
gate-4-security: ## Gate 4 — vulnerability and secret scanning
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

.PHONY: ci
ci: gate-1-lint gate-2-types gate-3-test gate-4-security ## Run every gate in order

.PHONY: test
test: ## Run tests without the race detector (faster inner loop)
	go test ./...

.PHONY: cover
cover: ## Report coverage for the money, security and domain paths
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -n 30

.PHONY: tidy
tidy: ## Tidy and verify module dependencies
	go mod tidy
	go mod verify
