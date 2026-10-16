import "./env";
import { expect, test } from "@playwright/test";
import pg from "pg";
import { createTestApi } from "./helpers";

for (const locale of ["en", "zh-Hans"]) {
  test(`two top-level mentions share one queued run and show an offline office (${locale})`, async ({ page }, testInfo) => {
    test.setTimeout(120_000);
    const api = await createTestApi();
    const db = new pg.Client(process.env.DATABASE_URL);
    await db.connect();
    let runtimeId: string | undefined;
    let agentId: string | undefined;
    try {
      const workspace = (await api.getWorkspaces())[0]!;
      const user = await db.query(
        `UPDATE "user" SET language = $2 WHERE email = $1 RETURNING id`, [api.getEmail(), locale],
      );
      await page.context().addCookies([{
        name: "multica-locale", value: locale, url: testInfo.project.use.baseURL!,
      }]);
      const runtime = await db.query(
        `INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status, device_info, metadata, owner_id)
         VALUES ($1, 'Office verification', 'cloud', 'codex', 'offline', 'E2E fixture', '{}'::jsonb, $2) RETURNING id`,
        [workspace.id, user.rows[0].id],
      );
      runtimeId = runtime.rows[0].id;
      const agent = await db.query(
        `INSERT INTO agent (workspace_id, name, runtime_id, runtime_mode, visibility, permission_mode, owner_id)
         VALUES ($1, 'Office Agent', $2, 'cloud', 'private', 'private', $3) RETURNING id`,
        [workspace.id, runtimeId, user.rows[0].id],
      );
      agentId = agent.rows[0].id;
      const issue = await api.createIssue("One issue, one agent, one run");
      await api.createComment(issue.id, `[@Office Agent](mention://agent/${agentId}) Check the login page.`);
      await api.createComment(issue.id, `[@Office Agent](mention://agent/${agentId}) Also verify the mobile layout.`);
      await page.addInitScript((token) => {
        localStorage.setItem("multica_token", token!);
        localStorage.setItem("multica:chat:isOpen", "false");
      }, api.getToken());
      await page.setViewportSize({ width: 1280, height: 960 });
      await page.goto(`/${workspace.slug}/issues/${issue.id}`, { waitUntil: "domcontentloaded" });
      await expect(page.getByText("Also verify the mobile layout.", { exact: false }).first()).toBeVisible({ timeout: 45_000 });
      await expect(page.locator("[data-run-id]").first()).toBeVisible();
      await page.screenshot({ path: testInfo.outputPath("queued-runs.png"), fullPage: true });
      const runs = await db.query(`SELECT trigger_comment_id, coalesced_comment_ids FROM agent_task_queue WHERE issue_id = $1`, [issue.id]);
      expect(runs.rows).toHaveLength(1);
      expect(runs.rows[0].coalesced_comment_ids).toHaveLength(1);
      const offlineHint = locale === "en"
        ? "Office offline · open the Office app on the agent's device to reconnect."
        : "办公室离线 · 请打开员工所在设备的 Office App 恢复连接。";
      await expect(page.getByText(offlineHint, { exact: true })).toBeVisible();
      await page.screenshot({ path: testInfo.outputPath("office-offline.png"), fullPage: true });
      await page.setViewportSize({ width: 390, height: 844 });
      await page.screenshot({ path: testInfo.outputPath("office-offline-mobile.png"), fullPage: true });
      await db.query(`UPDATE agent_runtime SET status = 'online', last_seen_at = now() WHERE id = $1`, [runtimeId]);
      await expect(page.getByText(locale === "en" ? "Waiting for an available agent." : "等待可用的智能体。", { exact: true })).toBeVisible({ timeout: 10_000 });
      await expect(page.getByText(offlineHint, { exact: true })).toHaveCount(0);
      await page.screenshot({ path: testInfo.outputPath("office-reconnected.png"), fullPage: true });
    } finally {
      await api.cleanup();
      if (agentId) await db.query(`DELETE FROM agent_task_queue WHERE agent_id = $1`, [agentId]);
      if (agentId) await db.query(`DELETE FROM agent WHERE id = $1`, [agentId]);
      if (runtimeId) await db.query(`DELETE FROM agent_runtime WHERE id = $1`, [runtimeId]);
      await db.end();
    }
  });
}
