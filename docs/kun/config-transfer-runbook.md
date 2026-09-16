# 跨环境迁移操作手册

把一个 Multica 工作区的**配置 + 你自己的聊天记录**打包，迁到另一台跑 `kun` 的自建实例。照着敲即可，不需要看代码。

迁完之后你会看到：聊天记录、说话顺序、时间都在。智能体在源机器上的「续聊指针」迁不过去，新实例上从新会话开始，不会接着源机器那一轮往下聊。

## 顺序：先导出 → 再切换 → 再导入

V2 的导出源 = 桌面**当前连接的服务器 + 当前工作区**。先切服务器，导出源就会变成自建实例自己，等于把空工作区搬进空工作区，官方云那批对话不会进包。

正确顺序：

1. **先导出**：桌面仍连官方云时，设置 → 工作区管理 → 跨环境迁移（含对话）→ 一键导出，把 zip 存到本机。
2. 确认拿到 zip：会话数 / 消息数与导出报告一致，包能打开。
3. **再切换**：设备 → 服务器，切到自建实例。完全退出再重开（macOS 用 ⌘Q）。
4. **再导入**：进入自建实例的目标工作区，同一张卡片 → 一键导入。

切换**不会**删除官方云的登录档（`~/.multica/profiles/desktop-api.multica.ai` 保留）。万一已经先切了服务器，仍可用这条从官方云补导：

```bash
multica --profile desktop-api.multica.ai transfer export --workspace <官方云工作区 slug> --out ~/transfer.zip
```

## 一次性准备

登录档命令必须你自己在本机终端敲。Agent 代跑会被拒绝（`requireHumanLocalCommand`）。

官方云档和自建档落在不同文件里，互不覆盖：

| 档 | 命令 | 配置文件 |
| --- | --- | --- |
| 官方云（默认档） | `multica setup` 或 `multica login` | `~/.multica/config.json` |
| 自建实例 | `multica setup self-host --profile selfhost` | `~/.multica/profiles/selfhost/config.json` |

第一次接自建实例：

```bash
multica setup self-host --profile selfhost
```

有自定义地址时加上 `--server-url` 和 `--app-url`。浏览器登录走完，这个档就记下了。

源端如果是官方云，用默认档（不要加 `--profile`，或另建一个 `--profile cloud`）。目标端用 `--profile selfhost`。同一条命令不要靠环境变量 `MULTICA_TOKEN` / `MULTICA_SERVER_URL` 临时改目标，否则会压过所有档。

## 命令行导出

先估体积，再打包。包里有聊天记录和成员邮箱，按敏感文件保管。

```bash
multica transfer export --profile <源档> --workspace <slug> --estimate
multica transfer export --profile <源档> --workspace <slug> --out ~/transfer.zip
```

- `<源档>`：官方云一般省略 `--profile`；若你建过 `cloud` 档就写 `--profile cloud`。
- `<slug>`：工作区地址栏里的短名，例如 `deneb`。
- `--estimate` 只打印会话数 / 消息数 / 估算体积，不写文件。
- 完成后终端会打出 zip 路径。权限是 `0600`，不要传到公开位置。

## 命令行导入

目标必须是跑 `kun` 的自建实例。先预演，再正式导入。

```bash
multica transfer import --profile <目标档> --workspace <slug> --in ~/transfer.zip --dry-run
multica transfer import --profile <目标档> --workspace <slug> --in ~/transfer.zip
```

- `<目标档>`：通常是 `selfhost`。
- `<slug>`：目标工作区必须已经存在，导入不会帮你新建工作区。
- `--dry-run` 只出报告、不写数据。预演没问题再去掉它跑一次。

### 导入选项

跨环境迁移的语义是复刻同一套环境，所以默认往「迁过去就能用」靠。这几个选项在 CLI 与 Desktop 卡片上含义一致，默认值也一致，并且同时作用于预演和正式导入：预演报告里的数字就是按当前开关算出来的。

| flag | 默认 | 作用 |
| --- | --- | --- |
| `--activate-autopilots` | `true` | 保留源端自动化的状态：源端是 `active` 的迁过去就是 `active`。**这个默认值意味着导入一结束这些自动化就会开始触发**，会真的派任务、消耗运行时额度。写 `--activate-autopilots=false` 则全部以 `paused` 导入，等你手动放行。 |
| `--apply-workspace-settings` | `true` | 把包里的工作区设置（上下文、仓库、归因）写进目标工作区。写 `--apply-workspace-settings=false` 则不落，目标端保留原设置。 |
| `--apply-issue-prefix` | `false` | 采用源端的 issue 前缀。会改掉目标工作区此后所有新任务的编号，属破坏性操作，所以默认不开。仅当目标工作区还没有任何任务时才生效；有任务时跳过，并在报告里记一条 `issue_prefix_skipped_target_has_issues`。 |
| `--auto-bind-runtimes` | `true` | 把导入的智能体绑到目标端与它原来那台相符的运行时上，**只在恰好一个候选时才绑**；多个候选一律留给你在卡片上选。候选只取你自己可见的运行时（你拥有的，或 `public` 的），绝不会把智能体挪到别人的机器和账号上。写 `--auto-bind-runtimes=false` 则全部不绑，退回 V2 原来的「只报告」行为。 |

