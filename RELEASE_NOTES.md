# Release Notes

User-facing change summary: [ChangeLog](CHANGELOG.md). Supported plans: **CommandCode GOAT / Pro / Max**, not CommandCode Go.

## v0.2.5 — Request error scope and quota cooldown (2026-11-14)

**中文**

- 修复 `user message must have content`：三种请求协议统一清理空 user 消息和空文本块，保留工具结果、图片及有效内容。
- 修复不支持的 `namespace` 等工具、参数及本地请求转换错误触发账号轮换和误冷却的问题。现在返回 HTTP 400、`request_scoped`、不可重试；未新增 namespace 工具支持。上游鉴权、封禁、额度和限流错误仍按各自作用域处理。
- 按原始配额的整数剩余百分比四舍五入为 0% 判断冷却，优先级月 → 周 → 5-hour。月冷却到有效未来订阅 `currentPeriodEnd`，缺失/无效时不设月度期限；周和 5-hour 从首次观察/失败分别起算 7 天、5 小时。同一轮冷却不因重复查询续期，到期取新快照重新评估。
- 状态仅保存在插件内存，重启后重新查询。健康/失败快照缓存为 60/10 秒；执行前额度检查最多 5 秒，查询失败时不凭空冷却账号。额度查询页面仍返回完整快照，不把耗尽当作取数失败。
- 补齐 Chat 原生 SSE 和 Responses 嵌套额度错误分类；上游响应损坏不归咎客户端，已输出内容后不重试。CPA 自身最低冷却和已保留的更长期限仍可能影响实际恢复时间。
- 本地验证：全量 Go 测试、go vet、空 user/请求作用域/配额冷却回归。5 小时和 7 天到期使用假时钟验证，不是真实长期等待。

**English**

- Filter empty user messages and text blocks across all three request formats while preserving tool results, images and meaningful content.
- Report unsupported tools such as namespace and local request-conversion failures as non-retryable HTTP 400 `request_scoped`, preventing credential rotation and cooldown. Namespace tools remain unsupported; authentication, account bans, quota and rate limits retain their separate scope.
- Use rounded-zero remaining quota with monthly → weekly → five-hour precedence. Monthly cooldown ends at a valid future subscription `currentPeriodEnd`; no monthly deadline is invented without one. Weekly/five-hour cooldowns start at first observation/failure plus seven days/five hours. Repeated reads never extend a round; expiry requires a fresh quota read.
- State is process-local. Healthy/failed reads cache for 60/10 seconds; execution preflight has a five-second budget and retrieval failure does not invent cooldown. Exhausted quota cards remain readable.
- Observe native Chat SSE and nested Responses quota errors. Do not blame clients for malformed upstream responses or retry after downstream output. CPA minimum and retained longer deadlines can affect actual recovery.
- Validation includes Go tests, go vet and regression tests. Long cooldown expiry is tested with an injected clock rather than real multi-hour waits.

## v0.2.4 — Account proxy pool (2026-11-13)

### Proxy pool and account bindings

- Add a persistent proxy directory for HTTP, HTTPS, SOCKS5 and SOCKS5H endpoints. Bulk text import has row-level validation and duplicate preview; proxy IDs are stable random identifiers and are independent of the proxy URL.
- Add manual per-account proxy selection, selected-account batch updates and previewed random assignment for unbound accounts. Random mappings are fixed before save and preserved on retry; proxies may be reused.
- Save the account's `commandcode_proxy_id` and native `proxy_url` together through CPA's narrow auth-fields patch. The auth file remains the binding source of truth; the pool stores the proxy directory only.
- Protect directory updates with ETag/If-Match. Referenced proxies cannot be deleted or have their URL edited. Per-account outcomes are read back and reported; multi-auth updates are not a transaction.
- The account page masks proxy userinfo, reports external/orphaned/mismatched settings, and warns about global-proxy conflicts and plaintext server-side storage.

### Verified egress boundary

This implementation uses CPA's HostBridge and existing auth proxy support. On CPA 8.0.69, isolated native-host tests verified two distinct HTTP proxy exits and a SOCKS5 proxy exit for model unary/stream calls when no global proxy overrides them. A configured global proxy can take precedence, and the host may fall back if proxy setup fails. This feature does not claim strict proxy isolation or fail-closed behavior. Quota requests, credential verification before account admission, and background catalog refresh still use the host's default transport. Saving a proxy is not a connectivity or egress test.

