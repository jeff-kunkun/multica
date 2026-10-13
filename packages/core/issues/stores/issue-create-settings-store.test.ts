import { beforeEach, describe, expect, it } from "vitest";
import {
  DEFAULT_MANUAL_CREATE_FIELDS,
  useIssueCreateSettingsStore,
} from "./issue-create-settings-store";

describe("issue create settings store", () => {
  beforeEach(() => {
    useIssueCreateSettingsStore.setState({
      manualCreateFields: DEFAULT_MANUAL_CREATE_FIELDS,
    });
  });

  it("keeps manual create fields in canonical order regardless of toggle sequence", () => {
    const { setManualCreateFieldVisible } = useIssueCreateSettingsStore.getState();

    setManualCreateFieldVisible("labels", false);
    setManualCreateFieldVisible("due_date", true);

    expect(useIssueCreateSettingsStore.getState().manualCreateFields).toEqual([
      "status",
      "priority",
      "assignee",
      "project",
      "due_date",
    ]);
  });

  it("is a no-op to re-enable an already visible field", () => {
    const { setManualCreateFieldVisible } = useIssueCreateSettingsStore.getState();

    setManualCreateFieldVisible("status", true);

    expect(useIssueCreateSettingsStore.getState().manualCreateFields).toEqual(
      DEFAULT_MANUAL_CREATE_FIELDS,
    );
  });
});
