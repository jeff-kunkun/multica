# 阻塞根因与父子任务聚合：任务图认知模型（DENE-281 / 设计）

本页是 DENE-281 的设计稿。它**不改**生产代码，只给出模型、判定表、交互方向和拆票。它建在 [`scheduling-close-protocol.md`](./scheduling-close-protocol.md)（DENE-230–234）之上，不另起状态体系。

核对基线：`origin/kun` @ `79be10242`（2026-09-16）。行号指向该提交。

## 0. 一页结论

1. **阻塞事实只写在最小责任单元上**，也就是真正卡住的那张票。父票不抄原因。父票的“卡点”由前端从子树现算出来。
2. **不新增状态，不新增表。** 在已有 `close.*` 里只加两个键：`close.block_kind`、`close.block_action`。只有 `close.conclusion=blocked` 时才要求写。“卡住多久”直接用已有的 `close.at`。
3. **`in_review` 默认不算阻塞。** 只有两种情况会变成“逾期”：一是超过阈值，二是下一责任人手上没有正在跑的 run。逾期是**推导出来的**，不落库。今天的 `closeProtocolIsStuck`（`packages/core/issues/close-protocol.ts:88-99`）把 `in_review` 一律当成卡住，这正是认知噪音的来源，要拆开。
4. **传播只沿两条真实的边走**：父子 stage 屏障（`parent_issue_id` + `stage`），以及跨票等待（`close.waiting_on`）。不走评论文字，也不复活 `issue_dependency` 表。
5. **任务图分三层**：根因节点挂实心徽标；受影响的上游节点挂空心“受阻于 → X”；父票顶部一张摘要卡，回答“几个根因、要不要你动手、先处理哪个”。
6. **推荐外观**：方向 B（父票摘要优先）作为 MVP。方向 A（根因链路优先）的“实心 / 空心”徽标规则同时用在行上。理由见 §4.3。
7. **最小垂直切片全在前端**：一个 core 纯函数 `deriveBlockerTree`，加一个 views 摘要卡。服务端只做一件事：`closeprotocol.Validate` 认识这两个新键。回滚就是 revert，不涉及 migration。

## 1. 现状盘点

### 1.1 能表达什么

| 能力 | 载体 | 代码 |
| --- | --- | --- |
| 父子关系 + 屏障分组 | `issue.parent_issue_id` + `issue.stage` | `packages/core/types/issue.ts:194-197` |
| 屏障何时关 | 只认 `done`/`cancelled`。staged 集合里 unstaged 子票**不参与**屏障 | `server/internal/handler/issue_child_done.go:431-433`、`:505-530` |
| 子票进终态 → 叫醒父票 | 系统评论 + enqueue | `issue_child_done.go:70-99`；挂钩 `issue.go:3759`、`github.go:1938` |
| 收口五要素 | 8 个扁平 `close.*` metadata 键 | `server/internal/closeprotocol/closeprotocol.go:22-31`，校验 `:94-202` |
| 跨票等待 → 被等票终态时叫醒 | `close.waiting_on` + GIN containment | `server/pkg/db/queries/issue.sql:90-100`；`server/internal/handler/issue_waiting_on.go:32`、`:93-128`；挂钩 `issue.go:3760`、`:4474`、`github.go:1939` |
| 同家族等待不重复叫醒 | waiter 是被等票的父票时跳过 | `issue_waiting_on.go:97-103` |
| 停滞发现（补偿） | 4 个扫描，30 分钟阈值，只写 mention 评论 | `server/internal/handler/stagnation_watchdog_scan.go:18`、scan A `:85-131`、scan D `:151-188` |
| 屏障叫醒失败留痕 | `stage_wakeup_failure` 表 | `server/migrations/480_stage_wakeup_failure.up.sql` |
| 任务失败回滚 | 只把 `in_progress` 打回 `todo`；`in_review`/`blocked` 不动 | `server/internal/service/task.go:5889-5912` |
| 子票行展示 | 每行一条 `SubIssueCloseStrip`：stage / conclusion / 下一责任人 / waiting_on / 最近活动；外加 missing、drift 两个异常态 | `packages/views/issues/components/sub-issue-close-strip.tsx:22-107`，挂载 `issue-detail.tsx:896` |
| 子票列表 | 按 stage 分组，组头只有 “Stage N” | `issue-detail.tsx:422-440`、`:3191-3219` |
| “几个 agent 在干活”聚合 | 服务端投影 `/api/working-agents?parent=` | `sub-issues-agent-working-chip.tsx:21-38` |
| 实时 | `issue_metadata:changed` → `onIssueMetadataChanged` → `patchIssueSnapshot` | `packages/core/issues/ws-updaters.ts:634-653` |
| 表格层级树 | 按 parent 递归展开的层级行 | `packages/views/issues/components/table-view.tsx:1826-1880` |

