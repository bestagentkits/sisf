.PHONY: build test lint e2e smoke up down

ARGS ?=

build:
	CGO_ENABLED=0 go build ./...

test:
	CGO_ENABLED=1 go test ./... -race

lint:
	golangci-lint run

# e2e runs the suite twice: first with no SISF_HOME, where the Standalone
# tests bring up factories of their own and the Compose tests skip; then
# against a Docker factory of the simulation profile in a temporary SISF_HOME,
# which `sisf down -v` takes down and removes whatever the outcome.
E2E_REPO ?= bestagentkits/sisf-sandbox

e2e:
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
smoke:
	go test -tags smoke ./tests/smoke/... -count=1 -v -timeout 150m

up:
	go run ./cmd/sisf up $(ARGS)

down:
	go run ./cmd/sisf down $(ARGS)
