# 跨环境迁移操作手册

把一个 Multica 工作区的**配置 + 你自己的聊天记录 +（可选）任务与评论**打包，迁到另一台跑 `kun` 的自建实例。照着敲即可，不需要看代码。

迁完之后你会看到：聊天记录、说话顺序、时间都在；带了任务分组时，任务编号、评论线程与父子层级也和源端一致。智能体在源机器上的「续聊指针」迁不过去，新实例上从新会话开始，不会接着源机器那一轮往下聊。

## 顺序：先导出 → 再切换 → 再导入

V2 的导出源 = 桌面**当前连接的服务器 + 当前工作区**。先切服务器，导出源就会变成自建实例自己，等于把空工作区搬进空工作区，官方云那批对话不会进包。

正确顺序：

1. **先决定目标工作区。** 只迁配置与对话，用现有工作区即可；**要把任务也迁过去，目标必须是一个没有任何任务的工作区**（见下节），先在目标实例上新建一个空工作区。
2. **先导出**：桌面仍连官方云时，设置 → 工作区管理 → 跨环境迁移（含对话）→ 一键导出，把 zip 存到本机。
3. 确认拿到 zip：会话数 / 消息数与导出报告一致，包能打开。
4. **再切换**：设备 → 服务器，切到自建实例。完全退出再重开（macOS 用 ⌘Q）。
5. **再导入**：进入自建实例的目标工作区，同一张卡片 → 一键导入。

切换**不会**删除官方云的登录档（`~/.multica/profiles/desktop-api.multica.ai` 保留）。万一已经先切了服务器，仍可用这条从官方云补导：

```bash
multica --profile desktop-api.multica.ai transfer export --workspace <官方云工作区 slug> --out ~/transfer.zip
```

一次导入内部的分组顺序是固定的：**配置 → 任务分片 → 对话 → 附件 → 任务收尾**。这是 CLI 自己走的顺序，你不需要分步敲；它和上面「先导出再切换再导入」的跨实例顺序是两件事。

## 导入任务前先建一个空工作区

任务分组的硬前置：**目标工作区必须没有任何任务**。编号按票逐一保真（源端 `DENE-365` 到目标端还是 `DENE-365`），工作区里只要已经躺着一张不是这个包带来的任务，导入端就会整包拒收，返回 `transfer_issues_target_not_empty`，一行都不写。

所以：

1. 在目标实例上新建一个工作区，不要给它手动建任务、也不要先跑别的导入。
2. 把包导进这个空工作区。
3. 配置与对话可以照常导进已有任务的工作区；只有任务分组有这条空工作区要求。

已经导过一次、想再补一遍同一个包是可以的：重导幂等，包内任务不会重复，也不会顶高编号水位。

`--renumber` 是这条前置的唯一替代：它给任务编号加一个偏移量，让它们能落进已有任务的工作区。**Desktop 卡片不提供这个选项**，只在 CLI 上，并且要敲 `yes` 确认；代价是正文与评论里写下的旧编号（`DENE-365` 这类纯文本引用）会全部指向错误的任务——这类文本引用两种场景下都不会被改写。走 `--renumber` 时也不再默认「采用 issue 前缀」（目标必然非空，前缀落不下去），所以两端 `identifier` 不会相同，下一节的自检只适用于空目标导入。

命令行默认的冲突策略是 `--on-conflict fail`，但**目标工作区自带的 7 个系统状态不算冲突**：每个工作区一建好就 seed 了 `backlog` / `todo` / … 这 7 条，源端包里也带着同名同色的这 7 条，导入端按「目标已有即跳过」处理。所以空工作区 + 默认参数不会一上来就 409 `entity already exists: backlog`，预览报告照常出得来；真正属于用户数据的同名才吃 409（DENE-408，见下面「导入选项」表）。

### 导入后自检：两端标识符必须逐字节相同

空目标 + 默认参数（**默认就带上了「采用 issue 前缀」**，见下节）导入后，两端 `identifier` 列表必须完全一致——这是「正文里的 `DENE-xxx` 引用一个字都不用改写」的唯一凭据。每个档各自指向自己的服务器，所以两边各跑一次：

```bash
multica --profile <源档>   issue list --output json | jq -r '.issues[].identifier' | sort > src-identifiers.txt
multica --profile <目标档> issue list --output json | jq -r '.issues[].identifier' | sort > dst-identifiers.txt
diff -u src-identifiers.txt dst-identifiers.txt && echo "(no differences)"
shasum -a 256 src-identifiers.txt dst-identifiers.txt
```

