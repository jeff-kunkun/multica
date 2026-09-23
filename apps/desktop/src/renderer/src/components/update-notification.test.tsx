import { act, fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { UpdateSnapshot } from "../../../shared/updater-types";

import { UpdateNotification } from "./update-notification";

const mocks = vi.hoisted(() => ({
  installUpdate: vi.fn(),
  openExternal: vi.fn(),
}));

const translations = {
  desktop: {
    updates: {
      update_ready_title: "Update ready",
      update_available_title: "Update available",
      update_downloading_title: "Downloading update",
      update_failed_title: "Update didn't finish",
      see_changelog: "See changelog",
      applied_on_restart: "v{{version}} will be applied on next launch.",
      progress: "Downloading v{{version}} — {{percent}}%",
      manual_available:
        "v{{version}} is available. This Mac can't install updates by itself yet, so download the installer.",
      download_installer: "Download installer",
      restart_now: "Restart now",
      no_test_release: "The test channel doesn't have a published build yet.",
      check_failed: "Couldn't check for updates.",
      dismiss: "Dismiss",
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

type StateListener = (state: UpdateSnapshot) => void;

const idle: UpdateSnapshot = {
  phase: "idle",
  version: null,
  percent: null,
  manualDownloadUrl: null,
  installMode: "manual",
  releaseChannel: "stable",
  error: null,
  errorCode: null,
};

describe("UpdateNotification", () => {
  let publish: StateListener;

  beforeEach(() => {
    mocks.installUpdate.mockReset().mockResolvedValue(undefined);
    mocks.openExternal.mockReset().mockResolvedValue(undefined);

    Object.defineProperty(window, "desktopAPI", {
      configurable: true,
      value: { openExternal: mocks.openExternal },
    });
    Object.defineProperty(window, "updater", {
      configurable: true,
      value: {
        getPreferences: vi.fn().mockResolvedValue({
          automaticUpdates: true,
          releaseChannel: "stable",
        }),
        getState: vi.fn().mockResolvedValue(idle),
        onState: (listener: StateListener) => {
          publish = listener;
          return vi.fn();
        },
        installUpdate: mocks.installUpdate,
      },
    });
  });

  it("opens the downloaded version's changelog from the update prompt", async () => {
    render(<UpdateNotification />);
    await act(async () => {
      publish({
        ...idle,
        phase: "ready",
        version: "0.4.27",
        percent: 100,
        installMode: "automatic",
      });
    });

    fireEvent.click(screen.getByRole("button", { name: "See changelog" }));

    expect(mocks.openExternal).toHaveBeenCalledWith(
      "https://multica.ai/changelog#release-0-4-27",
    );
  });

  it("still installs the update immediately from the primary action", async () => {
    render(<UpdateNotification />);
    await act(async () => {
      publish({
        ...idle,
        phase: "ready",
        version: "0.4.27",
        percent: 100,
        installMode: "automatic",
      });
    });

    fireEvent.click(screen.getByRole("button", { name: "Restart now" }));

    expect(mocks.installUpdate).toHaveBeenCalledOnce();
  });

  it("shows download progress and a manual installer when the Mac cannot install in place", async () => {
    render(<UpdateNotification />);
    const url =
      "https://github.com/jeff-kunkun/multica/releases/download/v0.5.6/multica-desktop-0.5.6-mac-arm64.dmg";

    await act(async () => {
      publish({
        ...idle,
        phase: "downloading",
        version: "0.5.6",
        percent: 40,
        installMode: "automatic",
      });
    });
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "40");
    expect(screen.getByText("Downloading v0.5.6 — 40%")).toBeInTheDocument();

    await act(async () => {
      publish({
        ...idle,
        phase: "available",
        version: "0.5.6",
        manualDownloadUrl: url,
        installMode: "manual",
      });
    });
    fireEvent.click(screen.getByRole("button", { name: "Download installer" }));
    expect(mocks.openExternal).toHaveBeenCalledWith(url);
  });
});
