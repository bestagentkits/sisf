# ADR 0023: Every release ends in a state the Owner can act on

## Status

Status: proposed. The Owner chose this direction after the trust review of the release lifecycle: a refused release leaves ReleaseCandidate, and an interrupted release is rolled back. This ADR records the decision for review before it is accepted.

## Context

A trust review of the release lifecycle found releases that stopped in a state nobody could act on:

- **Refused releases.** A release the supervisor refused before deploying anything left its issue in ReleaseCandidate for good. The refusal was recorded as a hold, and the Owner's retry was refused as final. A receipt or key that had expired since the hold was reported as a governance refusal, so the advice was to change the policy.
- **Failures after the rollout started.** A failure after the rollout began left the issue in Canary or Monitoring, or wrote a hold that named the wrong state. This covered a metrics read that failed, a promotion error, a lost lease, and a supervisor stopped mid-release. The LKG was not restored.
- **The synthetic suite.** A synthetic suite that could not be started, or whose run was cancelled, was judged as a failed check.
- **Restarts.** After a restart, the local deploy backend lost track of the application process it had started, and the next start of the LKG collided with it on the slot's port.
- **Reservation closes.** A budget reservation close that could never succeed was retried on every pass, for ever.

## Decision

- **A refused release blocks its issue.** A new lifecycle edge, ReleaseCandidate → Blocked on event `release_refused`, is unguarded like the other edges into Blocked (`pkg/core/statemachine/table.go`). The issue is blocked with cause `release_refused`, and the stop record keeps the refusal's code (`cmd/supervisor/handoff/run_stop.go`). The issue's summary names the code's meaning. `EXPIRED` says the receipt or the signing key expired. `APPROVAL_REQUIRED` says the change touches the trusted surface or a unit outside `release.units`. A verification failure says the candidate did not verify. The candidate's pull request is noted as refused, never as rolled back. Only a refusal that judged the candidate or the request blocks the issue: the codes `APPROVAL_REQUIRED`, `EXPIRED`, `SIGNATURE_INVALID`, `TAMPERED`, `GUARD_FAILED`, `INVALID_ARGUMENT`, `PERMISSION_DENIED`, `NOT_FOUND` and `REPLAYED`. A check that failed for any other reason, or whose context ended, judged nothing; the release does not begin and is tried again on the next pass. A candidate whose program is byte for byte the LKG's, as one that changes only tests or documentation, is refused with `ALREADY_EXISTS` after it verified, instead of being held. The lifecycle has no truthful way from ReleaseCandidate to Verified without a rollout, and a hold would only be resumed and held again. The summary says that the candidate builds the program already serving and that its pull request is merged by hand.
- **The supervisor is the control zone.** The task asked for the edge to be the supervisor zone's alone. The store has three zones (application, agent, control), and the supervisor writes as the control zone, so the edge is admitted to the control zone only. It is listed in `controlEvents` (`pkg/state/sqlstore/zone_ownership.go`) and in the PostgreSQL record trigger and row policies generated from those rules. Like every step from Claimed through Monitoring, the step needs the issue lease and the run the record names. The agent zone's role cannot take it, and the application zone writes no step.
- **Migration 10** (`0010_release_refusal_and_lease_steps`, backward compatible) installs the new step on PostgreSQL and a trigger on `leases`, `sisf_lease_step` (`pkg/state/sqlstore/lease_step_trigger.go`). The trigger holds every change of a lease to one of the writes the lease API makes:
  - insert at token 1;
  - take over by raising the token by exactly one under a named holder;
  - renew by the same holder;
  - give up by keeping the token.

  A zone role can no longer raise a token with plain SQL to meet the record trigger's rule without taking the lease. The trigger does not judge expiry, because the database does not share the store's clock. It does not bind holder names to zones either, because the lease API does not. On SQLite the migration is a no-op.
