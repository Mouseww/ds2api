# DS2API 账号封禁检测与负载池 — 需求规格

> 状态：需求规格（requirements）
> 范围：账号封禁检测、自动禁用、负载池目标并发账号数（`active_pool_size`）与备用账号补足、Admin API 与 WebUI 暴露面。
> 关联代码：`internal/config`、`internal/account`、`internal/auth`、`internal/deepseek/client`、`internal/completionruntime`、`internal/httpapi/admin`、`webui/src/features/account`。

---

## 1. 目标与非目标

### 1.1 目标
1. 当请求遭拒（captcha / 429 / 鉴权失败）时，触发对当前账号的**封禁状态复查**，判断该账号是否已被 DeepSeek 封禁（muted）。
2. 被判定为封禁的账号被**自动禁用**：立即从负载池移出、不再分配请求，但配置（email/mobile/password 等）与 token 仍保留。
3. 通过负载设置字段 `active_pool_size`（同时启用账号数）控制负载池规模；当活跃账号数因封禁等原因低于目标值时，**自动启用备用账号补足池子**。
4. Admin API 与 WebUI 暴露：账号启用/禁用开关、封禁状态与解封时间展示、负载池目标并发账号数设置。

### 1.2 非目标（本期不实现）
- 自动「解封后恢复」：账号封禁状态由后续成功登录自动刷新（`BanIsMuted` 归 0），但**重新启用由运营手动执行**（或通过下一次封禁复查覆盖）；本期不做「mute_until 到期后自动重新启用」。
- 主动/周期性地扫描所有账号的封禁状态（无请求也轮询登录）。封禁复查只在指定触发点上发生。
- captcha 自动答题、验证码绕过。

---

## 2. 现有基础（实现时直接复用）

以下能力当前已存在，本需求在其上叠加，不得重复实现：

| 现有能力 | 位置 | 说明 |
| --- | --- | --- |
| 封禁字段已建模 | `internal/config/config.go` `Account` | `BanIsMuted int`（`user.chat.is_muted`）、`BanMuteUntil float64`（`user.chat.mute_until`，unix 秒时间戳）、`BanStatus int`（`user.status`） |
| 登录时抽取封禁字段 | `internal/deepseek/client/client_auth.go` `extractBanFields` / `updateAccountBanStatus` | 登录成功后已把 `is_muted`/`mute_until`/`status` 写入 store |
| 封禁状态持久化 | `internal/config/store.go` `UpdateAccountBanStatus` | 已按 identifier 定位并 `saveLocked` |
| captcha 检测 | `internal/deepseek/client/captcha.go` `DetectCaptchaChallenge` | 递归扫描响应，返回挑战对象 |
| captcha→429 映射 | `internal/completionruntime/nonstream.go` `collectAttempt`、`internal/completionruntime/stream_retry.go` | 已把 captcha 失败映射为 `429 / captcha_required` 并触发换账号 |
| 鉴权失败→重登 | `internal/auth/request.go` `RefreshToken`；`internal/deepseek/client/client_auth.go` `isTokenInvalid`/`classifyResponseFailure` | 401/403、`40001/40002/40003`、token/expired/not-login 等被分类为类型化失败，由共享失败策略（`internal/completionruntime/failure_policy.go`）触发重新登录/切号 |
| 负载池 | `internal/account/pool_core.go` / `pool_acquire.go` / `pool_limits.go` | `Reset()` 把所有账号装入 queue，`Acquire*` 按 queue 分配，`Status()` 暴露运行状态 |
| Admin 账号 CRUD | `internal/httpapi/admin/accounts/*` | `GET/POST/PUT/DELETE /admin/accounts`、`GET /admin/queue/status` |
| 运行设置读写 | `internal/httpapi/admin/settings/*` | `GET/PUT /admin/settings`，`runtime` 段包含 `account_max_inflight` 等 |
| WebUI 账号页 | `webui/src/features/account/*` | `AccountsTable`、`useAccountsData`、`useAccountActions`、`QueueCards` |

---

## 3. 需求详述

### R1 — 封禁判定规则

