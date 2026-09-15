"use client";

import { useRef, useState } from "react";
import { AlertCircle, Loader2 } from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  api,
  clientErrorMessage,
  CONFIG_BUNDLE_FORMAT,
  CONFIG_BUNDLE_SCHEMA_VERSION,
  configExportFilename,
  parseLocalConfigBundle,
  type ConfigImportErrorInfo,
  type ConfigImportItem,
  type ConfigImportReport,
  type ConfigOnConflict,
  type LocalConfigBundleError,
  type SecretOmitted,
  type SecretToFill,
} from "@multica/core/api";
import { autopilotKeys } from "@multica/core/autopilots/queries";
import { issueStatusKeys } from "@multica/core/issue-statuses/queries";
import { issueViewKeys } from "@multica/core/issue-views/queries";
import { labelKeys } from "@multica/core/labels/queries";
import { paths, useCurrentWorkspace } from "@multica/core/paths";
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { AppLink } from "../../navigation";
import { useT } from "../../i18n";
import {
  SettingsCard,
  SettingsRow,
  SettingsSection,
  SettingsTab,
} from "./settings-layout";

type ImportPhase =
  | { step: "idle" }
  | {
      step: "preview" | "result";
      fileName: string;
      bundle: unknown;
      report: ConfigImportReport;
      error?: ConfigImportErrorInfo;
    };

