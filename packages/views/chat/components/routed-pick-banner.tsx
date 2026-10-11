"use client";

import { Route } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import type { ChatRoutingReceipt } from "@multica/core/types";
import { CHAT_COLUMN, CHAT_GUTTER } from "./chat-column";
import { useT } from "../../i18n";

/** One line over the composer, in the banner slot NoAgentBanner uses: before
 *  the first send, that routing will pick the agent; after it, who was picked
 *  and why (the receipt's own source label and reason). */
export function RoutedPickBanner({ receipt }: { receipt: ChatRoutingReceipt | null }) {
  const { t } = useT("chat");
  const text = receipt
    ? t(($) => $.window.routed_result) +
      routedResultText(receipt, t(($) => $.window.routed_result_offline))
    : t(($) => $.window.routed_pending);
  return (
    <div className={cn(CHAT_GUTTER, "mb-1.5")}>
      <div
        className={cn(
          CHAT_COLUMN,
          "flex items-center gap-1.5 rounded-md px-2.5 py-1.5 text-caption bg-muted text-muted-foreground ring-1 ring-border",
        )}
        data-testid="routed-pick-banner"
      >
        <Route className="size-3.5 shrink-0" />
        <span className="min-w-0 break-words">{text}</span>
      </div>
    </div>
  );
}

/** "由路由选派：<agent>（<source> · <tier>）<reason>" — every part comes from
 *  the server receipt so the three faces read the same line. */
export function routedResultText(receipt: ChatRoutingReceipt, offlineNote: string): string {
  const tag = [receipt.source, receipt.tier_label || receipt.tier]
    .filter((s) => !!s && s.trim() !== "")
    .join(" · ");
  const head = `${receipt.agent ?? receipt.agent_id ?? ""}${tag ? `（${tag}）` : ""}`;
  const reason = receipt.reason?.trim() ? ` ${receipt.reason.trim()}` : "";
  const offline = receipt.offline ? ` ${offlineNote}` : "";
  return `${head}${reason}${offline}`.trim();
}
