import { isAbsolute } from "path";
import {
  EMPTY_TRANSFER_IMPORT_REPORT,
  type TransferAutopilotSummary,
  type TransferErrorCode,
  type TransferExportGap,
  type TransferImportOptions,
  type TransferImportReportView,
  type TransferImportStats,
  type TransferProgressEvent,
  type TransferRunRequest,
  type TransferRunResult,
  type TransferRuntimeBind,
  type TransferRuntimeBindAction,
  type TransferRuntimeCandidate,
  type TransferSecretToFill,
} from "../shared/workspace-transfer";

export type TransferCliExportRequest = {
  action: "export";
  workspace: string;
  outPath?: string;
  estimate?: boolean;
};

export type TransferCliImportRequest = {
  action: "import";
  workspace: string;
  inPath: string;
  dryRun?: boolean;
  onConflict?: string;
  autoBindRuntimes?: boolean;
  options?: TransferImportOptions;
};

export type TransferCliRequest = TransferCliExportRequest | TransferCliImportRequest;

export type TransferCommandResult = {
  code: number;
  stdout: string;
  stderr: string;
};

export type TransferCommandHooks = {
  onStdout: (chunk: string) => void;
  onStderr: (chunk: string) => void;
};

export type TransferCliDeps = {
  resolveCli: () => Promise<string | null>;
  profileName: () => Promise<string | null>;
  runCommand: (
    bin: string,
    args: string[],
    hooks: TransferCommandHooks,
  ) => Promise<TransferCommandResult>;
  statSize: (path: string) => Promise<number>;
  sendProgress: (event: TransferProgressEvent) => void;
};

export function buildTransferCliArgs(
  profile: string,
  req: TransferCliRequest,
): string[] {
  if (!profile) {
    throw new Error("unresolved profile — refusing to fall back to the default CLI profile");
  }
  const args = ["--profile", profile, "transfer"];
  if (req.action === "export") {
    args.push("export", "--workspace", req.workspace);
    if (req.estimate === true) {
      args.push("--estimate");
      return args;
    }
    if (!req.outPath) {
      throw new Error("--out is required unless --estimate is set");
    }
    args.push("--out", req.outPath);
    return args;
  }
  args.push("import", "--workspace", req.workspace, "--in", req.inPath);
  if (req.dryRun === true) args.push("--dry-run");
  if (req.onConflict) args.push("--on-conflict", req.onConflict);
  // Explicit false only: the CLI default is on, and an older CLI would reject
  // an unknown flag, so the enabled case must stay flag-free (DENE-364).
  if (req.autoBindRuntimes === false) args.push("--auto-bind-runtimes=false");
  // The option flags default to the values the migration card starts on
  // (DENE-363), so only a deliberate change is sent. A CLI predating the flags
  // therefore keeps working for a default import instead of failing on an
  // unknown flag it would have obeyed anyway.
  if (req.options) {
    if (!req.options.activateAutopilots) args.push("--activate-autopilots=false");
    if (!req.options.applyWorkspaceSettings) args.push("--apply-workspace-settings=false");
    if (req.options.applyIssuePrefix) args.push("--apply-issue-prefix");
  }
  return args;
}

export function parseTransferRunRequest(raw: unknown): TransferRunRequest | null {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return null;
  const obj = raw as Record<string, unknown>;
  if (obj.action === "export") {
    const workspace = asNonEmptyString(obj.workspace);
    const outPath = asNonEmptyString(obj.outPath);
    if (!workspace || !outPath || !isAbsolute(outPath)) return null;
    return { action: "export", workspace, outPath };
  }
  if (obj.action === "import") {
    const workspace = asNonEmptyString(obj.workspace);
    const inPath = asNonEmptyString(obj.inPath);
    if (!workspace || !inPath || !isAbsolute(inPath)) return null;
    const onConflict = asNonEmptyString(obj.onConflict);
    const options = parseTransferImportOptions(obj.options);
    return {
      action: "import",
      workspace,
      inPath,
      dryRun: obj.dryRun === true,
      ...(onConflict ? { onConflict } : {}),
      ...(obj.autoBindRuntimes === false ? { autoBindRuntimes: false } : {}),
      ...(options ? { options } : {}),
    };
  }
  return null;
}

/**
 * Reads the option switches from the renderer. Absent or malformed input falls
 * back to the product defaults, so a renderer older than the switches still
 * gets the "migrated and usable" behavior instead of an all-off import.
 */
