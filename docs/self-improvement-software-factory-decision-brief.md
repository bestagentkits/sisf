# Self-improvement Software Factory (SISF)

> **Decision brief & implementation contract**  
> Ngày: 2026-11-10 · Trạng thái: **Product decisions fixed, implementation proposed**  
> Nguồn xuất phát: `saasvn/tose-app`, HHP decision #574, kiến trúc phác thảo do Owner cung cấp.  
> Mục đích: đưa cho coding agent trong **fresh context** để triển khai theo từng lát cắt có kiểm chứng.  
> **Không** tự coi tài liệu này là quyền thay đổi production hay làm rộng quyền của agent.

## 0. Tóm tắt quyết định không được thay đổi

1. **Tên sản phẩm:** `Self-improvement Software Factory` (SISF), framework mã nguồn mở. Là một *software factory* khép kín từ tiếp nhận yêu cầu đến xuất bản, giám sát, phục hồi và tự cải tiến chính hệ thống.
2. **Bất biến kiến trúc:** Zone 1 (Application), Zone 2 (Agent factory) và Zone 3 (Control plane) **phải cùng nằm trong một factory deployment**; có thể dùng nhiều process/container/service nhưng chung `factory_id`, release manifest, configuration lifecycle, gateway và authority plane. Không chia thành ba sản phẩm hay ba deployment độc lập. Một cloud agent bên ngoài chỉ là **execution backend** được Zone 2 gọi, không phải Zone 2 thay thế.
3. **Zone 1:** ứng dụng phục vụ người dùng có Webapp, API, MCP, WebMCP; tạo logs, traces, sessions, activities, feedback.
4. **Zone 2:** có built-in runtime Claude CLI, Codex CLI/app-server, Pi; cho phép adapter đến Claude Routines/Claude Cloud, Codex managed/cloud APIs, TOSE Cloud Agents, nếu API, quyền truy cập và lifecycle tương thích được kiểm chứng. Phải có dispatcher, state/worker control và QA orchestration **bên trong factory deployment**.
5. **Zone 3:** control plane sở hữu dashboard, API, CLI, MCP, WebMCP, authentication, RBAC, dashboard đồ thị, state graph, trạng thái, backlogs, approvals, traces/logs/activities/sessions, secrets, budget, feature catalogue, ADR, docs/changelog, release/rollback.
6. **GitHub SSOT:** Git là nơi có thẩm quyền với source/manifest/docs/ADR/decision policy; Issues/PRs/checks/releases thể hiện công việc, bằng chứng và quyết định. Webhooks phát sự kiện. **Không dùng GitHub thay DB giao dịch cho locks, jobs, ledger, sessions/secrets**; các bảng vận hành là trạng thái phái sinh/tác nghiệp và luôn tham chiếu SHA/issue ID/decision revision.
7. **Nền tảng triết lý:** `AGENTS.md`, `REVIEW.md`, `DECISIONS.md` (quy tắc ưu tiên và câu hỏi cho System One), `FEATURES.md` hoặc `features.yaml`, `docs/adr/` và các policy được ký/xác nhận bởi Owner. Dùng để giải thích vì sao cần cải tiến và cách đánh giá. Agent không thể sửa rule để tự qua QA.
8. **Tự triển khai Zone 1:** sau khi QA agent độc lập chấm điểm đạt **và** các deterministic gates/rollback preflight đạt; triển khai theo policy A/B/C. Có progressive rollout/canary hoặc blue-green với monitoring và **automatic rollback khi vượt ngưỡng lỗi**; nếu không thể quan sát/rollback an toàn thì không tự publish.
9. **Self-improvement of the factory:** có thể cải tiến code Zone 1/2/3 của chính factory; cập nhật bộ máy điều phối/QA/policy thuộc *trusted control surface*, yêu cầu bảo vệ đặc biệt và cơ chế updater/supervisor nguyên trạng. Không cho ứng viên mới vừa sửa tiêu chí QA vừa tự chấm điểm bản thân.
10. **Authentication:** đăng nhập GitHub hoặc Google SSO. Cả hai ánh xạ vào actor/role nội bộ; GitHub App installation là quyền đối với repo, **khác** đăng nhập GitHub của người dùng. MCP từ xa dùng cơ chế OAuth 2.1 phù hợp đặc tả; browser WebMCP không có quyền vượt backend.
11. **Ngân sách mặc định:** `unlimited` theo yêu cầu Owner; vẫn metering, hiển thị chi phí, cảnh báo và có thể cấu hình hard cap; không đồng nghĩa provider API cho phép chi vô hạn, không bao giờ dùng giá trị này để bỏ giới hạn security, concurrency, thời lượng hoặc số lần thử.
12. **Feature mới và upstream contribution:** Owner quyết định tính năng mới, mở rộng phạm vi/permissions, thay đổi policy quan trọng, gửi contribution về package gốc. Trong phạm vi đã ủy quyền, agent có thể tự cải tiến chức năng đã có. Cải tiến vẫn thuộc **một factory deployment** trừ khi Owner chủ động chia sẻ.

## 1. Mục tiêu và anti-goals

### Mục tiêu

