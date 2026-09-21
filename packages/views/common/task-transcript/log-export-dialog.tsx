"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import {
  AlertTriangle,
  Check,
  Copy,
  Download,
  ExternalLink,
  FileArchive,
  Inbox,
  Send,
  ShieldCheck,
} from "lucide-react";
import { api, errorCode } from "@multica/core/api";
import type {
  LogExportPreview,
  LogExportReport,
  LogExportScope,
} from "@multica/core/api";
import { cn } from "@multica/ui/lib/utils";
import { copyText } from "@multica/ui/lib/clipboard";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { Spinner } from "@multica/ui/components/ui/spinner";
import { useT } from "../../i18n";

const DEFAULT_HOURS = 6;
const MAX_HOURS = 720;
const SCOPES: LogExportScope[] = ["run", "hours", "task"];

type ExportState =
  | { phase: "idle" }
  | { phase: "exporting" }
  | { phase: "empty" }
  | { phase: "ready"; preview: LogExportPreview; partial: boolean }
  | { phase: "failed"; reason: string; collected: number | null };

type ReportState =
  | { phase: "idle" }
  | { phase: "reporting" }
  | { phase: "done"; report: LogExportReport }
  | { phase: "failed"; reason: string };

export interface LogExportPanelProps {
  taskId: string;
  /** Runs with no issue (chat, autopilot) can be exported but not reported. */
  canReport: boolean;
}

export interface LogExportDialogProps extends LogExportPanelProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function saveBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  anchor.rel = "noopener";
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  setTimeout(() => URL.revokeObjectURL(url), 0);
}

function collectedEntries(err: unknown): number | null {
  const body = (err as { body?: unknown } | null)?.body;
  if (body && typeof body === "object") {
    const n = (body as { collected_entries?: unknown }).collected_entries;
    if (typeof n === "number") return n;
  }
  return null;
}

/**
 * The export flow itself, with no container around it: scope → export →
 * bundle card → report / copy. `LogExportDialog` below wraps it in the shared
 * Dialog; a drawer or a popover card can wrap the same panel.
 */