- **Any failure after the rollout starts restores the LKG and blocks the issue.** Restores and blocks are written in `internal/supervisor/release/restore_and_recovery_report.go`. The cause names what happened:
  - `rolled_back`: the rollback rules judged the release unhealthy.
  - `release_unjudged`: the release could not be observed or measured, or its synthetic suite could not be run.
  - `release_interrupted`: the supervisor stopped, an emergency stop took effect, a promotion failed, or the step could not be written.

  The lease is renewed, or taken back as the same holder, before the backend rolls back. A restore that rolled back but could not block the issue is finished on the next pass. A hold is written only for a release still in ReleaseCandidate.
- **An unwatched release is recovered at startup and on every pass.** A record in Canary, Monitoring or RolledBack is recovered when no release of this process drives it. Recovery runs after the LKG adoption, so the local backend runs the LKG again before anything is rolled back to it (`internal/supervisor/release/release_recovery.go`, `cmd/supervisor/stages/release_stage.go`, `cmd/supervisor/lifecycle_loop.go`). Recovery restores the LKG through the deploy backend, records RolledBack and then Blocked with cause `release_interrupted`, and writes the recovery report. A per-issue registry in the pipeline lets one goroutine drive an issue at a time. Recovery takes the issue lease, and the version check of each step means that one of two concurrent recoveries writes the steps and the other finds the record already moved. Within one process, the LKG is restored once.
- **A verified release is always finished.** The finish records the verified release as the LKG first, then writes the Verified step and, in the same transaction, a `finish-due/<run>` record of what the release still owes (`internal/supervisor/release/verified_finish_due.go`). The publication, the landing and the closing step follow, and each is safe to repeat:
  - the publication is recorded as done in that record, and the Publisher's pull request is found again by its branch rather than opened twice (`cmd/supervisor/stages/publication_pull_requests.go`);
  - the Lander decides a landing once;
  - the closing step deletes the record.

  Every pass resumes every Verified record (`FinishVerified`), whether its publication, its landing, the write of its LKG or an emergency stop held it, and whether the stop has been lifted since. A record an earlier build left Verified, with only `landing-due/<run>` or nothing recorded as due, is finished the same way. What finishing waits on is written to the candidate's pull request record as a pending landing with its reason, the way a landing that waits is shown in the issue's progress, and is logged once per change. The LKG is written again only when it does not name the release and no later release has replaced it, and only after the request is verified once more. Before the LKG is adopted at startup, a Verified release that the LKG does not yet name is recorded as the LKG, so a restart serves what the record says. A release left in Monitoring that was already recorded as the LKG is verified by the recovery from the running system instead of being restored to itself. Under an emergency stop that verification waits until the stop is lifted and is reported once, because verifying is a step forward; a rollback is not held.
