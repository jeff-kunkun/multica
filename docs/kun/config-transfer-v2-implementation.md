# 跨环境迁移包 V2 实现备注（DENE-254）

实现对照 `docs/kun/config-transfer-v2.md`。本页只记录第 8 节 10 项验证结论与影响实现的取舍。

## 第 8 节验证结论

| # | 问题 | 结论 | 取舍 |
| --- | --- | --- | --- |
| 1 | CLI 能否同时持有两个不同服务器的登录档 | **能。** `--profile` 把 `server_url` + `token` 隔离到 `~/.multica/profiles/<name>/config.json`。`multica transfer export --profile cloud …` 与 `multica transfer import --profile kun …` 各用各的登录档。 | 不增加明文 `--token` 参数。 |
| 2 | 源实例成员列表是否返回邮箱 | **返回。** `GET /api/workspaces/{id}/members` 走 `ListMembersWithUser`，JSON 含 `email`。`GET /api/me` 也含导出者邮箱。 | `people.json` 含被引用成员；`--no-people` 只保留导出者。 |
| 3 | 上游 `main` 读接口对 V1 分组覆盖 | 见下方矩阵。缺口进 `manifest.export_gaps`。 | 导出从不调用 `config/export`。 |
| 4 | 官方云与 `kun` 的 `system_key` 集合 | **产品内置一致：`mika`。** `kind=system` 的 `agent_builder:*` 两侧都排除。kun 私有小队智能体是 `kind=user`，按名字映射，不走 `system_key`。 | 目标端缺少同名系统智能体时跳过，报告 `system_agent_not_in_target`（V1 规则）。 |
| 5 | 续聊失败回退是否注入 Multica 历史 | **不注入完整历史。** `chatSessionResumeFallbackNeeded` 只补 `session_id` / `work_dir`。导入不迁这些指针，也没有 `agent_task_queue` 行，所以第一次新消息会开新的智能体 CLI 会话。claim 路径只把**本轮未答复的 user 消息**拼进 `ChatMessage`，不是整段记录。 | 对外口径：**记录可见，智能体从新会话开始**。不得说「上下文无缝」。 |
| 6 | 聊天正文附件链接形态 | **相对路径与绝对 URL 都存在。** 权威形态是 `/api/attachments/{id}/download`；部署也可能写成 `MULTICA_PUBLIC_URL` + 该路径。消息气泡主要靠 `attachments[]` 元数据，不靠 Markdown。 | 导入不改写正文 URL；报告不单独计 `content_has_source_urls`（V2 契约允许确认前原样保留）。附件按确定性 id 重建后，新环境用新 id 的 download 路径。 |
| 7 | `runtime_profile.display_name` 是否唯一 | **是。** 迁移 `120_runtime_profile`：`UNIQUE (workspace_id, display_name)`。handler 冲突返回 409。 | 身份键用 `display_name`；`on_conflict` 作用于 profile。 |
| 8 | 本产品 token 明文前缀 | 个人访问令牌 `mul_`；daemon token `mdt_`；任务 token `mat_`；云舰队 `mcn_`。见 `server/internal/auth/jwt.go`。 | 扫描模式匹配这四个前缀。 |
| 9 | 官方云读接口速率限制与分页上限 | **代码里没有对成员读接口的全局 429 配额。** 聊天消息分页 `limit` 上限 **100**（默认 50）。公开 API / plugin 另有独立限额，导出不走那些路径。 | 导出对 429/503 指数退避（1s→60s）；消息翻页 `limit=100`；不并发超过 4 路（当前实现串行翻页）。压缩率未在本票实测。 |
| 10 | 导入历史消息是否触发写路径副作用 | **不会，只要走专用 sqlc。** `TransferInsertChatSession` / `TransferInsertChatMessage` / `TransferInsertAttachment` 是 `INSERT … ON CONFLICT (id) DO NOTHING`，不调用 `SendChatMessage`、不入队、不生成标题、不推渠道。`finalize: true` 才发一次工作区级 `chat:session_updated`。测试断言无 `agent_task_queue` 行。 | 禁止复用发送路径。 |

## 分组 → 读接口矩阵

| V1 分组 | 读接口 | 上游 `main` 是否可用 |
| --- | --- | --- |
| workspace.settings / context / repos / issue_prefix | `GET /api/workspaces/{id}` | 可用。`attribution_fail_closed` 不在该响应里，缺则保持零值。 |
| labels | `GET /api/labels` | 可用 |
| issue_statuses | `GET /api/issue-statuses` | 可用 |
| issue_properties | `GET /api/properties` | 可用 |
| skills + files + labels | `GET /api/skills`，`/files`，`/labels` | 可用 |
| mcp_servers | `GET /api/workspaces/{id}/mcp-servers` | 可用（仅 name/transport） |
| agents + invocation_targets + skills | `GET /api/agents`（含 `invocation_targets`、`skills`） | 可用。`custom_env` 明文不返回。`custom_args` 可能明文，导出端脱敏。`runtime_config.gateway.token` 可能是 `***`，视同秘密。 |
| system_agents | 同上；`kind=system` 行通常不出现在成员列表 | 可能 `export_gaps` |
| squads + members | `GET /api/squads`，`/members` | 可用 |
| projects + resources | `GET /api/projects`，`/resources` | 可用 |
| autopilots + triggers | `GET /api/autopilots`，`GET /api/autopilots/{id}` | 可用 |
| autopilot_collaborator | 详情 payload 的 `collaborators` | 有则导出，无则空数组 |
| quick_actions | `GET /api/quick-actions` | 可用 |
| issue_views | `GET /api/issue-views` | 可用（只收 `visibility=workspace`） |
| integrations | GitHub / VCS / MCP 清单 | 可用 |
| runtime_profiles | `GET /api/workspaces/{id}/runtime-profiles` | 可用 |
| chat sessions | `GET /api/chat/sessions?status=all` | 可用（本人会话） |
| chat messages | `GET /api/chat/sessions/{id}/messages/page?limit=100` | 可用 |
| attachments | `GET /api/attachments/{id}/download` | 可用 |
| pinned agents | `GET /api/chat/pinned-agents` | 可用 |

## 命名空间 UUID（发布后不得修改）

- chat_session `a1c0e5f0-7b12-4d3a-9e44-0f6c2b8d91a1`
- chat_message `a1c0e5f0-7b12-4d3a-9e44-0f6c2b8d91a2`
- attachment `a1c0e5f0-7b12-4d3a-9e44-0f6c2b8d91a3`

目标 id = `UUIDv5(ns, 目标工作区 id + "/" + 源 id)`。
