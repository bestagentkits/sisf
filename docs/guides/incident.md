# Incident runbook

For when the factory misbehaves and automatic rollback has not, or cannot, fix it. This is the manual runbook that [ADR 0018](../adr/0018-people-during-an-incident.md) depends on. That ADR is still proposed: the outbound notifier it recommends is not implemented, so nobody is paged automatically. The Owner is the default responder and must watch the dashboard and GitHub.

## 1. Stop the damage

An emergency stop freezes publishing, holds every issue so nothing is claimed, cancels the runs in flight and revokes their secrets. It has two routes (`internal/supervisor/killswitch`):

- the **store**: the stop is recorded straight in the state store, which alone freezes publishing and holds every issue. Only this route can lift a stop.
- the **supervisor**: its admin socket records the same stop and also cancels runs and revokes their secrets.

`sisf emergency-stop` uses every route it is given and needs neither the control plane nor the dispatcher. It prints what each route did as JSON and exits 1 when a route it was given failed, so a partial stop never looks complete. Running it twice is safe: the second run reports the stop already in force.

### Standalone

Prerequisites: a shell on the factory's host as the user who owns the install directory. The factory id is the `factory_id:` line of `<dir>/factory.yaml`; the state store is the SQLite file `<dir>/data/state/factory.db`; the supervisor listens on `127.0.0.1:7464`, the command's default.

```sh
D=$HOME/.sisf/factory                                   # your install directory
ID=$(sed -n 's/^factory_id: //p' "$D/factory.yaml")
SISF_STATE_DSN="$D/data/state/factory.db" \
  sisf emergency-stop --factory-id "$ID" --reason "<why>" --actor "<your name>"
```

Expected output, when both routes answered: a JSON object with a `store` and a `supervisor` part, each holding `stop_id`, `stopped_at` and `"publishing_frozen": true`, the supervisor's part also `claims_held`, `runs_cancelled` and `secrets_revoked`. When the supervisor does not answer, the store part is still printed, followed by (observed):

```text
  "failures": [
    "supervisor: UNAVAILABLE: no supervisor answers on 127.0.0.1:7464"
  ]
sisf: UNAVAILABLE: the emergency stop could not use every route it was given
```

The publishing freeze and the claim hold are in force then, but only the supervisor route cancels runs in flight: run the same command again once the supervisor answers.

Verify: `sisf emergency-stop --status` asks the supervisor and prints `"frozen": true` with the stop's id, actor and reason.

Undo, once the cause is fixed: lift it through the store. Cancelled runs stay cancelled and revoked secrets stay revoked; the held issues are let go and can be claimed again.

```sh
SISF_STATE_DSN="$D/data/state/factory.db" \
  sisf emergency-stop --factory-id "$ID" --reason "<what was fixed>" --actor "<your name>" --lift
```

Expected: `emergency stop lifted`. Lifting when no stop is in force answers `NOT_FOUND: no emergency stop is in force`; lifting without `SISF_STATE_DSN` answers `INVALID_ARGUMENT: no state store connection was given; set SISF_STATE_DSN` (observed).

`--actor` is optional when `$USER` is set (it then defaults to `user:$USER`); in cron, a container or a service without `$USER` the command refuses with `--factory-id, --reason and an actor (--actor or $USER) are required`.

### Docker Compose

In Docker mode the host has no route to either one by default: the state store is on the project's internal `factory` network, and the supervisor's admin socket is a Unix socket on the `<project>-supervisor-admin` volume (`templates/compose/compose.yaml`). Use the supervisor route through the control plane instead. It is the `factory.emergency_stop` action: Owner only, no approval and no step-up (`internal/actions/operationsactions`), served by every surface; `tests/acceptance/step_up_and_write_limits_test.go` drives it as a signed-in Owner over REST with no approval and checks that the supervisor recorded each stop:

```sh
sisf login                                  # once, as the Owner
sisf call factory.emergency_stop --input '{"reason":"<why>"}'
```

or the Emergency stop form on the dashboard overview (`/`). The form's text says it asks for an approval, but the action needs none: leave the approval id field empty (`internal/control/web/actions_post.go` passes an empty id through). The answer is the supervisor's report (`stop_id`, `publishing_frozen`, `claims_held`, `runs_cancelled`, `secrets_revoked`). This route needs control and the supervisor running.

If control does not answer, stop the whole project: `sisf stop --dir <dir>` (`docker compose stop`). That halts every unit, including Zone 1, and records no stop: the next `sisf up` or `sisf restart` resumes work as before.

This release has no tested host route in Docker mode to check whether a stop is in force or to lift one. The store route would need the database published: `sisf up --debug` publishes it on 127.0.0.1:7469, but that `up` also rebuilds and restarts the factory, and lifting that way has not been exercised. Treat a stop in Docker mode as lasting until this gap is closed, and plan the recovery with the Owner.

## 2. Assess

- `sisf status`, `sisf runs explain <issue|run-id>` and `sisf doctor` show what is held, blocked or unhealthy; how to read them is in [Why is my issue stuck?](stuck-issues.md).
- A release with no observable metrics is held, not published; a hold is not a failure of the factory.

## 3. Roll back

Automatic rollback to the last known good release is the supervisor's job (`internal/supervisor/release`). If it failed, an Owner-approved rollback is the `deployments.rollback` action, documented in [`api/openapi.yaml`](../../api/openapi.yaml): `sisf call deployments.rollback --dry-run` previews it, and without an approval it files a request and answers that approval is required. Deciding the approval and running the approved rollback are step-up actions: they run only from their dashboard forms, after you confirm your identity at the provider for that one call; the CLI, REST, MCP and WebMCP answer `STEP_UP_REQUIRED` and point at the dashboard. The supervisor admin socket also accepts an approved rollback with a unit and a reason (`internal/supervisor/admin_rollback_and_freeze.go`). The action is wired to the supervisor's admin socket (`SISF_SUPERVISOR_ADMIN_ADDR`); the control process never calls a deploy backend, and the supervisor spends the approval. An automatic or approved rollback is never blocked by an exhausted budget. Verify it on your install before relying on it. Afterwards, confirm with `sisf doctor` that the observed revision is healthy: an accepted API call is not proof of recovery.

Customer data in Zone 1 must survive a rollback intact; verify it before declaring recovery.

## 4. Recover and learn

1. Fix the cause through a normal reviewed change.
2. Lift the stop (step 1) and watch the first run.
3. If a signing key or secret may have leaked, treat it as compromised and follow the [security guide](security.md); a delegated key is revoked without replacing the root (`internal/signing/delegation`).
4. Record what happened in GitHub; the audit log is tamper-evident (`internal/audit`).

Report a vulnerability privately as described in the [security policy](../SECURITY.md).