- **A rollback never waits for the issue lease.** When recovery cannot take the lease of a release left in Canary, Monitoring or RolledBack, it restores the LKG through the backend anyway. This happens when an emergency stop of an earlier build holds the lease, or when a role took it over and keeps renewing it. The LKG is switched to once per release and process. The stage reports the wait once per change, and the report names the holder: a refused lease now carries its holder in the error details (`pkg/state/sqlstore/leases.go`). The RolledBack and Blocked steps are written by the first recovery that can take the lease. The emergency stop no longer holds the leases of issues in Canary, Monitoring or RolledBack (`internal/supervisor/killswitch/hold_claims_and_stop_runs.go`). Under a stop the supervisor moves those issues only back to the LKG, because the pipeline checks the stop before every forward step. The stop freezes forward progress and never blocks a rollback.
- **A RolledBack record keeps its cause.** The restore writes why it rolled the release back (`restore-cause/<run>`) in the transaction of the RolledBack step. A recovery that later blocks the issue reuses that cause and reason, so a release judged unhealthy is blocked as `rolled_back`, not as `release_interrupted`. Only a RolledBack record left by an earlier build, which kept no cause, is blocked as interrupted.
- **The Owner's retry returns these blocks to Ready.** The retry guard admits `release_refused`, `release_interrupted` and `release_unjudged` on the Owner's authorization alone, as it does `rolled_back` (`pkg/core/statemachine/retry_guard.go`). A rollback block written before its stop record existed is judged from its recovery report (`cmd/factoryd/wiring/owner_retry.go`). A second retry of the same block is refused with `INVALID_TRANSITION`, and a non-Owner is refused. The retry of a held build or release now reports a hold record it cannot read, such as `TAMPERED`, instead of going on as if nothing were held.
- **The synthetic suite tells a run that judged nothing from a failed check.** A program that cannot be started, or a run whose caller stopped it, is an error. Only a run that outlasts its own time limit is a failed run (`cmd/supervisor/synthetic_program.go`). The supervisor refuses to start when `--synthetic-program` is missing, not a regular file, or not executable. Before a low-traffic release deploys anything, the supervisor asks the suite whether it can run, and holds the release when it cannot. During the rollout, a suite error restores the LKG as `release_unjudged`.
- **The local backend records its processes.** For each slot it writes the pid, the process start time, the command line, the port and the release to `<trust>/local-slots/<slot>.json` (`adapters/deploy/local/slot_records.go`). Before the supervisor starts the LKG at startup, it stops the recorded processes the backend does not run itself. A pid is signalled only while `ps` still reports the recorded start time for it; the start time survives the `exec` by which the installed launcher becomes the release's program, and the command line is kept for the log only. Otherwise the record is dropped without a signal. No process is matched by name or pattern. A process that ignores `SIGTERM` is killed after five seconds. When a process does not leave, the LKG is not started over it.
- **A reservation close that can never succeed is abandoned once.** A close that fails with `NOT_FOUND`, `TAMPERED` or `INVALID_ARGUMENT` is counted. After five such failures, the run's close-due entry is replaced by `close-abandoned/<run>`, which holds the code and the reason, and the pass reports it once. A close that fails for a passing reason is retried on every pass and never counted (`cmd/supervisor/stages/run_close_due.go`).
- **The protected Go packages are the trusted binary's dependency closure.** The Go part of `DefaultProtectedPaths` is no longer kept by hand. `go generate ./internal/supervisor/release/` writes every directory of this module that `./cmd/supervisor` links, on Linux and macOS, amd64 and arm64, with and without cgo, to `trusted_go_package_dirs_generated.go` (`internal/supervisor/trustedclosure`). There is no separate QA evaluator binary: the evaluator runs inside the supervisor. Each directory covers everything below it. A test computes the closure again with `go list -deps` and fails when the checked-in list is stale. The hand-kept entries stay for what is not a linked Go package: the Dockerfiles, `templates/`, `.sisf/`, the foundation files, `docs/adr/`, the synthetic suite, the installer, the CLI and `.github/`. `internal/supplychain/`, `internal/redact/`, `build/runner.Dockerfile` and `build/qa-runner.Dockerfile` are among them. A candidate that touches any of these paths is refused with `APPROVAL_REQUIRED` and its issue is blocked before anything is deployed. The edge (`cmd/edge/`, `build/edge.Dockerfile`) and `go.mod` and `go.sum` are protected by hand too: the edge decides which release receives traffic, and a changed dependency version changes what every trusted binary links without touching one of its directories.
- **The supervisor links no part of the control unit.** It reads the simulation profile, the GitHub settings and the store directory through `internal/manifestsettings`, never through `cmd/factoryd`, and a test fails if its closure contains `cmd/factoryd/`, `ui/`, `internal/control/`, `internal/mcp/` or `internal/oauthas/`. The dashboard therefore stays outside the trusted surface, and a non-sensitive Zone 3 page remains self-publishable.
- **Identity and access code is protected by hand.** `internal/auth/`, `internal/rbac/`, `internal/oauthas/` and `pkg/identity/` decide who the Owner is and who may approve. A factory that rewrote them could grant itself the approval a governance change needs. The supervisor does not link them, so the generator cannot find them, and they are a short named list beside the generated closure. Nothing else in Go is hand-kept that the generator can derive.
- **Code only the control unit links is a build of the control unit.** `go generate ./cmd/supervisor/stages/` writes the directories `./cmd/factoryd` links and the application (`./examples/hello-app/...`) does not to `control_unit_package_dirs_generated.go`. `DefaultUnitRouter` routes those directories to `control` unless they are protected. Everything else goes to `app`. A change to `internal/mcp/` or `ui/` therefore deploys and observes the unit that runs it, and the read-only dashboard stays self-publishable through `control`. A test fails when the list is stale, or when a package factoryd links but the application does not routes to `app`.
- **Unchanged:** the QA pass mark, the hard gates, `SelectImageBuilder`'s refusal of an untrusted build without an isolating backend, the supervisor boundary, the Docker filter, and the supervisor-only merge.

