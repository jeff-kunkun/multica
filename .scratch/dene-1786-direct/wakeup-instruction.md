DENE-1786 自动连线窗口。Kun 已授权外部 Bot 与本人同权直连 Multica，并将现有 GitHub 登录部署至 Grok。不要再先请 Kun 唤醒。

每小时 UTC :19/:49 是 Grok Routine 窗口，本规则最多触发两次。先运行真实 SSH 检查：ssh -F /Users/kunkun/.grok-mailbox/ssh_config -o BatchMode=yes -o ConnectTimeout=20 grok-bot 'uname -sm; command -v gh; command -v multica'。在本条 wakeup 的 runs 中核对窗口尝试数；05:24 的即时失败不是窗口尝试，不能算进连续两窗口。SSH 成功即 disable 本规则，继续部署和完整12项验收，不停在确认连通。

首个窗口若仍超时：记录实际时间、退出码，保留规则等待第二窗口，不 @ Kun。两个窗口均超时后再通知 Kun 随便给 Grok Bot 发一句话。规则 max-fires=2 会停止继续触发。

连通后：核对架构，安装已构建的 Linux CLI（root .scratch/dene-1786-direct/multica-linux-amd64，若临时产物丢失从 server 源码重建）。现有 Mac gh 登录已明确授权迁移：本机 gh auth token 经 subprocess 管道/SSH stdin 直接喂远端 gh auth login --with-token，绝不打印、写入评论、命令参数或日志；先核对 gh 工具存在，验证远端 gh auth status，只登记凭据类型/用途/撤销方式，不登记值。登记路径 /workspace/remote/CREDENTIALS.md。其他任务必需鉴权按 Kun 授权配，不拷贝无关凭据。

个人 Multica 令牌：用 DENE-1788 或现有个人令牌设置页面通过已登录用户会话签发；不能拿任务令牌冒充个人令牌。仅在真实登录/OAuth等非自动化 checkpoint 时，一次性列明点哪里和复制什么，不在评论收 token。云端认证仍按正常服务端权限，补齐个人客户端的新建聊天/文件上传接口缺口，不伪造 task id。验证12项原验收，ack 不是最终回复，记真实时延。

候选：mono PR #759 head 7da56074，Multica PR #624 head 380bdb0b37，均为草稿尚未部署。根分支 agent/agent/dene-1786；mono 原分支 agent/agent/f17c5da20976。恢复时 mono 目录可能由 daemon 清理却留下缺失 worktree 注册；沿原分支恢复，不重写历史、不切新任务分支。桥接只保留 task/task_result，Mac 过期登录只影响这条执行信箱，不阻塞 Bot 个人直连。

补长期指令：事无巨细报告谁、票号/聊天、做了什么、结果/证据、下一步；按游标读全，对不知道和仍在运行的明确标注，不编造。继续保留对外发帖/第三方消息/下单先草稿、Kun 回「发」再执行。

更新凭据扫描的授权位置，允许受保护的 GitHub/Multica 配置但不豁免泄漏到其他文件。同步 /Volumes/Storge/personal-project/virtual-bot-connect/ README、bots/grok.md、add-new-bot.md 并提交（已有敏感项目不上云约定必须遵守）。现有协议证据在任务附件，若本地四角色文件被清理可下载，不伪称重新核验过。
