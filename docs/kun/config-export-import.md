# 工作区配置导出 / 导入契约（DENE-202）

本页是 DENE-202「一键导出所有配置，跨工作区完美复刻」的实现契约。Stage 2（后端）与 Stage 3（前端）按本页执行，不再自行决定范围、字段、占位格式或导入顺序。本页只定义契约，不含实现代码。

核对基线：`server/migrations/` 至 `470_agent_runtime_plan_limits`，字段以 `server/pkg/db/generated/models.go` 为准（2026-09-15）。凡本页写「未确认」的地方，Stage 2 实现前必须先验证，不得按猜测实现。

## 0. 一页结论

- 产品形态是「导出 + 导入」两半：`GET .../config/export` 产出一个 JSON bundle，`POST .../config/import` 把 bundle 写进另一个工作区。只导出无法复刻，场景不成立。
- bundle 里**永远没有密钥明文**。秘密字段导出为 `null`，并在 `secrets_omitted` 清单里逐条登记；导入时这些字段留空，绝不用空值覆盖目标工作区已有秘密，UI 明示需人工重填。
- 导入分 `dry_run` 预览和 apply 两段，冲突策略沿用仓库既有词表 `on_conflict: fail | overwrite | rename | skip`（先例：`server/internal/handler/skill.go` 的 `validImportOnConflict`）。
- 原子性边界是「实体类型批次」：每个批次一个应用层事务，失败批次整体回滚，已提交批次不回退。禁止外键与级联（CLAUDE.md 硬规则）。
- 跨实体引用一律用**源工作区 UUID** 表达，导入按固定拓扑顺序「先建实体并记映射表，再回填引用」。无法映射的引用按本页第 4 节的降级表处理，不报错中断。
- V1 只做同一 Multica 实例内的工作区到工作区复刻。跨实例、密钥随包迁移、CLI 子命令、增量同步、导入回滚都不做（第 7 节）。

## 1. 范围清单

结论用三个词：**导出**（整行）、**部分导出**（部分字段或仅清单）、**不导出**。每个实体给出「表名 → 导出字段 → 排除字段」。

通用规则，适用于所有导出实体，表格里不再重复：

- 始终排除：`id`（改为 bundle 内的 `source_id`）、`workspace_id`、`created_at`、`updated_at`。
- 始终排除运行态字段：`last_used_at`、`use_count`、`last_run_at`、`last_fired_at`、`next_run_at`、`status`（智能体的在线状态）。
- 已归档行（`archived_at IS NOT NULL` 或 `status = 'archived'`）默认不导出；`include_archived=true` 时导出并带 `archived: true`。
- 引用其他实体的字段导出为源工作区 UUID，映射规则见第 4 节。
- 引用成员（`user_id`）的字段导出为源 user UUID，只在该用户同时是目标工作区成员时才保留，否则按第 4 节降级。**不导出邮箱、姓名等任何成员资料**。

### 1.1 工作区级

| 表 | 结论 | 导出字段 | 排除字段 | 理由 |
| --- | --- | --- | --- | --- |
| `workspace` | 部分导出 | `settings`（JSON 整体）、`context`、`repos`（`[{url, description, ref}]`）、`issue_prefix`、`attribution_fail_closed`；`name`、`slug`、`description`、`avatar_url` 仅作为 `source` 元数据记录 | `issue_counter`、`id`、`slug`、`name`（不写入目标） | 目标工作区已由用户创建并命名，导入只补配置。`issue_prefix` 只在目标工作区尚无任何任务时才写入，否则跳过并告警。`settings` 已知键只有 `always_redact_env`（布尔），其余键未确认，整体透传。 |
| `issue_status` | 导出 | `key`、`name`、`description`、`category`、`color`、`position`、`is_system` | `archived_at`（转为 `archived` 布尔） | 状态目录是最核心的工作区配置。7 个内置 `key` 在目标工作区一定已存在，导入按 `key` 匹配后只更新 `name/description/color/position`；自定义状态新建。`category` 与 `key` 创建后不可变，与目标冲突时视为冲突。 |
| `issue_label` | 导出 | `name`、`color`、`description`、`resource_type`（`issue`/`agent`/`skill`） | 无 | 三个命名空间各自独立，唯一键是 `(workspace_id, resource_type, lower(name))`。 |
| `issue_property` | 导出 | `name`、`type`、`description`、`icon`、`config.options[{id,name,color}]`、`position` | `archived_at` | 属性定义不含任何成员引用；`actor` / `multi_actor` 只是类型名，成员引用存在任务的属性值里，那是运行态，不在导出范围。`config.options[].id` 原样保留（见「未确认」）。 |
| `quick_action` | 部分导出 | `name`、`description`、`assignee_type`、`assignee_id`（引用）、`prompt`、`visibility`、`status` | `use_count`、`last_used_at`、`created_by_type`、`created_by_id` | 默认只导出 `visibility = 'public'` 的，以及导出者自己创建的 `private`。`created_by_*` 导入时改为导入者。 |
| `issue_view` | 部分导出 | `name`、`scope_type`、`scope_id`（项目引用）、`scope_variant`、`visibility`、`definition_version`、`query`、`display` | `owner_id`、`revision` | 只导出 `visibility = 'workspace'` 的共享视图；`private` 与 `scope_type = 'my'` 是个人视角，不导出。`query` 内含多类引用，映射见 4.6。`owner_id` 导入时为导入者。 |

### 1.2 智能体

分组判定（导出与导入共用，**不得**用内部字段 `kind`）：

- **普通智能体**（进 `entities.agents`）：无 `system_key`。
- **系统智能体**（进 `entities.system_agents`）：`system_key` 非空且不以 `agent_builder:` 开头。
- `agent_builder:` 前缀是隐藏执行载体，不导出。
- `kind` 是内部字段、读接口不暴露（`AgentResponse` 只有 `system_key`），契约不以它作为任何导出判定条件。产品唯一的系统智能体 Mika 在库里是 `kind='user'` + `system_key='mika'`；`kind='system'` 在本 schema 里表示不可见执行载体，不会出现在任何列表接口。

