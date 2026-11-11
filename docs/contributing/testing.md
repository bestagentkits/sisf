# Testing

How the tests are layered, which of them a pull request needs, and how to run each. The rules behind the layers are in [AGENTS.md](../../AGENTS.md): tests first, a negative test for every security or spending rule, fakes instead of sleeps, and slow suites behind build tags.

## Layers

| Layer | Where | Build tag | Needs | Run with | Required for a PR |
|---|---|---|---|---|---|
| Unit | `*_test.go` beside the code, in every package under `cmd/`, `internal/`, `pkg/`, `adapters/`, `ui/`, `examples/` | none | Go | `go test ./<dir>/...` | yes, in `make test` |
| Acceptance | `tests/acceptance/` | none | Go | `go test ./tests/acceptance/... -count=1` | yes, in `make test` |
| Docs | `tests/docs/` | none | Go | `go test ./tests/docs/... -count=1` | yes, in `make test` |
| Threat-model coverage | `tests/security/` | none | Go | `go test ./tests/security/... -count=1` | yes, in `make test` |
| End to end | `tests/e2e/` | `e2e` | Docker, ports 7460 to 7469 free | `make e2e` | compiled and vetted only (`make vet-tags`) |
| Smoke | `tests/smoke/` | `smoke` | a real GitHub sandbox repository, `gh`, `claude` and `codex` signed in, Docker; it spends money | `make smoke` | compiled and vetted only (`make vet-tags`) |

What each layer proves:

- **Unit** tests check one package against fakes. The shared fakes are in `internal/testkit/` (`fakegithub`, `fakeoidc` and the in-process `harness`).
- **Acceptance** tests drive the wired factory in process, end to end across packages, against those fakes. Many of the SISF-01 to SISF-20 scenarios of brief §13 are proven here; the [acceptance matrix](../acceptance-matrix.md) names the file for each.
- **Docs** tests keep the documents honest: the English and Vietnamese briefs share section numbers, table IDs and SISF IDs (`brief_parity_test.go`); the ADR set is complete, numbered without gaps and carries the required sections (`TestADRSetIsComplete`); and the transition table in `pkg/core/statemachine` matches the state diagram of brief §5.2 (`lifecycle_diagram_test.go`).
- **Threat-model coverage** fails when a high-severity scenario of [docs/threat-model.md](../threat-model.md) names a test that does not exist.
- **End to end** brings real factories up: first in Standalone mode on 127.0.0.1, ports 7460 to 7469, then a Docker Compose factory of the simulation profile. See below.
- **Smoke** is the one real run against a sandbox repository with real coding agents. It is not something a contributor runs for a PR.

## What a pull request needs

CI ([.github/workflows/ci.yml](../../.github/workflows/ci.yml)) runs three jobs on every push and pull request, and all must pass:

1. `go build ./...` with `CGO_ENABLED=0`, then `go vet -tags e2e ./tests/e2e/...` and `go vet -tags smoke ./tests/smoke/...`.
2. `go test ./... -race` with `CGO_ENABLED=1`: every untagged layer above.
3. `golangci-lint` v2.12.2 with [.golangci.yml](../../.golangci.yml): `govet`, `staticcheck`, `errcheck`, `ineffassign`, `unused` and the `gofmt` formatter.

`make check` runs the same locally, plus a plain `gofmt -l` that lists any unformatted file, and needs no Docker, network service or account.

The generated API documents are checked by a test, not a separate step: `TestGeneratedDocumentsAreFresh` in `internal/actions/apispec` fails when `api/openapi.yaml` or `api/llms.txt` differ from what `make generate` writes.

## Running a subset

```bash
go test ./internal/conformance/... -count=1                     # one package tree
go test ./pkg/core/statemachine -run TestLifecycleTableHoldsExactlyTheDiagramEdges -count=1 -v
GOFLAGS=-p=2 make test                                           # everything, at lower load
```

## The end-to-end suite

`make e2e` runs `go test -tags e2e ./tests/e2e/...` twice:

1. With no `SISF_HOME`. The Standalone tests build `sisf` and bring up factories of their own on 127.0.0.1; they fail at once, naming the busy ports, if anything already holds a port between 7460 and 7469. The Compose tests skip.
2. Against a Docker factory. The target builds `sisf` into a temporary directory, runs `sisf init --repo bestagentkits/sisf-sandbox --profile sim --mode docker --confirm-unlimited --no-alert`, `sisf up --mode docker --profile sim` and `sisf doctor`, then runs the suite again with `SISF_HOME` pointing at that factory. The simulation profile runs against a fake GitHub and a fake identity provider, so no GitHub account or token is used. A trap runs `sisf down -v` and removes the directory whatever the outcome.

It needs a running Docker daemon reachable through a local socket, Go, and the ports 7460 to 7469 free; stop any other factory with `sisf down` first. The repository can be changed with `make e2e E2E_REPO=owner/name`. The Docker filter test skips when `DOCKER_HOST` is not a unix socket or no daemon answers.

## The smoke run

`make smoke` is the real run against `bestagentkits/sisf-sandbox`. Its requirements and the cheaper modes that spend nothing (`SISF_SMOKE_SANDBOX_CHECK=1`, `SISF_SMOKE_UP_ONLY=1`) are described above the `smoke` target in the [Makefile](../../Makefile). It needs credentials and spends money, so CI never runs it.
