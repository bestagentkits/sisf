# SISF — Self-improvement Software Factory

**Software is solved. Here is the drawing.**

SISF is a free, open-source software factory, specified as one deployment. A GitHub issue goes in; agents write the fix, an independent QA gate signs it off, and a trusted supervisor ships it by canary and rolls it back on its own. The drawing is finished, and the factory it describes now runs.

> **Status:** Core built and tested. Runs on your machine in simulation. No release yet.
> Free and open source under Apache-2.0. From the creator of [AgentKit.best](https://agentkit.best).

- Repository: <https://github.com/bestagentkits/sisf>
- This page as HTML: <https://factory.agentkit.best/>

## What SISF is

SISF is an open-source framework for a closed-loop software factory. It takes in requests, writes and independently verifies code, publishes, monitors, rolls back and improves itself, all inside one deployment you own.

- One `factory_id`, one release manifest, one lifecycle.
- GitHub is the source of truth for code, policy and decisions. The factory's own database holds locks, jobs and the ledger.
- The agent that writes a change never grades it.

## How it works

One deployment, three zones and one self-improvement loop. The app reports how it is doing, the Decision Layer proposes a fix only when the evidence holds, separate agents build and grade it, and the supervisor ships it behind a canary before the loop goes back to watching.

The page shows this as an animated 3D drawing (a 2.5D drawing on devices without GPU acceleration, a still drawing without JavaScript). It is an illustration of the design, not live data. Values marked * are starting points, to be calibrated.

### Flow 1 · The self-improvement loop

1. **Zone 1 reports how it is doing.** Traces, logs and feedback leave the app as data only. Here, one endpoint's error rate starts to climb.
2. **The Decision Layer weighs the evidence.** Samples, baseline, trend and user impact. If the evidence is thin, it stops here: it collects more, or ignores the signal.
3. **It proposes a fix within its authority.** Within its delegated scope, an improvement to existing behaviour can go ahead. A new feature would wait here for the Owner.
4. **The proposal becomes a GitHub issue.** It carries the evidence, a scope and a measurable Definition of Done.
5. **A runner writes the fix.** The dispatcher claims the issue. A fresh runner in the sandbox writes the fix and reaches out only through the egress proxy.
6. **A different agent grades it.** `qa-runner`, with its own identity, scores it against a read-only baseline; the result is kept in a signed `qa_receipt`. The writer never grades.
7. **The supervisor ships a canary.** Only the supervisor deploys: 5%, then 25%, then 100%* of traffic, while the error rate is compared with baseline.
8. **The metric recovers. The loop closes.** The issue is verified on the running revision and closed. The factory goes back to watching.

### Flow 2 · The factory fixes itself

1. **The factory fixes itself, too.** Here, a read-only dashboard page in Zone 3 shows stale data. The fix enters the same loop.
2. **Built and graded the usual way.** A runner writes the fix; a `qa-runner` with a different identity grades it.
3. **Zones 2 and 3 restart. Zone 1 keeps serving.** The supervisor rolls out the new `control`. Users are not interrupted.
4. **The trusted core stays locked.** The supervisor, signing keys, policy enforcement, QA baseline and rollback rules sit where no candidate can write, so no fix can change them.
5. **Changing that core takes a human.** It is a governance change: approved by the Owner and run as its own, separate cycle.
6. **Watched like any release.** If the new `control` misbehaves, the supervisor restores the last known good on its own.

### Flow 3 · Issue to release

1. **An issue arrives.** Labelled `sisf:ready` on GitHub, it reaches `control` as a signed webhook. The label asks; it grants nothing.
2. **Scope first.** A versioned scope and Definition of Done. A new feature waits here for the Owner.
3. **Claimed and built.** The dispatcher takes a lease over the authenticated API; a runner in the sandbox writes the change.
4. **Graded by another agent.** A score of 85/100* and every hard gate, recorded in a signed `qa_receipt`.
5. **Released as a canary.** Only the supervisor deploys: 5% → 25% → 100%* of traffic, watched against baseline.
6. **Closed only when observed.** The issue closes once the running revision meets the Definition of Done. A merge alone is not done.

### Flow 4 · Rollback

1. **A canary goes out.** The supervisor releases a new revision and the edge gives it 5%* of traffic.
2. **Errors cross the threshold.** For example 5xx above 2%* and more than a point over baseline, after enough samples.
3. **The last known good comes back.** The supervisor restores it on its own, from storage no agent can write. Code and config only; customer data is never overwritten.
4. **Traffic is back on stable.** Zone 1 keeps serving the verified revision. The candidate is gone.
5. **The issue is blocked, with evidence.** It stays open with a recovery report. Nothing is marked as a success.

### Run the line: every state, in order

Label a GitHub issue. The factory scopes it, waits for the Owner when the work is new, builds it with one agent, grades it with another, and ships it through a supervisor that can always take it back.

Six stations:

1. **Intake.** An issue labelled `sisf:ready` is read, clarified through comments if information is missing, and given a versioned Scope of Work and Definition of Done.
2. **Owner gate.** New features and anything outside delegated authority wait in the approval inbox for the Owner.
3. **Coding agent.** One run claims the issue with an atomic lease and a budget reservation, and produces a patch, tests and artifacts.
4. **QA gate.** A separate agent with a separate identity grades the candidate and signs a `qa_receipt`.
5. **Supervisor.** The trusted release supervisor, the only unit with deploy rights, checks that a rollback is possible before anything ships.
6. **Canary and monitor.** The release ramps up under observation. If the error threshold is exceeded the last known good release is restored automatically.

### The 15 lifecycle states

`Intake` → `Clarifying` → `ScopeDraft` → `ApprovalPending` → `Ready` → `Claimed` → `Developing` → `QAEvaluation` → `ReleaseCandidate` → `Canary` → `Monitoring` → `Verified` → `Closed`, plus the fault track `RolledBack` → `Blocked` (→ `Ready` after a safe, authorized retry).

An issue closes only after the observed deployed revision meets its Definition of Done. A merged pull request is not completion.

## Architecture

### Three zones, one deployment

| Zone | Role | Runtime units |
|---|---|---|
| Zone 1 · Application | The user-facing app (Webapp, API, MCP, WebMCP). Emits traces, logs, sessions and feedback. | `app` |
| Zone 2 · Agent factory | Dispatcher, run state, coding agents (Claude CLI, Codex, Pi, optional remote executors) and an independent QA agent. | `dispatcher`, `runner`, `qa-runner` |
| Zone 3 · Control plane | Dashboard, API, CLI, MCP, WebMCP, SSO and RBAC, approvals, policies, budget, secrets, trusted release supervisor. | `control`, `supervisor` |

Shared units: `state`, `artifacts`, `edge`. The three zones share one `factory_id`, one release manifest and one lifecycle. They are never three separate products.

### QA gates

- The coding agent and the QA agent have different identities and workspaces. With two or more model routes, QA uses a different model or provider.
- Starting rubric, to be calibrated: correctness and regression 30, security and auth 25, contract and data safety 20, UX, performance and output 15, observability and rollback 10.
- Proposed pass mark: score ≥ 85/100 (a design starting point) with no hard-gate failure.
- Eight hard gates: tests and contract, permissions, migrations, secrets, confidence of observed build, independent evaluator, resource and budget policy, rollback readiness. A high score never overrides a hard-gate failure.
- The evaluator signs a `qa_receipt` pinned to the candidate digest, suite digest, baseline SHA and the model routes of both agents.

### Canary and rollback

- Pipeline: build immutable candidate → preview → QA → rollback preflight → canary or blue-green → observed revision and metrics → promote or restore.
- Proposed canary ramp where the platform can split traffic: 5% → 25% → 100% (to be calibrated). Otherwise blue-green with smoke tests and a hold.
- Illustrative rollback trigger: 5xx above 2% and more than 1 percentage point over baseline, with at least 100 samples, in two consecutive 10-minute windows. All starting values.
- Low traffic: mandatory synthetic checks run for a minimum hold instead of drawing conclusions from one failed request.
- Missing metrics, no recovery version or a broken deployment adapter means hold. Success is never assumed.

### Supervisor boundary

The trusted release supervisor belongs to the same deployment but sits outside what a candidate can modify. All four must hold, or self-update is disabled:

1. It runs from its own digest-pinned image, not built from the candidate's artifact.
2. The last known good release, signing keys and rollback configuration live where Zone 1, Zone 2 and every agent workspace have no write access.
3. The normal release pipeline cannot change the supervisor's digest; only a governance cycle with human approval can.
4. It checks its own digest at startup and refuses to run if it deviates from the signed manifest.

### Five surfaces

One service layer, five ports: Dashboard, API, CLI, MCP and WebMCP. They call the same policy and action service, so permissions are the same everywhere.

## Philosophy: seven rules for software that agents write

1. **New features belong to the Owner.** Agents improve only what is already authorized. (SISF-03)
2. **The agent that writes a change never grades it.** Separate identity, workspace and, with two routes, model. (SISF-06)
3. **A score never overrides a hard gate.** Eight hard gates veto any score. (SISF-07)
4. **No way back, no release.** No safe rollback or no metrics means hold, not publish. (SISF-20)
5. **Done means observed.** An issue closes only when the deployed revision meets its Definition of Done. (SISF-02, SISF-08)
6. **The factory may improve itself, never its own judge.** The supervisor, signing keys, QA baseline and rollback rules change only through a human-approved governance cycle. (SISF-10, SISF-11)
7. **When evidence is thin, do nothing.** The Decision Layer chooses `ignore` or `collect_more_evidence`. (SISF-17)

Every rule is built and proven by the acceptance test named after it (see the [acceptance matrix](https://github.com/bestagentkits/sisf/blob/main/docs/acceptance-matrix.md)). The tests run against a simulated GitHub and scripted agents; a paid run with real agents is not yet proven.

Every factory carries its own written philosophy, signed by its Owner: `AGENTS.md`, `REVIEW.md`, `DECISIONS.md`, `FEATURES.md`, `docs/adr/` and `.sisf/factory.yaml`. Markdown guides the models; deterministic checks enforce the rules.

## Roadmap

| Phase | Outcome | Status |
|---|---|---|
| P0 | ADRs, three-zone conformance checker, interface contracts | Built, tested |
| P1 | Local and Compose three-zone deployment, GitHub issue lifecycle, durable state | Built, tested |
| P2 | Two built-in agents, independent QA, publish with automatic rollback, factory self-update | Built; a paid run with real agents is still to prove |
| P3 | GitHub and Google SSO, five control surfaces, Decision Layer, budgets | Built; tested against fake identity providers, real SSO still to prove |
| P4 | Remote agent adapters, Kubernetes, TOSE and serverless profiles, public conformance matrix | Planned |

Supported today: Standalone (trusted code only) and Docker Compose (no container sandbox for untrusted code yet). Kubernetes, Vercel, Cloudflare, TOSE and Railway are experimental or unsupported; each is marked supported only after it passes end-to-end tests. Nothing is released yet.

## Get started

Run a whole factory on your machine against a simulated GitHub, a fake sign-in and a scripted agent: no token, no paid model. You need Linux or macOS, Go 1.25, git, GNU Make, and ports 7460-7469 on 127.0.0.1 free. Windows is not supported yet.

```sh
git clone https://github.com/bestagentkits/sisf.git && cd sisf
make install                               # sisf on your PATH via $(go env GOPATH)/bin
export SISF_HOME=$HOME/.sisf/sim           # an empty install directory outside the checkout
sisf init --repo acme/hello --profile sim --confirm-unlimited --no-alert
sisf up                                    # builds and starts all three zones; stays in the foreground
```

In a second terminal, `sisf doctor` checks the install, and `http://127.0.0.1:7463` is the dashboard, signed in as the Owner. The [quickstart](https://github.com/bestagentkits/sisf/blob/main/docs/guides/quickstart.md) puts a policy in force, sends a first issue through the loop, and covers the Docker Compose variant and clean-up. For a real repository, see [installing a factory](https://github.com/bestagentkits/sisf/blob/main/docs/guides/install.md).

Not proven yet:

- A paid end-to-end run with real Claude and Codex agents. Two release rehearsals passed with a scripted coder and a fake QA judge: a good candidate shipped, a bad one rolled back.
- A real GitHub App, real Google sign-in and real MCP clients; all three are tested against fakes.
- Container sandboxing for untrusted code in Docker mode, and the Standalone program export on platforms other than macOS on Apple silicon.

Not available yet: one-click **Deploy on TOSE**. TOSE is not a supported SISF backend today, and SISF never requires it.

Found something wrong? [Open an issue](https://github.com/bestagentkits/sisf/issues), or report a vulnerability privately through the [Security tab](https://github.com/bestagentkits/sisf/security/advisories/new).

## Docs

- [Decision brief (English)](https://github.com/bestagentkits/sisf/blob/main/docs/self-improvement-software-factory-decision-brief.en.md)
- [Decision brief (Vietnamese, authoritative)](https://github.com/bestagentkits/sisf/blob/main/docs/self-improvement-software-factory-decision-brief.md)
- [Architecture page](https://github.com/bestagentkits/sisf/blob/main/docs/self-improvement-software-factory-architecture.html)
- [Architecture review](https://github.com/bestagentkits/sisf/blob/main/docs/self-improvement-software-factory-architecture-review.html)
- [Quickstart](https://github.com/bestagentkits/sisf/blob/main/docs/guides/quickstart.md)
- [Documentation index](https://github.com/bestagentkits/sisf/blob/main/docs/README.md)
- [Acceptance matrix](https://github.com/bestagentkits/sisf/blob/main/docs/acceptance-matrix.md)
- [Security policy](https://github.com/bestagentkits/sisf/blob/main/docs/SECURITY.md)
- [License: Apache-2.0](https://github.com/bestagentkits/sisf/blob/main/LICENSE)

---

Free and open source. Apache-2.0. From the creator of [AgentKit.best](https://agentkit.best). [TOSE](https://tose.sh) is the creator's deployment platform; SISF does not require it.
