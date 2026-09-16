export type TransferErrorCode =
  | "target_unsupported"
  | "cli_too_old"
  | "transfer_bundle_corrupt"
  | "cli_not_found"
  | "cancelled"
  | "busy"
  | "unknown";

export type TransferProgressEvent = {
  phase: "estimating" | "running" | "finalizing";
  sessionsTotal?: number;
  sessionsDone?: number;
  currentSessionTitle?: string;
  attachmentsDownloaded?: number;
  attachmentsTotal?: number;
};

export type TransferSecretToFill = {
  entity: string;
  name: string;
  field: string;
  target_id: string;
};

export type TransferRuntimeCandidate = {
  runtime_id: string;
  /** Machine label the picker shows, e.g. "Claude (MacBook-Pro.local)". */
  name: string;
  provider: string;
  runtime_mode: string;
  profile_name: string;
};

/**
 * Outcome of the three-tier bind rule. "" is what a server that predates
 * DENE-364 sends (or omits): the card then falls back to showing the plain
 * candidate list.
 */
export type TransferRuntimeBindAction =
  | "bound"
  | "already_bound"
  | "candidates"
  | "no_candidate"
  | "agent_missing"
  | "failed"
  | "";

export type TransferRuntimeBind = {
  agent_target_id: string;
  agent_name: string;
  provider: string;
  runtime_mode: string;
  profile_name: string;
  candidate_ids: string[];
  candidates: TransferRuntimeCandidate[];
  action: TransferRuntimeBindAction;
  /** True when the unique-candidate rule picked the runtime with no human input. */
  auto_bind: boolean;
  bound_runtime_id: string;
  bound_runtime_name: string;
  /** Stable reason token; `reason` is the server's English fallback. */
  reason_code: string;
  reason: string;
};

export type TransferExportGap = {
  group: string;
  reason: string;
  status?: number;
};

export type TransferImportStats = {
  created: number;
  updated: number;
  renamed: number;
  skipped: number;
  failed: number;
};

/**
 * What the import does with the switches the V1 transfer path never forwarded
 * (DENE-363). Absent options keep the server's own defaults.
 */
export type TransferImportOptions = {
  activateAutopilots: boolean;
  applyWorkspaceSettings: boolean;
  applyIssuePrefix: boolean;
};

/** How many automations the import wrote (created, updated or renamed). */
export type TransferAutopilotSummary = {
  imported: number;
};

export type TransferImportReportView = {
  secrets_to_fill: TransferSecretToFill[];
  runtimes_to_bind: TransferRuntimeBind[];
  export_gaps: TransferExportGap[];
  stats: TransferImportStats;
  /** Absent on reports from a Desktop build older than the option switches. */
  autopilots?: TransferAutopilotSummary;
};

export type TransferRunRequest =
  | { action: "export"; workspace: string; outPath: string }
  | {
      action: "import";
      workspace: string;
      inPath: string;
      dryRun: boolean;
      onConflict?: string;
      /**
       * Omitted means on: the server binds a unique runtime candidate so a
       * migrated agent is runnable straight after import (DENE-364).
       */
      autoBindRuntimes?: boolean;
      options?: TransferImportOptions;
    };

export type TransferRunResult =
  | { ok: true; action: "export"; outPath: string; bytes: number }
  | {
      ok: true;
      action: "import";
      dryRun: boolean;
      report: TransferImportReportView;
    }
  | { ok: false; code: TransferErrorCode; message: string };

export type TransferPickPathResult =
  | { ok: true; path: string; fileName: string }
  | {
      ok: false;
      reason: "cancelled" | "no_window" | "error";
      error?: string;
    };

export function transferExportFilename(slug: string, now = new Date()): string {
  const safe = slug.trim() || "workspace";
  const stamp = now.toISOString().slice(0, 10).replaceAll("-", "");
  return `multica-transfer-${safe}-${stamp}.zip`;
}

export const EMPTY_TRANSFER_IMPORT_REPORT: TransferImportReportView = {
  secrets_to_fill: [],
  runtimes_to_bind: [],
  export_gaps: [],
  stats: { created: 0, updated: 0, renamed: 0, skipped: 0, failed: 0 },
  autopilots: { imported: 0 },
};
