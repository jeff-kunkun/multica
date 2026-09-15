"use client";

import { useEffect, useState } from "react";
import { AlertCircle, Loader2 } from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { autopilotKeys } from "@multica/core/autopilots/queries";
import { chatKeys } from "@multica/core/chat/queries";
import { issueStatusKeys } from "@multica/core/issue-statuses/queries";
import { issueViewKeys } from "@multica/core/issue-views/queries";
import { labelKeys } from "@multica/core/labels/queries";
import { useCurrentWorkspace } from "@multica/core/paths";
import { useCurrentMember } from "@multica/core/permissions";
import { projectKeys } from "@multica/core/projects/queries";
import { propertyKeys } from "@multica/core/properties/queries";
import { quickActionKeys } from "@multica/core/quick-actions/queries";
import { workspaceKeys } from "@multica/core/workspace/queries";
import {
  Alert,
  AlertDescription,
  AlertTitle,
} from "@multica/ui/components/ui/alert";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../i18n";
import {
  fileNameFromPath,
  formatTransferBytes,
  isDesktopShell,
  pickTransferExportPath,
  pickTransferImportPath,
  runWorkspaceTransfer,
  subscribeTransferProgress,
  type TransferErrorCode,
  type TransferImportReportView,
  type TransferProgressEvent,
} from "../../platform";
import {
  SettingsCard,
  SettingsRow,
  SettingsSection,
} from "./settings-layout";

type ImportPhase =
  | { step: "idle" }
  | {
      step: "preview" | "result";
      fileName: string;
      inPath: string;
      report: TransferImportReportView;
    };

function secretLabel(item: {
  entity: string;
  name: string;
  field: string;
}): string {
  return [item.entity, item.name, item.field].filter(Boolean).join(" · ");
}

function runtimeLabel(item: {
  agent_name: string;
  provider: string;
  runtime_mode: string;
  profile_name: string;
}): string {
  return [item.agent_name, item.provider, item.runtime_mode, item.profile_name]
    .filter(Boolean)
    .join(" · ");
}

