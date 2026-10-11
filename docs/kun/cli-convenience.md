# CLI project, file and read options

These options change CLI ergonomics and opt-in API response projections. They
do not change daemon heartbeat, offline detection, credential selection or
renewal. Cross-workspace feeds and active-run discovery have their own
[fast-access contract](fast-access.md).

```sh
multica issue create --project "Multica 魔改" --title "Example" --description-file /tmp/body.md
multica issue list --project "Multica 魔改" --fields id,title,status --output json
multica chat list --project "Multica 魔改" --compact --output json
multica agent list --compact --output json --workspace "My workspace"
multica runtime list --fields id,name,status --output json --workspace <workspace-id>
multica auth status --output json
```

`issue create/list` and `chat list/search --project` accept a UUID, UUID prefix,
or case-insensitive exact project title. Duplicate names produce candidate
UUIDs, titles and statuses. Full UUIDs need no directory lookup; other inputs
read the selected workspace's project directory with `fields=id,title,status`.

Body files under the current working directory remain allowed. Content and
description files may also be regular files owned by the current OS user under
`/tmp`, `$TMPDIR` or the platform system temp root. Both roots and candidates
are resolved through symlinks; an escape outside every allowed root remains
external. Other paths require `--allow-external-file`. Attachments and other
file flags keep their existing rules. Use unique temp filenames when runs
share an OS account: ownership does not distinguish two runs of that account.

`agent list`, `chat list/search` and `runtime list` accept `--compact` and
`--fields`. Full responses remain the default. Explicit fields override compact
defaults and require JSON output. The APIs accept the same `compact=true` and
`fields=a,b` query options; fields are public top-level response names and
unknown/empty names fail validation, including on empty results. Projection
runs after existing visibility and secret-redaction checks. The CLI also
filters replies from older servers that ignore these query parameters.

Compact agent rows omit instructions, skills and configuration. Chat rows omit
summaries and originator details. Runtime rows omit metadata, device details and
plan snapshots. Fetch a full row when those details are actually needed.
Responses retain their original array/envelope shape. Lists are not newly
paginated by these flags; use the command's existing pagination options.

API reads accept the global `--workspace <UUID|slug|exact-name>` option. Names
are case-insensitive and duplicates require a slug/UUID. The selection is local
to this invocation and wins over `--workspace-id`, `MULTICA_WORKSPACE_ID` and
the profile default. It does not write the environment or profile. The chosen
workspace and an environment conflict are printed on stderr so JSON remains
alone on stdout. Existing local `--workspace` options (transfer and any
cross-workspace command) retain their own meaning. `--workspace` and `--linked`
are mutually exclusive; a link is not membership in another workspace.

`auth status` reports key type (`bot`, `personal`, `task`, `cloud_node`,
`session`, or `unknown`), source (`MULTICA_TOKEN` or profile), key name/ID and
expiry without printing token material. Bot and personal metadata come from
existing token/Bot read endpoints. Null expiry with `expiry_known=true` means
no expiry; `expiry_known=false` means unknown. Seven days before expiry a
renewal hint is printed. A rejected key returns the existing auth exit code 3
and keeps the selected identity. Network failures retain network errors.

Bot renewal uses `multica bot key <bot-id> --workspace <workspace-id> --profile
<admin-profile>` from an administrator's authenticated session without
`MULTICA_TOKEN`, then installs the new issued key. Personal keys use
`multica login`; an explicit environment key must also be updated. Display
metadata is cached per profile/server/key fingerprint, never used for
authentication or token selection. A rejected key may show cached information;
a key rejected before its metadata was ever read has unknown type/expiry and
receives both Bot and personal recovery instructions. Task/cloud keys retain
their own renewal paths.

The Web/Desktop and native mobile UI continue using full default responses.
These CLI-only options introduce no screen or navigation flow on those clients.
