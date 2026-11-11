# Contributing

SISF is Apache-2.0 licensed, copyright AgentKit.best. Contribution rules are proposed in [ADR 0016](docs/adr/0016-oss-governance.md); the DCO requirement below applies now.

Everyone taking part follows the [code of conduct](.github/CODE_OF_CONDUCT.md).

## Before you start

- Read the [decision brief](docs/self-improvement-software-factory-decision-brief.en.md) sections for your area and the [ADRs](docs/adr/). [AGENTS.md](AGENTS.md) holds the development rules for people and agents alike.
- Do not reverse a product invariant. Propose a change through an ADR.
- Do not add code taken from another project without a provenance and license audit.
- A new dependency needs a recorded decision; the approved modules are in [ADR 0020](docs/adr/0020-approved-dependencies.md).
- For anything larger than a fix, open an issue first so the scope can be agreed. A new feature needs Owner approval before it is built ([FEATURES.md](FEATURES.md)).

## Set up

You need:

| Tool | Version | Used by |
|---|---|---|
| Go | 1.25.14 or newer 1.25.x ([ADR 0019](docs/adr/0019-go-1-25-toolchain.md)); with `GOTOOLCHAIN=auto`, an older `go` fetches it | everything |
| A C compiler (Xcode Command Line Tools on macOS, `gcc` on Linux) | any | `make test`: the race detector needs CGO. The binaries themselves build without CGO. |
| GNU Make | 3.81 or newer | the `make` targets |
| git | any | cloning, the stale-generated-code check |
| [golangci-lint](https://golangci-lint.run/) | v2.12.2, the version CI pins | `make lint`, `make check` |
| Docker | a running daemon on a local socket | `make e2e` only |

Nothing else is needed for a pull request: no environment variables, no account and no running service.

```bash
git clone https://github.com/bestagentkits/sisf.git
cd sisf
make help     # every target, one line each
make check    # what CI runs, without Docker
```

## Everyday commands

| Task | Command |
|---|---|
| Build every package (no CGO, as shipped) | `make build` |
| Run all tests that CI runs, with the race detector | `make test` |
| Lint, including the gofmt check | `make lint` |
| Check formatting only / fix it | `make fmt-check` / `make fmt` |
| Vet | `make vet` |
| Compile and vet the `e2e` and `smoke` suites without running them | `make vet-tags` |
| Regenerate `api/openapi.yaml` and `api/llms.txt` | `make generate` |
| Everything CI checks, in one go | `make check` |
| One package | `go test ./internal/conformance/... -count=1` |
| One test | `go test ./pkg/core/statemachine -run TestLifecycleTableHoldsExactlyTheDiagramEdges -count=1` |
| The acceptance tests | `go test ./tests/acceptance/... -count=1` |
| The docs and threat-model tests | `go test ./tests/docs/... ./tests/security/... -count=1` |

`-count=1` skips Go's test cache. On a machine shared with other work, `GOFLAGS=-p=2 make check` keeps the load down.

The API documents in `api/` are generated from the action specs. If you change an action, run `make generate` and commit the result; `TestGeneratedDocumentsAreFresh` in `internal/actions/apispec` fails while they are stale.

[docs/contributing/testing.md](docs/contributing/testing.md) explains the test layers, which of them a pull request needs, and how to run the Docker-based `e2e` suite. [docs/contributing/project-layout.md](docs/contributing/project-layout.md) maps the directories to the decision brief, and [docs/contributing/common-changes.md](docs/contributing/common-changes.md) walks through adding an ADR, an action, a state transition, a conformance rule or a test for one of the SISF-01 to SISF-20 scenarios.

## Sign your commits (DCO)

Every commit needs a `Signed-off-by` line certifying the [Developer Certificate of Origin](https://developercertificate.org/). Add it with:

```bash
git commit -s
```

The line must match the commit author, for example `Signed-off-by: Your Name <you@example.com>`. There is no CLA.

## Commits

- Conventional commits: `type(scope): summary`, for example `fix(supervisor): ...`, `test(qa): ...`, `docs: ...`, `ci: ...`, `build: ...`. The scope is usually the package or area.
- One concern per commit. Say what the change does, not how it was produced; no AI references.
- No plan or phase identifiers in code, tests or commit messages.

## Pull requests

- Branch from `main`; never push to `main`. Open a PR; the Owner merges.
- A change that builds on another open PR may be based on that PR's branch; say so in the description.
- Write the test first, with negative tests for security and spending rules.
- Interfaces under `pkg/` are experimental until 1.0; make changes additive.
- If you change one language of the decision brief, change the other in the same PR. A test checks that section numbers and IDs match.
- No secrets, tokens or personal data.
- Fill in the [pull request template](.github/PULL_REQUEST_TEMPLATE.md). CI (build, the tagged suites vetted, race-enabled tests, lint) must be green.

## Review

Reviews follow [REVIEW.md](REVIEW.md): its hard gates override any score, and every claim in docs needs code or a test behind it. A change to dependencies, lockfiles, base images or CI configuration is a hard gate of its own. A change to the supervisor, signing keys, policy enforcement, the QA baseline or rollback rules is a governance change, not a normal PR; so is a change to `AGENTS.md`, `REVIEW.md`, `DECISIONS.md`, `FEATURES.md`, an ADR or `.sisf/`.

## Security

Report vulnerabilities privately; see [docs/SECURITY.md](docs/SECURITY.md). Do not open a public issue for a security problem.
