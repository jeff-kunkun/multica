# 前端做法进对齐：契约形状、进入条件与落地切片（DENE-426 / 设计）

本页是 DENE-426 的设计稿，属于 DENE-421 的阶段 1 第 3 条。它**不改**生产代码，只给出契约形状、进入条件、最小字段集、版本与兼容结论、与 `grill-frontend-look` 的边界，以及可独立开票的落地切片。

核对基线：`origin/kun` @ `d2dbed91f9`（2026-09-17）。下文行号指向该提交。

## 0. 一页结论

1. **不加结构化字段，不加第三档 policy。** 前端做法写进 `description` 的一个**固定标题段**，子单的屏规格写进该子单自己的 `description`。选项 (b)，但不是"随便提一嘴"的 (b) —— 段落标题是定死的，所以它可被人和机器检出。
2. **理由是"结构要有读者"。** `children` 值得结构化，是因为服务端真的拿它建行、stage 真的门禁派单、assignee 真的绑人。UI 字段的唯一消费者是**读 Markdown 的人或实现 Agent** —— 结构化之后仍然要在 finalize 时渲染回 description，中间那层 merge 语义、面板编辑器、zod schema、桌面端版本漂移全是净成本。
3. **"第三档 policy"是把两条正交的轴复用成一条。** `question` / `conversation` 区分的是**怎么问**，不是**问什么**。加一个 `frontend` 键会让用户在"谈前端"时失去"引导/不引导"的选择，并且会撞坏 `issue-draft-policy-picker.tsx` 的单选组（详见 §3.3）。
4. **进入条件由 carrier 判，人可以一句话推翻，不加开关、不加端点、不加状态。** 判据写死成一句可检验的话：**这个需求里有没有哪一条产出会改变人在屏幕上看到或做的事**。拿不准时**默认按"有"处理** —— 误入的代价是一个 turn，漏判的代价是一整轮返工。
5. **版本：共享契约动了，两个条目一起 bump 到 `3`**，和 v2 加 `children` 时一模一样。已有的 v2 草稿**不升级**，继续跑 v2 prompt、继续上报 `2`；这是正确的审计行为，不是遗留问题。`issueDraftPolicyResponseFromRow` **不改**。
6. **边界一句话：对齐产出"够开工的 UI 说明"，不做候选筛选。** 五方向 / 一屏一档留在实现阶段，因为它的产物必须是**可打开的 HTML**，而对齐是一场纯文本对话 —— 让文字去做"必须看见才能决定"的事，是把 `grill-frontend-look` 存在的理由本身给否了。
7. **落地是 3 个切片，全部只改 `server/internal/handler/issue_draft_policy.go` 一个文件加测试**，零迁移、零新字段、零前端改动。

---

## 1. 现状核对

### 1.1 契约里确实一个字都没提界面

`server/internal/handler/issue_draft_policy.go` 的 `issueDraftContract` 是两份 policy 共享的那一半。它规定了 `<issue_draft>` 块的形状、`children` 的拆分规则、key 的稳定性、stage 的含义、`assignee_hint` 的写法。关于 UI 的全部内容是：

```
- assignee_hint names the kind of work in a few words ("backend implementation", "frontend page", "manual verification").
```

`"frontend page"` 是整份契约里唯一和前端沾边的字符串，而它只是一个**举例**，说的是"怎么描述一类活"，不是"这一屏长什么样"。

`description` 的要求是：

```
- description is Markdown: the problem, the acceptance criteria, and the constraints that are already known.
```

"已知的约束"在理论上覆盖 UI，在实践中不覆盖 —— 因为没有任何一句话告诉 carrier **界面也是需求的一部分**。于是对齐谈完"要做什么"就停了，"长什么样、从哪进、空态怎么办"全部丢给实现方猜。

### 1.2 两份 policy 区分的是"怎么问"，不是"问什么"

