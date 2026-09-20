# 权限底座：四档档位 × 三档共享范围

DENE-695 权限系统的判定规则。这篇是给人读的矩阵；可执行的版本是 `server/internal/permission`，它的测试把每一格都写死了，两边不一致时以测试为准并回来改这篇。

## 两层，互不越界

| 层 | 回答的问题 | 来源 |
| --- | --- | --- |
| 共享范围（visibility） | 这个资源对你来说**存不存在** | 资源自己身上的 `visibility` 字段 |
| 档位（role） | 对一个存在的资源，你**能做什么** | `member.role` |

- 共享永远不给写权限：把东西共享给访客，他还是只读。
- 档位永远不给视野：Member 再怎么是正式成员，没共享给他的东西他也看不到。
- 唯一的例外方向是 Owner / Admin 的「管理兜底」，见下文——它来自 `listAccessibleProjectIDs` 的既有行为，不是新规则。

**范围内的资源**：Issue（评论、附件跟随所属 issue）、项目、仓库。
**不在范围内**：Agent 与 Squad（归 `agent.permission_mode` + `agent_invocation_target`，Parent B）；工作区设置 / 成员 / 计费（只看档位，不参与共享）。

## 判定顺序

每一次「用户 + 资源 + 操作」按这个顺序走，走到第一个否定就停：

1. **是不是这个工作区的成员。** 不是 → 找不到。
2. **看不看得见**（`permission.CanSee`）。看不见 → 找不到。**不是「无权限」**——后者等于告诉对方这个东西存在。列表、搜索、聚合、通知同理，直接不出现。
3. **档位允不允许这个操作**（`permission.Allowed`）。不允许 → 无权限（403）。此时对方已经看得见资源，说「无权限」不泄露任何东西。

不针对具体资源的操作（新建、邀请、改设置、计费）跳过第 2 步，只看档位（`permission.AllowedInWorkspace`）。

## 第一层：看不看得见

「关系」三选一：**创建者**、**在项目里**（资源所属项目在此人的 `listAccessibleProjectIDs` 结果里）、**无关**。

| 档位 | 范围 | 无关 | 在项目里 | 创建者 |
| --- | --- | --- | --- | --- |
| Owner / Admin | `private` | 不可见 | 不可见 | 可见 |
| Owner / Admin | `project` | —（见注） | 可见 | 可见 |
| Owner / Admin | `workspace` | 可见 | 可见 | 可见 |
| Member | `private` | 不可见 | 不可见 | 可见 |
| Member | `project` | 不可见 | 可见 | 可见 |
| Member | `workspace` | 可见 | 可见 | 可见 |
| Guest | `private` | 不可见 | 不可见 | 可见 |
| Guest | `project` | 不可见 | 可见 | 可见 |
| Guest | `workspace` | **不可见** | **不可见** | 可见 |

注：Owner / Admin 对工作区内**每一个**项目都算「在项目里」（管理兜底），所以他们和 `project` 范围的资源不存在「无关」这种关系。

读这张表的几条要点：

- **零信任**：新建资源默认 `private`。什么都没共享时，Member 与 Guest 那几行只剩「创建者」一列是可见——也就是只看得到自己建的，别人的一概不存在。
- **`private` 对 Owner / Admin 同样不可见。** 管理兜底只覆盖 `project` 范围（它是「所有项目」的兜底，不是「所有资源」的兜底）。这和 `issue_view` 现在的行为一致。
- **`workspace` 不含访客**，哪怕访客恰好也在那个项目里。要给访客看，范围必须是 `project`。
- **创建者永远看得见自己建的东西。** 访客不能新建，这一列对访客只在「原来是 Member、后来被降成访客」时才会出现：他还看得见自己以前建的，但已经只读。
- `project` 范围只能设在属于某个项目的资源上（`permission.CanSetVisibility`）；不属于任何项目的资源只能 `private` 或 `workspace`。对「项目」这种资源，「所属项目」就是它自己。

## 第二层：能做什么

前提是第一层已经判了可见。

| 操作 | Owner | Admin | Member | Guest |
| --- | --- | --- | --- | --- |
| 查看 | 可 | 可 | 可 | 可 |
| 评论 / 回复 | 可 | 可 | 可 | 否 |
| 编辑、改状态、传附件、指派 | 可 | 可 | 可 | 否 |
| 改这个资源的共享范围 | 可 | 可 | 仅自己建的 | 否 |
| 把人加进 / 移出项目 | 可 | 可 | 仅自己带的项目 | 否 |

工作区级操作（不看共享）：

| 操作 | Owner | Admin | Member | Guest |
| --- | --- | --- | --- | --- |
| 新建 issue / 项目 / 仓库 | 可 | 可 | 可 | 否 |
| 被指派、被 @ 触发运行 | 可 | 可 | 可 | 否 |
| 邀请成员、改别人档位 | 可 | 可 | 否 | 否 |
| 工作区设置 / 集成 / 密钥 | 可 | 可 | 否 | 否 |
| 计费、转让或删除工作区 | 可 | 否 | 否 | 否 |

