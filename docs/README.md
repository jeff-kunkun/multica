# 项目文档

- [项目词汇表](../CONTEXT.md)
- [架构决定](adr/)
- [统一带选项提问的架构决定](adr/0001-option-questions.md)
- [目标任务的完成线与服务端续跑](adr/0002-goal-mode.md)
- [路由派票的执行席归属与来源可见性](adr/0003-routing-executor-ownership.md)
- [跨工作区连通使用显式出口，不复用访客](adr/0004-workspace-link.md)
- [Web/Desktop 移动适配先抽共享外壳再修页面](adr/0005-mobile-web-shell.md)
- [备用运行时只能显式配置、同主人同提供商，不自动按系列挪](adr/0009-fallback-runtime-explicit-same-owner.md)
- [证据索引](evidence/INDEX.md)
- [运行时掉线告警与高时效任务的机器选择](kun/runtime-availability.md)
- [Bot 写入来源与推送集成契约](../server/docs/bot-write-provenance.md)
- [外部 Bot 提及原文、原楼层回复与 30 秒通知合并](../server/internal/service/builtin_skills/multica-platform/references/external-bots.md#mention-delivery-and-replying-in-place)
- [Bot 唤醒限制、回执、恢复与审计](../server/docs/bot-wake-contract.md#causality-lifecycle-and-verification-limits)
- [要人决定的事推给本人的 Bot](kun/human-needed.md)
- [个人 Bot 动态流、主人确认与代理审计契约](../server/docs/bot-activity-contract.md)
- [办公室事件流、逐条回执与成果送达契约](../server/docs/office-event-contract.md)
- [每台机器同时运行上限](kun/machine-run-capacity.md)
- [Qwen Code 思考档位按模型声明（含 Nexus 客户端说明）](kun/qwen-thinking-levels.md)
- [新办公室成员的模型交给 magpie 网关](kun/magpie-office-routing.md)
- [工作区全景命令与筛选范围](../server/internal/service/builtin_skills/multica-platform/references/overview.md)
- [运行合并、连带停止与聊天转单](../server/internal/service/builtin_skills/multica-platform/references/run-control.md)
- [新能力的智能体可读契约与收尾检查清单](kun/agent-readable-capabilities.md)
- [Compact cross-workspace read and durable replay protocol](kun/fast-access.md)
- [Local MCP adapter and client configuration](kun/fast-access-mcp.md)

- [CLI project names, temp files, key status and compact reads](kun/cli-convenience.md)

## 当前适配资料

- [DENE-1277 全站响应式盘点](evidence/DENE-1277/report.html)
- [移动共性外壳预览](design/dene-1291-mobile-shell-preview.html)
- [手机 App 连通工作区交互路径](../INTERACTION.md)
- [手机 App 连通工作区交互预览（示例数据）](design/prototypes/mobile-workspace-links.html)

可复跑的操作手册放在本目录，并从这里建立入口。