| 条目 | Guided | 行为 |
| --- | --- | --- |
| `question` | true | 一次一问，带 2-4 个选项，有一个 recommended |
| `conversation` | false | 不面试，直接提方案并说出假设 |

两条都是**问法**。没有任何一条限定话题范围 —— 这正说明"谈前端"不该以第三个条目的形式进来（§3.3 展开）。

### 1.3 已经就位、不需要动的东西

- `description` 是 `IssueDraftPatch` 的普通字段，patch 覆盖、缺省保留（`protocol.ts:parseIssueDraftBlock` / `mergeIssueDraftPayload`）。UI 说明写进去**不需要任何新的合并语义**。
- 子单的 `description` 由 `parseIssueDraftChildren` 原样收下（只有 `title` 为空才丢行）。
- `maxIssueDraftBytes = 256 * 1024`（`issue_draft.go:41`）。多写几屏规格离上限差着两个数量级，不是约束。
- `TestIssueDraftPolicyRegistryIsWellFormed` 是**遍历注册表**写的，加段落不需要改它；它已经断言了每个条目的 `Instructions()` 都带 `<issue_draft>`。

---

## 2. 进入条件：谁判、怎么判、判错了会怎样

### 2.1 结论

**carrier 判，人可以一句话推翻。不加开关、不加端点、不加草稿上的状态位。**

判据写进契约，是一句可检验的话：

> A request has a surface when any outcome in it changes what a person sees or does on a screen; a request that only changes data, jobs, APIs or infrastructure has none.

这句话之所以能用，是因为它**指向草稿本身**（草稿里的产出条目），而不是指向 carrier 的直觉。审计"当时为什么没问前端"时，能拿着草稿逐条对。

### 2.2 为什么不是"用户显式切换"

用户显式切换就是把 kk 的原始抱怨换个按钮重演一遍。他的原话是"对齐好，前端如何做也在 grill frontend 里面对齐"—— 问题不是**没地方谈**，问题是**谈完需求之后没人提这茬**，得他自己另起一轮。一个需要用户先知道它存在、再主动点开的开关，解决不了"没人提这茬"。

而且它会引入一个新的错误态：用户忘了点开 → 对齐产出一张没有 UI 说明的单 → 和今天一模一样。自动判断至少会失败得**很吵**（多问了一句），手动开关会失败得**很安静**。

### 2.3 为什么不是"每次都问"

纯后端需求（"给 webhook 加重试"、"迁移 486 建个索引"）被强塞 UI 问题，代价是每一场后端对齐都多烧一个 turn，而且**教会用户忽略这类提问** —— 一个总在不该问时问的提示，用户学会的是跳过它，于是它在该问时也被跳过。

### 2.4 代价不对称，所以默认偏向"有"

| 判错方向 | 代价 | 什么时候被发现 | 谁来纠 |
| --- | --- | --- | --- |
| 对纯后端需求强塞 UI 问题 | **一个 turn**。用户回一句"这个没有界面"，carrier 丢掉该段继续。`question` policy 本来就允许用户用自己的话回答，`conversation` policy 本来就不面试。 | 立刻，在对齐里 | 用户，一句话 |
| 对有 UI 的需求漏掉 | **一整轮实现返工**。单建出来了、派单了、Agent 写完了，有人打开一看入口没有 / 空态没有 / 又造了一个和现有组件平行的抽象。 | 实现之后，review 或验收时 | 重新开票 |

不对称是两个数量级的。所以契约里写死**拿不准时按"有"处理**：

> When you cannot tell, assume it has one: write the surface you would build and say you assumed it.

注意措辞是 **assume + say**，不是 **ask**。这样它在 `conversation` policy 下也成立 —— 那条 policy 明令"不要面试"，要求它提问会让两份 prompt 互相打架。把"提问"留给 `question` policy 自己加（切片 B）。

### 2.5 逃生口已经有了

`issueDraftQuestionPolicy` 最后一条：

```
- If the user asks you to stop asking questions, stop for the rest of the conversation
```

用户说"别管前端"就是这条。不需要新机制。

