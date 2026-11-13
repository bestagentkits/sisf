# ADR 0024: A build is accepted only on bytes the supervisor received

## Status

Status: proposed. Taken by the session that resolved issue #35, acting for the product owner under the rule "evidence the factory relies on comes from a source the candidate cannot write, or is refused"; recorded for the Owner's review before it is accepted.

## Context

A sandboxed build runs the build command once per phase (`cmd/supervisor/stages/image_builders.go`). The candidate's code runs in the test and build phases; the report phase, tagged with a nonce made after the candidate's code last ran, names the image digest, the image, the SBOM and the provenance. Where the deploy backend runs programs (Standalone, [ADR 0022](0022-standalone-deploys-the-candidates-own-program.md)) the report also exports the program, and the digest must be the sha256 of the bytes the supervisor received.

Where the deploy backend runs images (Docker mode, the Compose backend) the supervisor received nothing. The digest, the image and the SBOM and provenance references were one line each of the report, accepted as written:

- An image a build makes in a sandbox can only leave it through a registry or a store the sandbox reaches. The test and build phases, where candidate code runs, reach the same places, so a digest the report phase finds there (the protocol's own comment suggested "asking the registry it was pushed to") is one the candidate can choose.
- The supervisor has no way to look: neither Docker request filter lets it read an image, and a build holds no registry credential.
- The deploy filter starts only the images the Owner installed. A report that named the installed application image would be staged, watched and verified as the candidate, and the candidate's pull request merged on a release of a program that does not hold its change, the defect ADR 0022 removed from Standalone.

## Decision

- **A sandboxed build is accepted only on the program the supervisor receives from it.** `SandboxImages` without a program store refuses every build before any phase runs, with `NOT_SUPPORTED`, the configuration's fault and never the candidate's (`unobservedImagesRefused`).
- **The supervisor refuses at start to select such a builder** (`SelectImageBuilder`), so no issue is taken up only to be held.
- **Docker mode outside the simulation runs no stages.** `sisf init` and `sisf up` start its supervisor without the lifecycle flags, and `sisf doctor` warns under the check `build evidence` with the reason (`internal/install/compose_render_lifecycle.go`). The simulation is unchanged: its scripted agent builds nothing and its candidates run as the installed image.
- **The report phase always exports the program** and is always told the supervisor's target; the protocol no longer has a variant without one. Its comment no longer suggests reading a registry the earlier phases can write.

## Consequences

- Docker mode builds, evaluates and releases no agent-written candidate until the supervisor can observe what it deploys. It did not release one before either: the deploy filter refuses every image but the installed one, so the only builds it could stage were the forged kind above.
- A Standalone install with a build sandbox is unchanged.
- **Not proven by the program digest:** that the program was built from the commit. That rests on the report phase running no candidate code and building the commit it is told, in a fresh container, The SBOM and provenance references were that phase's word too; the supervisor now makes and holds both documents itself ([ADR 0025](0025-release-evidence-is-what-the-supervisor-measured-and-made.md)).

## Alternatives considered

- **Check the reported digest against the registry or the Docker engine.** It proves only that some image has that digest, which a candidate that can push to the registry controls; it needs an image route in a Docker filter or a registry credential the supervisor does not hold. Rejected for this release; it is the shape a future image mode would take, together with a deploy filter that admits verified candidate images.
- **Keep image mode and trust the report.** Rejected: that is the forgery.

## Acceptance test

- `cmd/supervisor/stages/sandbox_build_unobserved_image_test.go`: a build without a program store is refused before any phase runs and never charged to the candidate; the builder is refused at selection.
- `cmd/supervisor/standalone_build_sandbox_route_test.go`: a factory that keeps no programs builds nothing; one that keeps programs is given the store.
- `internal/install/zone_isolation_topology_mutations_test.go`, `internal/install/doctor_topology_test.go`: a production Docker install runs no stages, and the doctor says why.
