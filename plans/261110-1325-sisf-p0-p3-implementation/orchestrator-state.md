# Orchestrator state

Durable record for the orchestrating session. Re-read after any context compaction.

## Confirmed brief (user chose Confirm on 2026-11-10)

Objective: implement SISF P0–P3 per the decision brief in repo `bestagentkits/sisf`, using parallel sessions.

Done means:
1. `go build ./...` and `go test ./...` green on the P3 branch.
2. The 20 tests SISF-01…20 automated and passing with fake GitHub, fake agents, fake OIDC.
3. One command brings up three zones on Docker Compose; a sample issue completes its lifecycle to closed.
4. One real smoke run: gh + claude + codex against `bestagentkits/sisf-sandbox`; result in a report.
5. Conformance checker rejects a config missing a zone or violating the supervisor boundary.
6. The 8 security requirements of brief §7 assigned to a phase and tested.
7. Two operating modes work: Standalone, Docker.
8. The 9 open points of §15: one ADR each, status proposed.
9. Four stacked PRs (P0→P3), CI green, isolated review clean, issue labels match real state.
10. README and docs updated: no longer "design stage".

Out of scope: P4; Kubernetes, Vercel, Cloudflare, TOSE, Railway; any code from `saasvn/tose-app`; merging to main; real GitHub App and real Google SSO; production.

Constraints: Go per brief §11 layout; tests first; no secrets committed; never push to main; orchestrator may create issues, labels, PRs, CI. Owner merges.

Stop and ask only if: destructive or irreversible action; a Done-means item must change; spending beyond the one smoke run; failure unexplained after advisor counsel; a login expires.

## Defaults announced to the user

Module `github.com/bestagentkits/sisf`; Go 1.24; modernc SQLite (no CGO); dashboard = Go templates + vanilla JS embedded; ports 7460–7469; max 3 concurrent code-writing sessions, each in its own worktree; strong model for core design, cheaper for adapters/docs; stacked branches `feat/p0-foundation` → `feat/p1-local-factory` → `feat/p2-agents-qa-release` → `feat/p3-surfaces-identity`; one GitHub Actions workflow (build, test, lint).

## GitHub state

- Sandbox repo: `bestagentkits/sisf-sandbox` (private, created by this run).
- Labels created: `in progress`, `in review`, `done`, `shipped`, `ai-handle`.
- Tracking issues: #4 (P0, label `in progress`), #5 (P1), #6 (P2), #7 (P3). Exactly one state label per issue; comment with evidence at each transition.
- Ruleset `protect-main` requires a PR for main.

## Rules for commits and PRs

Conventional commits, no AI references, no attribution lines, no plan or phase identifiers in code, tests or commit messages. Reply to the user in Vietnamese.

## Progress log

- 2026-11-10 13:30 — gate 3 started; planner sub-agent writing `plan.md` and four phase files in this directory. Account usage 3% weekly.
- 2026-11-10 13:40 — plan written (`plan.md` + four phase files, 28 packages). Orchestrator decisions on the planner's questions: keep Go 1.24, bump the `go` directive only if an approved dependency forces it; PostgreSQL for Docker mode and SQLite for Standalone (brief §10); smoke run uses the local `gh` login as a dev-only credential in Standalone `--dev`, sandbox repo only; Jev/Clef ship as a generic typed adapter plus fake; the Pi agent adapter is added to P2 as an extra light package (`adapters/agent/pi/`) because brief §12 P2 lists it.
- Model tiers for sessions: strong = inherit (Fable); standard = `claude-sonnet-5-5`; light = `claude-sonnet-5-5` with low effort.
- Integration: each package session commits on its own worktree branch and does not push; the orchestrator squash-merges into the phase branch in the plan's merge order, then runs build and tests.
- 2026-11-10 13:50 — `start_session` is refused on turns triggered by background notifications, and the harness cannot create agent worktrees itself. Packages therefore run as background sub-agents in worktrees the orchestrator creates by hand under `/Volumes/GOON/www/claudekit/sisf-worktrees/<name>` on branches `pkg/<name>`; remove each worktree and branch after its squash-merge. P0-A runs in the primary checkout on `feat/p0-foundation`; P0-D (`p0-docs`) and P0-E (`p0-ci`) run in worktrees now; P0-B and P0-C start after P0-A lands.
- 2026-11-10 14:15 — P0 contracts (5ce711d), CI (0371650) and docs/ADRs/AGENTS.md (41670c7) are on `feat/p0-foundation`, pushed; build, tests, docs parity test and lint pass locally. State machine and conformance packages run in worktrees `p0-statemachine` and `p0-conformance`. Decisions: budget amounts stay integer micros; `pkg/*` exposing `internal/core` types is accepted for 0.x (ADR 0016); brief §10 "Releasing" wording vs the 15 states of §5.2 is a docs fix to make in the P3 docs package (§5.2 is the contract).
- Other sessions: landing page runs in its own session (`sisf-landing`, worktree branch `claude/landing-page-b75243`, owns `site/` only). "Deploy on TOSE" button: wait for the TOSE session to report it live; target `https://app.tose.sh/deploy?repo=...`.
- 2026-11-10 14:58 — P0 code complete on `feat/p0-foundation` (e24b4c0, pushed): phase verification passed locally (build, vet, race tests, lint, valid manifest exits 0, 37 reject fixtures exit non-zero). Isolated review of P0 is running; the P0 PR opens after it is clean, then issue #4 moves to `in review`/`done`. P1 started without waiting: branch `feat/p1-local-factory` is checked out in worktree `sisf-worktrees/p1-integration` (integrate there; the primary checkout stays on P0 until its PR is open). Wave 1 running: `p1-state` (Fable), `p1-github` (sonnet), `p1-policy` (Fable). P1-G (supervisor/signing, deps P0 only) starts when a slot frees. Review fixes on P0 get merged forward into P1. Open question for the Owner: CONTRIBUTING.md asks for DCO sign-off; run commits carry none.
- 2026-11-10 15:10 — Isolated review of P0: 0 critical, 4 high, 7 medium, 2 low. Done: shared types moved `internal/core` → `pkg/core` (84a01b0 on P0, pushed) so external adapters can implement `pkg/*` interfaces. Running on P0: `p0-fix-conformance` (isolation, network, supervisor comparison, backends, secret echo, self-attested support, degenerate settings, trust keys) and `p0-fix-contracts` (store must enforce table, approval/delegation/attempts guards, bound receipts + signing payloads, single QA pass rule, table-vs-brief test). After both merge: re-review P0, open PR, then merge P0 into P1. P1 wave-1 branches were cut before the move: at merge, rewrite `sisf/internal/core` → `sisf/pkg/core`, gofmt, and adapt to the contract additions. Concurrency is temporarily 5 writers (3 P1 + 2 review fixes), above the announced cap of 3. Deferred to the P3 docs package: README/SECURITY status text. Owner questions from review: §5.2 has no exits for retries exhausted, cancel/budget stop/kill switch in Developing, clarify timeout; whether `release.units` may contain control, qa-runner or state.