Guest 一整列除了「查看」全是否，没有任何关系能翻过来：即使他是某资源的创建者、某项目的 lead，也不能改范围、不能管项目成员。全局只读拦截层（DENE-697）只需要问一句 `Role.CanWrite()`。

**失败即关闭**：不认识的档位、不认识的范围值、不认识的操作，一律按「看不见 / 不允许」处理。

## 数据模型

- `member.role`：CHECK 扩成 `owner / admin / member / guest`（migration 502）。存量行不动，无人降级。
- 资源上的 `visibility TEXT NOT NULL DEFAULT 'private'`，CHECK 只允许 `private / project / workspace`，外加一条配对约束「`project` 范围必须有所属项目」——写法照抄 `479_issue_view_project_visibility`。加列与存量回填成 `workspace` 属于 DENE-698。
- **不新建任何名单表。**「项目成员」= `project_member` 的行 + 项目 lead，由 `listAccessibleProjectIDs` 合并；共享界面里不逐个勾人，也不给某个人单独选读写。
- 回滚只收紧不放宽。502 的 down 不能把访客改写成 Member（那是给只读的人发写权限），所以访客在回滚时直接失去成员资格，连同他们的 `project_member` 行一起删掉；重新上线后再邀请。

### 现在还不能在界面或接口里选「访客」

502 只是让数据库**放得下** `guest`。`normalizeMemberRole` 和邀请表（`workspace_invitation`、`workspace_share_link` 的 role CHECK 仍是 `admin / member`）这次故意没动：今天绝大多数写接口只检查「是不是成员」，不看档位，此刻放出一个访客，他实际上拥有 Member 的全部写权限，却顶着「只读」的名字。接口放行 `guest` 必须和 DENE-697 的拦截层同一次上线。

## 缓存失效

先纠正一个前提：**两级缓存里都没有档位，也没有共享范围。**

| 缓存 | 键 → 值 | TTL | 里面有什么 |
| --- | --- | --- | --- |
| `MembershipCache` | 用户 + 工作区 → `"1"` | 5 分钟 | 只有「是成员」这一个事实 |
| `PATCache` | token 哈希 → 用户 id | 10 分钟 | 只有「这个 token 是谁的」 |

普通请求走 `RequireWorkspaceMember` 中间件，每次都从数据库读 `member` 行，所以**改档位对普通接口本来就是即时的**。真正会延迟的只有绕过中间件、直接信 `MembershipCache` 的三处：daemon 工作区校验两处（`handler/daemon.go`）、附件下载一处（`handler/file.go`）。

由此定下四条规则：

1. **档位与共享范围永远不进这两级缓存。** 可见性判定每次读库（`visibility` 列 + `listAccessibleProjectIDs`）。要提速就在单次请求内复用结果，不跨请求缓存——跨请求缓存一旦出现，「即时生效」就变成了又一处要记得失效的地方。
2. **档位变更、移除成员 → 失效该用户的 `MembershipCache`。** 现状已经做到（`UpdateMember` / `DeleteMember` / 退出 / 删工作区都调了 `Invalidate`），DENE-697 只需补一条测试把它钉住。
3. **`PATCache` 不需要因为档位或共享变更而失效。** 它只回答「token 属于谁」，这个答案不随档位变。它唯一需要失效的时机是吊销 token，现状已做。把它列进「每次共享变更都要清」只会制造无意义的缓存击穿。
4. **信 `MembershipCache` 的三处，缓存命中之后仍要补判定。** 这是本次排查发现的真正漏洞：
   - 附件下载：命中缓存就直接放行，完全不看附件所属 issue 的可见性。DENE-698 必须在这里加 `CanSee`，否则任何成员拿到附件 id 就能下载别人 `private` issue 里的文件。
   - daemon 两处：访客不应能注册或操作 runtime。DENE-697 在缓存命中后补读一次档位，或者干脆不给访客写缓存。

共享变更（改范围、项目加人减人）因为第 1 条，**没有任何缓存需要清**：下一次请求读库就是新答案。前端侧由 WebSocket 事件让相关 Query 失效即可，和现有 `member:updated` 同一个模式。

## 留给后续票的边界

- **lead 看不见自己带的 `private` 项目。** 如果 A 建了一个 `private` 项目并把 B 设成 lead，按矩阵 B 看不见它（`private` 只认创建者）。矩阵答案是唯一的，但体验上会怪；DENE-698 做「设 lead」时应提示把项目范围改成 `project`。
- **创建者离开工作区后，他的 `private` 资源对所有人不可见**（包括 Owner）。需要产品决定：移除成员时转交给操作人，还是保留为孤儿。不决定也不会出错，只是那些资源谁也找不回来。
- 模块级可见性（DENE-699）是叠在这两层之上的第三道「与」门，不改变这张矩阵的任何一格。