---

## 3. 落在哪：三选一的结论

### 3.1 结论：选 (b)，但标题段是定死的

前端做法写进 `description` 的一个**固定标题段**；一个子单拥有某一屏时，那一屏的规格**同时**写进该子单自己的 `description`。

不加 `IssueDraftPayload` 字段，不加 `<issue_draft>` 块里的新 key，不加第三份 policy。

### 3.2 为什么不是 (a) 扩结构化字段

**结构要有读者。** 对比一下 `children` —— 它值得结构化，是因为下游真的在机读它：

- `issueGroupParamsFromDraft` 拿它建行；
- `issueDraftChildStatus` 拿 `stage` 决定子单建出来是 `todo` 还是 `backlog`；
- `stageBarrierClosed` 拿 `stage` 做派单门禁；
- 预览面板拿 `assignee_id` 绑人，`mergeIssueDraftChildren` 专门为此按 key 保留客户端拥有的字段。

一个 `ui: { screens: [...] }` 字段的下游读者是谁？没有。服务端 finalize 只读 title / description / status / priority / children / project_id / assignee；它最终必须被**渲染成 Markdown 塞进 description**，否则实现 Agent 打开 issue 根本看不到它。

也就是说 (a) 的净效果是：在"carrier 写 Markdown"和"issue 里是 Markdown"之间插一层结构，这一层要付的代价是：

| 成本项 | 具体是什么 |
| --- | --- |
| 合并语义 | `mergeIssueDraftPayload` 要决定 `ui` 是整体替换还是按屏合并。按屏合并就无法表达"删掉一屏"（`children` 的注释已经把这个教训写下来了）；整体替换就要求 carrier 每轮原样带回全部屏 —— 那和 (b) 的要求完全一样，结构没买到任何东西。 |
| 面板编辑器 | 预览面板的存在理由是"对话是个到达草稿的好办法、改错别字的坏办法"。有了结构化 `ui` 就必须给它编辑器，否则它是面板里唯一不能改的字段。那是一整套新 UI。 |
| API 兼容 | CLAUDE.md 的硬约束：新字段要过 `parseWithFallback` + zod schema + 一条 malformed-response 测试。装机的桌面端会拿到不认识这个字段的旧后端，反过来也一样。 |
| 服务端校验 | 屏数上限、名字格式、空值 —— 又一套和 `children` 平行的边界矩阵。 |

买到的是什么？一个 UI 里能画成表格的形状。但对齐页的右栏已经是"确认会创建什么"的镜子，而 UI 说明**不创建任何东西** —— 它是给人读的说明，放在 description 里就是它最终的样子。

**(a) 是结构没有读者时的典型过度设计：付全部代价，买一个渲染。**

### 3.3 为什么不是 (c) 第三份 policy

三条独立的理由，任何一条都够。

**第一，它把两条正交的轴复用成一条。** 注册表区分的是"怎么问"（guided / 不 guided）。加 `frontend` 之后，用户在谈前端时是 guided 还是不 guided？答不上来 —— 因为这是两个维度。用户一旦切到 `frontend`，就**失去了引导/不引导的选择**，而那个选择是 DENE 上一轮专门做出来的。要保住它就得做成二维（一个新列、一个新端点、一个新 picker），成本远超 (b)，而收益仍然是零：话题不需要有状态。

**第二，"谈前端"不是一个状态，是草稿的一个覆盖要求。** policy 是**从下一轮起改变 carrier 行为**的持久设置，`SwitchIssueDraftPolicy` 为此专门拦了在途 turn（409 `stop the current reply before switching policy`）。而"这个需求有前端面"不是一段时期，是**这张单的一个属性** —— 它不需要被切进去也不需要被切出来，它只需要在草稿完成时被满足。把属性做成模式，就得回答"什么时候切回去""切回去之后前端那段还算数吗"这类本不存在的问题。

**第三，它会撞坏现有 picker。** `packages/views/issues/draft/issue-draft-policy-picker.tsx`：