Authenticated proxy URLs are plaintext in the host's config/auth files. The management page does not persist them in browser storage; visible summaries and errors redact userinfo.

### Validation

- `go test ./...` and `go vet ./...` pass.
- `tests/proxy_pool_page.cjs` passes import/dedup/redaction, ETag conflict, random-unbound-only, partial-save retry, stale-account guard, empty-string unbinding and responsive/dark-theme checks.
- `tests/proxy_pool_host.cjs` passes with CPA 8.0.69 and a locally built Windows amd64 C-shared plugin, covering ETag persistence, auth-field retention, HTTP/SOCKS5 unary and stream egress, default quota egress, global proxy precedence, restart and unbinding.
- The host integration used only temporary config/auth directories and local mock upstream/proxy servers; it used no real credentials or external proxies.

**中文**

### 代理池与账号绑定

- 新增 HTTP、HTTPS、SOCKS5、SOCKS5H 代理目录及逐行批量导入预览，展示有效、重复和错误项。代理 ID 随机生成且稳定，不由代理地址推导。
- 支持账号手动绑定、所选账号批量设置，以及只针对未绑定账号的一键随机分配。先固定并展示映射再保存；失败重试沿用原映射，代理允许复用。
- 通过 CPA 窄字段 PATCH 同次保存 `commandcode_proxy_id` 和原生 `proxy_url`；认证文件是绑定唯一真相，代理池仅保存代理目录。
- 目录使用 ETag/If-Match 防止陈旧覆盖。被引用代理不可删除或修改地址。逐账号回读并报告结果；跨多个账号不具备事务原子性。
- 账户页脱敏代理凭据，标注外部、悬空或不匹配设置，并提示全局代理冲突及服务器端明文存储风险。

### 出口边界与验证

实现只使用 CPA HostBridge 和现有原生账号代理。CPA 8.0.69 隔离真实宿主测试确认：无全局代理覆盖时，两个 HTTP 代理及 SOCKS5 代理都可分别用于模型普通/流式请求。非空全局代理可能优先，代理设置失败时宿主可能回退，因此不宣称严格隔离或 fail-closed。额度查询、新账号入库前核验及后台目录刷新仍使用宿主默认 transport，不跟随账号绑定。保存绑定不等于连通性或出口检测。

带认证代理地址以明文保存在宿主配置和认证文件中；账户页不写入浏览器存储，列表和错误会脱敏 userinfo。

验证：`go test ./...`、`go vet ./...`、`tests/proxy_pool_page.cjs`、CPA 8.0.69 原生 `tests/proxy_pool_host.cjs` 均通过。原生测试只使用临时配置/认证目录和本地 mock 上游、HTTP/SOCKS5 代理，不使用真实凭据或外部代理。

## v0.2.3 — Credential quota cooldown, adapter and auth fixes (2026-11-13)

### Bug fixes

**Credential not cooled when Monthly/Weekly window is exhausted (user-reported)**

When the monthly or weekly usage window was spent but the 5-hour window still showed headroom, CPA kept routing to the credential and considered it healthy. This was a compound failure:

1. CPA's credential-wide cooldown is exclusively 429-gated (`case 429` in `MarkResult`). Quota errors arriving as 402/403 (spend cap, `USAGE_EXCEEDED`) only cooled the reporting model for 30 minutes.
2. The 24-hour `RetryAfter` cap shortened weekly window cooldowns to one day; each retry reset the timer.
3. Only narrow wording was recognised; "weekly usage limit", "monthly credits", and similar phrases were misclassified as transient model-level rate limits.
4. SSE-stream quota errors were unconditionally rewritten to `stream_truncated` (no cooldown).
5. A chunked error body could be truncated, causing the JSON to fail to parse and fall back to status-only classification.

All five are fixed. Every credential-scoped error is now reported as HTTP 429 regardless of upstream status. The `RetryAfter` cap is raised to 32 days. Reset hints accept seconds, milliseconds, numeric strings, RFC3339, and prose ("resets at…" / "Resets in Xd Yh Zm"). In-stream errors carry their original classification. Error bodies are read to completion (3 s / 64 KB cap).

**Crash protection: pump panic kills host process**

A panic inside the stream adapter (`Feed`/`Flush`) or the emit path, in the background pump goroutine, was unrecovered and would terminate the whole CPA host. The goroutine now has its own `recover()`.