| 表 | 结论 | 导出字段 | 排除字段 | 理由 |
| --- | --- | --- | --- | --- |
| `agent`（无 `system_key`） | 部分导出 | `name`、`description`、`instructions`、`avatar_url`、`runtime_mode`、`runtime_config`（去除 `gateway.token`）、`custom_args`、`model`、`thinking_level`、`service_tier`、`visibility`、`permission_mode`、`max_concurrent_tasks`、`conversation_starters`、`disabled_runtime_skills`、`composio_toolkit_allowlist` | `custom_env`（秘密）、`mcp_config`（秘密）、`runtime_config.gateway.token`（秘密）、`runtime_id`（机器绑定）、`status`、`owner_id`（改为导入者）、`archived_at/archived_by`、`system_key`、`kind` | 本组是无 `system_key` 的普通智能体。读取接口本身就不返回 `custom_env` 明文（`has_custom_env` / `custom_env_key_count`），`mcp_config` 对 agent actor 与 `always_redact_env` 工作区始终脱敏。导出走同一条规则，并为这三项各登记一条 `secrets_omitted`。`runtime_id` 指向 `agent_runtime`（守护进程注册，工作区隔离），导入后智能体处于 `runtime_bound = false`，需重新绑定运行时。`composio_toolkit_allowlist` 只是 slug 列表，本身不是秘密，但生效依赖 owner 的 Composio 连接，导入后自动失效直至导入者自己连接。 |
| `agent`（`system_key` 非空且不以 `agent_builder:` 开头） | 部分导出 | `source_id`、`system_key`、`instructions`、`model`、`thinking_level`、`service_tier`、`conversation_starters`、`disabled_runtime_skills` | 其余全部 | 系统智能体由产品在每个工作区自动创建，不能新建。判定键是 `system_key`，不是内部字段 `kind`。导入按 `system_key` 找到目标工作区同名系统智能体后 patch 上述字段。`source_id`（DENE-442 起）是源工作区的 agent uuid，只给 V3 任务迁移反查身份用：配置导入本身不读它，旧包没有这个字段也照常工作。 |
| `agent_skill` | 导出 | `agent_id`（引用）、`skill_id`（引用）、`enabled` | 无 | 随智能体一起以 `skills: [{skill: <source_id>, enabled}]` 内嵌导出。 |
| `agent_to_label` | 导出 | `agent_id`、`label_id`（均引用） | 无 | 内嵌为 `label_ids`。只允许引用 `resource_type = 'agent'` 的标签。 |
| `agent_mcp_server` | 部分导出 | `agent_id`（引用）、`server_id`（引用，按**名字**回填）、`enabled` | 无 | `workspace_mcp_server` 条目本身不可导出（见 1.6），导入时按名字在目标工作区查找同名条目，找到才绑定，否则记 `unmapped_refs`。 |
| `agent_invocation_target` | 部分导出 | `target_type`、`target_id`（`member` 时为 user 引用；`workspace` 时不导 id） | `created_by` | `workspace` 行导入时 `target_id` 改写为目标工作区 id；`member` 行按成员规则保留或丢弃；`team` 行 V1 本来就不生效，原样导出、原样导入。 |

不导出的智能体相关表：`agent_runtime`（守护进程注册，机器绑定）、`agent_builder_draft`（个人草稿）、`agent_task_queue` / `daemon_connection` / `daemon_token`（运行态与凭据）。

### 1.3 小队

| 表 | 结论 | 导出字段 | 排除字段 | 理由 |
| --- | --- | --- | --- | --- |
| `squad` | 导出 | `name`、`description`、`instructions`、`avatar_url`、`leader_id`（智能体引用） | `creator_id`（改为导入者）、`archived_at/archived_by` | `leader_id` 必须是同工作区智能体，且创建时自动写入一条 `role = 'leader'` 成员行。小队名在工作区内**不唯一**，冲突识别用 `name` 作为身份键（见 5.3）。 |
| `squad_member` | 导出 | `member_type`、`member_id`（智能体或成员引用）、`role` | 无 | 内嵌为 `members`。`member_type = 'member'` 的行按成员规则处理；`role = 'leader'` 的行由创建路径自动生成，导出时跳过以免重复。 |

### 1.4 自动化

| 表 | 结论 | 导出字段 | 排除字段 | 理由 |
| --- | --- | --- | --- | --- |
| `autopilot` | 部分导出 | `title`、`description`、`assignee_type`、`assignee_id`（智能体或小队引用）、`project_id`（项目引用）、`execution_mode`、`issue_title_template`、`priority`、`concurrency_policy`、`status` | `created_by_*`（改为导入者）、`last_run_at`、`pause_reason` | 导入时 `status` 默认落为 `paused`（无论源值），避免导入瞬间在新工作区触发定时任务；`options.activate_autopilots = true` 时才保留源状态。`archived` 的默认不导出。 |
| `autopilot_trigger` | 部分导出 | `kind`、`enabled`、`cron_expression`、`timezone`、`label`、`provider`、`event_filters` | `webhook_token`（秘密，导入时新生成）、`signing_secret`（秘密，write-only，需人工重设）、`next_run_at`、`last_fired_at`、`published_by_*`、`created_by_*` | `webhook_token` 就是 webhook 路径本身，等同凭据。导入后 webhook 触发器拿到新 token 与新 URL，外部系统必须换地址，UI 要提示。`created_by_id` 是定时 / webhook 运行的**执行主体**（MUL-6951），导入时一律写为导入者：导入者对所有导入的触发器负责，这是安全边界，不是可选项。 |
| `autopilot_subscriber` | 导出 | `user_type`、`user_id`（成员引用） | 无 | 内嵌为 `subscribers`，按成员规则保留或丢弃。 |
| `autopilot_collaborator` | 导出 | `user_type`、`user_id`（成员引用） | `granted_by`（改为导入者） | 同上。 |
| `autopilot_rule_version` | 不导出 | — | 全部 | 这是不可变的审计快照（谁在何时发布了什么），属于源工作区的历史。导入走正常创建路径时会由 `service.RecordAutopilotRuleVersion` 自动记录一条新版本，发布者为导入者。 |

不导出：`autopilot_run`、`autopilot_quota_*`、`webhook_delivery`（运行态）。

### 1.5 项目与技能

| 表 | 结论 | 导出字段 | 排除字段 | 理由 |
| --- | --- | --- | --- | --- |
| `project` | 导出 | `title`、`description`、`icon`、`status`、`priority`、`lead_type`、`lead_id`（成员或智能体引用）、`start_date`、`due_date` | 无 | 项目是配置也是容器；任务不导出，项目壳导出。`status IN ('completed','cancelled')` 的默认不导出（视同归档）。 |
| `project_resource`（`github_repo`） | 导出 | `resource_type`、`resource_ref`（`{url, default_branch_hint, ref}`）、`label`、`position` | `created_by` | 纯 URL 引用，可移植。 |
| `project_resource`（`local_directory`） | 部分导出 | 同上，`resource_ref = {local_path, daemon_id, label, execution_mode}` | `created_by` | `daemon_id` 绑定某台机器上的守护进程注册；导入时只有目标工作区能看到同一 `daemon_id` 的运行时才写入，否则跳过并记 `unmapped_refs`（见「未确认」）。 |
| `skill`（`plugin_installation_id IS NULL`） | 导出 | `name`、`description`、`content`、`config` | `created_by`（改为导入者）、`plugin_installation_id` | 与现有技能导入共用创建路径和冲突词表。 |
| `skill`（`plugin_installation_id IS NOT NULL`） | 不导出 | — | 全部 | 插件安装时贡献的技能归插件所有，卸载会整体删除；应随插件重装出现，不能当人写的技能复制。 |
| `skill_file` | 导出 | `path`、`content` | 无 | 内嵌为 `files`。唯一键 `(skill_id, path)`。 |
| `skill_to_label` | 导出 | `skill_id`、`label_id`（均引用） | 无 | 内嵌为 `label_ids`，只允许 `resource_type = 'skill'` 的标签。 |

### 1.6 连接与集成

这一组统一结论：**不导出实体，只导出「需要重新连接」清单**（bundle 的 `integrations` 段）。理由：每一项都含 token / OAuth 安装 / 外部租户身份，且授权对象是源工作区。

