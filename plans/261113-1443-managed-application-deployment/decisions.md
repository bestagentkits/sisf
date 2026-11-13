# Design decisions (orchestrator / product owner, 2026-11-13)

A. Runtime image + content bundle — approved with conditions
- Bundle assembled by trusted code as pure packaging; no candidate code runs (no composer/npm/install scripts/hooks). Pinned archives fetched and sha256-checked by trusted code, not the candidate.
- Supervisor unpacks a candidate-influenced tar: refuse (never sanitise) `..`, absolute paths, symlinks, hardlinks, device/special entries, size bombs. Mounted tree = exactly the tree the digest covers, not writable after hashing.
- Docker filter enforces the mount rule: one digest-named source, read-only, fixed target, deploy role only. Prefer a read-only named volume over a bind if tighter / if the Docker-mode supervisor cannot name a host path.
- Artifact identity = (runtime image digest, bundle digest); QA receipt, release record, rollback bind the pair.
- Runtime image change (Core upgrade) = Owner manifest change.

B. Approval via protected paths — approved with corrections
- Bump classification (patch vs major, new plugin, new permission) runs supervisor-side against the Owner-approved baseline, never candidate-side.
- Manifest read from the Owner-approved baseline, never the candidate tree. Manifest, lockfile and protected-pattern list are themselves protected. Patterns additive only.
- Docs: a sha256 in a candidate-edited lockfile proves integrity, not authenticity.

C. Starting work — redirected
- No script applies `sisf:ready` (bypass). Add a manifest-declared auto-start class, default off, applied by trusted intake only: a detected regression of Owner-authorised behaviour (declared health/synthetic check that passed on the last verified release and now fails), never when the fix touches a protected path.
- Issue body = supervisor-structured facts only (check, expected, observed status/latency, revision); never raw logs or request text.
- Report MTTR with the class on; report separately cases that waited for the Owner.

D. Database as Owner-declared application dependency — approved, plus
- A release carrying a DB migration cannot be auto-canaried; sensitive class: Owner approval + verified backup first.
- QA purchase flows run against a disposable seeded copy, never the live DB.
- Backup credentials stay in the supervisor zone; never agent/QA zones. Restore never automatic. ADR required.

E. Ownership and sequencing
- Do not edit `internal/supervisor/release/governance.go` while continuous-release is in flight; feed patterns through configuration governance already reads, else ask with exact lines.
- `cmd/supervisor/stages`, `internal/install`, install docs: wait for the build-evidence follow-up (#58/#59) to land, rebase, then touch. Meanwhile: trial app repo, seed, adapters/deploy/compose, tests.
- PR must NOT merge before the v0.1.0-alpha tag; lands after with its own independent security review. 72h observation may run from the branch.
