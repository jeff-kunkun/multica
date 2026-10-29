# Hide wakeups in office builds (DENE-2761)

The office calls people through the manager doorbell (status-light
`office/reminder`, @mention comments on the issue), not through issue wakeups.
Office client builds hide the wakeup surfaces; the server keeps the wakeup
machinery, because other workspaces on the same server use it.

## What the flag hides

With `office_hide_wakeups` in `VITE_MULTICA_FEATURE_FLAGS` (desktop) or
`NEXT_PUBLIC_MULTICA_FEATURE_FLAGS` (web):

- Settings → Wakeups leaves the settings list and search. It still opens from a
  link (`?tab=wakeups`), so an owner or admin can turn the sub-issue default off.
- Issue cards and list rows drop every wakeup cue: the bell on a run a wakeup
  started, the `+N` upcoming count, the "waiting for …" label and the "wakeup
  paused" alert. The card no longer fetches the wakeup summaries or paused
  rules. Running and queued runs still show as before.
- The run rows in the activity hover card drop the wakeup bell.

The issue page's own Wakeups section and `multica issue wakeup …` stay.

## Why the server side stays

Measured on production, read-only, 10-29 (last 30 days):

- Agent-created rules started 457 runs, member rules 125, the sub-issue rule
  (`child_done`) 28. Almost all of them ran in workspaces other than the office
  (alex, peter, 总有刁民想害朕, leslie). The office workspace had 2 agent runs and
  13 `child_done` runs.
- The sub-issue event pipeline (`issue_child_event`, the 30 s
  `issue_child_event_sweep`) also starts dependent sub-issues when the one they
  wait for is done (DENE-2671). That does not depend on the `child_done`
  switch, so turning the switch off leaves it working.
- The doorbell, the server watchdog (`internal/issuewatchdog`), status-light's
  watchdog and kun-agent-mono make no wakeup calls.

## Turning the sub-issue rule off for the office

Hiding the page does not stop the rule. An owner or admin of the office
workspace opens Settings → Wakeups from the link and switches 「默认开启」 off
(`PUT /api/system-wakeups/child_done` with `{"enabled": false}`). Issues that
never changed their own rule follow at once.

## Rollback

Remove `office_hide_wakeups` from the client build flags and the page and the
card cues return. Turn the sub-issue default back on from the same page.

The DENE-2756 per-workspace session context limit lived on this page. It was
never set in any workspace and is removed; see
[office-session-handoff.md](office-session-handoff.md).