export function WorkspaceMigrationCard() {
  const { t } = useT("settings");
  const workspace = useCurrentWorkspace();
  const wsId = workspace?.id ?? "";
  const slug = workspace?.slug ?? "";
  const { role } = useCurrentMember(wsId);
  const canManage = role === "owner" || role === "admin";
  const qc = useQueryClient();

  const [busy, setBusy] = useState(false);
  const [progress, setProgress] = useState<TransferProgressEvent | null>(null);
  const [exportResult, setExportResult] = useState<{
    path: string;
    bytes: number;
  } | null>(null);
  const [error, setError] = useState<{
    code: TransferErrorCode;
    fallback: string;
  } | null>(null);
  const [importPhase, setImportPhase] = useState<ImportPhase>({ step: "idle" });
  const [confirmOpen, setConfirmOpen] = useState(false);

  useEffect(() => {
    return subscribeTransferProgress((event) => setProgress(event));
  }, []);

  if (!isDesktopShell()) return null;

  function errorMessage(code: TransferErrorCode, fallback: string): string {
    switch (code) {
      case "target_unsupported":
        return t(($) => $.config_transfer.migration.target_unsupported);
      case "cli_too_old":
      case "cli_not_found":
        return t(($) => $.config_transfer.migration.cli_too_old);
      case "transfer_bundle_corrupt":
        return t(($) => $.config_transfer.migration.bundle_corrupt);
      default:
        return fallback || t(($) => $.config_transfer.migration.failed);
    }
  }

  async function invalidateImportedQueries() {
    if (!wsId) return;
    await Promise.all([
      qc.invalidateQueries({ queryKey: chatKeys.all(wsId) }),
      qc.invalidateQueries({ queryKey: ["agents"] }),
      qc.invalidateQueries({ queryKey: workspaceKeys.all(wsId) }),
      qc.invalidateQueries({ queryKey: labelKeys.all(wsId) }),
      qc.invalidateQueries({ queryKey: issueStatusKeys.all(wsId) }),
      qc.invalidateQueries({ queryKey: propertyKeys.all(wsId) }),
      qc.invalidateQueries({ queryKey: quickActionKeys.all(wsId) }),
      qc.invalidateQueries({ queryKey: projectKeys.all(wsId) }),
      qc.invalidateQueries({ queryKey: autopilotKeys.all(wsId) }),
      qc.invalidateQueries({ queryKey: issueViewKeys.all(wsId) }),
    ]);
  }

  async function handleExport() {
    if (!slug || !canManage) return;
    setError(null);
    setExportResult(null);
    const picked = await pickTransferExportPath(slug);
    if (!picked.ok) return;
    setBusy(true);
    setProgress({ phase: "estimating" });
    try {
      const result = await runWorkspaceTransfer({
        action: "export",
        workspace: slug,
        outPath: picked.path,
      });
      if (!result.ok) {
        setError({ code: result.code, fallback: result.message });
        toast.error(errorMessage(result.code, result.message));
        return;
      }
      if (result.action !== "export") return;
      setExportResult({ path: result.outPath, bytes: result.bytes });
      toast.success(t(($) => $.config_transfer.migration.export_success));
    } finally {
      setBusy(false);
      setProgress(null);
    }
  }

  async function handleImportPick() {
    if (!slug || !canManage) return;
    setError(null);
    const picked = await pickTransferImportPath();
    if (!picked.ok) return;
    setBusy(true);
    setProgress({ phase: "estimating" });
    try {
      const result = await runWorkspaceTransfer({
        action: "import",
        workspace: slug,
        inPath: picked.path,
        dryRun: true,
      });
      if (!result.ok) {
        setImportPhase({ step: "idle" });
        setError({ code: result.code, fallback: result.message });
        toast.error(errorMessage(result.code, result.message));
        return;
      }
      if (result.action !== "import") return;
      setImportPhase({
        step: "preview",
        fileName: picked.fileName,
        inPath: picked.path,
        report: result.report,
      });
    } finally {
      setBusy(false);
      setProgress(null);
    }
  }

  async function handleApply() {
    if (!slug || importPhase.step !== "preview") return;
    setConfirmOpen(false);
    setBusy(true);
    setProgress({ phase: "running" });
    try {
      const result = await runWorkspaceTransfer({
        action: "import",
        workspace: slug,
        inPath: importPhase.inPath,
        dryRun: false,
      });
      if (!result.ok) {
        setError({ code: result.code, fallback: result.message });
        toast.error(errorMessage(result.code, result.message));
        return;
      }
      if (result.action !== "import") return;
      setImportPhase({
        step: "result",
        fileName: importPhase.fileName,
        inPath: importPhase.inPath,
        report: result.report,
      });
      toast.success(t(($) => $.config_transfer.migration.success));
      await invalidateImportedQueries();
    } finally {
      setBusy(false);
      setProgress(null);
    }
  }

  const report =
    importPhase.step === "preview" || importPhase.step === "result"
      ? importPhase.report
      : null;

  return (
    <SettingsSection
      title={t(($) => $.config_transfer.migration.title)}
      description={t(($) => $.config_transfer.migration.description)}
    >
      <div data-testid="workspace-migration-card">
        <SettingsCard>
        <SettingsRow
          label={t(($) => $.config_transfer.migration.export_button)}
          description={t(($) => $.config_transfer.migration.export_hint)}
        >
          <Button
            type="button"
            disabled={!canManage || busy || !slug}
            onClick={() => void handleExport()}
          >
            {busy && !report ? (
              <Loader2 className="size-4 animate-spin" aria-hidden="true" />
            ) : null}
            {busy && progress && importPhase.step === "idle"
              ? t(($) => $.config_transfer.migration.exporting)
              : t(($) => $.config_transfer.migration.export_button)}
          </Button>
        </SettingsRow>

        {progress && importPhase.step === "idle" ? (
          <div
            className="space-y-1 px-4 py-3 text-caption text-muted-foreground"
            data-testid="workspace-migration-progress"
          >
            <ProgressLines progress={progress} />
          </div>
        ) : null}

        {exportResult ? (
          <p className="px-4 py-3 text-body text-foreground">
            {t(($) => $.config_transfer.migration.saved_as, {
              name: fileNameFromPath(exportResult.path),
              size: formatTransferBytes(exportResult.bytes),
            })}
          </p>
        ) : null}

        <SettingsRow
          label={t(($) => $.config_transfer.migration.import_button)}
          description={
            importPhase.step === "idle"
              ? t(($) => $.config_transfer.migration.import_hint)
              : importPhase.fileName
          }
        >
          <Button
            type="button"
            variant="outline"
            disabled={!canManage || busy || !slug}
            onClick={() => void handleImportPick()}
          >
            {busy && importPhase.step !== "result" ? (
              <Loader2 className="size-4 animate-spin" aria-hidden="true" />
            ) : null}
            {busy && importPhase.step !== "result"
              ? t(($) => $.config_transfer.migration.previewing)
              : importPhase.step === "idle"
                ? t(($) => $.config_transfer.migration.import_button)
                : t(($) => $.config_transfer.import.replace_file)}
          </Button>
        </SettingsRow>
        </SettingsCard>
      </div>

      {error ? (
        <Alert variant="destructive" data-testid="workspace-migration-error">
          <AlertCircle />
          <AlertTitle>
            {t(($) => $.config_transfer.migration.failed)}
          </AlertTitle>
          <AlertDescription>
            {errorMessage(error.code, error.fallback)}
          </AlertDescription>
        </Alert>
      ) : null}

      {report ? (
        <ImportReportView
          report={report}
          showConfirm={importPhase.step === "preview" && !busy}
          applying={busy && importPhase.step === "preview"}
          onConfirm={() => setConfirmOpen(true)}
        />
      ) : null}

      <AlertDialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.config_transfer.migration.confirm_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.config_transfer.migration.confirm_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>
              {t(($) => $.config_transfer.migration.cancel)}
            </AlertDialogCancel>
            <AlertDialogAction onClick={() => void handleApply()}>
              {t(($) => $.config_transfer.migration.confirm)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsSection>
  );
}

function ProgressLines({ progress }: { progress: TransferProgressEvent }) {
  const { t } = useT("settings");
  const lines: string[] = [];
  if (progress.sessionsTotal != null) {
    lines.push(
      t(($) => $.config_transfer.migration.progress_sessions, {
        done: progress.sessionsDone ?? 0,
        total: progress.sessionsTotal,
      }),
    );
  }
  if (progress.currentSessionTitle) {
    lines.push(
      t(($) => $.config_transfer.migration.progress_session_current, {
        title: progress.currentSessionTitle,
      }),
    );
  }
  if (progress.attachmentsTotal != null) {
    lines.push(
      t(($) => $.config_transfer.migration.progress_attachments_total, {
        done: progress.attachmentsDownloaded ?? 0,
        total: progress.attachmentsTotal,
      }),
    );
  } else if (progress.attachmentsDownloaded != null) {
    lines.push(
      t(($) => $.config_transfer.migration.progress_attachments, {
        count: progress.attachmentsDownloaded,
      }),
    );
  }
  if (lines.length === 0) {
    return <p>{t(($) => $.config_transfer.migration.exporting)}</p>;
  }
  return (
    <>
      {lines.map((line) => (
        <p key={line}>{line}</p>
      ))}
    </>
  );
}

function ImportReportView({
  report,
  showConfirm,
  applying,
  onConfirm,
}: {
  report: TransferImportReportView;
  showConfirm: boolean;
  applying: boolean;
  onConfirm: () => void;
}) {
  const { t } = useT("settings");
  const stats = report.stats;
  return (
    <div className="space-y-4" data-testid="workspace-migration-report">
      <SettingsCard>
        <div className="grid grid-cols-2 gap-3 px-4 py-4 sm:grid-cols-5">
          {(
            [
              {
                key: "created",
                label: t(($) => $.config_transfer.import.stats_created),
                count: stats.created,
              },
              {
                key: "updated",
                label: t(($) => $.config_transfer.import.stats_updated),
                count: stats.updated,
              },
              {
                key: "renamed",
                label: t(($) => $.config_transfer.import.stats_renamed),
                count: stats.renamed,
              },
              {
                key: "skipped",
                label: t(($) => $.config_transfer.import.stats_skipped),
                count: stats.skipped,
              },
              {
                key: "failed",
                label: t(($) => $.config_transfer.import.stats_failed),
                count: stats.failed,
              },
            ] as const
          ).map((item) => (
            <div key={item.key}>
              <p className="text-caption text-muted-foreground">{item.label}</p>
              <p className="text-title font-medium tabular-nums">{item.count}</p>
            </div>
          ))}
        </div>
      </SettingsCard>

      {report.secrets_to_fill.length > 0 ? (
        <ReportList
          title={t(($) => $.config_transfer.import.secrets_title)}
          items={report.secrets_to_fill.map(secretLabel)}
        />
      ) : null}

      {report.runtimes_to_bind.length > 0 ? (
        <ReportList
          title={t(($) => $.config_transfer.migration.runtimes_title)}
          items={report.runtimes_to_bind.map(runtimeLabel)}
        />
      ) : null}

      {report.export_gaps.length > 0 ? (
        <ReportList
          title={t(($) => $.config_transfer.migration.gaps_title)}
          items={report.export_gaps.map((gap) =>
            [gap.group, gap.reason].filter(Boolean).join(" · "),
          )}
        />
      ) : null}

      {showConfirm ? (
        <div className="flex justify-end">
          <Button type="button" disabled={applying} onClick={onConfirm}>
            {applying ? (
              <Loader2 className="size-4 animate-spin" aria-hidden="true" />
            ) : null}
            {applying
              ? t(($) => $.config_transfer.migration.applying)
              : t(($) => $.config_transfer.migration.confirm)}
          </Button>
        </div>
      ) : null}
    </div>
  );
}

function ReportList({ title, items }: { title: string; items: string[] }) {
  return (
    <div className="space-y-2">
      <h3 className="text-body font-semibold">{title}</h3>
      <SettingsCard>
        <ul className="divide-y divide-surface-border">
          {items.map((item, index) => (
            <li key={`${item}-${index}`} className="px-4 py-3 text-body">
              {item}
            </li>
          ))}
        </ul>
      </SettingsCard>
    </div>
  );
}
