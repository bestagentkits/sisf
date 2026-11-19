# Real smoke run report

- Date: 2026-11-19 12:15:02 +07
- Finished: 2026-11-19 12:18:50 +07
- Outcome: **FAIL during assertions**
- Repository: bestagentkits/sisf-sandbox

## Tool versions

| Tool | Version |
|---|---|
| gh | gh version 2.85.0 (2026-01-14) |
| claude | 2.1.323 (Claude Code) |
| codex | codex-cli 0.167.2 |
| docker | 29.8.2 |
| go | go version go1.25.14 darwin/arm64 |
| sisf source revision | 3b3ba72f7d0c1210846268476f5179db51133db4 |

## Issue and pull requests

- issue: https://github.com/bestagentkits/sisf-sandbox/issues/36

## Run IDs

- run-c5172bfe956f5ad5b0b760ea

## Timings per phase

| Phase | Took |
|---|---|
| preflight and build | 11s |
| build sandbox | 1m22s |
| install, up, doctor and policy | 1m23s |
| run (issue opened to closed) | 47s |
| assertions | 4s |
| teardown | 1s |

## States visited (factory journal)

| State | Run | Entered | Time in state |
|---|---|---|---|
| Intake |  | 2026-11-19 12:18:13 +07 | 0s |
| ScopeDraft |  | 2026-11-19 12:18:13 +07 | 0s |
| Ready |  | 2026-11-19 12:18:13 +07 | 0s |
| Claimed | run-c5172bfe956f5ad5b0b760ea | 2026-11-19 12:18:13 +07 | 0s |
| Developing | run-c5172bfe956f5ad5b0b760ea | 2026-11-19 12:18:14 +07 | 29s |
| Blocked | run-c5172bfe956f5ad5b0b760ea | 2026-11-19 12:18:42 +07 | - |

## Receipt

candidate digest none, release digest none; the supervisor recorded no QA verdict for the run

## Cost from the usage ledger

mode capped, spent verified 0 micros, spent unverified 1000000 micros, reserved 0 micros, available 4000000 micros, warnings ["some usage is unverified: the provider could not confirm the amount" "some usage had no cost reported by its provider: such usage counts against the budget only through the amount reserved for its run, of which only the part not covered by the costs reported for that run can be charged when the run closes, possibly nothing, and does not count when nothing was reserved, as with an unlimited budget and a run estimate of 0" "the provider has no hard cap: the factory limits what it starts, but the provider may still charge beyond it"]

## Assertions