1. **判定依据**：以登录响应中的 `user.chat.is_muted == 1` 作为「账号被封禁」的判定条件。
2. **记录字段**：每次登录（含 token 刷新、封禁复查触发的重新登录）成功时，抽取并持久化以下字段（复用现有 `extractBanFields` 语义）：
   - `ban_is_muted`：`user.chat.is_muted`（int，`1` 表示 muted/封禁）。
   - `ban_mute_until`：`user.chat.mute_until`（unix 秒时间戳，float64；`0`/缺省表示无解封时间）。
   - `ban_status`：`user.status`（int，账号状态码）。
3. **判定输出**：
   - `ban_is_muted == 1` → 账号处于封禁状态，进入「自动禁用」流程（R2/R3）。
   - `ban_is_muted != 1`（含 `0` 或字段缺省）→ 不视为封禁，账号保留/恢复为「未封禁」。
4. **幂等**：对同一账号重复登录得到的封禁字段直接覆盖旧值，不产生累积副作用。

### R2 — 请求遭拒触发点与复查流程

#### 2.1 触发点（三者之一即触发复查）
1. **captcha / 数美风控挑战**：`DetectCaptchaChallenge(resp) != nil`。当前已映射为 `429/captcha_required`，此处复用该检测结果作为触发信号。
2. **HTTP 429**：上游返回 `429 Too Many Requests`（或已被映射为 429 的 rate-limit/风控失败）。
3. **鉴权失败**：满足现有 `isTokenInvalid(...)` 的判定（HTTP 401/403；`code/biz_code` ∈ `{40001,40002,40003}`；或消息含 `token`/`unauthorized`/`expired`/`not login`/`login required`/`invalid jwt` 等）。

> 注：触发点判断逻辑已收敛为一处共享实现：`internal/completionruntime/failure_policy.go`（`FailurePolicy.Handle` / `HandleUpstreamFailure`）。DeepSeek client RPC（`CreateSession` / `GetPow` / `UploadFile` / 会话操作）不再在内部做 RecheckBan / RefreshToken / SwitchAccount，而是把 captcha / 429 / 鉴权失败返回为类型化 `RequestFailure`，由该共享策略统一决定「封禁复查（带冷却）→ 刷新 token（同账号重试）→ 切换账号」，且每个请求各动作至多一次；`auth.Resolver.SwitchAccount` 另有一次性守卫，保证一个请求至多成功切换一次账号（避免 client 层与 runtime 层重复切号烧掉两个账号）。completionruntime 的非流式/流式重试循环与各协议适配器均调用该策略，不再各自实现（遵循仓库 `AGENTS.md` 的协议适配器边界原则）。

#### 2.2 复查流程（按账号执行，带冷却）
对触发点命中的账号 `A`：

1. **冷却检查**：每个账号维护「最近一次封禁复查时间」`lastBanRecheckAt`（进程内，非持久化）。若距上次复查不足 `ban_recheck_cooldown_seconds`（默认 **60** 秒），跳过本次复查，沿用现有错误处理路径（换账号/重试/返回错误）。
2. **重新登录拉取封禁状态**：调用 `client.Login(ctx, A)`（复用现有 `LoginFunc`）。登录成功即会通过 `updateAccountBanStatus` 刷新 `ban_is_muted/ban_mute_until/ban_status`。
3. **判定**：
   - 若登录成功且 `ban_is_muted == 1` → **自动禁用**账号 `A`（见 R3），并**立即补足池子**（见 R5），本次请求继续走现有「切换账号」路径。
   - 若登录成功但 `ban_is_muted != 1` → 账号未被封禁，仅更新封禁字段；按现有逻辑继续（token 已刷新，可继续重试当前账号）。
   - 若登录失败（网络错误、密码错误等非封禁原因）→ **不**判定为封禁、**不**禁用；按现有 token 刷新失败逻辑处理（换账号/返回鉴权错误）。
4. **记录时间戳**：无论登录成功与否，复查一旦真正执行（未被冷却跳过），更新 `lastBanRecheckAt[A] = now`。
5. **避免登录风暴**：冷却与「仅在触发点上复查」共同保证：正常成功请求不会触发登录复查。