## Consequences

- No release waits in ReleaseCandidate, Canary or Monitoring without a supervisor driving it. Every refusal and every undo ends in Blocked with a cause, a public summary and the Owner's way out. A retry spends agent budget on a new run, and the refused candidate itself is never tried again.
- An interrupted release is rolled back even when it might have been healthy. The summary says that nothing is known about its health.
- **Recovery across processes.** The registry and the lease narrow the race between two supervisor processes of one factory, but do not close it:
  - the lease re-grants itself to the same holder name, which is what makes a restarted supervisor take over at once;
  - a second process with the same name could therefore also call the backend's Rollback before the version check refuses its steps.

  One supervisor per factory is the supported setup. A rollback to the LKG is idempotent on both backends.
- The leases trigger stops a zone role from forging a token. It does not stop a role from taking over a lease that is still live, because the trigger does not judge expiry.
- The slot records are on the trust volume. In Standalone mode the agent's operating-system user can write that volume, so it could forge a record. The start time check means a forged record can only name a live process whose start time it gives exactly, which that user could signal anyway.
- A stale process is stopped only at startup adoption. A backend that was never given a state directory keeps nothing on disk, as before.
- An abandoned reservation stays held until a person settles it. The record and the one-time report say which one.

## Alternatives considered

- **Keep a refused release in ReleaseCandidate with a final hold.** This was the previous behaviour. Rejected by the Owner, because the issue could never move again without hand repair of the store.
- **Retry a refused release in place.** Rejected. An expired receipt or a governance refusal does not change by waiting, and resuming the same candidate would sidestep the requirement for a new QA judgment.
- **Let a restarted supervisor resume watching an unwatched release.** Rejected. The watch window, its samples and the synthetic runs of the interrupted release are lost, so the release cannot be judged on evidence. Restoring the LKG is the safe direction.
- **Find stale processes by port or program name.** Rejected. A pattern or port match could stop a process the supervisor did not start.

## Acceptance test

- `pkg/core/statemachine/retry_guard_test.go`: `TestABlockedReleaseIsRetriedOnTheAuthorizationAlone`. The transition matrix, edge, lease and fixture tests count the new edge.
- `pkg/state/sqlstore/release_refusal_step_test.go`, on SQLite and PostgreSQL:
  - `TestOnlyTheControlZoneBlocksAnIssueWhoseReleaseWasRefused`;
  - `TestTheReleaseRefusalMigrationAdmitsTheNewStepToTheControlZoneOnly`;
  - `TestPostgresReleaseRefusalMigrationUpgradesAStoreWithRecordsInEveryState`.
- `pkg/state/sqlstore/lease_step_trigger_test.go`: `TestPostgresEveryZoneTakesRenewsAndGivesUpLeasesThroughItsOwnRole` and `TestPostgresAZoneRoleCannotForgeALeaseToken`.
- `internal/supervisor/release/release_recovery_test.go`:
  - `TestARestartedSupervisorRestoresTheLKGOfAnUnwatchedRelease`;
  - `TestConcurrentRecoveriesOfOneReleaseRestoreTheLKGOnce`;
  - `TestARecoveryFinishesARestoreThatCouldNotBlockTheIssue`;
  - `TestARolloutThatCannotGoOnIsRestoredAndBlockedNotHeld`;
  - `TestAReleaseStoppedWithItsSupervisorIsRecoveredAtTheNextStart`;
  - `TestARecoveryLeavesAReleaseThePipelineDrivesAlone`.
