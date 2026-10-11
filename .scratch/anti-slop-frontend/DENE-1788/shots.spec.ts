import { test, expect, type Page } from "@playwright/test";
import pg from "pg";
import { createTestApi } from "./helpers";
import { TestApiClient } from "./fixtures";

test.use({ channel: "chrome" });
const OUT = ".scratch/anti-slop-frontend/DENE-1788/evidence";
const API = process.env.NEXT_PUBLIC_API_URL!;

async function open(page: Page, token: string, slug: string, w: number) {
  await page.setViewportSize({ width: w, height: w < 500 ? 844 : 800 });
  await page.context().addCookies([{ name: "multica-locale", value: "zh-Hans", url: process.env.FRONTEND_ORIGIN! }]);
  await page.addInitScript((t) => {
    localStorage.setItem("multica_token", t);
    localStorage.setItem("multica:chat:isOpen", "false");
  }, token);
  await page.goto(`/${slug}/settings?tab=bots`, { waitUntil: "domcontentloaded" });
  await page.addStyleTag({ content: "nextjs-portal{display:none!important}" });
}
const shot = (page: Page, name: string) =>
  page.screenshot({ path: `${OUT}/${name}.png` });

for (const w of [1280, 390]) {
  test(`admin flow ${w}`, async ({ page }) => {
    const api = await createTestApi();
    const token = api.getToken()!;
    const ws = (await api.getWorkspaces())[0];
    const db = new pg.Client(process.env.DATABASE_URL);
    await db.connect();
    await db.query(`DELETE FROM personal_access_token WHERE kind='bot' AND workspace_id=$1`, [ws.id]);
    await db.query(`INSERT INTO workspace_domain (workspace_id,name,position) VALUES ($1,'自媒体',0) ON CONFLICT DO NOTHING`, [ws.id]).catch(() => {});
    await open(page, token, ws.slug, w);
    await expect(page.getByRole("heading", { name: "外部 Bot" }).first()).toBeVisible({ timeout: 30000 });
    await page.waitForTimeout(800);
    await shot(page, `${w}-01-empty`);
    await page.getByRole("button", { name: "添加 Bot" }).first().click();
    await page.getByLabel("名字").fill("Multica 管家");
    await page.waitForTimeout(300);
    await shot(page, `${w}-02-add-dialog`);
    await page.getByRole("button", { name: /生成钥匙|创建钥匙/ }).click();
    await expect(page.getByTestId("bot-setup-text")).toBeVisible();
    await page.waitForTimeout(300);
    await shot(page, `${w}-03-key-dialog`);
    const setup = await page.getByTestId("bot-setup-text").innerText();
    const key = setup.match(/mul_[A-Za-z0-9]+/)![0];
    await page.getByRole("button", { name: "完成" }).click();
    // the bot uses its key once, so last-used shows up
    const r = await fetch(`${API}/api/workspaces/${ws.id}`, { headers: { Authorization: `Bearer ${key}` } });
    expect(r.status).toBe(200);
    // a second bot with a 1-year key
    await page.getByRole("button", { name: "添加 Bot" }).first().click();
    await page.getByLabel("名字").fill("个人事务");
    await page.getByRole("combobox").last().click();
    await page.getByRole("option", { name: "1 年" }).click();
    await page.getByRole("button", { name: /生成钥匙|创建钥匙/ }).click();
    await page.getByRole("button", { name: "完成" }).click();
    await page.reload({ waitUntil: "domcontentloaded" });
    await page.addStyleTag({ content: "nextjs-portal{display:none!important}" });
    await expect(page.getByText("Multica 管家")).toBeVisible({ timeout: 30000 });
    await page.waitForTimeout(500);
    await shot(page, `${w}-04-list`);
    if (w > 500) {
      await page.getByText("连接与扩展").first().evaluate((el) => el.scrollIntoView({ block: "start" }));
      await shot(page, `${w}-09-admin-nav-entry`);
    }
    await page.getByRole("button", { name: "Multica 管家 的操作" }).click();
    await page.waitForTimeout(300);
    await shot(page, `${w}-05-menu`);
    await page.getByRole("menuitem", { name: "停用" }).click();
    await page.waitForTimeout(300);
    await shot(page, `${w}-06-stop-confirm`);
    await page.getByRole("alertdialog").getByRole("button", { name: "停用" }).click();
    await page.waitForTimeout(800);
    const r2 = await fetch(`${API}/api/workspaces/${ws.id}`, { headers: { Authorization: `Bearer ${key}` } });
    expect(r2.status).toBe(401);
    await shot(page, `${w}-07-stopped`);
    await db.end();
  });
}

test("member cannot see bots", async ({ page }) => {
  const admin = await createTestApi();
  const ws = (await admin.getWorkspaces())[0];
  const m = new TestApiClient();
  const email = `dene1788-member-${Date.now()}@multica.ai`;
  await m.login(email, "Member User");
  await admin.addMemberByEmail(email, "member");
  const db = new pg.Client(process.env.DATABASE_URL);
  await db.connect();
  await db.query(`UPDATE "user" SET onboarded_at=now(), onboarding_questionnaire='{"source":["friends_colleagues"],"source_other":null,"source_skipped":false}'::jsonb WHERE email=$1`, [email]);
  await db.end();
  for (const w of [1280, 390]) {
    await open(page, m.getToken()!, ws.slug, w);
    await page.goto(`/${ws.slug}/settings?tab=api-tokens`, { waitUntil: "domcontentloaded" });
    await page.addStyleTag({ content: "nextjs-portal{display:none!important}" });
    await page.waitForTimeout(3000);
    expect(await page.getByText("外部 Bot").count()).toBe(0);
    if (w > 500) await page.getByText("连接与扩展").first().evaluate((el) => el.scrollIntoView({ block: "start" }));
    await shot(page, `${w}-08-member-no-entry`);
  }
});
