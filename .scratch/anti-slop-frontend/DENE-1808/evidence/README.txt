DENE-1808 frontend evidence — final managed-delivery scope

Preview: docs/design/prototypes/dene-1808-managed-delivery.html
Screenshots: managed-375.png, managed-390.png, managed-768.png, managed-1280.png
All four captured via Ego browser, scrollY=0, opened with image viewer. No clipping.
Preview contains fixture content: newly created link has managed enabled, can be unchecked;
message reads Kun 经 WD·游戏助手 代发. Preview is not actual route verification.

Actual app attempt on managed isolated local instance:
http://localhost:13319/dev/issues initially timed out during compilation.
Later /login rendered; used documented local dev@localhost / dev verification code.
Observed resource requests use http://localhost:18399 (isolated managed API).
Login reached onboarding, no existing workspace shown. Entered DENE-1808 QA /
dene-1808-qa and clicked create; remained on creation screen, success not confirmed.
Actual settings/chat route not visually verified. No native simulator available.
Task Spaces 147, 151, 155 all finished; no browser spaces retained.

Native app has no workspace link settings screen. Its existing chat screen now
preserves linked_delivery kind and renders server-authored provenance as literal
text. Web/desktop share settings and chat implementation.

Copy gate: preview 0 block / 0 warn. Source scan warnings were existing Boolean
negation (!!) false positives and pre-existing workspace intro copy; new visible
copy manually reviewed. Old reception proposal and preview screenshots removed.
