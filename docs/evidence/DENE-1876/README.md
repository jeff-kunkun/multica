# DENE-1876 页面说明偏好同步核验

核验日期：2026-10-11。此记录汇总本任务实际执行结果，不复制个人画像正文、账号标识、机器路径或原始运行日志。

## 结论与来源

页面说明偏好已由自动分发回写到唯一来源 `kun-agent-mono/prompt/AGENT.md`。
[已合并的来源修复](https://github.com/kkunkunya/kun-agent-mono/pull/784)于 18:03:07 UTC 合并，提交为 `3cf1ba3766d63d88a7171c91ac77d0c9eb187eb2`，仅修改该文件（新增 4 行、删除 2 行）。本任务随后核验的主线版本为 `151d9c0494d648e5eba91ec65be4c4c5b955d49c`。

主线文件与本任务已验证候选逐字节一致；重新读取线上画像后，I17 一致性检查通过。因此引用已有修复，没有再开重复 PR，也没有把外仓提交导入 Multica。

确认内容涵盖：先用大白话和生活比喻讲原理；直观对比及有助理解的动画；工程细节放底部折叠区；手机可打开的单文件与内联资源；对齐小结区分已定和未定；以“要你做的”收尾。其他已有规则保留。

## 实际检查

以下均为本任务先前运行的已保存结果，本轮整理证据没有重新执行全量检查。

| 检查 | 结果 | 退出码 |
| --- | --- | --- |
| I17：原版与同一份线上画像比较 | FAIL：线上 1491 字，来源 1178 字 | 1 |
| I17：修订版与同一份线上画像比较 | PASS | 0 |
| I17：独立副本回滚后使用同一输入 | FAIL，恢复原版差异，哈希等于原版 | 1 |
| 补丁重建修订文件 | 字节一致 | 0 |
| 合并后的主线与新读取线上画像比较 | I17 PASS | 0 |
| `python3 scripts/check-agent-drift.py --static-only` | I8/I9/I11/I12/I13/I15/I16/I17 通过 | 0 |
| `python3 scripts/multica-skills.py drift --workspace kunkunya --repo <mono工作面> --json` | 98 个技能内容、50 个席位绑定一致 | 0 |
| `git diff --check` | 无空白错误 | 0 |

原版及回滚副本 SHA256：`1de83ccaae5012b24c79c861cb7276a21c7f2f26c74cbb2f85ac0ac18f42f619`。
修订版及已核验主线文件 SHA256：`eadc3c39bf0dfe963fae7c26b6839aa6e98082a340cbea5b623df2a102c310e2`。

联网全量 `check-agent-drift.py --workspace kunkunya --json` 曾返回 1，但 I17 的 errors/warnings 均为空；失败来自未在本任务修改的席位指令、模型配置和待收编席位。不据此声称所有受管配置都无漂移，也未为通过检查覆盖这些配置。

## 证据与范围

[原始核验附件（需工作区权限）](https://ai.ferryway.cc/api/attachments/01a12c3b-7694-71e6-9dd0-fcc12052c34f/download)，SHA256：`b06083eb02d7deb58f1e5a3cfcfd2e73dff4d51fef51c73b31ee601cc36a3b40`。原始材料保留在附件，不复制入本证据目录。

本次 Multica 交付仅增加本记录与证据索引，不修改应用、画像来源或运行时技能。文档检查为相对链接检查、脱敏检查和 `git diff --check`；未运行应用代码测试。
