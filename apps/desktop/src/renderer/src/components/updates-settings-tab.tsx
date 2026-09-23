import { useCallback, useEffect, useState } from "react";
import {
  AlertCircle,
  ArrowDownToLine,
  Check,
  ExternalLink,
  FileText,
  Loader2,
  RefreshCw,
} from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Switch } from "@multica/ui/components/ui/switch";
import { useT } from "@multica/views/i18n";
import { SettingsCard, SettingsRow, SettingsTab } from "@multica/views/settings";
import { toast } from "sonner";
import { useUpdater } from "../hooks/use-updater";
import { DownloadProgressBar } from "./update-notification";
import type {
  ReleaseChannel,
  UpdateCheckRecord,
} from "../../../shared/updater-types";

function formatCheckedAt(iso: string): string {
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? iso : date.toLocaleString();
}

export function UpdatesSettingsTab() {
  const { t } = useT("settings");
  const {
    phase,
    lastCheck,
    capabilities,
    autoUpdateSupported,
    check,
    download,
    install,
    openReleasePage,
    openLogFile,
  } = useUpdater();
  const [automaticUpdates, setAutomaticUpdates] = useState(true);
  const [releaseChannel, setReleaseChannel] = useState<ReleaseChannel>("stable");
  const [preferencesReady, setPreferencesReady] = useState(false);
  const [savingPreference, setSavingPreference] = useState(false);
  const currentVersion = window.desktopAPI.appInfo.version;

  useEffect(() => {
    let mounted = true;
    void window.updater
      .getPreferences()
      .then((preferences) => {
        if (!mounted) return;
        setAutomaticUpdates(preferences.automaticUpdates);
        setReleaseChannel(preferences.releaseChannel);
      })
      .catch(() => {
        // The main process falls back to enabled when preferences cannot be
        // read. Keep the same safe default if IPC itself becomes unavailable.
      })
      .finally(() => {
        if (mounted) setPreferencesReady(true);
      });

    return () => {
      mounted = false;
    };
  }, []);

  const handleAutomaticUpdatesChange = useCallback(
    async (enabled: boolean) => {
      setSavingPreference(true);
      try {
        const preferences = await window.updater.setAutomaticUpdates(enabled);
        setAutomaticUpdates(preferences.automaticUpdates);
        toast.success(t(($) => $.auto_save.toast_saved), {
          id: "settings-auto-save",
        });
      } catch {
        toast.error(t(($) => $.desktop.updates.automatic_updates_save_failed));
      } finally {
        setSavingPreference(false);
      }
    },
    [t],
  );

  const handleReleaseChannelChange = useCallback(
    async (value: string) => {
      if (value !== "stable" && value !== "test") return;
      if (value === releaseChannel) return;
      setSavingPreference(true);
      try {
        // The main process re-checks the newly selected feed itself and
        // broadcasts the outcome, so there is nothing to trigger here — the
        // rows below are already driven by those events.
        const preferences = await window.updater.setReleaseChannel(value);
        setReleaseChannel(preferences.releaseChannel);
        setAutomaticUpdates(preferences.automaticUpdates);
        toast.success(t(($) => $.auto_save.toast_saved), {
          id: "settings-auto-save",
        });
      } catch {
        toast.error(t(($) => $.desktop.updates.release_channel_save_failed));
      } finally {
        setSavingPreference(false);
      }
    },
    [releaseChannel, t],
  );

  const describeLastCheck = (record: UpdateCheckRecord): string => {
    const trigger = t(($) => $.desktop.updates.last_check_trigger[record.trigger]);
    const outcome = !record.ok
      ? t(($) => $.desktop.updates.last_check_failed, { error: record.error })
      : record.available
        ? t(($) => $.desktop.updates.last_check_available, {
            version: record.latestVersion,
          })
        : t(($) => $.desktop.updates.last_check_up_to_date);
    return `${formatCheckedAt(record.checkedAt)} · ${trigger} · ${outcome}`;
  };

  const manualOnly = !autoUpdateSupported;
  const checking = phase.status === "checking";

  return (
    <SettingsTab
      title={t(($) => $.desktop.updates.title)}
    >
      <SettingsCard>
        <SettingsRow label={t(($) => $.desktop.updates.current_version)}>
          <span className="font-mono text-caption text-muted-foreground">
            v{currentVersion}
          </span>
        </SettingsRow>

        <SettingsRow
          label={t(($) => $.desktop.updates.automatic_updates_title)}
          description={t(($) => $.desktop.updates.automatic_updates_description)}
        >
          <Switch
            checked={automaticUpdates}
            onCheckedChange={handleAutomaticUpdatesChange}
            disabled={!preferencesReady || savingPreference}
            aria-label={t(($) => $.desktop.updates.automatic_updates_title)}
          />
        </SettingsRow>

        <SettingsRow
          label={t(($) => $.desktop.updates.release_channel_title)}
          description={t(($) => $.desktop.updates.release_channel_description)}
          size="select-wide"
        >
          <select
            aria-label={t(($) => $.desktop.updates.release_channel_title)}
            className="h-9 w-full rounded-lg border border-input bg-background px-2 text-body text-foreground focus-visible:outline-2 focus-visible:outline-ring"
            value={releaseChannel}
            disabled={!preferencesReady || savingPreference}
            onChange={(event) => {
              void handleReleaseChannelChange(event.target.value);
            }}
          >
            <option value="stable">
              {t(($) => $.desktop.updates.release_channel_stable)}
            </option>
            <option value="test">
              {t(($) => $.desktop.updates.release_channel_test)}
            </option>
          </select>
        </SettingsRow>

        {manualOnly && (
          <SettingsRow
            label={t(($) => $.desktop.updates.manual_only_title)}
            description={t(($) => $.desktop.updates.manual_only_description)}
            align="start"
          >
            <Button variant="outline" size="sm" onClick={() => void openReleasePage()}>
              <ExternalLink className="size-3.5" />
              {t(($) => $.desktop.updates.open_release_page)}
            </Button>
          </SettingsRow>
        )}

        <SettingsRow
          label={t(($) => $.desktop.updates.check_section_title)}
          align="start"
          description={
            <>
              <p>{t(($) => $.desktop.updates.check_section_description)}</p>
              <p
                className="mt-2 text-muted-foreground"
                data-testid="updater-last-check"
              >
                {t(($) => $.desktop.updates.last_check_label)}:{" "}
                {lastCheck
                  ? describeLastCheck(lastCheck)
                  : t(($) => $.desktop.updates.last_check_never)}
              </p>
              {phase.status === "up-to-date" && (
                <p className="mt-2 inline-flex items-center gap-1.5">
                  <Check className="size-3.5 text-success" />
                  {t(($) => $.desktop.updates.up_to_date)}
                </p>
              )}
              {phase.status === "available" && (
                <p className="mt-2 inline-flex items-center gap-1.5">
                  <ArrowDownToLine className="size-3.5 text-primary" />
                  {manualOnly
                    ? t(($) => $.desktop.updates.available_manual, {
                        version: phase.version,
                      })
                    : t(($) => $.desktop.updates.available, {
                        version: phase.version,
                      })}
                </p>
              )}
              {phase.status === "downloading" && (
                <div className="mt-2 flex flex-col gap-1.5">
                  <p className="inline-flex items-center gap-1.5">
                    <ArrowDownToLine className="size-3.5 text-primary" />
                    {t(($) => $.desktop.updates.downloading_progress, {
                      version: phase.version,
                      percent: String(Math.round(phase.percent)),
                    })}
                  </p>
                  <DownloadProgressBar percent={phase.percent} />
                </div>
              )}
              {phase.status === "downloaded" && (
                <p className="mt-2 inline-flex items-center gap-1.5">
                  <RefreshCw className="size-3.5 text-success" />
                  {t(($) => $.desktop.updates.downloaded, {
                    version: phase.version,
                  })}
                </p>
              )}
              {phase.status === "error" && (
                <p className="mt-2 inline-flex items-center gap-1.5 text-destructive">
                  <AlertCircle className="size-3.5" />
                  {t(($) => $.desktop.updates.failed, { error: phase.message })}
                </p>
              )}
            </>
          }
        >
          <div className="flex flex-col items-end gap-1.5">
            <Button
              variant="outline"
              size="sm"
              onClick={() => void check()}
              disabled={checking}
            >
              {checking ? (
                <>
                  <Loader2 className="size-3.5 animate-spin" />
                  {t(($) => $.desktop.updates.checking)}
                </>
              ) : (
                t(($) => $.desktop.updates.check_now)
              )}
            </Button>
            {phase.status === "available" && !manualOnly && (
              <Button size="sm" onClick={() => void download()}>
                <ArrowDownToLine className="size-3.5" />
                {t(($) => $.desktop.updates.download)}
              </Button>
            )}
            {phase.status === "available" && manualOnly && (
              <Button size="sm" onClick={() => void openReleasePage()}>
                <ExternalLink className="size-3.5" />
                {t(($) => $.desktop.updates.open_release_page)}
              </Button>
            )}
            {phase.status === "error" && phase.version && !manualOnly && (
              <Button size="sm" onClick={() => void download()}>
                <ArrowDownToLine className="size-3.5" />
                {t(($) => $.desktop.updates.retry_download)}
              </Button>
            )}
            {phase.status === "downloaded" && (
              <Button size="sm" onClick={() => void install()}>
                <RefreshCw className="size-3.5" />
                {t(($) => $.desktop.updates.restart_now)}
              </Button>
            )}
          </div>
        </SettingsRow>

        {capabilities?.logPath && (
          <SettingsRow
            label={t(($) => $.desktop.updates.log_title)}
            description={
              <span className="font-mono break-all">{capabilities.logPath}</span>
            }
            align="start"
          >
            <Button variant="outline" size="sm" onClick={() => void openLogFile()}>
              <FileText className="size-3.5" />
              {t(($) => $.desktop.updates.open_log)}
            </Button>
          </SettingsRow>
        )}
      </SettingsCard>
    </SettingsTab>
  );
}
