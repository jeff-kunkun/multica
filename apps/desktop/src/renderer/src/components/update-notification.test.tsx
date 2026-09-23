import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { DESKTOP_RELEASES_PAGE_URL } from "../../../shared/updater-types";
import type {
  UpdateAvailableInfo,
  UpdateCheckRecord,
  UpdaterErrorInfo,
  UpdaterSnapshot,
} from "../../../shared/updater-types";
import { resetUpdaterStoreForTests } from "../hooks/use-updater-state";

const mocks = vi.hoisted(() => ({
  installUpdate: vi.fn(),
  downloadUpdate: vi.fn(),
  openExternal: vi.fn(),
  getSnapshot: vi.fn(),
}));

const translations = {
  desktop: {
    updates: {
      available_title: "Update available",
      update_available: "v{{version}} is available.",
      downloading_update: "Downloading v{{version}}",
      update_ready: "Update ready",
      ready_to_install: "v{{version}} will be applied on next launch.",
      update_failed: "Update failed",
      manual_title: "Manual download required",
      manual_download_required:
        "This Mac build is not Developer ID signed, so automatic install cannot succeed. Download v{{version}} from the release page.",
      dismiss: "Dismiss",
      see_changelog: "See changelog",
      download: "Download",
      open_releases: "Open release page",
      restart_now: "Restart now",
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

import { UpdateNotification } from "./update-notification";

const listeners: {
  available: ((info: UpdateAvailableInfo) => void) | null;
  progress: ((progress: { percent: number }) => void) | null;
  downloaded: ((info: { version: string; releaseNotes?: string }) => void) | null;
  error: ((info: UpdaterErrorInfo) => void) | null;
  check: ((result: UpdateCheckRecord) => void) | null;
} = {
  available: null,
  progress: null,
  downloaded: null,
  error: null,
  check: null,
};

function emptySnapshot(): UpdaterSnapshot {
  return {
    phase: "idle",
    version: null,
    percent: null,
    error: null,
    manualDownloadRequired: false,
    releasePageUrl: DESKTOP_RELEASES_PAGE_URL,
    lastCheck: null,
  };
}

describe("UpdateNotification", () => {
  beforeEach(() => {
    resetUpdaterStoreForTests();
    listeners.available = null;
    listeners.progress = null;
    listeners.downloaded = null;
    listeners.error = null;
    listeners.check = null;
    mocks.installUpdate.mockReset().mockResolvedValue(undefined);
    mocks.downloadUpdate.mockReset().mockResolvedValue(undefined);
    mocks.openExternal.mockReset().mockResolvedValue(undefined);
    mocks.getSnapshot.mockReset().mockResolvedValue(emptySnapshot());

    Object.defineProperty(window, "desktopAPI", {
      configurable: true,
      value: { openExternal: mocks.openExternal },
    });
    Object.defineProperty(window, "updater", {
      configurable: true,
      value: {
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
        onUpdateDownloaded: (listener: (info: { version: string }) => void) => {
          listeners.downloaded = listener;
          return () => {
            listeners.downloaded = null;
          };
        },
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
        downloadUpdate: mocks.downloadUpdate,
        installUpdate: mocks.installUpdate,
        getSnapshot: mocks.getSnapshot,
      },
    });
  });

  async function renderCard() {
    render(<UpdateNotification />);
    await waitFor(() => expect(listeners.progress).toEqual(expect.any(Function)));
    await act(async () => {
      await mocks.getSnapshot.mock.results[0]?.value;
    });
  }

  it("opens the downloaded version's changelog from the update prompt", async () => {
    await renderCard();
    act(() => listeners.downloaded?.({ version: "0.4.27" }));

    expect(screen.queryByRole("button", { name: "Later" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "See changelog" }));

    expect(mocks.openExternal).toHaveBeenCalledWith(
      "https://multica.ai/changelog#release-0-4-27",
    );
  });

  it("still installs the update immediately from the primary action", async () => {
    await renderCard();
    act(() => listeners.downloaded?.({ version: "0.4.27" }));

    fireEvent.click(screen.getByRole("button", { name: "Restart now" }));

    expect(mocks.installUpdate).toHaveBeenCalledOnce();
  });

  it("advances the progress bar from download-progress events", async () => {
    await renderCard();
    act(() =>
      listeners.available?.({
        version: "2.0.0",
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

  it("renders the failure from an updater error and retries the download", async () => {
    await renderCard();
    act(() =>
      listeners.available?.({
        version: "2.0.0",
        manualDownloadRequired: false,
        releasePageUrl: DESKTOP_RELEASES_PAGE_URL,
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Download" }));
    expect(mocks.downloadUpdate).toHaveBeenCalledOnce();

    act(() => listeners.error?.({ message: "socket hang up" }));

    expect(screen.getByText("Update failed")).toBeInTheDocument();
    expect(screen.getByText("socket hang up")).toBeInTheDocument();
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Download" }));
    expect(mocks.downloadUpdate).toHaveBeenCalledTimes(2);
  });

  it("points an unsigned Mac at the release page instead of an in-app download", async () => {
    await renderCard();
    act(() =>
      listeners.available?.({
        version: "2.0.0",
        manualDownloadRequired: true,
        releasePageUrl: DESKTOP_RELEASES_PAGE_URL,
      }),
    );

    expect(screen.getByText("Manual download required")).toBeInTheDocument();
    expect(screen.getByText(/Developer ID/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Download" })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Open release page" }));
    expect(mocks.openExternal).toHaveBeenCalledWith(DESKTOP_RELEASES_PAGE_URL);
  });
});
