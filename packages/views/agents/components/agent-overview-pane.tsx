"use client";

import { useEffect, useRef, useState } from "react";
import type {
  Agent,
  AgentRuntime,
  MemberWithUser,
} from "@multica/core/types";
import { cn } from "@multica/ui/lib/utils";
import { PAGE_GUTTER, PAGE_RAIL } from "../../layout/page-header";
import { ActivityTab } from "./tabs/activity-tab";
import { AgentOverviewSummary } from "./agent-overview-summary";
import { ActorIssuesPanel } from "../../common/actor-issues-panel";
import { useT } from "../../i18n";
import { useNavigation } from "../../navigation";

export type DetailTab = "overview" | "work";

const TABS: DetailTab[] = ["overview", "work"];

function isDetailTab(value: string | null): value is DetailTab {
  return value !== null && (TABS as string[]).includes(value);
}

interface AgentOverviewPaneProps {
  agent: Agent;
  runtime: AgentRuntime | null;
  owner: MemberWithUser | null;
}

/**
 * The office mirrors magpie: Overview shows what is happening now and Work the
 * agent's issues. How magpie runs the agent is configured in magpie itself.
 */
export function AgentOverviewPane({
  agent,
  runtime,
  owner,
}: AgentOverviewPaneProps) {
  const { t } = useT("agents");
  const navigation = useNavigation();
  const urlView = navigation.searchParams.get("view");
  const [activeView, setActiveView] = useState<DetailTab>(() =>
    isDetailTab(urlView) ? urlView : "overview",
  );
  const lastUrlViewRef = useRef(urlView);

  const selectView = (next: DetailTab) => {
    if (next === activeView) return;
    setActiveView(next);
    const params = new URLSearchParams(navigation.searchParams);
    if (next === "overview") params.delete("view");
    else params.set("view", next);
    const query = params.toString();
    navigation.replace(`${navigation.pathname}${query ? `?${query}` : ""}`);
  };

  useEffect(() => {
    if (urlView === lastUrlViewRef.current) return;
    lastUrlViewRef.current = urlView;
    setActiveView(isDetailTab(urlView) ? urlView : "overview");
  }, [urlView]);

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-background">
      <div
        className="shrink-0 overflow-x-auto border-b"
        role="tablist"
        aria-label={t(($) => $.tabs.page_navigation_aria)}
      >
        <div className={cn(PAGE_RAIL, PAGE_GUTTER, "flex items-center gap-6")}>
          {TABS.map((tab) => (
            <button
              key={tab}
              type="button"
              role="tab"
              aria-selected={activeView === tab}
              onClick={() => selectView(tab)}
              className={cn(
                "relative shrink-0 py-3 text-body font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring",
                activeView === tab
                  ? "text-foreground after:absolute after:inset-x-0 after:bottom-0 after:h-0.5 after:bg-foreground"
                  : "text-muted-foreground hover:text-foreground",
              )}
            >
              {t(($) => $.tabs[tab])}
            </button>
          ))}
        </div>
      </div>

      {/* Header, tab bar and every panel read PAGE_RAIL, so the page is one
          centred column at every width (MUL-7107). */}
      <div className="min-h-0 flex-1 overflow-y-auto">
        {activeView === "overview" && (
          <div className={cn(PAGE_RAIL, PAGE_GUTTER, "py-4 sm:py-6")}>
            <div className="grid gap-6 xl:grid-cols-[minmax(0,1fr)_320px]">
              <ActivityTab agent={agent} showPerformance={false} />
              <AgentOverviewSummary
                agent={agent}
                runtime={runtime}
                owner={owner}
              />
            </div>
          </div>
        )}

        {activeView === "work" && (
          <div className={cn(PAGE_RAIL, "flex min-h-[620px] flex-col")}>
            <ActorIssuesPanel actorType="agent" actorId={agent.id} />
          </div>
        )}
      </div>
    </div>
  );
}
