# Incident runbook

For when the factory misbehaves and automatic rollback has not, or cannot, fix it. This is the manual runbook that [ADR 0018](../adr/0018-people-during-an-incident.md) depends on. That ADR is still proposed: the outbound notifier it recommends is not implemented, so nobody is paged automatically. The Owner is the default responder and must watch the dashboard and GitHub.

## 1. Stop the damage

Emergency stop needs neither the control plane nor the dispatcher:

```sh
sisf emergency-stop --factory-id <id> --reason "<why>"
```

It records the stop in the state store (this alone freezes publishing and holds every issue so nothing is claimed), then asks the supervisor to cancel runs in flight and revoke their secrets. The state connection comes from `$SISF_STATE_DSN` unless `--state-dsn-env` names another variable. Running it twice is safe. It exits 1 when a route could not be used (for example the supervisor did not answer) and prints what was done as JSON either way.

- Check whether a stop is in force: `sisf emergency-stop --factory-id <id> --reason "<why>" --status`.
- Lift it, after the cause is fixed: `--lift`. Only the store route can lift a stop; the supervisor socket cannot.

The code is in `internal/supervisor/killswitch`; the dashboard overview offers the same stop as the `factory.emergency_stop` action (`internal/control/web`).

## 2. Assess

- `sisf status`, `sisf runs explain <issue|run-id>` and `sisf doctor` show what is held, blocked or unhealthy.
- A release with no observable metrics is held, not published; a hold is not a failure of the factory.

## 3. Roll back

Automatic rollback to the last known good release is the supervisor's job (`internal/supervisor/release`). If it failed, an Owner-approved rollback is the `deployments.rollback` action, documented in [`api/openapi.yaml`](../../api/openapi.yaml): `sisf call deployments.rollback --dry-run` previews it, and without an approval it files a request and answers that approval is required. The supervisor admin socket also accepts an approved rollback with a unit and a reason (`internal/supervisor/admin_rollback_and_freeze.go`). The action is wired to the supervisor's admin socket (`SISF_SUPERVISOR_ADMIN_ADDR`); the control process never calls a deploy backend, and the supervisor spends the approval. Verify it on your install before relying on it. Afterwards, confirm with `sisf doctor` that the observed revision is healthy: an accepted API call is not proof of recovery.

Customer data in Zone 1 must survive a rollback intact; verify it before declaring recovery.

## 4. Recover and learn

1. Fix the cause through a normal reviewed change.
2. Lift the stop (step 1) and watch the first run.
3. If a signing key or secret may have leaked, treat it as compromised and follow the [security guide](security.md); a delegated key is revoked without replacing the root (`internal/signing/delegation`).
4. Record what happened in GitHub; the audit log is tamper-evident (`internal/audit`).

Report a vulnerability privately as described in the [security policy](../SECURITY.md).
