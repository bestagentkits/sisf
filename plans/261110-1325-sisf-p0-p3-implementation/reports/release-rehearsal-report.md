# Release rehearsal report (scripted coder and fake QA judge; not the paid smoke run)

- Date: 2026-11-12 16:25:13 +07
- Finished: 2026-11-12 16:58:56 +07
- Outcome: **PASS (release rehearsal: no model ran, nothing was spent)**
- Repository: bestagentkits/sisf-sandbox

## Tool versions

| Tool | Version |
|---|---|
| claude, codex | not used: the release rehearsal runs a scripted coder and a fake QA judge |
| gh | gh version 2.85.0 (2026-01-14) |
| docker | 29.8.2 |
| go | go version go1.25.14 darwin/arm64 |
| sisf source revision | 5b01034ba2e9f6fd6e74dffa03b40933e5c810d7 |

## Issue and pull requests

- issue: https://github.com/bestagentkits/sisf-sandbox/issues/28
- pull request: https://github.com/bestagentkits/sisf-sandbox/pull/29

## Run IDs

- run-820073e0ea300f2883b6306d

## Timings per phase

| Phase | Took |
|---|---|
| preflight and build | 7s |
| build sandbox | 21s |
| install, up, doctor and policy | 52s |
| run (issue opened to closed) | 32m3s |
| assertions | 10s |
| restarts (units restarted, then sisf down and sisf up) | 7s |
| teardown | 1s |

## States visited (factory journal)

| State | Run | Entered | Time in state |
|---|---|---|---|
| Intake |  | 2026-11-12 16:26:43 +07 | 0s |
| ScopeDraft |  | 2026-11-12 16:26:43 +07 | 0s |
| Ready |  | 2026-11-12 16:26:43 +07 | 1s |
| Claimed | run-820073e0ea300f2883b6306d | 2026-11-12 16:26:45 +07 | 0s |
| Developing | run-820073e0ea300f2883b6306d | 2026-11-12 16:26:45 +07 | 1m6s |
| QAEvaluation | run-820073e0ea300f2883b6306d | 2026-11-12 16:27:51 +07 | 12s |
| ReleaseCandidate | run-820073e0ea300f2883b6306d | 2026-11-12 16:28:03 +07 | 3s |
| Canary | run-820073e0ea300f2883b6306d | 2026-11-12 16:28:06 +07 | 0s |
| Monitoring | run-820073e0ea300f2883b6306d | 2026-11-12 16:28:06 +07 | 30m1s |
| Verified | run-820073e0ea300f2883b6306d | 2026-11-12 16:58:07 +07 | 12s |
| Closed | run-820073e0ea300f2883b6306d | 2026-11-12 16:58:19 +07 | - |

## Receipt

candidate digest sha256:bba09db0b40ae2b57d85d59942fdd0e69467ee971b758e3c4327975b8f7c844b, release digest sha256:bba09db0b40ae2b57d85d59942fdd0e69467ee971b758e3c4327975b8f7c844b; qa/run-820073e0ea300f2883b6306d/1: verdict passed, score 100, signed true by "ed25519:982acbe513a252e73c9d756b7253d93c069dd96d843d2d847f252a337446fbbb"

## Cost from the usage ledger

mode capped, spent verified 0 micros, spent unverified 1000000 micros, reserved 0 micros, available 4000000 micros, warnings ["some usage is unverified: the provider could not confirm the amount" "the provider has no hard cap: the factory limits what it starts, but the provider may still charge beyond it"]

## Assertions

