# FEATURES.md

Catalog of functionality approved by a human. Every new feature needs Owner approval before it is listed (brief §4.1). Approved is not the same as built: the status column says which.

| Feature | Approved scope | Status |
|---|---|---|
| Three-zone factory in one deployment | Brief §2, §10 | built: `internal/install/fresh_install_single_factory_test.go`, `internal/install/docker_lifecycle_test.go`, `internal/install/zone_isolation_test.go` |
| GitHub issue workflow and state machine | Brief §5 | built: `tests/acceptance/issue_full_lifecycle_test.go`, `tests/acceptance/duplicate_reordered_webhooks_test.go`, `internal/intake/clarify_flow_test.go`, `cmd/factoryd/wiring/control_reconcile_test.go` |
| Independent QA and receipts | Brief §6.1 | built: `internal/qa/self_scoring_rejected_test.go`, `internal/qa/hard_gate_overrides_score_test.go`, `tests/acceptance/agent_judge_lifecycle_test.go` |
| Release with observed revision and automatic rollback | Brief §6.2 | built: `internal/supervisor/release/publish_observed_revision_test.go`, `internal/supervisor/release/auto_rollback_restores_lkg_test.go`, `internal/supervisor/release/hold_without_metrics_or_rollback_test.go` |
| Trusted release supervisor | Brief §6.3 | built: `internal/conformance/supervisor_boundary_test.go`, `internal/supervisor/digest_self_check_test.go`, `tests/acceptance/factory_self_update_zone3_test.go` |
| GitHub and Google sign-in, roles, approvals | Brief §7 | built against a fake identity provider, not yet tried with real Google SSO: `internal/auth/sso_oauth_negative_test.go`, `tests/acceptance/cross_surface_approval_test.go` |
| Budget metering, default unlimited, optional cap | Brief §8 | built: `internal/budget/budget_modes_test.go`, `internal/budget/budget_status_test.go` |
| Dashboard, API, CLI, MCP, WebMCP | Brief §9 | built, tested in process against a fake identity provider: `tests/acceptance/five_surface_parity_test.go`, `tests/acceptance/surface_clients_test.go`, `tests/acceptance/surface_clients_cli_and_dashboard_test.go` |
| Decision Layer | Brief §4.2 | built, not yet proven against a live edge: `internal/decision/no_benefit_signal_ignored_test.go`, `internal/decision/untrusted_operational_data_test.go`, `cmd/factoryd/wiring/decision_layer_test.go` |

Update the status column only when code and a passing test prove it. Contributors propose entries by PR; the Owner approves.
