# 把 grill-frontend-look 接进需求对齐（DENE-424 / 设计）

本页是 DENE-424 的设计稿。**不改生产代码、不改 handler、不改 prompt 常量、不实现原型生成**，只给出读法、载体能力的核查结果、推荐方案、被否掉的备选、改动草案和待拍板清单。

核对基线：`origin/kun` @ `d2dbed91f9`（2026-09-17）。行号指向该提交。issue 正文写的 `e30393b415` 是两个提交之前，本页所引位置在这两个提交之间没有变化。

## 0. 一页结论

1. **kk 的读法成立**，但要改一个字：不是把 `grill-frontend-look` 整个搬进对齐，而是把它的**判据**搬进来——「没有可打开的收成物 = 这一轮没完成」。方法本身要重写，因为它的收成目标是仓库路径，而对齐载体够不着仓库。
2. **载体能写文件、能交付可打开的 HTML，但拿不到仓库**。这三条都跑命令核实过（§2）。所以收成物落在**对齐回复的附件**上，不是 `docs/design/prototypes/<screen>.html`。
3. **不加第三个 policy key**。`policy_key` / `policy_version` 是一场对齐一条记录、切换即覆盖，加一个 `frontend` policy 会把「这场对齐其余部分跑的是哪个 prompt」这条记录擦掉——而版本号存在的唯一理由就是保住它。前端 grill 是两个现有 policy 里的一个**阶段**，不是第三种 policy。
4. **产物进单走 `description` 里的固定小节，不加 JSON 字段**。`parseIssueDraftBlock` 是字段白名单，新字段不会炸但会被静默丢掉——真正的风险不是解析破坏，是数据无声丢失。
5. **触发权：载体提议 + 用户一键，默认不自动开**。误判成本不对称：该 grill 没 grill 等于回到现状，不该 grill 却拦一道要烧掉 3–8 个昂贵回合去画没人会看的界面。
6. 共享 prompt 变了 → 两个 policy 的 `Version` 一起 `2` → `3`，和 DENE-411 加 `children` 时同一个理由。

## 1. 读法

kk 原话（DENE-366 评论 `01a0ad5a-e2d1-7e33-bb5c-30c617c01c46`）：「对齐好，前端如何做也在 grill frontend 里面对齐」。语音输入，`grill frontend` = `grill-frontend-look`。

**采纳的读法**：需求谈完之后，**界面长什么样**这件事本身也要在对齐里谈掉，并且必须留下一个人能打开的东西——因为 kk 判断进展靠可见产物，而现在对齐的全部输出是文字。

**明确推翻的那半**：不把 `grill-frontend-look` 原样接进来。理由是它的收成契约写死在仓库路径上——

```
1. 收成一屏 — 把选定或组合后的方向提取到 `docs/design/prototypes/<screen>.html`。
   ... `design-reference.md` 写 question、verdict、路径。
2. 收成一条线 — ... 写 `INTERACTION.md` 的 flow，并把屏按 walk 拼成
   可打开的 `docs/design/flows/<flow>.html`。
```

（`multica skill get 99474c5f-833f-4b15-aa85-6c831d2e4a92 --with-content --output json` 的正文）

这三个路径对齐载体一个都写不到（§2.3）。所以接进来的是**判据和考法**，不是路径：

- 考法保留：五个结构方向（同一 surface、结构真不一样、picker 切换、desktop/mobile 都能看、loading/empty/error 横切候选）与一屏一档（Screen 为单位、kebab-case 短名）。
- 判据保留：**没有可打开的收成物 = 这一轮没完成**；不许口头说「方向定了」。
- 路径替换：收成物 = 对齐回复上的一个 HTML 附件（§3.2）。
- 「收成一条线」**不接**：它硬依赖 `/interaction-graph` 去写仓库里的 `INTERACTION.md`。对齐载体没有仓库，也不该在建单之前维护产品 UX 链。一条线属于实现阶段。

一句话：**对齐负责把「哪一屏、什么形状、有哪些状态」变成一个能点开的东西；`anti-slop-frontend` 负责把它实现成生产页。** 后者的描述本来就写着「当……方向已经能看见时触发」——现状里没有任何一步负责让它「能看见」。

## 2. 载体能力核查（有证据）