### R3 — 账号禁用语义

1. **新增账号字段** `enabled`（bool，配置持久化字段，JSON key `enabled`）：
   - 语义：`enabled == true` 表示账号可被负载池分配；`enabled == false` 表示账号被禁用，**不被负载池分配请求**。
   - 缺省：字段缺失或 `null` 时按 `true` 处理（向后兼容，旧账号默认启用）。
2. **新增账号字段** `disabled_reason`（string，可选，JSON key `disabled_reason`）：
   - 记录禁用原因，取值：空串（启用/无）、`"banned"`（因封禁自动禁用）、`"manual"`（运营手动禁用）。
   - 仅用于 WebUI 展示与审计，不参与分配判定；`enabled == false` 即硬性排除。
3. **自动禁用**（封禁触发）：
   - 设置 `enabled = false`、`disabled_reason = "banned"`，同时保留并刷新封禁字段。
   - 持久化通过 `store.Update(...)` 完成。
   - **立即生效**：负载池把该账号从可用队列/活跃集合中移除（见 R5），无需重启进程。
4. **配置与 token 保留**：禁用只翻转 `enabled` 与 `disabled_reason`；**不清空、不删除** `email/mobile/password/token/proxy_id` 等字段，也不调用 `ClearAccountTokens`。
5. **手动禁用/启用**：运营通过 Admin API/WebUI 切换 `enabled`（R6/R7）。手动启用时清空 `disabled_reason`。
6. **封禁账号重新启用约定**：被封禁账号如需重新启用，运营在确认账号恢复后手动将 `enabled` 置为 `true`；系统在下一次登录复查中若再次发现 `is_muted==1`，会再次自动禁用（闭环）。

### R4 — 负载设置字段 `active_pool_size`

1. **字段**：`RuntimeConfig.ActivePoolSize int`，JSON key `active_pool_size`（位于 `runtime` 配置段，与现有 `account_max_inflight` 等同级）。
2. **含义**：负载池**同时启用（活跃分配）的账号数量**。池子按确定性顺序从中选取前 N 个「启用且未封禁」账号作为活跃池成员。
3. **默认值**：`0`。语义为**不限制**——所有「启用且未封禁」的账号都进入活跃池（与现有行为一致，向后兼容）。
4. **合法范围**：`0 <= active_pool_size`（非负整数）。`0` = 不限制；`>= 1` = 活跃池目标/上限账号数。
5. **校验规则**：
   - 配置加载（`ValidateRuntimeConfig`）与 Admin 写入（`parseSettingsUpdateRequest` + `validateMergedRuntimeSettings`）两处校验：
     - 负值 → 校验失败并返回错误（不允许静默修正为 0）。
     - 非整数（浮点/非数字字符串）→ 解析为 0 时按「未提供」处理；解析为负则报错。
   - 上界：`active_pool_size` 不设硬上限；若大于可用账号数，池子实际大小为可用账号数（不报错），差额通过状态接口暴露（见 R6）。
   - 与 `account_max_inflight` / `global_max_inflight` 无强制数学约束（三者正交：`active_pool_size` 管「账号数量」，`*_inflight` 管「每账号/全局并发」）。
6. **热更新**：修改 `active_pool_size` 后调用池子的重新平衡（rebalance，见 R5），即时生效，无需重启。

### R5 — 补足池子规则

#### 5.1 术语定义
- **启用账号**：`enabled == true`（缺省为 true）。
- **封禁账号**：`enabled == false && disabled_reason == "banned"`（或 `ban_is_muted == 1`）。
- **禁用账号**：`enabled == false`（无论原因），不被分配。
- **候选账号（备用池来源）**：`enabled == true && ban_is_muted != 1`。
- **活跃池（active pool）**：候选账号中，按确定性顺序取前 `N` 个；其中 `N = active_pool_size`（当 `active_pool_size == 0` 时 `N = 全部候选账号`）。
- **备用账号（standby）**：候选账号中、未被选入活跃池的账号（即 `active_pool_size > 0` 时排序在第 N 个之后的账号）。

