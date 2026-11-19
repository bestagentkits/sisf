# Scout: SISF release and detection path for managed apps

## Answers

**1. What `sisf up` runs; build input; app config**
- Mode is chosen by `--mode standalone|docker` (`cmd/sisf/cmd_install_flags.go:40`). Unit set is fixed at nine (`pkg/manifest/manifest.go:56`, "All nine must be present" at `:171`).
- Standalone: `standalonePrograms` builds only `./examples/hello-app` with `CGO_ENABLED=0 go build` (`internal/install/build_standalone.go:35`). The app is started by the supervisor's local backend through `sisf-app-launcher`, not by `standaloneUnits` (`internal/install/standalone_units.go:45-89` has no app entry; `standalone_release_units.go` writes the launcher). Go single-binary only (ADR 0022).
- Docker: `dockerImages` is a hard-coded list (`internal/install/build_docker.go:19-27`). The app image is always `build/hello-app.Dockerfile`. The compose app service is a fixed template block (`templates/compose/compose.yaml:317-331`): `env_file` secrets, `HELLO_*` env, `app-data:/data`, `SISF_REVISION`.
- Port: `--deploy-app-port` defaults to 8080 (`cmd/supervisor/lifecycle_flags.go:79`), but the template passes `PortApp` 7461 (`compose.yaml:128`, `options.go:19`).
- Health: observed only via `GET /healthz` returning JSON `revision` equal to the digest label (`pkg/deploy/healthprobe/healthprobe.go:34-48`, `adapters/deploy/compose/observe.go:32-42`). `Component.HealthCheck` (`manifest.go:247`, set to `/healthz` in `manifest_build.go:87`) is not read by the runtime. It is only secret-scanned (`internal/conformance/rules_secrets_typed_fields.go:45-46`).
- `compose.Config` (`adapters/deploy/compose/config.go:47`) is built in `cmd/supervisor/deploy_backend.go:67-70` with Endpoint, Factory, Network, AppPort, Edge, Clock, Logger only. No Env, Volumes or Limits.
- Candidate in Docker mode: `Stage` pulls `artifact.Image`, which must be digest-pinned (`adapters/deploy/compose/backend.go:72-93`). The container spec is `SISF_REVISION` plus `cfg.Env` (empty), mounts from `cfg.Volumes` (empty), read-only root, tmpfs `/tmp`, 512 MiB default (`containers.go:57-75`, `config.go`). So a candidate gets no env_file and no data volume.
- Candidate build: sandbox `SandboxImages.BuildImage` runs test, build and report phases (`cmd/supervisor/stages/image_builders.go:114`). The report must give `image-digest` and `image` (`report_reader.go:18`, `:146-151`). Docker-mode build command comes from `--build-command` (`compose.yaml:136-137`).
- Arbitrary Dockerfile: the installed app cannot be an arbitrary Dockerfile image (hard-coded in `build_docker.go:19-27`). A candidate can be any digest-pinned image the build command reports, if the sandbox can build it (unverified, see Unresolved).

