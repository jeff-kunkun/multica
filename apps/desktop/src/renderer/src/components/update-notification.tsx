import { useState } from "react";
import { RefreshCw, X } from "lucide-react";
import { useT } from "@multica/views/i18n";
import { UpdateProgressBar } from "./update-progress";
import {
  canOfferInAppDownload,
  shouldOfferReleasePage,
  useUpdaterState,
} from "../hooks/use-updater-state";

function changelogUrl(version: string): string {
  return `https://multica.ai/changelog#release-${version.replace(/\./g, "-")}`;
}

const primaryButtonClass =
  "inline-flex items-center rounded-md bg-primary px-3 py-1.5 text-caption font-medium text-primary-foreground hover:bg-primary/90 transition-colors";
const secondaryButtonClass =
  "inline-flex items-center rounded-md border border-border bg-background px-3 py-1.5 text-caption font-medium text-foreground hover:bg-accent transition-colors";

export function UpdateNotification() {
  const { t } = useT("settings");
  const { state, downloadUpdate, installUpdate, openReleasePage } = useUpdaterState();
  const dismissKey = `${state.phase}:${state.version ?? ""}:${state.errorMessage ?? ""}`;
  const [dismissedKey, setDismissedKey] = useState<string | null>(null);

  const visible =
    state.phase === "available" ||
    state.phase === "downloading" ||
    state.phase === "downloaded" ||
    state.phase === "error";
  if (!visible || dismissedKey === dismissKey) return null;

  const version = state.version ?? "";
  const manual = shouldOfferReleasePage(state);
  const offerDownload = canOfferInAppDownload(state);

  let title = t(($) => $.desktop.updates.available_title);
  let body = t(($) => $.desktop.updates.update_available, { version });
  if (state.phase === "downloading") {
    title = t(($) => $.desktop.updates.downloading_update, { version });
    body = "";
  } else if (state.phase === "downloaded") {
    title = t(($) => $.desktop.updates.update_ready);
    body = t(($) => $.desktop.updates.ready_to_install, { version });
  } else if (state.phase === "error") {
    title = t(($) => $.desktop.updates.update_failed);
    body = state.errorMessage ?? "";
  } else if (manual) {
    title = t(($) => $.desktop.updates.manual_title);
    body = t(($) => $.desktop.updates.manual_download_required, { version });
  }

  return (
    <div className="fixed bottom-4 right-4 z-50 w-80 rounded-lg border border-border bg-background p-4 shadow-lg animate-in slide-in-from-bottom-2 fade-in duration-300">
      <button
        type="button"
        onClick={() => setDismissedKey(dismissKey)}
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
          {body ? (
            <p className="mt-0.5 text-caption text-muted-foreground">{body}</p>
          ) : null}
          {state.phase === "downloading" ? (
            <UpdateProgressBar
              percent={state.percent ?? 0}
              label={t(($) => $.desktop.updates.downloading_update, { version })}
            />
          ) : null}
          <div className="mt-2 flex items-center gap-1.5">
            {state.phase === "downloaded" && version ? (
              <button
                type="button"
                onClick={() => window.desktopAPI.openExternal(changelogUrl(version))}
                className={secondaryButtonClass}
              >
                {t(($) => $.desktop.updates.see_changelog)}
              </button>
            ) : null}
            {offerDownload ? (
              <button type="button" onClick={() => void downloadUpdate()} className={primaryButtonClass}>
                {t(($) => $.desktop.updates.download)}
              </button>
            ) : null}
            {manual ? (
              <button type="button" onClick={() => void openReleasePage()} className={primaryButtonClass}>
                {t(($) => $.desktop.updates.open_releases)}
              </button>
            ) : null}
            {state.phase === "downloaded" ? (
              <button type="button" onClick={() => void installUpdate()} className={primaryButtonClass}>
                {t(($) => $.desktop.updates.restart_now)}
              </button>
            ) : null}
          </div>
        </div>
      </div>
    </div>
  );
}
