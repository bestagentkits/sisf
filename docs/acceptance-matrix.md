# Acceptance matrix

Maps each acceptance test of brief §13 (SISF-01 to SISF-20) and each additional security requirement of brief §7 to the test file that proves it. The brief is the contract; this page only routes to the proof. Run a row's package with `go test ./<dir>/...`; the whole matrix is covered by `go test ./... -race`.

Status meanings: **present** means the file exists in this branch and its package passes `go test`.

## SISF-01 to SISF-20

| ID | Test file | Status |
|---|---|---|
| SISF-01 | `internal/install/fresh_install_single_factory_test.go` (Standalone) and `internal/install/docker_lifecycle_test.go` (`TestFreshDockerInstallIsOneComposeProjectOfOneFactory`, Compose) | present (both modes against fakes of `go` and `docker`) |
| SISF-02 | `tests/acceptance/issue_full_lifecycle_test.go` (with `tests/acceptance/candidate_pull_request_landing_test.go` and `candidate_pull_request_owner_cases_test.go` for the candidate's pull request, its merge after verification and the cases left for the Owner, `cmd/supervisor/stages/candidate_landing_github_answers_test.go` for a lost merge answer, a retried or refused merge, a refused pull request, one GitHub refuses to show and one the factory did not open, `tests/acceptance/changed_paths_listed_in_part_test.go` for a comparison GitHub may list only in part, `cmd/supervisor/stages/candidate_pull_request_account_test.go` for telling the factory's own pull request by its login, `tests/acceptance/qa_hold_visibility_and_spend_test.go` for the one progress comment, and `cmd/factoryd/wiring/progress_close_gate_test.go` for the issue closing only after that comment shows the end) | present |
| SISF-03 | `internal/intake/unauthorized_and_new_feature_test.go` | present |
| SISF-04 | `tests/acceptance/duplicate_reordered_webhooks_test.go` (with `cmd/factoryd/wiring/control_reconcile_test.go` for reconciliation) | present |
| SISF-05 | `internal/dispatcher/worker_crash_fencing_test.go` | present |
| SISF-06 | `internal/qa/self_scoring_rejected_test.go` | present |
| SISF-07 | `internal/qa/hard_gate_overrides_score_test.go` | present |
| SISF-08 | `internal/supervisor/release/publish_observed_revision_test.go` | present |
| SISF-09 | `internal/supervisor/release/auto_rollback_restores_lkg_test.go` | present |
| SISF-10 | `tests/acceptance/factory_self_update_zone3_test.go` | present |
| SISF-11 | `internal/policy/unapproved_policy_change_ignored_test.go` | present |
| SISF-12 | `internal/auth/sso_oauth_negative_test.go` | present (against fakes, not real Google SSO) |
| SISF-13 | `tests/acceptance/five_surface_parity_test.go` (with `cross_surface_approval_test.go` and `step_up_and_write_limits_test.go`) | present (CLI, REST, MCP, WebMCP tool path and dashboard forms against the wired control server and fake identity provider) |
| SISF-14 | `internal/budget/budget_modes_test.go` (with `internal/budget/budget_status_test.go` `TestUnverifiedUsageAndAMissingHardCapAreShown` for a provider without a hard cap) | present |
| SISF-15 | `tests/acceptance/two_factories_same_upstream_test.go` | present |
| SISF-16 | `internal/conformance/backend_capability_matrix_test.go` | present |
| SISF-17 | `internal/decision/no_benefit_signal_ignored_test.go` | present |
| SISF-18 | `internal/agentrun/fresh_agent_context_rebuild_test.go` | present |
| SISF-19 | `tests/acceptance/prompt_injection_containment_test.go` | present |
| SISF-20 | `internal/supervisor/release/hold_without_metrics_or_rollback_test.go` | present |

## A Standalone release runs the candidate's own program (ADR 0022)

