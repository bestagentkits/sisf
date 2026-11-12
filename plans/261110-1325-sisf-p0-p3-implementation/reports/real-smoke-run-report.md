# Real smoke run report

- Date: 2026-11-12 17:18:59 +07
- Finished: 2026-11-12 17:54:09 +07
- Outcome: **PASS**
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

- issue: https://github.com/bestagentkits/sisf-sandbox/issues/33
- pull request: https://github.com/bestagentkits/sisf-sandbox/pull/34

## Run IDs

- run-70e98cbc2fc928c9a7754305

## Timings per phase

| Phase | Took |
|---|---|
| preflight and build | 13s |
| build sandbox | 38s |
| install, up, doctor and policy | 1m15s |
| run (issue opened to closed) | 32m48s |
| assertions | 12s |
| teardown | 1s |

## States visited (factory journal)

| State | Run | Entered | Time in state |
|---|---|---|---|
| Intake |  | 2026-11-12 17:21:19 +07 | 0s |
| ScopeDraft |  | 2026-11-12 17:21:19 +07 | 0s |
| Ready |  | 2026-11-12 17:21:19 +07 | 0s |
| Claimed | run-70e98cbc2fc928c9a7754305 | 2026-11-12 17:21:20 +07 | 0s |
| Developing | run-70e98cbc2fc928c9a7754305 | 2026-11-12 17:21:20 +07 | 1m30s |
| QAEvaluation | run-70e98cbc2fc928c9a7754305 | 2026-11-12 17:22:50 +07 | 38s |
| ReleaseCandidate | run-70e98cbc2fc928c9a7754305 | 2026-11-12 17:23:28 +07 | 4s |
| Canary | run-70e98cbc2fc928c9a7754305 | 2026-11-12 17:23:32 +07 | 0s |
| Monitoring | run-70e98cbc2fc928c9a7754305 | 2026-11-12 17:23:32 +07 | 30m1s |
| Verified | run-70e98cbc2fc928c9a7754305 | 2026-11-12 17:53:34 +07 | 12s |
| Closed | run-70e98cbc2fc928c9a7754305 | 2026-11-12 17:53:46 +07 | - |

## Receipt

candidate digest sha256:b9ca5507d472ab0f152cabdbe053bad812495fc04bfeb00a316e395abac53e16, release digest sha256:b9ca5507d472ab0f152cabdbe053bad812495fc04bfeb00a316e395abac53e16; qa/run-70e98cbc2fc928c9a7754305/1: verdict passed, score 100, signed true by "ed25519:14517cf3112769eb74b891fa8cea70b41444c3e889e88a2cc6e24d0b07f9b33f"

## Cost from the usage ledger

mode capped, spent verified 0 micros, spent unverified 1000000 micros, reserved 0 micros, available 4000000 micros, warnings ["some usage is unverified: the provider could not confirm the amount" "some usage had no cost reported by its provider: such usage counts against the budget only through the amount reserved for its run, of which only the part not covered by the costs reported for that run can be charged when the run closes, possibly nothing, and does not count when nothing was reserved, as with an unlimited budget and a run estimate of 0" "the provider has no hard cap: the factory limits what it starts, but the provider may still charge beyond it"]

## Assertions

