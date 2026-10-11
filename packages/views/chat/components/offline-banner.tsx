"use client";

import { WifiOff } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import type { AgentAvailability } from "@multica/core/agents";
import { CHAT_COLUMN, CHAT_GUTTER } from "./chat-column";
import { useT } from "../../i18n";

interface Props {
  /** Display name shown in the banner copy. */
  agentName?: string;
  lastSeenAt?: string | null;
  /**
   * Resolved presence availability. Pass `undefined` (or "loading") to
   * suppress the banner — we only surface known offline / unstable states,
   * never speculative copy.
   */
  availability: AgentAvailability | undefined;
}

// Inline notice rendered above the chat input when the active agent isn't
// reachable. Hides on `online`, `undefined`, or while presence is loading —
// users get the silent default behaviour and only see copy when there's a
// real-world implication for the message they're about to send.
export function OfflineBanner({ availability, lastSeenAt }: Props) {
  const { t } = useT("chat");
  if (availability !== "offline" && availability !== "unstable") return null;
  const date = lastSeenAt ? new Date(lastSeenAt) : null;
  const time = date && Number.isFinite(date.getTime())
    ? date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false })
    : null;
  return (
    <div className={cn(CHAT_GUTTER, "mb-1.5")}>
      <div role="status" className={cn(CHAT_COLUMN, "flex items-center gap-1.5 px-2.5 py-1.5 text-caption text-muted-foreground")}>
        <WifiOff className="size-3.5 shrink-0" />
        <span className="min-w-0 break-words">
          {time ? t(($) => $.offline_banner.last_seen, { time }) : t(($) => $.offline_banner.last_seen_unknown)}
        </span>
      </div>
    </div>
  );
}