| 表 | 结论 | 清单里保留的字段 | 排除字段 | 理由 |
| --- | --- | --- | --- | --- |
| `workspace_mcp_server` | 部分导出（仅名字） | `name`、`transport`（由 `config.type`/`command`/`url` 推导，同 `mcpTransportOf`） | `config`（整体，含 command / args / env / url / headers） | 条目 write-only，读接口只回 `name` 与 `transport`。导入**不创建**条目，只在 `secrets_to_fill` 列出，用户手工重建同名条目后再次导入即可让 `agent_mcp_server` 绑定生效。 |
| `vcs_connection` | 部分导出（仅清单） | `provider`、`instance_url`、`account_login` | `access_token_encrypted`、`webhook_secret_encrypted`、`connected_by_id` | 清单只用于 UI 展示「需重新连接 Forgejo/Gitea/GitLab: 某账号」。 |
| `github_installation` | 部分导出（仅清单） | `account_login`、`account_type` | `installation_id`、`account_avatar_url`、`connected_by_id` | GitHub App 安装授权绑定源工作区，必须重新走 connect。 |
| `channel_installation` | 部分导出（仅清单） | `channel_type`、`agent_id`（智能体引用） | `config`（含 app_secret / token）、`ws_lease_*`、`installer_user_id` | 渠道（feishu / slack / wecom / dingtalk / telegram）安装含凭据与外部租户绑定。 |
| `lark_installation` | 不导出 | — | 全部 | 旧表，查询层已改走 `channel_installation`，仅删除工作区时清理。 |

### 1.7 插件

| 表 | 结论 | 导出字段 | 排除字段 | 理由 |
| --- | --- | --- | --- | --- |
| `plugin_installation` | 部分导出（仅清单） | `plugin_key`、`version`、`enabled`、`granted_scopes`、`config` | `manifest`、`token_hash`、`token_rotated_at`、`mcp_approvals`、`package_version_id`、`installed_by` | `config` 里 `type = secret` 的字段在写入时就被拆到 `plugin_secret`，所以 `config` 列本身不含秘密。但安装依赖目标工作区已发布同 `plugin_key + version` 的 `plugin_package_version`，且 `mcp_approvals` 是一次同意行为、`token` 是凭据，V1 **不自动安装**，只在 `plugins_to_reinstall` 列出并附 `config` 供 UI 回填。 |
| `plugin_secret` | 不导出 | — | 全部 | 密文，无任何端点返回。 |
| `plugin_package` / `plugin_package_version` / `plugin_package_file` | 不导出 | — | 全部 | 工作区私有发布物，含文件内容，体积不可控；V2 再评估。 |
| `plugin_storage` / `plugin_invocation` / `plugin_hook_schedule` | 不导出 | — | 全部 | 运行态。 |

Issue 原文列的 `plugin_installation_config` 与 `plugin_grant` 两张表已在迁移 `344_plugin_v2_reset` 中删除，现行模型里不存在，本页不再涉及。

### 1.8 明确排除（含理由）

| 表 | 理由 |
| --- | --- |
| `personal_access_token` | 用户级凭据（`token_hash`），与工作区无关。 |
| `notification_preference`、`issue_view_preference`、`pinned_item`、`chat_pinned_agent` | 个人偏好，按 `user_id` 归属，复刻配置不应复制别人的偏好。 |
| `user_composio_connection` | 用户与 Composio 的 OAuth 连接，含外部账号 id。 |
| `runtime_profile` | 自定义运行时定义（命令名、固定参数）描述的是守护进程宿主机上的可执行文件，并通过 `agent_runtime.profile_id` 与机器绑定；导入后的智能体本来就未绑定运行时，先导 profile 没有意义。V2 若要支持，只导 `display_name / protocol_family / command_name / fixed_args / visibility`。 |
| `daemon_*`、`agent_runtime`、`daemon_token`、`daemon_connection` | 守护进程注册与凭据，机器绑定。 |
| `workspace_share_link`、`workspace_invitation`、`member` | 邀请码与成员关系是目标工作区自己的事；成员不能被「导入」。 |
| 运行态：`issue*`、`comment*`、`task_*`、`agent_task_queue`、`inbox_item`、`activity_log`、`attachment`、`chat_*`、`github_pull_request*`、`vcs_pull_request`、`issue_vcs_pull_request`、`vcs_commit_status`、`webhook_delivery`、`autopilot_run`、`*_usage*`、`seat_capacity_outbox`、`sys_cron_execution`、`channel_*`（除 installation 清单）、`lark_*`、`dingtalk_*`、`verification_code`、`feedback`、`contact_sales_inquiry` | 不是配置。 |

## 2. 秘密边界

### 2.1 秘密字段清单

导出实现必须**在 SQL 层就不 SELECT** 这些列（结构性保证），而不是查出来再抹掉：

| 表.字段 | 性质 | 导出处理 |
| --- | --- | --- |
| `agent.custom_env` | 环境变量键值，读接口本不返回明文 | 不读取。导出 `custom_env: null`，登记 `secrets_omitted`，附 `key_count`（沿用 `custom_env_key_count`，只给数量不给键名） |
| `agent.mcp_config` | MCP 配置（含 env / headers） | 不读取。`mcp_config: null`，登记 |
| `agent.runtime_config.gateway.token` | OpenClaw 网关 token | 读取 `runtime_config` 后删除 `gateway.token` 键，登记；不得输出现有掩码 `***` |
| `workspace_mcp_server.config` | 整个条目 write-only | 只读 `name` 与推导的 `transport` |
| `autopilot_trigger.webhook_token` | webhook 路径凭据 | 不读取，登记 |
| `autopilot_trigger.signing_secret` | HMAC 明文 | 不读取，登记 |
| `vcs_connection.access_token_encrypted`、`webhook_secret_encrypted` | 加密凭据 | 不读取 |
| `channel_installation.config`、`ws_lease_token` | app_secret / bot token / 租约 | 不读取 |
| `github_installation.installation_id` | 外部安装凭据标识 | 不读取 |
| `plugin_installation.token_hash`、`plugin_secret.*` | 凭据 | 不读取 |
| `personal_access_token.*`、`daemon_token.*` | 凭据 | 表整体不在范围 |

防御纵深：序列化 bundle 前对整个对象树跑一次键名清洗，键名（不区分大小写）命中 `token`、`secret`、`password`、`passwd`、`api_key`、`apikey`、`authorization`、`private_key`、`credential` 的叶子一律置 `null` 并登记 `secrets_omitted`（`reason: "key_name_denylist"`）。这一层是兜底，不能替代上表的结构性排除。

### 2.2 统一占位格式

秘密字段在实体里的值固定为 JSON `null`，不用 `"__secret__"` 之类字符串标记，原因：字符串标记有被当成真实值写回的风险（现有 `runtime_config.gateway.token` 的 `***` 掩码就需要 `preserveMaskedGatewayToken` 专门兜底）。`null` 在导入侧天然表示「没有值」。

登记表 `secrets_omitted` 每项：

```json
{
  "entity": "agent",
  "source_id": "01a0a3f2-0000-7000-8000-000000000a01",
  "name": "主力工作-贝吉塔",
  "field": "custom_env",
  "reason": "secret_material",
  "hint": { "key_count": 3 }
}
```

`reason` 枚举：`secret_material`（结构性排除）、`write_only`（如 MCP 库条目）、`key_name_denylist`（兜底清洗）。`hint` 只允许放数量、传输类型这类非敏感提示，**不允许放键名、前缀、长度以外的任何片段**。

### 2.3 导入时的处理