对齐载体是每场会话独建的隐藏 system agent：`server/internal/handler/issue_draft.go:264`（`CreateAgentBuilder`，`kind='system'`、`system_key='issue_draft:<uuid>'`），配套 chat session 在 `issue_draft.go:282`。

### 2.1 它是一个普通 chat task，有真实工作目录 —— 能写文件

对齐的每一轮就是一次普通的 chat 发送：`packages/core/issue-drafts/mutations.ts:79` 调的是 `api.sendChatMessage(draftId, ...)`，和其它任何会话同一条路。`/api/issue-drafts` 路由里**没有**发消息的端点（`server/cmd/server/router.go:2161-2183`）。

chat task 在守护进程侧和 issue task 走同一个 `runTask`：工作目录来自 `execenv.PrepareParams.LocalWorkDir`，运行时简报和技能文件无条件写进任务目录（`server/internal/daemon/daemon.go:8785`）。`server/internal/daemon/local_directory.go:180-193` 的注释直接写明 chat 轮次「at most saves a file the way the user's own Cmd+S does」——它跳过的是路径互斥锁，不是写权限。

磁盘上的实物证据（跑过的命令）：

```
$ cat ~/multica_workspaces_desktop-api.multica.ai/deneb-3029-bb694f80b4b9/task-01aeb2995729/.gc_meta.json
{"kind":"chat","chat_session_id":"01a09673-50b5-7a1d-86c2-7c23b80bfb26",
 "task_id":"01a09673-516b-7af7-8a23-01aeb2995729", ...}

$ ls ~/multica_workspaces_desktop-api.multica.ai/deneb-3029-bb694f80b4b9/task-b55b41d56325/codex-home/skills/
anti-slop-frontend  browser-tools  code-review  codebase-design  diagnosing-bugs
domain-modeling  end-work  handoff  implement  improve-codebase-architecture
model-tiers  multica-fork-sync  multica-platform  refresh-project  research
show-me  start-work  tdd  use-docker  worktree-local-state
```

第二条是关键：一个 **chat** 任务的环境里，守护进程已经把 20 个技能目录写进了磁盘。环境是可写的，机制是现成的。

单测侧（跑过）：

```
$ cd server && go test ./internal/daemon \
    -run 'TestChatTaskSkipsPathMutexButKeepsAssignment|TestManagedArtifact_LocalDirectoryChatReclaimsSandboxBin|TestShouldReusePriorWorkdirChatAcceptsMatchingConversation' \
    -count=1 -v
--- PASS: TestShouldReusePriorWorkdirChatAcceptsMatchingConversation (0.01s)
--- PASS: TestManagedArtifact_LocalDirectoryChatReclaimsSandboxBin (0.01s)
--- PASS: TestChatTaskSkipsPathMutexButKeepsAssignment (0.01s)
ok  	github.com/multica-ai/multica/server/internal/daemon	0.689s
```

工具权限上也没有额外收紧：载体行的 `permission_mode` 是 `'private'`，但那是 MUL-3963 的**调用权限**（谁能叫这个 agent），不是工具沙箱——见 `server/internal/handler/agent.go:144-148`。`custom_env` / `custom_args` 都是空（`server/pkg/db/queries/agent.sql:86-99`）。

### 2.2 它能把文件交到人面前，而且 HTML 是**渲染**出来的，不是下载

`multica attachment upload <path>`（跑过 `--help`）：

> Intended for agents running inside a chat task: the file is tagged with the task and, when the task completes, the server binds it to the assistant reply it produces — it appears as an attachment card below your reply even if you paste nothing.

三个环节都对得上：

- 环境变量：每个任务（含 chat）都注入 `MULTICA_TASK_ID` 和任务级 `mat_` token，`server/internal/daemon/daemon.go:174-189`。
- 绑定：`server/pkg/db/queries/attachment.sql:158`（`BindChatAttachmentsToMessage`）只按 `workspace_id + task_id + 四个 owner 列为空` 过滤，**和 agent 的 kind 无关**，所以 system 载体同样适用。
- 渲染：`packages/views/editor/attachment.tsx:412` —— `kind === "html"` 走 `HtmlAttachmentPreview`，480px 内联 sandbox iframe + 「新标签打开」整页路由（`packages/views/attachments/attachment-preview-page.tsx`）。对齐会话用的是共享的 `ChatMessageList` → `RichContent` → 同一个统一渲染器（`packages/views/issues/draft/issue-draft-conversation.tsx:8-12`、`packages/views/chat/components/chat-message-list.tsx:1158-1175`）。

