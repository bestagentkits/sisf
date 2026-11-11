# Operations

How to run and inspect an installed factory. Behaviour is owned by the code; this page says where to look. Commands and flags: `sisf help <command>` and the [CLI reference](cli-reference.md). Procedures: [installing](install.md), [maintenance](maintenance.md) (upgrade, backup, restore), [policy, approvals and budget](policy-and-approvals.md), [Why is my issue stuck?](stuck-issues.md), [troubleshooting](troubleshooting.md).

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
- `sisf approvals list` and `sisf approvals get <id>` show Owner approvals. A decision is bound to the approval's nonce, subject digest and policy revision, and is made on the dashboard at `/approvals/<id>`: `sisf approvals decide <id>` can only preview one with `--dry-run` and otherwise answers `STEP_UP_REQUIRED` ([deciding an approval](policy-and-approvals.md#deciding-an-approval)).
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

## Progress on the issue and the candidate's pull request

- Each claimed issue carries one factory comment, marked `<!-- sisf:progress -->`, edited in place at every checkpoint of a run in flight: the claim, each failed coding attempt, each QA verdict (score, pass mark, failed hard gates), a QA hold, the candidate's pull request, the release steps, a rollback, the merge or why there was none, and the close. It is rendered from the factory's records only, redacted, and written only when it changed (`cmd/factoryd/wiring/progress_projection.go`, `progress_render.go`).
- An issue whose run ended before it had a progress comment is not given one. A comment that cannot be written is tried again after a pause that doubles up to an hour. A Closed issue is closed on GitHub only after its comment shows the end: an outage, a 5xx or a rate limit holds the close until GitHub answers again, however long that takes. Only a comment GitHub refuses for good (a locked issue, or a 403, 404, 410 or 422 answer) is given up, after three such refusals counted from the run's end, and the issue then closes without it; failures while the run was in flight do not count.
- When QA passes a candidate, the supervisor opens one pull request from `sisf/<factory>/<run>-<attempt>` to `main`, saying `Refs #N` so the merge does not close the issue (`cmd/supervisor/stages/candidate_pull_request.go`). `sisf runs inspect` shows it as `pull_request` with its state: `open`, `pending` (merging), `merged`, `left_for_owner` or `rolled_back`.
- After the release of that candidate is verified, the supervisor merges the pull request at the candidate commit, then the issue closes ([ADR 0021](../adr/0021-candidate-lands-by-supervisor-merge.md), `cmd/supervisor/stages/candidate_landing.go`). It does not merge a pull request it did not open itself (one someone else opened on the candidate branch), nor a candidate that changes a foundation or policy file, whose branch moved, whose policy pin no longer holds, or whose release was rolled back; the pull request is then left open with a note, `sisf runs explain` gives the reason, and the progress comment says the change is **not** merged. Merge or close those by hand.
- When GitHub cannot be reached for the merge, or an emergency stop is in force when the merge is due, the issue stays in Verified with its landing recorded as due, and the merge is tried again on a later round (after the stop is lifted, and also after a restart); it is never merged twice. A merge GitHub refuses because the default branch was just modified is tried again up to five rounds. When GitHub permanently refuses to open the pull request (the token lacks Pull requests: write, say), the candidate is left for the Owner and the issue still closes.
- The factory's own pull request is told by its author's login, `extensions.github_app_login` (the App's bot login, as `name[bot]`); a numeric `github_app_id` never stands in for it. An install that names an ID but no login merges nothing: every candidate is left for the Owner and `sisf doctor` warns that the login is missing. A wrong login shows in the reason: the author is not the factory's configured account.
- The supervisor's token needs Contents: write and Pull requests: write. A protected default branch that refuses the factory's merge leaves every pull request for the Owner.
- Candidate branches are never deleted by the factory. Delete them once their pull request is merged or closed.

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
| `SISF_EDGE_COUNTERS_URL`, `EDGE_COUNTERS_TOKEN` | the edge's admin listener and its read-only counters token; setting the URL turns on the Decision Layer. The control process never holds `EDGE_ADMIN_TOKEN` |
| `SISF_LOCAL_STORE_DIR` | the control zone's encrypted local secret store (`local:` references); its key is made on the first start |
| `SISF_CONTROL_SECRET_<NAME>` | a secret the `env` provider serves as `env:<NAME>`; only in `secrets/control/runtime.env` |
| `SISF_TRUSTED_PROXIES` | networks the edge connects from; the public bug-report route limits clients by the address the edge forwards only from these (default loopback) |

`sisf init` writes these: the non-secret settings into `factory.yaml` and from there into the Compose file or the Standalone unit, the client secrets and `EDGE_COUNTERS_TOKEN` into `secrets/control/runtime.env` only. In the simulation profile and a `--dev` install, sign-in is the fake identity provider of `sisf sim oidc` on `127.0.0.1:7468`, which the install starts itself and which also stands in for GitHub sign-in, with the subject `owner` as the Owner; in a `--dev` install a browser must first unlock sign-in with the secret each `sisf up` writes to `secrets/control/dev-sign-in` (see [development install](install.md#development-install)). A real install must name at least one provider and one Owner; `sisf init` refuses otherwise, and refuses the fake. `sisf doctor` checks the same: a provider and an Owner are configured, the external URL is sane, the fake is used only by the simulation (and, with a loud warning, a development install), and control reads the edge's counters with its own token and holds no edge admin token.

In Docker mode control reaches the supervisor's admin socket as `unix:/run/sisf/admin/supervisor.sock` on the `supervisor-admin` volume the two share; in Standalone it is `127.0.0.1:7464`.

Sessions, tokens, step-up confirmations and OAuth grants are held in memory in this release. Restarting the control process signs everyone out and voids every token.

An action that needs step-up (deciding an approval, a rollback, a budget change) runs only from its form in the dashboard, after the person confirms their identity at the provider for that one call: the action and the approval request, approval or exact input it names. The confirmation lapses after five minutes and is spent by the one call; another call needs another confirmation. A token never carries one, and a session is not honoured for these actions over REST or by a WebMCP tool, which does not list them: those calls answer `STEP_UP_REQUIRED` and point at the dashboard. Minting a personal access token follows the same rule: the step-up names `auth.tokens.create` and the digest of the exact token (name, scopes, lifetime), and approving a `sisf login` request names `auth.device.approve` and its user code, which the verification page's form does for you. The confirmation is the control: a fresh sign-in at the provider, bound to one action and target and spent once. As defence in depth, both mints and every dashboard action form are served only to a form submitted on control's own pages (`POST /auth/tokens`, `POST /api/v1/device/approve` and `POST /actions/{name}` take form fields with `Sec-Fetch-Mode: navigate`, `Sec-Fetch-Dest: document`, `Sec-Fetch-Site: same-origin` and `Sec-Fetch-User: ?1`, never a JSON body, a script's fetch or a request without those headers). That check cannot tell a person's click from a script in the page that submits the form; the step-up does. Browsers send these headers only to an https or loopback origin, so a control plane on plain http off loopback refuses these forms. There is no untargeted step-up. Every surface limits writes to 60 per minute per person. A write called with an idempotency key (`idempotency_key` in the body, the `Idempotency-Key` header, or `sisf call --idempotency-key`) runs once per person, action and key: a retry with the same input within 24 hours gets the first answer, and the same key with another input answers `CONFLICT`. The control zone keeps these records in the `control.idempotency` namespace of its store and prunes them after the retention.

## Reconciliation with GitHub

Besides webhooks, the control process asks GitHub for its repository's changed issues every `SISF_GITHUB_RECONCILE_INTERVAL` (10s to 1h, default `1m`; at most `SISF_GITHUB_RECONCILE_MAX_PAGES` pages of 100 issues, default 10) and hands any change no webhook delivered to intake through the same inbox, so a lost delivery is picked up within one interval (`sisf init --github-reconcile-interval` records the interval in `factory.yaml`, from where it reaches the control unit in both modes), while a failed or slow poll (bounded at 30s) is retried an interval later without holding back webhooks or approvals (`cmd/factoryd/wiring/control_reconcile.go`).

- Changes are read oldest first from the stored position (`control.reconcile` namespace). A poll that reaches its page bound stores what it read and the next poll continues from there, so a repository with a long history is taken in over several polls and nothing is dropped. Until the backlog is worked off, `sisf status` (`factory.status`) shows `github.backlog: true`.
- The position advances only to the last change actually stored; it moves back only to read a one-second burst again (below). If the store fails partway, the poll stops and the next one reads the failed change again. A change that fails validation does not hold up the ones after it, which are stored. The position stays before that change for up to 5 polls (`github.retrying`). After that it is given up, logged, and listed in `github.skipped`. The issue's next change is read normally.
- The factory's own comments, labels and closes are not handed back to intake. Around each edit the control zone reads the issue (`cmd/factoryd/wiring/factory_issue_edits.go`) and records the edit only when the two reads differ by nothing but what the edit did. GitHub's update times have whole-second precision, so a poll takes a change for that edit only at exactly its update time, only when every change before the edit had already been taken in, and only when its content proves it: the issue's title, body, state and labels are what the factory saw after its edit, and every comment since just before the edit carries the factory's marker (`internal/control/factory_write.go`). Anything else, a person's change in the same second included, is handed to intake, which reads the issue and finds nothing new when there is none.
- A poll steps through more changes within one second than a page holds by page number and keeps the page with its position, so a bulk edit is worked off like any backlog. When an issue of that second changes during the walk, it shifts the others, so the walk starts that second over; a restart starts it over too (`pkg/github/poller_page_walk.go`).
- Before parking an issue whose wait for an answer is over, the control zone makes sure a poll that started after the deadline read the listing to its end, and polls at once when none did (after a pause, or when an answer landed within one interval of the deadline); a failed or unfinished poll defers the parking (`cmd/factoryd/wiring/control_clarify_expiry.go`).
- A parked issue resumes when a person removes `sisf:needs-info`, even without that webhook. This happens once the factory's own label was confirmed on GitHub: any later event or polled change that shows the issue without the label leads to the issue being read again, and it resumes only if the label is really gone (`internal/intake/service.go`). Every other trigger of intake (opened, edited, labeled, closed, reopened, comments) is already decided from the issue as GitHub shows it now.

## State and budget

`sisf state bootstrap` prepares the state store (migrations, one role per zone, the budget record). `sisf up` does this for you. The budget mode default and its confirmation are in [ADR 0007](../adr/0007-budget-default-unlimited.md); code is in `internal/budget`. `factoryd dispatcher --run-estimate-micros N` is the amount each run sets aside from the budget when it is claimed; `sisf init --budget-cap` sets it from `--run-estimate` (default a fifth of the cap), and an unlimited budget leaves it at 0. A rollback reserves through the rollback reserve that ordinary runs cannot reach and is never blocked by an exhausted budget; its nominal cost is recorded in the ledger either way (`internal/supervisor/release/rollback_budget.go`).

## Decision Layer

When `SISF_EDGE_COUNTERS_URL` is set, the control process reads the edge's counters every five minutes and runs the Decision Layer (`internal/decision`) under the policy in force. A recurring regression of an endpoint's 5xx rate becomes one GitHub issue for the Owner; the layer recommends and never changes anything itself. Proposals are remembered in the operational store, so a restart does not open a second issue for the same condition.

Without a `decision.system_one` section in the approved `.sisf/factory.yaml` (the repository's copy, read at the policy commit the Owner approved, not the install's `<dir>/factory.yaml`; see [two files named factory.yaml](policy-and-approvals.md#two-files-named-factoryyaml)), the deterministic rules decide alone. With one, the layer also asks that System One backend the typed questions of `DECISIONS.md` about each proposal; its answer can only hold the proposal, make it more cautious or lower its priority, never raise it:

```yaml
decision:
  system_one:
    backend: jev            # jev or clef
    endpoint: https://system-one.example.test   # HTTPS, or HTTP on loopback
    version: "1"            # an explicit protocol version, never "latest"
    timeout: 30s            # above zero, at most 2m
    credential: env:SYSTEM_ONE_KEY  # optional secret reference ("env:NAME" or "local:path/name"), never a value
    model:
      provider: jev
      model: jev-1          # judgments reporting another route are refused
```

The schema is `schemas/factory.schema.json`; the checks are in `pkg/manifest/decision_system_one.go`. The adapter refuses redirects, so a request and its credential only ever reach the configured endpoint.

## Secrets of the control zone

The control process opens two secret providers, and no other zone opens any (`cmd/factoryd/wiring/control_secret_providers.go`). `secrets.list_refs` lists their references, never values; a System One credential resolves through them.

- `env:NAME` reads `SISF_CONTROL_SECRET_NAME` from the control zone's env file, `secrets/control/runtime.env`. Add the line there and restart; the variable is refused in every other zone's file, and nothing else in the environment is reachable through a reference.
- `local:path/name` reads the encrypted store in `SISF_LOCAL_STORE_DIR` (`/var/lib/sisf/control/secrets` in the `control-data` volume, `<dir>/data/control/secrets` in Standalone). The store has no writing command yet, so use `env:` references for now. The `local:` references `sisf init` writes into `<dir>/factory.yaml`, each component's `credentials_ref` such as `local:control/credentials`, are a different thing: they name files below `<dir>/secrets` that the installer creates and checks (`internal/install/layout.go`), not entries of this store.

## Agents and builds

Real coding and QA agents run only in Standalone mode under `trusted_code_only`; the flags and the refusal of `--agent-home` and `--qa-home` near the factory's protected paths are in the [quickstart](quickstart.md#real-agents-in-standalone). With `--exec local` candidate builds are held: the supervisor refuses to build an agent's code with the local backend. A Standalone install builds in a Docker sandbox the operator provides (`--exec docker` with the runner filter, sandbox network, egress proxy, runner image and a QA launcher); see the [quickstart](quickstart.md#building-in-a-docker-sandbox).

## Edge tokens

The edge (`cmd/edge`) reads two tokens from its environment. `EDGE_ADMIN_TOKEN` guards its admin listener (`--admin-listen`) and can change the traffic split; only the release supervisor holds it. `EDGE_COUNTERS_TOKEN`, which `sisf init` writes for the edge and the control process alike, authorizes `GET /counters` and nothing else; every other request with it is refused with 403. The two must differ, or the edge refuses to start. The control process reads the counters with `EDGE_COUNTERS_TOKEN` through a client that has no way to change the split (`internal/edge/admin_client.go`).

## A held release

A release the supervisor holds or refuses is not tried again by itself: nothing was deployed, and the issue waits in ReleaseCandidate. It shows in four places.

- The supervisor logs it once per decision, at warning level: `release held: the issue waits in ReleaseCandidate for the Owner` (or `release refused`), with the issue, `result`, `state`, `code`, `reason` and `release` (`cmd/supervisor/lifecycle_loop.go`).
- `sisf runs explain <issue>` answers stopped with cause `release_held`, the supervisor's reason as `release_hold` evidence, and the way out. `sisf runs inspect <issue>` shows `release_hold`.
- The issue's progress comment adds a **Release waiting:** line. It is chosen by the result and the code only and never carries the reason (`cmd/supervisor/handoff/release_hold.go`).
- The record is `release-hold/<run>/<attempt>` in the supervisor's `build` namespace.

The usual cause is `RELEASE_HOLD` with "real traffic is too low to judge the release and the application has no synthetic suite". A release with fewer real requests than `release.rollback.min_samples` in a window is judged by the application's synthetic suite for at least `release.low_traffic.min_hold`. The supervisor runs the suite only when it is given `--synthetic-program`. A Standalone install builds `bin/hello-app-synthetic` and passes it when the manifest names `release.low_traffic.synthetic_suite`. A Docker install has no synthetic suite yet, so a release there with too little traffic holds; once real traffic reaches the minimum, the Owner's retry lets it start over.

The way out: remove the cause (send traffic, give the supervisor its synthetic suite, lift the emergency stop), then run `factoryd issue retry --issue owner/repo#N` on the factory's host, as the Owner, with the control zone's environment. The supervisor releases the candidate again on its next round, and the hold is cleared once that release goes ahead. A retry by anyone else is denied, and a second retry before the new release ends answers `CONFLICT`. A refused release (a change to the factory's trusted control surface, say) is never tried again: merge or close its pull request by hand.

## Ports

Host ports 7460-7469 on 127.0.0.1; the owner is `internal/install/options.go`. The ports are fixed, so one machine runs one factory: [one factory per machine](install.md#one-factory-per-machine).

## When something is wrong

Use the [incident runbook](incident.md).
