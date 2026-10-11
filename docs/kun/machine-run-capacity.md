# 每台机器同时运行上限

同一台机器的运行环境和工作区共用一个上限，默认 12，同时受智能体本身的上限约束。
机器身份沿用 `agent_runtime.daemon_id` 并按机器主人隔离，防止其他账户重用机器名称影响限额；
没有 daemon id 或主人的独立运行环境按自身 ID 隔离。
设置存放在 `runtime_machine_capacity`，注册和心跳不覆盖人的设置。

Web/Desktop 运行时列表的机器卡片与机器详情均可修改，手机 App 的设置页可查看。
只有机器主人可修改；工作区管理员身份本身不授权修改别人的全机上限。
取值为整数 1–100。调低不停止已有运行，等数量低于新上限后再领取新运行。

CLI 与外部 Bot 使用同一个服务端入口：

```sh
multica runtime capacity <runtime-id> --output json
multica runtime capacity <runtime-id> --max-concurrent-runs 8 --output json
```

`PATCH /api/runtimes/:id` 接受 `max_concurrent_runs`。
运行时列表、PATCH 返回与 `multica overview` 的 `runtimes` 包含
`machine_capacity: {active_runs, max_concurrent_runs}`；概览只暴露计数，不暴露其他工作区任务。
CLI 能力地图列出命令、Bot 权限和使用示例。

领取新运行与修改上限共用事务级机器 advisory lock；各智能体原有锁与上限继续生效。
已派发、运行中、等待本地目录均占一个名额，重新递送已有派发不会重复占用。
超额保持 `queued`；聊天待运行接口和工单运行接口动态返回
`queue_reason: {code: "machine_capacity", active_runs, max_concurrent_runs}`，前端显示
「排队中：机器已满（12/12）」。有排队任务时按 5 秒刷新；机器卡片也按 5 秒刷新当前计数。

数据库迁移为添加表和函数，旧版服务端可继续使用新库；旧版服务端仍不执行新上限，
因此全机保障从所有领取实例升级后成立。退回代码会回到旧领取行为，新增数据表可保留；
确认没有新版实例再执行 down migration。