**Client cancellation not propagated upstream**

`host_callback_id` was not forwarded to `host.http.do` / `do_stream`, so the host could not parent the upstream call on the client's request context. A disconnected client left the upstream request and the host goroutine alive until the 5-minute request timeout. Now forwarded; works silently with older hosts that do not send it.

**Corrupted auth file blocks all new account additions**

One unreadable commandcode auth file caused the duplicate-key check to abort with 502 for every new account addition and every OAuth delivery confirmation. Unreadable entries are now skipped (with a debug trace of the file name, no key material).

**Credential claim leaked after save failure, blocking re-add forever**

After a failed `AuthSave` (host error, envelope error), the in-memory claim on the key digest was never released. Re-adding the same key returned 409 "此 API key 正在保存或保存结果尚未确认" until process restart. After 64 such events, every new key was rejected. Claims are now released on definitive save failure (`errAuthSaveRejected` sentinel) and expire after 15 minutes in all other cases.

**OAuth transient error permanently fails a verified login**

A single AuthList/AuthGet timeout during the OAuth "ready" poll set the session to `error` and released the claim, forcing the user to restart the browser flow. Transient failures (non-409) now leave the session in "ready" for the next poll.

**Adapter: thinking history dropped on messages/responses routes**

Assistant `thinking` blocks were not replayed as historical context on the `messages` and `responses` routes. `reasoning_content` from OpenAI clients and `reasoning_text` from Responses clients were also dropped. DeepSeek rejects a tool turn that is missing prior thinking. All three are now replayed in the same way as the `chat-completions` route.

**Adapter: missing SSE lifecycle events on responses route**

The `responses` stream converter had no `Flush`, so upstream EOF before `response.completed` left Claude clients with open content blocks. The shared `ResponsesEventEmitter` was also missing `response.in_progress`, `response.content_part.added`, `response.output_text.done`, `response.content_part.done` and `response.output_item.done`.

**Adapter: whitespace reasoning fragments dropped**

`ReasoningText` filtered out `strings.TrimSpace(...) != ""` fragments, so `"\n\n"` paragraph breaks and standalone whitespace deltas were lost. Now only truly empty strings are skipped.

**Adapter: parallel tool-call ordering (responses route)**

Opening a new tool block closed all earlier blocks, but late argument fragments for an earlier index were still emitted as `input_json_delta` into an already-stopped block. Later tool_use blocks are now buffered and emitted whole at finish.

**Config: catalog-url host must match base-url**

The catalog request carries the bearer API key. A `catalog-url` pointing at a different host would leak the key. Configuration loading now rejects cross-host catalog URLs with an explicit error.

### Other changes

- `errclass.Redact` now covers `Authorization: Basic`, JSON keys `api_key`/`apiKey`/`token`/`access_token`/`secret`, bare `user_<alnum>` keys in prose, and query parameters named `secret`/`password`/`auth`.
- `route-overrides` accepts both the bare upstream model ID and the prefixed public ID as keys.
- `emitTimeout` decoupled from `logTimeout` and raised to 60 s (was 5 s, shared with log) to avoid aborting slow-but-healthy clients.
- Credential verification (whoami + subscriptions) moved outside `lifeMu`, unblocking other account operations during a slow upstream call.
- Monthly billing period support: `maxRetryAfterSeconds` covers it; `window "monthly"` added to the host's known set.
- Subscription `currentPeriodEnd` parsing now accepts null/empty (non-expiring plan) and all layouts that the quota path already accepted.
- The `inFlight` counter replaced its `sync.WaitGroup` with a design that is safe to `Add` after a timed-out `Wait`.
- Stale CLAUDE.md references to `dlclose`, the 24 h cap, and the `enc::v1/v2` origin key corrected.

**中文**

**Monthly/Weekly 耗尽时凭证未被冷却（用户反馈）**

月度或每周窗口用完但 5-hour 有余量时，CPA 仍会将请求路由到该凭证。根本原因：宿主 CPA 的凭证级冷却只在收到 429 时触发；402/403 只冷却单个模型；24 小时上限比 weekly 窗口短；部分措辞无法识别；SSE 流中的错误被无条件改写；错误体分块传输时可能被截断。以上五个问题均已修复。

**流式转发 goroutine panic 导致宿主进程崩溃**

