import { test } from "@playwright/test";
import pg from "pg";
import "./env";
import { createTestApi } from "./helpers";

test.use({ channel: "chrome" });
const OUT = "docs/evidence/DENE-1681";

test("memory monitor screenshots", async ({ browser }) => {
  test.setTimeout(180_000);
  const api = await createTestApi();
  const [ws] = await api.getWorkspaces();
  const db = new pg.Client({ connectionString: process.env.DATABASE_URL });
  await db.connect();
  const one = async (sql: string, args: unknown[] = []) => (await db.query(sql, args)).rows[0];
  const user = await one(`SELECT id FROM "user" WHERE email = $1`, [api.getEmail()]);
  await db.query(`DELETE FROM knowledge_sediment WHERE workspace_id = $1`, [ws.id]);
  await db.query(`DELETE FROM issue WHERE workspace_id = $1 AND number > 1700`, [ws.id]);
  await db.query(`DELETE FROM chat_session WHERE workspace_id = $1`, [ws.id]);
  await db.query(`DELETE FROM project WHERE workspace_id = $1`, [ws.id]);
  await db.query(`DELETE FROM agent WHERE workspace_id = $1`, [ws.id]);
  await db.query(`DELETE FROM agent_runtime WHERE workspace_id = $1`, [ws.id]);
  await db.query(`UPDATE workspace SET issue_prefix = 'DENE' WHERE id = $1`, [ws.id]);
  const runtime = await one(`INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at, visibility, owner_id)
    VALUES ($1,'shots rt','cloud','shots','online','','{}',now(),'private',$2) RETURNING id`, [ws.id, user.id]);
  const agent = await one(`INSERT INTO agent (workspace_id, name, description, runtime_mode, runtime_config, visibility, permission_mode, max_concurrent_tasks, owner_id, runtime_id)
    VALUES ($1,'孙悟空','', 'cloud','{}','workspace','private',1,$2,$3) RETURNING id`, [ws.id, user.id, runtime.id]);
  const project = await one(`INSERT INTO project (workspace_id, title, status, priority) VALUES ($1,'Multica 魔改','in_progress','none') RETURNING id`, [ws.id]);
  let n = 1700;
  const issue = async (title: string, status: string, extra: Record<string, unknown> = {}, meta: Record<string, unknown> = {}, ago = "1 day") => {
    n += 1;
    const row = await one(`INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, position, number, project_id, origin_chat_session_id, metadata, updated_at, created_at)
      VALUES ($1,$2,$3,'none','member',$4,0,$5,$6,$7,$8, now() - $9::interval, now() - $9::interval) RETURNING id`,
      [ws.id, title, status, user.id, n, project.id, extra.chat ?? null, JSON.stringify(meta), ago]);
    return row.id as string;
  };
  const sediment = (cols: { issue?: string; chat?: string; changes: unknown; files?: unknown; layer?: string; ago: string; verified?: boolean }) =>
    db.query(`INSERT INTO knowledge_sediment (workspace_id, project_id, issue_id, chat_session_id, changes, verified, author_type, layer, memory_files, created_at)
      VALUES ($1,$2,$3,$4,$5,$6,'agent',$7,$8, now() - $9::interval)`,
      [ws.id, project.id, cols.issue ?? null, cols.chat ?? null, JSON.stringify(cols.changes), cols.verified ?? true, cols.layer ?? "worker", JSON.stringify(cols.files ?? []), cols.ago]);

  const chat = await one(`INSERT INTO chat_session (workspace_id, agent_id, creator_id, title, status) VALUES ($1,$2,$3,'回执贯通方案','active') RETURNING id`, [ws.id, agent.id, user.id]);
  const routeChat = await one(`INSERT INTO chat_session (workspace_id, agent_id, creator_id, title, status) VALUES ($1,$2,$3,'路由规则整理','active') RETURNING id`, [ws.id, agent.id, user.id]);

  for (const c of [chat, routeChat]) {
    await db.query(`UPDATE chat_session SET explicitly_created_at = now() WHERE id = $1`, [c.id]);
    await db.query(`INSERT INTO chat_message (chat_session_id, role, content) VALUES ($1,'user','把回执贯通拆成几张票')`, [c.id]);
  }
  const w1 = await issue("子任务完成提示不点名父任务读者看不到的子任务", "done", {}, {}, "2 hours");
  await sediment({ issue: w1, ago: "2 hours", changes: [{ location: "agents", action: "supersede", entry: "旧派单规则", summary: "s", files: ["AGENTS.md"] }], files: [{ path: "AGENTS.md", bytes: 9000, deleted: 4, supersede_marks: 1 }] });
  const w2 = await issue("老板层跨票沉淀与记忆卫生", "done", {}, {}, "1 day");
  await sediment({ issue: w2, ago: "1 day", layer: "boss", changes: [{ location: "context", action: "update", entry: "沉淀轮次", summary: "s", files: ["CONTEXT.md"] }], files: [{ path: "CONTEXT.md", bytes: 4000, deleted: 0 }] });
  await sediment({ chat: routeChat.id, ago: "3 days", changes: [{ location: "adr", summary: "s", files: ["docs/adr/0009-routing.md"] }] });

  const closeMeta = (status: string) => ({ "close.conclusion": "delivered", "close.status": status, "close.evidence_comment_id": "", "close.next_owner_type": "", "close.next_owner_id": "", "close.wake_action": "", "close.waiting_on": "", "close.at": new Date().toISOString() });
  await issue("修复看板拖拽偏移", "done", {}, { "close.knowledge_audit": JSON.stringify({ none: true }) }, "5 hours");
  await issue("CLI 输出加 --output json", "done", {}, {}, "2 days");
  await issue("沉淀第 3 轮", "done", {}, { sediment_project: project.id, sediment_gap: ["context"] }, "4 days");
  await issue("沉淀第 4 轮", "in_progress", {}, { sediment_project: project.id, sediment_gap: ["docs_index"] }, "1 hour");

  await issue("状态卡回执汇总", "done", { chat: chat.id }, closeMeta("done"), "6 hours");
  await issue("来源聊天显示子任务回执", "done", { chat: chat.id }, {}, "1 day");
  await issue("回执失败重试", "in_progress", { chat: chat.id }, {}, "3 hours");
  await issue("路由兜底标签", "in_review", { chat: routeChat.id }, closeMeta("in_review"), "2 days");
  await db.end();

  const url = `/${ws.slug}/projects/${project.id}`;
  for (const [name, width, height, mobile] of [["375", 375, 812, true], ["390", 390, 844, true], ["768", 768, 1024, false], ["1280", 1280, 900, false]] as const) {
    const ctx = await browser.newContext({ viewport: { width, height }, isMobile: mobile, hasTouch: mobile });
    await ctx.addCookies([{ name: "multica-locale", value: "zh-Hans", url: "http://localhost:13510" }]);
    await ctx.addInitScript((token) => localStorage.setItem("multica_token", token), api.getToken()!);
    const page = await ctx.newPage();
    await page.goto(url, { waitUntil: "domcontentloaded" });
    const card = page.locator("section[aria-labelledby='project-memory-heading']");
    await page.getByText("Multica 魔改").first().waitFor({ timeout: 60_000 });
    await page.waitForTimeout(1500);
    if (!(await card.isVisible())) await page.locator("button:has(svg.lucide-panel-right)").click();
    await card.getByText(/近 14 天|last 14 days/).first().waitFor({ timeout: 60_000 });
    await page.evaluate(() => window.scrollTo(0, 0));
    await page.screenshot({ path: `${OUT}/page-${name}.png` });
    await card.scrollIntoViewIfNeeded();
    await card.screenshot({ path: `${OUT}/card-${name}.png` });
    if (mobile) {
      await card.getByText("DENE-1710 路由兜底标签").scrollIntoViewIfNeeded();
      await page.screenshot({ path: `${OUT}/sheet-bottom-${name}.png` });
      await page.keyboard.press("Escape");
      await card.waitFor({ state: "hidden" });
      await page.screenshot({ path: `${OUT}/sheet-closed-${name}.png` });
    } else if (name === "1280") {
      await card.getByRole("link", { name: "DENE-1703 修复看板拖拽偏移" }).click();
      await page.waitForURL(/\/issues\//);
      await page.getByText("修复看板拖拽偏移").first().waitFor({ timeout: 60_000 });
      await page.screenshot({ path: `${OUT}/click-unsettled-1280.png` });
      await page.goBack();
      await card.getByRole("link", { name: "聊天 回执贯通方案" }).click();
      await page.waitForURL(/\/chat/);
      await page.getByText("回执贯通方案").first().waitFor({ timeout: 60_000 });
      await page.waitForTimeout(1000);
      await page.screenshot({ path: `${OUT}/click-chat-1280.png` });
    }
    await ctx.close();
  }
});
