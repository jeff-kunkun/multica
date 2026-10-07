## What changed

The runtime brief is now a verb map, following ADR-0007:

- **Workflow** is four steps: read the state card (`multica issue context`), set `in_progress`, do the work, finish with `multica issue close`.
- Deleted from the brief: the close decision table, the long status rules, and the mandatory roots-only comment scan. The server already refuses each of these mistakes with a readable error (mapping below).
- **Commands** lists one line per verb; flags live in `--help`.
- **Formatting rules moved out of the brief:**
  - Comment bodies → `multica issue comment add --help`.
  - Issue title style and body formatting → `multica issue create --help`. Quick-create still gets both in its brief, because creating the issue is that brief's only job.
  - The acceptance seat's pass/hold → `multica issue close --help`.
- **Session resume:** the 4 `.multica/notes.md` references are gone. The continuity notices now come down to "read the state card first", or `multica chat history` for chats.
- **Background work** is one paragraph and keeps all four constraints: no background-and-yield, no waiting on CI, the persistent-service exception, and never killing `multica` by name or the daemon PID.
- **Per-turn hints** (`reply_instructions.go`, `prompt.go`) point at the state card and `issue comment add --help` instead of "workflow step 2" and "## Comment Formatting above".
- **Size gate tightened:** issue 6000, chat 4000, quick-create 5200, autopilot 3700. Every ceiling sits within 1.2KB of the measured size, and issue and chat are under their 8KB / 6KB targets.
- The `multica-platform` skill and ADR-0007 are updated to match.

## Bytes per section (minimal context, `claude`)

| Section | issue before | issue after | chat before | chat after | quick-create before | quick-create after | autopilot before | autopilot after |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| preamble | 127 | 127 | 127 | 127 | 127 | 127 | 127 | 127 |
| Background Task Safety → Background work | 2279 | 644 | 2279 | 644 | 2279 | 644 | 2279 | 644 |
| Agent Identity | 51 | 51 | 51 | 51 | 51 | 51 | 51 | 51 |
| Available Commands + Core → Commands | 7255 | 1316 | 7255 | 1316 | 1534 | 443 | 7255 | 1316 |
| Issue Body Formatting | 242 | — | 242 | — | 242 | 242 | 242 | — |
| Title Style | 1620 | — | 1620 | — | 1620 | 1620 | 1620 | — |
| Comment Formatting | 1055 | — | — | — | — | — | — | — |
| Instruction Precedence | 410 | 361 | — | — | — | — | — | — |
| Workflow | 10011 | 1372 | 1309 | 593 | 1325 | 814 | 441 | 441 |
| Sub-issue Creation | 184 | 184 | — | — | — | — | — | — |
| Mentions | 1102 | 446 | — | — | — | — | — | — |
| Attachments | 331 | — | — | — | — | — | — | — |
| Always Use the `multica` CLI | 250 | — (into Commands) | 250 | — | 250 | — | 250 | — |
| Output | 1426 | 1022 | 1062 | 804 | 984 | 858 | 786 | 660 |
| **Total** | **26343** | **5523** | **14195** | **3535** | **8412** | **4799** | **13051** | **3239** |

## Deleted rules and the server refusal that replaces each

| Rule removed from the brief | Who refuses it now |
| --- | --- |
| A sub-issue must not go to `in_review` | `issue close` refuses `in_review` on a sub-issue |
| `--verdict pass` only from the acceptance seat, on `in_review`, with `done` | `issue close` refuses any other combination |
| A `blocked` or `in_progress` close must name what it waits on | `issue close` refuses it, naming the missing flag |
| An agent's `blocked` needs a wait, a kind and an action | `issue status blocked` refuses it for agents |
| Reply under this turn's `--parent` | A comment-triggered run is refused when `--parent` is missing or wrong |
| The comment file must be in the workdir | The CLI rejects a `--content-file` outside the workdir (MUL-4252) |
| `--duplicate-of` | Documented in `issue status --help` |

Setting `in_progress` first stays in the brief, because the server does not do it automatically.

## Three surfaces

- **Server and CLI:** the moved rules live in each command's `--help`, and the server refusals above.
- **UI:** none. This only changes the text the agent runtime reads, so there is nothing for a person to see or change.

## Tests

- `go test ./internal/daemon/execenv ./internal/titling -count=1`: pass. Tests that pinned old wording were rewritten to the new contract, not deleted.
- `go test ./internal/daemon -count=1`: the brief and prompt tests pass. The remaining failures are environment leaks from the task runtime (`TestLoadConfig_*`, `TestEnsureDaemonID_*`, `TestPrepare*`). They fail the same way on the baseline.
- A new `cmd/multica` test pins the moved rules in the `issue create`, `issue comment add` and `issue close` help text.

Closes DENE-1329

🤖 Generated with [Claude Code](https://claude.com/claude-code)