**结论：一个 HTML 附件贴在对齐回复下面，在对齐页面里就是一个活的、能点开的界面。** 这是整件事可行的支点。

### 2.3 它拿不到仓库 —— `docs/design/prototypes/<screen>.html` 不可达

对齐 session 建的时候**没有传 `project_id`**（`issue_draft.go:282-288`，`CreateChatSessionParams` 只有 ID / WorkspaceID / AgentID / CreatorID / Title）。claim 时 `resolveClaimProjectContext` 拿到一个 NULL 的 project，降级到工作区仓库注册表（`server/internal/handler/project_resource.go:984-990`）。而这个工作区的注册表是空的（跑过）：

```
$ multica repo list --output json
[]
```

仓库挂在**项目**上（`github_repo` project resource）。所以今天：**对齐载体既没有项目，也没有工作区级仓库，`multica repo checkout` 无从下手。** 它连 `docs/design/` 长什么样都看不到。

这条正好是 DENE-423（对齐选项目）的对偶：等对齐能选项目，载体会顺带继承 `github_repo` 和 `local_directory`。所以「写进仓库」这条路**不是不可能，是被 DENE-423 挡着**——而且就算解了，§4.1 还有别的理由不走。

### 2.4 它没有技能，`grill-frontend-look` 装不上

`CreateAgentBuilder`（`agent.sql:86`）只 INSERT `agent` 一行，不写 `agent_skill`。工作区技能只能通过 `agent_skill` 挂到 agent 上（`server/pkg/db/queries/skill.sql:149`），所以载体的工作区技能数是 0。

它只拿到**通用内置技能**：`server/internal/service/builtin_skills.go:92-97` 按 `builtinSkillSystemKey` 精确匹配 system key 做限定，表里只有 `multica-onboarding → Mika` 一条；`builtin_skills/` 目录下只有两个（跑过 `ls`）。所以载体今天手上只有 `multica-platform`。

`grill-frontend-look` **确实在这个工作区的技能库里**（跑过）：

```
$ multica skill list --output json | ...
grill-frontend-look | 用五个结构方向或一屏一档做出可打开的原型，必须看见才能决定时触发。...
  id = 99474c5f-833f-4b15-aa85-6c831d2e4a92
interaction-graph   | id = 09705bbe-8354-40c9-8b40-4ac4240eff1c
anti-slop-frontend  | id = af2612bb-736b-4881-a304-3ac1d7ec78cf
```

但载体挂不上它。**所以方法必须进 prompt 常量，不能靠引用技能。**（把 `agent_skill` 也塞进建会话那个事务是能做的，但那会让这个产品特性依赖「用户工作区里恰好装了这个技能」——换一个工作区就静默失效。见 §4.4。）

### 2.5 附件不会跟着 finalize 进新建的 issue

`server/internal/handler/issue_draft.go` 全文没有出现 `attachment`（跑过 `grep -in attachment`，零命中）。finalize 只读 `draft` 里的字段建 issue。所以对齐里产生的原型附件**不会**自动出现在新建单上——只能靠描述里的 `markdown_url` 链接过去，而那个链接在 issue 页会降级（§3.4）。

## 3. 推荐方案

### 3.1 触发：载体提议，用户一键决定，默认不开

**判据交给载体自己判**，但**它只能提议，不能自己开工**。载体已经拿着整场对话，是唯一有判断依据的角色；而「要不要花几个回合先看界面」是产品决定，属于用户。

落地上不需要任何新协议：guided policy 已经有 `<issue_draft_question>` 块，客户端已经解析、已经渲染成答案 chip（`packages/core/issue-drafts/protocol.ts:238-269`）。载体判断这次需求碰了用户可见界面时，就把它当成**那一轮唯一的那个问题**问出来，选项写死两个：

- 「先看一屏原型再定」（recommended，当界面形状会改变要建几张单时）
- 「先不用，按文字单走」