- 秘密字段为 `null` 时**跳过该字段**，不写入。新建实体时该列取数据库默认值；`overwrite` 已有实体时**保留目标现有值**，绝不用 `null` 覆盖。
- `secrets_omitted` 与 `integrations`、`plugins_to_reinstall` 在导入报告里合并为 `secrets_to_fill`，每项带目标实体 id 与深链路径（例如 `/{slug}/agents/{id}/settings`），UI 在导入完成页逐条列出「需人工重填」。
- webhook 触发器导入时由创建路径生成新 `webhook_token`；报告里返回新的 `webhook_url`，并标注 `signing_secret` 需重设。
- 导入接口自身**不接受**任何秘密字段：请求体里若出现非 `null` 的 `custom_env` / `mcp_config` / `signing_secret` / `webhook_token` / `gateway.token`，返回 400 `config_bundle_contains_secret`。秘密只能走各自现有的专用端点（`PUT /api/agents/{id}/env` 等）。

### 2.4 验收断言

Stage 2 必须落地以下测试，任一失败即阻断合并：

1. **金丝雀测试**：在源工作区创建一个智能体，`custom_env` 含值 `CANARY_ENV_9f3a`，`mcp_config` 含 `CANARY_MCP_7b1c`，`runtime_config.gateway.token = CANARY_GW_2e8d`；创建一个 webhook 触发器并设置 `signing_secret = CANARY_SIG_4c6f`；创建一个 MCP 库条目，`config.env.KEY = CANARY_MCPLIB_5d2a`。调用导出，对响应体做子串断言：五个金丝雀均不出现，`***` 也不出现。
2. **登记完整性**：上述每个秘密字段在 `secrets_omitted` 里恰有一条记录。
3. **不覆盖**：目标工作区已有同名智能体且 `custom_env` 非空时，用 `on_conflict: overwrite` 导入，断言目标 `custom_env_key_count` 不变。
4. **拒收秘密**：请求体带非 `null` `custom_env` 时返回 400 且不产生任何写入。

## 3. bundle 结构

### 3.1 顶层

```json
{
  "format": "multica.workspace-config",
  "schema_version": 1,
  "bundle_id": "01a0a3f2-1111-7000-8000-0000000000b0",
  "exported_at": "2026-09-15T08:30:00Z",
  "source": {
    "workspace_id": "41a8b48e-4c0b-498a-9097-bb694f80b4b9",
    "slug": "deneb",
    "name": "Deneb",
    "issue_prefix": "DENE",
    "server_version": "v0.42.3",
    "exported_by": "c924599a-9548-4fc1-9146-a3645ecbb0c6"
  },
  "options": { "include_archived": false },
  "entities": { "...": "见 3.2" },
  "integrations": [ "...见 3.3" ],
  "plugins_to_reinstall": [ "...见 3.3" ],
  "secrets_omitted": [ "...见 2.2" ],
  "stats": { "agents": 5, "skills": 12, "labels": 9 }
}
```

- `format` 固定字符串，`schema_version` 整数从 1 起；导入端对不识别的 `schema_version` 返回 400 `config_bundle_version_unsupported`，不做降级解析。
- `bundle_id` 每次导出新生成，只用于日志与幂等追踪，不参与匹配。
- `source.exported_by` 是 user id，用于「导出者自己的 private quick action」这类规则；不含邮箱。
- 顶层与各实体对象都允许出现未知键，导入端忽略（前向兼容）；但不允许缺少必填键。

### 3.2 `entities` 分组

每组是数组，元素统一携带 `source_id`（源工作区 UUID，作为映射表键）与 `archived`（布尔，默认 false）。引用字段直接放源 UUID，成员引用也放源 user UUID；多态引用统一为 `{ "type": "agent" | "squad" | "member", "id": "<uuid>" }`。分组与写入顺序一致（第 4.2 节）：

```
entities.workspace            单对象，不是数组
entities.labels[]             issue_label
entities.issue_statuses[]     issue_status
entities.issue_properties[]   issue_property
entities.skills[]             skill + files[] + label_ids[]
entities.mcp_servers[]        workspace_mcp_server，仅 name/transport，导入不创建
entities.agents[]             无 system_key 的普通智能体 + skills[] + label_ids[] + mcp_servers[] + invocation_targets[]
entities.system_agents[]      system_key 非空且不以 agent_builder: 开头的可 patch 字段
entities.squads[]             squad + members[]
entities.projects[]           project + resources[]
entities.autopilots[]         autopilot + triggers[] + subscribers[] + collaborators[]
entities.quick_actions[]      quick_action
entities.issue_views[]        issue_view
```

### 3.3 `integrations` 与 `plugins_to_reinstall`

```json
"integrations": [
  { "kind": "github_installation", "account_login": "jeff-kunkun", "account_type": "User" },
  { "kind": "vcs_connection", "provider": "gitea", "instance_url": "https://git.example.com", "account_login": "kun" },
  { "kind": "channel_installation", "channel_type": "feishu", "agent": "01a0a3f2-0000-7000-8000-000000000a01" },
  { "kind": "workspace_mcp_server", "name": "github-mcp", "transport": "stdio" }
],
"plugins_to_reinstall": [
  { "plugin_key": "acme.deploy", "version": "1.4.0", "enabled": true,
    "granted_scopes": ["issues:read", "comments:write"],
    "config": { "region": "cn-north" } }
]
```

### 3.4 示例 bundle

下面是一段真实感的裁剪示例。`agents[0]` 同时带引用（技能、标签、MCP 库条目）与秘密占位；`autopilots[0]` 带多态引用与 webhook 触发器占位。