| Behaviour | Test file | Status |
|---|---|---|
| Programs are kept under the sha256 of their bytes, hashed again before use, and swept | `internal/supervisor/programstore/store_test.go` | present |
| The image digest of a build is the digest of the program the supervisor received; anything else is an outage that keeps nothing | `cmd/supervisor/stages/sandbox_build_program_export_test.go` | present |
| The local backend starts the store's file for a stored digest, the installed program for the pinned digest, nothing otherwise; a restarted supervisor starts the promoted program | `cmd/supervisor/local_programs_test.go` (with `standalone_build_sandbox_route_test.go` for which factories keep programs) | present |
| Stored programs that are neither the last known good release nor in flight are removed | `cmd/supervisor/program_retention_test.go` | present |
| The launcher starts the program it is given; `sisf up` keeps a promotion whose program is in the store | `internal/install/standalone_promoted_program_test.go` | present |
| The smoke run's build command exports the one program its digest names | `tests/smoke/build_candidate_script_unit_test.go` (build tag `smoke`; needs a local `go` and `git`) | present |

## Additional security requirements (brief §7)

| # | Requirement | Test file | Status |
|---|---|---|---|
| 1 | Agent egress | `internal/egress/allowlist_policy_test.go` | present |
| 2 | Isolation between the zones | `internal/install/zone_isolation_test.go` (static; the live check runs under build tag `e2e`) | present |
| 3 | Supply chain | `internal/supplychain/unsigned_artifact_rejected_test.go` | present |
| 4 | Operational data is untrusted | `internal/decision/untrusted_operational_data_test.go` | present |
| 5 | Signing-key management | `internal/signing/delegation/delegated_key_revocation_test.go`, `internal/signing/delegation/webhook_secret_rotation_test.go` | present |
| 6 | Tamper-evident audit log | `internal/audit/hash_chain_tamper_test.go` | present |
| 7 | Redaction and retention | `internal/redact/redaction_retention_test.go` | present |
| 8 | Kill switch and threat model | `internal/supervisor/killswitch/emergency_stop_test.go`, `tests/security/threat_model_coverage_test.go` | present |

## Not covered by unit-level proof yet

- End-to-end lifecycle on Compose and Standalone: `tests/e2e/` behind the `e2e` build tag, run by `make e2e` (not in CI). Every scenario signs the Owner in at the fake identity provider; there is no Owner token. Each mode also runs a surfaces scenario (`tests/e2e/surfaces_front_door_scenario_test.go`): sign-in, REST with a personal access token, an MCP read with an OAuth PKCE token, `/webmcp/actions.json`, and a dashboard approval after a targeted step-up. The surfaces are reached at control's external URL, not through the edge. One local `make e2e` run passed in both modes; that is a single run, not a CI gate.
- Real smoke run: `tests/smoke/` behind the `smoke` tag (`make smoke`, `SISF_SMOKE_REPO=bestagentkits/sisf-sandbox` only, never in CI). It is unattended: it installs with `sisf init --dev`, real Claude and Codex agents and `--exec local`, and approves the CLI sign-in itself. Only its command construction and redaction are unit-tested; the paid run has not been run.
- Release rehearsals: the same run with `SISF_SMOKE_REHEARSAL=1`, a scripted coder and a fake QA judge, so no model is paid for; the build sandbox, the release pipeline with its default windows, the local deploy backend and GitHub are real. Good candidate (`tests/smoke/deployed_candidate_test.go`, `lifecycle_run_test.go`): its changed page is served through the edge under its own digest while its pull request is open, the issue closes, the pull request merges, and the promoted program still serves after `sisf restart` and after `sisf down` and `sisf up`. Bad candidate (`SISF_SMOKE_REHEARSAL_CANDIDATE=bad`, `tests/smoke/release_rollback_rehearsal_test.go`): it passes the fake judge, fails the synthetic suite, is rolled back to the last known good release, its pull request stays unmerged and the issue ends Blocked with the reason. Each passed once locally on darwin/arm64; that is a single run each, not a CI gate. The rollback restored the installed release; a rollback to an earlier promoted program is covered only by `cmd/supervisor/local_programs_test.go`.
- The Decision Layer runs in the control process when `SISF_EDGE_COUNTERS_URL` is set, reading with the edge's read-only counters token (`cmd/factoryd/wiring/decision_layer_test.go` covers rules only, the System One section and the interval). SISF-17 and requirement 4 cover its negative rules. It has not been proven against a live edge.
- Remote MCP and WebMCP are tested in process and by the e2e surfaces scenario, always against the fake identity provider; no real provider has been tried.