- Một người có repository GitHub cài **một deployment SISF gồm đủ ba zone** và có hệ thống tự vận hành end-to-end; không yêu cầu TOSE.
- Hai đầu vào đều khép kín: (a) GitHub issue được gắn nhãn hợp lệ và (b) telemetry/feedback được Decision Layer chuyển thành issue/proposal có căn cứ.
- Mọi tác vụ có SoW/DoD versioned, agent worker và QA agent tách biệt, tiến độ và approvals quan sát được qua UI/API/CLI/MCP/WebMCP.
- Xuất bản Zone 1 an toàn, đo lại hậu triển khai, tự rollback; factory có đường nâng cấp chính mình với trusted updater.
- Backend thay thế được, nhưng không hứa mọi provider/platform có cùng capability.

### Không phải mục tiêu phiên đầu

- Multi-tenant SaaS, marketplace, bán licenses, đóng góp upstream tự động, hệ thống CI chung thay GitHub Actions, tự xây Git server/OCI registry/identity provider, scheduler phân tán phức tạp, hỗ trợ toàn bộ nền tảng bằng tên gọi mà chưa qua E2E.
- Tự mở tính năng mới hoặc để issue người ngoài nâng quyền triển khai.
- Đảm bảo 'unlimited' = không thể bị tính phí bởi provider.

## 2. Kiến trúc logic: một deployment, ba zone

```mermaid
flowchart TB
  G[(GitHub SSOT: source / issues / PR / decisions / release)]
  subgraph F[ONE SISF DEPLOYMENT · factory_id · release manifest · lifecycle]
    direction TB
    subgraph Z1[ZONE 1 · APPLICATION]
      A[Webapp + API + MCP + WebMCP] --> T[Traces · Logs · Sessions · Feedback]
    end
    subgraph Z2[ZONE 2 · AGENT FACTORY]
      D[Dispatcher + Run state] --> W[Agent adapter: Claude / Codex / Pi / TOSE / Cloud]
      W --> Q[Independent QA agent + deterministic tests]
      Q --> C[Release candidate + preview]
    end
    subgraph Z3[ZONE 3 · CONTROL PLANE]
      UI[Dashboard · API · CLI · MCP · WebMCP]
      AUTH[GitHub & Google SSO · OAuth 2.1 · RBAC]
      POL[AGENTS + REVIEW + DECISIONS + feature catalogue + ADR]
      DE[Decision Layer: Jev / Clef · provider adapters]
      OPS[Issue intake · State machine · Backlog · Budget · Secrets · Approvals]
      R[Trusted release supervisor · Canary · Monitor · Rollback]
      UI --> AUTH --> OPS
      POL --> DE --> OPS
      OPS --> R
    end
    T --> DE
    OPS --> D
    C --> R
    R --> A
  end
  G <-->|webhooks, reconciliation, GitHub App, refs| OPS
  G <-->|PRs/checks/artifacts refs| Q
  X[Remote agent services are OPTIONAL executors, not another factory deployment]
  W -.-> X
```

**Triển khai chung:** một `factory.yaml` là manifest của **một factory deployment** với `factory_id`, các thành phần `application`, `agent`, `control`, kho lưu trữ, trusted release supervisor và networking; runtime có thể là nhiều container trong cùng Compose stack/Kubernetes release/Cloudflare app deployment hoặc nhiều process. Platform không bảo đảm hợp đồng này thì ghi *unsupported full deployment*; không lén chuyển Zone 2 hoặc Zone 3 thành một SaaS bắt buộc bên ngoài. Cloud agent API và GitHub là các **tích hợp** được phép.

**Vòng đời:** khởi tạo/tạm dừng/cập nhật/phục hồi/dỡ cài đặt mang tính factory deployment, nhưng mỗi zone có process/health check riêng; release manifest ghim toàn bộ artifacts/digests và tương thích schema. Khi đổi Zone 1, không tự tái cấu hình Zone 2/3 nếu không cần.

**SSOT/GitHub:** mỗi zone có nguồn và quyết định trong GitHub nhưng không có nghĩa mỗi request ở Zone 1 phải đồng bộ GitHub. GitHub ngắt thì Zone 1 tiếp tục phục vụ revision đã xác minh; ingestion/publish cần dừng hoặc xếp đợi an toàn.

## 3. Định nghĩa interface/plugin

### 3.1 AgentBackend

Khai báo capabilities trước khi dùng:

`start(task, workspaceRef, scopes) -> runRef`, `status(runRef)`, `events(runRef,cursor)`, `cancel(runRef)`, `collectArtifacts(runRef)`; tùy chọn `resume`, `checkpoint`, `usage`, `humanInput`, `sessionLink`, `webhooks`.

- Built-in: Claude CLI; Codex CLI/app-server; Pi RPC. Agent chạy trong task workspace có giới hạn filesystem/network/time.
- Remote: Claude Routines/Cloud, OpenAI Codex Agents API/Cloud khi xác nhận giao thức, TOSE Cloud Agents. Adapter trả `NOT_SUPPORTED` khi thiếu cancel/replay/usage và không được tuyên bố có hard budget nếu provider không cưỡng chế được.
- Không đưa provider token/subscription token vào logs hoặc container người dùng; chỉ credentials theo phạm vi, cấp ngắn hạn.
- **Điều phối và tính toán quyết định nằm tại Zone 2/3 cùng deployment**, kể cả agent thực thi chạy ở bên ngoài.

### 3.2 ExecutionBackend

`createSandbox`, `exec`, `files`, `snapshot`, `inspect`, `destroy`, `health`, `capabilities`. Local process (trusted single-user), Docker, Docker+gVisor, K8s, Cloudflare Sandbox, Vercel Sandbox, TOSE runtime, backend tương thích khác. Không đồng nhất *hosted provider* với *isolation guarantee*. Nếu không thể chứng minh cách ly cần thiết, không chạy code không tin cậy ở chế độ tự động.