```tsx
export const ISSUE_DRAFT_POLICIES = ["question", "conversation"] as const;  // policy.ts
// picker:
value={policy.key}
{ISSUE_DRAFT_POLICIES.map((key) => (
  <DropdownMenuRadioItem key={key} value={key} …>
    {key === "question" ? t($.alignment.policy_guided) : t($.alignment.policy_plain)}
```

单选组的 `value` 是服务端记的 key，选项却是客户端白名单渲染的。服务端记了 `frontend` 而白名单没有它 → **两个选项都不选中**，header 的 ⋯ 菜单里显示一个谁都没勾的单选组。要修就得同时改 `policy.ts` 的白名单、picker 的三元表达式（它假设只有两档）、以及两套 i18n。这些改动本身不难，难的是它们全都只为了支撑一个不该存在的第三档。

**顺带回答票里的 §4 子问题：** 正因为不选 (c)，`issueDraftPolicyResponseFromRow` 对不再注册的 key 的处理**不需要改**（§5.3 给出完整论证）。

### 3.4 `children` 整集替换对这个方案意味着什么 —— 一条真实的坑

`mergeIssueDraftPayload` 对 `children` 是**整集替换**，`mergeIssueDraftChildren` 只按 key 保留了**客户端拥有的三个字段**（`assignee_type` / `assignee_id` / `assignee_hint`）。`description` **不在其中**。

后果很具体：**carrier 在下一轮重新吐出同一个 key 的子单时，如果 description 写空了或写短了，上一轮的屏规格就没了。** 没有任何兜底。

这不是 bug，是设计：能重写子单描述是对齐的核心能力，merge 层把描述"粘住"会让"改掉这一屏的写法"变得无法表达 —— 和 `children` 整集替换的注释是同一个论证。

所以这一条必须由 **prompt 保证**，写法和 key 的规则并列：

> Once you have written a screen spec into a sub-issue's description, carry it back unchanged in every later block, exactly as you carry the key — the list of sub-issues is replaced whole on every turn, so a spec you do not repeat is a spec you have deleted.

这是切片 C 的全部内容，并且要配一条**钉住 merge 现状**的测试，防止后人"顺手修一下"把描述也做成按 key 保留（§7.3）。

### 3.5 标题用哪种语言：一个明写的取舍

固定标题的价值是**可检出** —— 人在 review 里能一眼看到有没有，将来真有机读需求时它是一个前向兼容的接缝。但产品语音是中文优先（`apps/docs/.../conventions.zh.mdx`），而 prompt 常量全是英文。

三种写法：

| 写法 | 问题 |
| --- | --- |
| 只认 `## Frontend` | 一张全中文的 issue 里插一个英文标题，读起来像模板漏了没翻 |
| 只认 `## 前端做法` | 英文对话产出的 issue 里插中文标题，同样的毛病反过来 |
| **跟随描述语言，prompt 里把两种形式都点名** | 检查从一个字符串变成两个 |

**选第三种。** 检查退化成 `前端做法|Frontend` 的二选一，仍然是一个确定的检查；而前两种买到的那点"单一字符串"，代价是让产出看起来像没做完的模板 —— 一旦用户开始不信任输出的排版，这段说明本身也会被跳读。

---

## 4. 谈什么：最小必须收敛的字段集

### 4.1 五条，一屏一组

`grill-frontend-look` 的单位是 **Screen**：人能停住看完的一张界面。沿用它，一屏一组，每组五行：

