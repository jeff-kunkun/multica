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
  /**
   * The same attachment progress in bytes. Attachment bodies differ by two
   * orders of magnitude, so a count that stalls on one 15 MB archive while the
   * remaining 300 files are thumbnails is not progress the user can read
   * (DENE-443). A CLI that sends these decides the bar and the line.
   */
  attachmentsBytesUploaded?: number;
  attachmentsBytesTotal?: number;
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

/**
 * The reference kinds a task import can lose. The server reports the unmapped
 * rows themselves (status_unmapped, mention_unmapped, ...); the card only needs
 * which kind and how many, so the CLI report is flattened to this list.
 */
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
  /**
   * Absent on reports from a CLI older than the `issues` group, and on an
   * import whose bundle carried no tasks — the card then shows nothing instead
   * of a row of zeroes.
   */
  issues?: TransferIssuesSummary;
};

export type TransferRunRequest =
  | {
      action: "export";
      workspace: string;
      outPath: string;
      /**
       * The V3 task group (contract §9.3). Off unless the user ticks it: the
       * zip grows several times over, the target workspace must be empty, and
       * the default bundle stays readable by a target that predates V3.
       */
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
      reason: "cancelled" | "no_window" | "error" | "unsupported";
      error?: string;
    };

interface DesktopTransferAPI {
  pickTransferExportPath?: (input?: {
    slug?: string;
  }) => Promise<TransferPickPathResult>;
  pickTransferImportPath?: () => Promise<TransferPickPathResult>;
  runWorkspaceTransfer?: (request: TransferRunRequest) => Promise<TransferRunResult>;
  onTransferProgress?: (
    callback: (event: TransferProgressEvent) => void,
  ) => () => void;
  getTransferJobState?: () => Promise<TransferJobState>;
  onTransferJobState?: (
    callback: (state: TransferJobState) => void,
  ) => () => void;
}

function readDesktopAPI(): DesktopTransferAPI | undefined {
  if (typeof window === "undefined") return undefined;
  return (window as unknown as { desktopAPI?: DesktopTransferAPI }).desktopAPI;
}

const UNSUPPORTED_PICK: TransferPickPathResult = {
  ok: false,
  reason: "unsupported",
};

const MISSING_IPC: TransferRunResult = {
  ok: false,
  code: "cli_too_old",
  message: "Desktop transfer IPC is unavailable",
};

export async function pickTransferExportPath(slug?: string): Promise<TransferPickPathResult> {
  const api = readDesktopAPI();
  if (!api?.pickTransferExportPath) return UNSUPPORTED_PICK;
  return api.pickTransferExportPath(slug ? { slug } : undefined);
}

export async function pickTransferImportPath(): Promise<TransferPickPathResult> {
  const api = readDesktopAPI();
  if (!api?.pickTransferImportPath) return UNSUPPORTED_PICK;
  return api.pickTransferImportPath();
}

export async function runWorkspaceTransfer(
  request: TransferRunRequest,
): Promise<TransferRunResult> {
  const api = readDesktopAPI();
  if (!api?.runWorkspaceTransfer) return MISSING_IPC;
  return api.runWorkspaceTransfer(request);
}

/**
 * The run the main process is on. A Desktop build that predates the job state
 * reports idle, which is what the card assumed before anyway.
 */
export async function getTransferJobState(): Promise<TransferJobState> {
  const api = readDesktopAPI();
  if (!api?.getTransferJobState) return IDLE_TRANSFER_JOB_STATE;
  return api.getTransferJobState();
}

export function subscribeTransferJobState(
  callback: (state: TransferJobState) => void,
): () => void {
  const api = readDesktopAPI();
  if (!api?.onTransferJobState) return () => {};
  return api.onTransferJobState(callback);
}

export function subscribeTransferProgress(
  callback: (event: TransferProgressEvent) => void,
): () => void {
  const api = readDesktopAPI();
  if (!api?.onTransferProgress) return () => {};
  return api.onTransferProgress(callback);
}

export function fileNameFromPath(path: string): string {
  const parts = path.split(/[/\\]/);
  return parts[parts.length - 1] || path;
}

export function formatTransferBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GB`;
}

/**
 * How far the run has got, as a 0–1 ratio, or null when nothing in the sample
 * carries an honest denominator.
 *
 * The counters are read most-specific first: the task walk is the last and
 * longest stage, so once it reports a total it is the one the bar should track;
 * the attachment counter is the weakest signal because the CLI only knows an
 * attachment total on the import side (DENE-240).
 */
export function transferProgressRatio(
  progress: TransferProgressEvent,
): number | null {
  const pairs: [number | undefined, number | undefined][] = [
    [progress.issuesDone, progress.issuesTotal],
    [progress.sessionsDone, progress.sessionsTotal],
    // Bytes before the count: when the CLI reports both, the count can sit
    // still for minutes on one large blob while the bytes keep moving, and the
    // bar must show the movement (DENE-443).
    [progress.attachmentsBytesUploaded, progress.attachmentsBytesTotal],
    [progress.attachmentsDownloaded, progress.attachmentsTotal],
  ];
  for (const [done, total] of pairs) {
    if (total == null || total <= 0) continue;
    const ratio = (done ?? 0) / total;
    return Math.min(1, Math.max(0, ratio));
  }
  return null;
}

/** Host shown as the V2 export source (current API server). */
export function transferExportSourceHost(baseUrl: string): string {
  const trimmed = baseUrl.trim();
  if (!trimmed) return "";
  try {
    const url = new URL(trimmed.includes("://") ? trimmed : `https://${trimmed}`);
    return url.host;
  } catch {
    return trimmed
      .replace(/^https?:\/\//i, "")
      .replace(/[/?#].*$/, "")
      .trim();
  }
}

export const TRANSFER_EXPORT_COMPLETED_KEY = "multica.transfer.export-completed";

export type TransferFlagStorage = {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
};

function defaultFlagStorage(): TransferFlagStorage | null {
  if (typeof window === "undefined") return null;
  try {
    return window.localStorage;
  } catch {
    return null;
  }
}

export function hasCompletedTransferExport(
  storage: TransferFlagStorage | null = defaultFlagStorage(),
): boolean {
  return storage?.getItem(TRANSFER_EXPORT_COMPLETED_KEY) === "1";
}

export function markTransferExportCompleted(
  storage: TransferFlagStorage | null = defaultFlagStorage(),
): void {
  storage?.setItem(TRANSFER_EXPORT_COMPLETED_KEY, "1");
}