### 3.3 DeploymentBackend

`stage(artifact)`, `preview`, `measure`, `promote`, `observedRevision`, `rollback`, `capabilities`. Bắt buộc hỗ trợ ít nhất **một phương án chuyển phiên bản an toàn có đường quay lại** trước khi tự publish. Docker/Compose và Kubernetes đầu tiên; serverless adapter theo capability matrix. Không dùng API update thành công thay cho observed healthy release.

### 3.4 Identity, State, Secret, Telemetry, GitHub

- `IdentityProvider`: GitHub OIDC/OAuth login flow và Google OIDC; *GitHub App installation* riêng cho quyền repository.
- `StateStore`: SQLite WAL cho single-host; PostgreSQL cho HA/multi-process; lock/lease/CAS, outbox, transactional budget. Migration versioned.
- `SecretProvider`: local encrypted-at-rest, env reference hoặc cloud secret manager; tuyệt đối không lưu secret plaintext trong GitHub.
- `TelemetryAdapter`: OpenTelemetry spans + logs/metrics/events normalized, privacy redaction, attribute `factory_id`, `deployment_id`, `zone`, `release_digest`, `run_id`, `issue_ref`.
- `GitHubAdapter`: signed webhooks, event dedup, polling/reconciliation, issues/comments/labels, PR/checks/releases, token rotation/revocation.

## 4. Quy tắc quyết định có căn cứ

### 4.1 Các file nền

| File | Trách nhiệm | Chỉnh sửa tự động? |
|---|---|---|
| `AGENTS.md` | Bối cảnh sản phẩm, cách làm việc, nguyên tắc kỹ thuật cho agent | Có thể đề xuất PR; không có thẩm quyền thay policy |
| `REVIEW.md` | Tiêu chí QA/release, evidence bắt buộc, review checklist | PR có Owner duyệt; agent không tự hạ ngưỡng |
| `DECISIONS.md` | Triết lý, mục tiêu, no-go, phương pháp phân loại/xếp ưu tiên của Decision Layer/System One | PR có Owner duyệt; luôn ghim SHA và schema version |
| `FEATURES.md` hoặc `features.yaml` | Chức năng đã được con người chấp thuận; định nghĩa/giới hạn hành vi | Mọi feature mới phải có Owner approval |
| `docs/adr/*.md` | Ràng buộc kiến trúc và lý do quyết định | Có lịch sử và Owner review khi thay quyết định |
| `.sisf/factory.yaml` | Khai báo 3 zone, adapter, cấu hình desired state, release policy | Owner/Git branch protection; chi phí thực tế không nằm trong file |

**Thứ tự áp dụng:** bất biến an toàn trong code + quyền ủy quyền đã xác minh > owner-approved policy/config ở SHA protected > `DECISIONS.md` / `REVIEW.md` / `FEATURES.md` / ADR > hướng dẫn thực thi `AGENTS.md` > issue/bình luận/logs. Mâu thuẫn hoặc SHA chưa được chấp thuận => hold, không để model tùy chọn luật thuận tiện hơn.

**Lưu ý:** văn bản Markdown có thể hướng dẫn model nhưng không thay cho kiểm tra deterministic. Những điều khoản cần cưỡng chế phải có schema cấu trúc/test tương ứng. Mỗi quyết định lưu `policy_sha`, `review_sha`, `decision_sha`, `feature_catalog_sha`, `model_route`, evidence IDs để audit.

### 4.2 Hợp đồng Decision Layer

Luồng: `signal → data quality + redaction → baseline / affected feature → candidate goal → typed questions → rationale + evidence → scope & policy check → task proposal`.

- Evidence gồm metric, xu hướng/baseline, số mẫu, khoảng thời gian, correlation issue/session, ảnh hưởng user, tính lặp lại, mức độ tin cậy, source refs. Không đủ dữ liệu thì chọn `collect_more_evidence` hoặc `ignore`, không ép có cải tiến.
- Analysis Agent tạo mục tiêu, phương án, SoW dự thảo và measurable DoD. Jev/Clef lựa chọn `ignore|observe|investigate|propose|implement|stop`, `bug|existing_improvement|new_feature|policy_change`, priority và các điểm chưa chắc chắn.
- `DECISIONS.md` định nghĩa câu hỏi typed cho System One và trọng số/ưu tiên; model không có quyền sửa scope, test evidence, release threshold hay ngân sách.
- Model classification chỉ làm **tăng mức thận trọng** hoặc ưu tiên; deterministic checks có quyền veto.

## 5. GitHub issue workflow (bắt buộc)

### 5.1 Đầu vào và nhãn

- `sisf:ready`: yêu cầu intake; không mặc nhiên cấp quyền thực thi. Có thể cho cấu hình ánh xạ từ nhãn hiện có (ví dụ `ai-handle`), mặc định **tắt**.
- `kind:bug`, `kind:improvement`, `kind:feature`, `kind:question`.
- `sisf:needs-info`, `sisf:approved`, `sisf:running`, `sisf:qa`, `sisf:canary`, `sisf:blocked`, `sisf:verified`; framework cập nhật các nhãn trạng thái, không dùng nhãn làm distributed lock.
- `agent:*` là sở thích định tuyến; người gắn nhãn không tự tạo quyền hoặc ngân sách. `deployment:<id>` phải được xác thực với mapping repo/installation.

### 5.2 State machine

