// Package security_test holds the checks that tie the threat model to the
// tests that prove its controls.
package security_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// repoRoot is the repository root, seen from this package's directory.
const repoRoot = "../.."

// threatModel is the document whose high-severity scenarios must be covered.
const threatModel = "docs/threat-model.md"

// proof names test functions in one test file, relative to the root.
type proof struct {
	file  string
	tests []string
}

// coverage maps each high-severity scenario to the tests that exercise its
// required control. A scenario is covered only when every test listed exists.
var coverage = map[string][]proof{
	// Prompt injection makes an agent leak a token, widen its permissions or edit policy.
	"TM-01": {
		{"tests/acceptance/prompt_injection_containment_test.go", []string{"TestPromptInjectionInAnIssueIsContained"}},
		{"internal/secretbroker/control_tokens_never_reach_runner_test.go", []string{"TestControlPlaneCredentialsAreNeverGrantedToARun"}},
		{"internal/policy/precedence_test.go", []string{"TestIssueTextAndLogsNeverSetPolicy"}},
		{"internal/egress/allowlist_policy_test.go", []string{"TestAnAgentCannotWidenThePolicyNorTurnBlockingOff"}},
		{"cmd/factoryd/wiring/agent_zone_environment_test.go", []string{
			"TestARealAgentSeesNoneOfTheDispatchersVariables", "TestARealAgentNeedsASandboxALauncherOrTheTrustedCodeOnlyModel",
		}},
		{"pkg/agent/cliagent/empty_environment_test.go", []string{"TestAnEmptyEnvironmentIsEmptyAndNeverTheParents"}},
		{"cmd/factoryd/wiring/agent_zone_operator_home_test.go", []string{
			"TestTheOperatorsHomeIsAcceptedOnlyUnderTrustedCodeAndApartFromTheFactorysDirectories",
		}},
		{"cmd/supervisor/qa_operator_home_test.go", []string{"TestTheQAAgentsHomeLiesApartFromTheFactorysSecretsStateAndTrust"}},
	},
	// A candidate release modifies the supervisor, keys, policy enforcement, QA baseline or rollback rules.
	"TM-02": {
		{"internal/supervisor/release/governance_change_refused_test.go", []string{
			"TestACandidateThatTouchesTheTrustedControlSurfaceIsAGovernanceChange",
			"TestTheReleasePipelineRefusesAGovernanceChangeBeforeDeployingAnything",
			"TestABuildOfTheSupervisorIsNeverReleasedByThePipeline",
		}},
		{"internal/supervisor/release/config_rules_test.go", []string{"TestAPipelineRefusesParametersThatWeakenTheReleaseRules"}},
		{"internal/supervisor/digest_self_check_test.go", []string{"TestSupervisorRefusesToStartWhenItsDigestDiffersFromTheSignedManifest"}},
		{"internal/policy/build_base_test.go", []string{
			"TestAChangeToFoundationContentStopsTheFactoryUntilTheOwnerApproves",
			"TestOnlyTheSupervisorMovesTheCommitToBuildOn",
			"TestATamperedProofOfUnchangedContentIsRefused",
		}},
		{"tests/acceptance/base_contains_last_known_good_test.go", []string{
			"TestASecondReleaseOfAUnitOnABaselineWithoutTheFirstIsStoppedBeforeQA",
		}},
	},
	// A manipulated agent exfiltrates data through a network call.
	"TM-03": {
		{"internal/egress/allowlist_policy_test.go", []string{"TestTheDefaultAllowlistCoversWhatDevelopmentNeedsAndNothingElse"}},
		{"internal/egress/proxy_bypass_test.go", []string{"TestIPLiteralsAndDisguisedAddressesAreRefused", "TestAnUnavailablePolicyBlocksEverything"}},
		{"internal/conformance/backend_sandboxing_test.go", []string{"TestABackendThatCannotSandboxNeedsTheOwnersTrustedCodeAcknowledgement"}},
	},
	// QA scores its own patch, a score hides a hard-gate failure, or a receipt is reused.
	"TM-04": {
		{"internal/qa/self_scoring_rejected_test.go", []string{"TestTheCoderCannotScoreItsOwnCandidate", "TestAReceiptNamingTheCoderAsEvaluatorDoesNotVerify"}},
		{"internal/qa/hard_gate_overrides_score_test.go", []string{"TestAFailedHardGateOverridesAPerfectScore"}},
		{"internal/qa/signed_receipt_test.go", []string{"TestAReceiptIsNotBorrowedByAnotherCandidate"}},
		{"internal/supervisor/release/unverified_candidate_refused_test.go", []string{"TestAReleaseWithAnUnacceptableReceiptIsNeverDeployed"}},
		{"tests/acceptance/candidate_build_trust_test.go", []string{
			"TestTheCoderAndTheAttemptComeFromTrustedRecords", "TestAnUnverifiedCoderFailsTheIndependenceGate",
		}},
	},
	// A delegated signing key or the Zone 3 store is compromised.
	"TM-05": {
		{"internal/signing/delegation/delegated_key_revocation_test.go", []string{
			"TestRevokedKeyIsRefusedWithoutReplacingTheRoot", "TestExpiredKeyIsRefused", "TestOnlyTheIssuedTrustedHolderMayLoadAKey",
		}},
		{"internal/supervisor/release/lkg_survives_key_expiry_test.go", []string{"TestAPromotedLKGStaysRestorableAfterTheKeyThatSignedItExpired"}},
	},
	// A forged, replayed or reordered webhook triggers work or duplicate spending.
	"TM-06": {
		{"internal/control/webhook_test.go", []string{"TestWebhookWithABadSignatureStoresNothing"}},
		{"tests/acceptance/duplicate_reordered_webhooks_test.go", []string{"TestDuplicateAndReorderedDeliveriesForOneIssueMakeOneRun"}},
		{"pkg/state/sqlstore/inbox_outbox_test.go", []string{"TestDeliveryIsAcceptedOnceHoweverOftenItArrives"}},
		{"pkg/state/sqlstore/transition_lease_test.go", []string{"TestAWorkerThatLostItsLeaseCannotWriteWithItsOldTokenOrWithNone"}},
	},
	// Zone 1 is compromised and reaches Zone 3 or Zone 2 secrets.
	"TM-07": {
		{"pkg/state/sqlstore/zone_privileges_test.go", []string{"TestApplicationZoneReachesNothingInTheFactoryStore"}},
		{"internal/install/zone_isolation_test.go", []string{"TestNothingMountsTheSecretsDirectoryAndSecretsStayOutOfTheComposeFile"}},
		{"internal/conformance/missing_zone_test.go", []string{"TestAManifestMissingAnyZoneIsRejected"}},
	},
	// A poisoned dependency, base image, CI change or unsigned artifact reaches release.
	"TM-08": {
		{"internal/supplychain/supply_chain_gate_test.go", []string{
			"TestGateRequiresOwnerApprovalForANewDependency", "TestGateRefusesUnpinnedBaseImages", "TestGateRefusesUnpinnedCIConfiguration",
		}},
		{"internal/supplychain/unsigned_artifact_rejected_test.go", []string{"TestUnsignedArtifactIsRejected"}},
		{"internal/qa/hard_gate_overrides_score_test.go", []string{"TestTheSupplyChainGateOverridesAPerfectScore"}},
		{"internal/supervisor/release/unverified_candidate_refused_test.go", []string{"TestAnArtifactWhoseSignatureCannotBeVerifiedNeverBecomesTheLKG"}},
		{"tests/acceptance/candidate_build_trust_test.go", []string{"TestAPatchTouchingCIIsRefusedBeforeAnythingIsPushed"}},
		{"cmd/supervisor/stages/protected_paths_test.go", []string{"TestTheBuilderRefusesAProtectedPathBeforeReadingOrPushingAnything"}},
	},
	// An approval is replayed, forged, or granted through an ambient Owner session.
	"TM-09": {
		{"pkg/approvals/store_consume_test.go", []string{
			"TestAnApprovalIsSpentExactlyOnce", "TestAnApprovalIsNotSpentOnAnEditedSubjectOrAChangedPolicy", "TestAnExpiredApprovalIsRefused",
		}},
		{"pkg/approvals/submit_test.go", []string{"TestOnlyAnOwnerMayDecide"}},
		{"internal/control/surfaces_test.go", []string{"TestAPIIgnoresADashboardSession", "TestASessionWritesOnlyWithItsCSRFToken"}},
		{"tests/acceptance/cross_surface_approval_test.go", []string{
			"TestAnApprovalCrossesEverySurfaceAndOnlyAFreshSessionDecidesIt", "TestAnApprovalIsSpentOnceAndOnlyInItsOwnFactory",
		}},
		{"tests/acceptance/step_up_and_write_limits_test.go", []string{"TestAStaleStepUpIsRefusedAndAFreshOneDecides"}},
		{"internal/supervisor/release/owner_rollback_test.go", []string{"TestARollbackWithoutAValidSingleUseApprovalIsRefused"}},
	},
	// Audit records are edited or deleted to hide an action.
	"TM-10": {
		{"internal/audit/hash_chain_tamper_test.go", []string{
			"TestDatabaseRefusesToEditOrDeleteAnAuditRecord", "TestEditedRecordIsDetected", "TestDeletedRecordIsDetected",
		}},
	},
	// A malicious PR from a fork runs with secrets or write access.
	"TM-11": {
		{"internal/intake/prgate/gate_test.go", []string{
			"TestPublicRepoForkRunsSandboxedWithoutSecretsUntilAccepted", "TestPrivateRepoAcceptsOnlyCollaboratorsAndFactoryAgents",
		}},
	},
	// A loop or provider quirk causes runaway spend.
	"TM-12": {
		{"internal/dispatcher/limits_test.go", []string{
			"TestAHangingAttemptIsCancelledAtTheRunTimeout", "TestTheAttemptLimitStopsARunWithAnUnlimitedBudget", "TestTheConcurrencyCapHoldsWithAnUnlimitedBudget",
		}},
		{"internal/dispatcher/budget_gate_test.go", []string{"TestReservationsAreIdempotentBoundToTheirRunAndSettleOrRelease"}},
		{"internal/install/manifest_build_test.go", []string{"TestUnconfirmedBudgetAlertsOnAnySpendingAndNeverSwitchesTheAlertOff"}},
		{"internal/actions/service_roles_rate_delegated_test.go", []string{"TestWritesBeyondTheRateLimitAreRefusedAndReadsAreNot"}},
		{"tests/acceptance/step_up_and_write_limits_test.go", []string{"TestWritesBeyondTheLimitAreRefusedOnEverySurfaceAndReadsAreNot"}},
	},
	// Secrets or personal data reach logs, transcripts, issues, PRs or providers.
	"TM-13": {
		{"internal/redact/redaction_retention_test.go", []string{"TestSecretsAndPersonalDataAreRedactedBeforeEveryDestination", "TestSweepPurgesEachKindAfterItsOwnPeriod"}},
		{"internal/redact/sharing_policy_test.go", []string{"TestDefaultSharingPolicySendsWorkDataButNeverPersonalDataOrSecrets"}},
	},
	// The compromised or failed factory cannot be stopped.
	"TM-14": {
		{"internal/supervisor/killswitch/emergency_stop_test.go", []string{
			"TestAnEmergencyStopHaltsClaimsRunsSecretsAndPublishing", "TestAPartThatFailsIsReportedAndTheRestOfTheStopHolds",
			"TestAStopThatCannotBeRecordedFailsAndAnUnreadableStopCountsAsFrozen",
		}},
		{"internal/supervisor/killswitch/operator_stop_test.go", []string{"TestAnOperatorStopWithoutASupervisorStillFreezesButFails"}},
		{"internal/supervisor/release/auto_rollback_restores_lkg_test.go", []string{"TestAnEmergencyStopDuringTheRolloutRestoresTheLKG"}},
		{"internal/secretbroker/broker_test.go", []string{"TestCancellingARunRevokesEveryGrantAndRefusesNewOnes"}},
	},
	// Rollback overwrites customer data, or a release is published without metrics or rollback.
	"TM-15": {
		{"internal/supervisor/release/auto_rollback_restores_lkg_test.go", []string{
			"TestAFailingCanaryIsRolledBackToTheLKGWithinTwoWindows", "TestARestoreThatCannotBeObservedIsNotReportedAsRolledBack",
		}},
		{"internal/supervisor/release/hold_without_metrics_or_rollback_test.go", []string{
			"TestAReleaseHoldsWhenItCannotBeObservedOrRolledBack", "TestLosingTheMetricsDuringTheRolloutRestoresTheLKGInsteadOfAssumingSuccess",
		}},
		{"internal/supervisor/release/publish_observed_revision_test.go", []string{"TestAReleaseTheRunningSystemDoesNotReportIsNeverVerified"}},
	},
}