适配器在后台 goroutine 中 panic 会终止整个 CPA 进程。现已添加 `recover()`。

**客户端取消未传播到上游**

未转发 `host_callback_id`，导致客户端断开后上游请求仍在运行，直到 5 分钟超时。现已修复。

**损坏的认证文件阻塞所有新增操作**

单个损坏的认证文件会让 add/OAuth confirm 均返回 502。现在会跳过无法读取的文件。

**凭证占位记录泄漏，导致重新添加永久返回 409**

保存失败后占位记录不会释放。现在在确认失败时立即释放，其余情况 15 分钟后过期。

**OAuth 瞬时错误使已验证的登录永久失败**

轮询期间单次 AuthList 超时会将会话设为 error 状态。现在瞬时错误保持 ready 状态等待重试。

**messages/responses 路由丢失 thinking 历史**

thinking 块、`reasoning_content`、`reasoning_text` 未被回放，导致 DeepSeek 拒绝带工具历史的请求。已修复。

**配置校验：catalog-url 主机不得与 base-url 不同**

bearer key 会发到 catalog-url，跨主机会泄漏密钥，现在会报错拒绝。



- Fix a v0.2.1 regression: when any usage window was exhausted the whole card showed "failed to fetch quota: … usage window exhausted", hiding every window value.
- Exhaustion is no longer treated as a fetch failure. The card shows all three windows (already at 100%) and adds one row naming the exhausted limit (monthly / weekly / 5-hour).
- Only genuine retrieval failures — network, auth, malformed response — surface as errors.
- Behaviour is otherwise identical to v0.2.1.
- Supported plans remain **CommandCode GOAT / Pro / Max; CommandCode Go is not supported**.

**中文**

- 修复 v0.2.1 的回归问题：任一用量窗口耗尽时，整张额度卡片显示「额度加载失败」，看不到任何窗口数据。
- 耗尽不再当作查询失败。卡片照常显示三个窗口（进度条已是 100%），并多出一行说明是哪个限额已用完（月度 / 每周 / 5 小时）。
- 只有真正的取数失败（网络、鉴权、响应格式）才会报错。
- 其余行为与 v0.2.1 相同。
- 仍然只支持 **CommandCode GOAT / Pro / Max，不支持 Go 套餐**。

## v0.2.0 — Renamed to `commandcode-or-or-cliproxyapi` (2026-11-12)

- The plugin is now **`commandcode-or-or-cliproxyapi`** (formerly `commandcode-go-cliproxyapi`). The new name drops the misleading `go`, which never meant CommandCode Go plan support.
- **Breaking change** — the plugin ID is part of the host contract:
  - move the `plugins.configs.commandcode-go-cliproxyapi` entry to the new ID; the old key will not apply;
  - replace the library with `commandcode-or-or-cliproxyapi.so` / `.dylib` / `.dll` and delete the old file;
  - management routes are now `/v0/resource/plugins/commandcode-or-or-cliproxyapi/*` and `/v0/management/plugins/commandcode-or-or-cliproxyapi/*`.
- Existing auth files keep working: the provider key is still `commandcode`, so accounts, models and quota are unaffected. Only the plugin-side ID changed.
- Behaviour is otherwise identical to v0.1.5.
- Supported plans remain **CommandCode GOAT / Pro / Max; CommandCode Go is not supported**.
- Upgrade: replace the library, update the `plugins.configs` key, restart CPA and refresh Management Center.

**中文**

- 插件改名为 **`commandcode-or-or-cliproxyapi`**（原 `commandcode-go-cliproxyapi`）。新名字去掉了容易误解的 `go`，它从来不代表支持 Go 套餐。
- **不兼容变更** —— 插件 ID 是宿主接口的一部分：
  - 配置里的 `plugins.configs.commandcode-go-cliproxyapi` 需要改成新 ID，旧键不会生效；
  - 插件库改名为 `commandcode-or-or-cliproxyapi.so` / `.dylib` / `.dll`，并删除旧文件；
  - 管理接口路由变为 `/v0/resource/plugins/commandcode-or-or-cliproxyapi/*` 与 `/v0/management/plugins/commandcode-or-or-cliproxyapi/*`。