**2. Release path**
- Entry: `Pipeline.Release` (`internal/supervisor/release/pipeline.go:121`). Preflight (`preflight_and_start.go:50`) requires `SelfPublishAllowed` (`pkg/deploy/deploy.go:20-24`), an LKG, the LKG observed healthy, and a baseline `Measure`.
- Start (`preflight_and_start.go:106`): `Stage`, `Preview`, then `EventRolloutStarted`.
- Rollout (`rollout_and_watch.go:17-41`): canary steps (`DefaultCanarySteps` 5/25/100, `internal/supervisor/release/config.go:59`), `Promote` per step, `watch` per step, `takeAllTraffic`, final watch at 100%. Any undo goes to `p.restore`.
- Metrics (`pkg/deploy/deploy.go:118-128`): `Requests`, `ServerErrors`, `Healthy`, `CriticalErrors`. There is no latency. Compose fills them from edge counts per revision digest and critical log lines (`adapters/deploy/compose/release.go:18-50`).
- Monitor (`internal/supervisor/release/monitor.go:61-89`): unhealthy or critical rolls back at once. Breach is `rate > ErrorRateThreshold && rate-baseline > MinDelta`, rolling back after `Consecutive` breaches. Defaults at `config.go:36-40` (10 min, 100 samples, 2%, 1%, 2).
- Manifest `release.rollback` (`manifest.go:426-432`) is not read by the supervisor. It is only validated (`internal/conformance/rules_release.go:56,78`) and written (`internal/install/manifest_build.go:108`). Window and min-samples come from CLI flags (`cmd/supervisor/release_tuning_flags.go:21-35`).
- Hard gates (`cmd/supervisor/stages/hard_gates.go:51-63`): tests_contract, permissions (CI, code ownership, policy), migrations (`migrations/` and `pkg/state/sqlstore/`, `protected_paths.go:26-38`), secrets, observed_build, resource_budget, rollback_readiness, supply_chain. Independent evaluator is separate.
- Synthetic suite: a supervisor-run executable (`cmd/supervisor/synthetic_program.go:53,67`). It is invoked as `prog --url <preview> --revision <digest>` with env `PATH` only. It runs only on low-traffic holds (`release/rollout_and_watch.go:168-194`). Its contract is a process exit code. The contract in `examples/hello-app/synthetic/synthetic.go:72-100` is four HTTP checks: health revision, page visits, metrics, writes refused.
- Synthetic is wired only in Standalone (`build_standalone.go:36-37`, `standalone_release_units.go:42-43`). Docker template has no `--synthetic-program`, so low-traffic releases hold.
- Pre-release backup hook: none found in code. ADR 0012 is proposed only.
- Post-rollback restore hook: none. `deploy.Backend.Rollback` replaces code and config only, never data (`pkg/deploy/deploy.go:171-173`; compose impl `release.go:90`).

**3. Detection**
- Input: edge counters, keyed by revision digest (`internal/decision/counters.go:10-19`, `cmd/factoryd/wiring/decision_layer_adapters.go:29-41`). Only 5xx and request counts; no latency.
- Cadence: `DecisionInterval` 5 min (`cmd/factoryd/wiring/decision_layer.go:30`, `pass` at `:71`).
- Rules: `DefaultRules` (`internal/decision/rules.go:31-35`, used at `decision_layer.go:113`): MinSamples 100, 2%, delta 1%, 2 consecutive windows, baseline 3 healthy windows. Not manifest-configurable.
- Run: `Layer.Run` (`internal/decision/layer.go:111`), `decide` (`:157`).
- Enabled only when `SISF_EDGE_COUNTERS_URL` is set (`cmd/factoryd/control_surfaces_env.go:38-46`). Both installers set it (`internal/install/standalone_units.go:138`, `compose.yaml:247`).
- Automatic GitHub issue: yes, for a confirmed regression. `issueProposals.Propose` calls `CreateIssue` with title and body only, no labels (`decision_layer_adapters.go:114-116`). Deduped by fingerprint.
- Work does not start from that issue. Intake acts on `sisf:ready` (`internal/intake/doc.go`).
- Second auto-issue path: public `/report` route (`compose.yaml:350`) creates issues labelled `sisf:report` (`internal/control/report_publisher.go:417`, `internal/intake/report/report_issue.go:15`).

