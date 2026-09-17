"use client";

import { create } from "zustand";

/**
 * Why an alignment's first turn never reached the carrier, per draft (DENE-425).
 *
 * The entry dialog is the only surface that sees the failure — it makes the
 * create, sends the first turn, and closes — and the page it hands off to is
 * the only surface that can say so. The request itself is safe either way (the
 * seed draft holds it), so the fact has to travel: a conversation that opens on
 * an empty transcript with no explanation reads as "nothing is wrong", and the
 * only thing the user can do about it is retype into the composer, which is
 * exactly what nobody will do without being told.
 *
 * The caches are not the channel for this. `chatKeys.messages` is empty for a
 * lost turn and also for a turn still in flight, and "empty transcript" cannot
 * tell the two apart on its own; the mutation knows which one happened, so it
 * records the reason here and the page reads it back.
 *
 * Client/view state, never persisted: it describes one screen transition inside
 * this session, and a reload lands on the page with the transcript the server
 * actually holds. Not keyed by workspace either — a draft id is already unique
 * across workspaces.
 */
interface IssueDraftSeedFailureState {
  /** Draft id to the reason its first turn never left ("" when the failure
   *  carried none of its own: a 5xx or a transport error). */
  failures: Record<string, string>;
  set: (draftId: string, reason: string) => void;
  clear: (draftId: string) => void;
}

export const useIssueDraftSeedFailureStore = create<IssueDraftSeedFailureState>(
  (set) => ({
    failures: {},
    set: (draftId, reason) =>
      set((state) => ({
        failures: { ...state.failures, [draftId]: reason },
      })),
    clear: (draftId) =>
      set((state) => {
        if (!(draftId in state.failures)) return state;
        const failures = { ...state.failures };
        delete failures[draftId];
        return { failures };
      }),
  }),
);

/**
 * Records that this alignment's first turn was lost.
 *
 * `reason` is what the page shows the user; the server's own 4xx wording is the
 * useful half of it, so it is passed through as given and an empty string means
 * the failure said nothing worth repeating.
 */
export function markIssueDraftSeedFailure(
  draftId: string,
  reason: string,
): void {
  // An unaddressable create has no page to carry this to, and "" is not a draft
  // id: keying on it would put one draft's reason under every future one.
  if (!draftId) return;
  useIssueDraftSeedFailureStore.getState().set(draftId, reason);
}

export function clearIssueDraftSeedFailure(draftId: string): void {
  useIssueDraftSeedFailureStore.getState().clear(draftId);
}

/** The recorded reason for this draft's lost first turn, or null. */
export function useIssueDraftSeedFailure(draftId: string): string | null {
  return useIssueDraftSeedFailureStore(
    (state) => state.failures[draftId] ?? null,
  );
}
