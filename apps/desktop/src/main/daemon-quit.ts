import type { DaemonPrefs } from "../shared/daemon-types";

// Electron only honors preventDefault during the event, before any await.
export function createDaemonQuitHandler(deps: {
  isInstallingUpdate: () => boolean;
  stopSupervision: () => void;
  loadPrefs: () => Promise<DaemonPrefs>;
  stop: () => Promise<unknown>;
  quit: () => void;
  logError: (error: unknown) => void;
}) {
  let finished = false;
  let pending = false;
  return (event: { preventDefault: () => void }): void => {
    if (finished) return;
    if (deps.isInstallingUpdate()) {
      deps.stopSupervision();
      return;
    }
    event.preventDefault();
    if (pending) return;
    pending = true;
    deps.stopSupervision();
    void (async () => {
      try {
        const prefs = await deps.loadPrefs();
        if (prefs.autoStop) await deps.stop();
      } catch (error) {
        deps.logError(error);
      } finally {
        finished = true;
        deps.quit();
      }
    })();
  };
}