```mermaid
stateDiagram-v2
  [*] --> Intake
  Intake --> Clarifying: thiếu thông tin
  Clarifying --> ScopeDraft: phản hồi đầy đủ
  Intake --> ScopeDraft: đủ thông tin
  ScopeDraft --> ApprovalPending: new feature / change outside delegation
  ScopeDraft --> Ready: policy permits / approved
  ApprovalPending --> Ready: owner approved
  ApprovalPending --> Blocked: rejected/expired
  Ready --> Claimed: atomic lease + budget reservation
  Claimed --> Developing
  Developing --> QAEvaluation: patch + tests + artifacts
  QAEvaluation --> Developing: not pass / retry allowed
  QAEvaluation --> ReleaseCandidate: score + hard gates pass
  ReleaseCandidate --> Canary
  Canary --> Monitoring: rollout healthy
  Canary --> RolledBack: error threshold exceeded
  Monitoring --> Verified: post-deploy DoD + observation
  Monitoring --> RolledBack: regression
  Verified --> Closed: close issue + changelog + feature catalog
  RolledBack --> Blocked: evidence + recovery report
  Blocked --> Ready: safe authorized retry
```

- Mọi trạng thái ghi vào DB vận hành có `version`, `idempotency_key`, `lease_owner`, `attempt`, `run_id`, `issue_id` rồi chiếu lên GitHub. Không giả định webhook giao đúng thứ tự hay một lần duy nhất.
- GitHub webhook: verify HMAC secret, action/type, install/repo, delivery ID; lưu inbox transaction, trả 2xx nhanh, xử lý async, định kỳ reconcile và redeliver theo phạm vi có thể. Bot tự chỉnh nhãn/comment phải được chống vòng lặp.
- AI hỏi làm rõ bằng comment; issue không có phản hồi theo timeout thì chuyển `needs-info`, không tiêu tiền vô hạn.
- SoW/DoD được version hóa và đính kèm issue/PR; sửa đáng kể sau approval làm approval cũ hết hiệu lực.
- Tiến độ: một comment tổng hợp có thể cập nhật, checkpoints/PR links, trạng thái ngay trong dashboard; tránh spam từng log/token vào issue.
- `close issue` chỉ sau khi **observed deployed version** đạt DoD và qua monitor/rollback window. PR merge không tự động đồng nghĩa hoàn thành.

### 5.3 Luồng phản hồi/tính năng qua MCP/API/UI/WebMCP

`report / propose → triage / phỏng vấn → phân loại → SoW/DoD → tạo issue → thông qua approval theo loại → chạy pipeline trên`. Với feature mới: **Owner chấp thuận rõ scope** trước coding/production. Người gửi report không trở thành Owner.

### 5.4 Approval inbox

Mọi protocol có cùng `list approvals`, `get proposal + evidence + diff + affected features`, `approve/reject/request-changes`, `status/audit`. Approval chỉ chấp nhận actor có quyền, dựa trên `subject_digest`, policy revision, expiry, nonce và không được replay; hành động nhạy cảm yêu cầu step-up/verified user confirmation, **không cho một browser agent dùng ambient Owner session tự giả danh người duyệt**. Với CLI/MCP có thể khởi động device/browser verification flow, rồi nhận kết quả qua chính protocol đó.

## 6. QA, tự publish, tự rollback

### 6.1 QA độc lập

- Coding agent khác identity/workspace với QA agent. QA có test baseline **read-only**, được khóa với commit và policy SHA, chạy deterministic suite + threat negative tests; có thể dùng judge model cho tiêu chí ngữ nghĩa.
- **Rubric khởi điểm cần hiệu chỉnh:** correctness/regression 30, security + auth 25, contract + data safety 20, UX/performance/output 15, observability + rollback 10. Default proposal `score >= 85/100`, không có một hard-gate failure nào; số này là **mức khởi điểm thiết kế**, không phải ngưỡng được nghiên cứu chứng minh.
- *Hard gates:* tests/contract, permissions, migrations, secrets, confidence of observed build, independent evaluator, resource/budget policy, rollback readiness. QA score cao không được override hard failure.
- Ghi `qa_receipt` ký bởi trusted evaluator: candidate digest, suite digest, baseline SHA, QA agent identity, checks, score, timestamp, expires, evaluator attempt.
- QA không thể chấm mã chưa xây xong hoặc mượn receipt của commit khác.

### 6.2 Release pipeline

`build immutable candidate → preview → QA → preflight rollback → canary OR blue-green → observed revision + metrics → promote/restore`.

- Có thể bắt đầu bằng **blue-green + smoke + monitor**, khi environment hỗ trợ canary traffic thì `5%→25%→100%` là **mặc định đề xuất, phải hiệu chỉnh**, không được ép platform không có traffic split.
- Auto rollback khi metric vượt `rollback_error_rate` sau `min_sample_count` trong `window` và vượt baseline có ý nghĩa; ví dụ cấu hình minh họa: 5xx > 2% và tăng > 1 điểm phần trăm so baseline, `min_samples=100`, `window=10m`, 2 cửa sổ liên tiếp. Tất cả là thông số *khởi điểm* cần đánh giá trên lưu lượng thực; critical security/health failure rollback hoặc dừng ngay. Với lưu lượng thấp, không kết luận từ 1 request lỗi; dùng smoke/synthetic + hold.
- Khi thiếu metrics/không có bản phục hồi/broken deployment adapter: `pause/hold`, không tự coi thành công.
- Rollback chỉ thay code/config tương thích; không tự ghi đè dữ liệu khách hàng bằng snapshot cũ. Cần versioned storage migrations tương thích ngược hoặc Owner approval.
- Release monitor không được cập nhật lại QA baseline bởi chính bản đang được thử nghiệm.

