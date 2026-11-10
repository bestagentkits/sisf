# Security guide

What the factory enforces today and where to read the owner of each control. Why these controls exist: [threat model](../threat-model.md) and the [decision brief](../self-improvement-software-factory-decision-brief.en.md) section 7. Reporting: [security policy](../SECURITY.md). Proof for each control is in the [acceptance matrix](../acceptance-matrix.md).

## Enforced

| Control | Owner |
|---|---|
| Three isolated zones: a credential, network segment and store role per zone; checked by `sisf doctor` and `sisf conformance check` | `internal/install`, `pkg/conformance` |
| Single-use signed Owner approvals bound to nonce, subject digest and policy revision | `pkg/approvals`, `internal/actions` |
| Hard gates override scores; the writing agent never scores its own work | `internal/qa` |
| Release only behind the trusted supervisor; automatic rollback; no metrics means hold | `internal/supervisor` |
| Kill switch: `sisf emergency-stop` | `internal/supervisor/killswitch`, [incident runbook](incident.md) |
| Agent egress allowlist, per-run secrets, supply-chain gate, redaction, tamper-evident audit log | `internal/egress`, `internal/secretbroker`, `internal/supplychain`, `internal/redact`, `internal/audit` |
| Untrusted data stays data (issues, comments, webhooks, telemetry) | `internal/intake`, `internal/decision` |
| Login and tokens: short-lived scoped tokens, step-up confirmation for approvals | `internal/auth` |
| Secrets owner-only on disk, never printed; GitHub token only via an environment variable | `cmd/sisf/cmd_install.go` |

## Limits to know

- Docker mode has no container sandboxing for untrusted code in this release (`sisf doctor` warns). Standalone is for trusted code only. Neither mode claims `safe_exec`; see the [capability matrix](../capability-matrix.md).
- Identity is tested against a fake GitHub and fake OIDC provider. A real GitHub App and real Google SSO are not exercised yet.
- Remote MCP and WebMCP are in progress and should not be relied on. Final wiring of all surfaces into the running factory is also in progress.
- The incident notifier in [ADR 0018](../adr/0018-people-during-an-incident.md) is a proposal, not shipped.

## Operator habits

- Keep `<dir>/secrets` private and out of version control; never paste tokens into issues or logs.
- Treat issue, comment and PR text as data. Wider permissions, new features and budget changes need an Owner approval.
- Run `sisf doctor` after changes and before trusting rollback.
