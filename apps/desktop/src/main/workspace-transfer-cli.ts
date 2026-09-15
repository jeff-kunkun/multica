import { isAbsolute } from "path";
import {
  EMPTY_TRANSFER_IMPORT_REPORT,
  type TransferErrorCode,
  type TransferExportGap,
  type TransferImportReportView,
  type TransferImportStats,
  type TransferProgressEvent,
  type TransferRunRequest,
  type TransferRunResult,
  type TransferRuntimeBind,
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
    return {
      action: "import",
      workspace,
      inPath,
      dryRun: obj.dryRun === true,
      ...(onConflict ? { onConflict } : {}),
    };
  }
  return null;
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
    attachmentsDownloaded: asNumber(obj.attachments_downloaded),
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
  };
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
      const code = classifyTransferError(`${estimate.stdout}\n${estimate.stderr}`);
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
      const code = classifyTransferError(`${result.stdout}\n${result.stderr}`);
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
  });
  const result = await run(importArgs);
  feedProgress.flush();
  if (result.code !== 0) {
    const code = classifyTransferError(`${result.stdout}\n${result.stderr}`);
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
  return {
    agent_target_id: asNonEmptyString(obj.agent_target_id) ?? "",
    agent_name: asNonEmptyString(obj.agent_name) ?? "",
    provider: asNonEmptyString(obj.provider) ?? "",
    runtime_mode: asNonEmptyString(obj.runtime_mode) ?? "",
    profile_name: asNonEmptyString(obj.profile_name) ?? "",
    candidate_ids: candidateIds,
  };
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

function trimOutput(text: string): string {
  return text.trim().slice(0, 2000);
}