### 6.3 Khả năng cải tiến chính factory

- Factory source bao gồm code của cả ba zone, cho phép làm ticket/sửa lỗi của Zone 2/3, **không** giới hạn tự cải tiến vào Webapp của người dùng.
- Trusted release supervisor ở Zone 3 là tiến trình/image nhỏ, riêng và pinned, giữ LKG bên ngoài vùng sửa của ứng viên, nhưng **vẫn thuộc một factory deployment**. Không cần service ngoài factory để cứu factory.
- Trong một lượt tự nâng cấp bản thân, candidate không được thay đồng thời trusted supervisor, signing keys, policy enforcement, QA baseline hoặc rollback rules. Những thay đổi đó là **governance change có human approval**, đi qua một chu kỳ cập nhật riêng.
- DoD phải có thử nghiệm update Zone 3 không nhạy cảm (ví dụ trang read-only dashboard), self-monitor và auto-recover; chứng minh Zone 1 vẫn phục vụ khi Zone 2/3 khởi động lại.

## 7. Authentication, authorization và secrets

- GitHub: login qua GitHub OAuth/GitHub App user auth; quyền repo qua **installation token** tối thiểu, không đánh đồng login với quyền repo. Google SSO dùng OIDC ID token, định danh theo `issuer+sub`, validate signature/iss/aud/exp/nonce; không định danh bằng email đơn thuần.
- Workspace roles: Owner, Maintainer, Reporter, Viewer, Agent, QA, Publisher/Deployment service. Mapping GitHub membership + local granted roles, kiểm tra tại thời điểm tác vụ, không dùng nhãn issue làm role.
- API phiên người dùng: session cookie HttpOnly/SameSite + CSRF cho browser; personal access/service token ngắn hạn, scopes cho API/CLI.
- Remote MCP: OAuth 2.1 + Protected Resource Metadata + resource-bound scopes, issuer/audience validation. WebMCP: chỉ khi browser hỗ trợ, bám session hiện có và chung backend auth; khi không hỗ trợ phải có API/MCP/UI fallback.
- Secrets: lưu encrypted, không gửi vào issue/PR, không để agent chạy trên mã không tin cậy đọc token GitHub App/Owner. Per-run secret broker giới hạn purpose/scope/TTL, revoke on cancel.
- Approval nhạy cảm là signed, single-use, subject/release/policy-bound, hết hạn; audit ai thực hiện (human hay agent).

## 8. Ngân sách và chi phí

- Mặc định **`budget.mode: unlimited`**, vẫn có usage ledger, dashboard expense/retry alerts, per-provider key controls và cancel. `unlimited` là không áp *hạn mức tiền do SISF tự đặt*, không cam kết chi phí thực tế vô hạn hoặc dùng provider miễn phí.
- Chủ factory có thể chọn `budget.mode: capped`, reset kỳ chi/one-time, dự phòng rollback. Đồng bộ chi phí theo provider adapter; chênh lệch/usage chưa đo được hiển thị `unverified`, không thổi phồng guarantee.
- Dù unlimited tiền, mọi task có timeout, max attempts, concurrency, cancellation, rate limit và resource quotas để chống vòng lặp. Không tự nâng role/scope.
- Khi người dùng đặt hard cap, giữ chỗ trước khi bắt đầu; side effects phải idempotent và chi phí vượt dự toán được phản ánh/suspend. Factory local không thể cưỡng chế nhà cung cấp model nếu provider không có hard limit: báo mức bảo đảm công khai.

## 9. Dashboard và giao diện công cụ

### Trang tối thiểu

- **Overview:** sức khỏe ba zone, revision chạy, chiến lược canary, error rate, throughput, chi phí, nút stop/rollback.
- **Workboard:** backlog/issue list, trạng thái và owner/agent, SLA/age, live progress, retry, session link.
- **Run graph:** DAG/state graph cho từng nhiệm vụ, thời gian, tool events, decision receipts và QA evidence.
- **Telemetry:** traces/logs/activities/sessions đã lọc, tìm theo release, issue, run, feature.
- **Approvals:** yêu cầu, impact, diff, SoW/DoD, quyết định, lý do, audit.
- **Product:** feature catalogue, changelog, docs/guides, ADR, DECISIONS.md history.
- **Security & finance:** user roles, agent capabilities, secret refs, budget unlimited/capped, usage breakdown, SSO/GitHub App installations.

### Một service layer, nhiều protocol

- REST/OpenAPI là hợp đồng backend nội bộ; CLI, Dashboard, MCP và WebMCP gọi cùng policy/action service, không tự định nghĩa quyền độc lập.
- Truy vấn: `factory.status`, `issues.list`, `runs.inspect`, `approvals.list`, `approvals.decide`, `secrets.list_refs`, `budget.get/set`, `decisions.propose_update`, `docs.list`, `features.list`, `changelog.list`, `deployments.rollback` (scopes khác nhau).
- Write nhạy cảm qua `plan -> approval -> execute -> audit`, có human reauthentication/step-up theo loại quyền; **không cấp permission qua câu mô tả tool**.
- WebMCP là client-side tool surface tùy chọn, không thay thế remote MCP; kế hoạch hỗ trợ browser feature detection và fallback.

## 10. Deployment portability: compliance classes

