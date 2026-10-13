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