- `internal/supervisor/release/low_traffic_synthetic_hold_test.go`: the suite held before the rollout, restored as unjudged during it, and blocked as rolled back on a failed check.
- `cmd/supervisor/synthetic_program_test.go`: `TestTheSyntheticProgramMustBeAnExecutableAtAnAbsolutePath` and `TestASyntheticRunThatJudgedNothingIsAnError`.
- `adapters/deploy/local/slot_records_test.go` and `cmd/supervisor/lkg_adoption_test.go`: a real process left by an earlier backend is stopped, including one its launcher `exec`ed into, a record whose pid names another process is dropped without a signal, and adoption stops stale processes before it stages.
- `cmd/supervisor/stages/run_close_due_bound_test.go`: abandoned once with its reason, including an entry in the earlier format, and never for a passing failure.
- `internal/supervisor/release/governance_change_refused_test.go`: the four new protected paths.
- `internal/supervisor/release/trusted_go_packages_protected_test.go`: `TestTheProtectedGoPackagesAreTheTrustedBinariesClosure`, `TestEveryPackageTheSupervisorLinksIsProtected`, `TestACandidateThatChangesOnlyALinkedPackageIsRefusedForApproval` and `TestAnOrdinaryApplicationChangeIsStillReleasedAndMerged`.
- `cmd/supervisor/trusted_dependency_boundary_test.go`: `TestTheSupervisorLinksNoPartOfTheControlUnit`.
- `internal/supervisor/release/identity_and_access_protected_test.go`: `TestACandidateThatChangesOnlyIdentityOrAccessCodeIsRefusedForApproval`.
- `internal/supervisor/release/edge_and_dependencies_protected_test.go`: `TestTheEdgeAndTheModulesDependenciesAreProtected`.
- `cmd/supervisor/stages/control_unit_routing_test.go`: `TestTheControlUnitsDirectoriesAreGeneratedFromWhatItLinks` and `TestCodeOnlyTheControlUnitLinksIsABuildOfTheControlUnit`.
- `tests/acceptance/release_hold_visibility_test.go`:
  - `TestARefusedReleaseBlocksItsIssueUntilTheOwnerRetriesIt`;
  - `TestAHeldReleaseResumedAfterItsReceiptExpiredBlocksItsIssue`;
  - `TestTheRetryOfAReleaseWhoseHoldCannotBeReadSaysSo`.
- `tests/acceptance/exhausted_run_blocked_test.go`: `TestTheRetryOfALaterRollbackIsJudgedByItsOwnRecord`.
- `internal/supervisor/release/verified_release_finished_after_failure_test.go`: a failed publication, a failed write of the LKG, an emergency stop that is later lifted, a restart between the verification and the landing, a release already recorded as the LKG but not verified, a record left by an earlier build, and an LKG that a later release replaced. Each one ends Closed, merged once.
- `tests/acceptance/verified_release_publication_outage_test.go`: `TestAVerifiedReleaseWhosePublicationFailedIsFinishedByALaterPass`.
- `cmd/supervisor/program_retention_test.go`: `TestTheProgramOfAFinishedReleaseGoesOnceItIsNoLongerTheLastKnownGood`.
- `tests/acceptance/release_recovery_after_restart_test.go`: `TestAReleaseLeftInCanaryByAStoppedSupervisorIsRestoredAndBlocked` and `TestAReleaseLeftInCanaryIsRolledBackWhileAnotherHolderKeepsItsLease`, through the supervisor's own pass.
- `internal/supervisor/release/recovery_without_the_issue_lease_test.go`, `internal/supervisor/killswitch/release_in_flight_not_held_test.go` and `pkg/state/sqlstore/leases_test.go` (`TestARefusedLeaseNamesItsHolder`): a rollback while another holder or an emergency stop keeps the lease.
- `internal/supervisor/release/same_program_as_serving_refused_test.go`: `TestACandidateThatBuildsTheServingProgramIsRefusedNotHeld`; `cmd/supervisor/handoff/release_hold_test.go`: `TestTheBlockOfACandidateThatBuildsTheServingProgramSaysToMergeItByHand`.
- `internal/supervisor/release/rolled_back_cause_kept_test.go`: `TestARolledBackReleaseIsBlockedWithTheCauseItsRestoreJudged`.
- `internal/supervisor/release/unverified_candidate_refused_test.go`: `TestAReleaseWhoseChecksWereCutShortIsTriedAgainNotRefused` and `TestOnlyAJudgedRefusalBlocksTheIssue`.
