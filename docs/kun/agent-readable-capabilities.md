# 新能力的智能体可读契约

Multica 魔改的每一个新能力都必须像 MCP 工具一样自描述：智能体能发现入口、理解输入和输出、执行操作，并在失败后知道下一步。这是交付要求，不要求把 CLI 改成 MCP 服务。Agent mono 与 Multica 魔改共用本页，其他入口只引用它。

## 最低标准

1. **入口可发现。** 新能力带来的每块信息都有 CLI 读命令；操作复用同一个服务端接口和校验。`--help` 说明能读什么、改什么、参数约束、权限、作用范围及副作用，至少给一个可运行的示例；需要对象 ID 的示例同时交代从哪里读取真实 ID。
2. **结果可解析。** CLI 提供 `--output json`，API 返回结构化数据。字段名称、类型和枚举含义稳定，说明 ID 指向什么、状态代表什么，以及空值、空列表、分页和单位；需要理解结果的名称、原因和关联信息随结果返回，不只给一串 ID 再让智能体另查。新增字段优先兼容旧调用者，破坏性变更要说明版本和迁移办法。JSON 独占 stdout，提示写 stderr，成功与失败用退出码区分。
3. **错误可恢复。** 错误说明失败原因和一个可执行的下一步，机器可读错误用稳定的 `code` 区分；说明哪些参数有误、缺什么权限或前提，不能只返回“失败”。写操作遇到超时等不确定结果时给出查证入口，让智能体先确认是否已写入再决定重试；服务端拒绝应原样传达，不能另写一套规则绕过校验。
4. **发现链随上线更新。** 新增命令进入现有能力地图；自动生成部分验证生成结果，手写说明在同一交付中更新。仅改变某条命令时更新它的帮助及相关 `multica-platform` reference；只有入门路径变化才改 Bot 入门步骤。纯内部能力或不适用的入口在 PR 中说明原因。

## 沿用现有入口

| 入口 | 现有来源 | 新能力上线时怎么对齐 |
| --- | --- | --- |
| 命令能力地图 `multica capabilities --output json` | [CLI 命令树生成器](../../server/cmd/multica/cmd_capabilities.go) | 命令注册后自动出现；核对 `usage`、`summary`、`example`、`help`、`access`、`bot_key` 和所属领域，默认推断不准时补 annotation。地图描述已安装 CLI 的能力，服务端权限校验仍为准。 |
| Bot 入门 `multica bot guide` | [共用入门步骤](../../server/internal/botonboarding/onboarding.go)、[外部 Bot 契约](../../server/internal/service/builtin_skills/multica-platform/references/external-bots.md) | `capabilities → --help` 继续复用；步骤变化只改 `botonboarding.Steps()`，由服务端接入说明、能力地图和设置页共用，不另建静态命令清单。 |
| 智能体按需说明 | [multica-platform 路由表](../../server/internal/service/builtin_skills/multica-platform/SKILL.md) | 更新受影响 reference；确有新领域才补路由。完整操作说明放帮助或 reference，不塞进每回合简报。 |

## 收尾检查清单

后续魔改任务在 PR 或收尾证据中引用本节，逐项记录命令输出、说明位置；不适用项写明原因。

- [ ] 找得到：新增信息可读，`--help` 说明输入、权限、影响，并有可运行示例。
- [ ] 读得懂：JSON 可解析，字段、状态、空值及分页含义清楚，stdout 与 stderr 分开。
- [ ] 接得上：成功结果能衔接下一步；至少一种代表性失败给出原因、稳定错误码和下一步。
- [ ] 不漏入口：已核对自动能力地图、Bot 入门及相关 reference，适用的更新与功能同批交付。

现有能力地图检查是 `server/cmd/multica/cmd_capabilities_test.go` 中的 `TestCapabilitiesEveryCommandIsDescribed` 与 `TestCapabilitiesNewCommandAppearsAutomatically`。它们验证命令可描述和自动收录；字段含义、真实例子、错误恢复仍须按上面的清单核对。
