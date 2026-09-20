"use client";

// A clock that moves when a spent quota comes back (DENE-468).
//
// The accounts surfaces answer "is this account usable right now?" from
// `quota_reset_at`, which is a deadline rather than a boolean. Rendering that
// answer from a `Date.now()` captured at mount leaves the screen claiming
// "quota used up" long after the account came back. This hook returns the same
// clock every caller already used, except that it re-renders at the soonest
// deadline — so the summary pill and the accounts tab's warning flip back on
// their own, and neither surface keeps a timer of its own.

import { useEffect, useMemo, useState } from "react";
import { type AgentAccount, nextQuotaResetMs } from "./agent-accounts-model";

export function useQuotaResetTick(accounts: readonly AgentAccount[]): number {
  const [nowMs, setNowMs] = useState(() => Date.now());
  const nextResetMs = useMemo(
    () => nextQuotaResetMs(accounts, nowMs),
    [accounts, nowMs],
  );

  useEffect(() => {
    if (nextResetMs === null) return;
    // setTimeout saturates around 24.8 days; a further deadline simply re-arms
    // when this one fires.
    const delay = Math.min(Math.max(nextResetMs - Date.now(), 0), 2_147_000_000);
    const timer = window.setTimeout(() => setNowMs(Date.now()), delay);
    return () => window.clearTimeout(timer);
  }, [nextResetMs]);

  return nowMs;
}