| Check | Result | Evidence |
|---|---|---|
| sandbox is private | pass | bestagentkits/sisf-sandbox PRIVATE |
| sandbox has no open factory work | pass | no open issue with a sisf: label and no open pull request |
| leftover candidate branches | info | 20 from earlier runs, left in place (delete them by hand once their pull requests are merged or closed): sisf/release/run-2db98a01de35cbf7142c349c, sisf/release/run-682c52dd8d8ec61c4b0f9028, sisf/release/run-820073e0ea300f2883b6306d, sisf/release/run-a34cc1a5d3deea3db50452d7, sisf/release/run-c14a8afab0491f022090a382, sisf/release/run-fb25d46e42ef4ca4a8e3365c, sisf/sisf-sisf-sandbox/run-2165aefea96700906043a861-1, sisf/sisf-sisf-sandbox/run-2165aefea96700906043a861-2, sisf/sisf-sisf-sandbox/run-2165aefea96700906043a861-3, sisf/sisf-sisf-sandbox/run-2db98a01de35cbf7142c349c-1, sisf/sisf-sisf-sandbox/run-682c52dd8d8ec61c4b0f9028-1, sisf/sisf-sisf-sandbox/run-820073e0ea300f2883b6306d-1, sisf/sisf-sisf-sandbox/run-851258e429f8ec41145cbcd6-1, sisf/sisf-sisf-sandbox/run-8aebad583227afb3e130e525-1, sisf/sisf-sisf-sandbox/run-a34cc1a5d3deea3db50452d7-1, sisf/sisf-sisf-sandbox/run-b00aa4c28872003b5abb0300-1, sisf/sisf-sisf-sandbox/run-c14a8afab0491f022090a382-1, sisf/sisf-sisf-sandbox/run-cbb81eb3c71d07fb5b0d34a2-1, sisf/sisf-sisf-sandbox/run-d0bc4d7da245962d0e7ba7a1-1, sisf/sisf-sisf-sandbox/run-fb25d46e42ef4ca4a8e3365c-1 |
| sandbox network has no direct way out | pass | a direct connection from sisf-smoke-todwo0-sandbox failed |
| sandboxed build of the sandbox's head | pass | sha256:1f25dc9922431ac3ec3b7b1d737962fb28cadada2b0fb95f526c6592826fa9dc, tests passed; the program it exported for darwin/arm64 is 8067874 bytes and hashes to that digest |
| sisf doctor | pass |                                (qa_agent_binary, the supervisor's --qa-agent-binary), which is                                trusted on your word to run it in a sandbox; nothing checks that it                                does  21 ok, 6 warnings, 0 failures |
| issue closed by the factory | pass | GitHub "CLOSED COMPLETED", factory record "Closed", last journal state "Closed", inspect error <nil> |
| exactly one progress comment | pass | 1 progress comments, error <nil> |
| pull request linked to the issue | pass | https://github.com/bestagentkits/sisf-sandbox/pull/34 state MERGED links #33 |
| pull request merged in the sandbox | pass | https://github.com/bestagentkits/sisf-sandbox/pull/34 state "MERGED", merge commit "6d0bd2c8532008c6cfac8d377a9d33ba55509c80", error <nil> |
| the candidate's own build served through the edge before the merge | pass | at 2026-11-12T10:23:38Z the edge served revision sha256:b9ca5507d472ab0f152cabdbe053bad812495fc04bfeb00a316e395abac53e16; GET / answered HTTP 200 "Hello from release rehearsal 20261112-092513: visit 1"; the store's file for it hashes to sha256:b9ca5507d472ab0f152cabdbe053bad812495fc04bfeb00a316e395abac53e16; pull request https://github.com/bestagentkits/sisf-sandbox/pull/34 was OPEN; the factory records the candidate as sha256:b9ca5507d472ab0f152cabdbe053bad812495fc04bfeb00a316e395abac53e16 and the installed release is sha256:a09c671f89f14cb0b27a18d52eb078d09ea70acfe487f1ef5b6deb47ef693d5d; the page had to answer HTTP 200 with "" |
| requested change merged on the default branch | pass | 6d0bd2c8532008c6cfac8d377a9d33ba55509c80 against main: identical; its Go diff adds "/version": true, error <nil> |
| signed QA receipt scoring at least 85 | pass | run "run-70e98cbc2fc928c9a7754305": qa/run-70e98cbc2fc928c9a7754305/1: verdict passed, score 100, signed true by "ed25519:14517cf3112769eb74b891fa8cea70b41444c3e889e88a2cc6e24d0b07f9b33f", error <nil> |
| QA receipt signature verifies | not checked | no CLI or route verifies a receipt; the run checked the supervisor's record of a signed receipt and that the issue passed the QA guard into ReleaseCandidate |
| release completed | pass | release/run-70e98cbc2fc928c9a7754305/1: result closed, state Closed, release sha256:b9ca5507d472ab0f152cabdbe053bad812495fc04bfeb00a316e395abac53e16; journal through QAEvaluation, ReleaseCandidate, Verified and Closed: true; error <nil> |
| application healthy through the edge | pass | GET /healthz on the edge: HTTP 200, body "{\"revision\":\"sha256:b9ca5507d472ab0f152cabdbe053bad812495fc04bfeb00a316e395abac53e16\",\"status\":\"ok\"}\n", error <nil> |
| usage ledger non-empty | pass | mode capped, spent verified 0 micros, spent unverified 1000000 micros, reserved 0 micros, available 4000000 micros, warnings ["some usage is unverified: the provider could not confirm the amount" "some usage had no cost reported by its provider: such usage counts against the budget only through the amount reserved for its run, of which only the part not covered by the costs reported for that run can be charged when the run closes, possibly nothing, and does not count when nothing was reserved, as with an unlimited budget and a run estimate of 0" "the provider has no hard cap: the factory limits what it starts, but the provider may still charge beyond it"] |
| the factory addressed only the sandbox | pass | factory.yaml names the sandbox; 1 issue(s) in its journal and 1 pull request(s) of its run; manifest error <nil>, journal error <nil> |
| other activity of the gh account | info | 42 public event(s) of the gh account since the start named another repository; they are other work of the same account, not counted against the factory, and not named here |
| release record pull request closed | info | the factory's changelog pull request, left for the Owner, was closed with its branch kept: #35 |

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
- `gh issue list --repo bestagentkits/sisf-sandbox --state open --json number,labels --limit 1000`
- `gh pr list --repo bestagentkits/sisf-sandbox --state open --json number --limit 1000`
- `gh api --paginate repos/bestagentkits/sisf-sandbox/git/matching-refs/heads/sisf/ --jq .[].ref`
- `go build -o <run-dir>/bin/sisf ./cmd/sisf`
- `<run-dir>/bin/sisf init --dev --mode standalone --dir <run-dir>/factory --repo bestagentkits/sisf-sandbox --github-credential gh-cli --budget-cap 5 --alert-threshold 2 --coder-agent claude --coder-model claude-sonnet-5-5 --qa-judge agent --qa-agent codex --qa-model gpt-6.1-sol --agent-home ~ --workspace-source <run-dir>/sandbox --exec docker --exec-docker-endpoint http://[REDACTED:ip]:1 --exec-docker-network sisf-smoke-probe --egress-proxy http://egress:3128 --runner-image [REDACTED:ip]:1/sisf-smoke-runner@sha256:0000000000000000000000000000000000000000000000000000000000000000 --qa-agent-binary /nonexistent/qa-codex-launcher --build-command /usr/local/bin/sisf-smoke-build --github-reconcile-interval 15s --help`
- `<run-dir>/bin/sisf init --dev --mode standalone --dir <run-dir>/factory --repo bestagentkits/sisf-sandbox --github-credential gh-cli --budget-cap 5 --alert-threshold 2 --qa-judge agent --qa-agent codex --exec docker --exec-docker-endpoint http://[REDACTED:ip]:1 --exec-docker-network sisf-smoke-probe --egress-proxy http://egress:3128 --runner-image [REDACTED:ip]:1/sisf-smoke-runner@sha256:0000000000000000000000000000000000000000000000000000000000000000 --qa-agent-binary /nonexistent/qa-codex-launcher --build-command /usr/local/bin/sisf-smoke-build --github-reconcile-interval 15s --help`
- `<run-dir>/bin/sisf up --dir <run-dir>/factory --mode standalone --source <repo> --help`
- `<run-dir>/bin/sisf down --dir <run-dir>/factory --help`
- `gh repo clone bestagentkits/sisf-sandbox <run-dir>/sandbox`
- `/usr/local/bin/docker context inspect --format {{.Endpoints.docker.Host}}`
- `/usr/local/bin/docker network create --internal sisf-smoke-todwo0-sandbox`
- `/usr/local/bin/docker network create sisf-smoke-todwo0-out`
- `/usr/local/bin/docker run -d --name sisf-smoke-todwo0-registry -p [REDACTED:ip]:59678:5000 registry:2`
- `/usr/local/bin/docker build -q -f <repo>/tests/smoke/sandbox/runner.Dockerfile -t [REDACTED:ip]:59678/sisf-smoke-runner:sisf-smoke-todwo0 <repo>/tests/smoke/sandbox`
- `/usr/local/bin/docker push -q [REDACTED:ip]:59678/sisf-smoke-runner:sisf-smoke-todwo0`
- `/usr/local/bin/docker image inspect --format {{join .RepoDigests " "}} [REDACTED:ip]:59678/sisf-smoke-runner:sisf-smoke-todwo0`
- `/usr/local/bin/docker build -q -f <repo>/build/edge.Dockerfile -t sisf-smoke-todwo0-edge:run <repo>`
- `/usr/local/bin/docker build -q -f <repo>/tests/smoke/sandbox/qa-codex.Dockerfile --build-arg CODEX_VERSION=0.167.2 -t sisf-smoke-todwo0-qa-codex:run <repo>/tests/smoke/sandbox`
- `/usr/local/bin/docker create --name sisf-smoke-todwo0-egress --network sisf-smoke-todwo0-out --read-only --cap-drop ALL --security-opt no-new-privileges sisf-smoke-todwo0-edge:run edge --listen=[REDACTED:ip]:8080 --upstream=http://[REDACTED:ip]:9 --egress-listen=egress:3128 --allow-non-loopback`
- `/usr/local/bin/docker network connect --alias egress sisf-smoke-todwo0-sandbox sisf-smoke-todwo0-egress`
- `/usr/local/bin/docker start sisf-smoke-todwo0-egress`
- `git init -q --bare <run-dir>/mirror/sandbox.git`
- `git -C <run-dir>/mirror/sandbox.git config uploadpack.allowAnySHA1InWant true`
- `git -C <run-dir>/mirror/sandbox.git -c credential.helper= -c credential.helper=!gh auth git-credential fetch -q --prune https://github.com/bestagentkits/sisf-sandbox.git +refs/heads/*:refs/heads/*`
- `/usr/local/bin/docker run -d --name sisf-smoke-todwo0-git --network sisf-smoke-todwo0-sandbox --network-alias git-mirror --user 65534:65534 -e GIT_CONFIG_COUNT=1 -e GIT_CONFIG_KEY_0=safe.directory -e GIT_CONFIG_VALUE_0=* -v <run-dir>/mirror:/srv/git:ro --read-only --cap-drop ALL --security-opt no-new-privileges [REDACTED:ip]:59678/sisf-smoke-runner@sha256:ac218260676c9b07e4eebc8575a2593c3aa45ea0cb28e1049f5f1269ac62c5c7 git daemon --base-path=/srv/git --export-all --reuseaddr /srv/git`
- `/usr/local/bin/docker run --rm --name sisf-smoke-todwo0-isolation --network sisf-smoke-todwo0-sandbox --read-only --cap-drop ALL [REDACTED:ip]:59678/sisf-smoke-runner@sha256:ac218260676c9b07e4eebc8575a2593c3aa45ea0cb28e1049f5f1269ac62c5c7 wget -q -T 5 -O /dev/null http://[REDACTED:ip]/`
- `<run-dir>/bin/sisf init --dev --mode standalone --dir <run-dir>/factory --repo bestagentkits/sisf-sandbox --github-credential gh-cli --budget-cap 5 --alert-threshold 2 --coder-agent claude --coder-model claude-sonnet-5-5 --qa-judge agent --qa-agent codex --qa-model gpt-6.1-sol --agent-home ~ --workspace-source <run-dir>/sandbox --exec docker --exec-docker-endpoint http://[REDACTED:ip]:59679 --exec-docker-network sisf-smoke-todwo0-sandbox --egress-proxy http://egress:3128 --runner-image [REDACTED:ip]:59678/sisf-smoke-runner@sha256:ac218260676c9b07e4eebc8575a2593c3aa45ea0cb28e1049f5f1269ac62c5c7 --qa-agent-binary <run-dir>/bin/qa-codex-launcher --build-command /usr/local/bin/sisf-smoke-build --github-reconcile-interval 15s`
- `<run-dir>/bin/sisf docker-filter --role runner --listen [REDACTED:ip]:59679 --factory-id sisf-sisf-sandbox --network sisf-smoke-todwo0-sandbox --image [REDACTED:ip]:59678/sisf-smoke-runner@sha256:ac218260676c9b07e4eebc8575a2593c3aa45ea0cb28e1049f5f1269ac62c5c7 --socket ~/.orbstack/run/docker.sock`
- `gh api repos/bestagentkits/sisf-sandbox/commits/HEAD --jq .sha`
- `<run-dir>/bin/sisf up --dir <run-dir>/factory --mode standalone --source <repo>`
- `<run-dir>/bin/sisf doctor --dir <run-dir>/factory`
- `<run-dir>/factory/bin/factoryd policy approve --revision 7dbd2b32e0740c8061671f6db4bb872d0cf4b466`
- `<run-dir>/bin/sisf login --url http://[REDACTED:ip]:7463 --dir <run-dir>/factory --name sisf-smoke`
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
- `<run-dir>/bin/sisf call runs.inspect --input {"issue":"bestagentkits/sisf-sandbox#33"}`
- `gh issue view 33 --repo bestagentkits/sisf-sandbox --json state,stateReason --jq .state + " " + .stateReason`
- `gh pr view https://github.com/bestagentkits/sisf-sandbox/pull/34 --json state,mergeCommit`
- `gh api --paginate repos/bestagentkits/sisf-sandbox/issues/33/comments?per_page=100 --jq [.[] | select(.body | contains("<!-- sisf:progress "))] | length`
- `gh pr view https://github.com/bestagentkits/sisf-sandbox/pull/34 --json state,body,closingIssuesReferences`
- `gh api repos/bestagentkits/sisf-sandbox --jq .default_branch`
- `gh api repos/bestagentkits/sisf-sandbox/compare/6d0bd2c8532008c6cfac8d377a9d33ba55509c80...main --jq .status`
- `gh api repos/bestagentkits/sisf-sandbox/commits/6d0bd2c8532008c6cfac8d377a9d33ba55509c80 --jq .files[] | select(.filename | endswith(".go")) | .patch // ""`
- `<run-dir>/bin/sisf call budget.get --input {}`
- `gh api users/<gh-login>/events?per_page=100 --jq .[] | [.created_at, .repo.name, .type] | @tsv`
- `gh pr list --repo bestagentkits/sisf-sandbox --state open --json number,headRefName --limit 1000`
- `gh pr close 35 --repo bestagentkits/sisf-sandbox --comment Closed by the smoke run so the sandbox keeps no open factory work; the branch is kept.`
- `<run-dir>/bin/sisf down --dir <run-dir>/factory`
- `/usr/local/bin/docker ps -aq --filter name=^sisf-smoke-todwo0-`
- `/usr/local/bin/docker ps -aq --filter label=sisf.smoke=sisf-smoke-todwo0`
- `/usr/local/bin/docker ps -aq --filter network=sisf-smoke-todwo0-sandbox`
- `/usr/local/bin/docker rm -f d283eae5f5be`
- `/usr/local/bin/docker rm -f 0a553dc2cedd`
- `/usr/local/bin/docker rm -f b37a1bf5b7f4`
- `/usr/local/bin/docker network rm sisf-smoke-todwo0-sandbox`
- `/usr/local/bin/docker network rm sisf-smoke-todwo0-out`
- `/usr/local/bin/docker image rm sisf-smoke-todwo0-edge:run`
- `/usr/local/bin/docker image rm sisf-smoke-todwo0-qa-codex:run`
- `/usr/local/bin/docker image rm [REDACTED:ip]:59678/sisf-smoke-runner:sisf-smoke-todwo0`