### 1.2 不能表达什么（本票要补的）

| 缺口 | 现象 | 证据 |
| --- | --- | --- |
| G1 卡点“是什么类型” | `blocked` 只有 conclusion，分不清是等人拍板、缺权限、外部系统，还是等另一张票 | `closeprotocol.go:187-190` 只校验 status |
| G2 用户该做什么 | 要读证据评论才知道 | `close.evidence_comment_id` 只是个指针 |
| G3 审核中 vs 阻塞 | 条带把 `in_review` 和 `blocked` 画成同一种 `stuck` 色调 | `close-protocol.ts:93`、`sub-issue-close-strip.tsx:85-100` |
| G4 父票看不到根因 | 父票详情里只有子票逐行条带。孙票、跨票等待的对象都不上浮，得逐层点进去 | `issue-detail.tsx:3192` 只拿直接子票 |
| G5 哪一层真正挡路 | 组头只写 “Stage N”，不说“这一组正在挡屏障”，也不说“后面 backlog 在排队” | `issue-detail.tsx:3198-3204` |
| G6 逾期不可见 | 看门狗 30 分钟命中只写一条评论，UI 不知道 | `stagnation_watchdog_scan.go:111`、`:183` |
| G7 父票自己卡 vs 因子票卡 | 两者都只能写 `blocked`，没有区分 | — |

### 1.3 不复用的东西

- `issue_dependency` 表（`server/migrations/001_init.up.sql:89-94`）：带 `REFERENCES … ON DELETE CASCADE`，违反本仓“禁 FK / 禁级联”的规则。全仓没有任何 query 或 handler 读写它，菜单注释也只写着“以后会加”（`packages/views/issues/actions/issue-actions-menu-items.tsx:326-329`）。复活它等于新开一条平行的依赖边，和 `close.waiting_on` 重复。**不用。**
- `attribution.KindStageWakeup`：close protocol §5 第 12 条已冻结。

## 2. 领域模型：Blocker Record

### 2.1 定义

**Blocker Record = 一张票自己写下的“我卡住了”这件事。** 它就是已有的 close 记录，只在 `conclusion=blocked` 时多带两个键。它有唯一归属：只挂在写它的那张票上。

另有三种**推导态**（Derived Stall）。它们不落库，由前端从已有字段现算：

| 推导态 | 条件 | 数据来源 |
| --- | --- | --- |
| `review_overdue` | `issue.status=in_review`，`close.at` 超过阈值，且 `next_owner` 在本票上没有 active run | `close.at`、`close.next_owner_*`，加上工作区任务快照（行级 agent 活动指示器已在读） |
| `wake_missed` | `close.waiting_on` 指向的票已进入 `done`/`cancelled`，本票仍非终态 | 被等票的 status。与 scan D1 条件一致（`stagnation_watchdog_scan.go:151-163`） |
| `unclosed` / `drift` | 缺 `close.*` / `close.status ≠ issue.status` | 已有，`close-protocol.ts:64-86` |

阈值：agent 责任人用 30 分钟，与服务端 `watchdogStaleAfter` 对齐（`stagnation_watchdog_scan.go:18`）。member 责任人默认 24 小时。**24 小时是产品取值，需 kk zi 确认**，写成 core 常量，不进服务端。

### 2.2 新增字段（仅两个，都在 `close.*` 下）

| 键 | 值 | 何时必填 | 说明 |
| --- | --- | --- | --- |
| `close.block_kind` | `decision` / `permission` / `external` / `dependency` / `capacity` | `conclusion=blocked` | 卡点类型，决定徽标和“谁能解” |
| `close.block_action` | ≤ 80 字符的一句话 | `conclusion=blocked` | 解阻者要做的**动作**，不是原因叙述。例：“确认是否允许删旧表” |

为什么是扩展，不是平行体系：

- 仍是 `close.*` 扁平 string 键，满足 metadata 键 / 值约束（`server/internal/handler/issue_metadata.go:37`、`:71-76`）。
- 仍由同一个 `closeprotocol.Validate` 校验，同一次收口写入，同一个 `issue_metadata:changed` 实时刷新。
- “卡了多久”复用 `close.at`，“等谁”复用 `close.waiting_on`，“谁能解”复用 `close.next_owner_*`。现有 8 个键都不改义。

新增校验（Stage 2 写进 `closeprotocol.Validate`）：

