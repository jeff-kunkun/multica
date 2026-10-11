import { z } from "zod";
import { LinkedProjectSettingsSchema } from "./schemas";
import { runtimeDisplayLabel } from "../runtimes/display";
import { parseWithFallback } from "./schema";

const recordSchema = z.object({
  id: z.string().default(""),
  name: z.string().nullish(),
  title: z.string().nullish(),
  description: z.string().nullish(),
  model: z.string().nullish(),
  status: z.string().nullish(),
  runtime_id: z.string().nullish(),
  runtime_availability: z.string().nullish(),
  routing_tier: z.string().nullish(),
  specialization: z.string().nullish(),
  domain_name: z.string().nullish(),
  work_enabled: z.boolean().nullish(),
  custom_name: z.string().nullish(),
  provider: z.string().nullish(),
  device_info: z.string().nullish(),
  lead_name: z.string().nullish(),
  assignee_name: z.string().nullish(),
  lead_id: z.string().nullish(),
  assignee_id: z.string().nullish(),
  next_run_at: z.string().nullish(),
  source: z.object({ name: z.string() }).nullish(),
  target: z.object({ name: z.string() }).nullish(),
  tickets: z.array(z.object({ id: z.string(), title: z.string(), status: z.string().nullish() })).nullish(),
  resources: z.array(z.object({ resource_ref: z.object({
    url: z.string().nullish(), repo_key: z.string().nullish(), local_path: z.string().nullish(), real_path: z.string().nullish(),
  }) })).nullish(),
  schedules: z.array(z.object({ kind: z.string(), enabled: z.boolean(), cron_expression: z.string().nullish(), timezone: z.string().nullish(), next_run_at: z.string().nullish() })).nullish(),
  project_ids: z.array(z.string()).nullish(),
  linked_projects: LinkedProjectSettingsSchema.shape.linked_projects.optional(),
  effective_linked_projects: LinkedProjectSettingsSchema.shape.effective_linked_projects.optional(),
  inherited: z.boolean().optional(),
}).passthrough().transform((row) => ({
  id: row.id,
  name: row.source && row.target ? `${row.source.name} → ${row.target.name}` : row.provider != null ? runtimeDisplayLabel({ name: row.name ?? "", custom_name: row.custom_name ?? null, provider: row.provider }) : row.name ?? row.title ?? row.id,
  deviceInfo: row.device_info,
  ownerName: row.lead_name ?? row.assignee_name,
  leadId: row.lead_id,
  assigneeId: row.assignee_id,
  nextRunAt: row.next_run_at,
  tickets: row.tickets ?? [],
  resources: (row.resources ?? []).map(({ resource_ref: ref }) => ref.url ?? ref.repo_key ?? ref.local_path ?? ref.real_path ?? "").filter(Boolean),
  schedules: (row.schedules ?? []).map((schedule) => ({ kind: schedule.kind, enabled: schedule.enabled, cron: schedule.cron_expression, timezone: schedule.timezone })),
  description: row.description,
  model: row.model,
  status: row.status,
  runtimeId: row.runtime_id,
  availability: row.runtime_availability,
  tier: row.routing_tier,
  specialization: row.domain_name ?? row.specialization,
  workEnabled: row.work_enabled,
  projectIds: row.project_ids ?? [],
  linkedProjects: row.linked_projects,
  effectiveLinkedProjects: row.effective_linked_projects,
  referencesInherited: row.inherited,
}));

export type OverviewRecord = z.output<typeof recordSchema>;
const rows = () => z.array(recordSchema).default([]);
export const OverviewSchema = z.object({
  workspace_id: z.string(),
  agents: rows(), projects: rows(), chats: rows(), automations: rows(),
  skills: rows(), squads: rows(), runtimes: rows(), workspace_links: rows(),
  external_connections: rows(), omitted_sections: z.array(z.string()).default([]),
}).transform((value) => ({
  workspaceId: value.workspace_id,
  agents: value.agents, projects: value.projects, chats: value.chats,
  automations: value.automations, skills: value.skills, squads: value.squads,
  runtimes: value.runtimes, workspaceLinks: value.workspace_links,
  externalConnections: value.external_connections, omittedSections: value.omitted_sections,
}));
export type WorkspaceOverview = z.output<typeof OverviewSchema>;
export interface OverviewFilters { project?: string; agent?: string; compact?: boolean }
export function parseOverview(raw: unknown): WorkspaceOverview | null {
  return parseWithFallback<WorkspaceOverview | null>(raw, OverviewSchema, null, { endpoint: "/api/overview" });
}
