# DSH 供应商切换：真实生效面与写入契约（DENE-341）

本页是证据，不是推测。所有结论都来自本机 `@deepseek-ai/dsh` **0.1.5-rc.1** 的包源码
（`/opt/homebrew/lib/node_modules/@deepseek-ai/dsh/lib/` 与
`node_modules/@deepseek-ai/dsh-*/lib/`），并用本机可观察的假 HTTP 端点跑通了一次真实切换。

本页只回答「怎么做」。实现（daemon / 前端）在 DENE-339 的后续阶段。

## 结论速查

| # | 问题 | 结论 |
| --- | --- | --- |
| 1 | 哪些键真正决定请求打到哪 | `agent-default-model.{provider,model}` 选路由；`llm-pi-ai.providers.<route>.{api,baseURL,models}`（+ `compat`/`headers`/…）定义该路由。**key 不在 settings 里**。`llm-deepseek.baseURL` 是**另一条独立路由**（`deepseek-official`）的端点，只在 `agent-default-model.provider` 指向它时生效 |
| 2 | key 从哪读 | `apiKeyEnv` 是**凭据引用名**，不是内联值。取值链（高→低）：**进程环境变量 > `$DSH_HOME/.credentials.yaml` 的 `refs` > `<cwd>/.env` > `$DSH_HOME/.env`**。settings 内联 key（`apiKey:`/`apiKeyEnv: sk-…`）**会被静默忽略** |
| 3 | 环境变量能否覆盖 baseURL | **只有 `deepseek-official` 能**：`DEEPSEEK_BASE_URL`（端点）+ `DEEPSEEK_API_KEY`（key）。pi-ai 路由**没有** baseURL 环境变量。`llm-deepseek.baseURL` 写进 settings.yaml 也生效 |
| 4 | `DSH_HOME` 换账号后配置跟着走吗 | **跟着走**。`settings.yaml`、`.credentials.yaml`、`profiles/`、`sessions/`、`storages/` 全在 `$DSH_HOME` 下。⇒ **供应商预设必须每账号一份**；但 key 可用进程环境变量下发而绕开每账号落盘 |
| 5 | 写回 settings.yaml 的安全写法 | DSH 运行中不长期持有该文件；每次写都**重读磁盘 + 跨进程锁 + 注释/键序保持的叶子级 diff + 原子替换**。跑着的请求不会被打断，新写入对新请求立即生效。**但 `settings.yaml.bak-*` / `cordis.patch.yml.bak-*` 这类备份不是 DSH 产生的——DSH 不写任何 `.bak-*`**，那是人工或历史改动留下的 |

## 依据与路径

| 事实 | 路径 |
| --- | --- |
| HOME 解析优先级：显式配置 > `$DSH_HOME` > `~/.dsh` | `dsh-home-paths/lib/index.js` → `resolveDshHome` |
| `settings.yaml` = `<home>/settings.yaml`，可用插件 `path` 覆盖 | `dsh-settings-file/lib/index.js` → `resolveSpec` |
| `.credentials.yaml` = `<home>/.credentials.yaml` | `dsh-credentials-local/lib/index.js` → `resolveSpec` |
| 凭据分层（process > .credentials.yaml > cwd/.env > home/.env） | `dsh-credentials-local/lib/index.js` → `resolve` / `inherited` / `dotenvFallback` |
| 命名空间解析顺序：schema 默认 → 组合 base → 用户层 | `dsh-settings/lib/index.js` → `resolve` / `mergeLayers` |
| `apiKeyEnv: z.string().role("credential-ref")` | `dsh-llm-pi-ai/lib/index.js` → `profile` |
| key 解析（`resolveApiKey`）与 `MISSING_CREDENTIAL` | `dsh-llm-pi-ai/lib/index.js` → `apply` |
| `deepseek-official` 的 `DEEPSEEK_BASE_URL` / `DEEPSEEK_API_KEY` | `dsh-llm-deepseek/lib/index.js` → `resolveAdapterOptions` |
| 路由集变化时原地重注册（不用重启） | `dsh-llm-pi-ai/lib/index.js` → `ensureRegistrationFacts` / `ensureDirectory` |
| 原子写与写锁 | `dsh-atomic-write/lib/index.js` → `writeFileAtomic` / `withFileLock` |
| 注释保持的叶子级 diff | `dsh-settings-file/lib/index.js` → `patchNode` / `persistSection` |
| 每个任务的模型选择 | `~/.agents/dsh-multica-runtime/src/index.ts` → `resolveSelection` |

