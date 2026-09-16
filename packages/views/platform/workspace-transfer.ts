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