- `conclusion=blocked` ⇒ `block_kind` ∈ 5 值，`block_action` 非空且 ≤ 80 字符。
- `conclusion≠blocked` ⇒ 两键必须是 `""` 或不存在。这样解除阻塞后旧原因不会残留。
- `block_kind=dependency` ⇒ `close.waiting_on` 非空。等父子 stage 内的票**不许**写 dependency，因为那是屏障的活（与 `issue_waiting_on.go:97-103` 同理）。
- `block_kind=decision|permission` ⇒ `next_owner_type` ∈ {`member`, `agent`, `squad`}。能拍板的必须是一个具体的人或席。

兼容性：新键在 Validate 里只对**新写入**生效。老的 `conclusion=blocked` 记录缺这两个键时，UI 显示为“类型未标注”，不算 `unclosed`，避免老票一夜变红。

### 2.3 生命周期

```text
            写 close(conclusion=blocked, kind, action, next_owner, [waiting_on])
  (无记录) ──────────────────────────────────────────────────────────▶ OPEN
                                                                         │
     解阻方处理后，本票责任人重新收口：                                  │
     conclusion ∈ {delivered, awaiting_review, awaiting_human}           │
     block_kind / block_action 置 ""                                     │
                                                                         ▼
                                                                     RESOLVED
     跨票 dependency 的特例：被等票进入终态
       → server 叫醒等待方（issue_waiting_on.go，已存在）
       → 等待方跑起来后重新收口 → RESOLVED
       → 若 30 分钟没跑 → UI 推导 wake_missed，看门狗 scan D 补 mention
```

- **唯一写者**：本票当前责任人（agent 收口时写），或人类在 UI 改状态。父票责任人**不写**子票的 Blocker Record。
- **解除必须重新收口**，不能只改 status。只改 status 的话，`close.status≠issue.status` 会触发已有的 drift 态，自然暴露出来。
- **验证解除**：UI 用 `close.status === issue.status && conclusion !== "blocked"` 判定已解除。不另设 `resolved_at`，因为 `close.at` 已经是最近一次收口时间。

### 2.4 与相邻概念的边界

| 概念 | 它是什么 | 是否 Blocker | 显示 |
| --- | --- | --- | --- |
| 进行中 `in_progress` | 有人在做 | 否 | 无徽标；scan B 命中（30 分钟无 run）才推导为停滞，归入 `wake_missed` 同类的“无人在跑” |
| 审核中 `in_review` | 交付了，等验收 | **否**，除非 `review_overdue` | 中性“待 X 审核”；逾期才升级为警示 |
| 等待中（跨票） | `waiting_on` 非空 | 是：`kind=dependency`，挂在等待方 | 空心“受阻于 → DENE-N”，根因沿边继续找 |
| 阶段排队 | 后续 stage 在 `backlog`，等前一 stage 关屏障 | **否** | 只在 stage 组头写“排队中，等 Stage N”，不在每张 backlog 子票上画 |
| 运行失败 | run 级事件，`in_progress→todo`（`task.go:5898-5912`） | **否**，它是 run 的事，不是票的事实 | 由已有失败 inbox / 行级活动指示承担；同一票反复额度失败，由调度席收口成 `blocked + capacity` 后才进入本模型 |
| 未收口 / 漂移 | 协议没写全 / 写错 | 否，是协议异常 | 已有 missing / drift 态（DENE-234） |

## 3. 归因规则

### 3.1 节点三态

对任务图里的每个节点，前端算出一个态：

- **ROOT（根因）**：本票自己有 OPEN 的 Blocker Record 且 `kind≠dependency`；或本票处于推导态 `review_overdue` / `wake_missed`。
- **PROPAGATED（受阻）**：本票自己不是根因，但它的“当前挡路集合”里有 ROOT 或 PROPAGATED 节点。
- **CLEAR**：其余。

“当前挡路集合”（blocking frontier）严格照搬服务端屏障语义，不自创：

1. 父票的子票里若有 staged 子票：取**最低的未全终态 stage** 里的子票。unstaged 子票**不挡屏障**（`issue_child_done.go:505-512`），归入“旁路卡点”，照样列出，但标“不影响阶段推进”。
2. 子票全部 unstaged：全部非终态子票都挡（`issue_child_done.go:513-519`）。
3. 本票 `close.waiting_on` 非空：被等票也在挡路集合里（跨票边）。

### 3.2 聚合算法（`deriveBlockerTree`，纯函数，放 `packages/core/issues/`）

```text
输入：root issue、children-by-parent 查找、issue-by-identifier 查找、活动快照、now
输出：每个节点 { state, rootCauses: IssueRef[], frontierStage, sideBlockers, userActionCount }

walk(node, visited, depth):
  if node ∈ visited or depth > 4: return { state: CLEAR, cycle: node ∈ visited }
  own = ownStall(node)                      // OPEN record(kind≠dependency) / review_overdue / wake_missed
  frontier = blockingFrontier(node)         // §3.1 的 1–3
  childResults = frontier.map(walk)
  roots = own ? [node] : []
  roots += childResults.flatMap(r => r.rootCauses)
  state = own ? ROOT : (roots.length > 0 ? PROPAGATED : CLEAR)
  return { state, rootCauses: dedupe(roots), ... }
```