## 1. 生效面

### `agent-default-model`（选路由）

```yaml
agent-default-model:
  provider: <llm-pi-ai.providers 的键>   # 也接受 deepseek-official
  model: <该路由 models[].id>
  # reasoningEffort 可选
```

schema：`dsh-agent-default-model/lib/index.js`（`provider` / `model` / `reasoningEffort`）。
它只构成**默认选择**；带模型选择的任务会覆盖它（见下）。

### `llm-pi-ai.providers.<route>`（定义路由）

```yaml
llm-pi-ai:
  providers:
    <route>:
      api: openai-completions        # 也可以是 anthropic-messages 等，见 supportedProtocols()
      baseURL: https://…/v1
      apiKeyEnv: SOME_ENV_NAME       # ← 引用名，不是 key
      models: [{ id, name, contextWindow, … }]
      compat: { thinkingFormat: deepseek }
```

schema：`dsh-llm-pi-ai/lib/index.js` → `profile` / `Config`。
`resolveProfiles()` 里 `apiKeyEnv` 被包成 `credentialRef(apiKeyEnv)`——确认它是**名字**。

### `deepseek-official`（独立路由）

`llm-deepseek` 插件自己拥有路由 id `deepseek-official`，**不读** `llm-pi-ai` 段：

```yaml
# 端点三选一（高→低）：插件组合 config.baseURL > $DEEPSEEK_BASE_URL > https://api.deepseek.com
llm-deepseek:
  baseURL: http://127.0.0.1:8791/v1
```

key 固定引用 `DEEPSEEK_API_KEY`（`apiKeyEnv` 默认值）。**实测：settings 里的 `llm-deepseek.baseURL` 确实生效**
（见「实测证据」C 段）。

## 2. key 的三种写法：只有一种被接受

`resolveApiKey(provider, profile)` 的实际行为：

1. `profile.apiKeyEnv === undefined` ⇒ **不做凭据查找**，交给 pi-ai 自己的环境发现（本线不用）。
2. 否则 `ctx.credentials.resolve(apiKeyEnv)`；DSH 未挂 `credentials` 服务时退回进程环境变量。
3. 取到空/没有 ⇒ `MISSING_CREDENTIAL`，请求发出前失败。

| 写法 | 接受？ | 说明 |
| --- | --- | --- |
| `apiKeyEnv: PROBE_API_KEY` + `.credentials.yaml` 里有 `PROBE_API_KEY` | ✅ | **推荐**。值不落 settings |
| `apiKeyEnv: PROBE_API_KEY` + 进程环境变量里有 `PROBE_API_KEY` | ✅ | 且**压过**文件里的同名声（trusted layer） |
| `apiKey: sk-…` 直写在 provider 下 | ❌ | 未知键被保留但**完全没人读**。实测请求带的是 `refs` 里的值，不是内联值 |
| `apiKeyEnv: sk-…`（把值当名字） | ❌ | 会拿 `sk-…` 当引用名去查，查不到 ⇒ `MISSING_CREDENTIAL` |

`.credentials.yaml` 的形状（v1，严格模式——未版本化的扁平布局会被拒绝并提示迁移）：

```yaml
version: 1
refs:
  SOME_ENV_NAME: <secret>
records: {}      # 可选；OAuth 记录专用，供应商预设不用
```

文件权限必须是 `0600`：`assertOwnerOnly` 在读取前检查 group/other 位，非 0600 直接拒绝启动。

## 3. 环境变量覆盖 baseURL：半开的口子

