本次失败已处理：后台探针调用 multica 返回 exit 3，而上轮只验证了任务进程中的 sh -c，不能证明 daemon 环境可用。现有 traceback 未显示 CLI stderr，不能据此断言是登录、权限还是工作区解析问题；不切换身份或复制凭据。

已撤掉本票 block.wait_probe 及其条件超时字段，改用平台原生 wake-at，在原截止时刻 2026-10-12T18:05:00Z 唤醒后由正常任务环境复查父任务讨论。这样不再周期执行后台 CLI，也不把失败伪装成 pending。发布限制仍未解除，任务保持 blocked。

原修订提交与四份产物继续保留；已有验证仍为 BASELINE I17 FAIL、MODIFIED PASS、ROLLBACK FAIL 且恢复原始哈希。本轮只处理等待配置，未改画像或应用代码。

## 知识审计
等待配置错误和修复记录补进原 VERIFICATION.txt；无新增全局规则或项目知识，未改技能。之前的“本地命令通过”不再被当成后台探针通过。