**Bất biến:** không platform adapter nào được tự ý chia một SISF deployment thành ba application deployment độc lập; phải chung factory release manifest và lifecycle. Các dịch vụ hạ tầng thuê ngoài (GitHub, IdP, DB/object store, remote coding backend) là dependencies, không phải một zone mới.

| Profile | Cách đóng gói | Kết luận hỗ trợ |
|---|---|---|
| local native | app + dispatcher/worker + control plane trong một supervised local install, state SQLite | MVP; chỉ dùng code tin cậy nếu thiếu sandbox |
| Docker Compose | một compose project, zone service tách container + DB/volume, pin image digests | **MVP bắt buộc**; gVisor tùy chọn khi host hỗ trợ |
| Kubernetes | một Helm release/namespace ownership, app/development runner/control + persistent store/rollback | giai đoạn tiếp; security E2E bắt buộc |
| TOSE | một factory deployment với TOSE storage/runtime/budget adapters | giai đoạn tiếp, không phụ thuộc bắt buộc |
| Cloudflare | Workers + container/sandbox/DO bindings dưới một factory application deployment có đủ zone | chỉ đánh dấu đạt sau E2E; Workers thuần không thay shell runner |
| Vercel | App/API + agent dispatcher/job control + Vercel Sandbox và persistent state trong cùng logical factory installation | chỉ đánh dấu đạt sau E2E và timeout/recovery proof |
| Render / Railway | service group/containers + persistent state dưới một factory installation manifest | sau E2E; không mặc định sandbox hardening |

Nếu nhà cung cấp không thể đáp ứng `single_factory_deployment`, `durable_tasks`, `safe_exec`, `releasable_zone1`, `rollback` thì profile ở trạng thái **experimental/unsupported**, không viết “deploy anywhere” với full capability.

## 11. Repository layout khuyến nghị

```text
sisf/                                      # OSS repo: github.com/bestagentkits/sisf
  cmd/sisf/                                # CLI
  cmd/factoryd/                           # Zone 3 server, dispatcher
  internal/ or pkg/core/                  # clean policy & decision contracts, no TOSE imports
  pkg/identity/ pkg/state/ pkg/github/
  pkg/agent/ pkg/execution/ pkg/deploy/
  pkg/qa/ pkg/telemetry/ pkg/approvals/
  adapters/agent/{claude,codex,pi,tose,claude-cloud,codex-cloud}/
  adapters/exec/{local,docker,kubernetes,cloudflare,vercel,tose}/
  adapters/deploy/{compose,kubernetes,tose,cloudflare,vercel,render,railway}/
  ui/                                      # Dashboard + optional WebMCP
  templates/                               # three-zone full deployment manifests
  examples/                                # hello-app, app-with-existing-repo
  docs/adr/ docs/guides/ docs/security/
  .sisf/                                   # example instance policy/config
  AGENTS.md REVIEW.md DECISIONS.md FEATURES.md
  LICENSE NOTICE.md SECURITY.md CONTRIBUTING.md
```

**Kế thừa:** ứng viên tái sử dụng từ `saasvn/tose-app/internal/harnessplatform` gồm `delegation`, `decision`, `scope`, `budget`, `promotion`, `upstream`. Phải loại `tose-v2/internal/policy`, TOSE OAuth/wallet/K8s binding và xác minh provenance/license trước khi public. `internal/services/piadapter` hiện gắn chặt vào K8s/agentsec; chỉ tái sử dụng contract và phần adapter giao thức, không mang cả TOSE agent service sang OSS. Giữ nguyên vận hành TOSE hiện tại trong lúc trích xuất.

**Giấy phép:** MIT, Owner đã chốt ngày 2026-11-10 (`LICENSE`, bản quyền AgentKit.best). Giấy phép này áp dụng cho mã và tài liệu viết mới trong repo; mã trích từ TOSE chỉ được đưa vào sau khi audit provenance và dependencies, tài liệu này không tự cấp phép mã nguồn cũ. MIT không có điều khoản cấp bằng sáng chế như Apache-2.0; nếu sau này cần thì đổi giấy phép là một quyết định của Owner qua ADR.

## 12. SoW và thứ tự triển khai (fixed scope cho coding agent)

### P0: ADR + kiến trúc bất biến + khảo sát quyền tái sử dụng

**Trong phạm vi:** lập ADR three-zones-one-deployment, trust roots, policy precedence; license/provenance; interface contracts; kịch bản test single-deployment; đánh dấu adapter capability matrix.

**DoD:** tài liệu + tests/conformance checker từ chối cấu hình thiếu zone; tách được danh tính factory/runtime IDs; không thay production TOSE; owner chấp thuận thay đổi API/format.

### P1: Bản chạy ba zone local/Compose + GitHub workflow

**Trong phạm vi:** một manifest dựng ứng dụng mẫu Zone 1 + dispatcher/agent zone 2 + control plane zone 3, SQLite durable state, GitHub App/webhook/poll, nhận issue và đồng bộ state, SoW/DoD/clarify comment, worker lease/idempotency, dashboard tối thiểu, stop/restart.

**DoD:** cài từ fresh clone bằng một entrypoint; health đủ 3 zone; issue có nhãn đi qua clarification đến claimed, duplicate/reordered webhook không chạy trùng; kill worker và restart vẫn resume/reconcile; trình duyệt và API xem cùng trạng thái.

### P2: Built-in agents + QA + phát hành có rollback

