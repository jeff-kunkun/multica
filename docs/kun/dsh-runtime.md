# DeepSeek Harness 自托管接入（魔改方案）

Multica 官方已经把 DeepSeek Harness（`dsh`）做成一等运行时，自托管却还差最后一公里：运行时桥接包没上公共 npm。本页记录官方现状、已知坑，以及本 fork 的一键安装方案。

## 官方状态

- 协议与守护进程发现已经在主线：`server/pkg/agent/dsh.go`、`server/internal/daemon/dsh_profile.go`（MUL-7232 / PR #6923 / #8237）。
- 守护进程只在 `dsh --profile multica --probe` 返回 `protocol_version: 1` 时注册 `dsh`。profile 缺失时 `/health` 报 missing profile，运行时被跳过。
- 官方文档：[安装智能体运行时](https://multica.ai/docs/install-agent-runtime) 已经写了「从 [multica-ai/dsh-multica-runtime](https://github.com/multica-ai/dsh-multica-runtime) 自行构建」。
- 守护进程可以用 `MULTICA_DSH_PROFILE_BUNDLE` 自动 `dsh plugin --profile multica add <bundle>`，但 `<bundle>` 目前没有可填的公共 npm 包名。

## 已知问题：upstream #6936

[multica-ai/multica#6936](https://github.com/multica-ai/multica/issues/6936) 仍 OPEN：桥接仓库 `package.json` 为 `"private": true`、版本 `0.1.0-private.1`、README 仍写 Private out-of-tree bridge。自托管用户不能：

```bash
dsh plugin --profile multica add @multica-ai/dsh-runtime
```

Issue 原文里的 clone 地址还写着 `forrestchang/dsh-multica-runtime`；现在公开仓库是 `multica-ai/dsh-multica-runtime`。`package.json` 的 `repository` 仍指向 `dsh-external/dsh-multica-runtime`。

Issue 建议构建时用 `pnpm install --trust-lockfile`。那是 **pnpm 11** 的 flag。本机 Homebrew pnpm 10.28.2 会直接拒绝它。可移植写法是：

```bash
pnpm install --frozen-lockfile && pnpm build
```

## 魔改方案

本 fork 提供一键脚本，把 Stage 1 在宿主机上验证过的步骤固化下来：

```bash
bash scripts/setup-dsh-runtime.sh
```

脚本会：

1. 检查 Node.js 20+（桥接 package.json 更希望 `^22.19 || >=24`；Node 26 已在本机验证）。
2. 确认 pnpm；没有就走 corepack / `npm i -g pnpm`。
3. PATH 上没有 `dsh` 时执行 `npm install -g @deepseek-ai/dsh@latest`。
4. 克隆或更新 `https://github.com/multica-ai/dsh-multica-runtime` 到 `~/.agents/dsh-multica-runtime`。
5. `pnpm install --frozen-lockfile && pnpm build`。
6. `dsh plugin --profile multica add <runtime-dir>`。
7. 用 `dsh --profile multica --probe` 验收 `protocol_version: 1`。
8. 若 `DEEPSEEK_API_KEY` 未设置则警告，**不打印任何凭据值**。

常用选项：`--dry-run`、`--no-update`、`--skip-dsh-install`、`--runtime-dir DIR`。详见 `bash scripts/setup-dsh-runtime.sh --help`。

装好后：

```bash
dsh --version
dsh --profile multica --probe
dsh --profile multica --list-models
multica daemon status --output json   # agents 里应有 dsh
```

若守护进程已经在跑，需要在**任务外**重启它才会重新探测。不要从它正在托管的任务里执行 `multica daemon restart`，那会把当前 run 杀掉。

### 凭据

真正调模型需要 `DEEPSEEK_API_KEY`，或 dsh 自己的凭据通道（browser-session / Keychain）。probe 和 `--list-models` 不走模型接口，所以没 key 也能绿。不要把 key 写进仓库、issue 或 PR。

## 本机已验证版本（DENE-24 / DENE-25）

| 组件 | 版本 |
| --- | --- |
| Node.js | v26.3.0 |
| pnpm | 10.28.2 |
| `@deepseek-ai/dsh` | 0.1.5-rc.1 |
| 桥接 HEAD | `e29aae2`（`plugin_version` `0.1.0-private.1`） |

probe：

```json
{"v":1,"type":"probe","runtime":"dsh","plugin_version":"0.1.0-private.1","protocol_version":1}
```

模型目录（当时）：`deepseek-official/deepseek-v4-flash`、`deepseek-official/deepseek-v4-flash-vision-exp`、`deepseek-official/deepseek-v4-pro`、`deepseek-official/deepseek-flash`（default）。

桥接 README 对照的是 `dsh@0.1.0-rc.6`。`0.1.5-rc.1` 的 probe / list-models / 端到端冒烟已通过，后续 runtime 行为若漂移需要再对版本。

## 给 upstream #6936 的反馈建议

整理自本机接入，不是官方承诺：

1. **尽快发布 `@multica-ai/dsh-runtime`**（或同名稳定包），去掉 `"private": true` 和 `-private` 版本后缀，这样 `dsh plugin --profile multica add @multica-ai/dsh-runtime` 以及 `MULTICA_DSH_PROFILE_BUNDLE` 才有可填的公共值。
2. **修正仓库元数据**：`package.json` 的 `repository` 仍是 `dsh-external/dsh-multica-runtime`；README 仍自称 Private。公开仓库已是 `multica-ai/dsh-multica-runtime`。
3. **文档里不要只写 `--trust-lockfile`**。该 flag 属于 pnpm 11；pnpm 10 会失败。建议同时写 `pnpm install --frozen-lockfile`。
4. **文档对照版本跟上**。桥接 README 写 `dsh@0.1.0-rc.6`，当前 CLI 已是 `0.1.5-rc.1`。至少注明已验证的 CLI 范围。
5. **给自托管一条官方一键路径**。在 npm 发布前，官方 `install-agent-runtime` 可以给出 clone → frozen-lockfile → build → `dsh plugin add` 的完整命令，而不是只说「自行构建」。
6. **missing profile 的可发现性**。没装 profile 时 daemon 会静默跳过 `dsh`。`/health` 有原因，但 `multica daemon status` 对第一次接入的人不够显眼。

本 fork 在发布前用 `scripts/setup-dsh-runtime.sh` 填这个缺口。桥接一旦上 npm，脚本应改为优先用包名、本地构建只作回退。