| 字段 | 一句话 | 漏了会返什么工 |
| --- | --- | --- |
| **屏名** | kebab-case 短名（`issue-filter-bar`），和 `grill-frontend-look` 的命名对齐 | 谈的时候指不准，两个人以为在说同一屏 |
| **入口** | 人从哪个现有页面 / 菜单 / 路由到达它 | **返工第一名**：屏做出来了，没有任何地方能进去 |
| **主要动作** | 人在这屏上做的那**一件**事，做完看到什么 | 做成了一个能看不能用的展示页 |
| **必备状态** | loading / 空 / 失败各显示什么 | **返工第二名**：只实现了 happy path。`grill-frontend-look` 要求这三态横切全部候选，就是因为它最容易被跳过 |
| **复用什么** | 基于哪个现有页面 / 组件 / 模式，没有就写 "new" | 造出一个和现有组件平行的抽象，撞 CLAUDE.md 的 "Prefer existing patterns/components over new parallel abstractions"，review 时才发现 |

**外加一条屏之外的、整单一次：平台面（web / desktop / mobile）。** 这个仓库有三个面，`packages/views` 是 web/desktop 共享的而 mobile 完全独立（自己的 UI、state、i18n、React 版本）。"顺便也做 mobile"是一整个额外实现，绝不能默认。契约里写死：**没点名的平台就是不做**。

### 4.2 哪些必须由人拍板

两条，其余 carrier 都可以自选并说出假设：

**（1）范围 —— 哪几屏进这张单，哪几屏等以后。** 这是优先级判断，不是技术判断。carrier 可以给出它会选的切法并标 recommended，但必须让人确认 —— 一个自作主张把三屏全塞进来的对齐，产出的是一张做不完的单。

**（2）方向 —— 满足同一需求的多种排布里选哪一种。** 这就是 `grill-frontend-look` 存在的理由：方向糊的时候必须变成可打开的东西。对齐做不到"可打开"，所以它只能做一件事：**把方向问一次，记下来，然后停**。

第二条要配一条明确的止损规则，否则 guided policy 会在观感上没完没了地问：

> Ask it once, with 2-4 named directions — then stop. You cannot show a picture, so a second question about the look buys nothing.

### 4.3 提问时机

有前端面时，**surface 问题要在草稿其余部分定下来之前问**。理由：最后才问的前端面，是一个已经被默认掉的前端面 —— 前面所有轮次都已经按"某种界面"在谈验收标准了，这时再问只是追认。

---

## 5. 版本与兼容

### 5.1 bump 到 `3`，两个条目一起

文件头注释写死了规则：

```go
// Changing an entry's *prompt* means bumping its Version, because the version
// is what a finished conversation points at when someone audits it later.
```

改的是 `issueDraftContract` —— 它是**每份 prompt 的一半**。注册表注释已经为 v2 记过同样的账：

```go
// Every entry moved to version 2 when the shared contract grew `children`: the
// contract block is half of each prompt, so both entries are different prompts
// now, and a draft that recorded "1" was produced by one that could not split.
```

照抄这个先例：**两个条目一起到 `3`**，注释改成"a draft that recorded `2` was produced by a prompt that never asked about the surface"。

### 5.2 已有 v2 草稿：不升级，这是正确行为

**未完成的 v2 草稿被恢复时会怎样？** 继续跑 v2 prompt，继续上报 `2`。

三条链路各自的事实：

- **上报的版本**从草稿行读（`issueDraftPolicyResponseFromRow(d.PolicyKey, d.PolicyVersion)`），注释明写"a conversation keeps reporting the prompt it actually ran after the registry moves on"。所以它报 `2` —— 那正是它跑的东西。
- **carrier 的活 instructions** 在 `agent` 行上，只有 create 和 policy switch 会重写。恢复一场对话不重写。所以它**真的**还在跑 v2。
- **上报值和实际行为一致**。这是审计想要的结果，不是需要修的漂移。

**产出还能解析吗？** 能。新契约加的是 description 里的一个段落要求，`<issue_draft>` 块的形状一个字没动 —— v2 carrier 的输出照样过 `parseIssueDraftBlock`，只是 description 里没有前端那段。

**要不要给一条升级路径？** 不要新建。理由：在一场进行中的对话下面换 prompt，正是 `SwitchIssueDraftPolicy` 的 409 在途 turn 门禁要防的事；而且换完之后记录的版本会开始**替之前那几轮撒谎**。

