# Automatic stall actions

停滞处理和任务本身共用同一张票、评论与收件箱记录，不另建清理页。

当父票的全部子票进入终态时，服务端直接把仍开放的父票标记为 `done`，并在票上写系统说明。父票若有 `close.conclusion=in_progress` 的有意暂停记录，服务端不处理。自动收口或自动取消后 7 天内可以撤销，撤销会恢复服务端记录的原状态。

重复或无效票由 AI/可信调用方先提交理由，服务端会强制把“已核对关联 PR、附件与其他交付产物”写进理由，再公示 24 小时。公示期内保留即可阻止自动取消；到期无人保留才会取消。每一步都会有票上系统评论和收件箱提醒。

```bash
multica issue stall list --output json
multica issue stall keep <issue-id> --output json
multica issue stall undo <issue-id> --output json
```

`list` 返回公示中、已保留、已取消、已自动收口和已撤销的处理记录。`keep` 只接受仍在 24 小时公示窗口内的票；`undo` 只接受仍在 7 天窗口内且由系统自动处理的票。Web/Desktop 使用相同的 `/api/issues/stall-actions`、`/api/issues/{id}/stall/keep` 与 `/api/issues/{id}/stall/undo` 接口。
