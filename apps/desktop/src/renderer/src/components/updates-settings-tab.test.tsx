import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { DESKTOP_RELEASES_PAGE_URL } from "../../../shared/updater-types";
import type {
  UpdateAvailableInfo,
  UpdateCheckRecord,
  UpdaterErrorInfo,
  UpdaterSnapshot,
} from "../../../shared/updater-types";
import { resetUpdaterStoreForTests } from "../hooks/use-updater-state";

const mocks = vi.hoisted(() => ({
  getPreferences: vi.fn(),
  setAutomaticUpdates: vi.fn(),
  checkForUpdates: vi.fn(),
  downloadUpdate: vi.fn(),
  installUpdate: vi.fn(),
  getSnapshot: vi.fn(),
  openExternal: vi.fn(),
  toastSuccess: vi.fn(),
  toastError: vi.fn(),
}));

const translations = {
  auto_save: { toast_saved: "Settings saved" },
  desktop: {
    updates: {
      title: "Updates",
      description: "Update preferences",
      current_version: "Current version",
      automatic_updates_title: "Automatic background updates",
      automatic_updates_description: "Download updates in the background",
      automatic_updates_save_failed: "Failed to save update settings",
      check_section_title: "Check for updates",
      check_section_description: "Check manually",
      up_to_date: "Up to date",
      downloading: "Downloading v{{version}}",
      check_now: "Check now",
      checking: "Checking",
      download: "Download",
      update_available: "v{{version}} is available.",
      manual_download_required:
        "This Mac build is not Developer ID signed, so automatic install cannot succeed. Download v{{version}} from the release page.",
      open_releases: "Open release page",
      update_failed: "Update failed",
      ready_to_install: "v{{version}} will be applied on next launch.",
      restart_now: "Restart now",
      downloading_update: "Downloading v{{version}}",
      last_check_never: "No update check yet.",
      last_check_up_to_date: "Last {{source}} check at {{time}}: already on the latest version.",
      last_check_available: "Last {{source}} check at {{time}}: v{{version}} is available.",
      last_check_failed: "Last {{source}} check at {{time}}: failed — {{error}}",
      check_source_startup: "startup",
      check_source_periodic: "hourly",
      check_source_manual: "manual",
      check_source_reenable: "re-enable",
    },
  },
};

vi.mock("@multica/views/i18n", () => ({
  useT: () => ({
    t: (
      selector: (resources: typeof translations) => string,
      values?: Record<string, string>,
    ) => {
      const template = selector(translations);
      return Object.entries(values ?? {}).reduce(
        (result, [key, value]) => result.replace(`{{${key}}}`, value),
        template,
      );
    },
  }),
}));

vi.mock("sonner", () => ({
  toast: {
    success: mocks.toastSuccess,
    error: mocks.toastError,
  },
}));

import { UpdatesSettingsTab } from "./updates-settings-tab";

const listeners: {
  available: ((info: UpdateAvailableInfo) => void) | null;
  progress: ((progress: { percent: number }) => void) | null;
  error: ((info: UpdaterErrorInfo) => void) | null;
  check: ((result: UpdateCheckRecord) => void) | null;
} = {
  available: null,
  progress: null,
  error: null,
  check: null,
};

function emptySnapshot(overrides: Partial<UpdaterSnapshot> = {}): UpdaterSnapshot {
  return {
    phase: "idle",
    version: null,
    percent: null,
    error: null,
    manualDownloadRequired: false,
    releasePageUrl: DESKTOP_RELEASES_PAGE_URL,
    lastCheck: null,
    ...overrides,
  };
}

