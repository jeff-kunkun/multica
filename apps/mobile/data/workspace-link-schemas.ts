import { z } from "zod";

const workspace = z.object({ name: z.string(), slug: z.string(), avatar_url: z.string().nullable().default(null) });
const project = z.object({ id: z.string(), title: z.string(), icon: z.string().nullable().default(null) });
export const WorkspaceLinkSchema = z.object({
  id: z.string(), side: z.enum(["source", "viewer"]), status: z.enum(["pending", "active"]),
  source: workspace, target: workspace, projects: z.array(project).default([]),
  managed: z.boolean().catch(false), can_set_managed: z.boolean().catch(false), can_update_projects: z.boolean().catch(false),
  created_at: z.string().default(""), accepted_at: z.string().nullable().default(null),
});
export const WorkspaceLinksSchema = z.object({
  links: z.array(WorkspaceLinkSchema).default([]),
  can: z.object({ create: z.boolean().catch(false), accept: z.boolean().catch(false), pull: z.boolean().catch(false), manage: z.boolean().catch(false), audit: z.boolean().catch(false) }).default({ create: false, accept: false, pull: false, manage: false, audit: false }),
});
export const WorkspaceLinkLookupSchema = z.object({
  workspace, pull: z.object({ allowed: z.boolean(), projects: z.array(project).default([]) }).optional(),
});
export const LinkedViewSchema = z.object({
  link_id: z.string(), source: workspace.omit({ slug: true }),
  projects: z.array(project.extend({ status: z.string(), done: z.number(), total: z.number(), description: z.string().optional(), memory_line: z.string().optional(), resources: z.array(z.object({ type: z.string(), label: z.string().nullable(), url: z.string().optional(), path: z.string().optional() })).optional() })).default([]),
  issues: z.array(z.object({ identifier: z.string(), title: z.string(), status: z.string(), priority: z.string(), project_id: z.string(), assignee_name: z.string().nullable(), assignee_avatar_url: z.string().nullable(), due_date: z.string().nullable(), updated_at: z.string() })).default([]),
  statuses: z.array(z.object({ key: z.string(), name: z.string(), category: z.string() })).default([]),
  next_cursor: z.string().nullable().default(null),
});
export const ChatLinkedProjectSchema = z.object({
  link_id: z.string(), project_id: z.string(), title: z.string().default(""), icon: z.string().nullable().default(null), source_name: z.string().default(""), available: z.boolean().catch(false),
});
