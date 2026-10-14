# 新办公室成员的模型交给 magpie（DENE-2039）

新办公室（office.ferryway.cc）的 Claude Code、Codex、OpenCode、Gemini 四位成员不再在 Multica 里填模型。每回合用哪个模型、哪个账号，由本机 magpie 网关按难易挑选，账号用完或报错时自动换下一个。Multica 侧只负责把成员的请求指到网关。

## 接线只做在成员身上，不碰全局

旧办公室的守护进程也读 `~/.claude/settings.json`、`~/.codex/config.toml`、`~/.config/opencode/opencode.json*` 和 `~/.gemini/antigravity-cli/settings.json`。`magpie claude|codex|opencode|agy <model>` 这类全局接线会改这些文件，所以**不要用**；接线一律写在新办公室成员自己的「环境变量」和「自定义参数」里。`magpie accounts import` 会把来源工具登出，同样不要用；补账号用 `magpie accounts add <agent>`。

| 成员 | 接线方式 | 网关组 |
|---|---|---|
| Claude Code | 环境变量：`ANTHROPIC_BASE_URL=http://127.0.0.1:3425`、`ANTHROPIC_AUTH_TOKEN=magpie`，`ANTHROPIC_MODEL` 及 `ANTHROPIC_DEFAULT_*_MODEL`、`ANTHROPIC_SMALL_FAST_MODEL`、`CLAUDE_CODE_SUBAGENT_MODEL` 都设为 `group/office-claude[1m]`，`CLAUDE_CODE_AUTO_COMPACT_WINDOW=272000`；源文件 `~/.config/magpie-office/claude.env` | `office-claude` |
| Codex | 自定义参数 `-c model_provider=magpie`、`-c model_providers.magpie.{name,base_url=http://127.0.0.1:3425/v1,wire_api=responses,experimental_bearer_token=magpie}`、`-c model=group/office-codex`、`-c model_catalog_json=~/.config/magpie-office/codex-models.json`（保留原有 `-c features.plugins=false`） | `office-codex` |
| OpenCode | 环境变量 `OPENCODE_CONFIG=~/.config/magpie-office/opencode.json`（openai-compatible provider 指向网关，模型 `magpie/group/office-opencode`） | `office-opencode` |
| Gemini（agy） | 环境变量 `GEMINI_API_KEY=magpie-agy`、`GOOGLE_GEMINI_BASE_URL=http://127.0.0.1:3425`；自定义参数 `--gemini_dir /Users/kunkun/.gemini-magpie-office`（**写绝对路径**），该目录的 `antigravity-cli/settings.json` 只多一行 `"modelProvider": "gemini"` | 组 id 改成 `gemini-3.1-pro-preview`（主对话）和 `gemini-3.1-flash-lite-preview`（旁路小调用） |

Gemini 的两处特殊：

- agy 在设置里不认自定义模型作默认值（报 `model is empty`），`--model` 又被守护进程挡住，成员模型也要留空；不带模型时 agy 固定请求 `gemini-3.1-pro-preview` 与 `gemini-3.1-flash-lite-preview`，所以把 magpie 组的 id 改成这两个名字接住。Multica 用量里会显示「Gemini 3.1 Pro」，实际模型以 magpie 用量为准。
- 新办公室桌面包内置的守护进程（0.7.0-test.1）不展开 `--gemini_dir` 里的 `~`，会在家目录建出名为 `~` 的文件夹并静默走默认登录，必须写绝对路径。

## 分组与规则

四个组都是 `routing=order`，分类器 `zhipu/glm-5.3-flash:none`，两条意图规则：写/改/调试代码、跑测试构建、审改动 → 强模型；快问、查状态、读或总结、简短回复 → 便宜模型。

| 组 | 强 | 便宜 | 兜底 |
|---|---|---|---|
| office-claude | claude/claude-opus-5-5 | claude/claude-haiku-5-5 | zhipu/glm-5.3 |
| office-codex | codex/gpt-6.1-sol | codex/gpt-6-luna | zhipu/glm-5.3 |
| office-opencode | zhipu/glm-5.3 | zhipu/glm-5.3-flash | codex/gpt-6-luna |
| gemini-3.1-pro-preview（原 office-gemini） | zhipu/glm-5.3 | zhipu/glm-5.3-flash | codex/gpt-6-luna |
| gemini-3.1-flash-lite-preview（原 office-gemini-side） | — | zhipu/glm-5.3-flash | codex/gpt-6-luna |

**Claude 订阅只给 Claude Code 用**：除 `office-claude` 外，任何组都不得放 `claude/*`。智谱 coding 套餐不含 `glm-5.3-flashx`，请求它会让 magpie 把整个智谱 key 休息 15 分钟，已从暴露模型里去掉。

意图按「一回合的用户消息」判断。票的第一回合带着整段运行规则，几乎总判成强模型；评论追问这类短消息才会落到便宜模型。

## 实测（2026-10-13）

- 四位成员各跑一张真票成功；首回合 Claude→opus、Codex→gpt-6.1-sol、OpenCode/Gemini→glm-5.3；评论追问 Claude→haiku、OpenCode→glm-5.3-flash。
- 换号：把智谱 key 临时改坏，OpenCode 的新票请求 401 后由 codex/gpt-6-luna 接手，票照常完成；key 已恢复并 `magpie provider test zhipu` 通过。
- 每家目前只登录了一个账号，所以接力是跨厂商的；要同厂商多账号接力，在 magpie 里 `magpie accounts add claude|codex` 加登录，再按上面的改坏-恢复办法复测一次同厂商接力。