- 已有认证文件继续可用：provider 键仍是 `commandcode`，账户、模型和额度都不受影响，改的只是插件侧 ID。
- 其余行为与 v0.1.5 完全相同。
- 仍然只支持 **CommandCode GOAT / Pro / Max，不支持 Go 套餐**。
- 升级时替换插件库、修改 `plugins.configs` 的键、重启 CPA 并刷新管理后台。

## v0.1.5 — Quota on the standard Management Center cards (2026-11-12)

- CommandCode usage now appears on the host's auth-file cards. The plugin registers CPA's `QuotaProvider` capability, so Management Center marks each CommandCode credential as quota-capable and fetches its usage through `POST /v0/management/plugins/commandcode-or-or-cliproxyapi/quota`.
- Each card shows the plan plus the 5-hour, weekly, and monthly windows: remaining ratio, used/cap, and reset time. The monthly window is omitted when the plan is unknown, because CommandCode reports only remaining monthly credits.
- Reset is not advertised — CommandCode has no quota reset endpoint.
- **Cooldowns now follow CPA's configuration.** The plugin reports CommandCode's real error shape and CPA decides the cooling from its own settings: a usage-window or credit exhaustion cools the whole credential for the upstream reset time, a plain 429 cools only the reporting model, a plan-entitlement failure cools only that model, client-caused request defects cool nothing, post-first-byte truncation and client disconnects cool nothing and are never replayed, and transport failures stay retryable without cooling. An upstream `Retry-After` header is forwarded as the duration.
- Minimum CLIProxyAPI rises to `v8.0.43`, the first release with both the quota provider and the cooldown error fields; the SDK dependency is now `v8.0.67`. The plugin declares schema 6 — the contract it actually uses — so hosts are not rejected for newer SDK features it never calls.
- **DeepSeek-documented client capabilities are complete on all three protocols.** Historical thinking is replayed as `reasoning_content` (required by tool-carrying multi-turn requests, which the upstream otherwise rejects), Anthropic `output_config.effort` is honoured, image `detail` survives translation, and Responses reasoning events are emitted under both the OpenAI-standard `response.reasoning_summary_text.*` and DeepSeek-documented `response.reasoning_text.*` families. A decoder bug that emptied every replayed Claude thinking block is fixed.
- The plugin's own **CommandCode accounts and quota** page is unchanged and still provides key entry and per-card refresh.
- Supported plans remain **CommandCode GOAT / Pro / Max; CommandCode Go is not supported**. Existing auth files and configuration are unchanged.
- Upgrade: replace the platform-specific plugin library, restart CPA and refresh Management Center. Publishing a release does not update an already running plugin.

**中文**

- CommandCode 额度现在会显示在宿主的认证文件卡片上。插件注册 CPA 的 `QuotaProvider` 能力后，管理后台会把每个 CommandCode 认证文件标记为支持额度，并通过 `POST /v0/management/plugins/commandcode-or-or-cliproxyapi/quota` 获取用量。
- 每张卡片显示套餐，以及 5 小时、每周、每月三个窗口的剩余比例、已用/上限和重置时间。套餐未知时不显示月度窗口，因为 CommandCode 只上报剩余月度额度。
- 不声明支持重置：CommandCode 没有重置额度的接口。
- **冷却完全跟随 CPA 配置。** 插件按 CommandCode 真实的错误形态上报，由 CPA 自己的冷却设置决定行为：用量窗口或 credit 耗尽冷却整条凭据并使用上游给的 reset 时长；普通 429 只冷却当前模型；模型不在套餐内只冷却该模型；客户端引起的请求缺陷不冷却；首字节后流中断和客户端断连不冷却、不重放；传输失败可重试但不冷却。上游 `Retry-After` 头会作为冷却时长透传。
- 最低 CLIProxyAPI 版本提高到 `v8.0.43`（首个同时具备配额提供者和冷却错误字段的版本），SDK 依赖升级到 `v8.0.67`。插件只声明 schema 6（自身实际用到的契约），不会因为未使用的新 SDK 特性而被更老的宿主误拒。
- **DeepSeek 文档定义的三种客户端协议能力已补齐。** 历史思考内容回传为 `reasoning_content`（带 tools 的多轮请求必需，否则上游拒绝）、Anthropic `output_config.effort` 生效、图片 `detail` 保留、Responses 思考事件同时以 OpenAI 标准的 `response.reasoning_summary_text.*` 和 DeepSeek 文档的 `response.reasoning_text.*` 两族发出。同时修复了导致历史思考块文本为空解码器缺陷。
- 插件自己的 **CommandCode账户与额度** 页面保持不变，仍然提供 key 录入和按卡片刷新。
- 仍然只支持 **CommandCode GOAT / Pro / Max，不支持 Go 套餐**；不改变已有认证文件和配置。
- 升级时替换对应平台的插件库、重启 CPA 并刷新管理后台；发布不会自动更新正在运行的插件。