现成的办法已经有了，写进文档即可：**切一下 policy 再切回来**。`SwitchIssueDraftPolicy` 会重写 carrier instructions 并重记版本，而且 `TestSwitchIssueDraftPolicyRewritesCarrierPromptOnly` 已经钉住了它不动草稿内容、不动 revision、不动 status。这是一条已被测试覆盖的恢复路径，不是新机制。

### 5.3 `issueDraftPolicyResponseFromRow`：不改

票里问它"对不再注册的 key 的处理要不要跟着改"。**不改**，两层理由：

**第一层，这次没有删任何 key。** 该函数的回落分支只在"草稿记了一个注册表里已经没有的 key"时生效。选 (b) 不新增也不删除键，这条分支的触发条件和今天完全一样。

**第二层，即便将来真的删键，现在的行为也已经是对的。** `version` 从行读（有真相就用真相），`guided` 回落 `false`（没有真相时取保守值 —— 面板画成"不引导"，不会去渲染一个其实不存在的问题块交互）。同时 `ISSUE_DRAFT_POLICIES` 这个客户端白名单保证一个陌生 key 不会被当作可切换项列出来。

**只有选 (c) 才会让这个问题变成真问题**：一个被回滚掉的 `frontend` 键会上报 `guided: false`，而 picker 的 `key === "question" ? 引导 : 普通对话` 三元表达式会把它**标成"普通对话"** —— 那不是"少了个标签"，是**标错**。又一条不选 (c) 的理由。

### 5.4 前端零改动

值得单独点出来，因为这是选 (b) 最省的地方：

- 不新增 wire 字段 → 不需要新 zod schema、不需要新 `parseWithFallback` 分支、不需要 malformed-response 测试；
- 装机的旧桌面端拿到新后端 → description 里多一段 Markdown，照常渲染；
- 新桌面端拿到旧后端 → description 里少一段，照常渲染。

**CLAUDE.md 的 API 兼容规则在这个方案下一条都不触发。**

---

## 6. 和 `grill-frontend-look` 的关系

### 6.1 边界，一句话

> **对齐只产出"够开工的 UI 说明" —— 哪几屏、从哪进、主要动作、必备状态、复用什么、落在哪些平台面；候选筛选不进对齐，五方向 / 一屏一档仍然发生在实现阶段。**

### 6.2 为什么这条线画在这里

因为 `grill-frontend-look` 的产物**必须是可打开的**。它自己写死了这条：

> 没有可打开的收成物（一屏 HTML，或 interaction-graph 交回的线）= 本轮没完成。不要口头说「方向定了」就去写生产代码。

对齐是一场纯文本对话，carrier 既不会给用户开窗口，用户也不会在对齐页里点 HTML。**把"必须看见才能决定"的事搬进对齐，等于用文字去做它明说了文字做不了的事** —— 结果不是省了一步，是把那一步做成了假的：一句"方向定了"写进 description，实现方照着做，做出来不对，返工。这恰好是 `grill-frontend-look` 存在的理由本身。

所以两者是**前后衔接**，不是替代：对齐定**什么必须为真**，`grill-frontend-look` 定**它长什么样**。对齐产出的屏清单，正好是 `grill-frontend-look` 的输入（它的"一屏一档"单位就是 Screen，短名 kebab-case，本设计沿用同一套命名就是为了这个衔接）。

### 6.3 这条边界让哪些返工消失

**消失的：**

| 返工 | 为什么消失 |
| --- | --- |
| 做完才发现**根本没人说过有界面** | 契约强制判定并写下判定结果，判错偏向"有" |
| 屏做出来了、**没有入口** | 入口是五行之一，对齐时就得说清从哪进 |
| 只做了 **happy path**，空态失败态在 review 里被打回 | 必备状态是五行之一 |
| 造了一个和现有组件**平行的抽象** | "复用什么"是五行之一，对齐时就点名基于哪个现有件 |
| **"顺便也做 mobile"** 引发的隐性翻倍 | 平台面必须点名，没点名 = 不做 |
| **范围** 在实现中途被重新谈 | 哪几屏进这张单由人拍板，写进单里 |

