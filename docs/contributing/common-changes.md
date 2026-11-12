# Common changes

How to make the changes contributors make most often, each pointing at a real example in the tree. Write the test first; [testing.md](testing.md) says where each kind of test lives.

## Add an ADR

ADRs live in [docs/adr/](../adr/), one file per decision, named `NNNN-kebab-title.md` with numbers contiguous from `0001`. Copy the shape of an accepted one, such as [ADR 0019](../adr/0019-go-1-25-toolchain.md): the sections `## Status`, `## Context`, `## Decision`, `## Consequences` and `## Acceptance test` are all required.

`TestADRSetIsComplete` in [tests/docs/brief_parity_test.go](../../tests/docs/brief_parity_test.go) checks the set. A new ADR therefore also changes that test in the same PR: the expected file count, and the `laterProposed` set if the new ADR is `Status: proposed` rather than `Status: accepted`. Run:

```bash
go test ./tests/docs/... -count=1
```

An ADR that changes a product invariant of the decision brief is a governance change; the Owner decides it.

## Add an action

Every surface (dashboard, REST, CLI, MCP, WebMCP) calls the one action service in [internal/actions](../../internal/actions/doc.go), so a new capability is a new action, never a surface-specific handler.

1. Write the action in a domain package that exposes its spec and its `[]core.Action`. [internal/actions/telemetryactions/telemetry_search.go](../../internal/actions/telemetryactions/telemetry_search.go) is a small, complete example: a name, a `core.ActionSpec` with sensitivity, scopes, roles and JSON schemas for input and output, and a constructor that refuses missing dependencies. Its tests are in `telemetry_search_test.go` beside it.
2. Register it in [internal/actions/actionsets/action_sets.go](../../internal/actions/actionsets/action_sets.go), in both `Register` (the running factory) and `Specs` (the description the API documents come from). `TestTheDescribedRegistryMatchesTheRealOne` fails when the two differ.
3. Run `make generate` and commit the changed `api/openapi.yaml` and `api/llms.txt`; `TestGeneratedDocumentsAreFresh` fails while they are stale.
4. A sensitive write goes through approval and step-up; see `internal/actions/service_approval_pipeline.go` and its tests before marking an action anything but a read.

```bash
go test ./internal/actions/... -count=1
```

## Change the issue lifecycle

The states and edges of brief §5.2 are fixed by the brief, so a new state or transition is a change to the contract, not only to code. It touches, in one PR:

- the state diagram in both languages of the brief (§5.2 of [English](../self-improvement-software-factory-decision-brief.en.md) and [Vietnamese](../self-improvement-software-factory-decision-brief.md));
- the states and events in [pkg/core/states.go](../../pkg/core/states.go);
- the transition table `lifecycle` in [pkg/core/statemachine/table.go](../../pkg/core/statemachine/table.go), with a comment on the edge that says why it needs no proof, or a `GuardKind` and its guard;
- the tests that pin the table, such as `table_test.go` and `transition_matrix_test.go` in the same package.
- the zone that may take the step, in `controlEvents` or its neighbours in [pkg/state/sqlstore/zone_ownership.go](../../pkg/state/sqlstore/zone_ownership.go), and a new migration at the end of `releaseMigrations` in [pkg/state/sqlstore/migrations.go](../../pkg/state/sqlstore/migrations.go) that writes the PostgreSQL record trigger and the zones' row policies afresh, as migrations 8 and 10 do: the trigger is generated from the edge table, and an applied migration is never edited.

[tests/docs/lifecycle_diagram_test.go](../../tests/docs/lifecycle_diagram_test.go) compares the table with the brief's diagram in both directions, so changing only one of them fails. Every way into Ready, every step back and every step towards users is guarded; keep it so, and add a negative test showing the step is refused without its proof (for example `approval_guard_test.go`).

```bash
go test ./pkg/core/... ./tests/docs/... -count=1
```

## Add a conformance rule

The checker in [internal/conformance](../../internal/conformance/check.go) judges a factory manifest.

1. Name the rule in [rule_names.go](../../internal/conformance/rule_names.go): a stable `Rule` constant with a comment saying what it rejects.
2. Implement it in the `rules_<area>.go` file of its area (for example [rules_budget.go](../../internal/conformance/rules_budget.go)) and make sure `checkWith` in `check.go` calls it. A finding carries the rule, the manifest path, a message and a fix.
3. Add a fixture `testdata/reject-<what>.yaml`: a copy of `valid-docker.yaml` or `valid-standalone.yaml` with a single defect, whose second line is `# expect: <rule>`. [fixtures_test.go](../../internal/conformance/fixtures_test.go) runs every `reject-*.yaml` and checks that only that rule fires, and that the valid examples and `.sisf/factory.yaml` stay conformant.
4. Add focused tests beside the rule when it has cases a fixture cannot show, as in `qa_independence_test.go`.

```bash
go test ./internal/conformance/... -count=1
go run ./cmd/sisf conformance check .sisf/factory.yaml
```

## Add a test for a SISF scenario

The [acceptance matrix](../acceptance-matrix.md) maps each of SISF-01 to SISF-20 (brief §13) to the test files that prove it.

1. Put the test where the behavior lives: in the package that owns it for one component (SISF-09, `internal/supervisor/release/auto_rollback_restores_lkg_test.go`), or in `tests/acceptance/` when it crosses packages (SISF-15, `tests/acceptance/two_factories_same_upstream_test.go`, built on `internal/testkit/harness`).
2. Name the file and the test after the behavior, not the ID: test names read as the rule they prove, such as `TestTwoFactoriesOnOneUpstreamKeepTheirImprovementsApart`.
3. Add the file to the scenario's row in the acceptance matrix in the same PR. Do not mark a row present unless the file exists and its package passes.
4. If the test proves a control of a high-severity threat, also list it for that threat in [tests/security/threat_model_coverage_test.go](../../tests/security/threat_model_coverage_test.go).

```bash
go test ./tests/acceptance/... ./tests/security/... -count=1
```
