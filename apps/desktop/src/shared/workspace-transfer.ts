export type TransferErrorCode =
  | "target_unsupported"
  | "cli_too_old"
  | "transfer_bundle_corrupt"
  // V3: the target workspace already holds tasks outside the bundle, so the
  // task group cannot keep its issue numbers (contract §2.2).
  | "issues_target_not_empty"
  | "cli_not_found"
  | "cancelled"
  | "busy"
  | "unknown";

/**
 * Which export group the CLI is walking. An export ships up to three groups and
 * each can run for minutes, so the card names the one in flight instead of
 * leaving the previous group's counters frozen on screen (DENE-240).
 */
export type TransferStage = "config" | "conversations" | "issues";

export type TransferProgressEvent = {
  phase: "estimating" | "running" | "finalizing";
  stage?: TransferStage;
  sessionsTotal?: number;
  sessionsDone?: number;
  currentSessionTitle?: string;
  attachmentsDownloaded?: number;
  attachmentsTotal?: number;
  issuesDone?: number;
  issuesTotal?: number;
};

export type TransferSecretToFill = {
  entity: string;
  name: string;
  field: string;
  target_id: string;
};

export type TransferRuntimeCandidate = {
  id: string;
  name: string;
  provider: string;
  runtime_mode: string;
  profile_name: string;
};

/** The three-tier result: written, waiting for a pick, or nothing to pick. */
export type TransferRuntimeBindStatus = "bound" | "pending" | "no_candidate";

export type TransferRuntimeBind = {
  agent_target_id: string;
  agent_name: string;
  provider: string;
  runtime_mode: string;
  profile_name: string;
  status: TransferRuntimeBindStatus;
  reason_code: string;
  reason: string;
  bound_runtime_id: string;
  bound_runtime_name: string;
  candidate_ids: string[];
  candidates: TransferRuntimeCandidate[];
};

/** One agent → runtime pick made in the migration card. */
export type TransferRuntimeBinding = {
  agentId: string;
  runtimeId: string;
};

export type TransferRuntimeBindingOutcome = {
  agent_id: string;
  runtime_id: string;
  agent_name: string;
  runtime_name: string;
  bound: boolean;
  error_code: string;
  error: string;
};

export type TransferBindRuntimesReport = {
  applied: boolean;
  bound: number;
  failed: number;
  bindings: TransferRuntimeBindingOutcome[];
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
 * (DENE-363), plus the runtime auto-bind rule (DENE-364). Absent options keep
 * the server's own defaults.
 */
export type TransferImportOptions = {
  activateAutopilots: boolean;
  applyWorkspaceSettings: boolean;
  applyIssuePrefix: boolean;
  autoBindRuntimes: boolean;
};

/** How many automations the import wrote (created, updated or renamed). */
export type TransferAutopilotSummary = {
  imported: number;
};

/** What a task-group import did, per row kind (V3 contract §9.4 / §9.6). */
export type TransferIssuesSummary = {
  /** False on a dry run: the counts are the plan, not a write. */
  applied: boolean;
  issuesCreated: number;
  issuesSkipped: number;
  commentsCreated: number;
  commentsSkipped: number;
  labelsCreated: number;
  reactionsCreated: number;
  subscribersCreated: number;
  parentsBackfilled: number;
  /** Rows that lost a reference, by what could not be mapped; zero rows dropped. */
  degraded: TransferIssuesDegradation[];
};

export type TransferIssuesDegradation = {
  kind: TransferIssuesDegradationKind;
  count: number;
};

export type TransferIssuesDegradationKind =
  | "status"
  | "assignee"
  | "creator"
  | "project"
  | "author"
  | "resolution"
  | "property"
  | "parent"
  | "mention"
  | "label"
  | "reaction"
  | "reparented";

export type TransferImportReportView = {
  secrets_to_fill: TransferSecretToFill[];
  runtimes_to_bind: TransferRuntimeBind[];
  export_gaps: TransferExportGap[];
  stats: TransferImportStats;
  /** Absent on reports from a Desktop build older than the option switches. */
  autopilots?: TransferAutopilotSummary;
  /** Absent when the CLI predates the `issues` group, or the bundle had none. */
  issues?: TransferIssuesSummary;
};

export type TransferRunRequest =
  | {
      action: "export";
      workspace: string;
      outPath: string;
      /** The V3 task group (contract §9.3); absent unless the user ticked it. */
      includeIssues?: boolean;
    }
  | {
      action: "import";
      workspace: string;
      inPath: string;
      dryRun: boolean;
      onConflict?: string;
      options?: TransferImportOptions;
    }
  | {
      action: "bind-runtimes";
      workspace: string;
      bindings: TransferRuntimeBinding[];
    };

export type TransferRunResult =
  | { ok: true; action: "export"; outPath: string; bytes: number }
  | {
      ok: true;
      action: "import";
      dryRun: boolean;
      report: TransferImportReportView;
    }
  | {
      ok: true;
      action: "bind-runtimes";
      report: TransferBindRuntimesReport;
    }
  | { ok: false; code: TransferErrorCode; message: string };

/**
 * Which transfer a job is running, in the terms the card labels its buttons
 * with. A dry run and a write are told apart so the import button never says
 * "importing" while it is only refreshing a preview.
 */
export type TransferJobKind =
  | "export"
  | "import-preview"
  | "import-apply"
  | "bind-runtimes";

/**
 * The transfer the main process is running, or the last one it ran.
 *
 * A transfer outlives the card: the CLI runs in the main process, so switching
 * settings pages unmounts the card while the export keeps going. Before this
 * state existed the card's own `useState` was the only record of the run, so a
 * page switch made a running export look stopped, and the next click hit the
 * main process's "a transfer is already running" guard and appeared to do
 * nothing (DENE-240). The card now rebuilds itself from this snapshot on mount.
 */
export type TransferJobState = {
  /** Monotonic within an app session; 0 before the first transfer. */
  runId: number;
  kind: TransferJobKind | null;
  running: boolean;
  progress: TransferProgressEvent | null;
  /** The bundle path of a running or finished import, for the card's header. */
  inPath: string | null;
  /** The finished job's answer, kept so a remounted card can still read it. */
  result: TransferRunResult | null;
};

export const IDLE_TRANSFER_JOB_STATE: TransferJobState = {
  runId: 0,
  kind: null,
  running: false,
  progress: null,
  inPath: null,
  result: null,
};

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