**Trong phạm vi:** hai built-in agent adapter thực đầu tiên, đề xuất Claude CLI và Codex; Pi là adapter built-in thứ ba trước khi tuyên bố v1 hoàn thiện; QA độc lập, baseline & typed QA receipts, preview, blue-green hoặc canary, observed revision, metrics threshold, automatic rollback, issue close sau verification; chứng minh factory tự cập nhật phần Zone 3 không nhạy cảm.

**DoD:** agent A làm patch, agent B QA, score và hard gates xác minh, triển khai Zone 1, inject lỗi khiến rollback tự động, issue mở lại/blocked với evidence; một self-update của factory và rollback được kiểm thử trên cùng deployment.

### P3: Auth/surfaces/Decision Layer/observability hoàn chỉnh

**Trong phạm vi:** GitHub/Google SSO; role/grant; dashboard đầy đủ; API/OpenAPI, CLI, remote MCP OAuth 2.1, WebMCP feature-detected; `DECISIONS.md` với typed model adapter Jev/Clef, telemetry-driven goal proposals, feature catalog/changelog/docs/ADR, approvals cross-surface, secrets, budgets default unlimited và tùy chọn capped.

**DoD:** cùng quyền và hành vi trên 5 surfaces; login 2 IdP và cross-role/cross-repo tests; Decision Layer đưa ra đề xuất có dẫn chứng và chọn do-nothing khi thiếu bằng chứng; Owner duyệt feature từ bất kỳ surface nào nhưng final human verification không thể bị agent bypass; budget measured và stop honored.

### P4: Remote agents + portability/benchmark + OSS release

**Trong phạm vi:** Pi (nếu chưa), Claude Cloud/Routines, Codex managed Agents API/Cloud, TOSE Cloud Agents dưới feature flags; provider capability validation, Kubernetes, TOSE và nền tảng serverless lần lượt; threat review, performance, privacy, licensing, documentation, example recipes.

**DoD:** ít nhất 2 môi trường triển khai đầy đủ độc lập (Compose + một nền tảng khác), 2 agent khác nhau xử lý cùng contract, failed/slow/unavailable provider không làm factory đánh dấu thành công; conformance matrix public; có hướng dẫn EN/VI; quyết định GO/MODIFY/STOP dựa trên số liệu.

### Ngoài phạm vi v1 mặc dù thuộc roadmap

- Tuyên bố full support tất cả host trước E2E.
- Tự phát hành phiên bản sửa trust root, auth provider, policy evaluator hoặc rollback supervisor mà không có human approval.
- Public multi-tenant billing/marketplace hoặc network Git server thay GitHub.

## 13. DoD hệ thống tổng thể (acceptance matrix)

| ID | Test có thể lặp lại | Pass criteria |
|---|---|---|
| SISF-01 | Fresh install Local/Compose | Đúng 1 `factory_id`, đủ Zone 1/2/3 cùng deployment manifest và chung lifecycle |
| SISF-02 | Issue có nhãn hợp lệ | Intake → clarification comment → SoW/DoD versioned → claim → coding → independent QA → deploy → verify → close; mọi mốc xuất hiện trên GitHub và UI |
| SISF-03 | Issue user ngoài quyền / feature chưa duyệt | Không triển khai/mở rộng quyền; feature mới vào approval inbox và yêu cầu Owner decision |
| SISF-04 | Hai sự kiện webhook trùng + reorder | Đúng 1 run có quyền claim, không chi trùng; reconciliation khôi phục sự kiện bỏ lỡ |
| SISF-05 | Worker chết giữa task | Khôi phục từ state/lease; fencing chặn báo cáo cũ; không mất SoW, PR hoặc artifact đã lưu |
| SISF-06 | QA agent tự chấm patch của mình | Bị từ chối; QA receipt pinned với candidate SHA và test baseline độc lập |
| SISF-07 | QA score đạt nhưng security/contract fail | Không publish bất kể score |
| SISF-08 | QA đạt + health tốt | Zone 1 lên đúng digest; kiểm tra observed revision; cập nhật changelog/feature catalogue đúng quyền |
| SISF-09 | Error rate vượt ngưỡng khi canary/monitor | Restore LKG tự động; log evidence + issue chưa close hoặc reopen; dữ liệu khách hàng nguyên vẹn |
| SISF-10 | Factory tự sửa một phần Zone 3 không nhạy cảm | QA + phát hành/rollback trong cùng factory deployment, supervisor không bị candidate thay thế |
| SISF-11 | `DECISIONS.md` đổi qua PR của agent không duyệt | Policy baseline cũ tiếp tục có hiệu lực; Decision Layer không có quyền thay tiêu chuẩn |
| SISF-12 | GitHub/Google SSO & OAuth | issuer/sub/audience, session, scopes, repo install mapping, approval step-up, revoke, CSRF negative tests đạt |
| SISF-13 | API/CLI/MCP/WebMCP/Dashboard | Cùng action service, phân quyền tương đương; WebMCP thiếu hỗ trợ vẫn có fallback |
| SISF-14 | Budget default unlimited/capped | Unlimited là default rõ ràng; chi phí được ghi nhận; capped reserve & stop, provider không hỗ trợ hard cap được đánh dấu |
| SISF-15 | Hai factory deployments cùng upstream | Cải tiến cục bộ A không đổi B hoặc upstream; contribution cần người dùng kích hoạt |
| SISF-16 | Chạy trên backend mới | Conformance matrix đủ lifecycle/isolation/rollback/observability; nếu thiếu thì đánh dấu unsupported |
| SISF-17 | No-activity / no-benefit telemetry | Không tạo issue vô hạn; có `ignore`/`observe`/`stop` đúng triết lý sản phẩm |
| SISF-18 | Fresh agent không có lịch sử chat | Đọc policy + SoW/DoD + repo; tái dựng context và hoàn tất công việc, không cần chat cũ |
| SISF-19 | Secrets + GitHub issue chứa prompt injection | Không vượt quyền, không lộ token, không tự thay QA/policy, không gọi domain egress cấm |
| SISF-20 | Release thiếu metrics hoặc khả năng rollback | Hold/blocked, không tự publish trên mô hình giả định thành công |

