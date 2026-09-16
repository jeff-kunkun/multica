"use client";

import { useEffect, useRef, useState } from "react";
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
import { api } from "@multica/core/api";
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
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { useT } from "../../i18n";
import {
  fileNameFromPath,
  formatTransferBytes,
  isDesktopShell,
  markTransferExportCompleted,
  pickTransferExportPath,
  pickTransferImportPath,
  runWorkspaceTransfer,
  subscribeTransferProgress,
  transferExportSourceHost,
  type TransferErrorCode,
  type TransferImportOptions,
  type TransferImportReportView,
  type TransferProgressEvent,
  type TransferRuntimeBind,
  type TransferRuntimeCandidate,
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

/**
 * How an import treats a name that already exists in the target workspace.
 * `skip` is the default: every workspace ships issue statuses, labels and
 * system agents, so `fail` (the CLI flag's own default) makes the first dry-run
 * of any real bundle 409 before the user can even read a preview (DENE-318).
 */
type TransferConflictPolicy = "skip" | "overwrite" | "rename" | "fail";

/**
 * Config-import switches the V2 card used to drop, which is why imported
 * automations all landed paused (DENE-363). A cross-environment import is meant
 * to reproduce the same environment, so automations arrive running and the
 * workspace settings land; the issue prefix stays untouched because adopting it
 * changes the key of every future issue in the target.
 */
const DEFAULT_IMPORT_OPTIONS: TransferImportOptions = {
  activateAutopilots: true,
  applyWorkspaceSettings: true,
  applyIssuePrefix: false,
  // On by default: the unique-candidate rule already refuses to guess, and an
  // import that binds nothing leaves every migrated agent unable to run
  // (DENE-364).
  autoBindRuntimes: true,
};

/** The transfer in flight, as the card's buttons need to describe it. */
type TransferAction = "export" | "import-preview" | "import-apply";

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
  // Which transfer is in flight. Preview and apply are told apart so the
  // import button never says "applying" while it is only re-running a preview
  // after a conflict-policy change (DENE-318).
  const [activeAction, setActiveAction] = useState<TransferAction | null>(null);
  const [onConflict, setOnConflict] = useState<TransferConflictPolicy>("skip");
  const [importOptions, setImportOptions] = useState<TransferImportOptions>(
    DEFAULT_IMPORT_OPTIONS,
  );
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
  // `busy` re-renders too late to swallow a double click, so the same guard
  // lives in a ref: the second click of a double click is a no-op instead of
  // reaching the main process and coming back as "a transfer is already
  // running" (DENE-318).
  const busyRef = useRef(false);

  useEffect(() => {
    return subscribeTransferProgress((event) => setProgress(event));
  }, []);

  if (!isDesktopShell()) return null;

  function startTransfer(action: TransferAction): boolean {
    if (busyRef.current) return false;
    busyRef.current = true;
    setActiveAction(action);
    setBusy(true);
    return true;
  }

  function finishTransfer() {
    busyRef.current = false;
    setActiveAction(null);
    setBusy(false);
    setProgress(null);
  }

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
    if (!slug || !canManage || busyRef.current) return;
    setError(null);
    setExportResult(null);
    const picked = await pickTransferExportPath(slug);
    if (!picked.ok) return;
    if (!startTransfer("export")) return;
    setProgress({ phase: "estimating" });
    try {
      const result = await runWorkspaceTransfer({
        action: "export",
        workspace: slug,
        outPath: picked.path,
      });
      if (!result.ok) {
        // The main process refuses a second transfer while one is running.
        // That is the same click, not a failure, so stay quiet.
        if (result.code === "busy") return;
        setError({ code: result.code, fallback: result.message });
        toast.error(errorMessage(result.code, result.message));
        return;
      }
      if (result.action !== "export") return;
      setExportResult({ path: result.outPath, bytes: result.bytes });
      markTransferExportCompleted();
      toast.success(t(($) => $.config_transfer.migration.export_success));
    } finally {
      finishTransfer();
    }
  }

  async function handleImportPick() {
    if (!slug || !canManage || busyRef.current) return;
    setError(null);
    const picked = await pickTransferImportPath();
    if (!picked.ok) return;
    await runImportPreview(picked.path, picked.fileName, onConflict);
  }

  async function runImportPreview(
    inPath: string,
    fileName: string,
    conflict: TransferConflictPolicy,
  ) {
    if (!slug || !startTransfer("import-preview")) return;
    setError(null);
    setProgress({ phase: "estimating" });
    try {
      const result = await runWorkspaceTransfer({
        action: "import",
        workspace: slug,
        inPath,
        dryRun: true,
        onConflict: conflict,
        options: importOptions,
      });
      if (!result.ok) {
        if (result.code === "busy") return;
        setImportPhase({ step: "idle" });
        setError({ code: result.code, fallback: result.message });
        toast.error(errorMessage(result.code, result.message));
        return;
      }
      if (result.action !== "import") return;
      setImportPhase({ step: "preview", fileName, inPath, report: result.report });
    } finally {
      finishTransfer();
    }
  }

  async function handleConflictChange(next: TransferConflictPolicy) {
    setOnConflict(next);
    // The preview counts depend on the policy (a skip turns a conflict into a
    // skipped row), so refresh the report the user is reading.
    if (importPhase.step === "preview") {
      await runImportPreview(importPhase.inPath, importPhase.fileName, next);
    }
  }

  async function handleApply() {
    if (!slug || importPhase.step !== "preview") return;
    setConfirmOpen(false);
    if (!startTransfer("import-apply")) return;
    setProgress({ phase: "running" });
    try {
      const result = await runWorkspaceTransfer({
        action: "import",
        workspace: slug,
        inPath: importPhase.inPath,
        dryRun: false,
        onConflict,
        options: importOptions,
      });
      if (!result.ok) {
        if (result.code === "busy") return;
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
      finishTransfer();
    }
  }

  const conflictItems = [
    {
      value: "fail",
      label: t(($) => $.config_transfer.import.on_conflict_fail),
    },
    {
      value: "skip",
      label: t(($) => $.config_transfer.import.on_conflict_skip),
    },
    {
      value: "rename",
      label: t(($) => $.config_transfer.import.on_conflict_rename),
    },
    {
      value: "overwrite",
      label: t(($) => $.config_transfer.import.on_conflict_overwrite),
    },
  ];

  const report =
    importPhase.step === "preview" || importPhase.step === "result"
      ? importPhase.report
      : null;
  const sourceHost =
    transferExportSourceHost(api.getBaseUrl?.() ?? "") || "—";
  const workspaceName = workspace?.name?.trim() || slug;

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
            {activeAction === "export" ? (
              <Loader2 className="size-4 animate-spin" aria-hidden="true" />
            ) : null}
            {activeAction === "export"
              ? t(($) => $.config_transfer.migration.exporting)
              : t(($) => $.config_transfer.migration.export_button)}
          </Button>
        </SettingsRow>

        <div
          className="space-y-1 px-4 py-3"
          data-testid="workspace-migration-export-source"
        >
          <p className="text-caption text-muted-foreground">
            {t(($) => $.config_transfer.migration.export_source, {
              host: sourceHost,
              workspace: workspaceName,
            })}
          </p>
          <p className="text-caption text-muted-foreground">
            {t(($) => $.config_transfer.migration.export_order_hint)}
          </p>
        </div>

        {progress ? (
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
            {activeAction === "import-preview" || activeAction === "import-apply" ? (
              <Loader2 className="size-4 animate-spin" aria-hidden="true" />
            ) : null}
            {activeAction === "import-apply"
              ? t(($) => $.config_transfer.migration.applying)
              : activeAction === "import-preview"
                ? t(($) => $.config_transfer.migration.previewing)
              : importPhase.step === "idle"
                ? t(($) => $.config_transfer.migration.import_button)
                : t(($) => $.config_transfer.import.replace_file)}
          </Button>
        </SettingsRow>

        <SettingsRow
          label={t(($) => $.config_transfer.import.on_conflict)}
          description={t(($) => $.config_transfer.import.on_conflict_hint)}
          size="select-wide"
        >
          <Select
            items={conflictItems}
            value={onConflict}
            onValueChange={(value) => {
              if (value) {
                void handleConflictChange(String(value) as TransferConflictPolicy);
              }
            }}
            disabled={!canManage || busy || !slug}
          >
            <SelectTrigger
              aria-label={t(($) => $.config_transfer.import.on_conflict)}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {conflictItems.map((item) => (
                <SelectItem key={item.value} value={item.value}>
                  {item.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </SettingsRow>

        <SettingsRow
          label={t(($) => $.config_transfer.migration.option_activate_autopilots)}
          description={t(
            ($) => $.config_transfer.migration.option_activate_autopilots_hint,
          )}
        >
          <Checkbox
            aria-label={t(
              ($) => $.config_transfer.migration.option_activate_autopilots,
            )}
            checked={importOptions.activateAutopilots}
            disabled={!canManage || busy || !slug}
            onCheckedChange={(checked) =>
              setImportOptions((current) => ({
                ...current,
                activateAutopilots: checked === true,
              }))
            }
          />
        </SettingsRow>

        <SettingsRow
          label={t(($) => $.config_transfer.migration.option_apply_workspace_settings)}
          description={t(
            ($) => $.config_transfer.migration.option_apply_workspace_settings_hint,
          )}
        >
          <Checkbox
            aria-label={t(
              ($) => $.config_transfer.migration.option_apply_workspace_settings,
            )}
            checked={importOptions.applyWorkspaceSettings}
            disabled={!canManage || busy || !slug}
            onCheckedChange={(checked) =>
              setImportOptions((current) => ({
                ...current,
                applyWorkspaceSettings: checked === true,
              }))
            }
          />
        </SettingsRow>

        <SettingsRow
          label={t(($) => $.config_transfer.migration.option_apply_issue_prefix)}
          description={t(
            ($) => $.config_transfer.migration.option_apply_issue_prefix_hint,
          )}
        >
          <Checkbox
            aria-label={t(
              ($) => $.config_transfer.migration.option_apply_issue_prefix,
            )}
            checked={importOptions.applyIssuePrefix}
            disabled={!canManage || busy || !slug}
            onCheckedChange={(checked) =>
              setImportOptions((current) => ({
                ...current,
                applyIssuePrefix: checked === true,
              }))
            }
          />
        </SettingsRow>

        <SettingsRow
          label={t(($) => $.config_transfer.migration.option_auto_bind_runtimes)}
          description={t(
            ($) => $.config_transfer.migration.option_auto_bind_runtimes_hint,
          )}
        >
          <Checkbox
            aria-label={t(
              ($) => $.config_transfer.migration.option_auto_bind_runtimes,
            )}
            checked={importOptions.autoBindRuntimes}
            disabled={!canManage || busy || !slug}
            onCheckedChange={(checked) =>
              setImportOptions((current) => ({
                ...current,
                autoBindRuntimes: checked === true,
              }))
            }
          />
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
          activateAutopilots={importOptions.activateAutopilots}
          applied={importPhase.step === "result"}
          onRuntimeBound={invalidateImportedQueries}
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
  activateAutopilots,
  applied,
  onRuntimeBound,
  showConfirm,
  applying,
  onConfirm,
}: {
  report: TransferImportReportView;
  activateAutopilots: boolean;
  /** False while this is still the preview, where nothing has been written. */
  applied: boolean;
  onRuntimeBound: () => void | Promise<void>;
  showConfirm: boolean;
  applying: boolean;
  onConfirm: () => void;
}) {
  const { t } = useT("settings");
  const stats = report.stats;
  // Reports from a Desktop build older than the option switches carry no
  // summary; showing nothing beats showing a made-up zero.
  const autopilots = report.autopilots;
  // An import without activation writes every automation paused, so the switch
  // the user is about to apply with — not the run that produced the preview —
  // is what decides this count.
  const pausedAutopilots =
    autopilots && !activateAutopilots ? autopilots.imported : 0;
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

      {autopilots && autopilots.imported > 0 ? (
        <div
          className="space-y-1 px-4"
          data-testid="workspace-migration-autopilots"
        >
          <p className="text-body text-foreground">
            {pausedAutopilots > 0
              ? t(($) => $.config_transfer.migration.autopilots_paused, {
                  imported: autopilots.imported,
                  paused: pausedAutopilots,
                })
              : t(($) => $.config_transfer.migration.autopilots_active, {
                  imported: autopilots.imported,
                })}
          </p>
          {pausedAutopilots > 0 ? (
            <p className="text-caption leading-5 text-muted-foreground">
              {t(($) => $.config_transfer.migration.autopilots_activate_hint)}
            </p>
          ) : null}
        </div>
      ) : null}

      {report.secrets_to_fill.length > 0 ? (
        <ReportList
          title={t(($) => $.config_transfer.import.secrets_title)}
          items={report.secrets_to_fill.map(secretLabel)}
        />
      ) : null}

      {report.runtimes_to_bind.length > 0 ? (
        <RuntimeBindSection
          binds={report.runtimes_to_bind}
          applied={applied}
          onBound={onRuntimeBound}
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

/**
 * The post-import runtime binding block (DENE-364).
 *
 * The server binds an agent only when exactly one target runtime matches its
 * source runtime — binding decides whose machine and account the agent runs on,
 * so anything ambiguous lands here for the operator to settle in one pass
 * instead of a dialog per agent. Rows with nothing to bind to say why.
 */
function RuntimeBindSection({
  binds,
  applied,
  onBound,
}: {
  binds: TransferRuntimeBind[];
  applied: boolean;
  onBound: () => void | Promise<void>;
}) {
  const { t } = useT("settings");
  const [choices, setChoices] = useState<Record<string, string>>({});
  const [bound, setBound] = useState<Record<string, string>>({});
  const [pending, setPending] = useState<string | null>(null);

  async function applyBind(bind: TransferRuntimeBind, runtimeId: string) {
    if (!bind.agent_target_id || !runtimeId) return;
    setPending(bind.agent_target_id);
    try {
      await api.updateAgent(bind.agent_target_id, { runtime_id: runtimeId });
      const name =
        bind.candidates?.find((c: TransferRuntimeCandidate) => c.id === runtimeId)
          ?.name ?? runtimeId;
      setBound((current) => ({ ...current, [bind.agent_target_id]: name }));
      await onBound();
    } catch {
      toast.error(t(($) => $.config_transfer.migration.runtimes_bind_failed));
    } finally {
      setPending(null);
    }
  }

  return (
    <div className="space-y-2" data-testid="workspace-migration-runtimes">
      <h3 className="text-body font-semibold">
        {t(($) => $.config_transfer.migration.runtimes_title)}
      </h3>
      <SettingsCard>
        <ul className="divide-y divide-surface-border">
          {binds.map((bind, index) => {
            const key = bind.agent_target_id || `${bind.agent_name}-${index}`;
            const manualName = bound[bind.agent_target_id];
            const candidates = bind.candidates ?? [];
            const selected = choices[key] ?? candidates[0]?.id ?? "";
            const boundName = manualName ?? bind.bound_runtime_name;
            return (
              <li key={key} className="space-y-2 px-4 py-3">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <span className="text-body">{runtimeLabel(bind)}</span>
                  {boundName ? (
                    <span className="text-caption text-muted-foreground">
                      {manualName || applied
                        ? t(($) => $.config_transfer.migration.runtimes_bound, {
                            runtime: boundName,
                          })
                        : t(($) => $.config_transfer.migration.runtimes_will_bind, {
                            runtime: boundName,
                          })}
                    </span>
                  ) : null}
                </div>

                {!boundName && candidates.length > 0 ? (
                  <div className="flex flex-wrap items-center gap-2">
                    <Select
                      items={candidates.map((candidate) => ({
                        value: candidate.id,
                        label: candidateLabel(candidate),
                      }))}
                      value={selected}
                      onValueChange={(value) =>
                        setChoices((current) => ({
                          ...current,
                          [key]: String(value),
                        }))
                      }
                    >
                      <SelectTrigger
                        className="w-64"
                        aria-label={t(
                          ($) => $.config_transfer.migration.runtimes_choose,
                        )}
                      >
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {candidates.map((candidate: TransferRuntimeCandidate) => (
                          <SelectItem key={candidate.id} value={candidate.id}>
                            {candidateLabel(candidate)}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      // Binding writes to the target, so it only makes sense
                      // once the import itself has been applied.
                      disabled={!applied || pending === bind.agent_target_id}
                      onClick={() => void applyBind(bind, selected)}
                    >
                      {pending === bind.agent_target_id ? (
                        <Loader2 className="size-4 animate-spin" aria-hidden="true" />
                      ) : null}
                      {t(($) => $.config_transfer.migration.runtimes_bind)}
                    </Button>
                  </div>
                ) : null}

                {!boundName && candidates.length === 0 ? (
                  <p className="text-caption leading-5 text-muted-foreground">
                    {runtimeBindReason(bind.reason)}
                  </p>
                ) : null}
              </li>
            );
          })}
        </ul>
      </SettingsCard>
    </div>
  );

  function candidateLabel(candidate: TransferRuntimeCandidate): string {
    return [candidate.name, candidate.provider, candidate.runtime_mode]
      .filter(Boolean)
      .join(" · ");
  }

  function runtimeBindReason(reason: string | undefined): string {
    switch (reason) {
      case "no_visible_runtime":
        return t(($) => $.config_transfer.migration.runtimes_reason_none_visible);
      case "auto_bind_disabled":
        return t(($) => $.config_transfer.migration.runtimes_reason_disabled);
      case "unknown_source_runtime":
        return t(($) => $.config_transfer.migration.runtimes_reason_unknown_source);
      default:
        return t(($) => $.config_transfer.migration.runtimes_reason_no_match);
    }
  }
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
