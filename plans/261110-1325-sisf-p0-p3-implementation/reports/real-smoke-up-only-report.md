# Real smoke run report

- Date: 2026-11-12 17:16:06 +07
- Finished: 2026-11-12 17:18:38 +07
- Outcome: **PASS (up only: nothing was spent)**
- Repository: bestagentkits/sisf-sandbox

## Tool versions

| Tool | Version |
|---|---|
| gh | gh version 2.85.0 (2026-01-14) |
| claude | 2.1.322 (Claude Code) |
| codex | codex-cli 0.167.2 |
| docker | 29.8.2 |
| go | go version go1.25.14 darwin/arm64 |
| sisf source revision | 5b01034ba2e9f6fd6e74dffa03b40933e5c810d7 |

## Issue and pull requests

- issue: none

## Run IDs

- none

## Timings per phase

| Phase | Took |
|---|---|
| preflight and build | 10s |
| build sandbox | 1m11s |
| install, up, doctor and sign-in | 1m7s |
| teardown | 1s |

## States visited (factory journal)

| State | Run | Entered | Time in state |
|---|---|---|---|

## Receipt

none

## Cost from the usage ledger

none

## Assertions

| Check | Result | Evidence |
|---|---|---|
| sandbox is private | pass | bestagentkits/sisf-sandbox PRIVATE |
| sandbox network has no direct way out | pass | a direct connection from sisf-smoke-todwj4-sandbox failed |
| sandboxed build of the sandbox's head | pass | sha256:1f25dc9922431ac3ec3b7b1d737962fb28cadada2b0fb95f526c6592826fa9dc, tests passed; the program it exported for darwin/arm64 is 8067874 bytes and hashes to that digest |
| sisf doctor | pass |                                (qa_agent_binary, the supervisor's --qa-agent-binary), which is                                trusted on your word to run it in a sandbox; nothing checks that it                                does  21 ok, 6 warnings, 0 failures |
| operator signed in | pass | budget.get answered: mode capped, spent verified 0 micros, spent unverified 0 micros, reserved 0 micros, available 5000000 micros, warnings ["the provider has no hard cap: the factory limits what it starts, but the provider may still charge beyond it"] |
| login renewal | pass | a forced renewal stored a new login and budget.get answered with it: mode capped, spent verified 0 micros, spent unverified 0 micros, reserved 0 micros, available 5000000 micros, warnings ["the provider has no hard cap: the factory limits what it starts, but the provider may still charge beyond it"] |

## Deviations and failures

- none

## Teardown

sisf down --dir <install> succeeded; sisf up exited; ports 7460-7469 free after teardown

## Command lines run

- `gh auth token`
- `gh auth status`
- `gh repo view bestagentkits/sisf-sandbox --json visibility,nameWithOwner`
- `gh label list --repo bestagentkits/sisf-sandbox --json name --limit 500`
- `gh api repos/bestagentkits/sisf-sandbox/contents/.sisf/factory.yaml --jq .path`
- `gh api user --jq .login`
- `gh --version`
- `claude --version`
- `codex --version`
- `docker version --format {{.Server.Version}}`
- `go version`
- `git rev-parse HEAD`
- `go build -o <run-dir>/bin/sisf ./cmd/sisf`
- `<run-dir>/bin/sisf init --dev --mode standalone --dir <run-dir>/factory --repo bestagentkits/sisf-sandbox --github-credential gh-cli --budget-cap 5 --alert-threshold 2 --coder-agent claude --coder-model claude-sonnet-5-5 --qa-judge agent --qa-agent codex --qa-model gpt-6.1-sol --agent-home ~ --workspace-source <run-dir>/sandbox --exec docker --exec-docker-endpoint http://[REDACTED:ip]:1 --exec-docker-network sisf-smoke-probe --egress-proxy http://egress:3128 --runner-image [REDACTED:ip]:1/sisf-smoke-runner@sha256:0000000000000000000000000000000000000000000000000000000000000000 --qa-agent-binary /nonexistent/qa-codex-launcher --build-command /usr/local/bin/sisf-smoke-build --github-reconcile-interval 15s --help`
- `<run-dir>/bin/sisf init --dev --mode standalone --dir <run-dir>/factory --repo bestagentkits/sisf-sandbox --github-credential gh-cli --budget-cap 5 --alert-threshold 2 --qa-judge agent --qa-agent codex --exec docker --exec-docker-endpoint http://[REDACTED:ip]:1 --exec-docker-network sisf-smoke-probe --egress-proxy http://egress:3128 --runner-image [REDACTED:ip]:1/sisf-smoke-runner@sha256:0000000000000000000000000000000000000000000000000000000000000000 --qa-agent-binary /nonexistent/qa-codex-launcher --build-command /usr/local/bin/sisf-smoke-build --github-reconcile-interval 15s --help`
- `<run-dir>/bin/sisf up --dir <run-dir>/factory --mode standalone --source <repo> --help`
- `<run-dir>/bin/sisf down --dir <run-dir>/factory --help`
- `gh repo clone bestagentkits/sisf-sandbox <run-dir>/sandbox`
- `/usr/local/bin/docker context inspect --format {{.Endpoints.docker.Host}}`
- `/usr/local/bin/docker network create --internal sisf-smoke-todwj4-sandbox`
- `/usr/local/bin/docker network create sisf-smoke-todwj4-out`
- `/usr/local/bin/docker run -d --name sisf-smoke-todwj4-registry -p [REDACTED:ip]:59368:5000 registry:2`
- `/usr/local/bin/docker build -q -f <repo>/tests/smoke/sandbox/runner.Dockerfile -t [REDACTED:ip]:59368/sisf-smoke-runner:sisf-smoke-todwj4 <repo>/tests/smoke/sandbox`
- `/usr/local/bin/docker push -q [REDACTED:ip]:59368/sisf-smoke-runner:sisf-smoke-todwj4`
- `/usr/local/bin/docker image inspect --format {{join .RepoDigests " "}} [REDACTED:ip]:59368/sisf-smoke-runner:sisf-smoke-todwj4`
- `/usr/local/bin/docker build -q -f <repo>/build/edge.Dockerfile -t sisf-smoke-todwj4-edge:run <repo>`
- `/usr/local/bin/docker build -q -f <repo>/tests/smoke/sandbox/qa-codex.Dockerfile --build-arg CODEX_VERSION=0.167.2 -t sisf-smoke-todwj4-qa-codex:run <repo>/tests/smoke/sandbox`
- `/usr/local/bin/docker create --name sisf-smoke-todwj4-egress --network sisf-smoke-todwj4-out --read-only --cap-drop ALL --security-opt no-new-privileges sisf-smoke-todwj4-edge:run edge --listen=[REDACTED:ip]:8080 --upstream=http://[REDACTED:ip]:9 --egress-listen=egress:3128 --allow-non-loopback`
- `/usr/local/bin/docker network connect --alias egress sisf-smoke-todwj4-sandbox sisf-smoke-todwj4-egress`
- `/usr/local/bin/docker start sisf-smoke-todwj4-egress`
- `git init -q --bare <run-dir>/mirror/sandbox.git`
- `git -C <run-dir>/mirror/sandbox.git config uploadpack.allowAnySHA1InWant true`
- `git -C <run-dir>/mirror/sandbox.git -c credential.helper= -c credential.helper=!gh auth git-credential fetch -q --prune https://github.com/bestagentkits/sisf-sandbox.git +refs/heads/*:refs/heads/*`
- `/usr/local/bin/docker run -d --name sisf-smoke-todwj4-git --network sisf-smoke-todwj4-sandbox --network-alias git-mirror --user 65534:65534 -e GIT_CONFIG_COUNT=1 -e GIT_CONFIG_KEY_0=safe.directory -e GIT_CONFIG_VALUE_0=* -v <run-dir>/mirror:/srv/git:ro --read-only --cap-drop ALL --security-opt no-new-privileges [REDACTED:ip]:59368/sisf-smoke-runner@sha256:ac218260676c9b07e4eebc8575a2593c3aa45ea0cb28e1049f5f1269ac62c5c7 git daemon --base-path=/srv/git --export-all --reuseaddr /srv/git`
- `/usr/local/bin/docker run --rm --name sisf-smoke-todwj4-isolation --network sisf-smoke-todwj4-sandbox --read-only --cap-drop ALL [REDACTED:ip]:59368/sisf-smoke-runner@sha256:ac218260676c9b07e4eebc8575a2593c3aa45ea0cb28e1049f5f1269ac62c5c7 wget -q -T 5 -O /dev/null http://[REDACTED:ip]/`
- `<run-dir>/bin/sisf init --dev --mode standalone --dir <run-dir>/factory --repo bestagentkits/sisf-sandbox --github-credential gh-cli --budget-cap 5 --alert-threshold 2 --coder-agent claude --coder-model claude-sonnet-5-5 --qa-judge agent --qa-agent codex --qa-model gpt-6.1-sol --agent-home ~ --workspace-source <run-dir>/sandbox --exec docker --exec-docker-endpoint http://[REDACTED:ip]:59369 --exec-docker-network sisf-smoke-todwj4-sandbox --egress-proxy http://egress:3128 --runner-image [REDACTED:ip]:59368/sisf-smoke-runner@sha256:ac218260676c9b07e4eebc8575a2593c3aa45ea0cb28e1049f5f1269ac62c5c7 --qa-agent-binary <run-dir>/bin/qa-codex-launcher --build-command /usr/local/bin/sisf-smoke-build --github-reconcile-interval 15s`
- `<run-dir>/bin/sisf docker-filter --role runner --listen [REDACTED:ip]:59369 --factory-id sisf-sisf-sandbox --network sisf-smoke-todwj4-sandbox --image [REDACTED:ip]:59368/sisf-smoke-runner@sha256:ac218260676c9b07e4eebc8575a2593c3aa45ea0cb28e1049f5f1269ac62c5c7 --socket ~/.orbstack/run/docker.sock`
- `gh api repos/bestagentkits/sisf-sandbox/commits/HEAD --jq .sha`
- `<run-dir>/bin/sisf up --dir <run-dir>/factory --mode standalone --source <repo>`
- `<run-dir>/bin/sisf doctor --dir <run-dir>/factory`
- `<run-dir>/bin/sisf login --url http://[REDACTED:ip]:7463 --dir <run-dir>/factory --name sisf-smoke`
- `<run-dir>/bin/sisf call budget.get --input {}`
- `<run-dir>/bin/sisf down --dir <run-dir>/factory`
- `/usr/local/bin/docker ps -aq --filter name=^sisf-smoke-todwj4-`
- `/usr/local/bin/docker ps -aq --filter label=sisf.smoke=sisf-smoke-todwj4`
- `/usr/local/bin/docker ps -aq --filter network=sisf-smoke-todwj4-sandbox`
- `/usr/local/bin/docker rm -f cbb69f7ac873`
- `/usr/local/bin/docker rm -f cc1b76334264`
- `/usr/local/bin/docker rm -f 0782c753c6b3`
- `/usr/local/bin/docker network rm sisf-smoke-todwj4-sandbox`
- `/usr/local/bin/docker network rm sisf-smoke-todwj4-out`
- `/usr/local/bin/docker image rm sisf-smoke-todwj4-edge:run`
- `/usr/local/bin/docker image rm sisf-smoke-todwj4-qa-codex:run`
- `/usr/local/bin/docker image rm [REDACTED:ip]:59368/sisf-smoke-runner:sisf-smoke-todwj4`