| 目标 | 有环境变量吗 |
| --- | --- |
| `deepseek-official` 的 baseURL | ✅ `DEEPSEEK_BASE_URL`（只从 **trusted** 层读：进程继承环境或 launch 快照的 process 层） |
| `deepseek-official` 的 key | ✅ `DEEPSEEK_API_KEY` |
| 任意 pi-ai 路由的 baseURL | ❌ **没有** |
| 任意 pi-ai 路由的 key | ✅ 通过 `apiKeyEnv` 指向的环境变量名 |

⇒ **「不改文件就能切」对 pi-ai 路由不成立**。只有把默认模型指向 `deepseek-official` 时，
才可以用 `DEEPSEEK_BASE_URL` + `DEEPSEEK_API_KEY` 纯环境变量切换——但那等于放弃预设路由语义
（没有 `displayName`、没有自定义 models、和 `command-code` 这类路由不能共存为预设）。

**结论：DENE-339 选「写配置文件」是对的。** 环境变量只作为可选的下发通道，不作为主通道。

## 4. `DSH_HOME` 与账号

`resolveDshHome()` 优先级：显式配置 > `$DSH_HOME`（空/纯空白视为未设）> `~/.dsh`。
`settings.yaml` / `.credentials.yaml` / `profiles/` / `sessions/` / `storages/` 全在根下。
Multica daemon 侧的账号杠杆就是 `env:DSH_HOME`（`server/internal/daemon/agent_accounts.go` → `agentLeverDshHome`）。

⇒ **预设落盘 = 每账号一份**。`~/.dsh/settings.yaml` 里的 `command-code` 在
`DSH_HOME=~/.dsh-account2` 时**不存在**。父票的「预设存 daemon 本地」需要补一句主语：**存哪个账号的**。

绕过办法：**key 走进程环境变量**（trusted 层压过文件），端点走 `DEEPSEEK_BASE_URL`。
这样 daemon 可以在**任务进程**里下发凭据，不必往每个账号目录里复制 secret。
但两条限制要写进实现说明：只有 `deepseek-official` 享受端点这条；而且 DSH 会把
`*KEY*/*SECRET*/*TOKEN*` 形状的环境变量从**它自己 spawn 的子进程**里剔除
（`dsh-subprocess` 的 `scrubbedParentEnv`），所以别指望 agent 的 bash 里能看到——也不需要，
harness 在发请求前就把值换成 `Authorization` 头了。

## 5. 写回 settings.yaml 的安全写法

DSH **不在运行中持有** `settings.yaml` 的内容；它靠 chokidar 监听 + 每次写前重新读盘。
所以外部写入是**被支持的**，但有四条硬约束：

1. **必须原子替换。** DSH 自己用 `writeFileAtomic`：同目录随机后缀 `wx` 创建临时文件 → `rename` 覆盖。
   跑着的请求不会被截断，读者永远看到旧内容或新内容。
2. **必须拿写锁。** 锁是 `<file>.lock` 的 `wx` 独占创建（DSH 默认等待 10s，凭据文档 30s，超时抛错）。
   不做「读-改-写」就等着被 DSH 的读-改-写覆盖。**注意 DSH 用完不删 `.lock` 文件**，
   所以「`settings.yaml.lock` 存在」不代表有活跃写者——只能靠 `wx` 创建失败来判断。
3. **注释与键序不会被重排。** `patchNode` 做叶子级 diff：只 `setIn` 变化的标量、只 `deleteIn` 删掉的键；
   未触碰的节点（含注释、锚点）保持原样。**只有被替换的数组/标量内部的注释会随整体替换消失。**
   实测：settings.yaml 里 provider 键序在 DSH 读取后未变。
4. **解析失败 = 保留上一份好配置，不是覆盖。** 运行时 reload 解析失败只 warn 并保留内存里的旧值；
   写路径则会先抛错再落盘——**永远不会把坏 YAML 写下去**。

### 强烈建议：不要用 Go 直接改 YAML，改成让 DSH 自己写

`ctx.settings` 提供了带冲突检测和路径级编辑的写接口：

```ts
await ctx.settings.mutate('agent-default-model', [
  { op: 'set', path: ['provider'], value: 'preset-b' },
  { op: 'set', path: ['model'],    value: 'model-b' },
], expectedRevision)
await ctx.credentials.set('PRESET_B_KEY', '<secret>')   // 写 .credentials.yaml 的 refs
```

