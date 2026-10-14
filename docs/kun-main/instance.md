# kun-main instance

The kun-main line runs its own instance at **https://office.example.com**. It
shares a host with the kun-line instance (ai.example.com) and nothing else:
own database, own upload volume, own ports, own images, own update timer.

| | kun-main (office) | kun line (ai) — do not touch |
| --- | --- | --- |
| Checkout | `/opt/multica-main` | `/opt/multica` |
| Compose project | `multica-main` | `multica` |
| Database / uploads | volumes `multica-main_pgdata`, `multica-main_backend_uploads` | `multica_*` |
| Host ports | backend 8090, web 3010 | 8080/3000, 8081/3001 |
| Images | `ghcr.io/kkunkunya/multica-main-{backend,web}:sha-<sha>` | `ghcr.io/kkunkunya/multica-{backend,web}` |
| Update | `multica-main-autoupdate.timer` | `multica-autoupdate.timer` |
| State | `/var/lib/multica-main/autoupdate.json` | `/var/lib/multica/autoupdate.json` |
| nginx | `/etc/nginx/sites-enabled/multica-main` | `multica`, `origin-ai` |

## Release path

1. A merge to `kun-main` is picked up by the `build-kun-main.yml` workflow in
   `kkunkunya/multica-image-builder` (every 10 minutes), which pushes
   `multica-main-backend` and `multica-main-web` tagged `sha-<sha>` and
   `kun-main`.
2. `scripts/kun-main/autoupdate.sh` (every 5 minutes) deploys the tip once both
   images exist, checks `/health`, and rolls back to the previous commit if it
   fails. Migrations run in the backend entrypoint.
3. CLI and Desktop builds are published only to
   `kkunkunya/multica-main-release`. `multica update` and Desktop auto-update
   read from there. Never publish them to `jeff-kunkun/multica` Releases —
   colleagues' Desktop follows that feed.

The office Desktop app is "Multica Office" (`ai.multica.office`, URL scheme
`multica-office://`), so it installs beside the kun-line Multica.app and the
browser login hands back to the right one. Its runtime override is
`~/.multica/office-desktop.json` and its daemon auto-start/stop prefs are
`~/.multica/office-desktop_prefs.json`; it never reads the kun-line
`desktop.json` / `desktop_prefs.json`. App data lives under its own name too.
The daemon it starts uses a profile named after the server host, separate from
the kun-line app's.

### Signing and Full Disk Access

Agents started by Office need Full Disk Access. macOS never asks for it: the
app has to be added with "+" in System Settings → Privacy & Security → Full
Disk Access. Without it, OpenCode and Codex's plugin loader hang on their first
protected file read. Office checks at startup and on window focus. When the
permission is missing, the first row of Settings → 本机运行 links to that page.

macOS keys the grant to the app's signature. Ad-hoc signatures change with
every build, which drops the grant on each upgrade. So `pnpm package` on a Mac
without Developer ID credentials (`CSC_LINK` / `CSC_NAME` / `APPLE_TEAM_ID`)
signs with one fixed local identity, "Multica Office Local Signing":

- `apps/desktop/scripts/mac-local-signing.sh` creates it the first time: a
  self-signed certificate in its own keychain under
  `~/.multica/office-signing/`, with a random password beside it (0600).
  Nothing is committed.
- `apps/desktop/scripts/mac-local-sign.mjs` is electron-builder's `mac.sign`
  hook. electron-builder ignores untrusted identities on its own.
- Check: `codesign -dr - "Multica Office.app"` should show
  `certificate leaf = H"…"` with the same hash on every build.

Build releases on the same Mac. Deleting that folder or switching Macs mints a
new certificate, and Full Disk Access must be granted once more. So does the
first build after switching away from ad-hoc. `MULTICA_MAC_LOCAL_SIGN=0`
restores ad-hoc.

Client defaults point here: the CLI's default server
(`server/cmd/multica/cmd_agent.go`) and Desktop's default runtime config
(`apps/desktop/src/shared/runtime-config.ts`).

## Login

Email codes are off. `MULTICA_PASSWORD_AUTH=1` plus the bootstrap
username/password/email in `/opt/multica-main/.env` turn on password login;
`MULTICA_SIGNUP_TOTP_SECRET` (base32) gates sign-up and self-service password
reset behind the team's shared authenticator code. Secrets live only in that
`.env` (mode 600).

## Mirror to the kun-line instance

People who stay on ai.example.com keep working with Kun there as before; the
work itself happens here. The office backend (`server/internal/mirror`)
keeps one workspace here and Kun's `kunkunya` workspace on ai.example.com in
step through both servers' public APIs, every 20 seconds. Nothing is changed
on the kun line.

Out (here → there): projects; issues (title, description, status, priority,
parent, project); comments. A person's comment starts with their name
(`Kun：`), an agent's with its coding CLI (`Codex：`, `Claude Code：`).
Mentions become plain text. A deleted issue or project becomes cancelled
there; a deleted comment is removed.

Back (there → here), when `MULTICA_MIRROR_LOCAL_TOKEN` is set:

- comments on the copies, posted here as Kun and starting with the speaker's
  name (`Allen：这里再看下`), threaded under the matching comment. They wake
  the issue's assignee like any comment; an `@agent` mention there becomes a
  mention of the agent with the same name here.
