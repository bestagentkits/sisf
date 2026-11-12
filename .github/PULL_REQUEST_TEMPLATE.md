## What and why

<!-- What this PR changes and why. Link the issue it addresses. -->

## How it was tested

<!-- The tests you added or ran, and their result. Say which layer: unit, acceptance, docs, e2e. -->

## Checklist

- [ ] `make check` passes locally (what CI runs: build, `gofmt`, the `e2e` and `smoke` suites vetted, lint, `go test ./... -race`).
- [ ] Tests come with the change; a security or spending rule has a negative test.
- [ ] If an action changed: `make generate` was run and `api/` is committed.
- [ ] If the decision brief changed: both languages changed in this PR.
- [ ] If an ADR was added: `TestADRSetIsComplete` was updated.
- [ ] If a SISF-01 to SISF-20 scenario gained a test: the acceptance matrix row names it.
- [ ] `pkg/` changes are additive.
- [ ] No new dependency without an ADR (ADR 0020).
- [ ] Docs describe only what code and a passing test prove.
- [ ] Conventional commit messages, each signed off (`git commit -s`).
- [ ] No secrets, tokens or personal data.

## Governance

<!-- Tick only if it applies; REVIEW.md treats these as hard gates or governance changes. -->

- [ ] Changes dependencies, lockfiles, base images or CI configuration.
- [ ] Changes the supervisor, signing keys, policy enforcement, the QA baseline or rollback rules.
- [ ] Widens permissions, egress, budget or scope.
- [ ] Ran `make e2e` (needs Docker); say where it ran.