describe("UpdatesSettingsTab", () => {
  beforeEach(() => {
    resetUpdaterStoreForTests();
    listeners.available = null;
    listeners.progress = null;
    listeners.error = null;
    listeners.check = null;
    mocks.getPreferences.mockReset().mockResolvedValue({
      automaticUpdates: true,
    });
    mocks.setAutomaticUpdates.mockReset();
    mocks.checkForUpdates.mockReset();
    mocks.downloadUpdate.mockReset().mockResolvedValue(undefined);
    mocks.installUpdate.mockReset().mockResolvedValue(undefined);
    mocks.getSnapshot.mockReset().mockResolvedValue(emptySnapshot());
    mocks.openExternal.mockReset().mockResolvedValue(undefined);
    mocks.toastSuccess.mockReset();
    mocks.toastError.mockReset();

    Object.defineProperty(window, "desktopAPI", {
      configurable: true,
      value: {
        appInfo: { version: "1.2.3" },
        openExternal: mocks.openExternal,
      },
    });
    Object.defineProperty(window, "updater", {
      configurable: true,
      value: {
        getPreferences: mocks.getPreferences,
        setAutomaticUpdates: mocks.setAutomaticUpdates,
        checkForUpdates: mocks.checkForUpdates,
        downloadUpdate: mocks.downloadUpdate,
        installUpdate: mocks.installUpdate,
        getSnapshot: mocks.getSnapshot,
        onUpdateAvailable: (listener: (info: UpdateAvailableInfo) => void) => {
          listeners.available = listener;
          return () => {
            listeners.available = null;
          };
        },
        onDownloadProgress: (listener: (progress: { percent: number }) => void) => {
          listeners.progress = listener;
          return () => {
            listeners.progress = null;
          };
        },
        onUpdateDownloaded: () => () => undefined,
        onUpdaterError: (listener: (info: UpdaterErrorInfo) => void) => {
          listeners.error = listener;
          return () => {
            listeners.error = null;
          };
        },
        onCheckResult: (listener: (result: UpdateCheckRecord) => void) => {
          listeners.check = listener;
          return () => {
            listeners.check = null;
          };
        },
      },
    });
  });

  async function renderTab() {
    render(<UpdatesSettingsTab />);
    await waitFor(() => expect(listeners.progress).toEqual(expect.any(Function)));
    await act(async () => {
      await mocks.getSnapshot.mock.results[0]?.value;
    });
  }

  it("loads the persisted preference and saves changes from the switch", async () => {
    mocks.getPreferences.mockResolvedValue({ automaticUpdates: false });
    mocks.setAutomaticUpdates.mockResolvedValue({ automaticUpdates: true });
    render(<UpdatesSettingsTab />);

    const toggle = screen.getByRole("switch", {
      name: "Automatic background updates",
    });
    // The switch renders as <span role="switch">, so jest-dom's toBeEnabled()
    // treats it as always enabled and does not actually wait for getPreferences
    // to resolve. Wait on the persisted value being reflected instead, which
    // deterministically holds until the loaded preference (false) is applied.
    await waitFor(() => expect(toggle).not.toBeChecked());

    fireEvent.click(toggle);

    await waitFor(() => {
      expect(mocks.setAutomaticUpdates).toHaveBeenCalledWith(true);
      expect(toggle).toBeChecked();
    });
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Settings saved", {
      id: "settings-auto-save",
    });
  });

  it("shows the last automatic check, including a later hourly failure", async () => {
    mocks.getSnapshot.mockResolvedValue(
      emptySnapshot({
        lastCheck: {
          checkedAt: "2026-09-23T07:40:17.000Z",
          source: "startup",
          ok: false,
          error: "network down",
        },
        phase: "error",
        error: "network down",
      }),
    );

    await renderTab();

    expect(screen.getAllByText(/network down/).length).toBeGreaterThan(0);
    expect(screen.getByText(/startup/)).toBeInTheDocument();

    act(() =>
      listeners.check?.({
        checkedAt: "2026-09-23T08:40:17.000Z",
        source: "periodic",
        ok: true,
        available: false,
        currentVersion: "1.2.3",
        latestVersion: "1.2.3",
      }),
    );

    expect(screen.getByText(/hourly/)).toBeInTheDocument();
    expect(screen.getByText(/already on the latest version/)).toBeInTheDocument();
    expect(screen.queryByText(/network down/)).not.toBeInTheDocument();
  });

  it("advances the progress bar from download-progress events", async () => {
    await renderTab();
    act(() =>
      listeners.available?.({
        version: "9.9.9",
        manualDownloadRequired: false,
        releasePageUrl: DESKTOP_RELEASES_PAGE_URL,
      }),
    );
    act(() => listeners.progress?.({ percent: 15 }));
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "15");

    act(() => listeners.progress?.({ percent: 42 }));
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "42");
    expect(screen.getByText("42%")).toBeInTheDocument();
  });

  it("renders an updater error and retries through the download IPC", async () => {
    await renderTab();
    act(() =>
      listeners.available?.({
        version: "9.9.9",
        manualDownloadRequired: false,
        releasePageUrl: DESKTOP_RELEASES_PAGE_URL,
      }),
    );

    fireEvent.click(screen.getByRole("button", { name: "Download" }));
    expect(mocks.downloadUpdate).toHaveBeenCalledOnce();

    act(() => listeners.error?.({ message: "socket hang up" }));

    expect(screen.getByText("socket hang up")).toBeInTheDocument();
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Download" }));
    expect(mocks.downloadUpdate).toHaveBeenCalledTimes(2);
  });

  it("offers the release page when the running Mac build cannot auto-install", async () => {
    await renderTab();
    act(() =>
      listeners.available?.({
        version: "9.9.9",
        manualDownloadRequired: true,
        releasePageUrl: DESKTOP_RELEASES_PAGE_URL,
      }),
    );

    expect(screen.getByText(/Developer ID/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Download" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Open release page" }));
    expect(mocks.openExternal).toHaveBeenCalledWith(DESKTOP_RELEASES_PAGE_URL);
  });
});