- issues created there, or open ones held by Kun or an agent there. They are
  created here with the same-named agent as assignee (else Kun), then the
  original there is handed to Kun like every copy.
- status changes on the copies, unless the issue also changed here since the
  last pass; then this side wins.

No echo: every copied or pulled object is a row in `mirror_link`, and
nothing linked, and nothing Kun writes there, is pulled back. Deleting a
pulled comment here leaves the original alone. The pull's position on the
target is kept in the same table, so a restart resumes; the first pull only
records where the target is now, so nothing older is pulled.

No runs start there. Every copy is assigned to Kun as a member with the
reviewer set to none, and issues are created in backlog before their real
status is set. People can still `@` an agent or assign one there, so turn
off work for every agent in `kunkunya` (agent settings → work, or
`multica agent update <id> --work-enabled=false` against that server); a
disabled agent refuses even a mention, and the pull brings the request here.

Setup, once:

1. On ai.example.com, create a personal access token as Kun. Note the
   `kunkunya` workspace id and Kun's member user id there. Turn off work for
   the `kunkunya` agents.
2. Here, create a personal access token as Kun for the pull.
3. Add to `/opt/multica-main/.env` and redeploy:

```bash
MULTICA_MIRROR_WORKSPACE=<office workspace slug>
MULTICA_MIRROR_TARGET_URL=https://ai.example.com
MULTICA_MIRROR_TARGET_TOKEN=mul_...
MULTICA_MIRROR_TARGET_WORKSPACE=<kunkunya workspace id>
MULTICA_MIRROR_TARGET_ASSIGNEE=<Kun's user id there>
MULTICA_MIRROR_LOCAL_TOKEN=mul_...   # optional: turns on the pull back
MULTICA_MIRROR_PROJECTS=<id>,<id>    # optional: copy only these projects
# MULTICA_MIRROR_LOCAL_URL defaults to http://127.0.0.1:$PORT
```

Any of the first five unset means off. With `MULTICA_MIRROR_PROJECTS` set,
only those projects, their issues and those issues' comments go out; moving
an issue out of them counts as deleting it. The pull back is not limited. The log shows `mirror enabled ...
pull=true` at start, `mirror: copy failed` / `mirror: pull failed` for
anything refused; failures retry on the next pass. Run only one office
backend with the mirror on.

Removing it: delete `server/internal/mirror`, its block in
`server/cmd/server/main.go`, the `MULTICA_MIRROR_*` lines in
`docker-compose.selfhost.yml`, and drop the `mirror_link` table. Nothing
else refers to it.

## Moving to the schema baseline

Migrations up to 585 are compressed into `001_baseline` (see
`server/cmd/migrate/README.md`). A database that stopped before 585 is refused,
and the office database was created at 573, so the first deploy of a baseline
build needs an empty database. Any deploy left to the timer would fail its
health check and roll back, and wiping early lets the old build recreate a
573 database. Do it in this order, once both baseline images exist:

```bash
cd /opt/multica-main
sudo systemctl stop multica-main-autoupdate.timer
docker compose -p multica-main down
docker volume rm multica-main_pgdata
sudo systemctl start multica-main-autoupdate.service   # deploys the tip on an empty database
sudo systemctl start multica-main-autoupdate.timer
```

The password-login bootstrap recreates Kun's account from `.env`; workspaces,
agents and tokens start over, so log the `office` CLI profile in again.

## Moving the kunkunya workspace in

`server/cmd/movein` copies Kun's `kunkunya` workspace on ai.example.com into
the office workspace once: projects as they were (memory and resources),
every issue done or cancelled with its comments, its old number where free
and a footer naming the old number, holder and status, and every chat read.
Nothing is assigned, notified or run; the inbox stays empty. Old agents and
attachments do not move. It only reads the old server, writes this database
directly, and running it again skips what already arrived.

Run it on the fresh database, after the office workspace and its agents
exist (chats go to the agent with the same name, else the same coding CLI)
and before the `MULTICA_MIRROR_*` lines are set: every moved object is
recorded as a mirror link to its original, so the mirror never copies them
back or pushes their new status, and it reads those links only at start.
`--from` must be the same URL as `MULTICA_MIRROR_TARGET_URL`.

```bash
# on a machine with Go, for the server's architecture
cd server && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o movein ./cmd/movein
# on the server
docker compose -p multica-main cp movein backend:/tmp/movein
docker compose -p multica-main exec -e MULTICA_MOVEIN_TOKEN=mul_... backend \
  /tmp/movein --from https://ai.example.com --from-workspace kunkunya --to-workspace <office slug>
```

The token is Kun's personal access token on ai.example.com. It prints the
counts as JSON; a run that stops halfway is finished by running it again.
Once the move is done, delete `server/cmd/movein`.

## Operating

```bash
cd /opt/multica-main
sudo systemctl start multica-main-autoupdate.service   # update now
journalctl -u multica-main-autoupdate -n 50             # last runs
cat /var/lib/multica-main/autoupdate.json               # deployed commit
docker compose -p multica-main ps
```

Connect a local daemon with a separate profile:

```bash
multica login --profile office --server-url https://office.example.com
multica daemon start --profile office
```
