验收不通过，PR #601 暂不合并；请实现席修复以下两项后重新交付。

## Standards
0 项。改动范围和包边界符合约定；没有修改 execenv，也没有新增界面。PR 已交代 Web/Desktop 无界面变化的原因。

## Spec
2 项阻断（P2），均已用隔离数据库上的真实 CloseIssue / HandoffIssue / project report handler 复现：

1. `server/internal/handler/project_report.go:482` 从 progress 读结论，无法保证 summary 原文。close 接受 501 个字的 summary，但 `issue_close.go:483` 调用 progress.Clip，只存 500 个字；报告因此少一个字。空白也会被压缩。临时测试 TestReviewReportSummaryVerbatim 失败；正常短句测试通过。关闭条件：报告读取独立、可靠保存的收尾结论，或等价实现，不能依赖有损进度文本；补原文一致性回归测试。
2. `server/internal/statecard/statecard.go:332` 假定每次 handoff 都更新 handoff.at。实际 `issue_handoff.go:242` 仅在 summary 非空时记录时间。先 close(summary="旧结论")，再成功 handoff(不传 summary)，报告仍返回 "旧结论"，违反最新一次没写就省略的契约。TestReviewReportEmptyHandoff 已复现。关闭条件：无 summary 的交棒也能被识别为最近操作并清掉旧结论；补 close→空 handoff 与有 summary handoff→空 handoff 回归。

## 验证
- 数据库命令：`bash scripts/test-db.sh --quiet -- sh -c 'cd server && go test ./internal/handler -run "TestReviewReport|TestProjectReportCarriesLatestSummary" -count=1'`。原有 TestProjectReportCarriesLatestSummary 通过，两个附加反例失败。附上可放入 server/internal/handler 的复现文件和精简输出；临时测试已移出源码目录。
- TestLatestSummary、TestBuiltinSkill*、CLI close/handoff 注册测试通过。
- TestBriefSizeBudget/chat 失败：4015 bytes > 4000；execenv 相对审查基线完全无改动，单独披露，不计入本 PR 阻断项。
- `git diff --check origin/kun...HEAD` 通过。工作区原有 AGENTS.md 运行时改动未触碰。
- jev 在技能指定的两处均不存在，按技能规定回落人工判断；没有伪造 results/confidence/usage。

审查固定点：origin/kun b6f3b1a6e18b3cedcf0ee37e34afe9b3ff270267；HEAD d8e87329e5059fe59a99fc8d1c9a6f83b827a04e。命令 `git diff origin/kun...HEAD`。
Standards：0 项；Spec：2 项，最高 P2。下一责任人：实现席；本次 hold 由平台唤醒修复。
