import { afterEach, describe, expect, it } from "vitest";
import { screen } from "@testing-library/react";
import { configStore } from "@multica/core/config";
import { OFFICE_WATCHDOG_GROUP_REMINDER_FLAG } from "@multica/core/feature-flags";
import { renderWithI18n } from "../../test/i18n";
import { IssueGroupReminderChip } from "./issue-group-reminder-chip";

const reminder = {
  group_reminded_at: new Date(Date.now() - 5 * 60_000).toISOString(),
  group_reminder_count: 2,
  task_id: "task-1",
};

afterEach(() => {
  configStore.getState().setFeatureFlags({});
  configStore.getState().setLocalFeatureFlags({});
});

describe("IssueGroupReminderChip", () => {
  it("says the project group was reminded, with count and time, in a flagged build", () => {
    configStore.getState().setLocalFeatureFlags({ [OFFICE_WATCHDOG_GROUP_REMINDER_FLAG]: true });
    renderWithI18n(<IssueGroupReminderChip reminder={reminder} />);
    const chip = screen.getByRole("status");
    expect(chip).toHaveTextContent("Group reminded");
    expect(chip.getAttribute("title")).toContain("reminded 2 times");
    expect(chip.getAttribute("title")).toContain("5m ago");
  });

  it("stays hidden without the flag, and a build that turns it off wins over the server", () => {
    renderWithI18n(<IssueGroupReminderChip reminder={reminder} />);
    expect(screen.queryByRole("status")).toBeNull();
    configStore.getState().setFeatureFlags({ [OFFICE_WATCHDOG_GROUP_REMINDER_FLAG]: true });
    configStore.getState().setLocalFeatureFlags({ [OFFICE_WATCHDOG_GROUP_REMINDER_FLAG]: false });
    renderWithI18n(<IssueGroupReminderChip reminder={reminder} />);
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("renders nothing when the server sent no reminder", () => {
    configStore.getState().setLocalFeatureFlags({ [OFFICE_WATCHDOG_GROUP_REMINDER_FLAG]: true });
    renderWithI18n(<IssueGroupReminderChip />);
    expect(screen.queryByRole("status")).toBeNull();
  });
});