`conversation` policy 不发问题块，那就用一句普通话问——它的行为契约本来就是「genuinely cannot be drafted without it 才问」，界面形状正好落在这个门槛里。

**误判成本（issue 问的）**：

| 误判方向 | 代价 |
| --- | --- |
| 该 grill 没 grill | 回到现状：文字单一张。代价是后面某个实现单里补一轮，或者界面由实现者定。**可恢复。** |
| 不该 grill 却拦一道 | 对齐多出 3–8 个昂贵回合，画一个没人会看的 surface；对齐流程从「问清楚就建单」变成「每次都要先拒绝一次画图」。**不可恢复地拖慢每一场对齐。** |

不对称，所以默认关、由人开。

**否掉的两种触发**：关键词匹配——对齐输入是中文口语，「页面」「按钮」「界面」在纯后端单里照样出现，而真正需要 grill 的那句话可能一个关键词都没有；入口开关——那要求用户在描述需求之前先给自己的需求分类，而「先不用分类，说了再谈」正是对齐存在的理由。

### 3.2 收成物：对齐回复上的 HTML 附件

一轮前端 grill 的产出固定是**一个 HTML 文件**，用 `multica attachment upload` 贴在那一轮回复上：

- **五个结构方向**：`frontend-look.html`，一个文件内 picker 切五个候选，desktop / mobile 两种宽度，loading / empty / error 横切。
- **一屏一档**：`<screen>.html`（kebab-case 短名），只留选中 UI 和必备状态。

为什么是附件不是仓库：§2.3 仓库不可达；而附件这条路**今天就是通的、而且是渲染的**（§2.2）。一个视觉参考的生命周期就该跟着它被讨论的那场对话，不该为它开一个 PR。

### 3.3 位置：两个 policy 里的一个阶段，不是第三个 policy

`policy_key` / `policy_version` 是**一场对齐一条记录**，`SwitchIssueDraftPolicy` 直接覆盖（`issue_draft.go:704-711`）。加一个 `frontend` policy 意味着：一场跑过前端 grill 的对齐，记录里只剩 `frontend`，而它其余部分实际跑的 `question` 版本被擦掉了——`issue_draft_policy.go:14-22` 说得很清楚，版本号存在的唯一理由就是「事后审计时指得出这场对齐跑的是哪份 prompt」。用一个阶段去覆盖这条记录，就是把这条设计废掉。

而且它本来也不是 policy 那个维度的东西。两个现有 policy 的区别是**问不问**（`Guided`）；前端 grill 在两种模式下都该能发生。

所以：**共享 prompt 长出前端阶段的规则，两个 policy 都拿到**。阶段进入靠用户接受提议（§3.1），阶段发生过的事实记在 draft 负载里（`description` 的固定小节，§3.4），不新增列。

不走「对齐终局后的独立子单」：那等于把界面决定推到单子切完之后，而**这正是本单要修的那个病**——界面形状会改变要建几张单（一屏一档 vs 一个巨页，子单数不一样），谈晚了等于没谈。

### 3.4 产物进单：`description` 的固定小节，不加 JSON 字段

`parseIssueDraftBlock` 是**字段白名单**（`packages/core/issue-drafts/protocol.ts:94`，只取 `title / description / status / priority` 四个 + `children`）。所以在块里加一个 `frontend` 字段：

- **不会**破坏解析（未知键被忽略，预览面板不会瞎）；
- **会**被静默丢掉。而按 CLAUDE.md 的 API 兼容规则，装机的 desktop 客户端可能比后端旧——服务端 prompt 先上、客户端解析后上的那段窗口里，这个字段在旧客户端上就是不存在的，用户看不出区别，只会觉得「说好的原型没进单」。

固定 Markdown 小节零协议改动、每个版本的客户端都能读、在 issue 描述里就是正常渲染。所以：**受影响的那张单（父单或某个 child）的 `description` 末尾追加一个 `## 前端` 小节**，三行：

```markdown
## 前端

- 选定屏：`alignment-entry`
- 必备状态：loading / empty / error
- 原型：!file[alignment-entry.html](<markdown_url>)
```

`markdown_url` 由 `multica attachment upload` 直接返回（`server/cmd/multica/cmd_attachment.go:100`，用的是 `att.MarkdownURL`），按契约是**可持久化、不带 TTL** 的（`server/internal/handler/file.go:83-108`）。

