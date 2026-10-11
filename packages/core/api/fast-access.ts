import { z } from "zod";
import { parseWithFallback } from "./schema";

export type FastAccessRequest = {
  scope?: { mode: "all_visible" } | { mode: "workspace_ids"; workspace_ids: string[] };
  since?: string;
  cursor?: string;
  include?: ("active_runs" | "chats" | "issues" | "run_changes")[];
  limit?: number;
  max_bytes?: number;
};
const itemSchema = z.object({
  event_id: z.string().optional(), revision: z.string().optional(), op: z.enum(["upsert", "delete"]).optional(),
  id: z.string(), workspace_id: z.string(), workspace_name: z.string().optional(), kind: z.string(),
  title: z.string().optional(), preview: z.string().optional(),
  changed_at: z.string(), last_active_at: z.string().nullish(),
  unread_count: z.number().optional(), phase: z.string().optional(),
  source_type: z.string().optional(), content_truncated: z.boolean(),
}).transform((item) => ({
  eventId: item.event_id, revision: item.revision, op: item.op,
  id: item.id, workspaceId: item.workspace_id, workspaceName: item.workspace_name, kind: item.kind,
  title: item.title, preview: item.preview, changedAt: item.changed_at,
  lastActiveAt: item.last_active_at, unreadCount: item.unread_count,
  phase: item.phase, sourceType: item.source_type, contentTruncated: item.content_truncated,
}));
export const FastAccessSchema = z.object({
  schema_version: z.literal("1"), mode: z.enum(["timestamp_snapshot", "durable_delta"]),
  checkpoint: z.string().nullish(), coverage_from: z.string().optional(), retention_floor: z.string().nullish(),
  as_of: z.string(), scope_revision: z.string(),
  active_runs: z.array(itemSchema), changes: z.array(itemSchema),
  active_runs_complete: z.boolean(), complete: z.boolean(),
  next_cursor: z.string().nullable(),
}).refine((page) => page.complete ? page.next_cursor === null : !!page.next_cursor)
  .transform((page) => ({
    mode: page.mode, checkpoint: page.checkpoint, coverageFrom: page.coverage_from, retentionFloor: page.retention_floor,
    asOf: page.as_of, scopeRevision: page.scope_revision,
    activeRuns: page.active_runs, changes: page.changes,
    activeRunsComplete: page.active_runs_complete,
    complete: page.complete, nextCursor: page.next_cursor,
  }));
export type FastAccessPage = z.output<typeof FastAccessSchema>;
export function parseFastAccess(raw: unknown): FastAccessPage | null {
  return parseWithFallback<FastAccessPage | null>(raw, FastAccessSchema, null, {
    endpoint: "/api/fast-access/v1/read",
  });
}
