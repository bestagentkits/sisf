# Operations

How to run and inspect an installed factory. Behaviour is owned by the code; this page says where to look. Flags: `sisf help <command>`.

## Lifecycle

| Task | Command |
|---|---|
| Start (builds, signs, checks isolation, starts all zones) | `sisf up` |
| Stop and keep everything | `sisf stop` |
| Restart every unit | `sisf restart` (Standalone also restarts on SIGHUP) |
| Stop and remove what the run created | `sisf down` (`-v` removes data too) |
| Health of the install | `sisf doctor` |

All of these take `--dir`; the default is `$SISF_HOME`, else `~/.sisf/factory`. Run `sisf doctor` after every start and before relying on rollback: it checks that a last known good release is recorded.

## Inspecting the factory

- `sisf status` shows the overview. `sisf issues` lists tracked issues.
- `sisf runs inspect <issue|run-id>` shows the record; `sisf runs explain <issue|run-id>` says why a run is stopped or blocked.
- `sisf approvals list`, `sisf approvals get <id>` and `sisf approvals decide <id>` handle Owner approvals. A decision is bound to the approval's nonce, subject digest and policy revision.
- `sisf call <action>` calls any action over REST (the catalogue is in [`api/openapi.yaml`](../../api/openapi.yaml)). Add `--dry-run` to preview.

Every surface runs through one action service and one action registry (`internal/actions`, registered by `internal/actions/actionsets`), so none holds extra power. The control server (`internal/control/surfaces.go`) mounts them all on one port:

| Path | Surface | Caller proves itself with |
|---|---|---|
| `/api/v1/` | REST, and the CLI through it | a personal access token, or a dashboard session plus its CSRF token |
| `/mcp` | remote MCP | an OAuth access token issued by the factory's own authorization server |
| `/webmcp/actions.json` | the WebMCP tool list `ui/static/webmcp.js` reads | a dashboard session |
| `/.well-known/oauth-*`, `/oauth/` | the OAuth authorization server (`internal/oauthas`) | none, or the dashboard session on consent |
| `/auth/` | sign-in callback, step-up confirmation, sign-out, the session and personal access tokens | the identity provider, then the session |
| `/` | the dashboard: its pages, `/signin`, `/logout` and the action forms under `/actions/` | a session cookie; form posts also need the CSRF token |

Parity across the five surfaces is proven by `tests/acceptance/five_surface_parity_test.go`; see the [acceptance matrix](../acceptance-matrix.md).

## Signing in

`sisf login` starts a device-style login: open the printed URL, check the code and approve. The CLI stores a short-lived token owner-only. The control URL comes from `--url`, then `$SISF_URL`, then `http://127.0.0.1:7463`; `$SISF_TOKEN` overrides the stored login.

People sign in to the dashboard with the providers the control process is given (`cmd/factoryd/wiring/identity_env.go`). There is no Owner token any more: the Owner is whoever signs in as an identity listed in `SISF_OWNER_IDENTITIES`. Roles other than Owner come from the person's permission on the factory's GitHub repository (`internal/rbac`).

| Variable | Meaning |
|---|---|
| `SISF_EXTERNAL_URL` | origin people and MCP clients reach the control server at; the OAuth issuer. Defaults to `http://<listen address>` only on loopback |
| `SISF_OWNER_IDENTITIES` | comma-separated `issuer#subject` identities that hold the Owner role |
| `SISF_AUTH_OIDC_ISSUER`, `SISF_AUTH_OIDC_CLIENT_ID`, `SISF_AUTH_OIDC_CLIENT_SECRET` | OpenID Connect sign-in, offered as `google` |
| `SISF_AUTH_GITHUB_CLIENT_ID`, `SISF_AUTH_GITHUB_CLIENT_SECRET`, `SISF_AUTH_GITHUB_URL`, `SISF_AUTH_GITHUB_API_URL` | GitHub sign-in, offered as `github`; the URLs default to github.com |
| `SISF_SUPERVISOR_ADMIN_ADDR` | the supervisor's admin socket, which the emergency stop and rollback actions call |
| `SISF_DOCS_DIR` | directory `docs.list` reads; unset means none |
| `SISF_EDGE_ADMIN_URL`, `EDGE_ADMIN_TOKEN` | the edge's counters; setting the URL turns on the Decision Layer |
| `SISF_TRUSTED_PROXIES` | networks the edge connects from; the public bug-report route limits clients by the address the edge forwards only from these (default loopback) |

In the simulation profile, sign-in defaults to the fake identity provider of `sisf sim oidc` on `127.0.0.1:7468`, which also stands in for GitHub sign-in, with the subject `owner` as the Owner. A real install must name at least one provider and one Owner.

Sessions, tokens, step-up confirmations and OAuth grants are held in memory in this release. Restarting the control process signs everyone out and voids every token.

Deciding an approval needs a step-up confirmation made in the dashboard within the last five minutes. A token never carries one: an approval over MCP or with a personal access token answers `STEP_UP_REQUIRED` and points at `/approvals` in the dashboard. Every surface limits writes to 60 per minute per person.

## State and budget

`sisf state bootstrap` prepares the state store (migrations, one role per zone, the budget record). `sisf up` does this for you. The budget mode default and its confirmation are in [ADR 0007](../adr/0007-budget-default-unlimited.md); code is in `internal/budget`. `factoryd dispatcher --run-estimate-micros N` is the amount each run sets aside from the budget when it is claimed; the default 0 suits an unlimited budget, and a capped budget needs it set.

## Decision Layer

When `SISF_EDGE_ADMIN_URL` is set, the control process reads the edge's counters every five minutes and runs the Decision Layer (`internal/decision`) under the policy in force. A recurring regression of an endpoint's 5xx rate becomes one GitHub issue for the Owner; the layer recommends and never changes anything itself. Proposals are remembered in the operational store, so a restart does not open a second issue for the same condition.

Without a `decision.system_one` section in the approved `.sisf/factory.yaml`, the deterministic rules decide alone. With one, the layer also asks that System One backend the typed questions of `DECISIONS.md` about each proposal; its answer can only make the proposal more cautious or raise its priority:

```yaml
decision:
  system_one:
    backend: jev            # jev or clef
    endpoint: https://system-one.example.test   # HTTPS, or HTTP on loopback
    version: "1"            # an explicit protocol version, never "latest"
    timeout: 30s            # above zero, at most 2m
    credential: secret://local/system-one       # optional secret reference, never a value
    model:
      provider: jev
      model: jev-1          # judgments reporting another route are refused
```

The schema is `schemas/factory.schema.json`; the checks are in `pkg/manifest/decision_system_one.go`. The control process has no secret provider configured yet, so a backend that needs a credential cannot be used in this release.

## Ports

Host ports 7460-7469 on 127.0.0.1; the owner is `internal/install/options.go`.

## When something is wrong

Use the [incident runbook](incident.md).
