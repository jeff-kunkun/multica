import { useCallback, useEffect, useState } from "react";
import { AlertCircle, Check } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Switch } from "@multica/ui/components/ui/switch";
import { useT } from "@multica/views/i18n";
import { SettingsCard, SettingsRow, SettingsTab } from "@multica/views/settings";
import { toast } from "sonner";
import type { UpdateCheckRecord, UpdateCheckSource } from "../../../shared/updater-types";
import { UpdateProgressBar } from "./update-progress";
import {
  canOfferInAppDownload,
  shouldOfferReleasePage,
  useUpdaterState,
} from "../hooks/use-updater-state";

function formatCheckTime(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso;
  return date.toLocaleString();
}

export function UpdatesSettingsTab() {
  const { t } = useT("settings");
  const { state, checkForUpdates, downloadUpdate, installUpdate, openReleasePage } =
    useUpdaterState();
  const [automaticUpdates, setAutomaticUpdates] = useState(true);
  const [preferencesReady, setPreferencesReady] = useState(false);
  const [savingPreference, setSavingPreference] = useState(false);
  const currentVersion = window.desktopAPI.appInfo.version;

  useEffect(() => {
    let mounted = true;
    void window.updater
      .getPreferences()
      .then((preferences) => {
        if (mounted) setAutomaticUpdates(preferences.automaticUpdates);
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

  const sourceLabel = useCallback(
    (source: UpdateCheckSource) => {
      switch (source) {
        case "startup":
          return t(($) => $.desktop.updates.check_source_startup);
        case "periodic":
          return t(($) => $.desktop.updates.check_source_periodic);
        case "manual":
          return t(($) => $.desktop.updates.check_source_manual);
        case "reenable":
          return t(($) => $.desktop.updates.check_source_reenable);
      }
    },
    [t],
  );

  const lastCheckLine = (record: UpdateCheckRecord | null): string => {
    if (!record) return t(($) => $.desktop.updates.last_check_never);
    const time = formatCheckTime(record.checkedAt);
    const source = sourceLabel(record.source);
    if (!record.ok) {
      return t(($) => $.desktop.updates.last_check_failed, {
        time,
        source,
        error: record.error ?? "",
      });
    }
    if (record.available) {
      return t(($) => $.desktop.updates.last_check_available, {
        time,
        source,
        version: record.latestVersion ?? "",
      });
    }
    return t(($) => $.desktop.updates.last_check_up_to_date, { time, source });
  };

  const version = state.version ?? "";
  const offerDownload = canOfferInAppDownload(state);
  const offerReleasePage = shouldOfferReleasePage(state);
  const checking = state.checking || state.phase === "checking";

  return (
    <SettingsTab title={t(($) => $.desktop.updates.title)}>
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
          label={t(($) => $.desktop.updates.check_section_title)}
          align="start"
          description={
            <>
              <p>{t(($) => $.desktop.updates.check_section_description)}</p>
              <p className="mt-2">{lastCheckLine(state.lastCheck)}</p>
              {state.phase === "available" && !offerReleasePage && (
                <p className="mt-2 inline-flex items-center gap-1.5">
                  <Check className="size-3.5 text-primary" />
                  {t(($) => $.desktop.updates.update_available, { version })}
                </p>
              )}
              {offerReleasePage && (
                <p className="mt-2">{t(($) => $.desktop.updates.manual_download_required, { version })}</p>
              )}
              {state.phase === "downloading" && (
                <>
                  <p className="mt-2">{t(($) => $.desktop.updates.downloading_update, { version })}</p>
                  <UpdateProgressBar
                    percent={state.percent ?? 0}
                    label={t(($) => $.desktop.updates.downloading_update, { version })}
                  />
                </>
              )}
              {state.phase === "downloaded" && (
                <p className="mt-2 inline-flex items-center gap-1.5">
                  <Check className="size-3.5 text-success" />
                  {t(($) => $.desktop.updates.ready_to_install, { version })}
                </p>
              )}
              {state.phase === "error" && state.errorMessage && (
                <p className="mt-2 inline-flex items-center gap-1.5 text-destructive">
                  <AlertCircle className="size-3.5" />
                  {state.errorMessage}
                </p>
              )}
            </>
          }
        >
          <div className="flex flex-col items-end gap-2">
            {offerDownload && (
              <Button variant="default" size="sm" onClick={() => void downloadUpdate()}>
                {t(($) => $.desktop.updates.download)}
              </Button>
            )}
            {offerReleasePage && (
              <Button variant="outline" size="sm" onClick={() => void openReleasePage()}>
                {t(($) => $.desktop.updates.open_releases)}
              </Button>
            )}
            {state.phase === "downloaded" && (
              <Button variant="default" size="sm" onClick={() => void installUpdate()}>
                {t(($) => $.desktop.updates.restart_now)}
              </Button>
            )}
            <Button variant="outline" size="sm" onClick={() => void checkForUpdates()} disabled={checking}>
              {checking ? t(($) => $.desktop.updates.checking) : t(($) => $.desktop.updates.check_now)}
            </Button>
          </div>
        </SettingsRow>
      </SettingsCard>
    </SettingsTab>
  );
}