规则：

- **父票不复制文本**：父票节点只持有 `rootCauses` 的引用（issue id），原因文字永远从根因票的 `block_action` 读。
- **去重**：同一根因经两条路径到达（子票和孙票都等同一张票），只计一次。
- **环**：`waiting_on` 可以成环（A 等 B，B 等 A）。`visited` 截断，并把环本身作为一个 ROOT 类型 `cycle` 报出来，因为这本来就是需要人拆的死锁。
- **深度**：默认 4 层。超出时显示“更深处还有卡点，点开查看”，不做无限递归请求。
- **懒加载**：直接子票用已有的 `childIssuesOptions`（`packages/core/issues/queries.ts:497`）。只对状态为 PROPAGATED 候选的子票（非终态且自己也有子票，用 `childIssueProgressOptions` `:483` 判断有子票）再拉下一层。跨票 `waiting_on` 用已有的按 identifier 取详情查询。不新增后端接口。

### 3.3 根因排序（父票摘要里谁排第一）

1. 需要**人类**动手的（`next_owner_type=member`，或 `kind ∈ {decision, permission}`）
2. 在**挡路集合**里的，排在旁路卡点前面
3. `close.at` 越早越靠前（卡得最久）
4. stage 越小越靠前

### 3.4 阻塞归属决策表

判定按行从上到下，**第一条命中**生效。“记录写在哪”指谁写 Blocker Record；“显示在哪”指 UI 在哪画实心 / 空心徽标。

| # | 场景 | 记录写在哪 | `block_kind` | 子票显示 | 父票显示 | 任务图边 | 用户下一动作 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | **单根因**：一个子票卡住，兄弟都在正常跑 | 该子票 | 按实际 | 实心 ROOT + `block_action` | 摘要卡 “1 个根因 → DENE-子”；父票行空心 | 父 ⇠ 子 | 按 `next_owner` 去处理子票 |
| 2 | **多根因**：同一挡路 stage 里 ≥2 个子票各自卡 | 各自子票 | 各自 | 各自实心 | 摘要卡按 §3.3 排序列出全部，顶部写“N 个根因，其中 M 个需要你” | 父 ⇠ 每个子 | 从第一条开始 |
| 3 | **深层根因**：孙票卡住，子票因此卡住 | **只**写孙票；子票不写 blocked | 按实际 | 孙票实心；子票空心“受阻于 → 孙” | 摘要卡直接列孙票（跨层上浮），附路径“Stage 2 › DENE-子 › DENE-孙” | 父 ⇠ 子 ⇠ 孙 | 直接去孙票 |
| 4 | **父票自身阻塞**：与子票无关（如父票等 kk zi 定方向，子票还没拆） | 父票 | `decision`/`permission`/… | 子票无变化 | 父票自己实心 ROOT；摘要卡第一行是“本票自身” | 无 | 处理父票 |
| 5 | **父票因子票卡而写 blocked**（协议误用） | 不应写。Validate 拒绝 `kind=dependency` 且 `waiting_on` 指向自己子票 | — | 子票照常 | UI 以子树推导为准；父票记录标“重复记录，建议移除” | 父 ⇠ 子 | 让父票责任人撤掉重复记录 |
| 6 | **跨票依赖**：等不同家族的票 | 等待方 | `dependency` + `waiting_on` | 等待方空心“受阻于 → DENE-X”；DENE-X 若自己 ROOT，则根因是 DENE-X | 若等待方在挡路集合里，摘要卡列 DENE-X（真正根因），并注“经 DENE-等待方” | 等待方 ⇢ DENE-X（虚线，跨家族） | 处理 DENE-X；若 DENE-X 只是在跑，则“等待中，无需动作” |
| 7 | **被等票已完成，但等待方没醒** | 不写，推导 | `wake_missed` | 等待方实心警示 | 摘要卡列等待方 | 虚线置灰 | 叫醒等待方（看门狗 scan D 也会补） |
| 8 | **阶段屏障排队**：Stage N 未全终态，Stage N+1 在 backlog | 不写 | — | backlog 子票不画徽标 | Stage N+1 组头写“排队 · 等 Stage N”；根因看 Stage N 里的 ROOT | 组头之间一条 stage 边 | 无；看 Stage N |
| 9 | **屏障已关但父票没被叫醒**（scan A barrier） | 不写，推导为父票 `wake_missed` | — | 无 | 父票实心警示“Stage N 已完成，未推进” | — | 叫醒父票责任人 |
| 10 | **人工决策** | 发起问题的那张票 | `decision`，`next_owner=member` | 实心 + “需要你决定：…” | 摘要卡置顶，计入“需要你” | — | kk zi 在该票回复 / 改状态 |
| 11 | **人工验收未逾期** | 不是 Blocker | — | 中性“待 @人 验收” | 摘要卡不计入根因，另起一行“等待验收 N” | — | 无（可选去验收） |
| 12 | **审核逾期**（`in_review` 超阈值且 reviewer 无 run） | 不写，推导 `review_overdue` | — | 实心警示 “审核逾期 3h · @Reviewer” | 列入根因；member 审核人计入“需要你” | — | 叫醒 reviewer / 人去验收 |
| 13 | **外部系统等待**（CI 外的第三方、额度池恢复、账号审核） | 发生的那张票 | `external` 或 `capacity` | 实心 + 预计等待点（写在 `block_action`） | 列入根因，不计入“需要你”，除非 `next_owner=member` | — | 通常无；到点复查 |
| 14 | **缺权限 / 凭据** | 发生的那张票 | `permission`，`next_owner`=能授权的人 | 实心 | 置顶，计入“需要你” | — | 授权 |
| 15 | **旁路卡点**：staged 集合里 unstaged 子票卡住 | 该子票 | 按实际 | 实心 | 摘要卡单列“不影响阶段推进”分组 | 父 ⇠ 子（淡色） | 可延后 |
| 16 | **运行失败**（未收口成 blocked） | 不写 | — | 由已有失败提示承担 | 不计入根因 | — | 看失败详情；反复失败由调度收口成场景 13 |
| 17 | **环** | 不写，推导 `cycle` | — | 环上每个节点实心“循环等待” | 列为一条根因 | 标红虚线 | 人拆环 |