| Check | Result | Evidence |
|---|---|---|
| sandbox is private | pass | bestagentkits/sisf-sandbox PRIVATE |
| sandbox has no open factory work | pass | no open issue with a sisf: label and no open pull request |
| leftover candidate branches | info | 17 from earlier runs, left in place (delete them by hand once their pull requests are merged or closed): sisf/release/run-2db98a01de35cbf7142c349c, sisf/release/run-682c52dd8d8ec61c4b0f9028, sisf/release/run-a34cc1a5d3deea3db50452d7, sisf/release/run-c14a8afab0491f022090a382, sisf/release/run-fb25d46e42ef4ca4a8e3365c, sisf/sisf-sisf-sandbox/run-2165aefea96700906043a861-1, sisf/sisf-sisf-sandbox/run-2165aefea96700906043a861-2, sisf/sisf-sisf-sandbox/run-2165aefea96700906043a861-3, sisf/sisf-sisf-sandbox/run-2db98a01de35cbf7142c349c-1, sisf/sisf-sisf-sandbox/run-682c52dd8d8ec61c4b0f9028-1, sisf/sisf-sisf-sandbox/run-851258e429f8ec41145cbcd6-1, sisf/sisf-sisf-sandbox/run-8aebad583227afb3e130e525-1, sisf/sisf-sisf-sandbox/run-a34cc1a5d3deea3db50452d7-1, sisf/sisf-sisf-sandbox/run-b00aa4c28872003b5abb0300-1, sisf/sisf-sisf-sandbox/run-c14a8afab0491f022090a382-1, sisf/sisf-sisf-sandbox/run-d0bc4d7da245962d0e7ba7a1-1, sisf/sisf-sisf-sandbox/run-fb25d46e42ef4ca4a8e3365c-1 |
| sandbox network has no direct way out | pass | a direct connection from sisf-smoke-todu68-sandbox failed |
| sandboxed build of the sandbox's head | pass | sha256:c81ced1a16611e0cf3e75b898e25937e5d53d7a5c9583d632059796ddef4fd4e, tests passed; the program it exported for darwin/arm64 is 8067874 bytes and hashes to that digest |
| sisf doctor | pass |                                (qa_agent_binary, the supervisor's --qa-agent-binary), which is                                trusted on your word to run it in a sandbox; nothing checks that it                                does  21 ok, 6 warnings, 0 failures |
| issue closed by the factory | pass | GitHub "CLOSED COMPLETED", factory record "Closed", last journal state "Closed", inspect error <nil> |
| exactly one progress comment | pass | 1 progress comments, error <nil> |
| pull request linked to the issue | pass | https://github.com/bestagentkits/sisf-sandbox/pull/29 state MERGED links #28 |
| pull request merged in the sandbox | pass | https://github.com/bestagentkits/sisf-sandbox/pull/29 state "MERGED", merge commit "7dbd2b32e0740c8061671f6db4bb872d0cf4b466", error <nil> |
| the candidate's own build served through the edge before the merge | pass | at 2026-11-12T09:28:19Z the edge served revision sha256:bba09db0b40ae2b57d85d59942fdd0e69467ee971b758e3c4327975b8f7c844b; GET / answered HTTP 200 "Hello from release rehearsal 20261112-092513: visit 1"; the store's file for it hashes to sha256:bba09db0b40ae2b57d85d59942fdd0e69467ee971b758e3c4327975b8f7c844b; pull request https://github.com/bestagentkits/sisf-sandbox/pull/29 was OPEN; the factory records the candidate as sha256:bba09db0b40ae2b57d85d59942fdd0e69467ee971b758e3c4327975b8f7c844b and the installed release is sha256:a09c671f89f14cb0b27a18d52eb078d09ea70acfe487f1ef5b6deb47ef693d5d; the page had to answer HTTP 200 with "Hello from release rehearsal 20261112-092513" |
| requested change merged on the default branch | pass | 7dbd2b32e0740c8061671f6db4bb872d0cf4b466 against main: identical; its diff of app.go adds "Hello from release rehearsal 20261112-092513": true, error <nil> |
| signed QA receipt scoring at least 85 | pass | run "run-820073e0ea300f2883b6306d": qa/run-820073e0ea300f2883b6306d/1: verdict passed, score 100, signed true by "ed25519:982acbe513a252e73c9d756b7253d93c069dd96d843d2d847f252a337446fbbb", error <nil> |
| QA receipt signature verifies | not checked | no CLI or route verifies a receipt; the run checked the supervisor's record of a signed receipt and that the issue passed the QA guard into ReleaseCandidate |
| release completed | pass | release/run-820073e0ea300f2883b6306d/1: result closed, state Closed, release sha256:bba09db0b40ae2b57d85d59942fdd0e69467ee971b758e3c4327975b8f7c844b; journal through QAEvaluation, ReleaseCandidate, Verified and Closed: true; error <nil> |
| application healthy through the edge | pass | GET /healthz on the edge: HTTP 200, body "{\"revision\":\"sha256:bba09db0b40ae2b57d85d59942fdd0e69467ee971b758e3c4327975b8f7c844b\",\"status\":\"ok\"}\n", error <nil> |
| no paid model configured | pass | coder "" (empty or fake is the scripted agent), QA launcher is the rehearsal's fake judge: true |
| the factory addressed only the sandbox | pass | factory.yaml names the sandbox; 1 issue(s) in its journal and 1 pull request(s) of its run; manifest error <nil>, journal error <nil> |
| other activity of the gh account | info | 18 public event(s) of the gh account since the start named another repository; they are other work of the same account, not counted against the factory, and not named here |
| release record pull request closed | info | the factory's changelog pull request, left for the Owner, was closed with its branch kept: #30 |
| the promoted program serves after a restart of the units | pass | the edge serves revision sha256:bba09db0b40ae2b57d85d59942fdd0e69467ee971b758e3c4327975b8f7c844b; GET / answered HTTP 200 "Hello from release rehearsal 20261112-092513: visit 8"; port 7461 pid 53993 runs <run-dir>/factory/data/trust/programs/sha256-bba09db0b40ae2b57d85d59942fdd0e69467ee971b758e3c4327975b8f7c844b; the store's file hashes to sha256:bba09db0b40ae2b57d85d59942fdd0e69467ee971b758e3c4327975b8f7c844b |
| the promoted program serves after sisf down and sisf up | pass | the edge serves revision sha256:bba09db0b40ae2b57d85d59942fdd0e69467ee971b758e3c4327975b8f7c844b; GET / answered HTTP 200 "Hello from release rehearsal 20261112-092513: visit 10"; port 7461 pid 54067 runs <run-dir>/factory/data/trust/programs/sha256-bba09db0b40ae2b57d85d59942fdd0e69467ee971b758e3c4327975b8f7c844b; the store's file hashes to sha256:bba09db0b40ae2b57d85d59942fdd0e69467ee971b758e3c4327975b8f7c844b; sisf down --dir <install> succeeded; sisf up exited; ports 7460-7469 free after teardown; the new sisf up dropped the promotion: false |
| rehearsal's own issue and pull requests closed | info | the rehearsal left no issue or pull request of its own open |

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
- `docker version --format {{.Server.Version}}`
- `go version`
- `git rev-parse HEAD`
- `gh issue list --repo bestagentkits/sisf-sandbox --state open --json number,labels --limit 1000`
- `gh pr list --repo bestagentkits/sisf-sandbox --state open --json number --limit 1000`
- `gh api --paginate repos/bestagentkits/sisf-sandbox/git/matching-refs/heads/sisf/ --jq .[].ref`
- `go build -o <run-dir>/bin/sisf ./cmd/sisf`
- `<run-dir>/bin/sisf init --dev --mode standalone --dir <run-dir>/factory --repo bestagentkits/sisf-sandbox --github-credential gh-cli --budget-cap 5 --alert-threshold 2 --coder-agent claude --coder-model claude-sonnet-4-5 --qa-judge agent --qa-agent codex --qa-model gpt-5-codex --agent-home ~ --workspace-source <run-dir>/sandbox --exec docker --exec-docker-endpoint http://[REDACTED:ip]:1 --exec-docker-network sisf-smoke-probe --egress-proxy http://egress:3128 --runner-image [REDACTED:ip]:1/sisf-smoke-runner@sha256:0000000000000000000000000000000000000000000000000000000000000000 --qa-agent-binary /nonexistent/qa-codex-launcher --build-command /usr/local/bin/sisf-smoke-build --github-reconcile-interval 15s --help`
- `<run-dir>/bin/sisf init --dev --mode standalone --dir <run-dir>/factory --repo bestagentkits/sisf-sandbox --github-credential gh-cli --budget-cap 5 --alert-threshold 2 --qa-judge agent --qa-agent codex --exec docker --exec-docker-endpoint http://[REDACTED:ip]:1 --exec-docker-network sisf-smoke-probe --egress-proxy http://egress:3128 --runner-image [REDACTED:ip]:1/sisf-smoke-runner@sha256:0000000000000000000000000000000000000000000000000000000000000000 --qa-agent-binary /nonexistent/qa-codex-launcher --build-command /usr/local/bin/sisf-smoke-build --github-reconcile-interval 15s --help`
- `<run-dir>/bin/sisf up --dir <run-dir>/factory --mode standalone --source <repo> --help`
- `<run-dir>/bin/sisf down --dir <run-dir>/factory --help`
- `/usr/local/bin/docker context inspect --format {{.Endpoints.docker.Host}}`
- `/usr/local/bin/docker network create --internal sisf-smoke-todu68-sandbox`
- `/usr/local/bin/docker network create sisf-smoke-todu68-out`
- `/usr/local/bin/docker run -d --name sisf-smoke-todu68-registry -p [REDACTED:ip]:56290:5000 registry:2`
- `/usr/local/bin/docker build -q -f <repo>/tests/smoke/sandbox/runner.Dockerfile -t [REDACTED:ip]:56290/sisf-smoke-runner:sisf-smoke-todu68 <repo>/tests/smoke/sandbox`
- `/usr/local/bin/docker push -q [REDACTED:ip]:56290/sisf-smoke-runner:sisf-smoke-todu68`
- `/usr/local/bin/docker image inspect --format {{join .RepoDigests " "}} [REDACTED:ip]:56290/sisf-smoke-runner:sisf-smoke-todu68`
- `/usr/local/bin/docker build -q -f <repo>/build/edge.Dockerfile -t sisf-smoke-todu68-edge:run <repo>`
- `/usr/local/bin/docker create --name sisf-smoke-todu68-egress --network sisf-smoke-todu68-out --read-only --cap-drop ALL --security-opt no-new-privileges sisf-smoke-todu68-edge:run edge --listen=[REDACTED:ip]:8080 --upstream=http://[REDACTED:ip]:9 --egress-listen=egress:3128 --allow-non-loopback`
- `/usr/local/bin/docker network connect --alias egress sisf-smoke-todu68-sandbox sisf-smoke-todu68-egress`
- `/usr/local/bin/docker start sisf-smoke-todu68-egress`
- `git init -q --bare <run-dir>/mirror/sandbox.git`
- `git -C <run-dir>/mirror/sandbox.git config uploadpack.allowAnySHA1InWant true`
- `git -C <run-dir>/mirror/sandbox.git -c credential.helper= -c credential.helper=!gh auth git-credential fetch -q --prune https://github.com/bestagentkits/sisf-sandbox.git +refs/heads/*:refs/heads/*`
- `/usr/local/bin/docker run -d --name sisf-smoke-todu68-git --network sisf-smoke-todu68-sandbox --network-alias git-mirror --user 65534:65534 -e GIT_CONFIG_COUNT=1 -e GIT_CONFIG_KEY_0=safe.directory -e GIT_CONFIG_VALUE_0=* -v <run-dir>/mirror:/srv/git:ro --read-only --cap-drop ALL --security-opt no-new-privileges [REDACTED:ip]:56290/sisf-smoke-runner@sha256:ac218260676c9b07e4eebc8575a2593c3aa45ea0cb28e1049f5f1269ac62c5c7 git daemon --base-path=/srv/git --export-all --reuseaddr /srv/git`
- `/usr/local/bin/docker run --rm --name sisf-smoke-todu68-isolation --network sisf-smoke-todu68-sandbox --read-only --cap-drop ALL [REDACTED:ip]:56290/sisf-smoke-runner@sha256:ac218260676c9b07e4eebc8575a2593c3aa45ea0cb28e1049f5f1269ac62c5c7 wget -q -T 5 -O /dev/null http://[REDACTED:ip]/`
- `<run-dir>/bin/sisf init --dev --mode standalone --dir <run-dir>/factory --repo bestagentkits/sisf-sandbox --github-credential gh-cli --budget-cap 5 --alert-threshold 2 --qa-judge agent --qa-agent codex --exec docker --exec-docker-endpoint http://[REDACTED:ip]:56291 --exec-docker-network sisf-smoke-todu68-sandbox --egress-proxy http://egress:3128 --runner-image [REDACTED:ip]:56290/sisf-smoke-runner@sha256:ac218260676c9b07e4eebc8575a2593c3aa45ea0cb28e1049f5f1269ac62c5c7 --qa-agent-binary <run-dir>/bin/qa-codex-launcher --build-command /usr/local/bin/sisf-smoke-build --github-reconcile-interval 15s`
- `<run-dir>/bin/sisf docker-filter --role runner --listen [REDACTED:ip]:56291 --factory-id sisf-sisf-sandbox --network sisf-smoke-todu68-sandbox --image [REDACTED:ip]:56290/sisf-smoke-runner@sha256:ac218260676c9b07e4eebc8575a2593c3aa45ea0cb28e1049f5f1269ac62c5c7 --socket ~/.orbstack/run/docker.sock`
- `gh api repos/bestagentkits/sisf-sandbox/commits/HEAD --jq .sha`
- `gh api -H Accept: application/vnd.github.raw repos/bestagentkits/sisf-sandbox/contents/app.go?ref=889a2b7f25979f14f4b9ce8f6a4ddb5fc85497bb`
- `<run-dir>/bin/sisf up --dir <run-dir>/factory --mode standalone --source <repo>`
- `<run-dir>/bin/sisf doctor --dir <run-dir>/factory`
- `<run-dir>/factory/bin/factoryd policy approve --revision 889a2b7f25979f14f4b9ce8f6a4ddb5fc85497bb`
- `<run-dir>/bin/sisf login --url http://[REDACTED:ip]:7463 --dir <run-dir>/factory --name sisf-smoke`
- `gh issue create --repo bestagentkits/sisf-sandbox --title release rehearsal 20261112-092513: change the page's greeting --body ## Scope

Change the greeting of the page `GET /` in `app.go` to "Hello from release rehearsal 20261112-092513". This
issue is a release rehearsal of the factory: a scripted agent writes the
change and a fake judge scores it; no model runs.

## Affected features

- Visit page

## Definition of Done

- `GET /` answers "Hello from release rehearsal 20261112-092513: visit N".
- `go test ./...` passes.
 --label sisf:ready --label kind:improvement`
- `<run-dir>/bin/sisf call runs.inspect --input {"issue":"bestagentkits/sisf-sandbox#28"}`
- `gh issue view 28 --repo bestagentkits/sisf-sandbox --json state,stateReason --jq .state + " " + .stateReason`
- `gh pr view https://github.com/bestagentkits/sisf-sandbox/pull/29 --json state,mergeCommit`
- `gh api --paginate repos/bestagentkits/sisf-sandbox/issues/28/comments?per_page=100 --jq [.[] | select(.body | contains("<!-- sisf:progress "))] | length`
- `gh pr view https://github.com/bestagentkits/sisf-sandbox/pull/29 --json state,body,closingIssuesReferences`
- `gh api repos/bestagentkits/sisf-sandbox --jq .default_branch`
- `gh api repos/bestagentkits/sisf-sandbox/compare/7dbd2b32e0740c8061671f6db4bb872d0cf4b466...main --jq .status`
- `gh api repos/bestagentkits/sisf-sandbox/commits/7dbd2b32e0740c8061671f6db4bb872d0cf4b466 --jq .files[] | select(.filename == "app.go") | .patch // ""`
- `<run-dir>/bin/sisf call budget.get --input {}`
- `gh api users/<gh-login>/events?per_page=100 --jq .[] | [.created_at, .repo.name, .type] | @tsv`
- `gh pr list --repo bestagentkits/sisf-sandbox --state open --json number,headRefName --limit 1000`
- `gh pr close 30 --repo bestagentkits/sisf-sandbox --comment Closed by the smoke run so the sandbox keeps no open factory work; the branch is kept.`
- `lsof -nP -t -iTCP:7461 -sTCP:LISTEN`
- `ps -o command= -p 21789`
- `lsof -nP -t -iTCP:7462 -sTCP:LISTEN`
- `ps -o command= -p 24243`
- `<run-dir>/bin/sisf restart --dir <run-dir>/factory`
- `ps -o command= -p 53993`
- `<run-dir>/bin/sisf down --dir <run-dir>/factory`
- `<run-dir>/bin/sisf up --dir <run-dir>/factory --mode standalone --source <repo>`
- `ps -o command= -p 54067`
- `/usr/local/bin/docker ps -aq --filter name=^sisf-smoke-todu68-`
- `/usr/local/bin/docker ps -aq --filter label=sisf.smoke=sisf-smoke-todu68`
- `/usr/local/bin/docker ps -aq --filter network=sisf-smoke-todu68-sandbox`
- `/usr/local/bin/docker rm -f 78044604f328`
- `/usr/local/bin/docker rm -f f54ccdc5160c`
- `/usr/local/bin/docker rm -f 16cf419df621`
- `/usr/local/bin/docker network rm sisf-smoke-todu68-sandbox`
- `/usr/local/bin/docker network rm sisf-smoke-todu68-out`
- `/usr/local/bin/docker image rm sisf-smoke-todu68-edge:run`
- `/usr/local/bin/docker image rm [REDACTED:ip]:56290/sisf-smoke-runner:sisf-smoke-todu68`
