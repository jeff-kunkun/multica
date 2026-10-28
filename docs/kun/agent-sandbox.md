# Agent 沙箱姿态（魔改方案）

本 fork 的立场：**守护进程启动的 agent 不加 OS 沙箱**。agent 要能在自己的 worktree 之外干活——打 DMG、调 `hdiutil`、写缓存、跑构建工具链——才谈得上把打包发版交给它。

## 各 runtime 的现状

| runtime | 权限姿态 | 代码位置 |
| --- | --- | --- |
| claude | `--permission-mode bypassPermissions` | `server/pkg/agent/claude.go` |
| codex | 所有平台默认写死 `sandbox_mode=danger-full-access`，审批请求一律自动同意；只有 Windows 上用户自己开了 `windows.sandbox` 才保留 `workspace-write` | `server/internal/daemon/execenv/codex_sandbox.go` |
| grok | `--always-approve`，无 OS 沙箱 | `server/pkg/agent/grok.go` |
| antigravity | `--dangerously-skip-permissions` | `server/pkg/agent/antigravity.go` |
| opencode | `--dangerously-skip-permissions` | `server/pkg/agent/opencode.go` |
| cursor | `--yolo` | `server/pkg/agent/cursor.go` |
| dsh | `DSH_PERMISSION_MODE=danger-full-access`（本页） | `server/internal/daemon/daemon.go` |
| reasonix | `--sandbox-network auto --sandbox-bash auto`，仍有沙箱 | `server/pkg/agent/reasonix.go` |

Codex、Claude、OpenCode 三家的实际取值写在 `server/pkg/agent/permissions.go` 和 `codex_sandbox.go`，守护进程启动时打一行 `agent permissions` 日志列出来（`codex_sandbox_mode`、`codex_approvals`、`claude_permission_mode`、`opencode_permissions`）。Windows 上 `codex_sandbox_mode` 按共享的 Codex 配置（`$CODEX_HOME` 或 `~/.codex/config.toml`）算：开了 `windows.sandbox` 或配置读不出来就记 `workspace-write`；单个 agent 用 `-c windows.sandbox=…` 自定义参数打开的，只在它自己的任务里生效，启动日志看不到。

OpenCode 自动升级只跟到 2.0 以下（`agent_cli_release.go` 的 `Below`）：2.x 收掉了插话插件和 MCP 配置用的通道。最新版到了 2.x 时，运行时页显示「不能自动更新」并写明原因，手动「立即更新」也不会跨过去。

## 为什么 dsh 需要单独处理

`dshLaunchArgs()` 只传 `--profile multica --stdio`，DeepSeek Harness 没有对应的「跳过权限」命令行开关；它从环境变量 `DSH_PERMISSION_MODE` 读取姿态，未设置时落到自己的默认档 `workspace-write`（见 `~/.dsh/profiles/multica/cordis.yml` 的 `sandbox-policy` 插件）。该档的 seatbelt 策略只放行 worktree 内的写操作。

DENE-329 打包 Desktop v0.4.56 时就撞在这里：最后一步 `hdiutil: create failed - Operation not permitted`——建磁盘镜像要碰 `/dev` 上的镜像设备，沙箱直接拒绝，只能靠重试绕过去。

守护进程现在在 `provider == "dsh"` 分支里通过 `applyDshTaskEnv` 默认注入：

```
DSH_PERMISSION_MODE=danger-full-access
```

这一档在 dsh 侧同时把 approval 置成 `never`，所以一个变量同时解掉沙箱和审批弹窗。

## 按 agent 收回沙箱

注入点在 `layerCustomEnvAndHermesHome` 之后，写法是「custom_env 没给才设」。要让某个 agent 重新带沙箱跑，在 Desktop 的 Agent 设置 → 环境变量里加：

```
DSH_PERMISSION_MODE=workspace-write
```

`DSH_PERMISSION_MODE` 不在 `isBlockedEnvKey` 名单里，custom_env 的值会原样生效，对新 run 立即起作用，不用重启守护进程。

`MULTICA_DSH_SESSION_ROOT` 和 `DSH_TELEMETRY_DISABLED` 是守护进程独占的，custom_env 覆盖不了。

## 验收

```
cd server && go test ./internal/daemon/ -run 'Dsh|AgentPermission|OpenCode|CLIVersionAllowed' -count=1
cd server && go test ./internal/daemon/execenv/ -run 'CodexSandbox|CodexStartupSandbox' -count=1
```