出现 `-DENE-1 … +TGT-1` 这种逐行差异，只可能是两件事：`--apply-issue-prefix` 被显式关掉了（卡片上就是「采用 issue 前缀」被取消勾选），或目标工作区在导入前并不空、前缀被守卫跳过（报告里会有 `issue_prefix_skipped_target_has_issues`）。`issue list` 默认一页 50 条，任务多时按 `has_more` / `--offset` 翻完再比。

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
multica transfer export --profile <源档> --workspace <slug> --include config,conversations,attachments,issues --out ~/transfer.zip
```

- `<源档>`：官方云一般省略 `--profile`；若你建过 `cloud` 档就写 `--profile cloud`。
- `<slug>`：工作区地址栏里的短名，例如 `deneb`。
- `--estimate` 只打印会话数 / 消息数 / 估算体积，不写文件。
- `--include` 默认是 `config,conversations,attachments`。**要迁任务就显式加上 `issues`**，它是整个包里唯一的默认关闭项：带任务的包体积是平时的数倍到数十倍，而且有上面那条空工作区前置。
- 版本号跟着内容走：不带 `issues` 产出 `schema_version: 1`（未升级的目标端也能读），带 `issues` 产出 `2`（目标端必须是含 V3 的 `kun`）。
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
- 带任务的包按固定顺序上传：配置 → 任务 → 对话 → 附件 → 任务收尾。中途失败就从这一步重跑同一个包，重导幂等。
- `--renumber` 只给「目标工作区已经有任务、又要把任务搬进去」的场景用。它会要求你敲一遍 `yes`，然后把编号映射表写到 `<in>.number-map.csv`（每张导入的票一行）。**Desktop 卡片不暴露这个选项**。

### 导入选项

跨环境迁移的语义是复刻同一套环境，所以默认往「迁过去就能用」靠。下表中前四个开关在 CLI 与 Desktop 卡片上含义一致，默认值也一致，并且同时作用于预演和正式导入：预演报告里的数字就是按当前开关算出来的（自动绑定例外，见下）。

| flag | 默认 | 作用 |
| --- | --- | --- |
| `--activate-autopilots` | `true` | 保留源端自动化的状态：源端是 `active` 的迁过去就是 `active`。**这个默认值意味着导入一结束这些自动化就会开始触发**，会真的派任务、消耗运行时额度。写 `--activate-autopilots=false` 则全部以 `paused` 导入，等你手动放行。 |
| `--apply-workspace-settings` | `true` | 把包里的工作区设置（上下文、仓库、归因）写进目标工作区。写 `--apply-workspace-settings=false` 则不落，目标端保留原设置。 |
| `--apply-issue-prefix` | 跟着包走（包带 `issues` 分组时 `true`，否则 `false`） | 采用源端的 issue 前缀。会改掉目标工作区此后所有新任务的前缀。**包带 `issues` 分组时默认开启**（DENE-404）：任务分组的硬前置是「目标工作区没有任何任务」，空目标下号是原样保留的，前缀不跟过来就等于把标识符从 `DENE-1` 改成 `TGT-1`，正文与评论里的 `DENE-xxx` 引用全部指空。显式写 `--apply-issue-prefix=false` 才会关掉。仅当目标工作区还没有任何任务时才生效；有任务时跳过，并在报告里记一条 `issue_prefix_skipped_target_has_issues`；走 `--renumber` 时不再默认开启（目标必然非空）。 |
| `--auto-bind-runtimes` | `true` | 唯一候选自动绑（DENE-364）：目标实例上有且只有一个运行时的 `provider` + `runtime_mode` + 自定义 profile 与源端一致时，直接给智能体写上这个运行时。有多个候选一律不动，留给人工点。写 `--auto-bind-runtimes=false` 则全部留给人工。**只在正式导入时写**，`--dry-run` 只出计划。 |
| `--on-conflict` | `fail` | 目标已有同名实体时的策略：`fail` / `overwrite` / `rename` / `skip`（见 `docs/kun/config-export-import.md` §5.2）。**目标工作区自带的 7 个系统状态不算冲突**（DENE-408，见「导入任务前先建一个空工作区」），所以空工作区 + 默认参数不会误吃 409。真正属于用户数据的同名（label / 智能体 / skill 等）在 `fail` 下照旧 409，错误里带冲突实体名。Desktop 卡片上这一项的默认值是 `skip`（DENE-318），比 CLI 宽松。 |

Desktop 的「跨环境迁移」卡片上有同样四个勾选项，默认值与上表一致（「采用 issue 前缀」默认勾上）；卡片看不到包里的分组，所以它**总是**把该开关显式传给 CLI，取消勾选发 `--apply-issue-prefix=false`。预览报告会写明「自动化：导入 N 条，其中 M 条已暂停」，勾上激活再应用一次，它们会立刻开始触发。

导入报告是一段 JSON。导入后必须自己做这三件事：

1. **`secrets_to_fill`**（在 `config_report` 里）  
   密钥不会进包。按每条的 `name` / `field` / `path` 到目标工作区对应设置页补：智能体环境变量、MCP、自动化 webhook 签名等。
2. **`runtimes_to_bind`**  
   每行一个 `status`：`bound`（已经绑好，看 `bound_runtime_name`）、`pending`（有多个候选，需要你选）、`no_candidate`（目标实例上没有匹配的运行时，`reason_code` + `reason` 说明缺什么）。零候选最常见的原因是目标实例上还没有那台机器：先把本机 daemon 连到目标实例，再重跑一次导入，或在卡片上手动选一个运行时。
3. **`export_gaps`**  
   源端哪些分组没导出或没按规则过滤。例如 `plugin_skills_unfiltered` 表示源端插件接口不可用，插件贡献的技能被整包带过来了，需要你自己核对。`issue_views_scope_capped` 与 `list_cap_reached`（源端单次请求上限已满，条目里带 `limit`）、`list_has_more`（源端还有下一页）、`list_shape_unknown`（源端返回了本项目读不懂的结构，等于整组没取到）都表示该分组列表不完整：这类分组只带回了服务端一次请求给得出的行，剩下的要你自己按提示回源端补。`autopilot_fields_unreadable` 表示某条自动化的详情读不出任何字段（源端返回了本项目读不懂的结构），导出侧没有把它当成一条空自动化塞进包里，而是直接丢掉并记这条：这类自动化不会出现在「自动化：导入 N 条」里，需要你回源端核对后再单独补。

多候选那部分也可以在命令行补：

```bash
multica transfer bind-runtimes --profile <目标档> --workspace <slug> \
  --bind <agent_id>=<runtime_id> --bind <agent_id>=<runtime_id>
