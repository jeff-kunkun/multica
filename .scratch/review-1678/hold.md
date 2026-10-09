本轮验收不通过，PR #610 暂不合并。自动关票还可能把普通评论或另一份交付的批准当成本次验收通过，需要修正后再送审。

## Standards

- **F1 / P2：缺少仓库要求的真实页面验收证据。** `docs/evidence/DENE-1678/preview.html` 是示意稿，执行席交接和 PR 正文明确说明未做真实浏览器截图。AGENTS.md 要求响应式页面在 375 / 768 / 1280 三档核验，并补 390 手机网页；静态 CSS 推断不能替代截图。请在真实任务详情页展示服务端写入的跳过原因，补各宽度截图，包含长 PR URL 和长审查人名称。手机 App 的运行验收情况也应如实交代。这是证据缺口，未声称已复现布局故障。

## Spec

- **F2 / P1：任意非执行人的评论被升级为平台验收结论。** `server/internal/service/review_skip.go:133` 读取所有 member/agent 的 verdict 评论，`:90` 只排除执行人；没有验证作者当时是否为平台验收席。需求已定的是“平台验收席给出的审查结论”，不是所有非执行人的评论。具体反例：验收席 R 没有通过，另一 Agent C 在票上写入独立一行 `verdict: pass`，PR 被合入；执行人送审时 `FindReviewSkip` 接受 C，`issue_silent_stall.go:168` 直接转 done。已检查现有保护：正常评论放行走 `maybeReleaseOnAcceptance` 的 `authorIsReviewer`（`issue_block_wait.go:257`、`:314`），新路径绕开了这条检查；评论创建本身没有禁止普通作者写这行。关闭条件：只采信可核实的验收席审查记录，保留必要的历史验收席身份事实；普通成员/Agent 的 pass 不得触发跳过，并覆盖执行席换人或没有 close 记录的场景。

- **F3 / P1：批准没有对应到本次交付，可借旧 PR 的 approve 放行新 PR。** `server/internal/service/review_skip.go:99` 在全部关联 PR 中找到任意一个 merged+approved 就返回 true，且 `FindReviewSkip` 没有使用当前交付范围；平台 pass 也没有绑定交付或版本。具体反例：同票 PR A 已审已合，票重开后新增 PR B，B 未经审查但已合入；两者都不再 open，于是 A 的 approve 使 B 这轮直接 done。代码链条中没有后续本次审查校验，时间字段也只保存、不参与判定。关闭条件：基于当前规范交付及其审查事实判断，不能借历史或另一 PR 的批准；补“A 已审已合、B 仅合入”和“旧 pass 后新增交付”的回归验证，仍保留本次交付确已审已合时正常跳过。

## 验证与范围

固定点 `origin/kun=7168ef706da2806c95555c92c27255a121b32aff`，候选 `HEAD=93d761f06d383740ca14a0e4f158dcda79078dee`，检查 `git diff origin/kun...HEAD`。

本轮实际运行：`go test ./internal/service ./internal/ghpr ./internal/blockwait ./internal/statecard -run 'TestDecideReviewSkip|TestReviewSkipReason|TestApplyApproval|TestDeriveNowCarriesReviewSkip|Test.*Review.*Fail' -count=1` 通过；blockwait 报 no tests to run。`git diff --check origin/kun...HEAD` 通过。未重跑数据库集成测试、前端类型检查或浏览器验收。F2/F3 基于完整调用链可证明的输入反例，不冒称运行了新增回归测试。

按 code-review 要求检查 Jev：本 run 的规定分发路径中没有脚本，定位落到 `//skills/general/jev/scripts/jev`，调用报文件不存在；按 skill 的缺失回落规则由当前审查席人工判断，没有模型判定结果、confidence 或 usage。

统计：Standards 1 项（最高 P2），Spec 2 项（最高 P1）。

## 下一责任人

[@孙悟空](mention://agent/73f4e44b-2a52-43c4-b957-ea8b1f8b2aba) 请修复上述两处自动关票边界、补回归验证和真实页面证据，更新 PR #610 后重新送审。使用本票现有交付分支，不扩大为其他调度改造。

## 要你做的

不用你做什么。