#### 5.2 确定性顺序
候选账号按**「有令牌优先（token-first）」**规则排序：拥有有效 token（`acc.Token != ""`）的账号排在前面，无 token 的排在后面；同状态内按配置 `Accounts` 切片顺序（插入顺序）保持稳定。封禁/禁用账号在排序前被剔除。该顺序用于确定「谁进活跃池、谁是备用」，保证可预期、可复现。token-first 策略确保已登录的账号优先被选入活跃池，降低因 token 缺失导致的自动登录频率。

#### 5.3 重新平衡（rebalance）
以下任一事件发生后，池子执行一次 `rebalance`：
1. 账号封禁导致自动禁用（R2/R3）；
2. 账号新增 / 删除（Admin CRUD）；
3. 账号手动启用 / 禁用（R6/R7）；
4. `active_pool_size` 修改；
5. 进程启动 / `Pool.Reset()`。

`rebalance` 的伪代码语义：
```
candidates = accounts where enabled==true and ban_is_muted!=1,
            sorted token-first (with-token first, then by config order)
N = (active_pool_size > 0) ? active_pool_size : len(candidates)
activeSet  = candidates[0:N]          // 活跃池
standbySet = candidates[N:]           // 备用
pool.queue = activeSet               // 分配仅从 activeSet 进行
```

#### 5.4 补足语义（「自动启用备用账号补足池子」）
- 当活跃池成员因封禁被自动禁用后，`candidates` 减少，`rebalance` 自动把原本的备用账号**提升进活跃池**（即「自动启用备用账号」），使活跃池大小回到 `min(active_pool_size, 候选账号数)`。
- 若候选账号不足（没有可用备用账号），池子实际大小 < 目标值；差额通过状态接口暴露，不报错、不影响已入池账号的分配。
- 账号从备用池提升到活跃池**不改变**其 `enabled` 配置值（它本就是 `enabled==true`）；提升是池子内部集合的重算。运营视角的「启用/禁用」开关只控制 `enabled` 字段，二者解耦但语义清晰。

#### 5.5 并发与即时生效
- `rebalance` 必须在池子锁内执行，并与 `Acquire*` 互斥；正在 in-flight 的请求不受影响（`inUse` 计数保留），仅影响后续新分配。
- 被自动禁用的账号若有 in-flight 请求，允许其完成当前请求后自然释放（`Release` 已是空操作安全），不再接受新分配。

---

## 4. 数据模型变更

### 4.1 `config.Account`（`internal/config/config.go`）
```go
type Account struct {
    // ...现有字段不变...
    // 新增：
    Enabled        *bool  `json:"enabled,omitempty"`        // 缺省 true
    DisabledReason string `json:"disabled_reason,omitempty"` // "" | "banned" | "manual"
    // 现有封禁字段保持不变：
    // BanIsMuted / BanMuteUntil / BanStatus
}
```
- 用 `*bool` 以便区分「未设置」（按 true）与「显式 false」。提供 helper：`Account.IsEnabled() bool`、`Account.IsBanned() bool`。

### 4.2 `config.RuntimeConfig`
```go
type RuntimeConfig struct {
    // ...现有字段不变...
    ActivePoolSize int `json:"active_pool_size,omitempty"` // 0=不限制
}
```
- 新增 store accessor：`RuntimeActivePoolSize() int`（缺省返回 0）。

### 4.3 运行期内存状态
- `account.Pool` 增加 `activePoolSize int`（镜像 `RuntimeActivePoolSize()`）。
- `account.Pool` 增加方法：`Rebalance()`（按 5.3 重算活跃/备用集合）与 `RemoveAccount(identifier string)`（封禁时即时剔除，等价于触发 rebalance）。
- `auth.Resolver` 增加进程内 map：`lastBanRecheckAt map[string]time.Time` 与冷却读取方法（对齐现有 `tokenRefreshedAt` 的实现方式）。

---

## 5. Admin API 暴露面

