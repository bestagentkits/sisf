# SISF — Self-improvement Software Factory

An open-source framework for a closed-loop *software factory*: it takes in requests, writes and independently verifies code, publishes, monitors, rolls back, and improves itself — all inside **one deployment**.

> **Status: design stage.** This repository currently contains the decision brief and architecture review only. Nothing described here is implemented yet.

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

- [Decision brief and implementation contract](docs/self-improvement-software-factory-decision-brief.md) (Vietnamese) — fixed product decisions, interfaces, issue state machine, QA and release rules, phased scope P0–P4, acceptance matrix SISF-01…20.
- [Architecture review](docs/self-improvement-software-factory-architecture-review.html) — interactive visual summary (VI/EN, light/dark). Download and open in a browser.

## Roadmap

| Phase | Outcome |
|---|---|
| P0 | ADRs, three-zone conformance checker, interface contracts |
| P1 | Local/Compose three-zone deployment, GitHub issue lifecycle, durable state |
| P2 | Two built-in agents, independent QA, publish with automatic rollback, factory self-update |
| P3 | GitHub/Google SSO, five control surfaces, Decision Layer, budgets |
| P4 | Remote agent adapters, Kubernetes and serverless profiles, public conformance matrix |

## License

[MIT](LICENSE)
