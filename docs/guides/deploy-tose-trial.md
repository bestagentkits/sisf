# Trial factory on TOSE

How to host a trial SISF factory on TOSE, a container platform that runs each workload as a hardened Kubernetes pod. This trial proves that the factory installs, starts and answers its health check on Linux. It is not a production install, and in this release it cannot process real issues on TOSE; see [What TOSE cannot provide](#what-tose-cannot-provide). The trial serves only `bestagentkits/sisf-sandbox`.

**Status, 2026-11-13: not yet run on TOSE.** No TOSE installation exists. Only step 1 below was executed, on Linux (arm64) in a local container under the limits of an unprivileged pod (`--cap-drop ALL --security-opt no-new-privileges`, `--cpus 2 --memory 4g`). Steps 2 to 5 were not run: pushing the image was refused because no registry credential with `write:packages` was available. The plan's blockers on TOSE (image scan, wallet, pulling a private image) are unchecked. The manifest was only validated by TOSE.

The deploy configuration is in [`deploy/tose/`](../../deploy/tose/): the image (`Dockerfile`), its entry point (`entrypoint.sh`), the proxy that publishes the health answer (`nginx.conf`) and the TOSE template manifest (`template.yaml`).

## Mode chosen and why

The trial runs one **Standalone** factory inside one container.

- **Docker mode is not possible.** It runs one container per unit and needs a Docker daemon. A TOSE workload is an unprivileged pod: no Docker daemon or socket is provided, and one cannot be started without privileges. The trial was checked locally under the same limits (`--cap-drop ALL --security-opt no-new-privileges`). The entry point logs this at start: `docker CLI: absent, docker socket: absent`.
- **A Standalone build sandbox is not possible either**, for the same reason: it needs Docker for the runner containers, the internal network and the request filter ([building in a Docker sandbox](install.md#building-in-a-docker-sandbox)).
- **Standalone without a sandbox** is what remains. The units run as local processes of the container's unprivileged user (uid 10001), and `sisf up` rebuilds them from the checkout in the image at every start.

### Profile inside the container

The entry point creates the install once, on the persistent volume at `/data/factory`, and picks the profile from the environment:

| `GH_TOKEN` | Install | Result |
|---|---|---|
| not set | `sisf init --profile sim` for `bestagentkits/sisf-sandbox`: fake GitHub, fake sign-in, scripted agent | healthy; `sisf doctor` 22 OK, 2 WARN, 0 FAIL |
| set | `sisf init --dev --github-credential gh-cli` for `bestagentkits/sisf-sandbox`: the factory acts as the token's account | degraded: the supervisor refuses to start, see below; kept only to reproduce that locally |

The TOSE manifest declares no `GH_TOKEN`, so a TOSE installation always runs the simulation profile: with the token set, `/healthz`, which is the readiness check, answers 503 and the installation would never become ready. The entry point refuses any `SISF_REPO` other than `bestagentkits/sisf-sandbox`. The profile is fixed when the install is created; to change it, remove the install (below) and start again.

## What TOSE cannot provide

Measured in the trial image on Linux with no Docker and all capabilities dropped, from source `83d592d`:

- **No real run on TOSE.** Outside the simulation the supervisor builds an agent's code only in a sandbox of an isolating backend. With only the local backend it refuses to start, and `sisf up` restarts it about once a second ([#67](https://github.com/bestagentkits/sisf/issues/67)). Its log says `PERMISSION_DENIED: the local execution backend cannot isolate untrusted code; configure the docker execution backend`; `sisf doctor` fails `zone application`, `edge to app` and `rollback`, and `/healthz` answers 503 `"status":"degraded"`. So a factory on TOSE cannot handle real issues of the sandbox repository in this release. Real runs need a host with Docker (a VM), where the [Standalone build sandbox](install.md#building-in-a-docker-sandbox) or Docker mode works.
- **No shell on the factory.** Putting a policy in force and retrying a Blocked issue run only through `factoryd` on the factory's host ([policy](policy-and-approvals.md#putting-the-first-policy-in-force)). The TOSE tools used here (MCP and the `tose` CLI 0.1.2) offer no command execution in the pod.
- **No reachable dashboard.** Every factory port is bound to 127.0.0.1. nginx publishes only `GET /healthz` of the control zone on port 8080; every other path answers 404 and other methods 403. Signing in, the REST API, MCP, webhooks and the public report route are not reachable from outside. This is deliberate: a development install signs the Owner in through the fake identity provider, which must never be public.
- **Slow start.** `sisf up` compiles the factory on every start: with an empty build cache and the manifest's limits (2 CPUs, 4 GiB), `/healthz` answered 200 after 279 seconds on arm64, without running out of memory. The build cache is kept on the volume (`GOCACHE=/data/cache/go-build`), so later starts are faster. The template gives the pod 1200 seconds to become ready.
- **An image pinned by digest.** The TOSE template takes its source as an image pinned by digest or a GitHub repository at a pinned commit. This repository keeps its trial Dockerfile under `deploy/tose/`, so the trial uses the image route: the image must be pushed to a registry TOSE can pull from.
- **A volume the container's user can write.** The entry point runs as uid 10001 and refuses to start when `/data` is not writable by it (`trial: /data is not writable by uid 10001`); a disk owned by root with mode 0755 is refused this way.

## Before you start

You need:

- Docker with `buildx` on the machine that builds the image.
- A registry credential that may push the image, for example a GitHub token with `write:packages` for `ghcr.io/bestagentkits`, and either a public package or a registry credential added to the TOSE workspace for a private one.
- An owner or admin of the TOSE workspace to approve the plan in the TOSE dashboard. Every TOSE change is approved there by a person.
No GitHub, Claude or Codex credential is used: no agent runs on TOSE in this release. On a host with Docker, a Standalone factory uses an existing Claude or Codex CLI login through `--agent-home` and `--qa-home` ([real agents](install.md#real-agents-in-standalone)).

## Steps

1. **Check the image locally first.** From the repository root:

   ```sh
   docker build -f deploy/tose/Dockerfile -t sisf-trial:local .
   docker run -d --name sisf-trial --cap-drop ALL --security-opt no-new-privileges \
     -p 18080:8080 -v sisf-trial-data:/data sisf-trial:local
   curl -s http://127.0.0.1:18080/healthz      # 200 and "status":"ok" once the factory is up
   docker exec sisf-trial sisf doctor --dir /data/factory
   ```

   Pass `--build-arg SISF_VERSION=<version> --build-arg SISF_COMMIT=$(git rev-parse HEAD)` to stamp `sisf version`; the build context carries no git metadata.

2. **Build and push the image for the cluster's platform**, then note its digest:

   ```sh
   docker buildx build --platform linux/amd64 -f deploy/tose/Dockerfile \
     --build-arg SISF_COMMIT=$(git rev-parse HEAD) \
     -t ghcr.io/bestagentkits/sisf-trial:<commit> --push .
   docker buildx imagetools inspect ghcr.io/bestagentkits/sisf-trial:<commit>
   ```

3. **Pin the digest in the manifest.** Replace the placeholder digest in `deploy/tose/template.yaml` with the pushed one. The manifest asks for 2 cores, 4 GiB, a 10 GiB volume at `/data`, a readiness check `GET /healthz` on port 8080 and the optional secret `GH_TOKEN`.

4. **Store, publish and plan the template** with the TOSE tools: `validate_template`, `create_template_draft`, `publish_template` with the reviewed manifest hash, then `plan_template_deployment` with `definition: sisf-trial` and the name `sisf-trial`. The plan creates nothing; read its blockers and its hourly cost ceiling.

5. **Approve.** A workspace owner or admin opens the plan's approval URL, reviews it and approves. Then `deploy_template` with the plan's id and hash installs it. Follow it with `get_template_deployment` and its `installation_id`.

## Check health

- The installation is ready when its readiness check passes: `GET <installation URL>/healthz` answers 200 with `"status":"ok"` and every zone `ok`. A 503 with `"status":"degraded"` means a zone is down; with `GH_TOKEN` set, in a local run, this is the expected supervisor refusal above.
- The pod log shows the entry point's `trial:` lines (version, platform, whether Docker is present, which profile was created), one `building ...` line per program and `factory sisf-sisf-sandbox is up`. Read it with `read_logs` or `get_template_deployment` with `log_lines`.
- `sisf doctor` needs a shell in the pod, which TOSE does not offer; run it in the local container of step 1.

## Roll back

- To an earlier version of the template: `plan_template_deployment` with the `installation_id` and `rollback: true`, approved like any plan. The volume and its install are kept.
- The image is pinned by digest, so a new factory version is a new image, a new template version and an upgrade plan. `sisf up` applies schema migrations on start and has no down migrations ([maintenance](maintenance.md#upgrade)); to undo one, remove the install and start again.

## Remove the trial

1. Delete the installation in the TOSE dashboard (or `tose template delete sisf-trial` with a valid API key). This removes the workload, its volume and the install on it, including its secrets and signing keys.
2. Delete the `sisf-trial` template definition from the workspace registry, if it is no longer wanted.
3. Delete the image package `sisf-trial` from the registry.
4. Locally: `docker rm -f sisf-trial && docker volume rm sisf-trial-data`.