## 4. 任务图交互稿

“任务图”不新开页面。它落在两个已有表面上：issue 详情的**子任务区**（`issue-detail.tsx:3126-3219`），以及表格视图的**层级树**（`table-view.tsx:1826-1880`）。仓里没有图形库依赖，也**不引入** react-flow。原因：节点数少（单父票通常 < 20 个子票），列表缩进加徽标就足够，还能复用现有的虚拟化、选择、右键菜单。

### 4.1 统一视觉语汇（两方向共用）

| 元素 | 规则 | token |
| --- | --- | --- |
| ROOT 徽标 | 实心圆点 + 类型图标（decision=❓、permission=🔑、external=⏳、capacity=⛽、dependency 不会是 ROOT） | 人需动手：`bg-destructive/10 text-destructive`；非人：`bg-warning/10 text-warning` |
| PROPAGATED 徽标 | 空心环 + “受阻于 → DENE-N”，点击跳到根因 | `text-muted-foreground`，`border` |
| 审核中（未逾期） | 不用警示色，保持现有中性 chip | `bg-muted/60` |
| 逾期 | ROOT 徽标 + “逾期 3h” | 同 ROOT |
| stage 组头 | `Stage 2 · 挡路中` / `Stage 3 · 排队` / `Stage 1 · 已完成` | 挡路中用 `font-medium text-foreground`，排队 / 完成用 muted |
| 边 | 列表内用左侧缩进竖线；跨家族 `waiting_on` 用行内“↗ DENE-X”链接，不画跨行连线 | — |
| 字号 | `text-micro` chip，`text-caption` 摘要正文，`text-body` 摘要标题 | `tokens.css` 角色字号 |

选中态加 hover 时，徽标颜色保持不变（CLAUDE.md UI 规则：active 必须在 hover 下仍可辨识）。

### 4.2 方向 A — 根因链路优先

**心智**：“顺着链子找到那个卡住的点。”

```text
子任务 · 5                                               [只看卡点 ▢]
────────────────────────────────────────────────────────────────────
Stage 1 · 已完成                                               2/2
Stage 2 · 挡路中                                               1/3
  ● DENE-301 迁移旧数据              ❓ 需要你决定：是否允许删旧表   3h
  ○ DENE-302 前端接入                受阻于 → DENE-301              40m
  ○ DENE-303 回归测试                受阻于 → DENE-310 ↗            12m
       └ DENE-310（另一家族）         ⏳ 等第三方沙箱开通             1d
Stage 3 · 排队 · 等 Stage 2
  · DENE-304 发布
```

