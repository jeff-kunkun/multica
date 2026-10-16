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
Bot is refused. An `owner_confirmation` card (a Bot asking its owner before
deleting the workspace, changing members, resetting keys or changing billing,
DENE-1936) is the exception: it is pushed to the owner's Bot as a notice, but
only the owner's own session answers it; the Bot's `answered` receipt, an agent
run or another member is refused. Answering fills the card exactly like `multica ask answer`,
leaves a comment on the ticket as the person marked "Bot X for Y", lifts the
ask-human block and queues one run for the agent that asked; a repeated receipt
changes nothing. Do not also post a comment yourself. Questions with fewer than
two options are free-text: no card, the person answers with a ticket comment.
In a chat, `multica chat ask-human` writes the question into the chat as a
message (options as a numbered list). With 2–6 options it is also the same
shared card: it shows in the person's inbox, `multica ask list/get/answer` reads
and fills it, and the Bot answers it with `--answers`. Whoever fills it (the
Bot, the app, the CLI), the chosen option is written into that chat as the
person's own message and queues the asking agent's run once; a repeated answer
is refused, and only the chat's owner can answer. The person typing a reply in
the chat instead closes the card and settles the Bot's chase. Fewer than two
options stays free text: no card, and the Bot's `--answer` is written back as
the person's message.

A card made for a person with a Bot binding (`multica ask --mode needs_you`,
`issue ask-human`, `chat ask-human`) resumes the asking agent once however it
is answered: through the Bot, in the app or with `multica ask answer`, also in
mirror mode and when the Bot channel is broken.

Answering the card and carrying the answer back (the chat message, or the
ticket comment) commit together. If the write fails the card stays open and the
error says so: give the same answer again and it lands once; an answer repeated
after that is refused with 409 and writes nothing. `--urgent` and `--deadline`
on `chat ask-human` apply to a choice card and to free text alike.