## v0.1.4 — Management Center remembered-auth compatibility (2026-11-04)

- Fix the quota and OAuth pages showing “Enable Remember password” when Management Center already had Remember password enabled. The plugin now decodes the current `enc::v2::` `cli-proxy-auth` storage format as well as legacy `enc::v1::` values.
- For `enc::v2::`, derive the storage key from the current Management Center origin without adding the browser User-Agent; retain the legacy v1 derivation for existing sessions.
- Supported plans remain **CommandCode GOAT / Pro / Max; CommandCode Go is not supported**. Existing auth files and configuration remain unchanged.
- Upgrade: replace the platform-specific plugin library, restart CPA and refresh Management Center. Publishing a release does not update an already running plugin.

**中文**

- 修复管理后台已经勾选“记住密码”时，配额页和 OAuth 页仍显示“Enable Remember password”的问题。插件现在同时支持当前的 `enc::v2::` `cli-proxy-auth` 存储格式和旧的 `enc::v1::` 格式。
- `enc::v2::` 按当前管理后台地址推导存储密钥，不再错误加入浏览器 User-Agent；旧 v1 会继续按原规则解码。
- 仍然只支持 **CommandCode GOAT / Pro / Max，不支持 Go 套餐**；不改变已有认证文件和配置。
- 升级时替换对应平台的插件库、重启 CPA 并刷新管理后台；发布不会自动更新正在运行的插件。

## v0.1.3 — Complete streamed Responses tool calls (2026-10-20)

- Fix tools remaining unfinished in Responses clients after the stream ends when converting Chat Completions or Messages upstream responses.
- Emit `response.function_call_arguments.done`, then `response.output_item.done`, before `response.completed`. Keep each tool's announced output index, stable `fc_` item ID and original `call_id` throughout the stream and final output.
- Supported plans remain **CommandCode GOAT / Pro / Max; CommandCode Go is not supported**. Existing auth files and configuration remain unchanged.
- Upgrade: replace the platform-specific plugin library, restart CPA and refresh Management Center. Publishing a release does not update an already running plugin.

**中文**

- 修复将 Chat Completions 或 Messages 上游转换为 Responses 流时，回复已结束但客户端仍认为工具未完成、不执行工具的问题。
- 在 `response.completed` 前依次发送参数结束和工具项结束事件，保持原来的输出序号，在整个流与最终输出中复用稳定的 `fc_` 工具项编号，并保留原始 `call_id`。
- 仍然只支持 **CommandCode GOAT / Pro / Max，不支持 Go 套餐**；不改变已有认证文件和配置。
- 升级时替换对应平台的插件库、重启 CPA 并刷新管理后台；发布不会自动更新正在运行的插件。

## v0.1.2 — Easier account setup and browser authorization (2026-10-20)

