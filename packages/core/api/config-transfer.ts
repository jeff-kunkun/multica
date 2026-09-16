import { z } from "zod";
import { parseWithFallback } from "./schema";

export const CONFIG_BUNDLE_FORMAT = "multica.workspace-config";
export const CONFIG_BUNDLE_SCHEMA_VERSION = 1;

export const CONFIG_ON_CONFLICT = ["fail", "overwrite", "rename", "skip"] as const;
export type ConfigOnConflict = (typeof CONFIG_ON_CONFLICT)[number];

const EMPTY_SOURCE = {
  workspace_id: "",
  slug: "",
  name: "",
  issue_prefix: "",
  exported_by: "",
};

export const ConfigBundleSourceSchema = z
  .object({
    workspace_id: z.string().default(""),
    slug: z.string().default(""),
    name: z.string().default(""),
    issue_prefix: z.string().default(""),
    server_version: z.string().optional(),
    exported_by: z.string().default(""),
  })
  .loose();

export const SecretOmittedSchema = z
  .object({
    entity: z.string().default(""),
    source_id: z.string().optional().default(""),
    name: z.string().optional().default(""),
    field: z.string().default(""),
    reason: z.string().default(""),
    hint: z.record(z.string(), z.unknown()).optional(),
  })
  .loose();

export const ConfigBundleSchema = z
  .object({
    format: z.string(),
    schema_version: z.number(),
    bundle_id: z.string().default(""),
    exported_at: z.string().default(""),
    source: ConfigBundleSourceSchema.default(EMPTY_SOURCE),
    options: z.unknown().optional(),
    entities: z.unknown().default({}),
    integrations: z.unknown().default([]),
    plugins_to_reinstall: z.unknown().default([]),
    secrets_omitted: z.array(SecretOmittedSchema).default([]),
    stats: z.record(z.string(), z.number()).default({}),
  })
  .loose();

export const ConfigImportItemSchema = z
  .object({
    source_id: z.string().optional().default(""),
    name: z.string().default(""),
    action: z.string().default(""),
    target_id: z.string().optional().default(""),
    reason: z.string().optional().default(""),
  })
  .loose();

export const ConfigImportBatchSchema = z
  .object({
    entity_type: z.string().default(""),
    batch_status: z.string().default(""),
    items: z.array(ConfigImportItemSchema).default([]),
  })
  .loose();

export const UnmappedRefSchema = z
  .object({
    entity: z.string().default(""),
    source_id: z.string().default(""),
    field: z.string().default(""),
    ref_type: z.string().default(""),
    ref_id: z.string().default(""),
    resolution: z.string().default(""),
  })
  .loose();

export const SecretToFillSchema = z
  .object({
    entity: z.string().default(""),
    target_id: z.string().optional().default(""),
    name: z.string().optional().default(""),
    field: z.string().default(""),
    path: z.string().optional().default(""),
    webhook_url: z.string().optional().default(""),
  })
  .loose();

export const ConfigWarningSchema = z
  .object({
    code: z.string().default(""),
    count: z.number().optional().default(0),
  })
  .loose();

const EMPTY_IMPORT_STATS = {
  created: 0,
  updated: 0,
  renamed: 0,
  skipped: 0,
  failed: 0,
};

export const ConfigImportStatsSchema = z
  .object({
    created: z.number().default(0),
    updated: z.number().default(0),
    renamed: z.number().default(0),
    skipped: z.number().default(0),
    failed: z.number().default(0),
  })
  .loose();

export const ConfigImportReportSchema = z
  .object({
    applied: z.boolean(),
    bundle_id: z.string().default(""),
    on_conflict: z.string().default(""),
    batches: z.array(ConfigImportBatchSchema).default([]),
    unmapped_refs: z.array(UnmappedRefSchema).default([]),
    secrets_to_fill: z.array(SecretToFillSchema).default([]),
    warnings: z.array(ConfigWarningSchema).default([]),
    stats: ConfigImportStatsSchema.default(EMPTY_IMPORT_STATS),
  })
  .loose();

export type ConfigBundle = z.infer<typeof ConfigBundleSchema>;
export type SecretOmitted = z.infer<typeof SecretOmittedSchema>;
export type ConfigImportReport = z.infer<typeof ConfigImportReportSchema>;
export type ConfigImportBatch = z.infer<typeof ConfigImportBatchSchema>;
export type ConfigImportItem = z.infer<typeof ConfigImportItemSchema>;
export type UnmappedRef = z.infer<typeof UnmappedRefSchema>;
export type SecretToFill = z.infer<typeof SecretToFillSchema>;
export type ConfigWarning = z.infer<typeof ConfigWarningSchema>;
export type ConfigImportStats = z.infer<typeof ConfigImportStatsSchema>;

export type ConfigImportRequest = {
  bundle: unknown;
  dry_run: boolean;
  on_conflict?: ConfigOnConflict;
  include?: string[];
  options?: {
    include_archived?: boolean;
    activate_autopilots?: boolean;
    apply_workspace_settings?: boolean;
    apply_issue_prefix?: boolean;
  };
};

export type ConfigImportErrorInfo = {
  code: string;
  message: string;
  status: number;
};