- 徽标：根因实心 ●，受影响空心 ○。跨家族根因以缩进子行“内联展开”一层（深度 ≤ 4）。
- 详情面板：点 ● 在右侧 `Sheet` 打开根因卡。内容是 `block_action`、类型、`next_owner`、卡了多久、证据评论（跳转 `close.evidence_comment_id`），以及“去处理”按钮（跳根因票）。
- 过滤：`只看卡点` 开关，只保留 ROOT / PROPAGATED 行和它们的 stage 组头。
- 排序：组内 ROOT 在上，PROPAGATED 次之，CLEAR 最后；同级按 §3.3。
- 超时提示：chip 右侧相对时间，超阈值变 ROOT 色。
- 下一动作：每个 ● 行末一个“处理”快捷按钮。

优点：因果一眼可见，深层根因不被埋。缺点：行数变多；父票在 collapsed 时什么都看不到。

### 4.3 方向 B — 父任务摘要优先（推荐做 MVP）

**心智**：“先告诉我整体卡没卡、要不要我动手，再给我入口。”

```text
┌ 卡点 · 3 个根因 · 其中 1 个需要你 ─────────────────────── 展开 ▾ ┐
│ ❓ 需要你决定：是否允许删旧表          DENE-301 · Stage 2 · 3h    │
│ ⏳ 等第三方沙箱开通                     DENE-310 ↗ 经 DENE-303 · 1d │
│ ⚠ 审核逾期：@代码审查-孙悟空            DENE-305 · 旁路 · 2h      │
│ 等待验收 1（不算卡点）                                            │
└───────────────────────────────────────────────────────────────────┘
子任务 · 5
Stage 2 · 挡路中                                               1/3
  DENE-301 迁移旧数据     ● 根因                (现有 close 条带)
  DENE-302 前端接入       ○ 受阻于 DENE-301     (现有 close 条带)
  ...
```

- 摘要卡位置：子任务区标题下方，**在折叠开关外**，因为子任务折叠时它仍要可见（与 `SubIssuesAgentWorkingChip` 折叠仍可见同理，`sub-issues-agent-working-chip.tsx:21-26`）。
- 行上只加一个 ● / ○ 小徽标，其余复用 DENE-234 的条带，不重复字段。
- 详情：点摘要里的一行 = 跳到根因票（跨层直达）。点“展开”列出路径 `Stage 2 › DENE-303 › DENE-310`。
- 过滤：摘要卡本身就是过滤结果，不另加开关。
- 排序：§3.3。
- 超时：每行右侧相对时间。卡片标题计数随实时事件刷新（`issue_metadata:changed` 已驱动 children cache）。
- 无卡点时：卡片**不渲染**（不放“一切正常”占位），避免噪音。

**选 B 的理由**（按判据，不按好看）：

| 判据 | A | B |
| --- | --- | --- |
| kk zi 打开父票，3 秒内能回答“要不要我动手” | 要扫行 | **标题直接回答** |
| 与 DENE-234 条带的重复度 | 高（行上再加一串文字） | **低**（行上只加 ● / ○） |
| 子任务折叠时可见 | 否 | **是** |
| 深层根因可达 | 是（内联展开） | 是（摘要跨层直达） |
| 实现面 | 改 `SubIssueRow` 布局 + 行内展开 | **新增一个组件 + 行上一个徽标** |
| 回滚面 | 行布局回退 | **删组件即可** |
| 表格层级树可复用 | 徽标规则可复用 | 徽标规则同样可复用 |

结论：MVP 做 B，行徽标用 A 的 ● / ○ 规则。A 的“只看卡点”过滤与行内展开，在 Stage 4 真实验收后按需再做。

### 4.4 与现有前端风格的兼容

- 组件全部来自 `packages/ui/components/ui/`：`badge`、`hover-card`、`sheet`、`collapsible`、`tooltip`。不引新依赖。
- 颜色只用语义 token（`destructive` / `warning` / `muted`），与 `sub-issue-close-strip.tsx:123-126` 同一套。
- 文案进 `packages/views/locales/{en,zh-Hans,ja,ko}/issues.json` 的 `close_protocol` 段（`zh-Hans/issues.json:126-133`）。中文按 conventions 文档口吻。
- 现有 `closeProtocolIsStuck` 改为只对 `blocked` 与推导逾期返回 true，并同步更新 `close-protocol.test.ts`。这是本设计里唯一**改动既有行为**的点，归 Stage 3 票单独断言。

## 5. 机制 / 协议 / 前端拆分

