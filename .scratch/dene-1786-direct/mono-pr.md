Kun changed DENE-1786 to direct personal-token CLI access on the Grok computer. This candidate removes the mailbox's comment/create/status/query interface, retaining only dispatched execution tasks and their correlated results. Bot operations no longer have a mailbox permission tier. The external-post/message/purchase draft convention remains.

README and standing instructions now describe direct login, complete reads, result correlation and truthful latency reporting. The credential scan permits the designated protected Multica configuration rather than demanding zero tokens. A read-only helper checks CLI command availability. Existing task/audit state is retained.

Validation: three task-protocol checks pass; Python compilation, shell syntax and scoped diff checks pass. A newly built macOS CLI exposes all 21 preflight commands; Linux amd64 CLI builds successfully. These are local checks, not personal-token/cloud acceptance. Root Multica skill-source draft is tracked separately.

Not deployed: Grok SSH still times out during banner exchange. Token provisioning, cloud installation, all 12 live checks, mobile experience, and personal-client new-chat/upload gaps remain unfinished. Do not merge or close the issue as delivered. This branch retains the prerequisite #757 without rewriting history.

## 知识审计
- 文档/长期指令：改为直连；删除旧“四动作/不保存 token/额外确认档”说明与旧延迟承诺。
- Skill：Multica 仓的现有 multica-platform 增加 external-bots 参考，尚未分发。
- AGENTS/ADR/术语/UI 契约/Project Context：不改。
- 删减：删除旧查询实现及其测试，以执行任务协议检查取代；其他分支不清理。

Web/Desktop/native mobile: no UI changes in this candidate. Bot enrollment UI is DENE-1788. Current work uses existing CLI/server interfaces; personal-client gaps are explicitly pending.

Refs DENE-1786 (incomplete draft; no close directive).


## DENE-1786 live deployment update
Deployed the task-only helper, standing instructions, scanner and Mac bridge. Real Grok SSH task t-29af8208c2 returned live.log; Mac verified size/SHA-256 and attached it in DENE-1797 comment 01a129bc-7250-731c-9ecb-f8b7b10f480a. Eight existing tests pass. GitHub and Multica protected persistent configurations work in a new SSH process. Whole-cloud reset not tested. Webhook wake is unconfigured in this bridge; do not claim woken when only delivered.

## 知识审计
README 更新部署事实、持久化边界和新 CLI 文件路径；长期指令更新三阶段汇报，移除云端旧四动作/零令牌限制。设计仓 README、bots/grok.md、add-new-bot.md 已提交 0d88edd。其他项目知识入口不改。