**不消失的（也不该消失）：**

- 五个结构性不同的候选之间选哪个 —— 那必须看见；
- 组合（"B 的头 + C 的主按钮"）—— 那必须看见；
- 多屏拼成一条 UX 线 —— 那是 `/interaction-graph` 的活，产物是可打开的 flow 图。

一句话：**对齐消灭的是"不知道有这回事"的返工，消灭不了"没见过所以选不出来"的返工 —— 后者本来也不该由对齐消灭。**

---

## 7. 最小可落地切片

三片，全部只改 `server/internal/handler/issue_draft_policy.go` 一个文件加测试。**零迁移、零新字段、零前端改动。**

### 7.1 切片 A：共享契约加「前端做法」段（必需）

**改哪些文件**

- `server/internal/handler/issue_draft_policy.go`：`issueDraftContract` 追加 §8.1 的段落；两个注册表条目 `Version` 从 `"2"` 改 `"3"`；更新注册表上方那段解释版本的注释。
- `server/internal/handler/issue_draft_policy_test.go`：加一条断言 —— 契约里带 surface 判据与五行要求，且两个条目都在 `3`。

**验收怎么看得见**

1. `(cd server && go test ./internal/handler -run 'IssueDraftPolicy' -count=1 -v)` 全绿（`TestIssueDraftPolicyRegistryIsWellFormed` 是遍历写的，自动覆盖新版本）。
2. 起一场对齐，说「issue 列表加个按小队筛选」→ 回复的 `<issue_draft>` 块里 description 含 `## 前端做法`（或 `## Frontend`）及五行。
3. 同一个对齐入口，说「给 webhook 加重试」→ 回复里**没有**该段，且 carrier 说了一句它判定这个需求没有界面。

第 2、3 条是同一张截图能证的事，也是这张设计票唯一真正要看的东西：**判据在真机上分得开**。

### 7.2 切片 B：引导策略把范围与方向交给人拍板

**改哪些文件**

- `server/internal/handler/issue_draft_policy.go`：`issueDraftQuestionPolicy` 追加 §8.2 的段落。
- `server/internal/handler/issue_draft_policy_test.go`：断言 guided prompt 含该段，且 `conversation` prompt **不**含（和现有那条 `<issue_draft_question>` 的正反断言同一个写法）。

**验收怎么看得见**

同一场「加按小队筛选」的对齐里，出现一条带 2-4 个选项的 UI 方向问题块，恰好一个 recommended；回答之后**不再问第二个观感问题**。截图即验收。

### 7.3 切片 C：子单的屏规格必须随 key 原样带回

**改哪些文件**

- `server/internal/handler/issue_draft_policy.go`：`issueDraftContract` 的 `children` 规则里追加 §8.3 的一条。
- `packages/core/issue-drafts/protocol.test.ts`：加一条**钉住 merge 现状**的测试 —— 同一个 key 的子单在下一轮带着空 description 回来时，`mergeIssueDraftPayload` **不会**把上一轮的描述接回去（只有三个 assignee 字段会）。

这条测试是反直觉的，所以它的注释要写清它在保护什么：**它保护的是"重写子单描述"这个能力**。有人日后"顺手修一下"把 description 也做成按 key 保留，就会让"改掉这一屏的写法"变得无法表达 —— 和 `children` 整集替换的注释是同一个论证。规则由 prompt 保证，不由 merge 兜底。

**验收怎么看得见**

`pnpm --filter @multica/core exec vitest run issue-drafts/protocol.test.ts` 绿；一场会拆子单的对齐里，前端子单的 description 带着屏规格，并且在后续几轮里**原样还在**。

### 7.4 版本 bump 的账

A 和 C 改的是**同一段契约**，拆开只是为了 review 粒度。

