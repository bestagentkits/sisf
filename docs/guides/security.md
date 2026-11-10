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
| Login and tokens: short-lived scoped tokens; a single-use step-up confirmation bound to one call, accepted only from the dashboard's form posts | `internal/auth` |
| Roles from local Owner grants and GitHub repository permission, never from a claim | `internal/rbac`, `cmd/factoryd/wiring/control_identity.go` |
| One action registry and service behind REST, the CLI, MCP, WebMCP and the dashboard; every action declares its scopes; writes limited to 60 per minute per person | `internal/actions`, `internal/control/surfaces.go` |
| OAuth for MCP clients: PKCE, audience-bound access tokens, no step-up in any token | `internal/oauthas`, `internal/mcp` |
| Dashboard: CSRF on every unsafe request, a Content-Security-Policy with no inline script | `internal/control/web`, `ui` |
| Secrets owner-only on disk, never printed; the GitHub token only via an environment variable, or `gh auth token` in a `--dev` install; sign-in client secrets only from a file or a variable, kept in the control zone's env file | `cmd/sisf/cmd_install_flags*.go`, `internal/install` |
| No Owner token: the Owner is an `issuer#subject` identity that signs in at the configured provider; `sisf init` and `sisf doctor` refuse an install without a provider or Owner, and the fake identity provider outside the simulation and `--dev` | `internal/install/identity*.go`, `internal/install/doctor_identity.go` |
| The control zone reads the edge's counters with a read-only token that differs from the admin token, and never holds the admin token | `internal/edge`, `internal/install/isolation_secrets*.go` |
| Only the control zone opens secret providers (`env:` and `local:` references) | `cmd/factoryd/wiring/control_secret_providers.go` |

## Limits to know

- Docker mode has no container sandboxing for untrusted code in this release (`sisf doctor` warns). Standalone is for trusted code only. Neither mode claims `safe_exec`; see the [capability matrix](../capability-matrix.md).
- Identity is tested against a fake GitHub and fake OIDC provider. A real GitHub App and real Google SSO are not exercised yet.
- A `--dev` install signs anyone who can reach 127.0.0.1 on its machine in as the Owner, without a password. It is refused off loopback and in Docker mode, and `init` and `doctor` warn loudly; use it only on a machine you alone use.
- `--agent-home` and `--qa-home` give an agent your own login under `trusted_code_only`; they are refused near the factory's secrets, state and trust, and the install directory must lie outside that home. Agent-written code is not built on the host: candidate builds are held in Standalone mode.
- Remote MCP and WebMCP are wired and tested in process against the fake identity provider; they have not been exercised against a real provider or MCP client deployment.
- Sessions, tokens, step-up confirmations and OAuth grants are held in memory: a restart of the control process signs everyone out. A personal access token or MCP access token can never decide an approval; only the dashboard's own form can, after a step-up confirming that one request, which the decision spends.
- The incident notifier in [ADR 0018](../adr/0018-people-during-an-incident.md) is a proposal, not shipped.

## Operator habits

- Keep `<dir>/secrets` private and out of version control; never paste tokens into issues or logs.
- Treat issue, comment and PR text as data. Wider permissions, new features and budget changes need an Owner approval.
- Run `sisf doctor` after changes and before trusting rollback.
