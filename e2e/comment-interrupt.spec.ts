import "./env";

import { expect, test } from "@playwright/test";
import pg from "pg";
import { createTestApi } from "./helpers";

// DENE-2667: the interrupt tier only shows behind the office_interrupt_steer
// flag, so this runs against a web build started with
// NEXT_PUBLIC_MULTICA_FEATURE_FLAGS=office_interrupt_steer. A reply to a run
// that can be interrupted goes in as an interrupt by default, and its receipt
// says after how many seconds the agent read it.
const flagOn = (process.env.NEXT_PUBLIC_MULTICA_FEATURE_FLAGS ?? "").split(",").includes("office_interrupt_steer");

for (const width of [390, 768, 1280]) {
  test(`a reply interrupts the thread agent's running turn at ${width}px`, async ({ page }, testInfo) => {
    test.skip(!flagOn, "web was not started with the office_interrupt_steer flag");
    test.setTimeout(120_000);
    const api = await createTestApi();
    const db = new pg.Client(process.env.DATABASE_URL);
    await db.connect();
    let runtimeId: string | undefined;
    let agentId: string | undefined;
    try {
      const workspace = (await api.getWorkspaces())[0]!;
      const user = await db.query<{ id: string }>(`SELECT id FROM "user" WHERE email = $1`, [api.getEmail()]);
      const userId = user.rows[0]!.id;
      const runtime = await db.query<{ id: string }>(
        `INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, last_seen_at)
         VALUES ($1, 'Interrupt verification', 'cloud', 'codex', 'online', 'E2E fixture', '{}'::jsonb, $2, now()) RETURNING id`,
        [workspace.id, userId],
      );
      runtimeId = runtime.rows[0]!.id;
      const agent = await db.query<{ id: string }>(
        `INSERT INTO agent (workspace_id, name, description, instructions, runtime_mode, runtime_config, runtime_id, visibility, permission_mode, max_concurrent_tasks, owner_id)
         VALUES ($1, 'Lambda', '', '', 'cloud', '{}'::jsonb, $2, 'private', 'private', 1, $3) RETURNING id`,
        [workspace.id, runtimeId, userId],
      );
      agentId = agent.rows[0]!.id;
      const issue = await api.createIssue("Fix the Safari 17 login page");
      const root = await db.query<{ id: string }>(
        `INSERT INTO comment (workspace_id, issue_id, author_type, author_id, content)
         VALUES ($1, $2, 'member', $3, $4) RETURNING id`,
        [workspace.id, issue.id, userId, `[@Lambda](mention://agent/${agentId}) The login page is blank in Safari 17. Find the cause and fix it.`],
      );
      const rootId = root.rows[0]!.id;
      const turn = await db.query<{ id: string }>(
        `INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, trigger_comment_id, delivered_comment_ids, status, dispatched_at, started_at)
         VALUES ($1, $2, $3, $4, ARRAY[$4]::uuid[], 'running', now() - interval '2 minutes', now() - interval '2 minutes') RETURNING id`,
        [agentId, runtimeId, issue.id, rootId],
      );
      const turnId = turn.rows[0]!.id;
      await db.query(
        `INSERT INTO task_supplement_capability (task_id, workspace_id, issue_id, capability, interrupt_supported)
         VALUES ($1, $2, $3, 'task-supplement-v1', true)`,
        [turnId, workspace.id, issue.id],
      );

      await page.addInitScript((token) => {
        localStorage.setItem("multica_token", token!);
        localStorage.setItem("multica:chat:isOpen", "false");
      }, api.getToken());
      await page.setViewportSize({ width, height: 900 });
      await page.goto(`/${workspace.slug}/issues/${issue.id}`, { waitUntil: "domcontentloaded" });
      await expect(page.locator(`[data-run-id="${turnId}"]`)).toBeVisible({ timeout: 45_000 });

      const shell = page.getByTestId("reply-composer-shell").first();
      await expect(shell).toHaveText("Leave a reply...");
      const placeholder = "Leave a reply...";
      const editor = page.locator(`.ProseMirror[data-placeholder="${placeholder}"], .ProseMirror:has([data-placeholder="${placeholder}"])`).first();
      // A cold dev server can swallow the first click before the shell is live.
      await expect(async () => {
        if (!(await editor.isVisible())) await shell.click();
        await expect(editor).toBeVisible({ timeout: 2_000 });
      }).toPass({ timeout: 20_000 });
      await editor.fill("Stop waiting on that command; write hello.txt instead.");
      const chip = page.getByRole("button", { name: "Lambda trigger: Interrupt" });
      await expect(chip).toBeVisible({ timeout: 15_000 });
      await chip.click();
      await expect(page.getByRole("menuitemradio", { name: /Stops the current step now/ })).toBeVisible();
      await page.waitForTimeout(400); // let the menu finish fading in for the screenshot
      await page.screenshot({ path: testInfo.outputPath(`interrupt-menu-${width}.png`) });
      await page.keyboard.press("Escape");

      const posted = page.waitForResponse((response) => response.request().method() === "POST" && response.url().endsWith(`/api/issues/${issue.id}/comments`));
      await page.getByRole("button", { name: "Send", exact: true }).first().click();
      const response = await posted;
      expect(response.status()).toBe(201);
      const reply = await response.json();
      expect(reply.supplements).toEqual([
        expect.objectContaining({ task_id: turnId, agent_id: agentId, status: "pending", delivery_mode: "interrupt" }),
      ]);
      const runs = await db.query(`SELECT id FROM agent_task_queue WHERE issue_id = $1`, [issue.id]);
      expect(runs.rows).toHaveLength(1);
      await expect(page.getByText("Interrupted Lambda's run")).toBeVisible();

      await db.query(
        `UPDATE task_supplement SET status = 'delivered', delivered_at = created_at + interval '3 seconds' WHERE comment_id = $1`,
        [reply.id],
      );
      await page.reload({ waitUntil: "domcontentloaded" });
      await expect(page.getByText("Delivered · read by Lambda after 3s")).toBeVisible({ timeout: 30_000 });
      await page.screenshot({ path: testInfo.outputPath(`interrupt-delivered-${width}.png`) });
    } finally {
      await api.cleanup();
      if (agentId) await db.query(`DELETE FROM agent_task_queue WHERE agent_id = $1`, [agentId]);
      if (agentId) await db.query(`DELETE FROM agent WHERE id = $1`, [agentId]);
      if (runtimeId) await db.query(`DELETE FROM agent_runtime WHERE id = $1`, [runtimeId]);
      await db.end();
    }
  });
}