**Release GO** chỉ khi không có High/Critical security issue chưa xử lý, có báo cáo E2E thật và phần nào ghi experimental được đánh dấu trung thực. **Dừng/MODIFY** nếu không chứng minh một deployment 3 zone, nếu factory mất khả năng phục hồi, nếu agent tự cấp quyền, nếu tự close issue chưa đạt DoD, hoặc nếu không tạo giá trị hơn workflow GitHub+agents đơn giản.

## 14. Implementation instructions for the coding agent

1. **Không tự đảo ngược bất biến sản phẩm**. Nếu thấy 3 zone nằm trên 3 deployment riêng, xem đó là lỗi thiết kế; đề xuất phương án hoặc fail capability gate.
2. Bắt đầu bằng repo/ADR read-only và dependency & license audit; xác nhận source snapshot. Không cho rằng HHP pure policy core đã có API/worker chạy thực tế.
3. Tạo **smallest vertical slice**, commit từng bước; tests phải đỏ trước mỗi hành vi, negative tests cho bảo mật/chi tiền.
4. Giữ backward compatibility nếu tách code từ TOSE: chuyển qua adapter, không sửa logic sản xuất TOSE chỉ để thuận tiện trích xuất.
5. Mỗi bước trả: công việc làm, thay đổi file/commit, kết quả test, rủi ro mới, DoD nào đạt/chưa đạt và blocker. Không hứa đã deploy khi chỉ có unit test.
6. Không tạo issue hay thêm `ai-handle` nếu chưa được Owner ủy quyền riêng. Không chạm production, không rotate secrets hoặc cấp quyền dựa trên tài liệu này.
7. Trước khi mở PR, đối chiếu lại policy/manifest/DoD đã được Owner ký với commit hiện tại; nếu có xung đột thì báo chứ không tự sửa quyết định.

## 15. Những quyết định đã chốt và điểm cần xác minh kỹ thuật

### Đã chốt (không phỏng vấn lại)

- Tên SISF, 3 zone cùng deployment, Zone2 linh hoạt, GitHub SSOT, input GitHub issue/telemetry, SoW/DoD + QA + auto publish Zone1 + rollback, dashboard/5 surfaces, GitHub/Google SSO, ngân sách unlimited mặc định, triết lý AGENTS/REVIEW/DECISIONS, tự cải tiến chính mình, feature mới/contribution thuộc Owner.

### Các tham số kỹ thuật mặc định để thử nghiệm, **được hiệu chỉnh theo benchmark**

- QA score khởi điểm 85/100 **cộng** hard gates; canary 5→25→100 khi platform hỗ trợ; threshold lỗi ví dụ 5xx>2% và delta>1pp với đủ mẫu. Không áp dụng máy móc cho app không có HTTP request hoặc lưu lượng thấp.
- Local SQLite WAL, multi-worker Postgres; Compose là full-deployment profile đầu tiên; 2 built-in agents đầu tiên Claude CLI + Codex, tiếp Pi. Có thể thay nếu proof chỉ ra adapter khác khả thi hơn mà vẫn đủ hai backend.
- GitHub App OAuth cho repository workflow, OIDC Google cho SSO, role mapping cần rõ. Repo OSS là `bestagentkits/sisf`, giấy phép MIT (đã chốt); mã kế thừa từ TOSE vẫn phải qua audit quyền sở hữu trước khi đưa vào.

## 16. Evidence & nguồn nghiên cứu

- TOSE HHP current design: https://github.com/saasvn/tose-app/blob/899851ae84ead1177b8d12356ce4c262cb8ffac0/docs/harness-platform.md ; issue #574: https://github.com/saasvn/tose-app/issues/574 ; PR #622: https://github.com/saasvn/tose-app/pull/622 .
- GitHub webhooks: https://docs.github.com/en/webhooks/using-webhooks/best-practices-for-using-webhooks
- GitHub App vs OAuth app: https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/differences-between-github-apps-and-oauth-apps
- MCP authorization: https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization
- WebMCP draft (not W3C standard): https://webmachinelearning.github.io/webmcp/
- Claude Routines API trigger, experimental and non-idempotent: https://platform.claude.com/docs/en/api/claude-code/routines-fire
- OpenAI Agents API (managed Codex harness): https://developers.openai.com/api/docs/guides/agents-api/overview
- Cloudflare Sandbox: https://developers.cloudflare.com/agents/tools/sandbox/
- Vercel Sandbox: https://vercel.com/docs/sandbox
- Progressive rollout/metric analysis (reference design): https://argoproj.github.io/argo-rollouts/features/analysis/
- Google OIDC: https://developers.google.com/identity/openid-connect/openid-connect

**Source status:** TOSE repository is source-derived as inspected; external platform capabilities are based on their published docs; thresholds, proposed interfaces, schema, licensing and staged SoW/DoD are **design recommendations**, not implemented facts.
