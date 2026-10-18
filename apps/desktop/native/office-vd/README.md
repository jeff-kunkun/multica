# office-vd

Office agents browse in Kun's everyday Chrome, but on a virtual display nobody looks at, so they never
take his screen, pointer, keyboard or focus. This folder is the versioned home of the tools that used to
live only in `~/.agents/vd` (DENE-2261).

| File | What it does |
| --- | --- |
| `src/vd-hold.m` | Keeps the "Office Agents" virtual display (vendor 505, model 0x5644) alive; writes `state.json`. |
| `broker.mjs` | One CDP connection to Chrome on `127.0.0.1:9339`; gates every request on the stop latch. |
| `vd.mjs` | The agents' command line (`open`, `shot`, `close`, ...), always through the broker. |
| `latch.mjs` | The device stop latch: format, reader, `stop` / `resume`, CLI. |
| `src/screen-capture.swift` | ScreenCaptureKit helper the desktop app runs for the live picture. |

## Stop latch

`<vd home>/control/latch.json`, where `<vd home>` is `$OFFICE_VD_HOME` or `~/.agents/vd`:

```json
{"version":1,"state":"stopped","generation":7,"changedAt":"…","source":"desktop","reason":"stop"}
```

- Three readers apply the same rules on their own: the broker, the daemon
  (`server/internal/daemon/stop_latch.go`) and the desktop app (`apps/desktop/src/main/office-screen/latch.ts`).
  `test/latch-fixtures.json` pins them.
- No file means running, generation 0. Anything unreadable means stopped.
- Every write bumps the generation. Stop always wins; resume must name the stopped generation it saw.
  Nothing resumes by itself after a restart, reconnect or update.
- Writes are atomic and durable (temp file, fsync, rename, directory fsync), mode 0600.

While stopped, the broker passes only read-only methods (`Target.getTargets`, `Page.captureScreenshot`, ...)
and answers everything else with 423; the daemon claims nothing and cancels its runs, reporting them
failed with `device_stopped`, which the server never retries. Requests carrying an `Origin` header are
refused, so a web page can't drive Chrome through the broker.

## Screen capture helper

Streams JSON lines (`frame` / `status` / `heartbeat`) until its stdin closes. Every second it re-checks
that it is looking at the office display (not the main display) and that every browser window there is
one the broker registered in `control/owned-targets.json`; otherwise it stops the picture with a reason.
`--check-permission` never prompts; `--request-permission` is only for an explicit click in the app.

## Build, test, install

```sh
node apps/desktop/native/office-vd/build.mjs          # -> apps/desktop/resources/office-vd (packaged into the Mac app)
node --test apps/desktop/native/office-vd/test/
apps/desktop/native/office-vd/install-local.sh         # only after sign-off: backs up ~/.agents/vd, keeps state and latch
```