**建议：A + C 同一个 PR，一次 bump 到 `3`；B 随后单独发，bump 到 `4`。**

B 单独发是因为它改的是**问法**，验收物是截图而不是测试输出，值得一次独立的可见验收。三片全塞一个 PR 也成立（只 bump 到 `3`），代价是 B 的行为变化混在契约变化里，真机上不好归因。

---

## 8. 可直接替换的 prompt 草稿

以下段落按现有常量的语气写（英文、祈使、具体），可直接粘进对应常量。

### 8.1 追加到 `issueDraftContract`（切片 A + C）

```
The user-facing surface is part of the requirement, not a detail left to whoever implements it. A request has a surface when any outcome in it changes what a person sees or does on a screen; a request that only changes data, jobs, APIs or infrastructure has none. Decide which it is before you write the draft, and say in your reply which way you decided. When you cannot tell, assume it has one: write the surface you would build and say you assumed it — a surface you proposed costs the user one sentence to reject, and a surface you skipped is discovered after the work is built.

When the request has a surface, description MUST contain a section headed "## 前端做法" (or "## Frontend" when the description is in English) with one group per screen. A screen is one view a person stops at and reads. Each group is five lines:
- Name: a short kebab-case name for the screen ("issue-filter-bar"), so later turns can refer to it without ambiguity.
- Entry: the existing page, menu or route a person reaches it from.
- Main action: the one thing a person does there, and what they see afterwards.
- States: what the screen shows while loading, when it is empty, and when it fails.
- Reuse: the existing page, component or pattern it is built from, or "new" when there is none.

Name the platforms the surface lands on (web, desktop, mobile) once for the whole request, and treat a platform you did not name as out of scope — each one is a separate implementation.

Do not choose the look. Which of several possible layouts or visual treatments wins is decided by looking at something, not by talking about it. Write down what must be true about the screen and leave how it looks to the implementation.

When a sub-issue owns a screen, repeat that screen's five lines in that child's own description — the child is what someone opens to build it, and a spec that only lives in the parent is one they will not read. Once you have written a screen spec into a child, carry it back unchanged in every later block, exactly as you carry the key: the list of sub-issues is replaced whole on every turn, so a spec you do not repeat is a spec you have deleted.
```

### 8.2 追加到 `issueDraftQuestionPolicy`（切片 B）

```
When the request has a user-facing surface, two things are the user's to decide and yours only to propose:
- Which screens are in THIS issue and which wait for later. That is a priority call, not a technical one. Ask it with the split you would choose marked recommended.
- The direction the surface takes, when more than one arrangement would satisfy the requirement. Ask it once, with 2-4 named directions — then stop. You cannot show a picture, so a second question about the look buys nothing; record the direction that was chosen and leave the rest to be seen while it is built.

Ask the surface question before the rest of the draft is settled. A surface agreed at the end is a surface that was already assumed.
```

### 8.3 `children` 规则处的一行（切片 C，已含在 8.1 末段）

见 §8.1 最后一段。放在契约里 `children` 相关规则附近，与 key 的稳定性规则并列，因为它们坏掉的方式完全一样。

---

## 9. 待拍板项

设计里没有留给实现方的开放问题；下面两条是**产品判断**，需要 kk zi 在实现票开出前定，或默认按推荐值走。

1. **固定标题的措辞。** 推荐 `## 前端做法`（中文描述）/ `## Frontend`（英文描述）。这是会出现在每一张有界面的 issue 正文里的字，属于产品语音，不该由实现方定。默认按推荐值。
2. **切片 B 的方向问题是否默认开启。** 推荐开启（它正是 kk 原话里"也在 grill frontend 里面对齐"的那半）。若认为对齐里问观感价值不大、宁可全留给实现阶段的 `grill-frontend-look`，那就只发 A + C，边界退成"对齐只产出屏清单与约束，方向一律留到看见时定"—— 这仍然是一个自洽的方案，且更省。

其余全部按本页结论执行，不需要再确认。
