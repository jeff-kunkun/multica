# kun-main instance

The kun-main line runs its own instance at **https://office.ferryway.cc**. It
shares a host with the kun-line instance (ai.ferryway.cc) and nothing else:
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

People who stay on ai.ferryway.cc read Kun's board through a one-way copy:
the office backend (`server/internal/mirror`) copies one workspace's
projects, issues and comments into Kun's existing `kunkunya` workspace on
ai.ferryway.cc through its public API, every 20 seconds. Nothing is changed
on the kun line and nothing flows back; edits made on the copy are
overwritten by the next change here.

What is copied: projects; issues (title, description, status, priority,
parent, project); comments, each starting with its author's name. Mentions
become plain text. Copied issues carry no extra markers so they read like
Kun's own. A deleted issue or project becomes cancelled there; a deleted
comment is removed. Progress lives in the `mirror_link` table, so a restart
resumes.

No runs start there, with routing left on: every copy is assigned to Kun as
a member with the reviewer set to none, and issues are created in backlog
before their real status is set. Kun-line routing and patrols leave such
issues alone. If issue analysis is on in that workspace, it may still make
one model call per new or edited issue text; that is not an agent run.

Setup, once:

1. On ai.ferryway.cc, create a personal access token as Kun. Note the
   `kunkunya` workspace id and Kun's member user id there.
2. Add to `/opt/multica-main/.env` and redeploy:

```bash
MULTICA_MIRROR_WORKSPACE=<office workspace slug>
MULTICA_MIRROR_TARGET_URL=https://ai.ferryway.cc
MULTICA_MIRROR_TARGET_TOKEN=mul_...
MULTICA_MIRROR_TARGET_WORKSPACE=<kunkunya workspace id>
MULTICA_MIRROR_TARGET_ASSIGNEE=<Kun's user id there>
```

Any of the five unset means off. The log shows `mirror enabled` at start and
`mirror: copy failed` for anything the target refused; failures retry on the
next pass. Run only one office backend with the mirror on.

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
multica login --profile office --server-url https://office.ferryway.cc
multica daemon start --profile office
```