| Check | Result | Evidence |
|---|---|---|
| sandbox is private | pass | bestagentkits/sisf-sandbox PRIVATE |
| sandbox has no open factory work | pass | no open issue with a sisf: label and no open pull request |
| leftover candidate branches | info | 22 from earlier runs, left in place (delete them by hand once their pull requests are merged or closed): sisf/release/run-2db98a01de35cbf7142c349c, sisf/release/run-682c52dd8d8ec61c4b0f9028, sisf/release/run-70e98cbc2fc928c9a7754305, sisf/release/run-820073e0ea300f2883b6306d, sisf/release/run-a34cc1a5d3deea3db50452d7, sisf/release/run-c14a8afab0491f022090a382, sisf/release/run-fb25d46e42ef4ca4a8e3365c, sisf/sisf-sisf-sandbox/run-2165aefea96700906043a861-1, sisf/sisf-sisf-sandbox/run-2165aefea96700906043a861-2, sisf/sisf-sisf-sandbox/run-2165aefea96700906043a861-3, sisf/sisf-sisf-sandbox/run-2db98a01de35cbf7142c349c-1, sisf/sisf-sisf-sandbox/run-682c52dd8d8ec61c4b0f9028-1, sisf/sisf-sisf-sandbox/run-70e98cbc2fc928c9a7754305-1, sisf/sisf-sisf-sandbox/run-820073e0ea300f2883b6306d-1, sisf/sisf-sisf-sandbox/run-851258e429f8ec41145cbcd6-1, sisf/sisf-sisf-sandbox/run-8aebad583227afb3e130e525-1, sisf/sisf-sisf-sandbox/run-a34cc1a5d3deea3db50452d7-1, sisf/sisf-sisf-sandbox/run-b00aa4c28872003b5abb0300-1, sisf/sisf-sisf-sandbox/run-c14a8afab0491f022090a382-1, sisf/sisf-sisf-sandbox/run-cbb81eb3c71d07fb5b0d34a2-1, sisf/sisf-sisf-sandbox/run-d0bc4d7da245962d0e7ba7a1-1, sisf/sisf-sisf-sandbox/run-fb25d46e42ef4ca4a8e3365c-1 |
| sandbox network has no direct way out | pass | a direct connection from sisf-smoke-toqh9c-sandbox failed |
| sandboxed build of the sandbox's head | pass | sha256:5fc9798f0d16572363fd525296fa281a3c0035c764607c89eb97b6a6fb2c5d95, tests passed; the program it exported for darwin/arm64 is 8067986 bytes and hashes to that digest |
| sisf doctor | pass |                                (qa_agent_binary, the supervisor's --qa-agent-binary), which is                                trusted on your word to run it in a sandbox; nothing checks that it                                does  21 ok, 6 warnings, 0 failures |
| issue closed by the factory | fail | GitHub "OPEN", factory record "Blocked", last journal state "Blocked", inspect error <nil> |
| exactly one progress comment | pass | 1 progress comments, error <nil> |
| pull request linked to the issue | fail | the run recorded no pull request |
| pull request merged in the sandbox | fail | the run recorded no pull request |
| the candidate's own build served through the edge before the merge | fail | the edge never reported the candidate's digest none as the running revision |
| requested change merged on the default branch | fail | no merged pull request |
| signed QA receipt scoring at least 85 | fail | run "run-c5172bfe956f5ad5b0b760ea": the supervisor recorded no QA verdict for the run, error <nil> |
| QA receipt signature verifies | not checked | no CLI or route verifies a receipt; the run checked the supervisor's record of a signed receipt and that the issue passed the QA guard into ReleaseCandidate |
| release completed | fail | the supervisor recorded no release for the run; journal through QAEvaluation, ReleaseCandidate, Verified and Closed: false; error <nil> |
| application healthy through the edge | pass | GET /healthz on the edge: HTTP 200, body "{\"revision\":\"sha256:a09c671f89f14cb0b27a18d52eb078d09ea70acfe487f1ef5b6deb47ef693d5d\",\"status\":\"ok\"}\n", error <nil> |
| usage ledger non-empty | pass | mode capped, spent verified 0 micros, spent unverified 1000000 micros, reserved 0 micros, available 4000000 micros, warnings ["some usage is unverified: the provider could not confirm the amount" "some usage had no cost reported by its provider: such usage counts against the budget only through the amount reserved for its run, of which only the part not covered by the costs reported for that run can be charged when the run closes, possibly nothing, and does not count when nothing was reserved, as with an unlimited budget and a run estimate of 0" "the provider has no hard cap: the factory limits what it starts, but the provider may still charge beyond it"] |
| the factory addressed only the sandbox | pass | factory.yaml names the sandbox; 1 issue(s) in its journal and 0 pull request(s) of its run; manifest error <nil>, journal error <nil> |
| other activity of the gh account | info | 2 public event(s) of the gh account since the start named another repository; they are other work of the same account, not counted against the factory, and not named here |
| release record pull request closed | info | the run left no release record open |

## Deviations and failures

- the run ended in Blocked
- issue closed by the factory: GitHub "OPEN", factory record "Blocked", last journal state "Blocked", inspect error <nil>
- pull request linked to the issue: the run recorded no pull request
- pull request merged in the sandbox: the run recorded no pull request
- the candidate's own build served through the edge before the merge: the edge never reported the candidate's digest none as the running revision
- requested change merged on the default branch: no merged pull request
- signed QA receipt scoring at least 85: run "run-c5172bfe956f5ad5b0b760ea": the supervisor recorded no QA verdict for the run, error <nil>
- release completed: the supervisor recorded no release for the run; journal through QAEvaluation, ReleaseCandidate, Verified and Closed: false; error <nil>

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
- `gh issue list --repo bestagentkits/sisf-sandbox --state open --json number,labels --limit 1000`
- `gh pr list --repo bestagentkits/sisf-sandbox --state open --json number --limit 1000`
- `gh api --paginate repos/bestagentkits/sisf-sandbox/git/matching-refs/heads/sisf/ --jq .[].ref`
- `go build -o /private/tmp/claude-502/smoke-tmp/TestRealLifecycleSmoke319491853/001/bin/sisf ./cmd/sisf`
- `/private/tmp/claude-502/smoke-tmp/TestRealLifecycleSmoke319491853/001/bin/sisf init --dev --mode standalone --dir /private/tmp/claude-502/smoke-tmp/TestRealLifecycleSmoke319491853/001/factory --repo bestagentkits/sisf-sandbox --github-credential gh-cli --budget-cap 5 --alert-threshold 2 --coder-agent claude --coder-model claude-sonnet-4-5 --qa-judge agent --qa-agent codex --qa-model gpt-5-codex --agent-home ~ --workspace-source /private/tmp/claude-502/smoke-tmp/TestRealLifecycleSmoke319491853/001/sandbox --exec docker --exec-docker-endpoint http://[REDACTED:ip]:1 --exec-docker-network sisf-smoke-probe --egress-proxy http://egress:3128 --runner-image [REDACTED:ip]:1/sisf-smoke-runner@sha256:0000000000000000000000000000000000000000000000000000000000000000 --qa-agent-binary /nonexistent/qa-codex-launcher --build-command /usr/local/bin/sisf-smoke-build --github-reconcile-interval 15s --help`
- `/private/tmp/claude-502/smoke-tmp/TestRealLifecycleSmoke319491853/001/bin/sisf init --dev --mode standalone --dir /private/tmp/claude-502/smoke-tmp/TestRealLifecycleSmoke319491853/001/factory --repo bestagentkits/sisf-sandbox --github-credential gh-cli --budget-cap 5 --alert-threshold 2 --qa-judge agent --qa-agent codex --exec docker --exec-docker-endpoint http://[REDACTED:ip]:1 --exec-docker-network sisf-smoke-probe --egress-proxy http://egress:3128 --runner-image [REDACTED:ip]:1/sisf-smoke-runner@sha256:0000000000000000000000000000000000000000000000000000000000000000 --qa-agent-binary /nonexistent/qa-codex-launcher --build-command /usr/local/bin/sisf-smoke-build --github-reconcile-interval 15s --help`
- `/private/tmp/claude-502/smoke-tmp/TestRealLifecycleSmoke319491853/001/bin/sisf up --dir /private/tmp/claude-502/smoke-tmp/TestRealLifecycleSmoke319491853/001/factory --mode standalone --source /Volumes/GOON/claude/sisf/feat-consecutive-releases-b0355c --help`
- `/private/tmp/claude-502/smoke-tmp/TestRealLifecycleSmoke319491853/001/bin/sisf down --dir /private/tmp/claude-502/smoke-tmp/TestRealLifecycleSmoke319491853/001/factory --help`
- `gh repo clone bestagentkits/sisf-sandbox /private/tmp/claude-502/smoke-tmp/TestRealLifecycleSmoke319491853/001/sandbox`
- `/usr/local/bin/docker context inspect --format {{.Endpoints.docker.Host}}`
- `/usr/local/bin/docker network create --internal sisf-smoke-toqh9c-sandbox`
- `/usr/local/bin/docker network create sisf-smoke-toqh9c-out`
- `/usr/local/bin/docker run -d --name sisf-smoke-toqh9c-registry -p [REDACTED:ip]:58893:5000 registry:2`
- `/usr/local/bin/docker build -q -f /Volumes/GOON/claude/sisf/feat-consecutive-releases-b0355c/tests/smoke/sandbox/runner.Dockerfile -t [REDACTED:ip]:58893/sisf-smoke-runner:sisf-smoke-toqh9c /Volumes/GOON/claude/sisf/feat-consecutive-releases-b0355c/tests/smoke/sandbox`
- `/usr/local/bin/docker push -q [REDACTED:ip]:58893/sisf-smoke-runner:sisf-smoke-toqh9c`
- `/usr/local/bin/docker image inspect --format {{join .RepoDigests " "}} [REDACTED:ip]:58893/sisf-smoke-runner:sisf-smoke-toqh9c`
- `/usr/local/bin/docker build -q -f /Volumes/GOON/claude/sisf/feat-consecutive-releases-b0355c/build/edge.Dockerfile -t sisf-smoke-toqh9c-edge:run /Volumes/GOON/claude/sisf/feat-consecutive-releases-b0355c`
- `/usr/local/bin/docker build -q -f /Volumes/GOON/claude/sisf/feat-consecutive-releases-b0355c/tests/smoke/sandbox/qa-codex.Dockerfile --build-arg CODEX_VERSION=0.167.2 -t sisf-smoke-toqh9c-qa-codex:run /Volumes/GOON/claude/sisf/feat-consecutive-releases-b0355c/tests/smoke/sandbox`
- `/usr/local/bin/docker create --name sisf-smoke-toqh9c-egress --network sisf-smoke-toqh9c-out --read-only --cap-drop ALL --security-opt no-new-privileges sisf-smoke-toqh9c-edge:run edge --listen=[REDACTED:ip]:8080 --upstream=http://[REDACTED:ip]:9 --egress-listen=egress:3128 --allow-non-loopback`
- `/usr/local/bin/docker network connect --alias egress sisf-smoke-toqh9c-sandbox sisf-smoke-toqh9c-egress`
- `/usr/local/bin/docker start sisf-smoke-toqh9c-egress`
- `git init -q --bare /private/tmp/claude-502/smoke-tmp/TestRealLifecycleSmoke319491853/001/mirror/sandbox.git`
- `git -C /private/tmp/claude-502/smoke-tmp/TestRealLifecycleSmoke319491853/001/mirror/sandbox.git config uploadpack.allowAnySHA1InWant true`
- `git -C /private/tmp/claude-502/smoke-tmp/TestRealLifecycleSmoke319491853/001/mirror/sandbox.git -c credential.helper= -c credential.helper=!gh auth git-credential fetch -q --prune https://github.com/bestagentkits/sisf-sandbox.git +refs/heads/*:refs/heads/*`
- `/usr/local/bin/docker run -d --name sisf-smoke-toqh9c-git --network sisf-smoke-toqh9c-sandbox --network-alias git-mirror --user 65534:65534 -e GIT_CONFIG_COUNT=1 -e GIT_CONFIG_KEY_0=safe.directory -e GIT_CONFIG_VALUE_0=* -v /private/tmp/claude-502/smoke-tmp/TestRealLifecycleSmoke319491853/001/mirror:/srv/git:ro --read-only --cap-drop ALL --security-opt no-new-privileges [REDACTED:ip]:58893/sisf-smoke-runner@sha256:525fe67aebac6dd133d564bf08dba74a79241ae577ad8addfccbe1e9c8da385f git daemon --base-path=/srv/git --export-all --reuseaddr /srv/git`
- `/usr/local/bin/docker run --rm --name sisf-smoke-toqh9c-isolation --network sisf-smoke-toqh9c-sandbox --read-only --cap-drop ALL [REDACTED:ip]:58893/sisf-smoke-runner@sha256:525fe67aebac6dd133d564bf08dba74a79241ae577ad8addfccbe1e9c8da385f wget -q -T 5 -O /dev/null http://[REDACTED:ip]/`
- `/private/tmp/claude-502/smoke-tmp/TestRealLifecycleSmoke319491853/001/bin/sisf init --dev --mode standalone --dir /private/tmp/claude-502/smoke-tmp/TestRealLifecycleSmoke319491853/001/factory --repo bestagentkits/sisf-sandbox --github-credential gh-cli --budget-cap 5 --alert-threshold 2 --coder-agent claude --coder-model claude-sonnet-4-5 --qa-judge agent --qa-agent codex --qa-model gpt-5-codex --agent-home ~ --workspace-source /private/tmp/claude-502/smoke-tmp/TestRealLifecycleSmoke319491853/001/sandbox --exec docker --exec-docker-endpoint http://[REDACTED:ip]:58894 --exec-docker-network sisf-smoke-toqh9c-sandbox --egress-proxy http://egress:3128 --runner-image [REDACTED:ip]:58893/sisf-smoke-runner@sha256:525fe67aebac6dd133d564bf08dba74a79241ae577ad8addfccbe1e9c8da385f --qa-agent-binary /private/tmp/claude-502/smoke-tmp/TestRealLifecycleSmoke319491853/001/bin/qa-codex-launcher --build-command /usr/local/bin/sisf-smoke-build --github-reconcile-interval 15s`
- `/private/tmp/claude-502/smoke-tmp/TestRealLifecycleSmoke319491853/001/bin/sisf docker-filter --role runner --listen [REDACTED:ip]:58894 --factory-id sisf-sisf-sandbox --network sisf-smoke-toqh9c-sandbox --image [REDACTED:ip]:58893/sisf-smoke-runner@sha256:525fe67aebac6dd133d564bf08dba74a79241ae577ad8addfccbe1e9c8da385f --socket ~/.orbstack/run/docker.sock`
- `gh api repos/bestagentkits/sisf-sandbox/commits/HEAD --jq .sha`
- `/private/tmp/claude-502/smoke-tmp/TestRealLifecycleSmoke319491853/001/bin/sisf up --dir /private/tmp/claude-502/smoke-tmp/TestRealLifecycleSmoke319491853/001/factory --mode standalone --source /Volumes/GOON/claude/sisf/feat-consecutive-releases-b0355c`
- `/private/tmp/claude-502/smoke-tmp/TestRealLifecycleSmoke319491853/001/bin/sisf doctor --dir /private/tmp/claude-502/smoke-tmp/TestRealLifecycleSmoke319491853/001/factory`
- `/private/tmp/claude-502/smoke-tmp/TestRealLifecycleSmoke319491853/001/factory/bin/factoryd policy approve --revision 6d0bd2c8532008c6cfac8d377a9d33ba55509c80`
- `/private/tmp/claude-502/smoke-tmp/TestRealLifecycleSmoke319491853/001/bin/sisf login --url http://[REDACTED:ip]:7463 --dir /private/tmp/claude-502/smoke-tmp/TestRealLifecycleSmoke319491853/001/factory --name sisf-smoke`
- `gh issue create --repo bestagentkits/sisf-sandbox --title add GET /version returning the build revision --body ## Scope

Add a `GET /version` route to the application that answers with the build
revision the process runs as, as plain text.

## Affected features

- Revision reporting

## Definition of Done

- `GET /version` answers 200 with the build revision as plain text.
- A Go test covers the `/version` handler.
- `go test ./...` passes.
 --label sisf:ready --label kind:improvement`
- `/private/tmp/claude-502/smoke-tmp/TestRealLifecycleSmoke319491853/001/bin/sisf call runs.inspect --input {"issue":"bestagentkits/sisf-sandbox#36"}`
- `gh issue view 36 --repo bestagentkits/sisf-sandbox --json state,stateReason --jq .state + " " + .stateReason`
- `gh api --paginate repos/bestagentkits/sisf-sandbox/issues/36/comments?per_page=100 --jq [.[] | select(.body | contains("<!-- sisf:progress "))] | length`
- `/private/tmp/claude-502/smoke-tmp/TestRealLifecycleSmoke319491853/001/bin/sisf call budget.get --input {}`
- `gh api users/<gh-login>/events?per_page=100 --jq .[] | [.created_at, .repo.name, .type] | @tsv`
- `gh pr list --repo bestagentkits/sisf-sandbox --state open --json number,headRefName --limit 1000`
- `/private/tmp/claude-502/smoke-tmp/TestRealLifecycleSmoke319491853/001/bin/sisf down --dir /private/tmp/claude-502/smoke-tmp/TestRealLifecycleSmoke319491853/001/factory`
- `/usr/local/bin/docker ps -aq --filter name=^sisf-smoke-toqh9c-`
- `/usr/local/bin/docker ps -aq --filter label=sisf.smoke=sisf-smoke-toqh9c`
- `/usr/local/bin/docker ps -aq --filter network=sisf-smoke-toqh9c-sandbox`
- `/usr/local/bin/docker rm -f 6743d383c04b`
- `/usr/local/bin/docker rm -f 468ecb487484`
- `/usr/local/bin/docker rm -f 50d54224201a`
- `/usr/local/bin/docker network rm sisf-smoke-toqh9c-sandbox`
- `/usr/local/bin/docker network rm sisf-smoke-toqh9c-out`
- `/usr/local/bin/docker image rm sisf-smoke-toqh9c-edge:run`
- `/usr/local/bin/docker image rm sisf-smoke-toqh9c-qa-codex:run`
- `/usr/local/bin/docker image rm [REDACTED:ip]:58893/sisf-smoke-runner:sisf-smoke-toqh9c`

## Evidence

```text
sisf up:
building hello-app
building hello-app-synthetic
building factoryd
building sisf-supervisor
building sisf-edge
sign-in at the development identity provider is locked: open http://[REDACTED:ip]:7468/_sim/unlock and enter the secret in /private/tmp/claude-502/smoke-tmp/TestRealLifecycleSmoke319491853/001/factory/secrets/control/dev-sign-in
factory sisf-sisf-sandbox is up: control http://[REDACTED:ip]:7463, app via edge http://[REDACTED:ip]:7460
started edge
started sim-oidc
started supervisor
started dispatcher
started control
stopped edge
stopped sim-oidc
stopped control
stopped supervisor
stopped dispatcher
```

```text
control.log:
time=2026-11-19T12:17:48.324+07:00 level=INFO msg="control plane listening" factory=sisf-sisf-sandbox addr=[REDACTED:ip]:7463
time=2026-11-19T12:17:49.403+07:00 level=INFO msg="reconciliation found issue changes no webhook delivered" issues=16
time=2026-11-19T12:17:49.404+07:00 level=WARN msg="a control pass failed" code=POLICY_HOLD error="POLICY_HOLD: the Owner has not approved a policy revision yet"
time=2026-11-19T12:17:50.419+07:00 level=WARN msg="a control pass failed" code=POLICY_HOLD error="POLICY_HOLD: the Owner has not approved a policy revision yet"
time=2026-11-19T12:17:52.426+07:00 level=WARN msg="a control pass failed" code=POLICY_HOLD error="POLICY_HOLD: the Owner has not approved a policy revision yet"
time=2026-11-19T12:18:11.255+07:00 level=INFO msg="reconciliation found issue changes no webhook delivered" issues=1
time=2026-11-19T12:18:42.665+07:00 level=WARN msg="blocked an issue whose run stopped without a candidate; it waits for the Owner's retry" issue=bestagentkits/sisf-sandbox#36 run=run-c5172bfe956f5ad5b0b760ea cause=run_exhausted code=INTERNAL
time=2026-11-19T12:18:48.911+07:00 level=WARN msg="the factory's edit of an issue could not be recorded; the next poll hands it to intake once more" issue=bestagentkits/sisf-sandbox#36 code=UNAVAILABLE
```

```text
dispatcher.log:
time=2026-11-19T12:17:47.660+07:00 level=INFO msg="dispatcher running" factory=sisf-sisf-sandbox owner=[REDACTED:email]#b6df6a30 agent=claude health=[REDACTED:ip]:7465
time=2026-11-19T12:18:14.008+07:00 level=INFO msg="issue claimed" issue=bestagentkits/sisf-sandbox#36
time=2026-11-19T12:18:24.166+07:00 level=WARN msg="a coding attempt failed" issue=bestagentkits/sisf-sandbox#36 run=run-c5172bfe956f5ad5b0b760ea attempt=1 code=INTERNAL detail="the agent run ended failed: the CLI reported success: There's an issue with the selected model (claude-sonnet-4-5). It may not exist or you may not have access to it. Run --model to pick a different model."
time=2026-11-19T12:18:34.336+07:00 level=WARN msg="a coding attempt failed" issue=bestagentkits/sisf-sandbox#36 run=run-c5172bfe956f5ad5b0b760ea attempt=2 code=INTERNAL detail="the agent run ended failed: the CLI reported success: There's an issue with the selected model (claude-sonnet-4-5). It may not exist or you may not have access to it. Run --model to pick a different model."
time=2026-11-19T12:18:42.365+07:00 level=WARN msg="a coding attempt failed" issue=bestagentkits/sisf-sandbox#36 run=run-c5172bfe956f5ad5b0b760ea attempt=3 code=INTERNAL detail="the agent run ended failed: the CLI reported success: There's an issue with the selected model (claude-sonnet-4-5). It may not exist or you may not have access to it. Run --model to pick a different model."
time=2026-11-19T12:18:42.365+07:00 level=ERROR msg="a run used every attempt and stopped; the issue stays where it is until the Owner acts" issue=bestagentkits/sisf-sandbox#36
```

```text
edge.log:
time=2026-11-19T12:17:47.447+07:00 level=INFO msg="edge serving" listen=[REDACTED:ip]:7460 admin=true egress=false
```

```text
sim-oidc.log:
fake identity provider listening on http://[REDACTED:ip]:7468 (issuer http://[REDACTED:ip]:7468)
```

```text
supervisor.log:
supervisor: self-check passed: executable sha256:6b98ab0a9d4df968df01b92bead7227d14cf72510a77336ff5c718a706609778 is the one pinned by the release manifest signed with ed25519:105f004ffbfb36208c2cfdec83d4eb2de45eb8dd63f1b4483f620bb2d227eea6
supervisor: serving status on admin socket [REDACTED:ip]:7464
time=2026-11-19T12:17:48.676+07:00 level=INFO msg="the last known good release serves the application" unit=app
supervisor: stopped
```