export function LogExportPanel({ taskId, canReport }: LogExportPanelProps) {
  const { t } = useT("agents");
  const [scope, setScope] = useState<LogExportScope>("run");
  const [hours, setHours] = useState(DEFAULT_HOURS);
  const [state, setState] = useState<ExportState>({ phase: "idle" });
  const [report, setReport] = useState<ReportState>({ phase: "idle" });
  const [copied, setCopied] = useState(false);
  const [downloading, setDownloading] = useState(false);
  // A response that lands after the scope changed describes a bundle the user
  // is no longer looking at.
  const requestRef = useRef(0);

  useEffect(() => {
    if (!copied) return;
    const id = setTimeout(() => setCopied(false), 2000);
    return () => clearTimeout(id);
  }, [copied]);

  const runExport = useCallback(
    async (nextScope: LogExportScope, nextHours: number, allowPartial: boolean) => {
      const request = ++requestRef.current;
      setState({ phase: "exporting" });
      setReport({ phase: "idle" });
      try {
        const preview = await api.previewTaskLogExport(taskId, {
          scope: nextScope,
          hours: nextHours,
          allowPartial,
        });
        if (request !== requestRef.current) return;
        if (preview.empty === true || preview.filename === "") {
          setState({ phase: "empty" });
          return;
        }
        setState({ phase: "ready", preview, partial: allowPartial });
      } catch (err) {
        if (request !== requestRef.current) return;
        setState({
          phase: "failed",
          reason: err instanceof Error ? err.message : String(err),
          collected: errorCode(err) === "partial_available" ? (collectedEntries(err) ?? 0) : null,
        });
      }
    },
    [taskId],
  );

  const changeScope = (next: LogExportScope) => {
    requestRef.current++;
    setScope(next);
    setState({ phase: "idle" });
    setReport({ phase: "idle" });
  };

  const changeHours = (raw: string) => {
    const parsed = Number.parseInt(raw, 10);
    requestRef.current++;
    setHours(Number.isFinite(parsed) ? Math.min(Math.max(parsed, 1), MAX_HOURS) : DEFAULT_HOURS);
    setState({ phase: "idle" });
    setReport({ phase: "idle" });
  };

  const widen = () => {
    setScope("task");
    void runExport("task", hours, false);
  };

  const download = async (partial: boolean, filename: string) => {
    setDownloading(true);
    try {
      const blob = await api.downloadTaskLogExport(taskId, { scope, hours, allowPartial: partial });
      saveBlob(blob, filename);
    } catch (err) {
      setState({
        phase: "failed",
        reason: err instanceof Error ? err.message : String(err),
        collected: null,
      });
    } finally {
      setDownloading(false);
    }
  };

  const sendReport = async (partial: boolean) => {
    setReport({ phase: "reporting" });
    try {
      const result = await api.reportTaskLogExport(taskId, { scope, hours, allowPartial: partial });
      setReport({ phase: "done", report: result });
    } catch (err) {
      setReport({ phase: "failed", reason: err instanceof Error ? err.message : String(err) });
    }
  };

  const scopeLabel = (value: LogExportScope) => {
    switch (value) {
      case "run":
        return t(($) => $.log_export.scope_run);
      case "hours":
        return t(($) => $.log_export.scope_hours);
      case "task":
        return t(($) => $.log_export.scope_task);
      default:
        return value;
    }
  };

  const busy = state.phase === "exporting";

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-col gap-2">
        <div className="text-caption font-medium text-muted-foreground">
          {t(($) => $.log_export.scope_label)}
        </div>
        <div
          role="radiogroup"
          aria-label={t(($) => $.log_export.scope_label)}
          className="grid grid-cols-3 gap-1 rounded-lg bg-muted p-1"
        >
          {SCOPES.map((value) => (
            <button
              key={value}
              type="button"
              role="radio"
              aria-checked={scope === value}
              disabled={busy}
              onClick={() => changeScope(value)}
              className={cn(
                "rounded-md px-2 py-1.5 text-caption text-muted-foreground transition-colors hover:text-foreground disabled:opacity-60",
                scope === value && "bg-background font-medium text-foreground shadow-sm",
              )}
            >
              {scopeLabel(value)}
            </button>
          ))}
        </div>
        {scope === "hours" && (
          <label className="flex items-center gap-2 text-caption text-muted-foreground">
            {t(($) => $.log_export.hours_prefix)}
            <Input
              type="number"
              min={1}
              max={MAX_HOURS}
              value={hours}
              disabled={busy}
              onChange={(e) => changeHours(e.target.value)}
              aria-label={t(($) => $.log_export.hours_aria)}
              className="h-7 w-20"
            />
            {t(($) => $.log_export.hours_suffix)}
          </label>
        )}
        <p className="flex items-start gap-1.5 text-caption text-muted-foreground">
          <ShieldCheck aria-hidden className="mt-0.5 h-3.5 w-3.5 shrink-0" />
          {t(($) => $.log_export.redaction_note)}
        </p>
      </div>

      {state.phase === "idle" && (
        <Button onClick={() => void runExport(scope, hours, false)} className="self-end">
          {t(($) => $.log_export.export)}
        </Button>
      )}

      {state.phase === "exporting" && (
        <div
          role="status"
          className="flex items-center gap-2 rounded-lg border px-3 py-4 text-body text-muted-foreground"
        >
          <Spinner className="h-4 w-4" />
          {t(($) => $.log_export.exporting)}
        </div>
      )}

      {state.phase === "empty" && (
        <div className="flex flex-col items-center gap-2 rounded-lg border border-dashed px-3 py-6 text-center">
          <Inbox aria-hidden className="h-5 w-5 text-faint-foreground" />
          <div className="text-body">{t(($) => $.log_export.empty_title)}</div>
          {scope !== "task" ? (
            <Button variant="outline" size="sm" onClick={widen}>
              {t(($) => $.log_export.empty_widen)}
            </Button>
          ) : (
            <div className="text-caption text-muted-foreground">
              {t(($) => $.log_export.empty_widest)}
            </div>
          )}
        </div>
      )}

      {state.phase === "failed" && (
        <div role="alert" className="flex flex-col gap-3 rounded-lg border border-destructive/40 px-3 py-3">
          <div className="flex items-start gap-2 text-body">
            <AlertTriangle aria-hidden className="mt-0.5 h-4 w-4 shrink-0 text-destructive" />
            <div className="min-w-0">
              <div>{t(($) => $.log_export.failed_title)}</div>
              <div className="mt-0.5 break-words text-caption text-muted-foreground">{state.reason}</div>
            </div>
          </div>
          <div className="flex flex-wrap justify-end gap-2">
            {state.collected !== null && (
              <Button variant="outline" size="sm" onClick={() => void runExport(scope, hours, true)}>
                {t(($) => $.log_export.export_partial, { count: state.collected })}
              </Button>
            )}
            <Button size="sm" onClick={() => void runExport(scope, hours, false)}>
              {t(($) => $.log_export.retry)}
            </Button>
          </div>
        </div>
      )}

      {state.phase === "ready" && (
        <div className="flex flex-col gap-3">
          <div className="flex items-start gap-3 rounded-lg border px-3 py-3">
            <FileArchive aria-hidden className="mt-0.5 h-5 w-5 shrink-0 text-muted-foreground" />
            <div className="min-w-0 flex-1">
              <div className="truncate text-body font-medium" title={state.preview.filename}>
                {state.preview.filename}
              </div>
              <div className="mt-0.5 text-caption text-muted-foreground">
                {t(($) => $.log_export.bundle_facts, {
                  size: formatBytes(state.preview.size_bytes ?? 0),
                  runs: state.preview.meta?.run_count ?? 0,
                  entries: state.preview.meta?.entry_count ?? 0,
                })}
              </div>
              {(state.preview.meta?.partial === true ||
                (state.preview.meta?.dropped_entries ?? 0) > 0) && (
                <div className="mt-1 text-caption text-warning">
                  {state.preview.meta?.partial === true
                    ? t(($) => $.log_export.bundle_partial)
                    : t(($) => $.log_export.bundle_trimmed, {
                        count: state.preview.meta?.dropped_entries ?? 0,
                      })}
                </div>
              )}
            </div>
            <Button
              variant="ghost"
              size="icon-sm"
              disabled={downloading}
              onClick={() => void download(state.partial, state.preview.filename)}
              aria-label={t(($) => $.log_export.download)}
              title={t(($) => $.log_export.download)}
            >
              {downloading ? <Spinner className="h-4 w-4" /> : <Download className="h-4 w-4" />}
            </Button>
          </div>

          {report.phase === "done" ? (
            <ReportResult report={report.report} />
          ) : (
            <>
              {report.phase === "failed" && (
                <div role="alert" className="break-words text-caption text-destructive">
                  {t(($) => $.log_export.report_failed, { reason: report.reason })}
                </div>
              )}
              <div className="flex flex-wrap justify-end gap-2">
                <Button
                  variant="outline"
                  onClick={() => {
                    void copyText(state.preview.summary ?? "").then(() => setCopied(true));
                  }}
                >
                  {copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
                  {copied ? t(($) => $.log_export.copied) : t(($) => $.log_export.copy_summary)}
                </Button>
                {canReport && (
                  <Button
                    disabled={report.phase === "reporting"}
                    onClick={() => void sendReport(state.partial)}
                  >
                    {report.phase === "reporting" ? (
                      <Spinner className="h-4 w-4" />
                    ) : (
                      <Send className="h-4 w-4" />
                    )}
                    {report.phase === "failed"
                      ? t(($) => $.log_export.report_retry)
                      : t(($) => $.log_export.report)}
                  </Button>
                )}
              </div>
              {canReport && (
                <p className="text-right text-caption text-muted-foreground">
                  {state.preview.log_repo_configured === true
                    ? t(($) => $.log_export.report_hint_repo)
                    : t(($) => $.log_export.report_hint_attachment)}
                </p>
              )}
            </>
          )}
        </div>
      )}
    </div>
  );
}