Desktop 的「跨环境迁移」卡片上有同样的勾选项，默认值与上表一致；预览报告会写明「自动化：导入 N 条，其中 M 条已暂停」，勾上激活再应用一次，它们会立刻开始触发。

导入报告是一段 JSON。导入后必须自己做这三件事：

1. **`secrets_to_fill`**（在 `config_report` 里）  
   密钥不会进包。按每条的 `name` / `field` / `path` 到目标工作区对应设置页补：智能体环境变量、MCP、自动化 webhook 签名等。
2. **`runtimes_to_bind`**  
   导入会自动绑定**唯一匹配**的运行时（`status: bound`，开关 `--auto-bind-runtimes`，默认开）。只有两种情况要你动手：`status: choose` 表示目标端有多台相符的机器，导入不替你挑，到卡片上一次选完；`status: none` 表示没有可用的运行时，`reason` 会说清是「这台服务器上没有你可用的运行时」（先把本机 daemon 连上来）还是「没有相符的供应商 / 运行模式」。
3. **`export_gaps`**  
   源端哪些分组没导出或没按规则过滤。例如 `plugin_skills_unfiltered` 表示源端插件接口不可用，插件贡献的技能被整包带过来了，需要你自己核对。`issue_views_scope_capped` 与 `list_cap_reached`（源端单次请求上限已满，条目里带 `limit`）、`list_has_more`（源端还有下一页）、`list_shape_unknown`（源端返回了本项目读不懂的结构，等于整组没取到）都表示该分组列表不完整：这类分组只带回了服务端一次请求给得出的行，剩下的要你自己按提示回源端补。

## 用 Desktop 按钮迁移

卡片只在 Desktop 下出现；网页版没有这个按钮（官方云没有服务端导出端点，网页端也做不了本地打包），网页版要走上面的命令行。**必须先导出再切服务器**，顺序见上文。

1. 打开 Desktop（此刻连的是官方云）→ 进入要导出的工作区 → 设置 →「配置导出/导入」→ 卡片「跨环境迁移（含对话）」→ 看导出源是不是官方云的那个工作区 → 点【一键导出】→ 选保存位置 → 等进度走完 → 记下 zip 路径。
2. 确认 zip 到手之后，再用 Desktop 设备设置里的服务器切换，切到目标自建实例（`kun` 版本）。切之前如果还没导出过，确认弹窗会提示先去跨环境迁移里导出——这不是硬拦截，关掉提示仍可切换。
3. 完全退出再重开 → 进入目标工作区 → 同一张卡片 → 点【一键导入】→ 选那个 zip → 先看预演报告 → 确认 → 等导入完成。
4. 读导入报告：`secrets_to_fill`（要你去哪儿补哪些密钥）、`runtimes_to_bind`（哪些智能体已自动绑好、哪些还要你选一台机器）、`export_gaps`（源端哪些分组没导出、为什么）。卡片的「运行时绑定」区块里，绑好的直接写明绑到了哪台，留给你选的给下拉加一个【绑定】按钮，没得绑的写清原因。

卡片上「导入后激活自动化」「应用工作区设置」「采用 issue 前缀」「自动绑定运行时」四个勾选项的默认值与上文的 CLI 选项表一致；只有第一条会影响「自动化」是否马上开始跑，预演报告里会直接写明导入几条、几条已暂停。

## 失败排查

| 现象 | 含义 | 怎么办 |
| --- | --- | --- |
| `target_unsupported`（HTTP 404） | 目标不是带 `/transfer/*` 的 `kun` 实例 | 确认切到了自建 `kun`，不是官方云，也不是过旧的上游自建 |
| `transfer_bundle_corrupt` | zip 缺文件或内容校验对不上 | 重新导出一份，不要手工改 zip 里的文件 |
| 429 / 503 | 源端限流或暂时不可用 | CLI 会自动退避重试（最多 8 次、间隔翻倍）。一直失败就换个时间再导 |

包坏了不要「修一下再导」。重新导出。

## 迁完长什么样

- 聊天记录、顺序、时间在目标端完整恢复。
- 智能体的续聊指针绑在源机器上，迁不过去；新实例上从新会话开始。
- 密钥、运行时绑定要按报告手工补。
