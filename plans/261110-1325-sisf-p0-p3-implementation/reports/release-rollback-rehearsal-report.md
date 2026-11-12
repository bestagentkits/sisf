# Release rehearsal report (scripted coder and fake QA judge; not the paid smoke run)

- Date: 2026-11-12 16:59:15 +07
- Finished: 2026-11-12 17:12:48 +07
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

- issue: https://github.com/bestagentkits/sisf-sandbox/issues/31
- pull request: https://github.com/bestagentkits/sisf-sandbox/pull/32

## Run IDs

- run-cbb81eb3c71d07fb5b0d34a2

## Timings per phase

| Phase | Took |
|---|---|
| preflight and build | 7s |
| build sandbox | 28s |
| install, up, doctor and policy | 54s |
| run (issue opened to rolled back and blocked) | 11m48s |
| assertions | 6s |
| teardown | 1s |

## States visited (factory journal)

| State | Run | Entered | Time in state |
|---|---|---|---|
| Intake |  | 2026-11-12 17:00:58 +07 | 0s |
| ScopeDraft |  | 2026-11-12 17:00:58 +07 | 0s |
| Ready |  | 2026-11-12 17:00:58 +07 | 0s |
| Claimed | run-cbb81eb3c71d07fb5b0d34a2 | 2026-11-12 17:00:58 +07 | 0s |
| Developing | run-cbb81eb3c71d07fb5b0d34a2 | 2026-11-12 17:00:58 +07 | 1m11s |
| QAEvaluation | run-cbb81eb3c71d07fb5b0d34a2 | 2026-11-12 17:02:10 +07 | 12s |
| ReleaseCandidate | run-cbb81eb3c71d07fb5b0d34a2 | 2026-11-12 17:02:21 +07 | 4s |
| Canary | run-cbb81eb3c71d07fb5b0d34a2 | 2026-11-12 17:02:25 +07 | 0s |
| Monitoring | run-cbb81eb3c71d07fb5b0d34a2 | 2026-11-12 17:02:25 +07 | 10m1s |
| RolledBack | run-cbb81eb3c71d07fb5b0d34a2 | 2026-11-12 17:12:26 +07 | 0s |
| Blocked | run-cbb81eb3c71d07fb5b0d34a2 | 2026-11-12 17:12:26 +07 | - |

## Receipt

candidate digest sha256:850ed9ad0333480ff380893eac26a76ef75691d27ffde02c8dd4bcc4b067ceac, release digest sha256:850ed9ad0333480ff380893eac26a76ef75691d27ffde02c8dd4bcc4b067ceac; qa/run-cbb81eb3c71d07fb5b0d34a2/1: verdict passed, score 100, signed true by "ed25519:c016da50cca93c3ddf29352d5f5eb4bfcd22bcba12b837e9e0ec7e35475f4dc8"

## Cost from the usage ledger

mode capped, spent verified 0 micros, spent unverified 1000001 micros, reserved 0 micros, available 3999999 micros, warnings ["some usage is unverified: the provider could not confirm the amount" "the provider has no hard cap: the factory limits what it starts, but the provider may still charge beyond it"]

## Assertions

