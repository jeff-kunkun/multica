已修复本次报错的配置原因：上轮把“读取……”的说明文字写进了 wait_probe，平台实际用 sh -c 执行，因此 command not found。

改为自包含的只读 Python/Multica CLI 命令，检查父任务交接线程是否出现新回复：有回复 exit 0，仅唤醒后人工语义核对；无回复 exit 2，继续等待；CLI/解析错误保留失败，不误报成功。已用与 daemon 相同的 sh -c 实测，当前输出 PENDING: no reply in parent handoff thread，exit 2，stderr 为空。

原 mono 修订与提交保留，发布限制未解除，不推送、不另开 PR、不重做补丁。原验证结果仍为 BASELINE I17 FAIL / MODIFIED PASS / ROLLBACK FAIL 且回滚哈希与原版相同；98 技能/50 席位绑定一致，全量漂移仍有其他席位差异。

## 知识审计
仅修正本票等待配置与补充原 VERIFICATION.txt；不改应用代码、全局画像、技能或项目记忆。四个原交付文件已重新打开核对。