| 层 | 改什么 | 文件 | 必要性 |
| --- | --- | --- | --- |
| fork 机制（server 调度） | **不改**。屏障、waiting_on 唤醒、看门狗语义全部保持 | `issue_child_done.go`、`issue_waiting_on.go`、`stagnation_watchdog*.go` | 模型只读它们的结果 |
| 协议（metadata 契约） | 加 `close.block_kind`、`close.block_action` 常量与 §2.2 校验 | `server/internal/closeprotocol/closeprotocol.go` + `_test.go` | 必需 |
| 协议文档 | §6.1 键表追加两行；§2.5 证据模板加“卡点类型 / 需要的动作”一行 | `docs/kun/scheduling-close-protocol.md` | 必需 |
| agent 运行时约束 | builtin skill 的 Close protocol 节补两键与“父票不抄子票原因” | `server/internal/service/builtin_skills/multica-platform/references/issues.md`（CLAUDE.md 要求同 PR 更新） | 必需 |
| core（前端纯逻辑） | 读两键；`deriveBlockerTree`；阈值常量；修正 `closeProtocolIsStuck` | `packages/core/issues/close-protocol.ts`、新增 `packages/core/issues/blocker-tree.ts` + `// @vitest-environment node` 测试 | 必需 |
| views | `SubIssueBlockerSummary` 摘要卡；`SubIssueRow` 加 ● / ○；stage 组头挡路 / 排队文案 | 新增 `packages/views/issues/components/sub-issue-blocker-summary.tsx`；改 `issue-detail.tsx:896`、`:3198-3204` | MVP 必需 |
| 表格层级树徽标 | 行上 ● / ○ | `table-view.tsx` | 非 MVP |
| 服务端投影（聚合下沉） | 仅当 Stage 4 测到深树懒加载请求过多时再做，参照 working-agents 的 `?parent=` 投影形态 | — | 暂不做 |

### 5.1 最小垂直切片

1. `closeprotocol.go` 认识两键（server 单测）。
2. `blocker-tree.ts` 覆盖 §3.4 第 1、2、3、6、8、11、12 行（node 环境单测）。
3. `SubIssueBlockerSummary` 挂在子任务区标题下（views 单测：一个根因、一个“需要你”、无卡点不渲染）。
4. 在 DENE-229 家族或一个新建演示父票上，写一条真实 `blocked + decision` 记录，截图验收。

### 5.2 回滚

- 前端：revert 该 PR。摘要卡与徽标消失，DENE-234 条带不受影响。
- 协议：revert `closeprotocol.go` 的校验。已写入的两个键作为未知 metadata 留在票上，无害，服务端不读它们调度。
- 无 migration，无 dual-write，无调度语义变化。

## 6. 模型外版比较计划

目的：拓宽方向 A / B 之外的交互候选。**输出只作为候选**，由架构席按 §6.3 筛选。

### 6.1 参与档位（读 `multica-squads/tiers.json`，不写死模型名）

- `dispatch` 档（当前 DeepSeek V4.1 Flash）：快速出 3 个差异化方案。
- `reason` 档（当前 GPT-6 Astra）：出 2 个方案，并对 A / B 做反驳。
- `vision` 档：只用于对比截图，不出方案。

执行方式：由 Scout 席开子票，分别以 model-tiers 换档跑。各档互不可见对方输出，防止收敛成同一答案。

### 6.2 提示词（统一输入）

```text
你是 Multica 前端交互设计师。只输出方案，不写代码。

## 已知约束（不可违反）
- 只能使用这些数据：issue.status、issue.stage、parent_issue_id、last_activity_at，
  metadata 中 close.conclusion / close.status / close.next_owner_type / close.next_owner_id /
  close.waiting_on / close.at / close.block_kind / close.block_action。
- 不得新增状态值；in_review 默认不是阻塞。
- 阻塞原因只存在于根因票上，父票不得复制原因文字。
- 只能用这些组件：badge, hover-card, sheet, collapsible, tooltip, 列表缩进。不引入图形库。
- 表面：issue 详情的子任务区（已有按 stage 分组的列表 + 每行一条 close 状态条），表格层级树。
- 颜色只用语义 token：destructive / warning / muted / foreground。

## 场景（必须每个都展示）
[粘贴本页 §3.4 第 1、3、6、8、10、12 行，以及一个 5 子票 3 stage 的示例数据 JSON]

## 输出格式（每个方案）
1. 一句话心智模型
2. ASCII 线框：父票打开时 / 子任务折叠时 / 无卡点时
3. 节点徽标、边、详情面板、过滤、排序、超时提示、用户下一动作 各一行
4. 与“每行 close 状态条”的字段重复清单
5. 你认为本方案最可能失败的用户场景
```

### 6.3 比较维度与淘汰标准

打分维度（每项 1–5）：

| 维度 | 怎么测 |
| --- | --- |
| 3 秒判断 | 给 kk zi 看线框，问“要不要你动手、先处理哪张”，计时 |
| 根因可达步数 | 从父票到深层根因票的点击次数（≤ 1 满分） |
| 字段重复度 | 与 DENE-234 条带重复字段数（0 满分） |
| 折叠可见 | 子任务折叠时是否仍能看到“有卡点” |
| 实现面 | 需改的文件数 / 是否要新接口 |
| 场景覆盖 | §3.4 六个指定场景是否都能表达 |

