# SISF — Self-improvement Software Factory

An open-source framework for a closed-loop *software factory*: it takes in requests, writes and independently verifies code, publishes, monitors, rolls back, and improves itself — all inside **one deployment**.

## Status

The factory core is implemented and tested in this repository (Go 1.25, see [ADR 0019](docs/adr/0019-go-1-25-toolchain.md)). The authoritative record of what a test proves is the [acceptance matrix](docs/acceptance-matrix.md).

- **Implemented:** the three-zone conformance checker, install and lifecycle tooling (`sisf init`, `up`, `doctor`, `down`), signed Owner approvals, the audit log, the dispatcher with fencing, independent QA with hard gates, the trusted release supervisor with automatic rollback, the emergency stop, identity (GitHub/OIDC login, device-flow CLI login), one action service behind the REST API, the `sisf` CLI, remote MCP, WebMCP and the dashboard, the Decision Layer on the edge's counters (rules, with an optional System One), and budget modes. Identity, MCP and WebMCP are tested against fakes; a real GitHub App, real Google SSO and real MCP clients are not yet exercised.
- **Sign-in:** there is no Owner token. People sign in at an OpenID Connect or GitHub provider and the Owner is named at `sisf init` (`--owner issuer#subject`); the simulation and the loopback-only `--dev` install sign in at a fake identity provider the install runs itself.
- **Not yet proven:** the real smoke run (`make smoke`), which has not been run, and agent-written candidate builds in Standalone mode, which are held in this release. Do not rely on them yet.
- **Supported operating modes:** Standalone (trusted code only) and Docker Compose. Kubernetes, Vercel, Cloudflare, TOSE and Railway are unsupported or experimental; see the [capability matrix](docs/capability-matrix.md).

## Quickstart

Try it on your machine against a simulated GitHub, with a fake sign-in and a scripted agent: no token, no paid model. You need Linux or macOS, Go 1.25, GNU Make, and ports 7460-7469 on 127.0.0.1 free.

```sh
git clone https://github.com/bestagentkits/sisf.git && cd sisf
make install                               # sisf on your PATH via $(go env GOPATH)/bin, stamped with its version
export SISF_HOME=$HOME/.sisf/sim           # the install directory, outside the checkout
sisf init --repo acme/hello --profile sim --confirm-unlimited --no-alert
sisf up                                    # builds and starts all three zones; stays in the foreground
```

In a second terminal: `sisf doctor` checks the install, and `http://127.0.0.1:7463` is the dashboard, signed in as the Owner. To send a first issue through it, a policy must be put in force first; the [quickstart](docs/guides/quickstart.md) walks through that, the Docker Compose variant and the clean-up.

## Installing for real

A factory that serves a real repository needs a GitHub App installation, which `sisf init` names with `--github-installation-id` and `--github-app-login`, a long-lived token it acts with, its webhook, a sign-in provider and an HTTPS origin. [Installing a factory](docs/guides/install.md) covers the development install on your own machine (`--dev`, your `gh` login), the real install step by step, real coding agents and running as a service. Real GitHub App and Google sign-in flows are tested only against fakes so far.

## Where to go next

- [Documentation index](docs/README.md): every guide and reference.
- [Operations](docs/guides/operations.md), [Policy, approvals and budget](docs/guides/policy-and-approvals.md), [Why is my issue stuck?](docs/guides/stuck-issues.md), [Maintenance](docs/guides/maintenance.md), [Troubleshooting](docs/guides/troubleshooting.md), [CLI reference](docs/guides/cli-reference.md).
- [Incident runbook](docs/guides/incident.md) and [Security guide](docs/guides/security.md).

## The idea in one picture

One factory deployment (`factory_id`, one release manifest, one lifecycle) made of three zones:

| Zone | Role |
|---|---|
| **Zone 1 · Application** | The user-facing app (Webapp, API, MCP, WebMCP). Emits traces, logs, sessions and feedback. |
| **Zone 2 · Agent factory** | Dispatcher, run state, coding agents (Claude CLI, Codex, Pi, optional remote executors) and an independent QA agent. |
| **Zone 3 · Control plane** | Dashboard, API, CLI, MCP, WebMCP, SSO and RBAC, approvals, policies, budget, secrets, trusted release supervisor with canary and automatic rollback. |

GitHub is the source of truth for source, policy, decisions and work items; operational state (locks, jobs, ledger, sessions) lives in the factory's own database.

## Core rules

- New features, wider permissions and policy changes need Owner approval; agents only improve what is already authorized.
- The agent that writes a change never grades it. QA score never overrides a hard-gate failure.
- No safe rollback or no metrics means hold, not publish.
- An issue closes only after the *observed deployed revision* meets its Definition of Done.
- The factory can improve its own code, but a candidate cannot change the supervisor, signing keys, QA baseline or rollback rules in the same cycle.

## Documents

- Decision brief and implementation contract: [English](docs/self-improvement-software-factory-decision-brief.en.md) · [Tiếng Việt](docs/self-improvement-software-factory-decision-brief.md) (original) — fixed product decisions, interfaces, issue state machine, QA and release rules, phased scope P0–P4, acceptance matrix SISF-01…20.
- [Architecture](docs/self-improvement-software-factory-architecture.html) — detailed interactive page: topology, entrypoints, workflows, playable issue state graph, decisions, user flows, QA and release rules, surfaces and protocols, trust boundary and security requirements, deployment design per operating mode, roadmap (VI/EN, light/dark). Download and open in a browser.
- [Architecture review](docs/self-improvement-software-factory-architecture-review.html) — interactive visual summary (VI/EN, light/dark). Download and open in a browser.

## Security

Report vulnerabilities privately; see the [security policy](docs/SECURITY.md).

## Roadmap

| Phase | Outcome |
|---|---|
| P0 | ADRs, three-zone conformance checker, interface contracts |
| P1 | Local/Compose three-zone deployment, GitHub issue lifecycle, durable state |
| P2 | Two built-in agents, independent QA, publish with automatic rollback, factory self-update |
| P3 | GitHub/Google SSO, five control surfaces, Decision Layer, budgets |
| P4 | Remote agent adapters, Kubernetes and serverless profiles, public conformance matrix |

## Contributing / Development

You need Go 1.25.14 or newer, a C compiler for the race detector, GNU Make and golangci-lint v2.12.2. `make help` lists every target; `make check` runs what CI runs, without Docker. See [CONTRIBUTING.md](CONTRIBUTING.md) for the rules, the [test layers](docs/contributing/testing.md), the [project layout](docs/contributing/project-layout.md) and [common changes](docs/contributing/common-changes.md).

## License

[Apache-2.0](LICENSE). See [NOTICE](NOTICE).