function parseTransferImportOptions(
  value: unknown,
): TransferImportOptions | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value)) return undefined;
  const obj = value as Record<string, unknown>;
  return {
    activateAutopilots: obj.activateAutopilots !== false,
    applyWorkspaceSettings: obj.applyWorkspaceSettings !== false,
    applyIssuePrefix: obj.applyIssuePrefix === true,
  };
}

export function classifyTransferError(text: string): TransferErrorCode {
  const blob = text.toLowerCase();
  if (blob.includes("target_unsupported")) return "target_unsupported";
  if (blob.includes("transfer_bundle_corrupt")) return "transfer_bundle_corrupt";
  if (
    /unknown command ["']transfer["']/.test(blob) ||
    blob.includes("unknown command transfer") ||
    /unknown command .*transfer/.test(blob)
  ) {
    return "cli_too_old";
  }
  return "unknown";
}

export function extractJsonValue(text: string): unknown | null {
  const trimmed = text.trim();
  if (!trimmed) return null;
  try {
    return JSON.parse(trimmed) as unknown;
  } catch {
    // fall through to brace matching for pretty-printed JSON mixed with logs
  }
  const start = trimmed.indexOf("{");
  const end = trimmed.lastIndexOf("}");
  if (start < 0 || end <= start) return null;
  try {
    return JSON.parse(trimmed.slice(start, end + 1)) as unknown;
  } catch {
    return null;
  }
}

export function parseTransferEstimate(stdout: string): {
  sessions: number;
  messages: number;
  attachments: number;
  attachment_bodies: number;
  estimated_bytes: number;
} | null {
  const raw = extractJsonValue(stdout);
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return null;
  const obj = raw as Record<string, unknown>;
  const sessions = asNumber(obj.sessions);
  if (sessions === undefined) return null;
  return {
    sessions,
    messages: asNumber(obj.messages) ?? 0,
    attachments: asNumber(obj.attachments) ?? 0,
    attachment_bodies: asNumber(obj.attachment_bodies) ?? 0,
    estimated_bytes: asNumber(obj.estimated_bytes) ?? 0,
  };
}

export function parseTransferProgressLine(line: string): TransferProgressEvent | null {
  const trimmed = line.trim();
  if (!trimmed.startsWith("{")) return null;
  let obj: Record<string, unknown>;
  try {
    const parsed = JSON.parse(trimmed) as unknown;
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return null;
    obj = parsed as Record<string, unknown>;
  } catch {
    return null;
  }
  if (obj.event !== "progress" && obj.type !== "progress") return null;
  return {
    phase: "running",
    sessionsTotal: asNumber(obj.sessions_total),
    sessionsDone: asNumber(obj.session_index ?? obj.sessions_done),
    currentSessionTitle: asNonEmptyString(obj.session_title),
    // Imports count uploads, exports count downloads; the card shows one
    // attachment counter either way (DENE-318).
    attachmentsDownloaded: asNumber(
      obj.attachments_downloaded ?? obj.attachments_uploaded,
    ),
    attachmentsTotal: asNumber(obj.attachments_total),
  };
}

export function parseTransferImportReport(stdout: string): TransferImportReportView {
  const raw = extractJsonValue(stdout);
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) {
    return EMPTY_TRANSFER_IMPORT_REPORT;
  }
  const obj = raw as Record<string, unknown>;
  const configReport =
    obj.config_report && typeof obj.config_report === "object" && !Array.isArray(obj.config_report)
      ? (obj.config_report as Record<string, unknown>)
      : obj;

  const secretsSource = firstArray(configReport.secrets_to_fill, obj.secrets_to_fill);
  const statsSource =
    configReport.stats && typeof configReport.stats === "object" && !Array.isArray(configReport.stats)
      ? (configReport.stats as Record<string, unknown>)
      : obj.stats && typeof obj.stats === "object" && !Array.isArray(obj.stats)
        ? (obj.stats as Record<string, unknown>)
        : {};

  const manifest =
    obj.manifest && typeof obj.manifest === "object" && !Array.isArray(obj.manifest)
      ? (obj.manifest as Record<string, unknown>)
      : null;

  return {
    secrets_to_fill: secretsSource.map(parseSecretToFill).filter(Boolean) as TransferSecretToFill[],
    runtimes_to_bind: firstArray(obj.runtimes_to_bind).map(parseRuntimeBind).filter(Boolean) as TransferRuntimeBind[],
    export_gaps: firstArray(obj.export_gaps, manifest?.export_gaps, configReport.export_gaps)
      .map(parseExportGap)
      .filter(Boolean) as TransferExportGap[],
    stats: parseStats(statsSource),
    autopilots: parseAutopilotSummary(configReport),
  };
}

