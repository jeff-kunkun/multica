import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";

const mocks = vi.hoisted(() => ({
  getPreferences: vi.fn(),
  getState: vi.fn(),
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
      channel_title: "Release channel",
      channel_description: "Stable or test",
      channel_stable: "Stable",
      channel_test: "Test",
      channel_save_failed: "Failed to save the release channel",
      check_section_title: "Check for updates",
      check_section_description: "Check manually",
      up_to_date: "Up to date",
      downloading: "Downloading v{{version}}",
      progress: "Downloading v{{version}} — {{percent}}%",
      ready: "v{{version}} downloaded",
      manual_available: "v{{version}} needs a manual download",
      download_installer: "Download installer",
      restart_now: "Restart now",
      no_test_release: "No test build yet",
      check_failed: "Couldn't check for updates.",
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
    mocks.getState.mockReset().mockResolvedValue({
      phase: "idle",
      version: null,
      percent: null,
      manualDownloadUrl: null,
      installMode: "manual",
      releaseChannel: "stable",
      error: null,
      errorCode: null,
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
        getState: mocks.getState,
        onState: () => vi.fn(),
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

  it("saves the release channel the user picks", async () => {
    mocks.setReleaseChannel.mockResolvedValue({
      automaticUpdates: true,
      releaseChannel: "test",
    });
    render(<UpdatesSettingsTab />);

    const testChannel = await screen.findByRole("radio", { name: "Test" });
    await waitFor(() => expect(testChannel).not.toBeDisabled());
    fireEvent.click(testChannel);

    await waitFor(() => {
      expect(mocks.setReleaseChannel).toHaveBeenCalledWith("test");
      expect(testChannel).toHaveAttribute("aria-checked", "true");
    });
  });
});
