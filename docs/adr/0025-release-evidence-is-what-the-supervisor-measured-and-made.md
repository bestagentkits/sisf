# ADR 0025: Release evidence is what the supervisor measured and made

## Status

Status: proposed. Taken by the session that resolved issues #58 and #59, acting for the product owner under the rule "evidence the factory relies on comes from a source the candidate cannot write, or is refused"; recorded for the Owner's review before it is accepted. It builds on [ADR 0024](0024-a-build-is-accepted-only-on-bytes-the-supervisor-received.md).

## Context

Two checks of a release read as verification but compared a claim with itself.

- **The tampered-artifact check (#58).** `supplychain.VerifyArtifact` refuses an artifact as `TAMPERED` when `observed`, the digest of what was fetched, differs from the digest it was signed for. Both callers in the release pipeline passed the artifact's own digest as `observed`, so the check could never fire.
- **The SBOM and provenance (#59).** The report phase of a sandboxed build named an SBOM and a provenance reference. The supervisor never received, kept or checked the documents they named. The observed-build hard gate passed any non-empty strings, and the supervisor then signed a deploy artifact carrying them.

## Decision

- **A release is verified on bytes the supervisor measures.** The release pipeline takes an `ArtifactObserver` (`internal/supervisor/release/dependencies.go`); a pipeline without one is refused. Before an artifact is accepted, and again before a verified release becomes the last known good, the observer returns the digest of what the deployment backend would start, and `VerifyArtifact` compares that digest with the signed one.
  - Where programs are kept (Standalone), the observer hashes the program store's file now (`programstore.Store.Measure`, `localPrograms.ObserveArtifact`). The installed release is measured too: the observer hashes the installed program's file, which `sisf up` names with `--deploy-local-installed-program`, and without that file it is `NOT_FOUND`, never the digest the root-signed release manifest gives. Anything else is `NOT_FOUND`.
  - In the simulation with the scripted agent, whose builds make no program, `stages.SimulatedArtifacts` returns the artifact's digest. It is selected only where `SimulatedImages` is. Any other factory has no observer and runs no lifecycle.
  - A verified release whose program measures as other bytes stays `TAMPERED` (blocked as not judged), and is no longer relabelled as a signature hold.
- **The supervisor makes a build's SBOM and provenance itself.** It makes them from what it observed (`supplychain.MakeBuildDocuments`): the program it received, the commit it asked to be built, the run, and its own clock. It keeps both documents in the build record (`handoff.BuildRecord.SBOM`, `.Provenance`). Each reference is the sha256 of its document's bytes.
- **The SBOM lists only what the supervisor read.** The modules come from the Go build information of the received program (`debug/buildinfo`), up to 1 MiB of it: the program was built from the candidate's code, so larger build information is left out and the SBOM says why. A program with none, including any non-Go program, is described without modules: the SBOM says `unknown:` and why, and never lists a module as known. The same applies to the simulation, which builds no program. Units are therefore not required to be Go programs.
- **The observed-build hard gate checks the documents** (`supplychain.CheckBuildDocuments`). It checks that:
  - each reference is the digest of a document the record holds;
  - the SBOM describes this program;
  - the provenance covers its digest, names its commit and records that SBOM.

  A reference that names no held document fails the gate. An SBOM that lists no module passes, and the gate's detail, which the QA receipt records, says the modules are unknown and why (`supplychain.ModulesUnknown`), so a passed gate never reads as a full list of modules.
- **The report phase names no SBOM and no provenance.** `sbom` and `provenance` leave the protocol, and a report that still names them is an outage of the build command, held for the Owner.

## Consequences

- A program swapped in the store after QA judged it is never released, and is never recorded as the last known good.
- A build command that speaks the earlier protocol is held until its report drops the two keys (`tests/smoke/sandbox/build-candidate.sh` is updated).
- The release pipeline changed only at its two verification call sites and its configuration.
- The build phase is told the target it builds for (`SISF_TARGET_OS`, `SISF_TARGET_ARCH`), so a candidate that does not compile for it is the candidate's failure, not an outage of the report phase.
- Docker mode, which builds and releases nothing since ADR 0024, refuses `--build-command` and `--qa-agent-binary` at `sisf init` and `sisf up` instead of taking them in silence, and its doctor check is always `build evidence`.
- **Not covered:**
  - **The staged digest is an echo.** The `staged.Digest` compared in `start()` is still the artifact's own digest echoed by the backends. What a local start runs is still re-hashed by the program store before it starts.
  - **The installed release's artifact is a label.** It names its origin (`lkg/<source>:<digest>`), not documents, and is not gated by the observed-build gate.
  - **The SBOM is limited to Go build information.** It does not prove that the program was built from the commit; that rests on the report phase, as in ADR 0024.

## Alternatives considered

- **Remove the `observed` parameter and point at the store's re-hash before each start.** Rejected. It verifies only at start, after QA and staging decisions, and leaves the LKG recorded on unmeasured bytes.
- **Refuse non-Go programs.** Rejected. Labelling unread modules is honest and keeps units backend-neutral. Refusing would turn a configuration choice into an outage of every build.
- **Keep the report's references and check that the documents exist in a registry.** Rejected. The supervisor holds no registry credential, and the phases where candidate code runs can write what the sandbox reaches.

## Acceptance test

- `internal/supervisor/release/artifact_observed_bytes_test.go`: a replaced or missing program is never staged; a verified release whose program was replaced never becomes the LKG; a pipeline without an observer is refused.
- `cmd/supervisor/local_programs_test.go`: the observer measures the store's bytes and the installed program's file, and only programs or the simulation observe.
- `cmd/supervisor/stages/build_info_size_limit_test.go`: build information larger than the bound is left out of the SBOM.
- `internal/supplychain/build_documents_test.go`: the documents hold only for the build they describe, the SBOM lists only modules the supervisor read, and an SBOM that does not say where its modules came from counts as unknown.
- `cmd/supervisor/stages/observed_build_gate_documents_test.go`: bare references fail the gate, and the supervisor's own documents pass it once stored, with a detail that says when the modules are unknown.
- `tests/acceptance/candidate_build_supply_chain_documents_test.go`: a record without documents fails the gate whatever the score, a sandboxed build is released on the supervisor's documents and its receipt says its modules are unknown, and a report that still claims an SBOM and provenance is held.