/**
 * Counts the automations a report says were imported. The card turns this into
 * "Automations: N imported, M of them paused" — the paused half comes from the
 * activation switch the apply will use, because a run without activation writes
 * every one of these rows paused (DENE-363).
 */
function parseAutopilotSummary(
  configReport: Record<string, unknown>,
): TransferAutopilotSummary {
  let imported = 0;
  for (const rawBatch of firstArray(configReport.batches)) {
    if (!rawBatch || typeof rawBatch !== "object" || Array.isArray(rawBatch)) continue;
    const batch = rawBatch as Record<string, unknown>;
    if (batch.entity_type !== "autopilots") continue;
    for (const rawItem of firstArray(batch.items)) {
      if (!rawItem || typeof rawItem !== "object" || Array.isArray(rawItem)) continue;
      const action = (rawItem as Record<string, unknown>).action;
      if (action === "created" || action === "updated" || action === "renamed") {
        imported += 1;
      }
    }
  }
  return { imported };
}

export function createLineParser(onLine: (line: string) => void): {
  push: (chunk: string) => void;
  flush: () => void;
} {
  let buf = "";
  return {
    push(chunk: string) {
      buf += chunk;
      let idx = buf.indexOf("\n");
      while (idx >= 0) {
        onLine(buf.slice(0, idx).replace(/\r$/, ""));
        buf = buf.slice(idx + 1);
        idx = buf.indexOf("\n");
      }
    },
    flush() {
      if (buf.length > 0) {
        onLine(buf.replace(/\r$/, ""));
        buf = "";
      }
    },
  };
}

export async function runTransferCli(
  req: TransferRunRequest,
  deps: TransferCliDeps,
): Promise<TransferRunResult> {
  const bin = await deps.resolveCli();
  if (!bin) {
    return { ok: false, code: "cli_too_old", message: "multica CLI is not installed" };
  }
  const profile = await deps.profileName();
  if (!profile) {
    return { ok: false, code: "unknown", message: "Desktop CLI profile is not ready" };
  }

  const feedProgress = createLineParser((line) => {
    const event = parseTransferProgressLine(line);
    if (event) deps.sendProgress(event);
  });

  const run = (args: string[]) =>
    deps.runCommand(bin, args, {
      onStdout: (chunk) => feedProgress.push(chunk),
      onStderr: (chunk) => feedProgress.push(chunk),
    });

  if (req.action === "export") {
    deps.sendProgress({ phase: "estimating" });
    const estimateArgs = buildTransferCliArgs(profile, {
      action: "export",
      workspace: req.workspace,
      estimate: true,
    });
    const estimate = await run(estimateArgs);
    if (estimate.code !== 0) {
      const code = classifyTransferError(stripProgressLines(`${estimate.stdout}\n${estimate.stderr}`));
      if (code === "cli_too_old" || code === "target_unsupported") {
        feedProgress.flush();
        return { ok: false, code, message: trimOutput(estimate.stderr || estimate.stdout) };
      }
    } else {
      const parsed = parseTransferEstimate(estimate.stdout);
      if (parsed) {
        deps.sendProgress({
          phase: "running",
          sessionsTotal: parsed.sessions,
          attachmentsTotal: parsed.attachment_bodies || parsed.attachments,
        });
      }
    }

    deps.sendProgress({ phase: "running" });
    const exportArgs = buildTransferCliArgs(profile, {
      action: "export",
      workspace: req.workspace,
      outPath: req.outPath,
    });
    const result = await run(exportArgs);
    feedProgress.flush();
    if (result.code !== 0) {
      const code = classifyTransferError(stripProgressLines(`${result.stdout}\n${result.stderr}`));
      return { ok: false, code, message: trimOutput(result.stderr || result.stdout) };
    }
    let bytes = 0;
    try {
      bytes = await deps.statSize(req.outPath);
    } catch {
      bytes = 0;
    }
    deps.sendProgress({ phase: "finalizing" });
    return { ok: true, action: "export", outPath: req.outPath, bytes };
  }

  deps.sendProgress({ phase: req.dryRun ? "estimating" : "running" });
  const importArgs = buildTransferCliArgs(profile, {
    action: "import",
    workspace: req.workspace,
    inPath: req.inPath,
    dryRun: req.dryRun,
    onConflict: req.onConflict,
    autoBindRuntimes: req.autoBindRuntimes,
    options: req.options,
  });
  const result = await run(importArgs);
  feedProgress.flush();
  if (result.code !== 0) {
    const code = classifyTransferError(stripProgressLines(`${result.stdout}\n${result.stderr}`));
    return { ok: false, code, message: trimOutput(result.stderr || result.stdout) };
  }
  deps.sendProgress({ phase: "finalizing" });
  return {
    ok: true,
    action: "import",
    dryRun: req.dryRun,
    report: parseTransferImportReport(result.stdout),
  };
}

