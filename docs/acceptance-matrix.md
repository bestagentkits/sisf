# Acceptance matrix

Maps each acceptance test of brief §14 (SISF-01 to SISF-20) and each additional security requirement of brief §7 to the test file that proves it. The brief is the contract; this page only routes to the proof. Run a row's package with `go test ./<dir>/...`; the whole matrix is covered by `go test ./... -race`.

Status meanings: **present** means the file exists in this branch and its package passes `go test`.

## SISF-01 to SISF-20

| ID | Test file | Status |
|---|---|---|
| SISF-01 | `internal/install/fresh_install_single_factory_test.go` | present |
| SISF-02 | `tests/acceptance/issue_full_lifecycle_test.go` | present |
| SISF-03 | `internal/intake/unauthorized_and_new_feature_test.go` | present |
| SISF-04 | `tests/acceptance/duplicate_reordered_webhooks_test.go` | present |
| SISF-05 | `internal/dispatcher/worker_crash_fencing_test.go` | present |
| SISF-06 | `internal/qa/self_scoring_rejected_test.go` | present |
| SISF-07 | `internal/qa/hard_gate_overrides_score_test.go` | present |
| SISF-08 | `internal/supervisor/release/publish_observed_revision_test.go` | present |
| SISF-09 | `internal/supervisor/release/auto_rollback_restores_lkg_test.go` | present |
| SISF-10 | `tests/acceptance/factory_self_update_zone3_test.go` | present |
| SISF-11 | `internal/policy/unapproved_policy_change_ignored_test.go` | present |
| SISF-12 | `internal/auth/sso_oauth_negative_test.go` | present (against fakes, not real Google SSO) |
| SISF-13 | `tests/acceptance/five_surface_parity_test.go` (with `cross_surface_approval_test.go` and `step_up_and_write_limits_test.go`) | present (CLI, REST, MCP, WebMCP tool path and dashboard forms against the wired control server and fake identity provider) |
| SISF-14 | `internal/budget/budget_modes_test.go` | present |
| SISF-15 | `tests/acceptance/two_factories_same_upstream_test.go` | present |
| SISF-16 | `internal/conformance/backend_capability_matrix_test.go` | present |
| SISF-17 | `internal/decision/no_benefit_signal_ignored_test.go` | present |
| SISF-18 | `internal/agentrun/fresh_agent_context_rebuild_test.go` | present |
| SISF-19 | `tests/acceptance/prompt_injection_containment_test.go` | present |
| SISF-20 | `internal/supervisor/release/hold_without_metrics_or_rollback_test.go` | present |

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

- End-to-end lifecycle on Compose and Standalone: tests exist under `tests/e2e/` behind the `e2e` build tag; a passing run is not claimed here. The e2e scenario still reads the dashboard with the removed Owner token and needs updating to a signed-in session.
- Real smoke run: `tests/smoke/` behind the `smoke` tag (`make smoke`, `SISF_SMOKE_REPO=bestagentkits/sisf-sandbox` only). It needs an operator to approve the CLI sign-in in a browser, and install flags that do not exist yet; it has not been run, so no passing run is claimed.
- The Decision Layer runs in the control process when `SISF_EDGE_ADMIN_URL` is set (`cmd/factoryd/wiring/decision_layer_test.go` covers rules only, the System One section and the interval). SISF-17 and requirement 4 cover its negative rules. It has not been proven against a live edge.
- Remote MCP and WebMCP are tested only in process, against the fake identity provider.
