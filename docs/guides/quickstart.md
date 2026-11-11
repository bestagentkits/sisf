# Quickstart

Run one factory on your machine against a simulated GitHub, sign in, and send a first issue through it. The simulation profile needs no GitHub token, no real identity provider and no paid model: it uses a fake GitHub on 127.0.0.1:7467, a fake identity provider on 127.0.0.1:7468 and a scripted agent. For a factory that serves a real repository, see [installing a factory](install.md).

## Prerequisites

- Linux or macOS, Go 1.25 ([ADR 0019](../adr/0019-go-1-25-toolchain.md)), git, and `curl` and `jq` for the first issue.
- Docker with Compose, for Docker mode only.
- Ports 7460-7469 on 127.0.0.1 free: the factory binds exactly these, and one machine runs one factory ([why](install.md#one-factory-per-machine)).

## 1. Get the source and build the command line

```sh
git clone https://github.com/bestagentkits/sisf.git
cd sisf
go install ./cmd/sisf          # puts sisf in $(go env GOPATH)/bin; make sure that is on your PATH
sisf help                      # lists the commands
```

Run every later command from this checkout: `sisf up` builds the factory from the current directory (or from `--source`).

Choose an install directory outside the checkout, without spaces in its path, and export it so every command finds it:

```sh
export SISF_HOME=$HOME/.sisf/sim
```

## 2. Create and start the factory (Standalone)

Standalone is the trusted-code profile: the units run as local processes under one foreground `sisf up`.

```sh
sisf init --repo acme/hello --profile sim --confirm-unlimited --no-alert
```

Expected output (observed, paths shortened):

```text
factory sisf-hello created in <SISF_HOME>
sign in at http://127.0.0.1:7463 as the Owner http://127.0.0.1:7468#owner
warning: The root signing key is stored in <SISF_HOME>/secrets/control. This is the lower-assurance local profile: ...
next: sisf up --dir <SISF_HOME>
```

`--confirm-unlimited --no-alert` settles the budget, without which the factory starts no work ([ADR 0007](../adr/0007-budget-default-unlimited.md)). The secrets are written owner-only under `$SISF_HOME/secrets` and never printed.

Start it; it stays in the foreground:

```sh
sisf up
```

It prints a `building ...` line per program and ends with `factory sisf-hello is up: control http://127.0.0.1:7463, app via edge http://127.0.0.1:7460`.

In a second terminal (with `SISF_HOME` exported again):

```sh
sisf doctor
```

One line per check, `OK`, `WARN` or `FAIL`, and a summary. Every check should be `OK` or a `WARN` you can explain: `artifacts` (not every image is built until a release builds it) and `trust model` (Standalone runs trusted code only) are expected. A `FAIL` names its cause; see [troubleshooting](troubleshooting.md).

## 3. Sign in

Open `http://127.0.0.1:7463`. The simulation signs you in as the Owner (`http://127.0.0.1:7468#owner`) at the fake identity provider; there is nothing to click. Sign the CLI in the same way:

```sh
sisf login
```

It prints a URL and a code: open the URL, check that the code matches and approve. Then `sisf status` shows the overview. There is no Owner token; see [Signing in](operations.md#signing-in).

## 4. Run a first issue

Nothing moves until a policy is in force: the factory reads its rules from foundation files in the served repository, at a commit the Owner approved ([policy](policy-and-approvals.md#what-the-policy-is)). In the simulation, commit this repository's own foundation files to the fake GitHub, with the install's manifest as `.sisf/factory.yaml`, as the end-to-end tests do:

```sh
jq -n --arg repo acme/hello --rawfile m "$SISF_HOME/factory.yaml" \
  --rawfile a AGENTS.md --rawfile r REVIEW.md --rawfile d DECISIONS.md --rawfile f FEATURES.md \
  '{repo:$repo, files:{".sisf/factory.yaml":$m, "AGENTS.md":$a, "REVIEW.md":$r, "DECISIONS.md":$d, "FEATURES.md":$f}}' \
  > "$SISF_HOME/sim-policy.json"
SHA=$(curl -s -X POST --data @"$SISF_HOME/sim-policy.json" http://127.0.0.1:7467/_sim/commits | jq -r .sha)
curl -s -X PATCH -H 'Authorization: Bearer sim' --data "{\"sha\":\"$SHA\",\"force\":true}" \
  http://127.0.0.1:7467/repos/acme/hello/git/refs/heads/main > /dev/null
echo "$SHA"
```

Put that commit in force, as the Owner on this host (the subshell keeps the control zone's secrets out of your shell):

```sh
( set -a; . "$SISF_HOME/secrets/control/runtime.env"; set +a
  SISF_CONFIG="$SISF_HOME/factory.yaml" "$SISF_HOME/bin/factoryd" policy approve --revision "$SHA" )
```

Expected: `policy revision <sha> is in force (content sha256:<digest>)`.

Open the sample issue: the **Run a sample issue** button on the dashboard's Setup page (`/setup`), or

```sh
sisf call setup.sample_issue
```

It opens one issue, labelled `sisf:ready` and `kind:improvement`, asking for a small `GET /version` endpoint; the scripted agent answers it with a fixed change. Follow it:

```sh
sisf issues
sisf runs explain acme/hello#1     # the issue number sisf call printed
```

`runs explain` says which lifecycle state the issue is in and, if it is stopped, why and what moves it. The dashboard's Workboard (`/workboard`) shows the same. What each answer means: [Why is my issue stuck?](stuck-issues.md)

The fake GitHub keeps everything in memory. After a restart of the factory, post the same `sim-policy.json` again: the fake's commit id depends only on the content, so the approved revision is found again.

## 5. Stop and clean up

```sh
sisf stop          # or Ctrl-C in the terminal running sisf up; keeps everything
sisf down -v       # also removes the data (issues, state); secrets and factory.yaml stay
rm -rf "$SISF_HOME"
```

## Docker Compose mode

The same, with containers. `sisf up` starts one Compose project and returns.

```sh
export SISF_HOME=$HOME/.sisf/sim-docker
sisf init --repo acme/hello --profile sim --mode docker --confirm-unlimited --no-alert
sisf up
sisf doctor
```

On a fresh simulation install `doctor` reports 0 failures and one warning: the artifacts image is not built until a release builds it. Docker mode has no container sandboxing for untrusted code in this release ([security guide](security.md#limits-to-know)). The first-issue steps are the same, except that the policy is approved inside the control container:

```sh
docker exec sisf-hello-control factoryd policy approve --revision "$SHA"
```

Clean up with `sisf down -v`, which also removes the project's containers, networks and volumes.

## Next

- [Installing a factory](install.md): a development install against your own repository, a real install, real agents, running as a service.
- [Operations](operations.md) and the [CLI reference](cli-reference.md) for day-to-day use; [maintenance](maintenance.md) for upgrades and backups.
- [Incident runbook](incident.md) when something goes wrong.
