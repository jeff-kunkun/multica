"use client";

/**
 * Whether issue mentions also show who is working on the issue right now.
 * Chat opts in: an issue a chat points at is work the reader is waiting on.
 * Descriptions and comments keep the bare chip so prose stays quiet.
 */

import { createContext, useContext } from "react";

const IssueMentionActivityContext = createContext(false);

export const IssueMentionActivityProvider = IssueMentionActivityContext.Provider;

export function useIssueMentionActivity(): boolean {
  return useContext(IssueMentionActivityContext);
}
