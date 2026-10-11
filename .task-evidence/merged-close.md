已按交棒接续并重读最新来源，发现等价修复已经由分发自动回写合并，不再重复开 PR。

GitHub https://github.com/kkunkunya/kun-agent-mono/pull/784 已于 2026-10-11T18:03:07Z MERGED，合并提交 3cf1ba3766d63d88a7171c91ac77d0c9eb187eb2，只改 prompt/AGENT.md（+4/-2）。DENE-905 评论 01a12c22-155d-73f1-a579-e2c5ad948c04 记录了来源回写、合并和漂移干净。

新 fetch 的 main 151d9c0494d648e5eba91ec65be4c4c5b955d49c 的 prompt/AGENT.md 与原已验证 MODIFIED_FILE.md 字节相同。重读线上画像后 I17 PASS（exit 0），静态门禁通过（exit 0），git diff --check 通过。原始 BASELINE FAIL / MODIFIED PASS / ROLLBACK FAIL 与恢复哈希记录保留。本任务没有覆盖任何后续画像更新，也没有将 mono 提交混入 Multica 父交付线。

## 知识审计
全局偏好已由上述合并落在唯一来源 prompt/AGENT.md；本轮只核验、引用既有修复，无新知识写入。应用 AGENTS.md、技能与其他受管配置不改。保留本地证据，不删除仍用于跨会话核验的工作面。

当前技能分发复验：multica-skills.py drift --workspace kunkunya --repo <mono工作面> --json，exit 0，98 技能内容及 50 席位绑定一致。本次页面偏好来源与线上已对齐；不声称其他席位模型配置的全量漂移已清零。