```json
{
  "format": "multica.workspace-config",
  "schema_version": 1,
  "bundle_id": "01a0a3f2-1111-7000-8000-0000000000b0",
  "exported_at": "2026-09-15T08:30:00Z",
  "source": {
    "workspace_id": "41a8b48e-4c0b-498a-9097-bb694f80b4b9",
    "slug": "deneb", "name": "Deneb", "issue_prefix": "DENE",
    "server_version": "v0.42.3",
    "exported_by": "c924599a-9548-4fc1-9146-a3645ecbb0c6"
  },
  "options": { "include_archived": false },
  "entities": {
    "workspace": {
      "settings": { "always_redact_env": true },
      "context": "私有魔改平台工作区。main 为上游镜像，kun 为魔改主线。",
      "repos": [ { "url": "https://github.com/jeff-kunkun/multica", "ref": "kun" } ],
      "issue_prefix": "DENE",
      "attribution_fail_closed": false
    },
    "labels": [
      { "source_id": "01a0a3f2-0000-7000-8000-0000000001a1", "resource_type": "agent", "name": "builder", "color": "#3b82f6", "description": "" },
      { "source_id": "01a0a3f2-0000-7000-8000-0000000001a2", "resource_type": "skill", "name": "review", "color": "#22c55e", "description": "" },
      { "source_id": "01a0a3f2-0000-7000-8000-0000000001a3", "resource_type": "issue", "name": "bug", "color": "#ef4444", "description": "" }
    ],
    "issue_statuses": [
      { "source_id": "01a0a3f2-0000-7000-8000-0000000005a1", "key": "todo", "name": "Todo", "description": "", "category": "todo", "color": "#6b7280", "position": 2, "is_system": true },
      { "source_id": "01a0a3f2-0000-7000-8000-0000000005a2", "key": "verifying", "name": "验证中", "description": "本机可验证项已交付", "category": "in_review", "color": "#a855f7", "position": 4.5, "is_system": false }
    ],
    "issue_properties": [
      { "source_id": "01a0a3f2-0000-7000-8000-0000000009a1", "name": "Tier", "type": "select", "description": "", "icon": "layers", "position": 1,
        "config": { "options": [ { "id": "opt_t1", "name": "T1", "color": "#22c55e" }, { "id": "opt_t3", "name": "T3", "color": "#ef4444" } ] } },
      { "source_id": "01a0a3f2-0000-7000-8000-0000000009a2", "name": "Owner", "type": "actor", "description": "", "icon": "user", "position": 2, "config": {} }
    ],
    "skills": [
      { "source_id": "01a0a3f2-0000-7000-8000-000000000ca1", "name": "code-review", "description": "沿 Standards 与 Spec 双轴审查", "content": "---\nname: code-review\n---\n...",
        "config": {}, "label_ids": [ "01a0a3f2-0000-7000-8000-0000000001a2" ],
        "files": [ { "path": "references/standards.md", "content": "# Standards\n..." } ] }
    ],
    "mcp_servers": [
      { "source_id": "01a0a3f2-0000-7000-8000-000000000da1", "name": "github-mcp", "transport": "stdio" }
    ],
    "agents": [
      { "source_id": "01a0a3f2-0000-7000-8000-000000000a01",
        "name": "主力工作-贝吉塔", "description": "常规实现", "instructions": "你是工作区的主力实现者……",
        "avatar_url": null, "runtime_mode": "local",
        "runtime_config": { "mode": "gateway", "gateway": { "url": "http://127.0.0.1:18789", "token": null } },
        "custom_args": [ "--permission-mode", "bypassPermissions" ],
        "custom_env": null, "mcp_config": null,
        "model": "claude-opus-5", "thinking_level": "high", "service_tier": null,
        "visibility": "workspace", "permission_mode": "public_to", "max_concurrent_tasks": 2,
        "conversation_starters": [ { "label": "修一个 bug", "prompt": "帮我定位并修复……" } ],
        "disabled_runtime_skills": [], "composio_toolkit_allowlist": [],
        "skills": [ { "skill": "01a0a3f2-0000-7000-8000-000000000ca1", "enabled": true } ],
        "label_ids": [ "01a0a3f2-0000-7000-8000-0000000001a1" ],
        "mcp_servers": [ { "server": "01a0a3f2-0000-7000-8000-000000000da1", "enabled": true } ],
        "invocation_targets": [ { "target_type": "workspace", "target_id": null }, { "target_type": "member", "target_id": "c924599a-9548-4fc1-9146-a3645ecbb0c6" } ] }
    ],
    "system_agents": [
      { "source_id": "01a0ab1b-...", "system_key": "mika", "instructions": "本工作区备注：优先中文回复。", "model": null, "thinking_level": null, "service_tier": null, "conversation_starters": [], "disabled_runtime_skills": [] }
    ],
    "squads": [
      { "source_id": "01a0a3f2-0000-7000-8000-000000000ea1", "name": "龙珠小队", "description": "", "instructions": "", "avatar_url": null,
        "leader_id": "01a0a3f2-0000-7000-8000-000000000a01",
        "members": [ { "member_type": "member", "member_id": "c924599a-9548-4fc1-9146-a3645ecbb0c6", "role": "决策人" } ] }
    ],
    "projects": [
      { "source_id": "01a0a3f2-0000-7000-8000-000000000fa1", "title": "Multica 魔改", "description": "私有深度魔改", "icon": null, "status": "in_progress", "priority": "high",
        "lead": { "type": "member", "id": "c924599a-9548-4fc1-9146-a3645ecbb0c6" }, "start_date": null, "due_date": null,
        "resources": [
          { "resource_type": "github_repo", "resource_ref": { "url": "https://github.com/jeff-kunkun/multica", "ref": "kun" }, "label": null, "position": 0 },
          { "resource_type": "local_directory", "resource_ref": { "local_path": "/Users/kun/.agents/multica", "daemon_id": "01a095c6-3636-7079-a5c5-6ec659c21e53", "execution_mode": "worktree" }, "label": null, "position": 1 }
        ] }
    ],
    "autopilots": [
      { "source_id": "01a0a3f2-0000-7000-8000-0000000004a1", "title": "夜间巡检", "description": "跑一遍 lint 并汇报",
        "assignee": { "type": "squad", "id": "01a0a3f2-0000-7000-8000-000000000ea1" },
        "project_id": "01a0a3f2-0000-7000-8000-000000000fa1",
        "execution_mode": "create_issue", "issue_title_template": "巡检 {{date}}", "priority": "medium", "concurrency_policy": "skip", "status": "active",
        "triggers": [
          { "kind": "schedule", "enabled": true, "cron_expression": "0 2 * * *", "timezone": "Asia/Shanghai", "label": "每晚 2 点", "provider": "generic", "event_filters": null },
          { "kind": "webhook", "enabled": true, "cron_expression": null, "timezone": null, "label": "CI 完成", "provider": "github",
            "event_filters": [ { "event": "workflow_run", "actions": [ "completed" ] } ],
            "webhook_token": null, "signing_secret": null }
        ],
        "subscribers": [ { "user_type": "member", "user_id": "c924599a-9548-4fc1-9146-a3645ecbb0c6" } ],
        "collaborators": [] }
    ],
    "quick_actions": [
      { "source_id": "01a0a3f2-0000-7000-8000-0000000006a1", "name": "请审查", "description": "", "assignee": { "type": "agent", "id": "01a0a3f2-0000-7000-8000-000000000a01" },
        "prompt": "请按 code-review 流程审查本任务的改动。", "visibility": "public", "status": "active" }
    ],
    "issue_views": [
      { "source_id": "01a0a3f2-0000-7000-8000-0000000007a1", "name": "T3 待验证", "scope_type": "workspace", "scope_id": null, "scope_variant": null,
        "visibility": "workspace", "definition_version": 1,
        "query": { "statusFilters": [ "verifying" ], "labelFilters": [ "01a0a3f2-0000-7000-8000-0000000001a3" ],
                   "assigneeFilters": [ { "type": "agent", "id": "01a0a3f2-0000-7000-8000-000000000a01" } ],
                   "propertyFilters": { "01a0a3f2-0000-7000-8000-0000000009a1": [ "opt_t3" ] } },
        "display": { "layout": "list", "groupBy": "status" } }
    ]
  },
  "integrations": [
    { "kind": "github_installation", "account_login": "jeff-kunkun", "account_type": "User" },
    { "kind": "channel_installation", "channel_type": "feishu", "agent": "01a0a3f2-0000-7000-8000-000000000a01" },
    { "kind": "workspace_mcp_server", "name": "github-mcp", "transport": "stdio" }
  ],
  "plugins_to_reinstall": [],
  "secrets_omitted": [
    { "entity": "agent", "source_id": "01a0a3f2-0000-7000-8000-000000000a01", "name": "主力工作-贝吉塔", "field": "custom_env", "reason": "secret_material", "hint": { "key_count": 3 } },
    { "entity": "agent", "source_id": "01a0a3f2-0000-7000-8000-000000000a01", "name": "主力工作-贝吉塔", "field": "mcp_config", "reason": "secret_material" },
    { "entity": "agent", "source_id": "01a0a3f2-0000-7000-8000-000000000a01", "name": "主力工作-贝吉塔", "field": "runtime_config.gateway.token", "reason": "secret_material" },
    { "entity": "workspace_mcp_server", "source_id": "01a0a3f2-0000-7000-8000-000000000da1", "name": "github-mcp", "field": "config", "reason": "write_only", "hint": { "transport": "stdio" } },
    { "entity": "autopilot_trigger", "source_id": "01a0a3f2-0000-7000-8000-0000000004a1", "name": "夜间巡检 / CI 完成", "field": "webhook_token", "reason": "secret_material" },
    { "entity": "autopilot_trigger", "source_id": "01a0a3f2-0000-7000-8000-0000000004a1", "name": "夜间巡检 / CI 完成", "field": "signing_secret", "reason": "secret_material" }
  ],
  "stats": { "labels": 3, "issue_statuses": 2, "issue_properties": 2, "skills": 1, "agents": 1, "system_agents": 1, "squads": 1, "projects": 1, "autopilots": 1, "quick_actions": 1, "issue_views": 1 }
}
```

