import type { DaemonPrefs } from "../shared/daemon-types";

// Electron only honors preventDefault during the event, before any await.
export function createDaemonQuitHandler(deps: {
  isInstallingUpdate: () => boolean;
  /** drainForUpgrade: leave the daemon running across an update install. */
  keepDaemonOnUpdateInstall: () => boolean;
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
      // Never hold an update install's quit. Default (old behaviour): autoStop
      // still stops the daemon, best-effort, while the app exits.
      deps.stopSupervision();
      if (!deps.keepDaemonOnUpdateInstall()) {
        finished = true;
        void deps.loadPrefs()
          .then((prefs) => (prefs.autoStop ? deps.stop() : undefined))
          .catch(deps.logError);
      }
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
