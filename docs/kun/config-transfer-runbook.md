# 跨环境迁移操作手册

把工作区配置和对话从一台 Multica 实例搬到另一台。导出可以在官方云或 kun 上做；导入的目标必须是 kun 实例。

## 用 Desktop 按钮迁移

1. 用 **kun 构建** 的 Desktop 登录**源**工作区（可以是官方云）。打开设置 → 配置导出/导入 → **跨环境迁移（含对话）**。
2. 点 **一键导出**，选 ZIP 保存路径。等进度走完（会话数 / 当前会话 / 已下载附件）。文件里有聊天记录和成员邮箱，按敏感文件保管。
3. 切到目标 kun 实例（例如 `ai.ferryway.cc`），打开**目标**工作区的同一设置页。
4. 点 **一键导入**，选刚才的 ZIP。先看 dry-run 预演（`secrets_to_fill` / `runtimes_to_bind` / `export_gaps` / 计数），确认后再写入。
5. 导入完成后按清单重填密钥、绑定运行时。不要把 ZIP 传到公开位置。

按钮只调用本机 `multica transfer`，不经过新的导出端点。若提示「目标实例需要 kun 版本」，说明当前连的不是 kun。若提示「Desktop 内置的 CLI 版本过旧」，升级 Desktop。若提示包损坏，重新导出。

命令行用法与排错见 `docs/kun/config-transfer-v2.md` 与 `multica transfer --help`。
