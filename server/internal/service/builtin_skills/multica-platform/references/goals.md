# Goals

A goal is one completion line attached to an issue. It does not add an issue
status or a new page. The completion line is a list of checks plus three
cumulative budgets: tokens, runs, and total duration.

## CLI

Use the same server contract from an agent or a human:

```bash
multica goal draft DENE-123 \
  --check 'unit tests pass' --method test \
  --check 'browser evidence attached' --method screenshot \
  --token-budget 50000 --run-budget 6 --duration-budget 3600
multica goal get DENE-123 --output json
multica goal confirm DENE-123
multica goal budget DENE-123 --tokens 10000 --runs 1 --duration 900
multica goal finish DENE-123 --status achieved
```

`draft` is the single opening action used by all product entry points. It
creates a goal in `draft` state. `confirm` locks the completion
line and moves it to `active`; once locked, an agent cannot change the
checks or budgets. Only a human can edit a locked goal through the product's
human confirmation flow. `budget` appends to the three limits, and `finish`
sets the goal to `achieved` or `stopped` after the server checks the request.

Every command accepts `--output json` for automation. The server is the source
of truth for validation, permissions, cumulative usage, and evidence; clients
must not reimplement those rules.
