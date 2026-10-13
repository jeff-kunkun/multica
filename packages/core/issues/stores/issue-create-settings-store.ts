"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { createWorkspaceAwareStorage, registerForWorkspaceRehydration } from "../../platform/workspace-storage";
import { defaultStorage } from "../../platform/storage";

export type ManualCreateField =
  | "status"
  | "priority"
  | "assignee"
  | "labels"
  | "project"
  | "due_date"
  | "start_date";

// Canonical field order — the settings tab renders rows in this order and
// setters normalize persisted arrays against it, so a toggle sequence never
// produces two different persisted encodings of the same selection.
export const MANUAL_CREATE_FIELDS: ManualCreateField[] = [
  "status",
  "priority",
  "assignee",
  "labels",
  "project",
  "due_date",
  "start_date",
];

// Mirrors the manual dialog's historical toolbar: these five always rendered,
// while due/start date lived behind the ⋯ overflow.
export const DEFAULT_MANUAL_CREATE_FIELDS: ManualCreateField[] = [
  "status",
  "priority",
  "assignee",
  "labels",
  "project",
];

// Which optional fields the create-issue dialog keeps on its toolbar. Owned by
// Settings → Preferences → Issue creation; a field toggled off here stays
// reachable from the dialog's ⋯ overflow and always re-surfaces while it holds
// a value. Also holds the dialog's "create another" toggle. Per-workspace via
// the workspace-aware storage (projects and custom properties differ per
// workspace), per-user for free from localStorage being browser-profile-local.
interface IssueCreateSettingsState {
  manualCreateFields: ManualCreateField[];
  setManualCreateFieldVisible: (field: ManualCreateField, visible: boolean) => void;
  keepOpen: boolean;
  setKeepOpen: (v: boolean) => void;
}

function toggle<F extends string>(all: F[], current: F[], field: F, visible: boolean): F[] {
  return all.filter((f) => (f === field ? visible : current.includes(f)));
}

export const useIssueCreateSettingsStore = create<IssueCreateSettingsState>()(
  persist(
    (set) => ({
      manualCreateFields: DEFAULT_MANUAL_CREATE_FIELDS,
      setManualCreateFieldVisible: (field, visible) =>
        set((s) => ({
          manualCreateFields: toggle(MANUAL_CREATE_FIELDS, s.manualCreateFields, field, visible),
        })),
      keepOpen: false,
      setKeepOpen: (v) => set({ keepOpen: v }),
    }),
    {
      name: "multica_issue_create_settings",
      storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
      merge: (persistedState, currentState) => {
        // Older builds also persisted `quickCreateFields` for the retired
        // agent-written create flow; it is dropped here.
        const persisted = (persistedState ?? {}) as Partial<IssueCreateSettingsState>;
        return {
          ...currentState,
          manualCreateFields: persisted.manualCreateFields ?? DEFAULT_MANUAL_CREATE_FIELDS,
          keepOpen: persisted.keepOpen === true,
        };
      },
    },
  ),
);

registerForWorkspaceRehydration(() => useIssueCreateSettingsStore.persist.rehydrate());