`mutate` 按路径编辑、`expectedRevision` 不匹配抛 `SETTINGS_CONFLICT`、
provider 侧负责原子落盘与注释保持。**这正是 DSH 自己的 Web Models 页在用的路径**——
由 DSH 写自己的文件，我们就不用复刻它的锁、diff 和原子替换三件套，也不会和它的
chokidar 重载打架。

桥接包（`~/.agents/dsh-multica-runtime`，npm 名 `@multica-ai/dsh-runtime`）当前
**没有暴露 settings/credentials**（`src/` 里对二者零引用；它只做 probe / list-models / stdio）。
所以要么给桥接加一个 action，要么 daemon 直接改 YAML。**成本对比：**
- 桥接加 action：实现面最小、最安全，但要动**另一个仓库**并让用户更新桥接包才能用上。
- daemon 直接改 YAML：随 monorepo 一起发，但要复刻原子写 + `.lock`，
  并且要在写完后再让 DSH 自己重读（DSH 的 chokidar 会处理，无需重启）。

## 6. 切换的最小写入集

**定义/维护一个预设**（一次）：

```yaml
llm-pi-ai.providers.<route> = { api, baseURL, apiKeyEnv: <REF_NAME>, models: [...] }
.credentials.yaml: refs.<REF_NAME> = <secret>        # 值不落 settings
```

**激活一个预设**——两条路，**按需选**：

- **A. 每任务切换（推荐，零文件写入）**：桥接的 `execute` 帧已经接 `model: {provider, id}`，
  且 `command.model` **优先于** `agent-default-model`（`resolveSelection`）。
  ⇒ daemon 只需在任务级下发模型选择，就能按任务换供应商，**不碰任何配置文件**。
  **实测已证**：同一 profile、同一进程，`model` 换成 `probe-route/probe-model` 打到 :8791，
  换成 `deepseek-official/deepseek-flash` 打到 :8792。
  前提：该 route **必须先在 settings 里注册**——`resolveSelection` 会调
  `llm.resolveModelInfo(provider, model)`，未注册的路由直接抛错。

- **B. 全局默认（换「新任务默认用哪个」）**：
  ```yaml
  agent-default-model.provider = <route>
  agent-default-model.model    = <model id>
  ```
  两个键都要写，**漏写 `model` 会指向上一家的模型 id，请求必然失败**。

无论 A 还是 B，**路由定义和 key 都在 CLI 侧的文件里**——没有「只改一个键就换一家端点」的魔法。

## 7. daemon 侧四个动作的数据形状建议

沿用既有 `runtimes/{runtimeId}/<action>/{requestId}/result` 请求—回执模式
（与 `models` / `local-skills/import` 同形）。

### 共同字段

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `id` | string | 预设 id，正则 `^[a-z0-9][a-z0-9-]{0,63}$`。也直接当 `llm-pi-ai.providers` 的键 |
| `name` | string | 显示名；空则回落 `id`。同时写进 provider 的 `displayName` |
| `api` | enum | `openai-completions` / `anthropic-messages` / …（闭集，daemon 侧白名单校验） |
| `baseURL` | string | **建议限制 `http(s)://`**，防 `file://`/`data:`；空串非法（DSH 也会拒） |
| `keyRef` | string | **环境变量名**（=`apiKeyEnv`），`^[A-Za-z_][A-Za-z0-9_]*$`，<=128 |
| `hasKey` | bool | 只读。该 ref 在当前账号**是否有值**（`ctx.credentials.describe`） |
| `keyMask` | string | 只读。`sk-ab…cdef`：**保留前 4 + 后 4，其余固定宽度省略**；<12 字符显示 `…`。**永不返回原值、永不写日志** |
| `models` | array | `[{id, name?, contextWindow?, maxTokens?}]` |
| `home` | string | 该预设生效的账号目录（见「每账号一份」） |
| `active` | bool | 只读。是否等于当前 `agent-default-model.provider` |
| `revision` | int | 只读。settings 命名空间的 revision，写回时用作 `expectedRevision` |

### `list`

