# SISF — Self-improvement Software Factory

An open-source framework for a closed-loop *software factory*: it takes in requests, writes and independently verifies code, publishes, monitors, rolls back, and improves itself — all inside **one deployment**.

## Status

The factory core is implemented and tested in this repository (Go 1.25, see [ADR 0019](docs/adr/0019-go-1-25-toolchain.md)). The authoritative record of what a test proves is the [acceptance matrix](docs/acceptance-matrix.md).

- **Implemented:** the three-zone conformance checker, install and lifecycle tooling (`sisf init`, `up`, `doctor`, `down`), signed Owner approvals, the audit log, the dispatcher with fencing, independent QA with hard gates, the trusted release supervisor with automatic rollback, the emergency stop, identity (GitHub/OIDC login, device-flow CLI login), one action service behind the REST API, the `sisf` CLI, remote MCP, WebMCP and the dashboard, the Decision Layer on the edge's counters (rules, with an optional System One), and budget modes. Identity, MCP and WebMCP are tested against fakes; a real GitHub App, real Google SSO and real MCP clients are not yet exercised.
- **Not yet proven:** the end-to-end lifecycle on Compose and the real smoke run (`make smoke`), which has not been run. Do not rely on them yet.
- **Supported operating modes:** Standalone (trusted code only) and Docker Compose. Kubernetes, Vercel, Cloudflare, TOSE and Railway are unsupported or experimental; see the [capability matrix](docs/capability-matrix.md).
- **Quickstart:** [docs/guides/quickstart.md](docs/guides/quickstart.md) runs a local factory against a simulated GitHub.

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

## Guides

- [Quickstart](docs/guides/quickstart.md) · [Operations](docs/guides/operations.md) · [Incident runbook](docs/guides/incident.md) · [Security guide](docs/guides/security.md)

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

## License

[Apache-2.0](LICENSE). See [NOTICE](NOTICE).
