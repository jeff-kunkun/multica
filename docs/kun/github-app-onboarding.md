# 新成员接 GitHub 仓库

目标：工作区里任何人的 GitHub 仓库（个人账号或组织都行）都能接进来，PR 自动挂到对应的票上。背景见 DENE-959。

## 先弄清三件事

- **PR 是怎么进来的**：GitHub App 装在某个账号或组织上，这个安装就叫 installation。GitHub 只推送这个安装授权范围内的仓库事件；服务端收到后，发给绑定了该安装的所有工作区，再按标题、分支名里的票号（例如 `DENE-123`）挂票。
- **设置 → 连接与扩展 → 代码仓库管的是另一件事**：它决定智能体能 clone 哪些仓库，不影响 PR 能不能进来。一个仓库要「能挂 PR」又要「能被智能体选到」，两边都得配。
- **一个 GitHub 账号 = 一个安装**：kkunkunya 的仓库要装在 kkunkunya 上，jeff-kunkun 的仓库要装在 jeff-kunkun 上。同一个工作区可以同时绑多个安装，事件互不覆盖（迁移 133）。

## 新成员接自己的仓库（日常）

1. **找能点连接的人**。只有工作区 owner / admin 能发起连接。普通成员请工作区 owner / admin 来点。
2. **admin 点连接**。在 **设置 → 连接与扩展 → 连接** 点 **添加连接**，类型选 **GitHub App**。浏览器会打开 GitHub 的安装页。
3. **在 GitHub 上选账号并授权仓库**：
   - admin 自己就是仓库主人：在安装页选对应的账号或组织，勾 *All repositories* 或者只勾要接的仓库，然后点 Install。
   - 仓库在别人的 GitHub 账号下：把第 2 步打开的地址（`https://github.com/apps/<slug>/installations/new?state=…`）原样发给对方。对方登录自己的 GitHub 打开这个链接，完成安装后会自动跳回来，绑到这个工作区。这个链接只绑工作区，不过期，别往工作区外发。
   - 组织仓库：只有组织 owner 能直接安装。普通组织成员点完会变成一条「请求安装」，要等组织 owner 在 GitHub 上批准。
4. **回到设置页核对**。**设置 → 连接与扩展 → 连接** 里会多出这个账号的 GitHub App 连接。再打开 **代码仓库**，每个仓库一行状态：
   - **已接通**：PR 会挂票，关单时查得到。
   - **待安装 / 未接通**：这个仓库所属账号还没装 App，或者装了但没勾这个仓库。要么连上它所属的账号，要么去 GitHub 上把它加进已有的安装（GitHub → Settings → Applications → 这个 App → Configure → Repository access）。
   - 被覆盖但没登记的仓库：PR 照常挂票，但智能体选不到它。需要的话在 **代码仓库** 里登记。
5. **验证**。在仓库里开一个标题带票号的 PR（例如 `DENE-123 修一下登录`），几秒后 `multica issue pull-requests DENE-123` 能看到它。

补充授权范围（给已有安装加仓库）不用在 Multica 里再点连接，在 GitHub 上改完就生效。服务端不存仓库清单，事件里带着哪个安装就按哪个安装分发。

## 存量 PR 补挂

App 只收接通之后的事件，接通前开的 PR 不会自己出现。要补挂的话，在 GitHub 上把 PR 标题随便改一个字再改回来（或者直接保存一次标题），这会触发一次 `edited` 事件，PR 就挂上了。

## 服务器侧（一次性，已为 ai.ferryway.cc 做过就跳过）

App 用 GitHub 的 manifest 流程创建，所有字段都由清单给定，不手填：

- 可安装范围：**Any account**（`public: true`）。选 "Only on this account" 的话，别的 GitHub 账号都装不上。
- Setup URL `https://ai.ferryway.cc/api/github/setup`，打开 Redirect on update；Webhook URL `https://ai.ferryway.cc/api/webhooks/github`。
- 权限全部只读：Metadata、Contents、Pull requests、Checks、Commit statuses。订阅的事件：Pull request、Check suite、Check run、Status。

步骤：

1. 在已登录 GitHub 的浏览器里，向 `https://github.com/settings/apps/new` POST 一个 `manifest` 表单字段，内容是上面这份清单（JSON）。清单的 `redirect_url` 指回 `https://ai.ferryway.cc/settings?tab=github`。GitHub 可能要求输入一次账号密码（sudo mode），这一步只能人来做。
2. 点 **Create GitHub App**。浏览器跳回设置页，地址栏里带 `code=…`，这个 code 一小时内有效。
3. 在本机仓库根目录运行 `scripts/selfhost-github-app.sh <code>`。脚本会依次：用 code 换回 App 的 ID、webhook secret 和私钥，写进服务器 `/opt/multica/.env`（原文件留 `.env.bak.github-app.<时间戳>` 备份），重建 backend 容器，最后确认 `POST /api/webhooks/github` 返回 401（表示已经在校验签名）。密钥只在服务器 `.env` 里，不进仓库。
4. 去 **设置 → 连接与扩展 → 连接** 按「新成员接自己的仓库」连第一个账号。

App 的拥有者账号不影响谁能安装（范围是 Any account）；以后要换拥有者，可以在 App 设置里 Transfer ownership。

## 排查

| 现象 | 先查 |
| --- | --- |
| 连接按钮是灰的 | 服务器 `.env` 里的 `GITHUB_APP_SLUG` 和 `GITHUB_WEBHOOK_SECRET` 是不是空的；改完要重建 backend |
| 添加连接里 GitHub App 是灰的 | 同上；另外 `GITHUB_APP_ID` / `GITHUB_APP_PRIVATE_KEY` 没配时不能浏览仓库 |
| 代码仓库里某账号的仓库全是「未接通」 | 这个安装在 GitHub 上被卸载或暂停了，重新连接 |
| PR 没挂上 | 先看代码仓库那一行的状态；再看标题 / 分支名里的票号前缀是不是本工作区的；最后到 GitHub App → Advanced → Recent Deliveries 看投递是不是 2xx |
| 投递返回 401 | 服务器上的 webhook secret 和 App 里的不一致 |