var scenarioRow = regexp.MustCompile(`^\|\s*(TM-\d+)\s*\|`)

// highSeverityScenarios reads the scenario IDs of the threat model's
// high-severity table.
func highSeverityScenarios(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot, threatModel))
	if err != nil {
		t.Fatalf("read the threat model: %v", err)
	}
	var ids []string
	inSection := false
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "## ") {
			inSection = strings.Contains(strings.ToLower(line), "high-severity")
			continue
		}
		if m := scenarioRow.FindStringSubmatch(line); inSection && m != nil {
			ids = append(ids, m[1])
		}
	}
	if len(ids) == 0 {
		t.Fatalf("%s lists no high-severity scenario; the table moved or was renamed", threatModel)
	}
	return ids
}

func TestEveryHighSeverityThreatIsCoveredByTestsThatExist(t *testing.T) {
	t.Parallel()
	ids := highSeverityScenarios(t)
	for _, id := range ids {
		proofs, ok := coverage[id]
		if !ok || len(proofs) == 0 {
			t.Errorf("%s has no test proving its control", id)
			continue
		}
		for _, p := range proofs {
			source, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(p.file)))
			if err != nil {
				t.Errorf("%s: test file %s cannot be read: %v", id, p.file, err)
				continue
			}
			for _, name := range p.tests {
				declared := regexp.MustCompile(`(?m)^func ` + regexp.QuoteMeta(name) + `\(t \*testing\.T\)`)
				if !declared.Match(source) {
					t.Errorf("%s: %s does not declare %s", id, p.file, name)
				}
			}
		}
	}
	var stale []string
	for id := range coverage {
		if !slices.Contains(ids, id) {
			stale = append(stale, id)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("coverage lists scenarios the threat model no longer has: %v", stale)
	}
}
