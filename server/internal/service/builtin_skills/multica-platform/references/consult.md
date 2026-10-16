# Consulting a stronger seat

`multica issue consult` lets a run working an issue ask a strong-tier seat one
question and wait for its advice. You stay the executor: the advisor only
reads, changes no code or issue, and its answer comes back to your terminal.

```bash
multica issue consult <id> --question-file ./ask.md [--wait 15m] [--output json]
multica issue consult <id> --resume <consult-id>   # the wait ran out; the answer is kept
```

## When to ask

| Moment | What to put in the question |
|---|---|
| Before you start | your plan: "here is how I would do it, what am I missing?" |
| Before you deliver | the diff or result: "review this before I close" |
| Stuck between two approaches | both options and what you cannot judge |

A ticket gets a few consults (3 by default; Settings → Routing → Consults per issue; the permission to consult remains in Agent permissions). Do not ask for what you can look up, and do not consult instead of
working. The advisor sees the issue and your question — not
your working copy — so paste the code, diff or error that matters.

## Who answers

The unified `route(ticket, consult)` selector targets one tier above weak/medium executors. Strong executors first try another strong peer (prefer another model family), then an ample-quota strongest seat. It uses the same direction and soft family preferences used for dispatch and review.
It tries an adjacent tier when needed (above before below), never strongest as
a fallback. Never you. The seat's daemon must be online and new enough to run consults.

## Cost

The advisor's tokens and time are added to the advisor seat's own usage. Each consult leaves one line on
the issue timeline — advisor, cost, and on expand the question and the answer.

## Refusals

A refusal comes back at once; the command prints it and exits 0. Carry on with
your own judgment.

| Code | Means |
|---|---|
| `consult_not_a_run` | only a run working this issue can consult; people comment and @mention |
| `consult_disabled` | the workspace switched consults off |
| `consult_limit_reached` | this issue used its consults (`limit` says how many) |
| `consult_no_advisor` | no eligible seat at the target or adjacent tier |
| `consult_advisors_unavailable` | every strong seat is out of quota, offline or busy |

A consult that got no answer (the advisor run failed or was cancelled) ends
`failed` and does not count against the limit.

## Consult or escalate

| You want | Run |
|---|---|
| advice, and keep the ticket | `multica issue consult` |
| a stronger seat to take the ticket over | `multica issue escalate` |

Consult before requesting escalation when an advisor is available. Escalation
records how many consultations have finished. Advisors may also be reviewers;
only the executor is excluded from reviewing its own delivery.