function asNonEmptyString(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() ? value : undefined;
}

function asNumber(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function firstArray(...values: unknown[]): unknown[] {
  for (const value of values) {
    if (Array.isArray(value)) return value;
  }
  return [];
}

function parseSecretToFill(value: unknown): TransferSecretToFill | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const obj = value as Record<string, unknown>;
  return {
    entity: asNonEmptyString(obj.entity) ?? "",
    name: asNonEmptyString(obj.name) ?? "",
    field: asNonEmptyString(obj.field) ?? "",
    target_id: asNonEmptyString(obj.target_id) ?? "",
  };
}

function parseRuntimeBind(value: unknown): TransferRuntimeBind | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const obj = value as Record<string, unknown>;
  const candidateIds = Array.isArray(obj.candidate_ids)
    ? obj.candidate_ids.filter((id): id is string => typeof id === "string")
    : [];
  const candidates = Array.isArray(obj.candidates)
    ? obj.candidates.map(parseRuntimeCandidate).filter(Boolean) as TransferRuntimeCandidate[]
    : [];
  return {
    agent_target_id: asNonEmptyString(obj.agent_target_id) ?? "",
    agent_name: asNonEmptyString(obj.agent_name) ?? "",
    provider: asNonEmptyString(obj.provider) ?? "",
    runtime_mode: asNonEmptyString(obj.runtime_mode) ?? "",
    profile_name: asNonEmptyString(obj.profile_name) ?? "",
    candidate_ids: candidateIds,
    candidates,
    // An unknown action token degrades to "" so a newer server cannot turn a
    // row into a silent no-render; the card treats "" as the legacy list.
    action: parseRuntimeBindAction(obj.action),
    auto_bind: obj.auto_bind === true,
    bound_runtime_id: asNonEmptyString(obj.bound_runtime_id) ?? "",
    bound_runtime_name: asNonEmptyString(obj.bound_runtime_name) ?? "",
    reason_code: asNonEmptyString(obj.reason_code) ?? "",
    reason: asNonEmptyString(obj.reason) ?? "",
  };
}

function parseRuntimeCandidate(value: unknown): TransferRuntimeCandidate | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const obj = value as Record<string, unknown>;
  const runtimeId = asNonEmptyString(obj.runtime_id);
  if (!runtimeId) return null;
  return {
    runtime_id: runtimeId,
    name: asNonEmptyString(obj.name) ?? "",
    provider: asNonEmptyString(obj.provider) ?? "",
    runtime_mode: asNonEmptyString(obj.runtime_mode) ?? "",
    profile_name: asNonEmptyString(obj.profile_name) ?? "",
  };
}

function parseRuntimeBindAction(value: unknown): TransferRuntimeBindAction {
  switch (value) {
    case "bound":
    case "already_bound":
    case "candidates":
    case "no_candidate":
    case "agent_missing":
    case "failed":
      return value;
    default:
      return "";
  }
}

function parseExportGap(value: unknown): TransferExportGap | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const obj = value as Record<string, unknown>;
  const group = asNonEmptyString(obj.group);
  const reason = asNonEmptyString(obj.reason);
  if (!group && !reason) return null;
  return {
    group: group ?? "",
    reason: reason ?? "",
    status: asNumber(obj.status),
  };
}

function parseStats(obj: Record<string, unknown>): TransferImportStats {
  return {
    created: asNumber(obj.created) ?? 0,
    updated: asNumber(obj.updated) ?? 0,
    renamed: asNumber(obj.renamed) ?? 0,
    skipped: asNumber(obj.skipped) ?? 0,
    failed: asNumber(obj.failed) ?? 0,
  };
}

// stripProgressLines drops the `{"event":"progress",...}` lines the CLI writes
// to stderr. They feed the migration card's counters; they are not prose about
// a failure, and an export emits one per session and per attachment, so leaving
// them in front of the CLI's error is what pushed the actual reason past the
// cap below (DENE-318).
function stripProgressLines(text: string): string {
  return text
    .split("\n")
    .filter((line) => parseTransferProgressLine(line) === null)
    .join("\n");
}

function trimOutput(text: string): string {
  const meaningful = stripProgressLines(text).trim();
  // The CLI prints its error last, so an over-budget stream keeps its tail.
  return meaningful.length > 2000 ? meaningful.slice(-2000) : meaningful;
}
