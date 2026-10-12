import { z } from "zod";

import { parseWithFallback } from "../api/schema";

/**
 * The workspace's 分工规则 (DENE-1834 part 4): a plain-language text, the rows
 * read out of it, and the audit trail of confirmed saves. Rows reach the
 * server only through the preview → confirm pair, never by autosave.
 */
export interface DispatchRule {
  id: string;
  category: string;
  hint?: string;
  series?: string;
  max_tier?: string;
  /** The sentence of `text` this row came from. */
  source?: string;
  /** "default" | "model" | "manual"; cleared when a person edits the row. */
  origin?: string;
}

export interface DispatchRevision {
  version: number;
  at: string;
  by?: string;
  text_diff?: string;
  rules_diff?: string;
}

export interface DispatchPolicy {
  text: string;
  rules: DispatchRule[];
  version?: number;
  history?: DispatchRevision[];
}

export interface TierOption {
  key: string;
  label: string;
}

export interface RoutingDispatch {
  policy: DispatchPolicy;
  is_default: boolean;
  series: string[];
  tiers: TierOption[];
  /** False for an agent run (task token): it may read, never write. */
  editable: boolean;
}

export interface DispatchPreview {
  policy: DispatchPolicy;
  /** Rows the model wrote that said nothing usable, each with the reason. */
  dropped: string[];
}

const DispatchRuleSchema = z.object({
  id: z.string().default(""),
  category: z.string().default(""),
  hint: z.string().optional(),
  series: z.string().optional(),
  max_tier: z.string().optional(),
  source: z.string().optional(),
  origin: z.string().optional(),
});

const DispatchRevisionSchema = z.object({
  version: z.number().default(0),
  at: z.string().default(""),
  by: z.string().optional(),
  text_diff: z.string().optional(),
  rules_diff: z.string().optional(),
});

const DispatchPolicySchema = z.object({
  text: z.string().default(""),
  rules: z.array(DispatchRuleSchema).default([]),
  version: z.number().optional(),
  history: z.array(DispatchRevisionSchema).optional(),
});

const RoutingDispatchSchema = z.object({
  policy: DispatchPolicySchema.default({ text: "", rules: [] }),
  is_default: z.boolean().default(true),
  series: z.array(z.string()).default([]),
  tiers: z.array(z.object({ key: z.string(), label: z.string() })).default([]),
  editable: z.boolean().default(false),
});

const DispatchPreviewSchema = z.object({
  policy: DispatchPolicySchema.default({ text: "", rules: [] }),
  dropped: z.array(z.string()).default([]),
});

export const EMPTY_ROUTING_DISPATCH: RoutingDispatch = {
  policy: { text: "", rules: [] },
  is_default: true,
  series: [],
  tiers: [],
  editable: false,
};

export function parseRoutingDispatch(raw: unknown): RoutingDispatch {
  return parseWithFallback(raw, RoutingDispatchSchema, EMPTY_ROUTING_DISPATCH, {
    endpoint: "GET /api/workspaces/{id}/routing/dispatch",
  });
}

export function parseDispatchPreview(raw: unknown): DispatchPreview {
  return parseWithFallback(
    raw,
    DispatchPreviewSchema,
    { policy: { text: "", rules: [] }, dropped: [] },
    { endpoint: "POST /api/workspaces/{id}/routing/dispatch/preview" },
  );
}

/** What the confirm button sends: the reviewed rows, or a reset to default. */
export type RoutingDispatchUpdate =
  | { text: string; rules: DispatchRule[] }
  | { reset: true };
