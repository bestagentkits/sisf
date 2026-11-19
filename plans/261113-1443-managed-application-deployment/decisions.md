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

# Implementation decisions (2026-11-16, decided by the factory with advisor counsel)

F. Secrets reach a revision as files only
- The Owner provisions each secret as a file of one secrets volume. The backend mounts it read-only at `/run/secrets`; the Spec maps `NAME` to a secret and the revision gets `NAME_FILE=/run/secrets/<secret>` (`Runtime.RevisionEnv`). No value enters the Spec, the environment, the factory or a log.
- The deploy filter's data volumes carry a read-only flag and match it exactly: the secrets volume mounted writable, or any undeclared volume, is refused (negative tests in `internal/dockerfilter`).

G. Bundles are stored and streamed by digest
- `appbundle.Store` keeps verified bundles as `<hex>.tar` in the supervisor zone. `Put` stores only what `Verify` accepted; `Open` hashes again while streaming and ends a mismatching stream with an error instead of EOF, so the loader upload fails and the half-loaded volume is removed. A tampered file never reaches a revision.

H. Stored releases keep verifying
- Image-only artifacts keep signing payload version 2 byte for byte; only bundle artifacts use version 3. The release pipeline re-verifies stored LKG artifacts, so a new payload for every artifact would have invalidated every existing LKG signature.

I. No factory write on the live database
- Canary and synthetic checks against live revisions are read-only. A check that writes (the purchase) runs against a disposable copy restored from the live database's latest backup, served by the live revision. That is also how `checkout_broken` on the live shop is detected: the fault lives in the database, so the copy carries it. The hourly probe thus also proves each backup restores.
- Not covered: a fault that only shows with the live database or live traffic (a payment provider outage). Recorded as a known gap.
- The smoke test's purchase through the edge stands for a customer, to prove orders placed between releases survive a rollback; it is not a factory check.

J. Sensitive changes are judged supervisor-side
- `pkg/managedapp/changegate` judges the candidate's changed paths and lockfile against the approved Spec and lock: the Spec, any protected path (prefix-aware, so `plugins/pay/*` covers its subtree) and any lock change the baseline policy does not make `auto` are the Owner's. Core and added components are always the Owner's. What the gate cannot read is the Owner's.

K. The real release path is proven on Docker before 0h
- `examples/woocommerce-app/bin/release-smoke.sh` drives the real deploy filter, Compose backend, edge and release pipeline over the trial shop: install LKG, good update (WooCommerce 11.2.2) by canary, failing update rolled back, original orders checksum unchanged, backup restored into a copy that matches.
- The smoke's monitor windows run on the test clock with real traffic sent through the edge each window (400 requests, 15 minimum samples). That calibrates the smoke only, not the production defaults.

L. 72h topology: the supervisor and edge run natively on the Mac
- The supervisor, edge and deploy filter run as host processes; revisions and the shop's database run in OrbStack. The supervisor reaches a revision at `<container>.orb.local` (`--deploy-host-suffix .orb.local`); inside a Docker install, without the flag, it reaches it by name on the Compose network as before. Reason: the Mac is the Owner's chosen host for the 72h run, and this is the path the release smoke already proved.

M. One derivation of the Spec configures both the filter and the backend
- `managedapp.Spec.Revision(Binding)` derives the revision once: image, port, user, environment, bundle mount, data volumes bound to the install's volumes, and the secrets volume read-only. `compose.Config.UseRevision` and `dockerfilter.Config.AdmitRevision` read that same value, and `sisf install docker-filter` and the supervisor take the same `--app-spec`, `--app-volume`, `--app-secrets-volume`. A parity test stages the shop through a filter built from the same Spec, and a revision that drifts by one entry (a writable secrets volume, an extra environment entry, another volume) is refused.
- The Spec path must be absolute: it is the Owner's installed file, never one inside a candidate's checkout. What the compose backend cannot honour (a runtime command, a health path other than `/healthz`) is refused, not dropped.
- With `--app-spec` the release pipeline observes a release as the pair of the Spec's image and the bundle bytes on the trust volume (`appbundle.PairObserver`), and judges a low-traffic release by the Spec's checks that do not write (health, home, catalogue), run against the revision itself. The purchase is never sent to a live revision. `--synthetic-program` is refused beside `--app-spec`, so a release has one judge.
- Not yet wired: a candidate build that yields a bundle. Until it is, a factory with `--app-spec` refuses every build as it refused before (it keeps no programs), and the change gate is not yet on the release path. Both are the next step; see the PR.
