DENE-1803 real browser verification
Local isolated workspace overview-qa; API localhost:18957, web localhost:13877.
No mock API or screenshot fixture injection. Database records seeded by parent for local acceptance.
Opened every PNG after capture. 375 / 390 / 768 / 1280 CSS pixel viewports: document width matches viewport; window scrollY=0.
390 lower screenshot scrolls only settings content to verify remaining sections.
Selected QA Builder and Mobile release through real Select controls; requests recorded in interaction-proof.json (one aggregate request each).
Escape closes selector; visible listboxes=0 after close.
Refresh performs one additional /api/overview request.
Phone settings picker leaves Overview for profile and returns to Overview.
Error screenshot came from real server pre-rebuild failure, showing retry control.
Native mobile app has no overview screen; this new settings tab is shared Web/Desktop, phone browser verified.
