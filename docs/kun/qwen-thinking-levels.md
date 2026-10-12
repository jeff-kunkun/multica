# Qwen Code 思考档位（按模型声明）

Multica 给 Qwen Code 智能体设思考强度，用的是已有的 `thinking_level` 字段、设置页的「思考」项和 `multica agent update --thinking-level`。不同的是：Qwen 的可选档位**不按 provider 写死**，而是读这台机器 Qwen 配置里对**这个端点 + 这个模型**的声明（DENE-1950）。

## 能力从哪来

daemon 列 Qwen 模型时读 Qwen 自己的设置分层（system-defaults < `~/.qwen/settings.json` < 项目 < `QWEN_CODE_SYSTEM_SETTINGS_PATH`），取 `modelProviders.<authType>[]` 里与模型 id 和当前 baseUrl 匹配的那一行的 `capabilities.reasoning`：

```json
"capabilities": { "reasoning": {
  "thinking": true,
  "disableField": "enable_thinking",
  "efforts": ["low", "medium", "xhigh"],
  "defaultEffort": "xhigh"
} }
```

- `efforts` 是可选档位；`disableField` 存在时多一档 `none`（关闭思考）。
- `defaultEffort` 可省；省了界面显示「跟随默认」，不猜默认值。
- 目录里每个模型带 `thinking.source`（声明出处），`multica runtime models <runtime-id>` 的 SOURCE 列就是它。
- 没声明的模型没有档位；界面写「这个模型没有声明思考档位」，服务端拒绝保存并给出原因。

## 保存与生效

- 服务端按 runtime 上报、缓存着的目录校验：没上报过/离线、模型没声明、档位没声明，一律 400 带原因，不存「看起来保存了其实不生效」的值。
- 带着档位换模型会被拒，除非同一次更新带 `thinking_level=""`（界面换模型时自动带上）。
- `""` = 跟随默认，只清 Multica 这边的覆盖，不碰 Qwen 原有配置。
- 下次执行生效；正在跑的任务继续用开始时的档位。

## 执行时怎么注入

每次运行生成一份临时 system settings（`QWEN_CODE_SYSTEM_SETTINGS_PATH`），先合并原有的 system settings，再只给当前模型那一行的 `generationConfig.extra_body` 加：

| 档位 | 发给端点的 `chat_template_kwargs` |
| --- | --- |
| `low` / `medium` / `xhigh` | `{enable_thinking: true, reasoning_effort: <档位>}` |
| `none` | `{enable_thinking: false}` |
| `""` | 不加任何东西 |

共享的 `~/.qwen/settings.json` 不改；上下文窗口、工具调用、鉴权等原有配置原样保留；生成请求和上下文压缩请求走同一份配置。Qwen 0.20 与 0.24 都实测送达（0.24 额外发顶层 `reasoning_effort`，0.20 额外发 `reasoning.effort`，llama-server 以 kwargs 为准）。

## Nexus GPU 客户端说明

拉蒂兹（`nexus-coder`，经 `127.0.0.1:8090`）的声明在本机 `~/.qwen/settings.json` 的 `modelProviders.openai` 里，改前备份为 `settings.json.bak-dene1950-20261012`：

- 档位只开 `low` / `medium` / `xhigh` 三档 + 关闭；按上线前用 llama-swap `/upstream/nexus-coder/apply-template` 核过的模板：三档各渲染出 "Reasoning effort is set to X"，关闭渲染成空 `<think></think>`。
- 模板不传 kwargs 时默认就是 `xhigh`，所以「跟随默认」≈ 超高；声明里没写 `defaultEffort`，界面不冒充知道默认值。
- 按智能体覆盖：同一台机器上别的智能体或手工 `qwen` 不受影响，端点的服务器默认值也不改。
- 换模型或改 llama-server 模板后，先重新核模板再改 `efforts`，不要照抄旧模型参数；`contextWindowSize` 跟服务端 `--ctx-size` 走（当前 131072）。
