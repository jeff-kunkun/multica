# office-vd

Office agents browse in Kun's everyday Chrome, but on a virtual display nobody looks at, so they never
take his screen, pointer, keyboard or focus. This folder is the versioned home of the tools that used to
live only in `~/.agents/vd` (DENE-2261).

| File | What it does |
| --- | --- |
| `src/vd-hold.m` | Keeps the "Office Agents" virtual display (vendor 505, model 0x5644) alive; writes `state.json`. |
| `broker.mjs` | One CDP connection to Chrome on `127.0.0.1:9339`; gates every request on the stop latch. |
| `vd.mjs` | The agents' command line (`open`, `shot`, `look`, `bring`, `close`, ...), always through the broker. |
| `handoff.mjs` | Handing a window to Kun without taking his focus (see below). |
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

## Handing a window to Kun

When an agent needs Kun (a login, a captcha), `vd.mjs look <targetId> <message>`:

1. moves that one window to the bottom-right corner of his main screen with `Browser.setWindowBounds` only.
   It never activates Chrome or the page, never moves the pointer and never sends input, so whatever he
   is typing into keeps the keyboard;
2. posts a notification. With `terminal-notifier` installed, clicking it runs `vd.mjs bring <targetId>`,
   the only command that activates the window (`Target.activateTarget`). Without it, macOS's own
   `osascript` notification can't run anything on click, so the text tells him to Cmd-Tab to Chrome.

macOS can't put a window above his others without activating its app, so until he clicks the window may
be covered by his current one. Both commands go through the broker and the latch: while stopped they exit
4 before touching Chrome. `peek` no longer opens Preview unless `PEEK_OPEN=1`.

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
