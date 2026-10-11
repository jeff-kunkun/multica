# DENE-1794 visual evidence

Local managed worktree: web localhost:13577, API localhost:18657.

Real issue route rendered from local database fixtures. Logged in through the actual email-code login page as dene1794-qa@example.test. Fixture writes through `make env-exec` only targeted the managed database. These initial fixtures do not prove PAT authentication.

Opened and inspected these PNGs:

| File | Observed browser innerWidth / innerHeight | Scope |
| --- | --- | --- |
| issue-375-verified.png | 375 / 1100 | Bot and direct comments, activity caption |
| issue-390-verified.png | 390 / 1100 | Bot and direct comments, activity caption |
| issue-768-verified.png | 768 / 1100 | Bot and direct comments, activity caption |
| issue-1280-verified.png | 1280 / 1100 | Bot and direct comments, activity caption |

Observed: long Bot name wraps beneath the author, direct member comment has no attribution caption, activity shows the historical Bot name. At CSS390 the comment caption occupies x68.90–199.57 and activity caption x57.98–235.76; both fit inside the viewport. `window.scrollTo(0,0)` ran before each capture.

Evidence limitation: Ego Default browser has110% zoom. CDP emulation width was compensated by1.1 to achieve the exact observed CSS widths above. Ego's screenshot helper also applies scaling, saving CSS390 as355×1000 and cropping the right edge. Thus these prove visible attribution behavior and wrapping, but are not full-width visual sign-off for the entire page. Do not claim full responsive acceptance from these cropped captures.

Chat: no usable screenshot. Cold page compilation took48–79seconds, exceeding the15second browser navigation timeout. After rendering issue successfully, subsequent chat navigation returned401 `/api/me` with `user not found` and redirected to login. The QA user still existed in the managed database (count1); re-login recovered temporarily but another cached issue navigation again redirected to login. Shared localhost cookies across concurrent worktrees are a suspected cause, not established proof. Stopped further retries per parent instruction.

Initial issue-390.png is a loading state. issue390-real.png used CSS354 due zoom. Exclude both from acceptance evidence.

Native simulator/device verification was not performed.

Cleanup: Ego spaces132 and133 closed; no task-owned browser pages retained.
