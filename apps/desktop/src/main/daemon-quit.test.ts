// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import { createDaemonQuitHandler } from "./daemon-quit";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

describe("daemon quit", () => {
  it("holds Electron's exit synchronously until the single stop finishes", async () => {
    const prefs = deferred<{ autoStart: boolean; autoStop: boolean }>();
    const stopped = deferred<void>();
    const deps = {
      isInstallingUpdate: () => false,
      stopSupervision: vi.fn(),
      loadPrefs: vi.fn(() => prefs.promise),
      stop: vi.fn(() => stopped.promise),
      quit: vi.fn(),
      logError: vi.fn(),
    };
    const handler = createDaemonQuitHandler(deps);
    const event = { preventDefault: vi.fn() };
    handler(event);
    expect(event.preventDefault).toHaveBeenCalledOnce();
    expect(deps.quit).not.toHaveBeenCalled();
    handler(event);
    expect(deps.loadPrefs).toHaveBeenCalledOnce();
    prefs.resolve({ autoStart: true, autoStop: true });
    await Promise.resolve();
    expect(deps.stop).toHaveBeenCalledOnce();
    expect(deps.quit).not.toHaveBeenCalled();
    stopped.resolve();
    await Promise.resolve();
    expect(deps.quit).toHaveBeenCalledOnce();
    handler(event);
    expect(event.preventDefault).toHaveBeenCalledTimes(2);
  });

  it.each([false, true])("keeps daemon running during an update or when autoStop is disabled (%s)", async (updating) => {
    const deps = {
      isInstallingUpdate: () => updating,
      stopSupervision: vi.fn(),
      loadPrefs: vi.fn(async () => ({ autoStart: true, autoStop: false })),
      stop: vi.fn(),
      quit: vi.fn(),
      logError: vi.fn(),
    };
    const event = { preventDefault: vi.fn() };
    createDaemonQuitHandler(deps)(event);
    await Promise.resolve();
    expect(deps.stop).not.toHaveBeenCalled();
    expect(deps.stopSupervision).toHaveBeenCalledOnce();
    expect(event.preventDefault).toHaveBeenCalledTimes(updating ? 0 : 1);
    expect(deps.quit).toHaveBeenCalledTimes(updating ? 0 : 1);
  });
});