/** One agent→runtime pair the Desktop card asks the server to bind. */
export type TransferRuntimeBinding = {
  agent_id: string;
  runtime_id: string;
};

export const TransferRuntimeBindResultSchema = z
  .object({
    agent_id: z.string().default(""),
    agent_name: z.string().default(""),
    runtime_id: z.string().default(""),
    runtime_name: z.string().default(""),
    ok: z.boolean().default(false),
    reason_code: z.string().default(""),
    reason: z.string().default(""),
  })
  .loose();

const EMPTY_TRANSFER_BIND_REPORT = {
  applied: false,
  bound: 0,
  failed: 0,
  results: [] as z.infer<typeof TransferRuntimeBindResultSchema>[],
};

export const TransferBindRuntimesReportSchema = z
  .object({
    applied: z.boolean().default(false),
    bound: z.number().default(0),
    failed: z.number().default(0),
    results: z.array(TransferRuntimeBindResultSchema).default([]),
  })
  .loose();

export type TransferRuntimeBindResult = z.infer<
  typeof TransferRuntimeBindResultSchema
>;
export type TransferBindRuntimesReport = z.infer<
  typeof TransferBindRuntimesReportSchema
>;

export const EMPTY_TRANSFER_BIND_RUNTIMES_REPORT: TransferBindRuntimesReport =
  EMPTY_TRANSFER_BIND_REPORT;

export function parseTransferBindRuntimesReport(
  raw: unknown,
  endpoint: string,
): TransferBindRuntimesReport {
  return parseWithFallback(
    raw,
    TransferBindRuntimesReportSchema,
    EMPTY_TRANSFER_BIND_REPORT,
    { endpoint },
  );
}

export type ConfigImportResult = {
  report: ConfigImportReport;
  error?: ConfigImportErrorInfo;
};

export const EMPTY_CONFIG_BUNDLE: ConfigBundle = {
  format: "",
  schema_version: 0,
  bundle_id: "",
  exported_at: "",
  source: { ...EMPTY_SOURCE },
  entities: {},
  integrations: [],
  plugins_to_reinstall: [],
  secrets_omitted: [],
  stats: {},
};

export const EMPTY_CONFIG_IMPORT_REPORT: ConfigImportReport = {
  applied: false,
  bundle_id: "",
  on_conflict: "",
  batches: [],
  unmapped_refs: [],
  secrets_to_fill: [],
  warnings: [],
  stats: { ...EMPTY_IMPORT_STATS },
};

export type LocalConfigBundleError =
  | "invalid_json"
  | "config_bundle_invalid"
  | "config_bundle_version_unsupported";

export function parseConfigBundle(raw: unknown, endpoint: string): ConfigBundle {
  return parseWithFallback(raw, ConfigBundleSchema, EMPTY_CONFIG_BUNDLE, {
    endpoint,
  });
}

export function parseConfigImportReport(
  raw: unknown,
  endpoint: string,
): ConfigImportReport {
  return parseWithFallback(
    raw,
    ConfigImportReportSchema,
    EMPTY_CONFIG_IMPORT_REPORT,
    { endpoint },
  );
}

/** Validate a file the user picked before sending it to dry_run. */
export function parseLocalConfigBundle(
  raw: unknown,
):
  | { ok: true; bundle: ConfigBundle }
  | { ok: false; code: LocalConfigBundleError } {
  const parsed = ConfigBundleSchema.safeParse(raw);
  if (!parsed.success) {
    return { ok: false, code: "config_bundle_invalid" };
  }
  if (parsed.data.format !== CONFIG_BUNDLE_FORMAT) {
    return { ok: false, code: "config_bundle_invalid" };
  }
  if (parsed.data.schema_version !== CONFIG_BUNDLE_SCHEMA_VERSION) {
    return { ok: false, code: "config_bundle_version_unsupported" };
  }
  return { ok: true, bundle: parsed.data };
}

export function reportFromImportError(err: unknown): ConfigImportReport | null {
  if (!err || typeof err !== "object" || !("body" in err)) return null;
  const body = (err as { body?: unknown }).body;
  if (!body || typeof body !== "object" || !("report" in body)) return null;
  const report = (body as { report?: unknown }).report;
  if (report === undefined || report === null) return null;
  return parseConfigImportReport(
    report,
    "POST /api/workspaces/:id/config/import#error",
  );
}

export function importErrorInfo(err: unknown): ConfigImportErrorInfo | null {
  if (!err || typeof err !== "object") return null;
  const status = "status" in err ? (err as { status?: unknown }).status : undefined;
  const message =
    "message" in err && typeof (err as { message?: unknown }).message === "string"
      ? (err as { message: string }).message
      : "";
  const body = "body" in err ? (err as { body?: unknown }).body : undefined;
  let code = "";
  if (body && typeof body === "object" && "code" in body) {
    const raw = (body as { code?: unknown }).code;
    if (typeof raw === "string") code = raw;
  }
  if (typeof status !== "number") return null;
  return { code, message, status };
}

export function configExportFilename(slug: string, now = new Date()): string {
  const safe = slug.trim() || "workspace";
  const stamp = now.toISOString().slice(0, 10).replaceAll("-", "");
  return `multica-config-${safe}-${stamp}.json`;
}
