## Option questions

Use `multica ask` when a run needs a person to choose among explicit options.
The server validates one to four questions with two to six options each, fills
unanswered questions from their recommended option, and wakes the asking agent
after an answer.

```sh
multica ask create --title "Choose the rollout" \
  --questions-file ./questions.json --issue <issue-id> --output json
multica ask list --status open --output json
multica ask get <ask-id> --output json
multica ask answer <ask-id> --answers-file ./answers.json --output json
```

`questions.json` is a JSON array of `{ "text": string, "options": [{
"id": string, "label": string, "recommended": boolean }] }` objects. The
answer file maps question indexes (`"0"`, `"1"`, …) to option ids or the text
entered through the explicit “other” choice.

When a goal reaches its budget or no-progress brake, the server creates the
same `needs_you` ask on the issue with `extend_budget`, `change_goal`, and
`stop` options. Answer it through the normal ask surface; use `multica goal
budget` to append the new limits, which resumes the stopped goal and queues its
next round.

When an acceptance seat fails and no other seat can cover it (DENE-1647), the
server opens one `needs_you` ask on the issue with `review_stuck.retry` (换席位),
`review_stuck.member` (我来验) and `review_stuck.close` (直接关票). The answer is
carried out by the server: retry looks for another acceptance seat, member
makes the answerer the reviewer, close marks the issue done.

### Cards and the person's Bot (DENE-1933)

A person who connected a Bot gets their cards through it. A `needs_you` card
for them, and every `multica issue ask-human` that carries two to six
`--option`s (the output shows `ask_id`), becomes one `human_needed` event
carrying the card: the push payload has an `ask` block with the questions and
option ids, and `GET /api/asks/<ask-id>` returns the same card. The in-app row
stays, read, as the record. The Bot's `answered` receipt fills the card:

```sh
multica bot wake answered <bot-id> <event-id> --answers 0=approve,1=later
```

`--answers` maps question numbers to option ids (or JSON `{"0":"approve"}`).
Only the person the card was asked of can be answered for: another person's
Bot is refused. Answering fills the card exactly like `multica ask answer`,
leaves a comment on the ticket as the person marked "Bot X for Y", lifts the
ask-human block and queues one run for the agent that asked; a repeated receipt
changes nothing. Do not also post a comment yourself. Questions with fewer than
two options are free-text: no card, the person answers with a ticket comment.
In a chat, `multica chat ask-human` writes the question into the chat as a
message (options as a numbered list); the Bot's `--answer` is written back as
the person's own message, which queues the run, and the person typing in the
chat settles the Bot's chase too.
