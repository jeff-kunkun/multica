## 设置覆盖清单

这张表以 `origin/kun` 的设置页和服务端路由为准；Git 连接由 DENE-968 负责，故不在本表实现范围内。`部分` 表示已有专用命令覆盖了实体，但设置页的全部操作还没有统一入口。

| 设置页项目 | 服务端接口 | CLI 覆盖 | AI 入口 |
| --- | --- | --- | --- |
| MCP 服务器库（工作区/智能体） | `/api/mcp-servers/*`, `/api/agents/{id}/mcp-servers` | 有：`workspace mcp`、`agent mcp` | 专用命令 |
| 运行时 profile | `/api/runtime-profiles/*` | 有：`runtime profile` | 专用命令 |
| 路由项目表 | `/api/workspaces/{id}/routing-projects` | 有：`workspace routing-projects` | 专用命令 |
| 成员 | `/api/workspaces/{id}/members` | 有：`workspace member` | 专用命令 |
| 智能体调用权限 | `/api/agents/{id}`（`permission_mode`） | 有：`agent update --permission-mode` | 专用命令 |
| 智能体环境变量、技能、标签、属性、自动化 | `/api/agents/{id}/env`、`skills`、`labels`、`properties`、`automations` | 有 | 专用命令 |
| 智能体临时通行证 | `/api/agents/{id}/access-passes` | 原无；现有 `settings get/set agent.{id}.access-passes` | 通用入口 |
| 工作区模块访问策略 | `/api/modules`、`PUT /api/modules/{module}/visibility` | 原无；现有 `settings get modules.visibility` / `set module.{module}.visibility` | 通用入口 |
| 仓库可见范围 | `PUT /api/repos/visibility` | 原无；现有 `settings set repo.visibility` | 通用入口 |
| 仓库共享成员 | `/api/repos/shares` | 原无；现有 `settings get/set repo.shares` | 通用入口 |
| 运行时开放范围（private/public） | `PATCH /api/runtimes/{id}`（`visibility`） | 原无；现有 `settings get/set runtime.{id}.visibility` | 通用入口 |
| 运行时技能开关 | `PUT /api/agents/{id}/runtime-skills/enabled` | 原无；现有 `settings set agent.{id}.runtime-skill` | 通用入口 |
| 模型目录刷新 | `POST /api/daemon/runtimes/{id}/model-catalog/refresh` | 无（daemon 专用动作，后续可加 `runtime refresh-models`） | 待补专用命令 |
| Slack / 飞书安装与绑定 | `/slack/*`、`/lark/*` | 无 | 待补（外部 OAuth 流程） |
| 插件与 Composio 连接 | `/plugins/*`、`/connections` | 无 | 待补（凭据生命周期） |
| 自动化触发器签名密钥 | `/api/autopilots/{id}/triggers/{id}/signing-secret` | 无；密钥不应进入通用 JSON 日志 | 待补专用命令（stdin/file） |

## AI-native 结论

逐项命令最容易发现、类型也最强，但每增加一个设置就要同时维护 Cobra、帮助文本和测试。一个把 key 映射到既有深接口的 `settings get/set` 更适合智能体：CLI 只做定位、JSON 编解码和输出，所有权限、审计和校验继续由服务端处理。推荐两者并用：高频实体保留专用命令，长尾设置进入通用入口；当前最小版本已覆盖仓库可见范围/共享、通行证、运行时开放范围和运行时技能开关。

例子：

```sh
multica settings get modules.visibility --output json
multica settings set repo.visibility --value-json '{"url":"https://github.com/acme/app.git","visibility":"workspace"}'
multica settings set runtime.RUNTIME_ID.visibility --value-json '{"visibility":"public"}'
multica settings set agent.AGENT_ID.runtime-skill --value-file ./runtime-skill.json
multica settings set agent.AGENT_ID.access-passes --value-stdin < pass.json
```

`settings set` 不复制服务端权限闸门；错误直接返回服务端响应。新增设置时先把接口和 key 加入本表，再为通用入口补一条 `settingsPath` 注册测试，避免文档与 CLI 漂移。
