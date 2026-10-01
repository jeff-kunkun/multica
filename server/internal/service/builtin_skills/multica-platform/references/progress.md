# Progress updates

Agents can publish the current one-line progress for work without opening the UI:

- `multica issue progress <issue-id> "<progress>" --output json`
- `multica chat progress "<progress>" --session <chat-session-id> --output json`

The update is stored in the issue or chat session, appears on the board/list/detail surfaces, and is pushed to connected clients over WebSocket. Issue close summaries become progress when no newer agent update exists.
