Bot PAT writes previously appeared as the credential owner personally speaking. This change records trusted Bot ID/name snapshots on comments, chat, activities, issue creation and agent runs; Web/Desktop, native mobile and CLI show the source. Bot comments cannot supply `--per-quote` evidence, and Bot assignment requests no longer get human-override treatment. Stopped/deleted credentials preserve history through tombstones.

Before: deleting a stopped Bot removed the credential and cascaded delivery history. The transaction probe now retains both rows and the name; rolling the query back reproduces the deletion with the pristine hash restored. Real PAT + PostgreSQL tests verify comments/chat, ordinary-human controls, forged fields/headers, queued/async run provenance and retained history. Redis auth tests, activity/GUC trigger probes, targeted routing/attribution regressions, migration lint, Go compilation, frontend/mobile typechecks, scoped lint and UI/schema/CLI tests passed. A fresh source review found one async creator-fallback gap; the persisted issue snapshot and database regression close it.

[Evidence archive](https://ai.ferryway.cc/api/attachments/01a129cf-0e38-7ade-8c71-5be316e025e7/download) contains raw verification logs, the four rollback roles and opened issue screenshots at CSS widths 375/390/768/1280. Visual limitations remain: Ego zoom cropped right edges, chat QA was blocked by repeated session 401s, and native device verification was not run. A direct local CLI/PAT probe was refused by the daemon task-token boundary; no safeguard was bypassed. CLI formatting and JSON fields were checked by tests, and real authenticated writes by the handler/DB regression.

Merge risk: this crosses authentication and attribution. Migration662 is additive; old binaries cannot label Bot writes and still physically delete Bot rows, so Bot traffic/deletion must not hit old replicas during rollout. #626 is now merged and its `origin/kun` was merged into this task branch. DENE-1792 owns the outbox trigger/worker consumer and four-hop cause propagation: it must consume the trusted GUC and persisted source contract in `server/docs/bot-write-provenance.md` before enabling delivery. Migrations660/661 remain that ticket's. Integrated wake delivery and real Grok are not claimed as verified. Please hold release until that integration and remaining visual acceptance pass.

Web/Desktop share the changed views. Native mobile comment/chat/activity surfaces were changed and typechecked; no platform surface was silently omitted. No production deployment performed.

## 知识审计
- 术语/ADR：不改；API、来源传播与部署顺序记录在 `server/docs/bot-write-provenance.md`。
- AGENTS.md：不改；保留工作区原有运行时注入，未提交。
- DESIGN.md / INTERACTION.md：不改；复用现有作者信息布局，无新交互路径。
- Skill：不改；没有新增 CLI 命令。
- Multica Project Context：不改。
- 文档索引：`docs/README.md` 新增 Bot 来源契约入口。
- 删减：无；任务分支待审未清理。

Closes DENE-1794