### 5.1 账号列表 `GET /admin/accounts`
现有返回的每项 `item` **新增**以下字段（`internal/httpapi/admin/accounts/handler_accounts_crud.go` `listAccounts`）：
```json
{
  "identifier": "...",
  "enabled": true,                       // 新增：账号启用开关（缺省 true）
  "disabled_reason": "",                 // 新增："" | "banned" | "manual"
  "ban_is_muted": 0,                     // 新增：封禁状态（1=封禁）
  "ban_mute_until": 0,                   // 新增：解封 unix 时间戳（0=无）
  "ban_status": 0,                       // 新增：账号状态码
  "test_status": "ok"                    // 现有字段保持不变
  // ...现有 name/remark/email/mobile/proxy_id/has_password/has_token/token_preview 不变...
}
```

### 5.2 账号更新 `PUT /admin/accounts/{identifier}`
在现有 `name`/`remark` 基础上**新增**可写字段：
```json
{ "name": "...", "remark": "...", "enabled": false }
```
- `enabled` 为 bool：`false` → 手动禁用（`disabled_reason = "manual"`）；`true` → 启用并清空 `disabled_reason`。
- 更新成功后触发 `Pool.Rebalance()`（无需 `Pool.Reset()` 全量重建）。
- 校验：`identifier` 不存在 → 404；`enabled` 非 bool → 400。

### 5.3 负载池目标并发账号数
- **读取**：扩展现有 `GET /admin/queue/status` 的返回（`internal/account/pool_core.go` `Status()` 与 `handler_accounts_queue.go`），新增：
```json
{
  "active_pool_size": 0,       // 新增：目标并发账号数（0=不限制）
  "active_pool_count": 0,      // 新增：当前活跃池账号数
  "standby_count": 0,          // 新增：备用账号数
  "banned_count": 0,           // 新增：封禁账号数
  "disabled_count": 0,         // 新增：禁用账号数（含封禁）
  // ...现有 available/in_use/total/... 保持不变...
}
```
- **写入**：通过现有 `PUT /admin/settings` 的 `runtime` 段新增 `active_pool_size`：
```json
{ "runtime": { "active_pool_size": 3 } }
```
  - 写入成功后 `applyRuntimeSettings` 同步调用 `Pool` 的 activePoolSize 更新与 `Rebalance()`，即时生效。
  - `GET /admin/settings` 的 `runtime` 段同步返回 `active_pool_size`（缺省 0）。
  - 校验规则同 R4；非法值返回 400 与明确 detail。

### 5.4 触发复查的管理入口（可选，建议提供）
新增 `POST /admin/accounts/{identifier}/recheck-ban`：手动对指定账号执行一次封禁复查（绕过冷却），返回 `{ "banned": bool, "ban_is_muted": int, "ban_mute_until": float64, "ban_status": int }`。用于运营手动确认/恢复后立即刷新状态。**列为建议项**：若实现成本可控则纳入本期，否则标记为后续迭代。

---

## 6. WebUI 暴露面（`webui/src/features/account/*`）

### 6.1 账号表（`AccountsTable.jsx`）新增列/徽标
1. **启用/禁用开关**：每行新增一个 toggle（或开关按钮），调用 `PUT /admin/accounts/{identifier}` 传 `enabled`；禁用账号行视觉置灰并标注「已禁用」。
2. **封禁状态徽标**：
   - `ban_is_muted == 1` → 黄色徽标「已封禁」（检测结果中不得显示为绿色）。
   - 同时展示解封时间：`ban_mute_until > 0` 时格式化显示「解封于 <本地时间>」；`== 0` 显示「无解封时间」。
   - `ban_status` 可附带展示为「状态码 <N>」（次要信息）。
3. **禁用原因**：`disabled_reason == "banned"` 显示「因封禁自动禁用」；`"manual"` 显示「手动禁用」。
4. 现有状态灯逻辑与新增字段兼容：封禁/禁用账号不得显示为「会话活跃」。

### 6.2 负载池目标并发账号数设置
- 在 `QueueCards.jsx` 或账号页顶部新增一个「负载池目标账号数」输入框（数字，`min=0`，`0` 显示为「不限制」），读写 `runtime.active_pool_size`，通过 `PUT /admin/settings` 提交。
- `QueueCards` 扩展展示：活跃池账号数（`active_pool_count`）、备用账号数（`standby_count`）、封禁数（`banned_count`）、禁用数（`disabled_count`），并保留现有 `available/in_use/total`。