## 4. ID 重映射规则

### 4.1 映射表

导入过程维护一张内存映射表 `map[(entity_type, source_id)] -> target_id`，实体每创建或匹配成功一条就登记。V1 不把映射表持久化到数据库（不新增表）；重复导入的幂等性靠第 5.3 节的身份键，不靠映射表。

成员引用不经过映射表：源 user id 与目标 user id 是同一个（`user` 表不按工作区隔离），只需检查该 user 是否是目标工作区成员（`member` 表）。

### 4.2 写入顺序（拓扑）

引用关系没有环，按下列顺序逐批写入即可；每批内部先建主行、记映射，再写引用与关联表（「先建实体并记映射表、再回填引用」）：

1. `workspace` 补丁（settings / context / repos / issue_prefix）
2. `labels`（无引用）
3. `issue_statuses`（无引用）
4. `issue_properties`（无引用）
5. `skills` → 主行；回填 `skill_to_label`（引用 2）、`skill_file`
6. `mcp_servers` → 不创建，只按 `name` 查目标工作区现有条目并登记映射
7. `agents` → 主行（owner = 导入者，`runtime_id` 为空）；回填 `agent_skill`（引用 5）、`agent_to_label`（引用 2）、`agent_mcp_server`（引用 6）、`agent_invocation_target`
8. `system_agents` → 按 `system_key` patch
9. `squads` → 主行（`leader_id` 引用 7）；回填 `squad_member`（引用 7 或成员）
10. `projects` → 主行（`lead` 引用 7 或成员）；回填 `project_resource`
11. `autopilots` → 主行（`assignee` 引用 7 或 9，`project_id` 引用 10）；回填 `autopilot_trigger`、`autopilot_subscriber`、`autopilot_collaborator`
12. `quick_actions`（`assignee` 引用 7 或 9）
13. `issue_views`（`scope_id` 引用 10，`query` 引用 2 / 7 / 10 / 4）

### 4.3 引用字段总表

| 引用位置 | 指向 | 映射方式 | 无法映射时的降级 |
| --- | --- | --- | --- |
| `agent.skills[].skill` | skill | 映射表 | 丢弃该绑定，记 `unmapped_refs` |
| `agent.label_ids[]` / `skill.label_ids[]` | label（对应 `resource_type`） | 映射表 | 丢弃该标签，记录 |
| `agent.mcp_servers[].server` | workspace_mcp_server | 按 `name` 匹配目标现有条目 | 丢弃绑定，记录并在 `secrets_to_fill` 提示先重建 MCP 条目再重导 |
| `agent.invocation_targets[]` `workspace` | 工作区 | 改写为目标 `workspace_id` | — |
| `agent.invocation_targets[]` `member` | 成员 | 成员规则 | 丢弃该行；若丢弃后 `permission_mode = public_to` 且无任何 target，保持 `public_to` 且无 target（等价于无人可调用），报告告警 |
| `agent.invocation_targets[]` `team` | 保留字段 | 原样写入 | — |
| `squad.leader_id` | agent | 映射表 | **整条小队跳过**（leader 必填），记录 |
| `squad.members[]` `agent` | agent | 映射表 | 丢弃该成员 |
| `squad.members[]` `member` | 成员 | 成员规则 | 丢弃该成员 |
| `project.lead` | agent 或成员 | 映射表 / 成员规则 | `lead_type/lead_id` 置空 |
| `project.resources[]` `local_directory.daemon_id` | 守护进程注册 | 目标工作区 `agent_runtime.daemon_id` 存在才保留 | 跳过该资源，记录 |
| `autopilot.assignee` | agent 或 squad | 映射表 | **整条自动化跳过**（assignee 必填），记录 |
| `autopilot.project_id` | project | 映射表 | 置空 |
| `autopilot.subscribers[] / collaborators[]` | 成员 | 成员规则 | 丢弃该行 |
| `autopilot.triggers[].created_by_id` | 成员 | 固定写导入者 | — |
| `quick_action.assignee` | agent 或 squad | 映射表 | **整条跳过**，记录 |
| `issue_view.scope_id` | project | 映射表 | **整条视图跳过**（project 视图无 project 无意义） |
| `issue_view.query.labelFilters[]` | label | 映射表 | 从数组移除该项 |
| `issue_view.query.projectFilters[]` | project | 映射表 | 移除该项 |
| `issue_view.query.assigneeFilters[] / creatorFilters[]` | `{type: agent}` 映射表；`{type: member}` 成员规则 | 见左 | 移除该项 |
| `issue_view.query.propertyFilters{<property_id>: [...]}` | property（键）；值内的 option id 原样保留 | 映射表（键） | 移除该键 |
| `issue_view.query.statusFilters[] / priorityFilters[]` | 状态 key / 优先级 | 不需映射（稳定字符串） | — |
| `issue_property.config.options[].id` | 属性内部 | 原样保留（见「未确认」） | — |

### 4.4 成员规则

一个源 user id 在导入时按以下顺序判定：

1. 是目标工作区成员 → 原样保留。
2. 不是 → 按 4.3 表的降级列处理，并在报告 `unmapped_refs` 里记一条 `{ entity, source_id, field, ref_type: "member", ref_id }`。**报告里不回显该用户的任何资料**，只回显 id。

「导入者」指发起 import 请求的 user，所有 `owner_id` / `creator_id` / `created_by_*` / `granted_by` / `installed_by` 一律写为导入者，不尝试还原源工作区的创建者。

### 4.5 `issue_property` 的 `actor` 类型

属性**定义**（`issue_property` 行）不含成员引用，`actor` / `multi_actor` 只是 `type` 的取值，直接导出导入。成员引用只出现在任务的属性**值**（`member:<user_id>`）里，任务是运行态、不导出，因此跨工作区不成立的问题在 V1 范围内不出现。视图 `propertyFilters` 里若对 actor 属性设了成员值，按 4.3 的 member 规则处理。

### 4.6 视图 `query` 的处理边界

服务端把 `query` 当不透明 JSON 存储，解释权在客户端（`definition_version`）。导入端只改写 4.3 表列出的键，其余键原样透传；遇到未知键不报错。改写后若某个数组被清空，保留空数组而不是删除键，避免客户端把「没有过滤」和「过滤集为空」混淆。

## 5. 导入语义

### 5.1 两段式