- **Supported plans and naming**: support CommandCode GOAT / Pro / Max, not Go. Go uses the official CLI's private, non-OpenAI-compatible interface/protocol and has no Provider API access; CPA's current CommandCode integration does not implement it. The `go` in `commandcode-go-cliproxyapi` is inherited from `opencode-go-cliproxyapi`, not a plan-support claim.
- **Easier manual setup**: enter an existing API key on the independent **CommandCode accounts and quota** page; the plugin validates it and saves an auth file. No hand-written auth JSON or manual key entry in plugin configuration is needed. Old `api-keys` configuration remains retired and is not imported.
- **CommandCode OAuth login**: authorize through the official website without manually pre-creating an API key. The official flow generates a credential; copy it from the fallback page into CPA to finish. This is not an automatic callback or token-renewal implementation.
- **Expected fallback transition**: credentials are returned by HTTP POST, not in the callback HTTP GET URL. The Chrome-blocked localhost:6000 callback intentionally triggers the official failed-delivery fallback. A brief callback-URL/credential-delivery error may precede the credential-copy page; this is expected. No fallback, inability to copy, or failed CPA validation/save remains a real error.
- **Equivalent, coexisting accounts**: both entry points verify identity and an active, unexpired subscription, then create the same kind of auth file with the same model/quota capabilities; only the source label differs. Identical keys are deduplicated; different keys may coexist without multiplying subscription quota. Existing auth files and host uploads are unchanged.
- **Retained Chat Completions translation**: Responses was unavailable when the plugin was created; the official API now supports it for most OpenAI/open models. At the 2026-10-20 release-preparation snapshot, GLM-5.3 Flash, Tencent Hy4 Preview and DeepSeek V4 Flash Fast still declare only Chat Completions. Keep the default translation path, rather than claiming native Responses works for every model or implementing automatic endpoint switching.
- **Login compatibility**: preserve sessions when older CPA versions resend identical configuration; decode HTML-escaped official URLs before validating their parameters. Browser regressions verify the final visible link, including state and fallback callback.
- **Save and security boundaries**: OAuth completion only stages credentials; CPA polling saves. Cross-entry digest claims avoid duplicate delivery and retain uncertain saves without automatic rewriting. The guide requires a remembered same-origin management login. Only the Chrome fallback has been verified. Post-delivery cancellation, concurrent host file edits, CPA redirects and the earlier email-sync race retain their documented limitations.
- **Quota location**: quota remains on the plugin page; the standard `QuotaProvider` for CPA auth-file cards is not implemented.
- Target release tag: `v0.1.2`; platform assets are available only after a successful GitHub Release. See [ChangeLog](CHANGELOG.md) and the README for user steps, compatibility evidence and upgrade instructions.

**中文**

- **支持范围与名称**：支持 CommandCode GOAT / Pro / Max，**不支持 Go 套餐**。Go 使用官方 CLI 专用的私有接口与协议，没有 Provider API 权限，CPA 当前的 CommandCode 接入没有实现这套协议。`commandcode-go-cliproxyapi` 中的 `go` 仅继承自 `opencode-go-cliproxyapi`。
- **优化易用性**：在插件添加的独立 **CommandCode账户与额度** 页面直接输入已有 API key，联网核验后自动保存为认证文件；不再需要手工向配置文件添加 API key，也不用手写认证 JSON。旧 `api-keys` 配置停用且不导入。
- **增加 CommandCode OAuth 登录**：无需预先到官网手动创建 API key；在官网授权后，从官方备用页复制授权流程生成的凭据，粘回 CPA 完成。不承诺自动回调或自动续期。
- **预期的错误页过渡**：官网通过 HTTP POST 返回凭据，不把认证 Token / API key 放进 Callback HTTP GET URL。Chrome 默认拦截 localhost:6000 回调后，官网会进入失败备用流程；授权后可能短暂显示回调网址或凭据传递错误，再跳到凭据复制备用页，这是当前设计。没有跳转、无法复制或 CPA 最终核验/保存失败仍需处理，不能忽略。
- **两种添加方式并行共存、能力相同**：都核验身份及有效订阅，生成同类认证文件，模型调用和额度能力一致，仅来源标记不同。相同 key 去重，不同 key 可共存，但不会增加同一订阅的总额度。不迁移或删除旧认证，不改变宿主上传入口。
- **保留 Chat Completions 转换**：插件创建时官方尚无 Responses 接口，现在多数 OpenAI 与开放模型已支持；截至 2026-10-20 本次发布准备，GLM-5.3 Flash、Tencent Hy4 Preview、DeepSeek V4 Flash Fast 等仍只声明 Chat Completions，因此保留默认转换路径，不宣称全模型原生 Responses 或自动切换接口。
- **修复登录兼容性**：旧版 CPA 重发相同配置不再清除会话；引导页先还原官方链接的 HTML 转义，再校验参数。浏览器回归检查最终显示链接的 state 和备用回调地址。
- **保存与安全边界**：OAuth 完成接口只暂存，由 CPA 轮询负责保存；两个入口共用 key 摘要占用，结果不明不自动重写。引导页要求同源管理登录并记住密码，仅验证过 Chrome 备用流程。交付后取消、宿主并发文件操作、HTTP 跳转和既有邮箱回写的限制仍未消除。
- **额度位置**：额度仍在插件页查看，未接入 CPA 认证文件卡片的标准 `QuotaProvider`。
- 目标发布标签为 `v0.1.2`；平台安装包以 GitHub Release 发布成功后的附件为准。操作步骤、兼容证据和升级方法见 [ChangeLog](CHANGELOG.md) 与 README。

