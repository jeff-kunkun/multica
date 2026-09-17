# 内置需求对齐技能集与复合配置（DENE-512）

本页记录 DENE-512 的落地形状：对齐会话从「一场一个 policy」变成「一场一组技能」，
以及新建对齐时的机器 / Model / Think Level / 技能四件事为什么收进同一颗药丸。

前置阅读：`docs/design/frontend-look-in-issue-alignment.md`（DENE-424，载体能力核查）、
`docs/design/issue-draft-frontend-alignment.md`（DENE-426）。本页取代它们当中
「一个 policy 一场对齐」的部分。

## 0. 一页结论

1. **对齐载体跑的不再是一个 policy，而是一组技能。** 三个键：`grill`（需求拷问）、
   `wayfinder`（决策寻路）、`frontend`（前端先看见）。任意组合，至少一项。
2. **prompt 是组合出来的**：共享线格式契约 + 每个启用技能的方法，按注册表顺序拼接。
   顺序与用户勾选顺序无关 —— 记录下来的集合必须唯一决定 prompt。
3. **审计记录不加表、不加列**：`issue_draft.policy_key` 存 `+` 连接的技能集
   （如 `frontend+grill`），`policy_version` 存同样连接的各自版本（如 `1+4`）。
   DENE-512 之前的行是一个裸键 + 一个裸版本，按 `+` 切开就是它自己的答案，
   所以**不需要迁移**。
4. **Model 与 Think Level 从「页面事后可改」变成「建会话时冻结」**：守护进程是从
   载体 agent 行上读这两个值的，会话建完再写就是首轮从未跑过的值。两者与机器一起
   放进底栏的复合配置浮层。
5. **`grilling` 不是第四个开关**，它是 `grill` 技能自带的方法（前沿轮、每题带推荐答案、
   未共识不建单）；`grill` 的描述里已经写明「接续盘问用 grilling」。

## 1. 技能集替代 policy

旧模型（迁移 489 / DENE-454）：`policy_key ∈ {question, conversation, frontend}`，
一场对齐一条记录，切换即覆盖。它回答不了 DENE-512 的问题：需求可以**同时**需要
把路线谈清、把界面看见、把措辞问到收敛，而三者不是互斥选项。

新模型：

| 技能 | 版本 | 方法要点 | 对应 kun-agent-mono 技能 |
| --- | --- | --- | --- |
| `grill` | 4 | 一轮一问、每题带推荐答案、可关；屏幕归属与方向由用户拍板 | `grill` + `grilling` |
| `wayfinder` | 1 | 决策地图（目的地 / 已经在手 / 下一步可决 / 还说不清 / 范围外），前沿轮推进 | `wayfinder` |
| `frontend` | 1 | 一屏一档 / 五个结构方向，产出可打开的 HTML 附件 | `grill-frontend-look` |

版本号沿用旧值，是因为**方法正文没有改**：记了 `question@4` 的对齐确实跑过
`grill@4` 组合出来的那份 prompt，逐字相同。`wayfinder` 是新增，所以从 1 起。

载体能力的三条前提（DENE-424 §2 实测）不变：能写工作目录、能把 HTML 作为回复附件
交出来、附件在对齐页里是内联 iframe。`wayfinder` 的产物因此落在**回复里**而不是
tracker 里 —— 载体没有 project、没有仓库，契约第一条也禁止它创建任何东西；
它说的「decision ticket」就是这场对齐最终建出来的子单。

## 2. 为什么不是四个开关

DENE-512 正文写「3 项对齐技能（wayfinder/grill/grill-frontend-look）」，而
「内置 `wayfinder`、`grill`、`grill-frontend-look`、`grilling`」列了四个。
合起来读只有一种自洽解：`grilling` 是 `grill` 的方法正文，不是并列的第四个开关。
把「拷问」和「拷问的方法」做成两个复选框，用户没有任何办法只开其中一个 ——
所以它进 `grill` 的 prompt，UI 只给三个。

## 3. 复合配置浮层（`AlignmentConfigPicker`）

底栏原来只有一颗机器药丸（DENE-443），Model 与 Think Level 在**对齐页**上，
而那两个值在会话创建时就写死在载体 agent 行上了 —— 页面上的控件改不动它。
所以它们不是「后来加的设置」，是**建会话时的选择**，和机器同一个决定。

一颗药丸四段：

1. **执行机器** —— 与 `RuntimePicker` 同一份机器行数据，只是收进浮层。
2. **Model** —— 来自所选机器的模型目录；`supported=false` 的机器照实说「由运行时托管」，
   不假装有一个可选项。
3. **Think Level** —— 档位来自**所选 Model** 的 `thinking.supported_levels`，不是来自
   机器。这正是三段必须在同一处的原因：换机器重置 Model 与 Think，换 Model 重置 Think。
   空串 = 跟随本机 CLI 配置，是合法选项。
4. **对齐技能** —— 三个复选框，默认只开 `grill`（「先对齐需求」一向的含义）。
   服务端拒绝空集，所以最后一个勾是禁用的并写明原因，而不是接受一次注定 400 的点击。

技能集落在 create draft 的 `align` 槽里（持久化，因为对齐页可以中途改），
机器 / Model / Think 只活在组件 state（一旦建会话就再也改不了，持久化等于给下一次对齐
提供一个这场会话兑现不了的选项）。

## 4. 对齐页上的同一个集合

对齐页 ⋯ 菜单里的三选一 radio 换成同一组复选框（`IssueDraftPolicyPicker`），
写回 `PATCH /api/issue-drafts/{id}/policy`，body 从 `{policy}` 变成
`{skills: [...]}` —— 送整集而不是增量：prompt 是集合的函数，增量会让它变成历史的函数。
客户端不认识的键（服务端跑着这个版本没有的技能）**显示成一行不可点的已启用项**，
而不是静默丢掉：丢掉会让界面谎报载体实际拿到的 prompt。

## 5. 兼容

- **旧客户端**仍然发 `{policy: "question"}`。服务端在 `skills` 缺席时读它，
  并把 `question → [grill]`、`conversation → []`、`frontend → [frontend]` 归一化。
  即 `conversation` 这条路仍被接受，只是客户端不再提供它 —— 旧装机版不会因此报错。
- **旧草稿行**照常读得懂：`policy_response` 把裸键按 `+` 切开，得到一项技能和它的版本，
  `guided` 回落到该技能自己的声明。
- **新客户端 + 旧服务端**：`policy.skills` 缺失时 `readIssueDraftSkills` 退回解析
  `policy.key`，仍然只显示得出来的那些技能。

## 6. 回滚

还原 `issue_draft_policy.go` 的注册表与 `issue_draft.go` 的归一化调用即可。
记了新键的草稿继续报它们实际跑过的版本 —— 正确，无迁移。前端回滚要把
`policy.skills` 的读取一并还原，否则旧后端下复选框会全空（`key: ""` 时控件本来就隐藏）。

## 7. 附：验收证据

- 原型（可交互）：`docs/design/prototypes/alignment-config.html`
- 真实组件在浏览器里的四段与联动：`alignment-config-panel.png`、`alignment-config-linked.png`
- 真实 app 里对齐弹窗底栏：`alignment-config-in-app.png`
