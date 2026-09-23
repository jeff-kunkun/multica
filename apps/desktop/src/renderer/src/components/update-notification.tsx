import { useState } from "react";
import { RefreshCw, X } from "lucide-react";
import { useT } from "@multica/views/i18n";
import { useDesktopUpdate } from "./use-desktop-update";

function changelogUrl(version: string): string {
  return `https://multica.ai/changelog#release-${version.replace(/\./g, "-")}`;
}

export function UpdateNotification() {
  const { t } = useT("settings");
  const { snapshot } = useDesktopUpdate();
  const signature = `${snapshot.phase}:${snapshot.version ?? ""}:${snapshot.errorCode ?? ""}`;
  const [dismissedSignature, setDismissedSignature] = useState<string | null>(null);

  if (snapshot.phase === "idle") return null;
  if (dismissedSignature === signature) return null;

  const version = snapshot.version ?? "";
  const percent = String(Math.round(snapshot.percent ?? 0));
  const title =
    snapshot.phase === "ready"
      ? t(($) => $.desktop.updates.update_ready_title)
      : snapshot.phase === "downloading"
        ? t(($) => $.desktop.updates.update_downloading_title)
        : snapshot.phase === "error"
          ? t(($) => $.desktop.updates.update_failed_title)
          : t(($) => $.desktop.updates.update_available_title);
  const body =
    snapshot.phase === "ready"
      ? t(($) => $.desktop.updates.applied_on_restart, { version })
      : snapshot.phase === "downloading"
        ? t(($) => $.desktop.updates.progress, { version, percent })
        : snapshot.phase === "error"
          ? snapshot.errorCode === "no_test_release"
            ? t(($) => $.desktop.updates.no_test_release)
            : snapshot.error || t(($) => $.desktop.updates.check_failed)
          : t(($) => $.desktop.updates.manual_available, { version });

  return (
    <div className="fixed bottom-4 right-4 z-50 w-80 rounded-lg border border-border bg-background p-4 shadow-lg animate-in slide-in-from-bottom-2 fade-in duration-300">
      <button
        type="button"
        onClick={() => setDismissedSignature(signature)}
        className="absolute top-2 right-2 rounded-md p-1 text-muted-foreground hover:text-foreground transition-colors"
        aria-label={t(($) => $.desktop.updates.dismiss)}
      >
        <X className="size-3.5" />
      </button>

      <div className="flex items-start gap-3">
        <div className="mt-0.5 rounded-md bg-success/10 p-1.5">
          <RefreshCw className="size-4 text-success" />
        </div>
        <div className="min-w-0 flex-1">
          <p className="text-body font-medium">{title}</p>
          <p className="mt-0.5 text-caption text-muted-foreground">{body}</p>
          {snapshot.phase === "downloading" && (
            <div
              className="mt-2 h-1.5 overflow-hidden rounded-full bg-muted"
              role="progressbar"
              aria-valuemin={0}
              aria-valuemax={100}
              aria-valuenow={Math.round(snapshot.percent ?? 0)}
            >
              <div
                className="h-full bg-primary"
                style={{ width: `${Math.min(100, Math.max(0, snapshot.percent ?? 0))}%` }}
              />
            </div>
          )}
          <div className="mt-2 flex items-center gap-1.5">
            {snapshot.phase === "ready" && (
              <button
                type="button"
                onClick={() =>
                  window.desktopAPI.openExternal(changelogUrl(version))
                }
                className="inline-flex items-center rounded-md border border-border bg-background px-3 py-1.5 text-caption font-medium text-foreground hover:bg-accent transition-colors"
              >
                {t(($) => $.desktop.updates.see_changelog)}
              </button>
            )}
            {snapshot.manualDownloadUrl && snapshot.phase !== "downloading" && snapshot.phase !== "ready" && (
              <button
                type="button"
                onClick={() =>
                  window.desktopAPI.openExternal(snapshot.manualDownloadUrl!)
                }
                className="inline-flex items-center rounded-md bg-primary px-3 py-1.5 text-caption font-medium text-primary-foreground hover:bg-primary/90 transition-colors"
              >
                {t(($) => $.desktop.updates.download_installer)}
              </button>
            )}
            {snapshot.phase === "ready" && (
              <button
                type="button"
                onClick={() => window.updater.installUpdate()}
                className="inline-flex items-center rounded-md bg-primary px-3 py-1.5 text-caption font-medium text-primary-foreground hover:bg-primary/90 transition-colors"
              >
                {t(($) => $.desktop.updates.restart_now)}
              </button>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
