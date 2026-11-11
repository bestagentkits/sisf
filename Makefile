.PHONY: help build test lint vet fmt fmt-check generate vet-tags check e2e smoke up down

# A bare `make` builds, as it always has; `make help` lists every target.
.DEFAULT_GOAL := build

ARGS ?=

help: ## List every target with one line each
	@awk -F ':.*## ' '/^[a-zA-Z0-9_-]+:.*## / { printf "  %-10s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

build: ## Build every package without CGO, as CI does
	CGO_ENABLED=0 go build ./...

test: ## Run every untagged test with the race detector (needs CGO and a C compiler)
	CGO_ENABLED=1 go test ./... -race

lint: ## Run golangci-lint with .golangci.yml (includes the gofmt check)
	golangci-lint run

vet: ## Run go vet on every untagged package
	go vet ./...

fmt: ## Rewrite every Go file with gofmt
	gofmt -w .

fmt-check: ## Fail when a Go file is not gofmt-formatted, listing it
	@files="$$(gofmt -l .)"; if [ -n "$$files" ]; then echo "not gofmt-formatted:"; echo "$$files"; exit 1; fi

generate: ## Regenerate api/openapi.yaml and api/llms.txt from the action specs
	go generate ./...

vet-tags: ## Compile and vet the e2e and smoke suites without running them
	go vet -tags e2e ./tests/e2e/...
	go vet -tags smoke ./tests/smoke/...

# check runs locally what CI runs, without Docker and without running the e2e
# or the paid smoke suites: the build, gofmt, the tagged suites compiled and
# vetted, lint, and the race-enabled tests (which include the docs tests and
# the test that fails when the generated API documents are stale).
check: build fmt-check vet-tags lint test ## Run what CI runs, locally and without Docker

# e2e runs the suite twice: first with no SISF_HOME, where the Standalone
# tests bring up factories of their own and the Compose tests skip; then
# against a Docker factory of the simulation profile in a temporary SISF_HOME,
# which `sisf down -v` takes down and removes whatever the outcome.
E2E_REPO ?= bestagentkits/sisf-sandbox

e2e: ## Run the e2e suite in Standalone, then against a Docker factory (needs Docker)
	env -u SISF_HOME go test -tags e2e ./tests/e2e/... -count=1 -timeout 30m
	@tmp="$$(mktemp -d "$${TMPDIR:-/tmp}/sisf-e2e.XXXXXX")" &&export SISF_HOME="$$tmp/factory" && bin="$$tmp/sisf" && \
	trap '"$$bin" down -v; rm -rf "$${tmp:?}"' EXIT && \
	go build -o "$$bin" ./cmd/sisf && \
	"$$bin" init --repo $(E2E_REPO) --profile sim --mode docker --confirm-unlimited --no-alert && \
	"$$bin" up --mode docker --profile sim && \
	"$$bin" doctor && \
	go test -tags e2e ./tests/e2e/... -count=1 -timeout 30m

# smoke is the one real run against bestagentkits/sisf-sandbox: it needs
# SISF_SMOKE_REPO=bestagentkits/sisf-sandbox, gh, claude and codex logged in,
# a running Docker daemon on a local socket (candidate builds and the codex
# QA agent run in containers the run provides), and TMPDIR outside $$HOME.
# It runs the real release defaults, so it is slow. The build sandbox alone,
# which spends nothing, is checked by SISF_SMOKE_SANDBOX_CHECK=1 with
# -run TestBuildSandboxSelfCheck. SISF_SMOKE_UP_ONLY=1 runs the install
# alone, which spends nothing: preflight, build, sandbox, init, up, doctor
# and the operator's sign-in, then teardown; no policy is approved, no issue
# opened and no agent given a task.
smoke: ## Run the real, paid smoke run (needs gh, claude, codex and Docker; see above)
	go test -tags smoke ./tests/smoke/... -count=1 -v -timeout 150m

up: ## Start the installed factory: go run ./cmd/sisf up ARGS
	go run ./cmd/sisf up $(ARGS)

down: ## Stop the installed factory: go run ./cmd/sisf down ARGS
	go run ./cmd/sisf down $(ARGS)
