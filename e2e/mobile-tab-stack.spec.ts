import { test, expect, type Page } from "@playwright/test";
import { createTestApi } from "./helpers";
import type { TestApiClient } from "./fixtures";

// 手机网页：聊天是返回栈的底，底部导航不压栈。
test.use({
  channel: "chrome",
  viewport: { width: 390, height: 844 },
  isMobile: true,
  hasTouch: true,
});

const SHOTS = process.env.E2E_SHOTS_DIR;
let api: TestApiClient;
let slug: string;
let issueId: string;

test.beforeAll(async () => {
  api = await createTestApi();
  slug = (await api.getWorkspaces())[0]!.slug;
  issueId = (await api.createIssue("Back stack probe")).id;
});

test.afterAll(async () => {
  await api.cleanup();
});

async function open(page: Page, path: string) {
  await page.addInitScript((token) => localStorage.setItem("multica_token", token), api.getToken()!);
  await page.goto(path);
  await page.addStyleTag({ content: "nextjs-portal{display:none!important}" });
  await expect(page.getByTestId("mobile-tab-bar")).toBeVisible({ timeout: 30000 });
}

const tab = (page: Page, name: RegExp) => page.getByTestId("mobile-tab-bar").getByRole("link", { name });
const here = (page: Page) => new URL(page.url()).pathname;
async function shot(page: Page, name: string) {
  if (SHOTS) await page.screenshot({ path: `${SHOTS}/${name}.png` });
}

test("聊天 → 收件箱 → 任务详情 → 返回 → 返回 → 回到聊天", async ({ page }) => {
  await open(page, `/${slug}/chat`);
  await tab(page, /inbox|收件箱/i).click();
  await expect.poll(() => here(page)).toBe(`/${slug}/inbox`);
  // 收件箱里没有现成的任务，换到任务页（替换收件箱）再点进任务。
  await tab(page, /issues|任务/i).click();
  await expect.poll(() => here(page)).toBe(`/${slug}/issues`);
  await page.getByText("Back stack probe").first().click();
  await expect.poll(() => here(page)).toMatch(new RegExp(`^/${slug}/issues/.+`));
  await shot(page, "1-issue-detail");
  await page.goBack();
  await expect.poll(() => here(page)).toBe(`/${slug}/issues`);
  await page.goBack();
  await expect.poll(() => here(page)).toBe(`/${slug}/chat`);
  await shot(page, "1-back-to-chat");
});

test("聊天 → 收件箱 → 点底部聊天 → 返回，不回收件箱", async ({ page }) => {
  await open(page, `/${slug}/chat`);
  await tab(page, /inbox|收件箱/i).click();
  await expect.poll(() => here(page)).toBe(`/${slug}/inbox`);
  await tab(page, /issues|任务/i).click();
  await expect.poll(() => here(page)).toBe(`/${slug}/issues`);
  await tab(page, /inbox|收件箱/i).click();
  await expect.poll(() => here(page)).toBe(`/${slug}/inbox`);
  // issues 和 inbox 互换是替换，栈里只有 聊天 + 收件箱。
  expect(await page.evaluate(() => (window as any).navigation.entries().length)).toBe(2);
  await tab(page, /chat|聊天/i).click();
  await expect.poll(() => here(page)).toBe(`/${slug}/chat`);
  await shot(page, "2-tapped-chat");
  // 栈底就是聊天：身后没有收件箱，也没有别的站内页。
  expect(await page.evaluate(() => (window as any).navigation.canGoBack)).toBe(false);
});

test("冷打开任务链接 → 返回 → 聊天", async ({ page }) => {
  await open(page, `/${slug}/issues/${issueId}`);
  await shot(page, "3-cold-issue");
  await expect.poll(() => page.evaluate(() => (window as any).navigation.entries().length)).toBe(2);
  await page.goBack();
  await expect.poll(() => here(page), { timeout: 30000 }).toBe(`/${slug}/chat`);
  await expect(page.getByTestId("mobile-tab-bar")).toBeVisible({ timeout: 30000 });
  await shot(page, "3-back-to-chat");
});