请求：`{ runtime_id, home? }` → 结果：

```json
{ "ok": true, "presets": [ /* ProviderPreset[] */ ],
  "active": { "provider": "preset-a", "model": "model-a" },
  "document": "/Users/…/.dsh/settings.yaml", "revision": 7 }
```

`active` 可以直接复用桥接已有的 `--list-models`：它的 `default: true` 标记就是
`agentDefaultModel.currentSelection()`（`src/index.ts` → `listModels`）——**读侧已经有现成数据源**。

### `upsert`

请求：`{ runtime_id, home?, preset: ProviderPreset, key?: string, expected_revision?: int }`

- `key` **只在写入时出现**，成功后不回传。
- `key` 省略 ⇒ 保留已有值（**轮换 key 不必重填 baseURL**）。
- `key: ""` ⇒ 显式清除（对应 `credentials.unset`）。
- 写入顺序必须是：先写凭据、再写 settings；两步都要成功才算成功。凭据写失败 ⇒ **不写 settings**
  （宁可预设没生效，也不要留下一个指向不存在 key 的路由——那会让整个命名空间落进
  「保留上一份好配置」分支）。

### `delete`

请求：`{ runtime_id, home?, id, drop_key?: bool }`

- 拒绝删除**当前 active** 的预设（除非请求带 `force: true` 且同时给出新的 `active`）。
- 默认**不删**凭据（同一 ref 可能被别的预设共用）；`drop_key: true` 才清。
- 删完必须检查 `agent-default-model.provider`：若指向被删的 route，回落 + 明确回执。

### `activate`

请求：`{ runtime_id, home?, id, model?, reasoning_effort? }` → 结果同 `list` 的 `active`

- `model` 省略 ⇒ 取该预设 `models[0].id`；预设没有 `models` ⇒ 必须显式给 `model`。
- 实现即 `settings.mutate('agent-default-model', [set provider, set model])`。

### 错误码

| 码 | 语义 |
| --- | --- |
| `PROVIDER_NOT_FOUND` | id 不存在 |
| `PROVIDER_INVALID` | api 不在闭集 / baseURL 非法 / models 为空 |
| `PROVIDER_NO_MODEL` | 激活时无法确定 model |
| `PROVIDER_IN_USE` | 删除 active 预设且未 `force` |
| `MISSING_CREDENTIAL` | 激活/校验时 ref 没有值（**沿用 DSH 自己的码**，便于前端直显） |
| `SETTINGS_UNREADABLE` | settings.yaml 解析失败或不是 map（**拒绝写，不覆盖**） |
| `SETTINGS_WRITE_FAILED` | 锁超时 / 磁盘错误 |
| `SETTINGS_CONFLICT` | `expected_revision` 不匹配（**沿用 DSH 的码**） |
| `CREDENTIALS_UNREADABLE` | `.credentials.yaml` 解析失败或权限不是 `0600` |
| `CREDENTIALS_WRITE_FAILED` | 凭据写失败 |

**掩码规则（硬要求）**：`keyMask` 在任何路径上都不返回原值——不进 JSON、不进日志、不进 issue。
`ProviderPreset` 里没有 `key` 字段，key 只能走 `upsert` 的入参，从结构上堵住回显。

## 8. 实测证据

环境：`dsh 0.1.5-rc.1`，Node v26.8.2，macOS。假端点是本机 `node` HTTP 服务，
记录每个请求的 `method/url/Authorization` 并回最小 SSE 完成帧。凭据值是占位串，非真实 key。

### A. 真实切换（每种字节都能复现）

```yaml
# $DSH_HOME/settings.yaml —— 只改这两个键就完成了切换
agent-default-model:
  provider: preset-a
  model: model-a
llm-pi-ai:
  providers:
    preset-a:
      api: openai-completions
      baseURL: http://127.0.0.1:8791/v1
      apiKeyEnv: PROBE_API_KEY
      models:
        - id: model-a
          name: Model A
          contextWindow: 32768
```

```yaml
# $DSH_HOME/.credentials.yaml (0600)
version: 1
refs:
  PROBE_API_KEY: placeholder-not-a-real-key
```

