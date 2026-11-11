# ADR 0022: A Standalone release runs the candidate's own program

## Status

Status: proposed. The Owner chose this direction on 2026-11-11 ("deploy exactly the candidate") and accepted its consequence, stated below; the ADR records it for review before it is accepted.

## Context

In Standalone mode the application runs as a host process the local deploy backend starts. Until now that backend started the program `sisf up` installed for every release, whatever digest the release named: a candidate was built in the sandbox, tested, digested and judged by QA, and then the installed program was started and reported the candidate's digest as its revision. The release pipeline watched, verified and promoted a program that did not contain the change, and the candidate's pull request was merged on that evidence. A release could not fail because of what the candidate changed.

The digest a sandboxed build reported was also only a claim: one line of the report phase's output, bound to no bytes the supervisor held.

## Decision

- **The digest names bytes the supervisor received.** Where the local backend deploys sandboxed builds, the report phase of the build command exports the unit's one program. The supervisor tells it the target (`SISF_REPORT_TARGET_OS`, `SISF_REPORT_TARGET_ARCH`: its own), and the program arrives on the report's nonce-tagged lines (`sisf-report <nonce> program: <base64>`, any number of lines, each decodable by itself). The supervisor hashes what it receives as it arrives and accepts the build only when the reported `image-digest` is the sha256 of exactly those bytes. A missing, malformed, oversized (256 MiB) or mismatching program is the build command's fault: an outage that spends no attempt, never a candidate under another digest (`cmd/supervisor/stages/report_reader.go`).
- **The bytes leave the sandbox only through that output.** Nothing is copied out of a container and no directory is shared with it. The Docker filter's allowlist is unchanged: archive, exec, attach and binds stay refused.
- **A content-addressed store on the supervisor's trust volume keeps the programs** (`<data>/trust/programs/sha256-<hex>`, directory `0700`, files `0500`; `internal/supervisor/programstore`). A file is hashed again every time its path is asked for, so a file that changed after it was received is refused (`TAMPERED`) instead of started.
- **The local backend starts the program of the release's digest or nothing.** A digest the store holds starts that file. The digest the root-signed release manifest pins starts the installed program. Any other digest is refused; the installed program is never started under a digest that is not its own (`cmd/supervisor/local_programs.go`).
- **A promotion survives restarts.** The supervisor's record of the promoted release already survived its restart; it now resolves to the store's file. `sisf up` keeps that record when the store still holds its program unchanged, and otherwise drops it and says the application starts from the installed release again (`internal/install/standalone_promoted_program.go`).
- **The store keeps what can still run.** After each round the supervisor removes every stored program that is neither the last known good release nor the candidate of a run between QA and the end of its release, a Verified release included until it closes. The program of a finished release therefore goes once it is neither (`cmd/supervisor/program_retention.go`).
- **Unchanged:** the simulation (its scripted agent builds nothing, so every release starts the installed program), Docker mode (the Compose backend runs images), the QA pass mark, the hard gates, `SelectImageBuilder`'s refusal of an untrusted build without an isolating backend, and the supervisor-only merge.
- The local backend moves all traffic to the new slot in one step. A Standalone release is watched and rolled back; it is not a canary rollout with a traffic share.

## Consequences

- **Accepted by the Owner:** code an agent wrote, once it passed QA, runs as a host process with the rights of the application unit. In Standalone that is the operating-system user of the whole factory. The build is isolated; the released program is not. This is what `trusted_code_only` already states for the mode, now also true of what is deployed: Standalone is for trusted code only.
- A release can now fail on the candidate's own behaviour, and is then rolled back to the last known good release with the pull request left unmerged.
- A build command must speak the extended report phase where the local backend deploys builds; one that exports nothing is held as an outage with a message that names the key. A unit is one program: the smoke run's build command refuses a candidate with more than one `main` package.
- Programs are built for the supervisor's operating system and architecture, statically (`CGO_ENABLED=0`) in the smoke run's command. A unit that needs more than one file to run cannot be deployed this way.
- The synthetic suite stays the one `sisf up` built from the factory's own source, never from a candidate.
- The store is on the trust volume, which the agent's operating-system user can write in Standalone. Hashing before every start detects a changed file; it does not stop that user from stopping or replacing the running process.

## Alternatives considered

- **Keep starting the installed program.** No agent-written code runs on the host outside a sandbox. Rejected by the Owner: a release then proves nothing about the candidate, and a merge follows a verification of another program.
- **Deploy candidates only in Docker mode.** The Compose backend runs the candidate's image in a container, which keeps the isolation. Standalone would hold every agent-written candidate, as it did before the build sandbox existed, so the mode could not finish an issue with a real agent. Rejected for this release.

## Acceptance test

- `internal/supervisor/programstore/store_test.go`: programs are kept under the digest of their bytes, a changed file is refused, only the kept digests survive a sweep.
- `cmd/supervisor/stages/sandbox_build_program_export_test.go`: the image digest is the digest of the received program; a claimed digest of other bytes, a missing, malformed or oversized program is an outage that keeps nothing; without a store the protocol is as it was.
- `cmd/supervisor/local_programs_test.go`: the store's file for a stored digest, the installed program for the pinned digest, nothing otherwise; a changed file is not started; the simulation is unchanged; a restarted supervisor starts the promoted program.
- `cmd/supervisor/program_retention_test.go`: programs that are neither the last known good release nor in flight are removed.
- `internal/install/standalone_promoted_program_test.go`: the launcher starts the program it is given; `sisf up` keeps a promotion whose program is in the store.
- `tests/smoke/build_candidate_script_unit_test.go`: the smoke run's build command exports the program its digest names and refuses two programs.
- `tests/smoke` under `SISF_SMOKE_REHEARSAL=1`, against the real sandbox repository without a paid model: a good candidate is served under its own digest before its pull request is merged and still after restarts; with `SISF_SMOKE_REHEARSAL_CANDIDATE=bad` a candidate that fails the synthetic suite is rolled back and left unmerged. Since a unit releases only the paths it consumes (ADR 0023), the sandbox repository's candidates, which change files at its root, are refused for the Owner before anything is deployed; the rehearsal proves this again only once that repository moves its application to `examples/hello-app/` or the allowlist becomes a per-repository setting.