```

`agent_id` 用报告里的 `agent_target_id`，`runtime_id` 用同一行的 `candidates[].id`。响应逐条给成败，一条失败不影响其余。

## 用 Desktop 按钮迁移

卡片只在 Desktop 下出现；网页版没有这个按钮（官方云没有服务端导出端点，网页端也做不了本地打包），网页版要走上面的命令行。**必须先导出再切服务器**，顺序见上文。

1. 打开 Desktop（此刻连的是官方云）→ 进入要导出的工作区 → 设置 →「配置导出/导入」→ 卡片「跨环境迁移（含对话）」→ 看导出源是不是官方云的那个工作区 → 勾选要带的分组（见下）→ 点【一键导出】→ 选保存位置 → 等进度走完 → 记下 zip 路径。
2. 确认 zip 到手之后，再用 Desktop 设备设置里的服务器切换，切到目标自建实例（`kun` 版本）。切之前如果还没导出过，确认弹窗会提示先去跨环境迁移里导出——这不是硬拦截，关掉提示仍可切换。
3. 完全退出再重开 → 进入目标工作区 → 同一张卡片 → 点【一键导入】→ 选那个 zip → 先看预演报告 → 确认 → 等导入完成。
4. 读导入报告：`secrets_to_fill`（要你去哪儿补哪些密钥）、`runtimes_to_bind`（哪台机器上的哪个运行时绑给了哪个智能体；多候选的在这里一次性点完）、`export_gaps`（源端哪些分组没导出、为什么）、「任务与评论」那一块（任务 / 评论各写入多少、跳过多少，以及哪些行降级了——状态不在目标状态目录、指派人映射不到之类）。

卡片上「导入后激活自动化」「应用工作区设置」「采用 issue 前缀」「自动绑定运行时」四个勾选项的默认值与上文的 CLI 选项表一致；只有第一条会影响「自动化」是否马上开始跑，预演报告里会直接写明导入几条、几条已暂停。

导出侧另有一个「**任务与评论**」勾选项，**默认不勾**，对应 CLI 的 `--include …,issues`。勾上后卡片会就地写明前置：目标工作区必须没有任何任务，请先建一个空工作区——不要等导入被拒了才知道。Desktop 上**没有** `--renumber`：它会打乱编号引用，只留在 CLI 上由确认提示把关。

## 失败排查

| 现象 | 含义 | 怎么办 |
| --- | --- | --- |
| `target_unsupported`（HTTP 404） | 目标不是带 `/transfer/*` 的 `kun` 实例 | 确认切到了自建 `kun`，不是官方云，也不是过旧的上游自建 |
| `transfer_bundle_corrupt` | zip 缺文件或内容校验对不上 | 重新导出一份，不要手工改 zip 里的文件 |
| `transfer_issues_target_not_empty` | 目标工作区已经有任务，任务分组被整包拒收（编号要逐票保真），此次一行都没写 | 新建一个空工作区再导；确实要落进已有任务的工作区，改用 CLI 的 `--renumber`，并接受正文里旧编号会指错任务 |
| `issue_limit_would_exceed` | 目标工作区的任务数会超过配额，一行都没写 | 清掉一些任务或换一个额度更宽的工作区再导 |
| 429 / 503 | 源端限流或暂时不可用 | CLI 会自动退避重试（最多 8 次、间隔翻倍）。一直失败就换个时间再导 |

包坏了不要「修一下再导」。重新导出。

## 迁完长什么样

- 聊天记录、顺序、时间在目标端完整恢复。
- 带任务分组时：任务数与导出报告一致，编号与源端逐一相同（`DENE-365` 还是 `DENE-365`），「我的任务」立刻有内容；评论顺序、作者与父子缩进和源端一致。
- 收件箱不会搬历史通知，但订阅关系会重建，导入之后的新活动照常进收件箱。
- 智能体的续聊指针绑在源机器上，迁不过去；新实例上从新会话开始。
- 密钥、运行时绑定要按报告手工补。
- 降级项要认账：状态不在目标状态目录里的任务会落回默认状态，指派人 / 创建人 / 项目映射不到时降级为未指派、未映射，`@提及` 映射不到时降级成纯文本。报告里「任务与评论」那一块按类列出条数。
