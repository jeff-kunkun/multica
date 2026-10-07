import "./env";

import { test } from "@playwright/test";
import pg from "pg";
import { createTestApi } from "./helpers";

// Throwaway screenshot script for DENE-1573 (not committed).
test.use({ locale: "zh-CN", channel: "chrome" });

const OUT = ".work/shots";
const WIDTHS = [375, 768, 1280];

test("DENE-1573 screenshots", async ({ browser }) => {
  test.setTimeout(300_000);
  const api = await createTestApi();
  const db = new pg.Client(process.env.DATABASE_URL);
  await db.connect();
  const workspace = (await api.getWorkspaces())[0]!;
  const userId = (await db.query<{ id: string }>(`SELECT id FROM "user" WHERE email = $1`, [api.getEmail()])).rows[0]!.id;
  const runtimeId = (
    await db.query<{ id: string }>(
      `INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, last_seen_at)
       VALUES ($1, 'DENE-1573 shots', 'cloud', 'e2e_dene1573', 'online', 'E2E fixture', '{}'::jsonb, $2, now()) RETURNING id`,
      [workspace.id, userId],
    )
  ).rows[0]!.id;
  const agentId = (
    await db.query<{ id: string }>(
      `INSERT INTO agent (workspace_id, name, description, instructions, runtime_mode, runtime_config, runtime_id, visibility, permission_mode, max_concurrent_tasks, owner_id)
       VALUES ($1, '布尔玛', '', '', 'cloud', '{}'::jsonb, $2, 'workspace', 'private', 1, $3) RETURNING id`,
      [workspace.id, runtimeId, userId],
    )
  ).rows[0]!.id;
  const skills: [string, string][] = [
    ["codebase-design", "动手前先结合源码和业务做设计"],
    ["code-review", "审查当前分支的改动"],
    ["jev", "有限解判断"],
  ];
  for (const [name, description] of skills) {
    const sid = (
      await db.query<{ id: string }>(
        `INSERT INTO skill (workspace_id, name, description, content, created_by) VALUES ($1, $2, $3, '# x', $4)
         ON CONFLICT (workspace_id, name) DO UPDATE SET description = EXCLUDED.description RETURNING id`,
        [workspace.id, name, description, userId],
      )
    ).rows[0]!.id;
    await db.query(`INSERT INTO agent_skill (agent_id, skill_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, [agentId, sid]);
  }

  // Issue A: assigned to the agent, one finished run that used two skills.
  const issueA = await api.createIssue("DENE-1573 示例：给设置页加一行开关");
  await db.query(`UPDATE issue SET assignee_type = 'agent', assignee_id = $1 WHERE id = $2`, [agentId, issueA.id]);
  const comment = (
    await db.query<{ id: string }>(
      `INSERT INTO comment (workspace_id, issue_id, author_type, author_id, content)
       VALUES ($1, $2, 'member', $3, $4) RETURNING id`,
      [workspace.id, issueA.id, userId, `[@布尔玛](mention://agent/${agentId}) 先做个设计再动手`],
    )
  ).rows[0]!.id;
  const taskId = (
    await db.query<{ id: string }>(
      `INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, trigger_comment_id, delivered_comment_ids, status, started_at, completed_at)
       VALUES ($1, $2, $3, $4, ARRAY[$4]::uuid[], 'completed', now() - interval '3 minute', now()) RETURNING id`,
      [agentId, runtimeId, issueA.id, comment],
    )
  ).rows[0]!.id;
  const msgs: [string, string | null, string | null][] = [
    ["text", null, "先看一下现有设置页结构。"],
    ["skill", "codebase-design", null],
    ["tool_use", "Read", null],
    ["skill", "code-review", null],
    ["text", null, "改好了，测试通过。"],
  ];
  let seq = 1;
  for (const [type, tool, content] of msgs) {
    await db.query(
      `INSERT INTO task_message (task_id, seq, type, tool, content, input) VALUES ($1, $2, $3, $4, $5, $6)`,
      [taskId, seq++, type, tool, content, type === "tool_use" ? JSON.stringify({ file_path: "settings.tsx" }) : null],
    );
  }
  await db.query(
    `INSERT INTO comment (workspace_id, issue_id, author_type, author_id, content, parent_id, source_task_id)
     VALUES ($1, $2, 'agent', $3, '设置页已加上开关，截图见附件。', $4, $5)`,
    [workspace.id, issueA.id, agentId, comment, taskId],
  );

  // Issue B: no agent anywhere — `/` shows the hint.
  const issueB = await api.createIssue("DENE-1573 示例：没有智能体的任务");

  const token = api.getToken()!;
  for (const width of WIDTHS) {
    const mobile = width < 640;
    const ctx = await browser.newContext({
      viewport: { width, height: mobile ? 812 : 900 },
      isMobile: mobile,
      hasTouch: mobile,
      locale: "zh-CN",
    });
    await ctx.addInitScript((t) => {
      localStorage.setItem("multica_token", t);
      localStorage.setItem("multica:chat:isOpen", "false");
    }, token);
    const page = await ctx.newPage();

    // 1) run line
    await page.goto(`/${workspace.slug}/issues/${issueA.id}`, { waitUntil: "domcontentloaded" });
    const line = page.locator("[data-run-skills]").first();
    await line.waitFor({ timeout: 30_000 });
    await line.scrollIntoViewIfNeeded();
    await page.waitForTimeout(500);
    await page.screenshot({ path: `${OUT}/${width}-run-skills.png` });

    // 2) full log shows the skill step
    await page.getByRole("button", { name: "打开完整日志" }).first().click();
    await page.getByText("codebase-design").last().waitFor({ timeout: 15_000 });
    await page.waitForTimeout(600);
    await page.screenshot({ path: `${OUT}/${width}-run-steps.png` });
    await page.keyboard.press("Escape");
    await page.waitForTimeout(400);

    // 3) `/` menu with the agent's skills (assignee fallback)
    const editor = page.locator(".ProseMirror[contenteditable=true]").last();
    await editor.scrollIntoViewIfNeeded();
    await editor.click();
    await page.keyboard.type("/");
    await page.waitForTimeout(800);
    await page.screenshot({ path: `${OUT}/${width}-slash-skills.png` });
    await page.keyboard.press("ArrowDown");
    // pick codebase-design
    await page.keyboard.type("codebase");
    await page.waitForTimeout(500);
    await page.keyboard.press("Enter");
    await page.waitForTimeout(400);
    await page.screenshot({ path: `${OUT}/${width}-slash-picked.png` });

    // 4) `/` menu with no agent → hint
    await page.goto(`/${workspace.slug}/issues/${issueB.id}`, { waitUntil: "domcontentloaded" });
    const editorB = page.locator(".ProseMirror[contenteditable=true]").last();
    await editorB.waitFor({ timeout: 30_000 });
    await editorB.scrollIntoViewIfNeeded();
    await editorB.click();
    await page.keyboard.type("/");
    await page.waitForTimeout(800);
    await page.screenshot({ path: `${OUT}/${width}-slash-hint.png` });
    if (mobile) {
      await page.keyboard.press("Escape");
      await page.waitForTimeout(300);
      await page.screenshot({ path: `${OUT}/${width}-slash-closed.png` });
    }
    await ctx.close();
  }
  await db.end();
});
