import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  openAlignIssue,
  openCreateIssueWithPreference,
  useCreateModeStore,
} from "./create-mode-store";
import { useModalStore } from "../../modals";

describe("openCreateIssueWithPreference", () => {
  const initialMode = useCreateModeStore.getState().lastMode;

  beforeEach(() => {
    useModalStore.getState().close();
  });

  afterEach(() => {
    useCreateModeStore.getState().setLastMode(initialMode);
    useModalStore.getState().close();
  });

  it("opens quick-create-issue when last mode is agent", () => {
    useCreateModeStore.getState().setLastMode("agent");
    openCreateIssueWithPreference();
    expect(useModalStore.getState().modal).toBe("quick-create-issue");
    expect(useModalStore.getState().data).toBeNull();
  });

  it("opens create-issue when last mode is manual", () => {
    useCreateModeStore.getState().setLastMode("manual");
    openCreateIssueWithPreference();
    expect(useModalStore.getState().modal).toBe("create-issue");
  });

  it("forwards seed data to whichever modal is opened", () => {
    useCreateModeStore.getState().setLastMode("manual");
    openCreateIssueWithPreference({ project_id: "p1" });
    expect(useModalStore.getState().modal).toBe("create-issue");
    expect(useModalStore.getState().data).toEqual({ project_id: "p1" });

    useCreateModeStore.getState().setLastMode("agent");
    openCreateIssueWithPreference({ project_id: "p2" });
    expect(useModalStore.getState().modal).toBe("quick-create-issue");
    expect(useModalStore.getState().data).toEqual({ project_id: "p2" });
  });
});

// DENE-370: alignment is the create-issue shell's third MODE, so its entry
// opens that one modal carrying the mode — there is no `create-issue-draft`
// modal type left for an entry point to open.
describe("openAlignIssue", () => {
  beforeEach(() => {
    useModalStore.getState().close();
  });

  afterEach(() => {
    useModalStore.getState().close();
  });

  it("opens the create-issue shell on its alignment face", () => {
    openAlignIssue();
    expect(useModalStore.getState().modal).toBe("create-issue");
    expect(useModalStore.getState().data).toEqual({ initial_mode: "align" });
  });

  it("carries seed data alongside the mode", () => {
    openAlignIssue({ project_id: "p1" });
    expect(useModalStore.getState().data).toEqual({
      initial_mode: "align",
      project_id: "p1",
    });
  });

  it("leaves the remembered filing preference untouched", () => {
    // The alignment entry is not a filing mode, so opening it must not change
    // what the `c` shortcut / "New issue" opens next.
    useCreateModeStore.getState().setLastMode("manual");
    openAlignIssue();
    useModalStore.getState().close();
    openCreateIssueWithPreference();
    expect(useModalStore.getState().modal).toBe("create-issue");
    expect(useModalStore.getState().data).toBeNull();
  });
});