- `dry_run: true`：完整跑一遍解析、映射与冲突检测，**不开事务、不写库**，返回与 apply 完全相同结构的报告，`applied: false`。UI 的预览页只消费这个报告。
- `dry_run: false`：按 4.2 顺序逐批写入，返回报告，`applied: true`。
- 两次调用之间目标工作区可能被别人改动，apply 不复用 dry_run 的结果，重新计算；报告里逐项给出实际动作。

### 5.2 冲突策略

请求级 `on_conflict`，默认 `fail`，沿用技能导入的四个值：

| 值 | 命中身份键时 |
| --- | --- |
| `fail` | 在 dry_run 阶段就把所有冲突汇总返回 409 `config_import_conflict`；apply 时任何批次首个冲突即回滚该批次并停止后续批次 |
| `overwrite` | 就地更新目标实体的可导出字段（秘密字段除外，见 2.3），关联表按「以 bundle 为准」重写（先删后建） |
| `rename` | 新建，名字加 `-2`、`-3`……后缀直到可用（与 `createRenamedImportedSkill` 一致，上限沿用 `maxImportRenameAttempts`）；不适用于 `issue_status`（key 不可改名）与 `system_agents`（只能 patch），这两类在 `rename` 下退化为 `skip` |
| `skip` | 跳过该实体，但**仍登记映射**（源 id → 目标已存在的实体），后续引用照常解析 |

V1 不支持按实体类型分别指定策略；`include` 数组可以把整类实体排除在导入之外。

**例外：平台自带的系统状态不算冲突**（DENE-408）。创建一个工作区时会 seed 7 条 `is_system = true` 的状态（`backlog` / `todo` / …，键即 category），源端导出里也带着它们，所以导入到一个**全新空工作区**时 `issue_statuses` 批次必然在第一行就撞上同名同 key 的行。这种「源与目标都是内置行」的配对不按冲突处理：`fail` 下记为 `skipped`（目标那 7 条名字、颜色、描述本来就与源端一致，跳过等于 overwrite 会写下的结果），`overwrite` / `skip` / `rename` 行为不变。这样 CLI 默认的 `fail` 不会被空目标误触发，护栏只对真正的用户数据（同名 label / agent / skill 等）生效。

### 5.3 身份键（判定「同一个实体」）

| 实体 | 身份键 | 备注 |
| --- | --- | --- |
| label | `(resource_type, lower(name))` | 数据库唯一索引 |
| issue_status | `key` | 数据库唯一索引；`category` 不同视为冲突且不可 overwrite（返回 `failed`） |
| issue_property | `lower(name)` | 数据库唯一索引；`type` 不同视为冲突且不可 overwrite |
| skill | `name` | 与技能导入一致 |
| workspace_mcp_server | `name` | 数据库唯一索引，只用于查找 |
| agent | `name` | 数据库唯一约束 |
| system agent | `system_key` | 永远是 overwrite 语义（patch） |
| squad | `name` | 数据库不唯一；导入用名字判定，同名多条时取最早创建的一条 |
| project | `title` | 同上 |
| autopilot | `title` | 同上 |
| quick_action | `name` | 同上 |
| issue_view | `(scope_type, scope_id, name)` 且 `visibility = workspace` | 同上 |

### 5.4 原子性边界

- 一个实体类型批次 = 一个应用层事务（`pgx.Tx`），批内主行与关联表一起提交或一起回滚。
- 批次失败（数据库错误、约束冲突、`fail` 策略命中）：回滚本批，**不再执行后续批次**，已提交的前序批次保留。响应 422 `config_import_partial_failure`，报告里每个批次带 `batch_status: committed | rolled_back | not_attempted`。
- 不使用外键与级联；关联表清理（overwrite 时的先删后建）在同一事务内显式执行。
- 恢复路径就是**用同一个 bundle 再导一次**：已提交实体被身份键识别为已存在，按当时的 `on_conflict` 处理；未执行批次照常写入。V1 不提供撤销。
- 单个实体级别的软失败（引用无法映射、成员不在目标工作区）**不**导致批次失败，只记入报告。

### 5.5 幂等性

同一 bundle 重复导入的结果：

- `skip`：第二次全部 `skipped`，目标不变。这是推荐的「补跑」策略（例如先手工重建 MCP 条目，再重导让绑定生效）。
- `overwrite`：第二次全部 `updated`，字段值与第一次一致，关联表重写后集合相同；秘密字段保持目标现值。
- `rename`：第二次会产生 `-2` 副本，这是策略的定义，不是 bug；UI 在 `rename` 下要提示这一点。
- webhook 触发器在 `overwrite` 下**不**重新生成 `webhook_token`（保留目标现值），只更新其他字段。

### 5.6 导入报告结构

```json
{
  "applied": false,
  "bundle_id": "01a0a3f2-1111-7000-8000-0000000000b0",
  "on_conflict": "skip",
  "batches": [
    { "entity_type": "labels", "batch_status": "committed",
      "items": [
        { "source_id": "…1a1", "name": "builder", "action": "created", "target_id": "…" },
        { "source_id": "…1a3", "name": "bug", "action": "skipped", "target_id": "…", "reason": "exists" }
      ] }
  ],
  "unmapped_refs": [
    { "entity": "squad", "source_id": "…ea1", "field": "members[0].member_id", "ref_type": "member", "ref_id": "c924599a-…", "resolution": "dropped" }
  ],
  "secrets_to_fill": [
    { "entity": "agent", "target_id": "…", "name": "主力工作-贝吉塔", "field": "custom_env", "path": "/new-slug/agents/…/settings" },
    { "entity": "workspace_mcp_server", "name": "github-mcp", "field": "config", "path": "/new-slug/settings/mcp" },
    { "entity": "autopilot_trigger", "target_id": "…", "name": "夜间巡检 / CI 完成", "field": "signing_secret", "path": "/new-slug/autopilots/…", "webhook_url": "https://…/webhooks/…" }
  ],
  "warnings": [
    { "code": "autopilots_imported_paused", "count": 1 },
    { "code": "issue_prefix_skipped_target_has_issues" }
  ],
  "stats": { "created": 9, "updated": 0, "renamed": 0, "skipped": 1, "failed": 0 }
}
```

`action` 枚举：`created | updated | renamed | skipped | failed`。`reason` 是稳定 code（`exists`、`assignee_unmapped`、`leader_unmapped`、`category_mismatch`、`type_mismatch`、`daemon_not_in_target`、`db_error`），UI 据此翻译。

## 6. API 契约

采纳 issue 建议的默认路径与权限，不推翻：两条路由挂在 `server/cmd/server/router.go` 的 `/api/workspaces/{id}` 分组内、`RequireWorkspaceRoleFromURL(queries, "id", "owner", "admin")` 的 admin 组里。理由：导出内容覆盖智能体的完整指令、自动化与项目结构，与「策展 MCP 库」「安装插件」同级；导入是批量写操作，只能由 owner/admin 执行。

### 6.1 `GET /api/workspaces/{id}/config/export`

| 项 | 值 |
| --- | --- |
| 权限 | 目标工作区 `owner` 或 `admin`；agent actor token 一律 403（与 `custom_env` 端点同规则，避免智能体横向读取整包配置） |
| Query | `include=labels,issue_statuses,...`（逗号分隔，默认全部）；`include_archived=true|false`（默认 false） |
| 响应 | `200 application/json`，body 即 bundle；头 `Content-Disposition: attachment; filename="multica-config-{slug}-{YYYYMMDD}.json"`；`Cache-Control: no-store` |
| 错误 | 400 `invalid_include`（未知实体类型）；401；403；404（工作区不存在或非成员，与现有工作区路由一致，不区分） |
| 体积 | 序列化后超过 20 MiB 返回 413 `config_bundle_too_large`（V1 硬上限，主要来自 `skill_file.content`） |

