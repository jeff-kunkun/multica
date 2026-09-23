import { useEffect, useState } from "react";
import { AlertCircle, Check, Loader2 } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Switch } from "@multica/ui/components/ui/switch";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "@multica/views/i18n";
import { SettingsCard, SettingsRow, SettingsTab } from "@multica/views/settings";
import type { ReleaseChannel } from "../../../shared/updater-types";
import { toast } from "sonner";
import { useDesktopUpdate } from "./use-desktop-update";

export function UpdatesSettingsTab() {
  const { t } = useT("settings");
  const { snapshot, preferences, setPreferences, preferencesReady } = useDesktopUpdate();
  const [checking, setChecking] = useState(false);
  const [savingAutomatic, setSavingAutomatic] = useState(false);
  const [savingChannel, setSavingChannel] = useState(false);
  const [upToDate, setUpToDate] = useState(false);
  const currentVersion = window.desktopAPI.appInfo.version;

  useEffect(() => {
    if (snapshot.phase !== "idle") setUpToDate(false);
  }, [snapshot.phase]);

  const failureText = () => {
    if (snapshot.errorCode === "no_test_release") {
      return t(($) => $.desktop.updates.no_test_release);
    }
    return snapshot.error || t(($) => $.desktop.updates.check_failed);
  };

  const handleAutomaticUpdatesChange = async (enabled: boolean) => {
    setSavingAutomatic(true);
    try {
      const next = await window.updater.setAutomaticUpdates(enabled);
      setPreferences(next);
      toast.success(t(($) => $.auto_save.toast_saved), {
        id: "settings-auto-save",
      });
    } catch {
      toast.error(t(($) => $.desktop.updates.automatic_updates_save_failed));
    } finally {
      setSavingAutomatic(false);
    }
  };

  const handleChannel = async (channel: ReleaseChannel) => {
    if (channel === preferences.releaseChannel) return;
    setSavingChannel(true);
    setUpToDate(false);
    try {
      const next = await window.updater.setReleaseChannel(channel);
      setPreferences(next);
      toast.success(t(($) => $.auto_save.toast_saved), {
        id: "settings-auto-save",
      });
    } catch {
      toast.error(t(($) => $.desktop.updates.channel_save_failed));
    } finally {
      setSavingChannel(false);
    }
  };

  const handleCheck = async () => {
    setChecking(true);
    setUpToDate(false);
    try {
      const result = await window.updater.checkForUpdates();
      if (!result.ok) return;
      const state = await window.updater.getState();
      const busy =
        state.phase === "available" ||
        state.phase === "downloading" ||
        state.phase === "ready" ||
        state.phase === "error";
      if (!result.available && !busy) setUpToDate(true);
    } finally {
      setChecking(false);
    }
  };

  const percent = Math.round(snapshot.percent ?? 0);

  return (
    <SettingsTab title={t(($) => $.desktop.updates.title)}>
      <SettingsCard>
        <SettingsRow label={t(($) => $.desktop.updates.current_version)}>
          <span className="font-mono text-caption text-muted-foreground">
            v{currentVersion}
          </span>
        </SettingsRow>

        <SettingsRow
          label={t(($) => $.desktop.updates.channel_title)}
          description={t(($) => $.desktop.updates.channel_description)}
        >
          <div
            role="radiogroup"
            aria-label={t(($) => $.desktop.updates.channel_title)}
            className="inline-flex rounded-md border border-border p-0.5"
          >
            {(["stable", "test"] as const).map((channel) => {
              const selected = preferences.releaseChannel === channel;
              return (
                <button
                  key={channel}
                  type="button"
                  role="radio"
                  aria-checked={selected}
                  disabled={!preferencesReady || savingChannel}
                  onClick={() => void handleChannel(channel)}
                  className={cn(
                    "rounded-[5px] px-2.5 py-1 text-caption",
                    selected
                      ? "bg-primary text-primary-foreground"
                      : "text-muted-foreground hover:text-foreground",
                  )}
                >
                  {channel === "stable"
                    ? t(($) => $.desktop.updates.channel_stable)
                    : t(($) => $.desktop.updates.channel_test)}
                </button>
              );
            })}
          </div>
        </SettingsRow>

        <SettingsRow
          label={t(($) => $.desktop.updates.automatic_updates_title)}
          description={t(($) => $.desktop.updates.automatic_updates_description)}
        >
          <Switch
            checked={preferences.automaticUpdates}
            onCheckedChange={(enabled) => void handleAutomaticUpdatesChange(enabled)}
            disabled={!preferencesReady || savingAutomatic}
            aria-label={t(($) => $.desktop.updates.automatic_updates_title)}
          />
        </SettingsRow>

        <SettingsRow
          label={t(($) => $.desktop.updates.check_section_title)}
          align="start"
          description={
            <>
              <p>{t(($) => $.desktop.updates.check_section_description)}</p>
              {upToDate && snapshot.phase === "idle" && (
                <p className="mt-2 inline-flex items-center gap-1.5">
                  <Check className="size-3.5 text-success" />
                  {t(($) => $.desktop.updates.up_to_date)}
                </p>
              )}
              {snapshot.phase === "downloading" && snapshot.version && (
                <div className="mt-2">
                  <p>
                    {t(($) => $.desktop.updates.progress, {
                      version: snapshot.version,
                      percent: String(percent),
                    })}
                  </p>
                  <div
                    className="mt-1.5 h-1.5 overflow-hidden rounded-full bg-muted"
                    role="progressbar"
                    aria-valuemin={0}
                    aria-valuemax={100}
                    aria-valuenow={percent}
                  >
                    <div
                      className="h-full bg-primary"
                      style={{ width: `${Math.min(100, Math.max(0, percent))}%` }}
                    />
                  </div>
                </div>
              )}
              {snapshot.phase === "available" && snapshot.version && (
                <p className="mt-2">
                  {t(($) => $.desktop.updates.manual_available, {
                    version: snapshot.version,
                  })}
                </p>
              )}
              {snapshot.phase === "ready" && snapshot.version && (
                <p className="mt-2 inline-flex items-center gap-1.5">
                  <Check className="size-3.5 text-success" />
                  {t(($) => $.desktop.updates.ready, { version: snapshot.version })}
                </p>
              )}
              {snapshot.phase === "error" && (
                <p className="mt-2 inline-flex items-center gap-1.5 text-destructive">
                  <AlertCircle className="size-3.5" />
                  {failureText()}
                </p>
              )}
            </>
          }
        >
          <div className="flex flex-col items-end gap-2">
            <Button
              variant="outline"
              size="sm"
              onClick={() => void handleCheck()}
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
            {snapshot.manualDownloadUrl &&
              (snapshot.phase === "available" || snapshot.phase === "error") && (
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() =>
                    void window.desktopAPI.openExternal(snapshot.manualDownloadUrl!)
                  }
                >
                  {t(($) => $.desktop.updates.download_installer)}
                </Button>
              )}
            {snapshot.phase === "ready" && (
              <Button size="sm" onClick={() => void window.updater.installUpdate()}>
                {t(($) => $.desktop.updates.restart_now)}
              </Button>
            )}
          </div>
        </SettingsRow>
      </SettingsCard>
    </SettingsTab>
  );
}