function downloadJson(value: unknown, filename: string) {
  const blob = new Blob([`${JSON.stringify(value, null, 2)}\n`], {
    type: "application/json",
  });
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

function secretLabel(item: SecretOmitted | SecretToFill): string {
  return [item.entity, item.name, item.field].filter(Boolean).join(" · ");
}

// The report's `path` follows the contract doc, but `/agents/:id/settings` and
// `/settings/mcp` are not routes in web or desktop. Build the href from the
// entity instead so the link lands on the tab where the secret is edited.
function secretFillHref(item: SecretToFill, slug: string): string | null {
  if (!slug) return null;
  const ws = paths.workspace(slug);
  switch (item.entity) {
    case "agent": {
      if (!item.target_id) return null;
      const view =
        item.field === "custom_env"
          ? "env"
          : item.field === "mcp_config"
            ? "mcp_config"
            : item.field === "custom_args"
              ? "custom_args"
              : item.field.startsWith("runtime_config")
              ? "runtime_config"
              : "general";
      return `${ws.agentDetail(item.target_id)}?view=${view}`;
    }
    case "workspace_mcp_server":
      return `${ws.settings()}?tab=mcp`;
    case "autopilot_trigger":
      return item.target_id ? ws.autopilotDetail(item.target_id) : null;
    default:
      return null;
  }
}

function conflictItems(report: ConfigImportReport): ConfigImportItem[] {
  return report.batches.flatMap((batch) =>
    batch.items.filter(
      (item) => item.action === "skipped" || item.action === "failed",
    ),
  );
}

export function ConfigTransferTab() {
  const { t } = useT("settings");
  const workspace = useCurrentWorkspace();
  const wsId = workspace?.id ?? "";
  const { role } = useCurrentMember(wsId);
  const canManage = role === "owner" || role === "admin";
  const qc = useQueryClient();
  const fileRef = useRef<HTMLInputElement>(null);

  const [exporting, setExporting] = useState(false);
  const [importBusy, setImportBusy] = useState(false);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [onConflict, setOnConflict] = useState<ConfigOnConflict>("skip");
  const [exportSecrets, setExportSecrets] = useState<SecretOmitted[] | null>(
    null,
  );
  const [exportError, setExportError] = useState<string | null>(null);
  const [fileError, setFileError] = useState<LocalConfigBundleError | null>(
    null,
  );
  const [importPhase, setImportPhase] = useState<ImportPhase>({ step: "idle" });

  const fileErrorMessage =
    fileError === "config_bundle_version_unsupported"
      ? t(($) => $.config_transfer.import.unsupported_version)
      : fileError
        ? t(($) => $.config_transfer.import.invalid_file)
        : null;

  function importErrorMessage(error?: ConfigImportErrorInfo): string | null {
    if (!error) return null;
    switch (error.code) {
      case "config_import_conflict":
        return t(($) => $.config_transfer.import.conflict_error);
      case "config_import_partial_failure":
        return t(($) => $.config_transfer.import.partial_failure);
      case "config_bundle_invalid":
        return t(($) => $.config_transfer.import.invalid_file);
      case "config_bundle_version_unsupported":
        return t(($) => $.config_transfer.import.unsupported_version);
      default:
        return error.message || t(($) => $.config_transfer.import.failed);
    }
  }

  function actionLabel(action: string): string {
    switch (action) {
      case "created":
        return t(($) => $.config_transfer.import.action_created);
      case "updated":
        return t(($) => $.config_transfer.import.action_updated);
      case "renamed":
        return t(($) => $.config_transfer.import.action_renamed);
      case "skipped":
        return t(($) => $.config_transfer.import.action_skipped);
      case "failed":
        return t(($) => $.config_transfer.import.action_failed);
      default:
        return action;
    }
  }

  function reasonLabel(reason: string): string {
    switch (reason) {
      case "exists":
        return t(($) => $.config_transfer.import.reason_exists);
      case "assignee_unmapped":
        return t(($) => $.config_transfer.import.reason_assignee_unmapped);
      case "leader_unmapped":
        return t(($) => $.config_transfer.import.reason_leader_unmapped);
      case "category_mismatch":
        return t(($) => $.config_transfer.import.reason_category_mismatch);
      case "type_mismatch":
        return t(($) => $.config_transfer.import.reason_type_mismatch);
      case "daemon_not_in_target":
        return t(($) => $.config_transfer.import.reason_daemon_not_in_target);
      case "db_error":
        return t(($) => $.config_transfer.import.reason_db_error);
      default:
        return reason;
    }
  }

  async function invalidateImportedQueries() {
    if (!wsId) return;
    await Promise.all([
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
    if (!wsId || !workspace) return;
    setExporting(true);
    setExportError(null);
    try {
      const bundle = await api.exportWorkspaceConfig(wsId);
      if (
        bundle.format !== CONFIG_BUNDLE_FORMAT ||
        bundle.schema_version !== CONFIG_BUNDLE_SCHEMA_VERSION
      ) {
        setExportError(t(($) => $.config_transfer.export.failed));
        toast.error(t(($) => $.config_transfer.export.failed));
        return;
      }
      downloadJson(bundle, configExportFilename(workspace.slug || "workspace"));
      setExportSecrets(bundle.secrets_omitted);
      toast.success(t(($) => $.config_transfer.export.success));
    } catch (err) {
      const message =
        clientErrorMessage(err) ?? t(($) => $.config_transfer.export.failed);
      setExportError(message);
      toast.error(message);
    } finally {
      setExporting(false);
    }
  }

  async function runDryRun(fileName: string, bundle: unknown, conflict: ConfigOnConflict) {
    if (!wsId) return;
    setImportBusy(true);
    setFileError(null);
    try {
      const result = await api.importWorkspaceConfig(wsId, {
        bundle,
        dry_run: true,
        on_conflict: conflict,
      });
      setImportPhase({
        step: "preview",
        fileName,
        bundle,
        report: result.report,
        error: result.error,
      });
    } catch (err) {
      setImportPhase({ step: "idle" });
      toast.error(
        clientErrorMessage(err) ?? t(($) => $.config_transfer.import.failed),
      );
    } finally {
      setImportBusy(false);
    }
  }

  async function handleFileChange(file: File | undefined) {
    if (!file) return;
    let raw: unknown;
    try {
      raw = JSON.parse(await file.text());
    } catch {
      setFileError("invalid_json");
      setImportPhase({ step: "idle" });
      return;
    }
    const parsed = parseLocalConfigBundle(raw);
    if (!parsed.ok) {
      setFileError(parsed.code);
      setImportPhase({ step: "idle" });
      return;
    }
    await runDryRun(file.name, raw, onConflict);
  }

  async function handleConflictChange(next: string) {
    const conflict = next as ConfigOnConflict;
    setOnConflict(conflict);
    if (importPhase.step === "preview") {
      await runDryRun(importPhase.fileName, importPhase.bundle, conflict);
    }
  }

  async function handleApply() {
    if (!wsId || importPhase.step !== "preview") return;
    setConfirmOpen(false);
    setImportBusy(true);
    try {
      const result = await api.importWorkspaceConfig(wsId, {
        bundle: importPhase.bundle,
        dry_run: false,
        on_conflict: onConflict,
      });
      setImportPhase({
        step: "result",
        fileName: importPhase.fileName,
        bundle: importPhase.bundle,
        report: result.report,
        error: result.error,
      });
      if (!result.error) {
        toast.success(t(($) => $.config_transfer.import.success));
      }
      // A partial failure still commits earlier batches, so refresh either way.
      await invalidateImportedQueries();
    } catch (err) {
      toast.error(
        clientErrorMessage(err) ?? t(($) => $.config_transfer.import.failed),
      );
    } finally {
      setImportBusy(false);
    }
  }

  const preview =
    importPhase.step === "preview" || importPhase.step === "result"
      ? importPhase
      : null;
  const stats = preview?.report.stats;
  const conflicts = preview ? conflictItems(preview.report) : [];

  return (
    <SettingsTab
      title={t(($) => $.config_transfer.title)}
      description={t(($) => $.config_transfer.description)}
    >
      {!canManage ? (
        <Alert>
          <AlertCircle />
          <AlertTitle>{t(($) => $.config_transfer.read_only)}</AlertTitle>
          <AlertDescription>
            {t(($) => $.config_transfer.read_only_description)}
          </AlertDescription>
        </Alert>
      ) : null}

      <SettingsSection
        title={t(($) => $.config_transfer.export.title)}
        description={t(($) => $.config_transfer.export.description)}
      >
        <SettingsCard>
          <SettingsRow
            label={t(($) => $.config_transfer.export.button)}
            description={t(($) => $.config_transfer.export.hint)}
          >
            <Button
              type="button"
              disabled={!canManage || exporting || !wsId}
              onClick={() => void handleExport()}
            >
              {exporting ? (
                <Loader2 className="size-4 animate-spin" aria-hidden="true" />
              ) : null}
              {exporting
                ? t(($) => $.config_transfer.export.exporting)
                : t(($) => $.config_transfer.export.button)}
            </Button>
          </SettingsRow>
          {exportError ? (
            <div className="px-4 py-3">
              <Alert variant="destructive">
                <AlertCircle />
                <AlertTitle>
                  {t(($) => $.config_transfer.export.failed)}
                </AlertTitle>
                <AlertDescription>{exportError}</AlertDescription>
              </Alert>
            </div>
          ) : null}
          {exportSecrets && exportSecrets.length > 0 ? (
            <div className="space-y-2 px-4 py-3">
              <p className="text-caption text-muted-foreground">
                {t(($) => $.config_transfer.export.secrets_title)}
              </p>
              <ul className="space-y-1">
                {exportSecrets.map((item, index) => (
                  <li
                    key={`${item.entity}-${item.field}-${item.source_id}-${index}`}
                    className="text-body text-foreground"
                  >
                    {secretLabel(item)}
                  </li>
                ))}
              </ul>
            </div>
          ) : null}
        </SettingsCard>
      </SettingsSection>

      <SettingsSection
        title={t(($) => $.config_transfer.import.title)}
        description={t(($) => $.config_transfer.import.description)}
      >
        <input
          ref={fileRef}
          type="file"
          accept="application/json,.json"
          className="sr-only"
          data-testid="config-transfer-file"
          disabled={!canManage || importBusy}
          onChange={(event) => {
            const file = event.target.files?.[0];
            event.target.value = "";
            void handleFileChange(file);
          }}
        />
        <SettingsCard>
          <SettingsRow
            label={t(($) => $.config_transfer.import.choose_file)}
            description={
              preview
                ? preview.fileName
                : t(($) => $.config_transfer.import.choose_hint)
            }
          >
            <Button
              type="button"
              variant="outline"
              disabled={!canManage || importBusy || !wsId}
              onClick={() => fileRef.current?.click()}
            >
              {importBusy && importPhase.step !== "result" ? (
                <Loader2 className="size-4 animate-spin" aria-hidden="true" />
              ) : null}
              {importBusy && importPhase.step !== "result"
                ? t(($) => $.config_transfer.import.previewing)
                : preview
                  ? t(($) => $.config_transfer.import.replace_file)
                  : t(($) => $.config_transfer.import.choose_file)}
            </Button>
          </SettingsRow>
        </SettingsCard>

        {fileErrorMessage ? (
          <Alert variant="destructive">
            <AlertCircle />
            <AlertTitle>{t(($) => $.config_transfer.import.failed)}</AlertTitle>
            <AlertDescription>{fileErrorMessage}</AlertDescription>
          </Alert>
        ) : null}

        {preview ? (
          <div className="space-y-4">
            {preview.error ? (
              <Alert variant="destructive">
                <AlertCircle />
                <AlertTitle>
                  {t(($) => $.config_transfer.import.failed)}
                </AlertTitle>
                <AlertDescription>
                  {importErrorMessage(preview.error)}
                </AlertDescription>
              </Alert>
            ) : null}

            <SettingsCard>
              <div className="grid grid-cols-2 gap-3 px-4 py-4 sm:grid-cols-5">
                {(
                  [
                    {
                      key: "created",
                      label: t(($) => $.config_transfer.import.stats_created),
                      count: stats?.created ?? 0,
                    },
                    {
                      key: "updated",
                      label: t(($) => $.config_transfer.import.stats_updated),
                      count: stats?.updated ?? 0,
                    },
                    {
                      key: "renamed",
                      label: t(($) => $.config_transfer.import.stats_renamed),
                      count: stats?.renamed ?? 0,
                    },
                    {
                      key: "skipped",
                      label: t(($) => $.config_transfer.import.stats_skipped),
                      count: stats?.skipped ?? 0,
                    },
                    {
                      key: "failed",
                      label: t(($) => $.config_transfer.import.stats_failed),
                      count: stats?.failed ?? 0,
                    },
                  ] as const
                ).map((item) => (
                  <div key={item.key}>
                    <p className="text-caption text-muted-foreground">
                      {item.label}
                    </p>
                    <p className="text-title font-medium tabular-nums">
                      {item.count}
                    </p>
                  </div>
                ))}
              </div>
            </SettingsCard>

            {importPhase.step === "preview" ? (
              <SettingsCard>
                <SettingsRow
                  label={t(($) => $.config_transfer.import.on_conflict)}
                  description={t(
                    ($) => $.config_transfer.import.on_conflict_hint,
                  )}
                  size="select-wide"
                >
                  <Select
                    items={[
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
                        label: t(
                          ($) => $.config_transfer.import.on_conflict_rename,
                        ),
                      },
                      {
                        value: "overwrite",
                        label: t(
                          ($) => $.config_transfer.import.on_conflict_overwrite,
                        ),
                      },
                    ]}
                    value={onConflict}
                    onValueChange={(value) => {
                      if (value) void handleConflictChange(String(value));
                    }}
                    disabled={importBusy}
                  >
                    <SelectTrigger
                      aria-label={t(
                        ($) => $.config_transfer.import.on_conflict,
                      )}
                    >
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="fail">
                        {t(($) => $.config_transfer.import.on_conflict_fail)}
                      </SelectItem>
                      <SelectItem value="skip">
                        {t(($) => $.config_transfer.import.on_conflict_skip)}
                      </SelectItem>
                      <SelectItem value="rename">
                        {t(($) => $.config_transfer.import.on_conflict_rename)}
                      </SelectItem>
                      <SelectItem value="overwrite">
                        {t(
                          ($) => $.config_transfer.import.on_conflict_overwrite,
                        )}
                      </SelectItem>
                    </SelectContent>
                  </Select>
                </SettingsRow>
              </SettingsCard>
            ) : null}

            {conflicts.length > 0 ? (
              <div className="space-y-2">
                <h3 className="text-body font-semibold">
                  {t(($) => $.config_transfer.import.conflicts_title)}
                </h3>
                <SettingsCard>
                  <ul className="divide-y divide-surface-border">
                    {conflicts.map((item, index) => (
                      <li
                        key={`${item.source_id}-${item.name}-${index}`}
                        className="px-4 py-3 text-body"
                      >
                        <span className="font-medium">{item.name}</span>
                        <span className="text-muted-foreground">
                          {" · "}
                          {actionLabel(item.action)}
                          {item.reason
                            ? ` · ${reasonLabel(item.reason)}`
                            : ""}
                        </span>
                      </li>
                    ))}
                  </ul>
                </SettingsCard>
              </div>
            ) : null}

            {preview.report.unmapped_refs.length > 0 ? (
              <div className="space-y-2">
                <h3 className="text-body font-semibold">
                  {t(($) => $.config_transfer.import.unmapped_title)}
                </h3>
                <SettingsCard>
                  <ul className="divide-y divide-surface-border">
                    {preview.report.unmapped_refs.map((item, index) => (
                      <li
                        key={`${item.entity}-${item.field}-${item.ref_id}-${index}`}
                        className="px-4 py-3 text-body text-muted-foreground"
                      >
                        {item.entity} · {item.field} · {item.resolution}
                      </li>
                    ))}
                  </ul>
                </SettingsCard>
              </div>
            ) : null}

            {preview.report.secrets_to_fill.length > 0 ? (
              <div className="space-y-2">
                <h3 className="text-body font-semibold">
                  {t(($) => $.config_transfer.import.secrets_title)}
                </h3>
                <SettingsCard>
                  <ul className="divide-y divide-surface-border">
                    {preview.report.secrets_to_fill.map((item, index) => {
                      const href = secretFillHref(item, workspace?.slug ?? "");
                      return (
                      <li
                        key={`${item.entity}-${item.field}-${item.target_id}-${index}`}
                        className="px-4 py-3 text-body"
                      >
                        {href ? (
                          <AppLink
                            href={href}
                            className="font-medium text-foreground hover:underline"
                          >
                            {secretLabel(item)}
                          </AppLink>
                        ) : (
                          secretLabel(item)
                        )}
                      </li>
                      );
                    })}
                  </ul>
                </SettingsCard>
              </div>
            ) : null}

            {preview.report.warnings.length > 0 ? (
              <div className="space-y-2">
                <h3 className="text-body font-semibold">
                  {t(($) => $.config_transfer.import.warnings_title)}
                </h3>
                <SettingsCard>
                  <ul className="divide-y divide-surface-border">
                    {preview.report.warnings.map((warning, index) => (
                      <li
                        key={`${warning.code}-${index}`}
                        className="px-4 py-3 text-body text-muted-foreground"
                      >
                        {warning.code === "autopilots_imported_paused"
                          ? t(
                              ($) =>
                                $.config_transfer.import
                                  .warning_autopilots_paused,
                              { count: warning.count },
                            )
                          : warning.code === "issue_prefix_skipped_target_has_issues"
                            ? t(
                                ($) =>
                                  $.config_transfer.import
                                    .warning_issue_prefix_skipped,
                              )
                            : warning.code}
                      </li>
                    ))}
                  </ul>
                </SettingsCard>
              </div>
            ) : null}

            {importPhase.step === "preview" ? (
              <div className="flex justify-end">
                <Button
                  type="button"
                  disabled={importBusy || Boolean(preview.error)}
                  onClick={() => setConfirmOpen(true)}
                >
                  {importBusy ? (
                    <Loader2 className="size-4 animate-spin" aria-hidden="true" />
                  ) : null}
                  {importBusy
                    ? t(($) => $.config_transfer.import.applying)
                    : t(($) => $.config_transfer.import.confirm)}
                </Button>
              </div>
            ) : null}
          </div>
        ) : null}
      </SettingsSection>

      <AlertDialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.config_transfer.import.confirm_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.config_transfer.import.confirm_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>
              {t(($) => $.config_transfer.import.cancel)}
            </AlertDialogCancel>
            <AlertDialogAction onClick={() => void handleApply()}>
              {t(($) => $.config_transfer.import.confirm)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsTab>
  );
}