导出是只读操作，但要写一条 `activity_log`（未确认现有 activity 类型表能否表达，若不能则用 `slog` 审计日志），记录谁在何时导出了哪个工作区。

### 6.2 `POST /api/workspaces/{id}/config/import`

请求体：

```json
{
  "bundle": { "...": "3.1 的完整 bundle" },
  "dry_run": true,
  "on_conflict": "fail",
  "include": [ "labels", "agents" ],
  "options": {
    "include_archived": false,
    "activate_autopilots": false,
    "apply_workspace_settings": true,
    "apply_issue_prefix": false
  }
}
```

| 项 | 值 |
| --- | --- |
| 权限 | 同导出；agent actor 403 |
| Content-Type | `application/json`；请求体上限 20 MiB |
| `dry_run` | 必填布尔。UI 必须先 `true` 再 `false`，服务端不强制顺序 |
| `on_conflict` | 缺省 `fail`；非法值 400，错误文案沿用 `on_conflict must be one of: fail, overwrite, rename, skip` |
| `include` | 缺省为 bundle 里存在的全部分组 |
| 响应 | `200` + 5.6 的报告（dry_run 与 apply 全部成功都是 200） |
| 错误 | 400 `config_bundle_invalid`（缺必填键 / `format` 不符 / JSON 结构错）；400 `config_bundle_version_unsupported`；400 `config_bundle_contains_secret`；400 `invalid_on_conflict`；401；403；404；409 `config_import_conflict`（`fail` 策略下 dry_run 或 apply 首批即命中，body 带报告）；413 `config_bundle_too_large`；422 `config_import_partial_failure`（apply 中途批次失败，body 带报告） |
| 并发 | 同一目标工作区同时只允许一个 apply（`pg_try_advisory_xact_lock(hash(workspace_id))`，失败返回 409 `config_import_in_progress`） |
| 跨工作区 | `bundle.source.workspace_id == {id}` 时返回 400 `config_import_same_workspace`；导入自己毫无意义且会在 `rename` 下制造副本 |

错误体沿用 `writeErrorCode` 的 `{ "error": "...", "code": "..." }`；带报告的 409 / 422 在此基础上加 `"report": {...}`。

### 6.3 前端契约（Stage 3）

- 两个端点都要在 `packages/core/api/schemas.ts` 加 zod schema，走 `parseWithFallback`，并各写一个畸形响应测试（CLAUDE.md API 兼容规则）。
- 导出按钮：工作区设置页，owner/admin 可见；点击直接下载文件。
- 导入流程：上传文件 → 本地用 zod 校验 `format` / `schema_version` → 调 `dry_run: true` → 预览页展示分批 items、冲突、`unmapped_refs`、`secrets_to_fill`，用户选 `on_conflict` → 调 `dry_run: false` → 结果页把 `secrets_to_fill` 做成可点击清单，每项跳到对应设置页。
- 导入是「确认后导航」型流程：不做乐观更新，apply 返回后再 `invalidate` 涉及的所有工作区级 query（labels / statuses / properties / skills / agents / squads / projects / autopilots / quick actions / views）。

## 7. V1 边界（明确不做）

| 不做 | 原因 / 何时再议 |
| --- | --- |
| CLI 子命令（`multica config export/import`） | 先用 Web/Desktop 跑通契约；CLI 只是同一端点的薄包装，V2 |
| 跨 Multica 实例迁移 | 成员 id、守护进程 id、系统智能体键在另一实例都不成立；V1 不阻止跨实例导入，但只保证同实例语义 |
| 密钥随包迁移（加密导出、口令保护包） | 没有通用的 secrets-at-rest 基础设施（见 `signing_secret` 迁移的说明）；先用「占位 + 人工重填」 |
| 增量同步 / 双向同步 | 复刻是一次性动作；同步需要持久化映射表与变更追踪 |
| 导入回滚 / 撤销 | 批次级事务已给出可预测的失败面；撤销需要快照，V2 |
| 导出任务、评论、附件、运行记录 | 那是数据迁移，不是配置复刻 |
| 自动重建集成（GitHub / VCS / 渠道 / Composio） | OAuth 与外部租户绑定必须由人在目标工作区重新授权 |
| 自动安装插件 | 依赖目标工作区已发布同版本包；先给清单 |
| `runtime_profile` 与运行时绑定 | 机器绑定，导入后智能体一律未绑定运行时 |
| 按实体类型分别指定 `on_conflict` | 一个策略 + `include` 已够用；V2 若有需求再加 `on_conflict_by_type` |
| 持久化导入映射表（新表） | V1 靠身份键幂等；只有做增量同步时才需要 |

## 8. 未确认项（Stage 2 实现前必须验证）

1. `issue_property.config.options[].id` 在 `CreateProperty` 时是否会被服务端重新生成。若会，需要在映射表里加 `(property_source_id, option_source_id) → option_target_id`，并改写视图 `propertyFilters` 的值；本页当前按「原样保留」设计。
2. `agent.avatar_url` / `squad.avatar_url` 指向的存储对象是否按工作区隔离。若隔离，跨工作区复制 URL 会 403，届时改为导出时置 `null` 并加 warning。
3. `project_resource.local_directory.daemon_id` 在同实例另一工作区是否可见（`agent_runtime` 按 `workspace_id` 隔离，同一台机器的守护进程在两个工作区大概率是两条注册）。本页按「不可见则跳过」设计。
4. `workspace.settings` 除 `always_redact_env` 外的键。本页按整体透传设计；若发现含个人或运行态键，改为白名单。
5. `agent.runtime_config` 除 OpenClaw 的 `{mode, gateway{url, token}}` 之外，其他运行时是否还有含凭据的键。本页用键名清洗兜底，Stage 2 需按 `pkg/agent` 各运行时逐个确认并补进 2.1 表。
6. 导出审计是否能落 `activity_log`（现有类型枚举可能不覆盖）。

## 9. 给 Stage 2 / Stage 3 的交接清单

Stage 2（后端，`server/`）：

- 新增 handler 文件 `internal/handler/workspace_config.go`（导出 / 导入）与 `internal/service/config_bundle.go`（bundle 结构体、序列化、键名清洗、映射表、批次执行器）。
- 导出 SQL 走 `pkg/db/queries/` 新增只读查询，SELECT 列表**不含** 2.1 表中的任何列。
- 2.4 的四条验收测试 + 每个实体类型至少一条「引用无法映射的降级」测试 + 幂等测试（同 bundle 用 `skip` 导两次）。
- 更新 `server/internal/service/builtin_skills/*/references/` 里涉及工作区设置的文档（若有）。

Stage 3（前端，`packages/core` + `packages/views` + 两端平台接线）：

- schema + `parseWithFallback` + 畸形响应测试。
- 设置页导出按钮、导入向导（上传 → 预览 → 应用 → 待填清单），页面放 `packages/views/settings/`，两端接线。
- 结果页对 `secrets_to_fill` 逐项深链；对 `autopilots_imported_paused` 给出「去启用」入口。
