# ADR 0026: A managed application is a runtime image and a content bundle

## Status

Status: proposed. Taken by the session that works on issue #66 (managed application deployment), with the design decisions the orchestrating session made for the product owner on 2026-11-13; recorded for the Owner's review before it is accepted. The change lands after the v0.1.0-alpha tag, with its own security review.

## Context

The factory releases what it can observe and undo ([ADR 0022](0022-standalone-deploys-the-candidates-own-program.md), [ADR 0024](0024-a-build-is-accepted-only-on-bytes-the-supervisor-received.md)). Today that is a Go program built in a sandbox. Most applications an Owner already runs are not: a WordPress shop is an interpreter image plus PHP files, plugins pinned by version, a database and an uploads folder.

Taking such an application over raises five questions the existing contract does not answer:

- What is the artifact? Building a new image per candidate needs a registry the candidate's phases could write, which ADR 0024 rejected.
- Who decides whether a change is safe? A candidate can edit its own lockfile, manifest and checks.
- How does a regression become work without a human applying `sisf:ready`?
- Where does the database live, who backs it up, and who restores it?
- How do checks describe a purchase flow without turning response text into instructions for an agent?

## Decision

- **The Owner describes the application in `sisf-app.yaml`** (`pkg/managedapp`), written by `sisf adopt` from what is already there (a Compose project or a Go program) and committed by the Owner. The factory reads it from the Owner-approved branch, never from a candidate tree. Parsing is strict: an unknown key, a second document or an unpinned image is refused. Adoption never copies a secret value. A secret is a Compose secret file; a secret-like variable in plain environment is refused by name, without its value.
- **The artifact is the pair (runtime image digest, bundle digest).**
  - The runtime image is pinned by digest in the manifest. Changing it, a Core upgrade for WordPress, is an Owner manifest change.
  - The bundle is the application's content (`pkg/appbundle`), assembled by trusted code as pure packaging. It is built either from a lockfile of pinned archives that trusted code fetches and checks by sha256, or from a program built in the existing sandbox. No candidate code runs while it is assembled: no installer, composer, npm or hook.
  - The tar is deterministic. Unpacking refuses, and never sanitises: `..`, absolute paths, symlinks, hardlinks, devices and size bombs.
  - The tree mounted is exactly the tree the digest covers. It is mounted read-only from a digest-named volume at a fixed target, by the deploy role only.
  - QA receipts, release records and rollback bind the pair. Only a bundle artifact signs the bundle (payload version 3); an image-only artifact keeps version 2, so a stored last-known-good release still verifies.
  - The supervisor stores a bundle only after verifying it, by digest, and hashes it again as it streams it into the volume. A stored file that no longer matches fails the upload, and the partly loaded volume is removed.
- **Secrets reach a revision as files only.** The Owner provisions them as files of one volume that every revision mounts read-only at `/run/secrets`. A revision gets `NAME_FILE=/run/secrets/<secret>`, never the value. The deploy role allows each data volume only with its declared read-only flag.
- **Sensitive changes are classified by the supervisor against the Owner-approved baseline.**
  - Lock changes fall into six classes: patch, minor, major, added, removed, core.
  - The manifest maps each class to auto or owner. `core` and `added` are always owner, whatever the manifest says.
  - Paths matching the manifest's protected patterns need a signed Owner approval. The manifest, the lockfile and the pattern list are themselves always protected. A candidate can add patterns but never remove them.
  - A sha256 in a lockfile the candidate edited proves integrity, not authenticity. Authenticity comes from the baseline comparison.
- **Checks are journeys of HTTP steps with captures** (`pkg/managedapp/checkrun`).
  - A result carries only facts the runner measured: check, step, reason from a fixed set, expected and observed status, latency, bound, and the Spec's field name. It never carries response text.
  - Checks that write, such as a purchase, are marked. They run against a disposable copy restored from the live database's latest backup and served by the live revision, never against the live database. A fault held in the data, such as a broken checkout, is therefore still seen, and each run proves the backup restores. A fault seen only with live traffic or the live database is not covered by these checks.
- **Auto-start is a declared class, off by default.** Trusted intake may start work without the Owner only for a regression of a check that passed on the last verified release. It never does so when the fix touches a protected path. No script applies `sisf:ready`, and the issue body holds supervisor-structured facts only.
- **A database is an Owner-declared dependency.**
  - The manifest names its image, data volume, readiness, backup and restore commands. The commands read credentials from secret files, never from a command line.
  - Backup runs in the supervisor zone before a release. Its credentials never reach the agent or QA zones.
  - A release carrying a migration is sensitive. It needs Owner approval and a verified backup, and it is never auto-canaried.
  - Restore is never automatic.

## Consequences

- An existing application can be adopted without rewriting it, and the WordPress knowledge sits in one detector (`pkg/managedapp/detect/stack_wordpress.go`). A second stack, a Go program, adopts through the same contract.
- Candidates change content, never the interpreter. A fix that needs a new runtime image waits for the Owner.
- The live database is shared by every revision, so a release that changes the schema cannot be undone by switching revisions. That is why it is Owner-gated and backed up first.
- Latency bounds are judged on the host that runs the application. A host overloaded by unrelated work fails latency checks; that is a true fact about the service, reported as such, and the bound is not relaxed to hide it.

## Alternatives considered

- **Build a full image per candidate.** Rejected: the image would leave the sandbox through a registry the candidate's phases can write (ADR 0024).
- **Let the candidate's manifest declare what is sensitive.** Rejected: a candidate could declare its own change safe.
- **Restore the database automatically on rollback.** Rejected: restoring discards orders placed since the backup. Rolling back the content leaves data intact, and restoring data is an Owner decision.
- **Label regressions `sisf:ready` from a script.** Rejected: it bypasses the trusted intake that decides what work may start.

## Acceptance test

- `pkg/managedapp/spec_test.go`: strict parsing, the validation boundaries, protected paths, and the rule that `core` and `added` are always owner.
- `pkg/managedapp/detect/detect_test.go`: the WooCommerce example and the hello app adopt; a plain secret is refused without printing it; unpinned images, missing lockfiles and ambiguous services are refused.
- `pkg/managedapp/changegate/changegate_test.go`: a WooCommerce patch and a shop plugin fix are the factory's; a Core upgrade, a new plugin, the payment plugin, the site configuration, a must-use plugin, the Spec and a path outside the application are the Owner's; an unreadable change is the Owner's.
- `pkg/appbundle/store_test.go` and `internal/dockerfilter/deploy_bundle_mounts_test.go`: a tampered stored bundle fails its stream; a writable secrets volume or an undeclared volume is refused.
- `internal/supervisor/release/managed_app_live_test.go` (tag `smoke`, run by `examples/woocommerce-app/bin/release-smoke.sh`): on real Docker, the real deploy filter, Compose backend, edge and release pipeline release a WooCommerce patch by canary and roll back a failing shop plugin; the original orders keep their checksum and a restored backup matches the live data.
- `pkg/managedapp/checkrun/run_test.go`: a journey carries captures; a checkout that answers 200 without taking payment fails; results never carry response content.
- `pkg/appbundle/*_test.go`: reproducible bundles, sha mismatch refused, traversal, links, devices and size bombs refused, and lock change classification.