### Account management and upgrade notes

Upgrade notice: auth files are now the only API key source. The retired plugin `api-keys` block is not imported; preserve the auth files you want to keep.

- The Management Center menu is **CommandCode**. **CommandCode accounts and quota** (CommandCode账户与额度) can paste an API key and save it as a host auth file. Hand-written JSON is unnecessary; uploading an auth file still works.
- Disable and delete stay on the host auth-file page.
- A leftover `api-keys` block in plugin config is not a key source. It is not parsed, expanded, stored, imported, or used for routing, catalog discovery, or quota, and it cannot bring back an account you deleted. The accounts page warns when the block is still present. An existing install may keep it; remove it yourself after the auth files are the accounts you want.
- The plugin can start with no accounts. Catalog discovery runs only when at least one account is enabled.
- Plugin config with no keys is valid.
- Known limitation: email sync is retained for both pages. Its final file check and replacement are not atomic with host deletion/disabling/metadata edits; concurrent changes may be overwritten or a deleted file recreated. This risk is accepted, not fixed. See the quota-page section in the README.

**中文**

- 管理后台菜单为 **CommandCode**。「CommandCode账户与额度」可以粘贴 API key 并保存为宿主认证文件。不必上传自制 JSON，仍可自行上传认证文件。
- 禁用和删除仍在宿主认证文件页。
- 插件配置里的旧 `api-keys` 不再是密钥来源：不解析、不展开、不保存、不导入，也不参与路由、目录发现或额度，不能复活已删除的账户。账户页会提示这段仍在。已有安装可以留着它，确认认证文件无误后自行移除。
- 没有账户也可以启动。目录发现需要至少一个启用的账户。
- 不含密钥的插件配置可以加载。
- 已知限制：保留两处显示邮箱的同步行为。最后的文件检查与替换不能和宿主删除、禁用、备注修改保持原子性；同时操作可能丢失修改或重新创建被删文件。这项风险已接受，未修复。详见 README 配额页面说明。

## v0.1.1 — Accept array function_call_output results

Responses `function_call_output.output` may be a string or an array of text
parts (`input_text`, `output_text`, or `text`). The array shape is flattened
into the string Chat Completions and Messages already send upstream. Non-text
parts are still rejected.

**Upgrade notes**

- Replace the plugin binary and restart CLIProxyAPI.

## v0.1.0 — CommandCode GOAT / Pro / Max provider

First release of the plugin as a CommandCode GOAT / Pro / Max provider, derived from
opencode-go-cliproxyapi v0.1.7.

**Adaptations**

- **Reasoning preservation**: upstream `reasoning`, `reasoning_details[].text`
  and `reasoning_content` are read together (mirrored values counted once) and
  emitted as a leading Claude `thinking` block, a leading Responses `reasoning`
  item with summary events, or a backfilled `reasoning_content` on the OpenAI
  passthrough — streaming and non-streaming.
- **Chat-completions-only routing at creation**: every catalog model defaulted to
  `{base-url}/chat/completions`. At that time, CommandCode's `/messages` endpoint
  accepted Claude models only and `/responses` was not yet available. The family
  prefix table was removed; `route-overrides` remained. The official Responses
  endpoint is now available for most OpenAI/open models; see the current README
  for the remaining exceptions and why Chat Completions translation is retained.
- **Effort passthrough**: models without thinking metadata (all CommandCode
  models) are no longer validated or clamped locally — `xhigh`/`max` reach the
  upstream verbatim, `auto`/`none` omit the field. A model that declares levels
  or bounds opts back into eager validation.
- **Quota page against the real account API**: cards show account email, plan,
  remaining plan credits, and the rolling 5-hour/weekly windows from
  `/alpha/billing/credits`, `/alpha/billing/subscriptions` and `/alpha/whoami`.
- Defaults renamed (`commandcode` prefix, `api.commandcode.ai/provider/v1`
  base URL, `commandcode-go-cliproxyapi` plugin id).

**Upgrade notes**

- The provider namespace changed, so client model ids must be updated
  (for example `commandcode/deepseek/deepseek-v4.1-flash`).
- Replace the plugin binary, restart CLIProxyAPI, and hard-refresh the
  Management Center.