**一个已知降级，要写进设计而不是藏着**：在对齐页里这个附件是内联 iframe（§2.2），但在**新建出来的 issue 页**上不是。`Attachment` 的 html 分支要求 `state.attachmentId`（`attachment.tsx:412`），而它靠 `AttachmentDownloadProvider` 从**当前实体自己的 `attachments[]`** 里按 URL 反查（`packages/views/editor/attachment-download-context.tsx:44-58`）。原型附件绑在 chat message 上、不属于这个 issue，反查不到，于是降级成下载 CTA——点开会下载那个 HTML，用户本地双击才能看。

两个选择，见 §5.3 和 §8-Q3。推荐做那个小的服务端补丁：finalize 时把选中的原型附件复制一行到新建的父单上。

## 4. 被否掉的备选

### 4.1 收成物写进仓库 `docs/design/prototypes/<screen>.html`

否。三条理由，按强度排：

1. **今天做不到**（§2.3 的实测）：载体没项目、工作区仓库注册表为空，没有可 checkout 的东西。要等 DENE-423。
2. 就算 DENE-423 解了，还要给载体开 checkout + 提交 + 推分支的权限。载体的行为契约第一条是「You are aligning a request, not executing it. Do not create, modify or delete anything」（`issue_draft_policy.go:63`）——给它写仓库的活儿，是把这条契约拆了。
3. 一个用完即弃的视觉参考不值一个 PR。`grill-frontend-look` 把它们放进仓库，是因为它跑在一个已经有 worktree 的实现会话里，顺手；对齐不是那个场景。

### 4.2 加第三个 policy key `frontend`

否，理由见 §3.3：它会覆盖掉 `policy_version` 这条审计记录，而且它不是 policy 那个维度的概念。

### 4.3 对齐终局后开一张独立的「设计子单」

否：界面形状会改变**要建几张单**，谈晚了等于没谈；而且这会引入一个新的对齐产品概念（「设计评审」），本单明确不许。

不过有一个**合法的近邻**要说清楚：当这次需求的前端复杂到一轮对齐吃不下（多屏 + 一条线），正确做法是让对齐产出一张**明写「先出原型」的实现子单**，由 Builder 在有仓库的实现会话里跑完整的 `grill-frontend-look`。对齐负责判断「这次要不要、能不能在这里谈完」，不负责把所有屏都画完。建议上限 2 屏（§8-Q4）。

### 4.4 在建会话的事务里给载体挂 `grill-frontend-look` 技能

否（但记为已知升级路径）。好处真实：方法留在 kk 自己维护的技能库里，不用抄进 Go 常量。坏处更大：工作区技能是**工作区作用域**的，别的工作区没有这个技能 → 特性静默失效。Multica 是产品，不是只跑在 kk 这一台上。

**升级路径**：把方法做成 `server/internal/service/builtin_skills/multica-frontend-look/`，并把 `builtinSkillSystemKey` 从精确匹配改成前缀匹配（载体 key 是 `issue_draft:<uuid>`，每场不同，精确匹配注定命不中——`builtin_skills.go:92-97`，约 3 行改动）。这样方法按需加载，不占每一轮的 prompt。**本轮不做**：内置技能的自动触发在一个系统 prompt 高度指令化的载体里可靠性未知，而 prompt 常量是确定的。先用常量把行为跑通，确认值得了再搬。

### 4.5 收成一条线（`INTERACTION.md` + `docs/design/flows/<flow>.html`）

否，见 §1：硬依赖 `/interaction-graph` 写仓库文件，且在建单之前维护产品 UX 链是越界。一条线属于实现阶段。

## 5. 改动草案

三处，全部在共享层，因此两个 policy 的 `Version` 一起 `2` → `3`。

### 5.1 `issueDraftContract` — 只加线格式那一句

`server/internal/handler/issue_draft_policy.go:43-63`，在 `description` 那条规则（`:53`）后面插一条：

```
- When a frontend look round has settled on a screen, end that issue's description with a section exactly like this, and nothing else about it:
  ## 前端
  - 选定屏：`<kebab-case-name>`
  - 必备状态：<the states this screen must show>
  - 原型：<the !file[...](...) snippet `multica attachment upload` printed>
  Never invent this section for a request that had no frontend look round.
```

