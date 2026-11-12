# Acceptance matrix

Maps each acceptance test of brief §13 (SISF-01 to SISF-20) and each additional security requirement of brief §7 to the test file that proves it. The brief is the contract; this page only routes to the proof. Run a row's package with `go test ./<dir>/...`; the whole matrix is covered by `go test ./... -race`.

Status meanings: **present** means the file exists in this branch and its package passes `go test`.

## SISF-01 to SISF-20

| ID | Test file | Status |
|---|---|---|
| SISF-01 | `internal/install/fresh_install_single_factory_test.go` (Standalone) and `internal/install/docker_lifecycle_test.go` (`TestFreshDockerInstallIsOneComposeProjectOfOneFactory`, Compose) | present (both modes against fakes of `go` and `docker`) |
| SISF-02 | `tests/acceptance/issue_full_lifecycle_test.go` (with `tests/acceptance/candidate_pull_request_landing_test.go` and `candidate_pull_request_owner_cases_test.go` for the candidate's pull request, its merge after verification and the cases left for the Owner, `cmd/supervisor/stages/candidate_landing_github_answers_test.go` for a lost merge answer, a retried or refused merge, a refused pull request, one GitHub refuses to show and one the factory did not open, `tests/acceptance/changed_paths_listed_in_part_test.go` for a comparison GitHub may list only in part, `cmd/supervisor/stages/candidate_pull_request_account_test.go` for telling the factory's own pull request by its login and adopting one an earlier build opened without the run's marker, `internal/supervisor/release/landing_due_cleared_test.go` for the record of a due landing being deleted when the issue closes, `cmd/factoryd/wiring/progress_restore_test.go` for the progress comment of a rolled-back run, `tests/acceptance/qa_hold_visibility_and_spend_test.go` for the one progress comment, and `cmd/factoryd/wiring/progress_close_gate_test.go` for the issue closing only after that comment shows the end) | present |
| SISF-03 | `internal/intake/unauthorized_and_new_feature_test.go` | present |
| SISF-04 | `tests/acceptance/duplicate_reordered_webhooks_test.go` (with `cmd/factoryd/wiring/control_reconcile_test.go` for reconciliation) | present |
| SISF-05 | `internal/dispatcher/worker_crash_fencing_test.go` | present |
| SISF-06 | `internal/qa/self_scoring_rejected_test.go` | present |
| SISF-07 | `internal/qa/hard_gate_overrides_score_test.go` | present |
| SISF-08 | `internal/supervisor/release/publish_observed_revision_test.go` | present |
| SISF-09 | `internal/supervisor/release/auto_rollback_restores_lkg_test.go` | present |
| SISF-10 | `tests/acceptance/factory_self_update_zone3_test.go` (with `tests/acceptance/release_units_owner_choice_test.go` and `cmd/supervisor/self_publishable_units_test.go` for `release.units`, `tests/acceptance/store_rules_need_governance_test.go` and `internal/supervisor/release/governance_change_refused_test.go` for the trusted control surface, `tests/acceptance/self_published_change_unit_inputs_test.go` for a dashboard template and the dashboard's Go code refused, whether or not `control` is a release unit) | present |
| SISF-11 | `internal/policy/unapproved_policy_change_ignored_test.go` | present |
| SISF-12 | `internal/auth/sso_oauth_negative_test.go` | present (against fakes, not real Google SSO) |
| SISF-13 | `tests/acceptance/five_surface_parity_test.go` (with `cross_surface_approval_test.go` and `step_up_and_write_limits_test.go`) | present (CLI, REST, MCP, WebMCP tool path and dashboard forms against the wired control server and fake identity provider) |
| SISF-14 | `internal/budget/budget_modes_test.go` (with `internal/budget/budget_status_test.go` `TestUnverifiedUsageAndAMissingHardCapAreShown` for a provider without a hard cap; `internal/budget/run_reservation_id_test.go`, `cmd/factoryd/wiring/control_stopped_run_reservation_test.go` and `cmd/supervisor/stages/run_close_own_reservation_test.go` for closing a run's reservation by its ID, never the one its run record names; `cmd/supervisor/stages/run_close_retry_test.go` and `cmd/supervisor/stages/run_close_due_bound_test.go` for retrying a failed close and abandoning one that can never succeed) | present |
| SISF-15 | `tests/acceptance/two_factories_same_upstream_test.go` | present |
| SISF-16 | `internal/conformance/backend_capability_matrix_test.go` | present |
| SISF-17 | `internal/decision/no_benefit_signal_ignored_test.go` | present |
| SISF-18 | `internal/agentrun/fresh_agent_context_rebuild_test.go` | present |
| SISF-19 | `tests/acceptance/prompt_injection_containment_test.go` | present |
| SISF-20 | `internal/supervisor/release/hold_without_metrics_or_rollback_test.go` (with `tests/acceptance/release_hold_visibility_test.go` for how a held release shows and how the Owner lets it go ahead, `internal/supervisor/release/low_traffic_synthetic_hold_test.go` and `cmd/supervisor/synthetic_program_test.go` for the synthetic suite) | present |

## A Standalone release runs the candidate's own program (ADR 0022)

| Behaviour | Test file | Status |
|---|---|---|
| Programs are kept under the sha256 of their bytes, hashed again before use, and swept | `internal/supervisor/programstore/store_test.go` | present |
| The image digest of a build is the digest of the program the supervisor received; anything else is an outage that keeps nothing | `cmd/supervisor/stages/sandbox_build_program_export_test.go` | present |
| The local backend starts the store's file for a stored digest, the installed program for the pinned digest, nothing otherwise; a restarted supervisor starts the promoted program | `cmd/supervisor/local_programs_test.go` (with `standalone_build_sandbox_route_test.go` for which factories keep programs) | present |
| Stored programs that are neither the last known good release nor in flight are removed | `cmd/supervisor/program_retention_test.go` | present |
| The launcher starts the program it is given; `sisf up` keeps a promotion whose program is in the store | `internal/install/standalone_promoted_program_test.go` | present |
| The smoke run's build command exports the one program its digest names | `tests/smoke/build_candidate_script_unit_test.go` (build tag `smoke`; needs a local `go` and `git`) | present |

## Releases, runs and builds that stop (ADR 0023 and related)

| Behaviour | Test file | Status |
|---|---|---|
| A refused release blocks its issue until the Owner retries it; only a refusal that judged the candidate blocks it | `tests/acceptance/release_hold_visibility_test.go`, `internal/supervisor/release/unverified_candidate_refused_test.go` | present |
| Only the control zone takes the new step, on SQLite and PostgreSQL, and migration 10 upgrades a store with records in every state | `pkg/state/sqlstore/release_refusal_step_test.go` | present (the PostgreSQL cases run only with `SISF_TEST_PG_DSN` set, otherwise they skip) |
| A zone role cannot forge a lease token with plain SQL (PostgreSQL) | `pkg/state/sqlstore/lease_step_trigger_test.go` | present (runs only with `SISF_TEST_PG_DSN` set, otherwise skips) |
| A record's run, attempt, candidate, release and lease token change only as the step sets them (PostgreSQL) | `pkg/state/sqlstore/record_step_columns_test.go` | present (runs only with `SISF_TEST_PG_DSN` set, otherwise skips) |
| An unwatched release is restored to the LKG and its issue blocked, at startup and through the supervisor's own pass | `internal/supervisor/release/release_recovery_test.go`, `tests/acceptance/release_recovery_after_restart_test.go` | present |
| The Owner's retry returns a refused, interrupted, unjudged or rolled-back release to Ready | `pkg/core/statemachine/retry_guard_test.go`, `tests/acceptance/exhausted_run_blocked_test.go` | present |
| A run that used its attempts, or whose policy was superseded, blocks its issue | `tests/acceptance/exhausted_run_blocked_test.go`, `tests/acceptance/policy_superseded_mid_run_test.go` | present |
| A process an earlier supervisor left is found again by pid and start time, judged before the LKG is adopted, and stopped when a release is staged over its slot or the supervisor stops | `adapters/deploy/local/slot_records_test.go`, `cmd/supervisor/lkg_adoption_test.go`, `internal/supervisor/release/standalone_restart_test.go` | present |
| A verified release whose publication failed, or that an emergency stop held, is finished on a later pass | `internal/supervisor/release/verified_release_finished_after_failure_test.go` (`TestAVerifiedReleaseWhosePublicationFailedIsFinishedOnALaterPass`), `tests/acceptance/verified_release_publication_outage_test.go` | present |
| An unwatched release is rolled back even when another holder keeps its issue lease | `internal/supervisor/release/recovery_without_the_issue_lease_test.go` (`TestAnUnwatchedReleaseIsRolledBackEvenWhenAnotherHolderKeepsItsLease`) | present |
| A candidate that changes only identity or access code is refused for approval | `internal/supervisor/release/identity_and_access_protected_test.go` (`TestACandidateThatChangesOnlyIdentityOrAccessCodeIsRefusedForApproval`) | present |
| The protected Go packages are the dependency closure of the supervisor, the command line and the control unit, and a candidate that changes only a linked package is refused for approval | `internal/supervisor/release/trusted_go_packages_protected_test.go` | present |
| The supervisor links no part of the control unit; the unit of a change is the unit whose inputs it changes, and a change of no unit's inputs is built for the Owner to judge | `cmd/supervisor/trusted_dependency_boundary_test.go`, `cmd/supervisor/stages/unit_routing_test.go` | present |
| A candidate the factory publishes by itself may change only the inputs of the unit it releases; every other path of the repository is an input of exactly one unit or needs approval | `internal/supervisor/release/unit_inputs_test.go`, `tests/acceptance/self_published_change_unit_inputs_test.go` | present |
| The Owner's `release.unit_inputs` replaces a unit's built-in inputs, a candidate cannot widen them through the repository's manifest, and the conformance rules refuse a protected (the dashboard's templates and scripts included), overlapping or Go-toolchain pattern of a unit other than the application | `internal/supervisor/release/unit_inputs_declared_test.go`, `pkg/manifest/unit_inputs_test.go`, `internal/conformance/unit_inputs_test.go`, `tests/acceptance/declared_unit_inputs_test.go` (with `cmd/supervisor/stages/unit_routing_test.go` `TestTheRouterFollowsDeclaredInputs` for routing) | present |
| A verified release is recorded as the last known good release only while the running system serves it, healthy; a later release supersedes an earlier one, and its evidence is judged as of its Verified step | `internal/supervisor/release/lkg_follows_what_serves_test.go`, `internal/supervisor/release/verified_release_lkg_decisions_test.go` | present |
| A verified release that no longer serves or turned unhealthy is switched back, and its issue blocked as `release_unjudged` until the Owner retries; a backend that does not answer is waited on, not a block | `tests/acceptance/verified_release_unjudged_test.go` (skips as root), `internal/supervisor/release/verified_release_lkg_decisions_test.go` (`TestAVerifiedReleaseWaitsWhileTheRunningSystemCannotBeObserved`) | present |
| Only the control zone takes the Verified to Blocked step, on SQLite and PostgreSQL | `pkg/state/sqlstore/verified_release_unjudged_step_test.go` | present (the PostgreSQL cases run only with `SISF_TEST_PG_DSN` set, otherwise they skip) |
| A verified release a failed probe cannot see is waited for over the observation windows, then restored to the LKG and blocked; a healthy one is not blocked | `internal/supervisor/release/verified_release_unseen_test.go` | present |
| Verified releases are ordered by a sequence the store numbers, never by the clock except between two unnumbered releases an earlier build verified, and the unit of a later release is read from its build record | `internal/supervisor/release/verified_order_by_sequence_test.go`, `internal/supervisor/release/superseded_unit_from_build_record_test.go` | present |
| A candidate whose base lacks the commit of its unit's LKG release is refused for the Owner, and waits while GitHub cannot compare | `internal/supervisor/release/base_contains_lkg_test.go`, `tests/acceptance/base_contains_last_known_good_test.go` | present |
| The changed paths are listed again from GitHub at release and merge, both paths of a renamed or copied file included, a listing cut short is refused, and a path not in its own clean form is refused | `tests/acceptance/changed_paths_recomputed_test.go`, `tests/acceptance/protected_file_renamed_into_app_test.go`, `cmd/supervisor/stages/changed_paths_rename_test.go`, `internal/supervisor/release/governance_change_refused_test.go` | present |
| A candidate that builds the serving program is refused, not held | `internal/supervisor/release/same_program_as_serving_refused_test.go` | present |
| An emergency stop leaves releases in flight to the supervisor's recovery | `internal/supervisor/killswitch/release_in_flight_not_held_test.go` | present |
| Label names are read without regard to case | `internal/intake/labels_test.go` (`TestLabelsAreReadWithoutRegardToCase`), `pkg/github/github_test.go` (`TestHasLabelIgnoresCase`) | present |
| The edge and the module's dependencies are protected | `internal/supervisor/release/edge_and_dependencies_protected_test.go` (`TestTheEdgeAndTheModulesDependenciesAreProtected`) | present |
| A held build is listed and its Owner reminded | `tests/acceptance/held_build_listing_test.go`, `tests/acceptance/held_build_reminder_test.go` | present |
| A sandboxed build that reports no SBOM or provenance is held before QA | `tests/acceptance/candidate_build_without_supply_chain_refs_test.go` | present |

## Standalone run lock and units

| Behaviour | Test file | Status |
|---|---|---|
| A process's start time is read from `/proc` with the boot id, a record matches only a start time read the same way in the same boot, a record without a boot id is stale, and another user's unreadable `/proc` entry is a gone process; the installer and the local deploy backend's slot records share it | `internal/processstart/process_start_test.go`, `internal/processstart/process_start_linux_test.go`, `internal/install/process_identity_test.go`, `internal/install/process_identity_linux_test.go`, `adapters/deploy/local/slot_records_linux_test.go` | present |
| A unit that outlives a killed `sisf up` keeps the install running until it is stopped; on Linux the unit is told to stop when its `sisf up` dies and the install is then not running | `internal/install/orphaned_units_test.go` (`TestAUnitThatOutlivesAKilledUpKeepsTheInstallRunningUntilStopped`) | present |
| A Standalone unit starts only its own binary from the install's `bin/` directory | `internal/conformance/launcher_commands_test.go` (`TestAStandaloneUnitStartsOnlyItsOwnBinaryFromTheInstallsBinDirectory`) | present |
| The `state` and `artifacts` units start no `bin/` program, a command word with surrounding whitespace is refused, and `sisf-supervisor` always counts as the factory's binary | `internal/conformance/launcher_binaries_test.go` | present |

## Public bug reports

| Behaviour | Test file | Status |
|---|---|---|
| Reports are kept in the control store across a restart, bounded, and pruned after their retention | `internal/control/report_store_test.go` | present |
| A stored report becomes one issue labelled `sisf:report`, its text inert; a refused report is given up after a few tries and a single outage gives no report up; a report whose issue GitHub leaves unanswered holds at most one charge of the daily budget, keeps waiting with retries spaced up to 5 minutes apart and is published once GitHub answers, while the reports behind it are tried (`internal/control/report_publish_unanswered_test.go`); a full store makes room first from reports given up, then from the busiest client's newest waiting report, never one being published | `internal/control/report_publisher_test.go` | present |
| Reports are published one batch a minute, and never by an install that names the factory by its ID only | `cmd/factoryd/wiring/control_report_publish_test.go`, `cmd/factoryd/wiring/control_report_publish_id_only_test.go` | present |
| `factory.status` counts reports waiting, given up and dropped, and shows until when publishing pauses | `cmd/factoryd/wiring/control_report_status_test.go` | present |
| A report issue is never taken up alone, and when asked for it is the request of someone without access | `internal/intake/report_issue_requester_test.go` | present |
| A report flood opens at most the daily budget of issues, in receipt order, and nothing waiting is dropped | `internal/control/report_flood_test.go` (`TestAReportFloodOpensAtMostTheDailyBudgetInReceiptOrder`) | present |
| One address holds at most ten waiting reports, and two publishers over one store keep to the daily budget | `internal/control/report_client_backlog_test.go` (`TestOneAddressHoldsAtMostTenWaitingReports`), `internal/control/report_publish_budget_test.go` (`TestTwoPublishersTogetherKeepToTheDailyBudget`) | present |
| A flood that fills the report store does not stop a person's report | `internal/control/report_store_fairness_test.go` (`TestAFloodFillingTheStoreDoesNotStopAPersonsReport`) | present |
| A secret split by invisible characters is not published, every default-ignorable code point is dropped before redaction, and a title split by character references is redacted | `internal/control/report_hidden_secret_test.go` (`TestASecretSplitByInvisibleCharactersIsNotPublished`), `internal/intake/report/invisible_test.go`, `internal/control/report_issue_render_internal_test.go` | present |
| A flood of starts rotating through many sources has its random evictions held to the eviction rate of sign-in, device login and client registration, and sources starting at their own rates never lock out another source | `internal/auth/login_flow_rate_test.go`, `internal/control/api/device_grants_rate_test.go`, `internal/oauthas/dynamic_client_rate_test.go`, `internal/auth/sign_in_busy_sources_test.go`, `internal/control/api/device_busy_sources_test.go`, `internal/oauthas/dynamic_client_busy_sources_test.go`, `internal/intake/report/limiter_test.go` | present |
| A flood of sign-ins, device logins or client registrations from one source pushes out its own entries, never another source's, and never locks out a new one | `internal/auth/sign_in_exhaustion_negative_test.go`, `internal/control/api/device_start_exhaustion_negative_test.go`, `internal/oauthas/client_registration_exhaustion_negative_test.go` | present |
| When every source holds one entry, a one-per-source flood rarely evicts a person's sign-in, login request or client, because the entry dropped is picked at random among the unprotected ones | `internal/auth/login_flow_eviction_test.go` (`TestAOnePerSourceFloodRarelyEvictsAPersonsSignIn`), `internal/control/api/device_grants_eviction_test.go` (`TestAOnePerSourceFloodRarelyDropsAPersonsLoginRequest`), `internal/oauthas/dynamic_client_eviction_test.go` (`TestAOnePerSourceFloodRarelyDropsAPersonsClient`) | present |

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
- Real smoke run: `tests/smoke/` behind the `smoke` tag (`make smoke`, `SISF_SMOKE_REPO=bestagentkits/sisf-sandbox` only, never in CI). It is unattended: it installs with `sisf init --dev`, a real Claude coder and a Codex QA judge run through a container launcher, and a Docker build sandbox (`--exec docker`), and approves the CLI sign-in itself (`tests/smoke/factory_command_lines_test.go`). Only its command construction and redaction are unit-tested; a paid run has not passed end to end on this tip.
- `tests/e2e/compose_self_update_zone3_test.go` (build tag `e2e`, Docker) expects a change to the factory's own control unit, and one to the QA baseline, to be refused with `APPROVAL_REQUIRED`.
- Release rehearsals: the same run with `SISF_SMOKE_REHEARSAL=1`, a scripted coder and a fake QA judge, so no model is paid for; the build sandbox, the release pipeline with its default windows, the local deploy backend and GitHub are real. Good candidate (`tests/smoke/deployed_candidate_test.go`, `lifecycle_run_test.go`): its changed page is served through the edge under its own digest while its pull request is open, the issue closes, the pull request merges, and the promoted program still serves after `sisf restart` and after `sisf down` and `sisf up`. Bad candidate (`SISF_SMOKE_REHEARSAL_CANDIDATE=bad`, `tests/smoke/release_rollback_rehearsal_test.go`): it passes the fake judge, fails the synthetic suite, is rolled back to the last known good release, its pull request stays unmerged and the issue ends Blocked with the reason. Each passed once locally on darwin/arm64; that is a single run each, not a CI gate. The rollback restored the installed release; a rollback to an earlier promoted program is covered only by `cmd/supervisor/local_programs_test.go`.
- The Decision Layer runs in the control process when `SISF_EDGE_COUNTERS_URL` is set, reading with the edge's read-only counters token (`cmd/factoryd/wiring/decision_layer_test.go` covers rules only, the System One section and the interval). SISF-17 and requirement 4 cover its negative rules. It has not been proven against a live edge.
- Remote MCP and WebMCP are tested in process and by the e2e surfaces scenario, always against the fake identity provider; no real provider has been tried.