### 6.3 交互一致性
- 所有变更（开关、池大小）复用现有 `useAccountActions` / `useAccountsData` 的 `onRefresh` + `fetchAccounts` / `fetchQueueStatus` 刷新链路，避免新增并行状态源。
- 新增文案 key 追加到 `webui/src/i18n.jsx` 的 `accountManager` 段（中英双语），命名遵循现有 `accountManager.*` 风格（如 `accountManager.banned`、`accountManager.unbanAt`、`accountManager.activePoolSize`、`accountManager.standbyCount` 等）。

---

## 7. 验收标准映射

| 验收标准 | 规格对应 |
| --- | --- |
| 以登录响应 `user.chat.is_muted==1` 为封禁依据，记录 `user.chat.mute_until` 与 `user.status` | R1 |
| 明确请求遭拒触发点（captcha / 429 / 鉴权失败）与复查流程（重新登录拉取封禁状态） | R2 |
| 明确账号禁用语义：禁用后不再被负载池分配，但配置与 token 保留 | R3 |
| 明确 `active_pool_size` 的默认值、合法范围与校验规则 | R4 |
| 明确补足池子规则：活跃账号数低于目标值时自动启用备用账号 | R5 |
| 明确 Admin API 与 WebUI 暴露：启用/禁用开关、封禁状态与解封时间展示、负载池目标并发账号数设置 | R5.x/R6/R7 |

---

## 8. 关键边界与安全约束

1. **登录风暴防护**：封禁复查必须受 `ban_recheck_cooldown_seconds`（默认 60s）冷却限制，且仅在触发点上执行。
2. **不误杀**：非封禁原因的登录失败（网络/密码错误）不得触发自动禁用。
3. **向后兼容**：`enabled` 缺省 true、`active_pool_size` 缺省 0，旧配置升级后行为与现状一致。
4. **Vercel/环境变量回写**：`enabled`/`disabled_reason`/`active_pool_size` 走既有 `Store.Update` 持久化路径，遵循现有 env-writeback 语义（该 skip 则 skip，不新增写路径）。
5. **并发安全**：`rebalance`、`RemoveAccount`、`Acquire*` 共享同一把池锁；`lastBanRecheckAt` 由 `Resolver.mu` 保护。
6. **测试**：为 `extractBanFields`（已有）、封禁→禁用、rebalance 补足、`active_pool_size` 校验、Admin API 新增字段、Settings 读写新增字段各补单元/HTTP 测试，纳入仓库 `./tests/scripts/run-unit-all.sh` 与 `./scripts/lint.sh` 门槛。

## 全局单账号限额（Quota）

- 设置项：`runtime.daily_token_limit_m`（单位百万，`m`，0 = 不限制）与 `runtime.daily_request_limit`（次，0 = 不限制）。
- 统计窗口：`runtime.quota_window_hours`（小时，0 = 默认 24，范围 1–168）。两个限额都按**过去 N 小时的滚动用量**统计，窗口外的旧用量不再计入，账号会自动恢复轮换资格。
- 两个指标是**独立或**关系：任一达到上限，该账号即退出轮换池。
- 触发后由备用池中未达上限的账号补位；主动池与备用池均按同一规则过滤。
- 数据源：usage-stats 的按账号分钟桶（窗口 ≤ 30 小时时精确到分钟）；更宽的窗口回退到小时桶，边界最多多计一个小时（对限额而言是保守方向）。
- 账号列表返回 `usage_today_requests`、`usage_today_tokens`（字段名保留兼容，语义为窗口内用量）、`daily_token_limit`、`daily_request_limit`、`daily_limited` 与顶层 `quota_window_hours` 供界面展示。
- 用量结果在账号获取热路径上缓存 5 秒，因此超限判定最多滞后约 5 秒生效。
- 界面：达到限额的账号显示黄色「已达限额」徽标；窗口用量以 `近 N 小时 X · Y 次` 展示，悬停可见上限。