| Check | Result | Evidence |
|---|---|---|
| sandbox is private | pass | bestagentkits/sisf-sandbox PRIVATE |
| sandbox has no open factory work | pass | no open issue with a sisf: label and no open pull request |
| leftover candidate branches | info | 19 from earlier runs, left in place (delete them by hand once their pull requests are merged or closed): sisf/release/run-2db98a01de35cbf7142c349c, sisf/release/run-682c52dd8d8ec61c4b0f9028, sisf/release/run-820073e0ea300f2883b6306d, sisf/release/run-a34cc1a5d3deea3db50452d7, sisf/release/run-c14a8afab0491f022090a382, sisf/release/run-fb25d46e42ef4ca4a8e3365c, sisf/sisf-sisf-sandbox/run-2165aefea96700906043a861-1, sisf/sisf-sisf-sandbox/run-2165aefea96700906043a861-2, sisf/sisf-sisf-sandbox/run-2165aefea96700906043a861-3, sisf/sisf-sisf-sandbox/run-2db98a01de35cbf7142c349c-1, sisf/sisf-sisf-sandbox/run-682c52dd8d8ec61c4b0f9028-1, sisf/sisf-sisf-sandbox/run-820073e0ea300f2883b6306d-1, sisf/sisf-sisf-sandbox/run-851258e429f8ec41145cbcd6-1, sisf/sisf-sisf-sandbox/run-8aebad583227afb3e130e525-1, sisf/sisf-sisf-sandbox/run-a34cc1a5d3deea3db50452d7-1, sisf/sisf-sisf-sandbox/run-b00aa4c28872003b5abb0300-1, sisf/sisf-sisf-sandbox/run-c14a8afab0491f022090a382-1, sisf/sisf-sisf-sandbox/run-d0bc4d7da245962d0e7ba7a1-1, sisf/sisf-sisf-sandbox/run-fb25d46e42ef4ca4a8e3365c-1 |
| sandbox network has no direct way out | pass | a direct connection from sisf-smoke-todvqy-sandbox failed |
| sandboxed build of the sandbox's head | pass | sha256:1f25dc9922431ac3ec3b7b1d737962fb28cadada2b0fb95f526c6592826fa9dc, tests passed; the program it exported for darwin/arm64 is 8067874 bytes and hashes to that digest |
| sisf doctor | pass |                                (qa_agent_binary, the supervisor's --qa-agent-binary), which is                                trusted on your word to run it in a sandbox; nothing checks that it                                does  21 ok, 6 warnings, 0 failures |
| the bad candidate was built and passed QA with at least 85 | pass | run "run-cbb81eb3c71d07fb5b0d34a2": qa/run-cbb81eb3c71d07fb5b0d34a2/1: verdict passed, score 100, signed true by "ed25519:c016da50cca93c3ddf29352d5f5eb4bfcd22bcba12b837e9e0ec7e35475f4dc8", error <nil> |
| the candidate's own build served through the edge before the merge | pass | at 2026-11-12T10:02:32Z the edge served revision sha256:850ed9ad0333480ff380893eac26a76ef75691d27ffde02c8dd4bcc4b067ceac; GET / answered HTTP 500 "injected failure"; the store's file for it hashes to sha256:850ed9ad0333480ff380893eac26a76ef75691d27ffde02c8dd4bcc4b067ceac; pull request https://github.com/bestagentkits/sisf-sandbox/pull/32 was OPEN; the factory records the candidate as sha256:850ed9ad0333480ff380893eac26a76ef75691d27ffde02c8dd4bcc4b067ceac and the installed release is sha256:a09c671f89f14cb0b27a18d52eb078d09ea70acfe487f1ef5b6deb47ef693d5d; the page had to answer HTTP 500 with "injected failure" |
| release rolled back by the synthetic suite | pass | release/run-cbb81eb3c71d07fb5b0d34a2/1: result blocked, state Blocked, release sha256:850ed9ad0333480ff380893eac26a76ef75691d27ffde02c8dd4bcc4b067ceac, reason "a synthetic check failed during the low-traffic hold"; journal through QAEvaluation, ReleaseCandidate, Canary, RolledBack and Blocked: true; error <nil> |
| recovery report restores the last known good release | pass | release sha256:850ed9ad0333480ff380893eac26a76ef75691d27ffde02c8dd4bcc4b067ceac restored to sha256:a09c671f89f14cb0b27a18d52eb078d09ea70acfe487f1ef5b6deb47ef693d5d at 2026-11-12T10:12:26Z, observed sha256:a09c671f89f14cb0b27a18d52eb078d09ea70acfe487f1ef5b6deb47ef693d5d healthy true, data untouched true, reason "a synthetic check failed during the low-traffic hold"; the installed release is sha256:a09c671f89f14cb0b27a18d52eb078d09ea70acfe487f1ef5b6deb47ef693d5d; errors <nil>, <nil> |
| the last known good release serves through the edge again | pass | the edge serves revision sha256:a09c671f89f14cb0b27a18d52eb078d09ea70acfe487f1ef5b6deb47ef693d5d (installed sha256:a09c671f89f14cb0b27a18d52eb078d09ea70acfe487f1ef5b6deb47ef693d5d); GET / answered HTTP 200 "Hello from the sample app: visit 1"; errors <nil>, <nil> |
| pull request left unmerged | pass | https://github.com/bestagentkits/sisf-sandbox/pull/32 state "OPEN", merge commit "", error <nil> |
| issue left Blocked with the reason | pass | GitHub "OPEN", factory record "Blocked", last journal state "Blocked", inspect error <nil>; the reason "a synthetic check failed during the low-traffic hold" on the issue: "### Release rolled back The release `sha256:850ed9ad0333480ff380893eac26a76ef75691d27ffde02c8dd4bcc4b067ceac` of run `run-cbb81eb3c71d07fb5b0d34a2` was rolled back to the last known good release `sha256:a09c671f89f14cb0b27a18d52eb078d09ea70...", error <nil> |
| the rolled-back program is removed from the supervisor's store | pass | the store has no file for sha256:850ed9ad0333480ff380893eac26a76ef75691d27ffde02c8dd4bcc4b067ceac; it holds 0 file(s) |
| no paid model configured | pass | coder "" (empty or fake is the scripted agent), QA launcher is the rehearsal's fake judge: true |
| the factory addressed only the sandbox | pass | factory.yaml names the sandbox; 1 issue(s) in its journal and 1 pull request(s) of its run; manifest error <nil>, journal error <nil> |
| other activity of the gh account | info | 12 public event(s) of the gh account since the start named another repository; they are other work of the same account, not counted against the factory, and not named here |
| release record pull request closed | info | the run left no release record open |
| rehearsal's own issue and pull requests closed | info | issue #31 closed (error <nil>); https://github.com/bestagentkits/sisf-sandbox/pull/32 closed (error <nil>) |

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
- `/usr/local/bin/docker network create --internal sisf-smoke-todvqy-sandbox`
- `/usr/local/bin/docker network create sisf-smoke-todvqy-out`
- `/usr/local/bin/docker run -d --name sisf-smoke-todvqy-registry -p [REDACTED:ip]:58282:5000 registry:2`
- `/usr/local/bin/docker build -q -f <repo>/tests/smoke/sandbox/runner.Dockerfile -t [REDACTED:ip]:58282/sisf-smoke-runner:sisf-smoke-todvqy <repo>/tests/smoke/sandbox`
- `/usr/local/bin/docker push -q [REDACTED:ip]:58282/sisf-smoke-runner:sisf-smoke-todvqy`
- `/usr/local/bin/docker image inspect --format {{join .RepoDigests " "}} [REDACTED:ip]:58282/sisf-smoke-runner:sisf-smoke-todvqy`
- `/usr/local/bin/docker build -q -f <repo>/build/edge.Dockerfile -t sisf-smoke-todvqy-edge:run <repo>`
- `/usr/local/bin/docker create --name sisf-smoke-todvqy-egress --network sisf-smoke-todvqy-out --read-only --cap-drop ALL --security-opt no-new-privileges sisf-smoke-todvqy-edge:run edge --listen=[REDACTED:ip]:8080 --upstream=http://[REDACTED:ip]:9 --egress-listen=egress:3128 --allow-non-loopback`
- `/usr/local/bin/docker network connect --alias egress sisf-smoke-todvqy-sandbox sisf-smoke-todvqy-egress`
- `/usr/local/bin/docker start sisf-smoke-todvqy-egress`
- `git init -q --bare <run-dir>/mirror/sandbox.git`
- `git -C <run-dir>/mirror/sandbox.git config uploadpack.allowAnySHA1InWant true`
- `git -C <run-dir>/mirror/sandbox.git -c credential.helper= -c credential.helper=!gh auth git-credential fetch -q --prune https://github.com/bestagentkits/sisf-sandbox.git +refs/heads/*:refs/heads/*`
- `/usr/local/bin/docker run -d --name sisf-smoke-todvqy-git --network sisf-smoke-todvqy-sandbox --network-alias git-mirror --user 65534:65534 -e GIT_CONFIG_COUNT=1 -e GIT_CONFIG_KEY_0=safe.directory -e GIT_CONFIG_VALUE_0=* -v <run-dir>/mirror:/srv/git:ro --read-only --cap-drop ALL --security-opt no-new-privileges [REDACTED:ip]:58282/sisf-smoke-runner@sha256:ac218260676c9b07e4eebc8575a2593c3aa45ea0cb28e1049f5f1269ac62c5c7 git daemon --base-path=/srv/git --export-all --reuseaddr /srv/git`
- `/usr/local/bin/docker run --rm --name sisf-smoke-todvqy-isolation --network sisf-smoke-todvqy-sandbox --read-only --cap-drop ALL [REDACTED:ip]:58282/sisf-smoke-runner@sha256:ac218260676c9b07e4eebc8575a2593c3aa45ea0cb28e1049f5f1269ac62c5c7 wget -q -T 5 -O /dev/null http://[REDACTED:ip]/`
- `<run-dir>/bin/sisf init --dev --mode standalone --dir <run-dir>/factory --repo bestagentkits/sisf-sandbox --github-credential gh-cli --budget-cap 5 --alert-threshold 2 --qa-judge agent --qa-agent codex --exec docker --exec-docker-endpoint http://[REDACTED:ip]:58283 --exec-docker-network sisf-smoke-todvqy-sandbox --egress-proxy http://egress:3128 --runner-image [REDACTED:ip]:58282/sisf-smoke-runner@sha256:ac218260676c9b07e4eebc8575a2593c3aa45ea0cb28e1049f5f1269ac62c5c7 --qa-agent-binary <run-dir>/bin/qa-codex-launcher --build-command /usr/local/bin/sisf-smoke-build --github-reconcile-interval 15s`
- `<run-dir>/bin/sisf docker-filter --role runner --listen [REDACTED:ip]:58283 --factory-id sisf-sisf-sandbox --network sisf-smoke-todvqy-sandbox --image [REDACTED:ip]:58282/sisf-smoke-runner@sha256:ac218260676c9b07e4eebc8575a2593c3aa45ea0cb28e1049f5f1269ac62c5c7 --socket ~/.orbstack/run/docker.sock`
- `gh api repos/bestagentkits/sisf-sandbox/commits/HEAD --jq .sha`
- `gh api -H Accept: application/vnd.github.raw repos/bestagentkits/sisf-sandbox/contents/app.go?ref=7dbd2b32e0740c8061671f6db4bb872d0cf4b466`
- `<run-dir>/bin/sisf up --dir <run-dir>/factory --mode standalone --source <repo>`
- `<run-dir>/bin/sisf doctor --dir <run-dir>/factory`
- `<run-dir>/factory/bin/factoryd policy approve --revision 7dbd2b32e0740c8061671f6db4bb872d0cf4b466`
- `<run-dir>/bin/sisf login --url http://[REDACTED:ip]:7463 --dir <run-dir>/factory --name sisf-smoke`
- `gh issue create --repo bestagentkits/sisf-sandbox --title release rehearsal 20261112-095915: a candidate that must be rolled back --body ## Scope

Add `release_rehearsal_fault.go`, which turns the application's injected page failures on
for every request. This issue is a release rehearsal of the factory that
must end in a rollback: a scripted agent writes the change and a fake judge
scores it; no model runs. The factory's synthetic suite must fail the
release and the last known good release must be restored.

## Affected features

- Fault injection

## Definition of Done

- `release_rehearsal_fault.go` exists.
- `go test ./...` passes.
 --label sisf:ready --label kind:improvement`
- `<run-dir>/bin/sisf call runs.inspect --input {"issue":"bestagentkits/sisf-sandbox#31"}`
- `gh issue view 31 --repo bestagentkits/sisf-sandbox --json state,stateReason --jq .state + " " + .stateReason`
- `gh pr view https://github.com/bestagentkits/sisf-sandbox/pull/32 --json state,mergeCommit`
- `gh api --paginate repos/bestagentkits/sisf-sandbox/issues/31/comments?per_page=100 --jq .[] | .body | @json`
- `<run-dir>/bin/sisf call budget.get --input {}`
- `gh api users/<gh-login>/events?per_page=100 --jq .[] | [.created_at, .repo.name, .type] | @tsv`
- `gh pr list --repo bestagentkits/sisf-sandbox --state open --json number,headRefName --limit 1000`
- `<run-dir>/bin/sisf down --dir <run-dir>/factory`
- `gh issue close 31 --repo bestagentkits/sisf-sandbox --reason not planned --comment Closed by the release rehearsal so the sandbox keeps no open factory work.`
- `gh pr close https://github.com/bestagentkits/sisf-sandbox/pull/32 --comment Closed by the release rehearsal so the sandbox keeps no open factory work; the branch is kept.`
- `/usr/local/bin/docker ps -aq --filter name=^sisf-smoke-todvqy-`
- `/usr/local/bin/docker ps -aq --filter label=sisf.smoke=sisf-smoke-todvqy`
- `/usr/local/bin/docker ps -aq --filter network=sisf-smoke-todvqy-sandbox`
- `/usr/local/bin/docker rm -f 5b608b3cf0ec`
- `/usr/local/bin/docker rm -f c58eea6df655`
- `/usr/local/bin/docker rm -f 4e78e2034a05`
- `/usr/local/bin/docker network rm sisf-smoke-todvqy-sandbox`
- `/usr/local/bin/docker network rm sisf-smoke-todvqy-out`
- `/usr/local/bin/docker image rm sisf-smoke-todvqy-edge:run`
- `/usr/local/bin/docker image rm [REDACTED:ip]:58282/sisf-smoke-runner:sisf-smoke-todvqy`