**4. Policy and approvals**
- No manifest knob to declare path patterns that need approval. Protected paths are hard-coded: `DefaultProtectedPaths` (`internal/supervisor/release/governance.go:38-40`) plus generated Go closure. Categories in `cmd/supervisor/stages/protected_paths.go:14-31,57-72`.
- Self-publish rule `checkGovernance` (`governance.go:100-121`): supervisor refused; every path must be an input of the one unit; none protected; else `APPROVAL_REQUIRED`. The function takes no approval argument, so no signed approval lifts a path refusal in this path. A refusal blocks the issue (ADR 0023).
- `release.unit_inputs` (`pkg/manifest/manifest.go:412-417`) is default-deny per unit. An entry replaces built-ins for that unit. Built-in app inputs are files directly in packages listed in the generated `appUnitPackageDirs`, minus Go toolchain files (`internal/supervisor/release/unit_inputs.go:149-162`). Pattern grammar: `pkg/manifest/unit_inputs.go:22-40` (no `**`, `*` does not cross `/`, case-folded).
- Owner-decided changes (new features, rules) wait in intake (`internal/intake/doc.go`).

**5. Manifest**
- `Manifest` at `pkg/manifest/manifest.go:162-195`. `Components` `:205-209`, with `Application`, `Agent`, `Control` as pointers.
- `Component` `:238-248`: `Source`, `CredentialsRef`, `StoreRole`, `HealthCheck`. `Source` and `HealthCheck` are informational.
- `Unit` `:251-269`: Image, Digest, Command, Lifecycle, Networks, Port, Public, PublicRoutes, Mounts, CredentialsRef.
- `Volume` `:281-288`. `Release` `:404-420`. `Rollback` `:426-432`. `LowTraffic` `:436-439`.
- `Extensions map[string]any` `:192-194`, schema says it grants nothing (`schemas/factory.schema.json:848-852`).
- Schema derived from Go types in `pkg/manifest/schema.go:24`, tables in `schema_rules.go` (e.g. `:146`). Published file `schemas/factory.schema.json`, regenerated via `go test ./pkg/manifest -run … -update` (`pkg/manifest/schema_test.go:20-22`).
- Validator `pkg/manifest/validate.go:59` (schema plus decision and unit-input checks). Load in `load.go:26,74`.
- Least invasive: new typed field on `Component` (schema regen, conformance rule, install writer). `Extensions` avoids schema churn but is unvalidated, so it needs its own checks.

**6. examples/hello-app**
- Faults in `examples/hello-app/faults.go:1-75`: `HELLO_FAULT_ERROR_PERCENT` (0-100 page 500s), `HELLO_FAULT_UNHEALTHY` (healthz 503), `HELLO_FAULT_CRITICAL_LOG` (critical lines). Set via process env (`main.go:14-16`).
- Smoke rehearsal injects the fault in code: a scripted candidate adds an `init()` that sets `HELLO_FAULT_ERROR_PERCENT=100` (`tests/smoke/release_rehearsal_test.go:111,215-222`).
- Acceptance tests use `examples/hello-app/main.go` only as a patch target against the fake harness (`tests/acceptance/agent_judge_lifecycle_test.go:31,110`). E2E uses it as a greeting diff (`tests/e2e/release_scenario_helpers_test.go:48-61`).

**7. Conformance**
- `sisf conformance check [file]` (`cmd/sisf/cmd_conformance.go`, self-registered via `init()`). Entry `internal/conformance/check.go:14,21,75`. Rules `rule_names.go`, `rules_*.go`.
- Checks: nine units, three zones, four segments; no shared credentials or store roles; isolation; QA independence; supervisor artifact; backends in capability matrix; secrets referenced; release.units only app, control, dispatcher, runner; synthetic suite backs self-publish (`rules_release.go:137-140`); unit_inputs validity.
- Capability areas `pkg/conformance/areas.go:20-30`. Deploy needs rollback, observed revision and metrics (`rules_backends.go:46`).