```console
$ DSH_HOME=~/.dsh-probe/home dsh --profile headless "reply with the single word probe-ok"
probe-ok
$ cat ~/.dsh-probe/requests.log
POST /v1/chat/completions  Bearer placeholde…(len=33)  {"model":"model-a", …}
POST /v1/chat/completions  Bearer placeholde…(len=33)  {"model":"model-a", …}
```

`len=33` 正是 `placeholder-not-a-real-key` 的长度 ⇒ 值来自 `.credentials.yaml`，不是 settings 内联。
两次请求是「任务正文」+「会话标题」，都走了新端点。

### B. 每任务模型选择换端点（同一进程，零文件写入）

用桥接自己的协议驱动 `dsh --profile multica --stdio`，只改 `execute.model`：

```console
$ node stdio-drive.mjs probe-route/probe-model        # 打到 :8791
ready | session | text=probe-ok | usage | result       exit: 0
$ node stdio-drive.mjs deepseek-official/deepseek-flash   # 打到 :8792（DEEPSEEK_BASE_URL 指向它）
ready | session | text=probe-ok | usage | result       exit: 0
```

:8791 与 :8792 的访问日志互不交叉——**每个任务的模型选择真的换了端点**，
且不重启进程、不改 `agent-default-model`。

### C. `llm-deepseek.baseURL` 写 settings 生效

`DEEPSEEK_BASE_URL` 未设，仅 settings 里写 `llm-deepseek.baseURL: http://127.0.0.1:8792/v1`，
请求确实打到 :8792（不是 `api.deepseek.com`）。

### D. 凭据分层

| 场景 | 结果 |
| --- | --- |
| ref 只在 `.credentials.yaml` | ✅ 请求带文件里的值 |
| ref 同时存在进程环境变量 | ✅ 请求带**环境变量**的值（trusted layer 压过文件） |
| ref 谁都没有 | ❌ `MISSING_CREDENTIAL`，请求未发出 |
| settings 里内联 `apiKey: sk-…` 且 ref 仍指向文件 | ✅ 请求带**文件里**的值 ⇒ 内联值被忽略 |
| `.credentials.yaml` 权限非 0600 | ❌ 启动即拒绝并提示 `chmod 600` |

### E. 读侧现成能力

```console
$ DSH_HOME=~/.dsh-probe/home dsh --profile multica --list-models
{"v":1,"type":"models","models":[ … {"id":"probe-route/probe-model","provider":"probe-route","default":true} ]}
```

`provider` 是**目录里的显示名**，`id` 是 `<provider-route>/<model-id>` 且做了 URL 编码。
`default: true` 直接对应 `agent-default-model`。**前端「当前生效」不必另造数据源。**

## 9. 与 DENE-339 已定架构的偏差

父票的取舍（密钥只在本机、key 写-only、原子写 + 备份、首个 CLI 只做 DSH、经服务端转发不落库）
**全部成立**，本阶段没有推翻任何一条。三点需要它在实现阶段明确：

1. **每任务切换比全局切换更省事。** 桥接已支持 `execute.model`，daemon 也已经在传
   （`server/pkg/agent/dsh.go` → `parseDshModelID` / `dshExecuteCommand.Model`）。
   ⇒ 「激活」可以做成**任务级下发**，完全不写文件；全局默认只是可选的「新任务默认用哪家」。
   这会影响 UI 语义：「当前生效」在任务级模式下是**每个任务各自的**，不是全局单值。
2. **预设落盘是每账号一份**（`$DSH_HOME` 决定）。「daemon-local store」要写成
   「按账号目录存放」；key 建议走进程环境变量下发以免在每个账号目录里复制 secret。
3. **`.bak-*` 轮转要我们自己实现。** DSH 不产出任何备份，父票写的「保留最近 10 份」是加法，
   不是「跟 DSH 一致」。而且如果走桥接的 `ctx.settings.mutate`，原子写与注释保持由 DSH 负责，
   我们只需在 mutate 之前自己留一份快照。

第 1、2 点会改变 UI 与存储路径的措辞，请编排确认；本阶段不自行改架构往下做。
