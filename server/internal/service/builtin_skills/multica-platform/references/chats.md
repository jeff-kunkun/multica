# Chats

Contracts for reading and messaging chats from the CLI. Steer / queue /
restart costs are `references/run-control.md`.

Use the read-only chat directory before opening a transcript:

- `multica chat list [--project <id-or-exact-name>] [--compact] [--all-projects] [--since <RFC3339>] [--output json|table]`
- `multica chat search <词> [--project <id-or-exact-name>] [--compact] [--all-projects] [--since <RFC3339>] [--output json|table]`

Task-scoped commands default to the current project. Use `--all-projects` to
opt in to a workspace-wide directory. Visibility follows the person who
started the task, so another member's private chats stay hidden. Listing and
reading chats never changes unread state. Use `multica chat history` for a
bounded transcript after choosing a session.

To message a chat while its agent may still be replying, use
`multica chat send --session <id> --content-file <path> --mode steer|queue|restart|replace`.
When Kun changes their mind, use `--mode replace` (voids queued old messages and
the running reply, runs the new message); `multica chat clear-queue --session <id>`
just drops the queued follow-ups.
The same `--mode` works on `multica issue comment add`, which also takes
`handoff` / `parallel` when you @ an agent other than the one running; a chat
moves to another agent with `multica chat handoff --to <agent>`. What each mode costs,
and what happens when the running CLI cannot steer, is in
`references/run-control.md`.
