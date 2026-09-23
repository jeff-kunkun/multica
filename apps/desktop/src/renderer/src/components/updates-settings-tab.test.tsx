import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";

const mocks = vi.hoisted(() => ({
  getPreferences: vi.fn(),
  setAutomaticUpdates: vi.fn(),
  setReleaseChannel: vi.fn(),
  checkForUpdates: vi.fn(),
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
      release_channel_title: "Update channel",
      release_channel_description:
        "Stable and test use the same install. Switching checks the selected line right away.",
      release_channel_stable: "Stable",
      release_channel_test: "Test (updates more often, may be unstable)",
      release_channel_save_failed: "Failed to save the update channel",
      check_section_title: "Check for updates",
      check_section_description: "Check manually",
      up_to_date: "Up to date",
      downloading: "Downloading v{{version}}",
      check_now: "Check now",
      checking: "Checking",
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

describe("UpdatesSettingsTab", () => {
  beforeEach(() => {
    mocks.getPreferences.mockReset().mockResolvedValue({
      automaticUpdates: true,
      releaseChannel: "stable",
    });
    mocks.setAutomaticUpdates.mockReset();
    mocks.setReleaseChannel.mockReset();
    mocks.checkForUpdates.mockReset();
    mocks.toastSuccess.mockReset();
    mocks.toastError.mockReset();

    Object.defineProperty(window, "desktopAPI", {
      configurable: true,
      value: { appInfo: { version: "1.2.3" } },
    });
    Object.defineProperty(window, "updater", {
      configurable: true,
      value: {
        getPreferences: mocks.getPreferences,
        setAutomaticUpdates: mocks.setAutomaticUpdates,
        setReleaseChannel: mocks.setReleaseChannel,
        checkForUpdates: mocks.checkForUpdates,
      },
    });
  });

  it("loads the persisted preference and saves changes from the switch", async () => {
    mocks.getPreferences.mockResolvedValue({
      automaticUpdates: false,
      releaseChannel: "stable",
    });
    mocks.setAutomaticUpdates.mockResolvedValue({
      automaticUpdates: true,
      releaseChannel: "stable",
    });
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

  it("switches the release channel and checks for updates immediately", async () => {
    mocks.getPreferences.mockResolvedValue({
      automaticUpdates: true,
      releaseChannel: "test",
    });
    mocks.setReleaseChannel.mockResolvedValue({
      automaticUpdates: true,
      releaseChannel: "stable",
    });
    mocks.checkForUpdates.mockResolvedValue({
      ok: true,
      currentVersion: "1.2.3",
      latestVersion: "0.5.4",
      available: true,
    });
    render(<UpdatesSettingsTab />);

    const channel = screen.getByRole("combobox", { name: "Update channel" });
    await waitFor(() => expect(channel).toBeEnabled());
    expect(channel).toHaveValue("test");
    expect(
      screen.getByRole("option", {
        name: "Test (updates more often, may be unstable)",
      }),
    ).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "Stable" })).toBeInTheDocument();
    expect(screen.getByText(/same install/i)).toBeInTheDocument();
    expect(screen.queryByText(/reinstall/i)).not.toBeInTheDocument();

    fireEvent.change(channel, { target: { value: "stable" } });

    await waitFor(() => {
      expect(mocks.setReleaseChannel).toHaveBeenCalledWith("stable");
      expect(mocks.checkForUpdates).toHaveBeenCalledTimes(1);
      expect(channel).toHaveValue("stable");
    });
  });
});