**8. Shared contracts (per AGENTS.md)**
- `pkg/core/*.go`. Interface files: `pkg/deploy/deploy.go` (Backend), `pkg/state/state.go`, `pkg/qa/qa.go`, `pkg/qa/judge.go`, `pkg/execution/execution.go`, `pkg/telemetry/telemetry.go`, `pkg/decision/decision.go`, `pkg/agent/agent.go`, `pkg/secrets/secrets.go`, `pkg/github/github.go`, `pkg/identity/identity.go`, `pkg/approvals/approvals.go`.
- `cmd/sisf/main.go` (`register` at `:127`). New commands self-register, so `main.go` need not change.
- `Makefile`: `generate` (`:39-40`) covers `api/openapi.yaml` only. Schema regen is test-driven.
- `.github/workflows/ci.yml`.
- Not in the list but public: `pkg/manifest` schema (`SchemaID`).
- `sisf adopt` does not exist (no matches for "adopt" in `cmd/sisf`).

## Relevant Files
1. `/Volumes/GOON/claude/sisf/feat-managed-application-deployment-c8b548/pkg/manifest/manifest.go` - Component, Unit, Release, Rollback, Extensions
2. `/Volumes/GOON/claude/sisf/feat-managed-application-deployment-c8b548/pkg/deploy/deploy.go` - Backend, Metrics, Artifact, SelfPublishAllowed
3. `/Volumes/GOON/claude/sisf/feat-managed-application-deployment-c8b548/internal/install/manifest_units.go` - nine units per mode
4. `/Volumes/GOON/claude/sisf/feat-managed-application-deployment-c8b548/internal/install/build_docker.go` - hard-coded Docker images
5. `/Volumes/GOON/claude/sisf/feat-managed-application-deployment-c8b548/templates/compose/compose.yaml` - fixed app service and supervisor flags
6. `/Volumes/GOON/claude/sisf/feat-managed-application-deployment-c8b548/adapters/deploy/compose/containers.go` - candidate container spec
7. `/Volumes/GOON/claude/sisf/feat-managed-application-deployment-c8b548/cmd/supervisor/stages/hard_gates.go` - hard gates
8. `/Volumes/GOON/claude/sisf/feat-managed-application-deployment-c8b548/internal/supervisor/release/rollout_and_watch.go` - canary, watch, low-traffic synthetic
9. `/Volumes/GOON/claude/sisf/feat-managed-application-deployment-c8b548/internal/supervisor/release/governance.go` - protected paths and checkGovernance
10. `/Volumes/GOON/claude/sisf/feat-managed-application-deployment-c8b548/cmd/factoryd/wiring/decision_layer.go` - detection cadence and rules

## Patterns
- Units are a closed set of nine. Zone, lifecycle and networks come from the name. Only realization differs by mode.
- Candidate builds run in a sandbox and must emit a report. Standalone accepts one static program; Docker accepts a digest-pinned image.
- Monitoring uses edge counters keyed by revision digest. Health requires the app's own `/healthz` to report the digest.
- Governance is default-deny. Protected paths are hard-coded, and self-publish is limited to declared unit inputs.

## Risks
- A stock WordPress image has no `/healthz` JSON revision, so it can never be observed healthy. Rollback or hold follows.
- Docker candidate containers get no env_file, no data volume and default limits. A candidate would be judged in a different environment than production.
- No backup-before-release and no restore-after-rollback. Rollback never touches data. The migrations gate covers only this repo's paths, not WordPress or MariaDB schema.
- Manifest `release.rollback` thresholds look authoritative but the supervisor ignores them. Operators may tune the wrong knob.
- Docker mode has no synthetic suite, so low-traffic releases hold.
- Metrics have no latency signal.

## Unresolved Questions
- Can the Docker-mode runner sandbox build and report an image (needs docker access inside the sandbox)? Not read: `tests/e2e/compose_sandboxed_build_runner_pin_test.go`.
- Is `SISF_EDGE_COUNTERS_URL` set unconditionally in standalone (`standalone_units.go:138` conditionality not read)?
- What does intake do with `sisf:report` issues? `internal/intake/report` beyond its header was not read.
- `pkg/conformance` kit files and `internal/conformance/rules_release.go` beyond the grep hits were not read in full.