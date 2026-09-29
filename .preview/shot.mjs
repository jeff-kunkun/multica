import { chromium } from "playwright";
const b = await chromium.launch(); const p = await b.newPage({ viewport: { width: 960, height: 900 } });
await p.goto("file://" + process.cwd() + "/.preview/dene-998-preview.html");
await p.screenshot({ path: ".preview/dene-998-preview.png", fullPage: true }); await b.close();