function ReportResult({ report }: { report: LogExportReport }) {
  const { t } = useT("agents");
  const viaGit = report.delivery === "git" && report.link !== "";
  return (
    <div role="status" className="flex flex-col gap-1 rounded-lg border bg-muted/40 px-3 py-3 text-body">
      <div className="flex items-center gap-2">
        <Check aria-hidden className="h-4 w-4 shrink-0 text-success" />
        {report.issue_identifier
          ? t(($) => $.log_export.reported_to, { issue: report.issue_identifier })
          : t(($) => $.log_export.reported)}
      </div>
      <div className="pl-6 text-caption text-muted-foreground">
        {report.mentioned
          ? t(($) => $.log_export.reported_mentioned, { name: report.mentioned })
          : t(($) => $.log_export.reported_no_mention)}
      </div>
      {viaGit ? (
        <a
          href={report.link}
          target="_blank"
          rel="noopener noreferrer"
          className="flex items-center gap-1 pl-6 text-caption text-primary hover:underline"
        >
          <ExternalLink aria-hidden className="h-3 w-3 shrink-0" />
          <span className="truncate">{t(($) => $.log_export.reported_link)}</span>
        </a>
      ) : (
        <div className="pl-6 text-caption text-muted-foreground">
          {t(($) => $.log_export.reported_attachment)}
        </div>
      )}
      {report.fallback_reason !== "" && (
        <div className="break-words pl-6 text-caption text-warning">
          {t(($) => $.log_export.reported_fallback, { reason: report.fallback_reason })}
        </div>
      )}
    </div>
  );
}

export function LogExportDialog({ open, onOpenChange, taskId, canReport }: LogExportDialogProps) {
  const { t } = useT("agents");
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t(($) => $.log_export.title)}</DialogTitle>
          <DialogDescription>{t(($) => $.log_export.description)}</DialogDescription>
        </DialogHeader>
        {/* Mounted only while open, so every opening starts from the scope
            picker instead of the previous bundle. */}
        {open && <LogExportPanel taskId={taskId} canReport={canReport} />}
      </DialogContent>
    </Dialog>
  );
}