**直接淘汰**（任一命中）：

- 需要新状态值、新表，或需要服务端新增调度逻辑。
- 在父票上复述子票原因文字。
- 把 `in_review` 画成阻塞色。
- 引入图形库 / 自由画布。
- 必须展开所有层级才能看到根因。
- 使用硬编码颜色或 Tailwind 默认字号梯度。

幸存方案进入 `prototype` 票，由 kk zi 看可点原型选型。“看起来更漂亮”不单独构成选择理由。

## 7. 实现路线（拆票建议）

本票 DENE-281 即 Stage 1。以下为后续子票建议，由项目编排席收敛后创建。

| Stage | 票 | 依赖 | 主责 | 验收命令 | 产物 |
| --- | --- | --- | --- | --- | --- |
| 1 | DENE-281 设计（本票） | — | Architect | `git diff --check` | 本页 |
| 2 | 契约：`close.block_kind` / `close.block_action` 校验 + 协议文档 + builtin skill | Stage 1 合并 | Builder | `cd server && go test ./internal/closeprotocol/... ./internal/service/... -count=1` | PR：`closeprotocol.go`、`_test.go`、`scheduling-close-protocol.md`、`references/issues.md` |
| 2 ∥ | core：`blocker-tree.ts` + `close-protocol.ts` 读两键 + 修正 `closeProtocolIsStuck` | Stage 1（只依赖本页键名，与上票并行） | Builder | `pnpm --filter @multica/core exec vitest run issues/blocker-tree.test.ts issues/close-protocol.test.ts`；`pnpm --filter @multica/core typecheck` | PR：纯函数 + §3.4 矩阵测试 |
| 3 | 前端 MVP：方向 B 摘要卡 + 行徽标 + stage 组头文案 + 四语言文案 | Stage 2 两票 | Builder（前端可换 `reason` 档） | `pnpm --filter @multica/views exec vitest run issues/components/sub-issue-blocker-summary.test.tsx issues/components/sub-issue-close-strip.test.tsx`；`pnpm --filter @multica/views typecheck` | PR + 截图 |
| 3 ∥ | 模型外版比较（§6） | Stage 1 | Scout | 无代码；比较表 | 评论附比较表与幸存方案 |
| 4 | 真实任务验收：在真实父票构造 §3.4 第 1、3、6、10、12 行，Desktop 截图；kk zi 3 秒判断测试 | Stage 3 | Operator + kk zi | `make up C=api,web`；实页截图 | 截图 + 验收评论；决定是否做表格树徽标与方向 A 过滤 |

每票收口仍按 close protocol 五要素。Stage 2 两票并行，符合 §9.1“共享上游、无资源冲突 → 同一 stage”。

## 8. 不建议做的方案

| 方案 | 为什么不做 |
| --- | --- |
| 父票也写一份 `blocked` + 原因 | 重复录入。子票解阻后父票原因残留，两处漂移，正是 G7 的反面 |
| 父票“吞掉”子票事实（只在父票显示，子票不显示） | 真正责任人打开子票时看不到自己是根因，下一动作落不到人 |
| 新增状态 `waiting` / `stalled` / `review_overdue` | 违反 `issuestatus` 七类行为等价的模型（`server/internal/issuestatus/issuestatus.go:3-16`），会影响看板列、失败回滚、看门狗 |
| 复活 `issue_dependency` 表做依赖边 | FK + 级联违反 DB 规则；与 `close.waiting_on` 平行 |
| 把 `in_review` / `blocked` 加进屏障终态 | close protocol §3.2 明令禁止，会让未验收阶段直接晋升 |
| 服务端落库“逾期”标记 / 定时写 metadata | 推导值落库必漂移；看门狗已经负责叫醒，UI 只需现算 |
| 用 JSON 对象存 Blocker Record | metadata 值只允许 primitive（`issue_metadata.go:71-76`） |
| 引入 react-flow 画自由图 | 节点少，画布增加认知负担，丢失列表已有的选择、编辑、右键能力 |
| 看门狗命中后自动改状态 / 自动改派 | close protocol §7 禁止 |
| 为每种场景加一条新规则键 | 场景由 §3.4 用 5 个 kind × 2 条边 × 3 个推导态组合表达，不按场景加键 |

## 9. 未决（需拍板）

1. 人类责任人审核逾期阈值：本页默认 24 小时。
2. `capacity` 是否计入“需要你”：本页默认不计，因为额度池恢复不需要人动手；换档由调度席决定。
