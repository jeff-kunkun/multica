import { chromium } from "playwright";
const b = await chromium.launch({ channel: "chrome" }).catch(() => chromium.launch());
const p = await b.newPage({ viewport: { width: 380, height: 470 }, deviceScaleFactor: 2 });
await p.goto("file://" + process.cwd() + "/.preview/cli-column.html");
await p.waitForTimeout(1500);
await p.screenshot({ path: ".preview/cli-column.png" });
await b.close();
