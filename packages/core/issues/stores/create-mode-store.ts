"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { defaultStorage } from "../../platform/storage";
import { useModalStore } from "../../modals";

/**
 * Which face of the create-issue dialog is showing. `align` is a mode of the
 * SAME shell, not a second dialog: it hands the typed request to an alignment
 * conversation instead of filing an issue, and a switch between the three
 * swaps only the inner panel.
 */
export type CreateMode = "agent" | "manual" | "align";

/**
 * The filing modes the "new issue" preference can remember — `CreateMode`
 * minus the alignment face.
 *
 * Aligning is a different act with its own entry point (the sidebar's
 * alignment row), so it must never become what "which mode did I use last"
 * answers: otherwise the `c` shortcut and the sidebar's "New issue" button
 * would start opening the alignment face for someone who only ever wanted to
 * file an issue.
 */
export type FilingCreateMode = Exclude<CreateMode, "align">;

/**
 * Last create-issue mode the user landed on. Drives the global `c` shortcut
 * and the in-modal mode switch — pressing `c` opens whichever modal the user
 * used last, and the switch button in either modal updates this so the
 * preference sticks.
 *
 * Workspace-agnostic on purpose: the user's mental preference for "how do I
 * file an issue" doesn't change per workspace, so this lives in plain
 * localStorage rather than the workspace-aware StateStorage that scopes
 * per-workspace stores like quick-create-store / draft-store.
 */
interface CreateModeState {
  lastMode: FilingCreateMode;
  setLastMode: (mode: FilingCreateMode) => void;
}

export const useCreateModeStore = create<CreateModeState>()(
  persist(
    (set) => ({
      lastMode: "agent",
      setLastMode: (mode) => set({ lastMode: mode }),
    }),
    {
      name: "multica_create_mode",
      storage: createJSONStorage(() => defaultStorage),
    },
  ),
);

/**
 * Open the create-issue flow in whichever mode the user landed on last.
 * Generic entry points (sidebar button, command palette, `c` shortcut) call
 * this so the persisted preference actually takes effect; entry points that
 * pre-seed manual-only fields (status, parent_issue_id) keep opening
 * "create-issue" directly because agent mode can't honour those seeds.
 */
export function openCreateIssueWithPreference(
  data?: Record<string, unknown> | null,
) {
  const lastMode = useCreateModeStore.getState().lastMode;
  const modal = lastMode === "manual" ? "create-issue" : "quick-create-issue";
  useModalStore.getState().open(modal, data ?? null);
}

/**
 * Opens the alignment face — the create-issue shell's third mode.
 *
 * The mode rides the modal DATA (`initial_mode`), which the modal registry
 * reads: the alignment entry is an `initialMode` of the create-issue dialog
 * now, so there is no `create-issue-draft` modal type left to open.
 */
export function openAlignIssue(
  data?: Record<string, unknown> | null,
): void {
  useModalStore.getState().open("create-issue", {
    ...(data ?? {}),
    initial_mode: "align",
  });
}