**只有这一条进 contract**。contract 的职责是线格式（`:28-33`），行为不该往里塞。

### 5.2 新常量 `issueDraftFrontendLook` — 方法，两个 policy 共享

新增一个常量，由 `Instructions()` 无条件拼在 contract 和 policy behaviour 之间：

```go
// issueDraftFrontendLook is the shared frontend-look phase: the part of an
// alignment that decides what the surface LOOKS like, not just what it does.
// It is shared rather than a policy because "is there a screen here" is a
// property of the request, not of whether the carrier interviews the user.
//
// It is a rewrite, not a copy, of the user's `grill-frontend-look` method:
// that method lands its artifact in `docs/design/prototypes/<screen>.html`,
// and an alignment carrier has no project and therefore no repository
// (chat_session.project_id is NULL here; the claim degrades to the workspace
// repo registry). The judgement it keeps is the only one that matters —
// nothing was decided until there is something the user can open.
const issueDraftFrontendLook = `Frontend look — when the request changes something a person will look at:

- Do NOT start this on your own. Offer it as the one question of that turn, with two options: look at one screen first (recommended when the shape of the screen changes how many issues this becomes), or stay with text-only issues. Start only after the user says yes.
- Never offer it for a request with no user-visible surface.
- Two ways to run it. One screen at a time is the default: pick the single screen the user must see, give it a short kebab-case name, and build only that one. Five structural directions is for when the user asks to compare: five candidates for the SAME surface in one file behind a picker, structurally different in layout, information hierarchy or primary-action shape — different colours and corner radii do not count.
- Whichever you run, the file must show the screen at desktop AND phone width, and must show this screen's required states (loading / empty / error).
- Write a single self-contained HTML file in your working directory, then run `multica attachment upload <path>`. It prints a markdown snippet — put that snippet in your reply so the user can open the prototype, and keep it for the description section.
- A round is not finished until the user has something to open. Never say a direction is settled without the file.
- Ask ONE question about it at a time. The user may pick one candidate or combine them ("B's header with C's primary button") — if they combine, rebuild the file and upload it again.
- At most two screens in one alignment. When the request needs more, stop and say so: the remaining screens belong in an implementation issue that says to prototype first.
- This is a visual reference, not production code. No framework, no build step, no real data.`
```

拼装（`issue_draft_policy.go:106-108`）：

```go
func (p issueDraftPolicy) Instructions() string {
	return issueDraftContract + "\n\n" + issueDraftFrontendLook + "\n\n" + p.Behaviour
}
```

### 5.3 finalize 把选中的原型带到新建单上（可选，推荐做）

目的：让 §3.4 那个 `!file[...]` 在 issue 页上也渲染成内联 iframe，而不是下载 CTA。

最小改法：`FinalizeIssueDraft` 建完父单之后，把这场会话里**被 `description` 引用到的**附件各复制一行（同 `url` / `content_type` / `filename`，`issue_id` = 新父单，`chat_message_id` = NULL）。`CreateAttachment`（`attachment.sql:1-26`）已经支持带 `issue_id` 插入，不需要新查询。

已知代价，必须一起写进 PR 描述：两行指向同一个存储对象，删掉任一侧的清理路径（如 `DeleteCommentAttachments` 按 url 回收）会让另一侧断链。要么接受「原型附件不参与存储回收」，要么在复制时重新上传一份字节。**推荐前者**：一个 HTML 原型是几十 KB。

`packages/core/issue-drafts/protocol.ts` **不需要任何改动**——这是选 Markdown 小节而不是 JSON 字段换来的（§3.4）。

## 6. 版本与回滚

