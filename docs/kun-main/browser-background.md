# Office browser operations: background channel only

Kun's everyday Chrome has "Remote debugging" switched on (`chrome://inspect/#remote-debugging`).
It listens on 127.0.0.1 only, on a random port, and survives no restart of Chrome:
the port and browser path are rewritten on every launch into
`~/Library/Application Support/Google/Chrome/DevToolsActivePort`
(line 1 = port, line 2 = `/devtools/browser/<id>`). Read that file every time; never hardcode the port.
Do not restart Chrome to enable it. The `Chrome-Tarot` profile on port 9235 is separate and stays as is.

Rules for every agent that operates Kun's browser:
- Use CDP over `ws://127.0.0.1:<port><path>`. Do not use AppleScript `activate`, `open -a`, pixel clicks,
  or hotkeys on Chrome; they raise the window and move the real mouse.
- Open pages with `Target.createTarget {url, background:true}`, never `newWindow`. Drive and read the page
  through the attached session (`Runtime.evaluate`, `Page.captureScreenshot`, `Input.dispatch*` sent to the page).
  Close the tab you opened with `Target.closeTarget` when done; leave Kun's tabs alone.
- Never call `Page.bringToFront` or `Target.activateTarget` on your own.

Only exception: a page that needs the boss himself (login, captcha, payment, a real click or confirmation).
Then, and only then, bring that one tab forward with `Target.activateTarget` and notify Kun in the same step:
say which page and what to click, in the issue comment (`@`-mention Kun) and, if the runtime has it, a push notification.
Do not keep driving the page until Kun has acted.

![demo](img/browser-background-demo.png)

Demo (DENE-2197): background-open `github.com/notifications`, scroll, read logged-in state, screenshot, close.
During it the frontmost app, Chrome window id, active tab and tab count did not change, and the script sends no
`Input.*` commands.
