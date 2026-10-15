# Chats

Contracts for reading and messaging chats from the CLI. Opening a chat with
another agent is `references/chat-spawn.md`; steer / queue / restart costs are
`references/run-control.md`.

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
A message that is worthless late (a verification code, a relayed SMS) goes with
`--urgent`: refused at once when the runtime is offline, otherwise head of the
queue, and the command exits 0 only once a daemon has started the turn within
the 30 s pickup deadline (`run-control.md` → "Urgent delivery").
The same `--mode` works on `multica issue comment add`, which also takes
`handoff` / `parallel` when you @ an agent other than the one running; a chat
moves to another agent with `multica chat handoff --to <agent>`. What each mode costs,
and what happens when the running CLI cannot steer, is in
`references/run-control.md`.

To talk to another agent without creating work, open a chat from your chat:
`multica chat open --agent <name> --brief-file ./brief.md`. Work with an owner
and a deliverable is still an issue. Limits and refusal codes:
`references/chat-spawn.md`.