- 共享 prompt 变了 → `issueDraftPolicyRegistry` 两条都 `Version: "2"` → `"3"`（`issue_draft_policy.go:116-129`）。和 DENE-411 加 `children` 时同一个理由：contract 是每份 prompt 的一半，动了它，两份 prompt 都是新的。
- **历史对齐照样读得懂**：`Version` 从 draft 行读，不从注册表读（`issue_draft_policy.go:158-176`）。记了 `"2"` 的对齐会一直报 `"2"`——它确实跑的是 v2。
- **正在进行中的对齐**：载体的 `instructions` 在建会话时写死（`issue_draft.go:264-279`），部署不会改写已有行。所以 v2 的对齐会把 v2 跑完，只有新建的和**切过 policy 的**（`SwitchIssueDraftPolicy` 会重写 instructions，`issue_draft.go:692-702`）才拿到 v3。这是既有行为，不是本单引入的。
- **回滚**：还原两个常量 + 版本号。记了 `"3"` 的 draft 继续报 `"3"`，正确——它们确实跑过 v3。无迁移。已经建出来的 issue 描述里那个 `## 前端` 小节是纯 Markdown，回滚后照常渲染；`markdown_url` 不带 TTL，链接不失效。
- 如果 §5.3 也上了，回滚它要一起决定已复制的附件行怎么办。推荐留着——它们只是多了一个 owner 指针，没有坏处。

## 7. 不做会怎样（现状方案的代价）

现状 = 对齐只产文字单，前端做法留给 `/anti-slop-frontend` 在实现阶段解决。代价三条：

1. **界面形状由实现者定，而且改起来贵**。到了实现阶段，改方向要扔掉的是生产代码，不是一个 HTML。对齐阶段扔一个 HTML 的成本接近零。
2. **对齐的输出对 kk 是不可验的**。kk 靠可见产物判断进展；一组文字单在被实现之前，没有任何东西能让他判断「这个界面对不对」。第一个可见产物出现在实现之后——那时反馈已经是返工。
3. **`anti-slop-frontend` 的前置条件永远不成立**。它的描述写着「当编写或重构前端页面与组件，且**方向已经能看见**时触发」。现状里没有任何一步负责让方向「能看见」，所以实际只有两种结局：要么在实现单里临时补一轮 grill（实现单越界成设计单，验收判据糊掉），要么跳过——由模型自己猜一个界面。

**所以本单该做。** 这一条 issue 允许结论是「不该做」，但论据指向反面：缺的不是一个新流程，而是现有两个技能之间那段没人负责的空白。

## 8. 待 kk zi 拍板

| # | 问题 | 推荐答案 |
| --- | --- | --- |
| Q1 | 触发权归谁：载体自动开 / 载体提议+你一键 / 入口开关？ | **载体提议 + 一键**，默认不开。误判成本不对称（§3.1）。 |
| Q2 | 对齐里默认哪种考法：一屏一档 vs 五个结构方向？ | **一屏一档**。对齐通常只有一屏值得先看；五方向留给你显式要求「我要比一比」。 |
| Q3 | finalize 要不要把原型附件复制到新建单（§5.3）？ | **要**。不做的话 issue 页只剩下载 CTA，「可打开」在最需要它的地方掉一级。代价是一个存储对象两行指针。 |
| Q4 | 一场对齐最多几屏？ | **2**。超过就让对齐产出一张「先出原型」的实现子单，由 Builder 在有仓库的会话里跑完整方法（§4.3）。 |
| Q5 | 要不要加结构化字段记录「本场对齐跑过前端阶段」？ | **暂不**。`description` 的 `## 前端` 小节已经是可查的事实，加列就要面对「阶段 vs policy」的记录模型改造，不值当。 |
| Q6 | 方法放 prompt 常量还是做成内置技能（§4.4）？ | **先常量**。确认行为对了再搬成 `builtin_skills/multica-frontend-look/` + 前缀匹配（约 3 行）。 |

## 附：本页跑过的核查命令

```bash
git rev-parse origin/kun                      # d2dbed91f9...
multica repo list --output json               # []  ← 工作区无仓库
multica skill list --output json              # grill-frontend-look 在库里，id 99474c5f-...
multica skill get 99474c5f-833f-4b15-aa85-6c831d2e4a92 --with-content --output json
multica attachment upload --help              # 「Intended for agents running inside a chat task」
cat ~/multica_workspaces_.../task-01aeb2995729/.gc_meta.json      # {"kind":"chat",...}
ls  ~/multica_workspaces_.../task-b55b41d56325/codex-home/skills/ # chat 任务里落了 20 个技能目录
cd server && go test ./internal/daemon -run 'TestChatTaskSkipsPathMutexButKeepsAssignment|...' -count=1 -v  # PASS
grep -in attachment server/internal/handler/issue_draft.go        # 零命中 ← finalize 不搬附件
```
