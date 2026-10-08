"use client";

import { useMemo, useRef, useState, type TouchEvent } from "react";
import { useQueries } from "@tanstack/react-query";
import { ChevronUp } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import { useIsMobile } from "@multica/ui/hooks/use-mobile";
import { Button } from "@multica/ui/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@multica/ui/components/ui/popover";
import { Sheet, SheetContent, SheetTitle } from "@multica/ui/components/ui/sheet";
import { defaultStorage } from "@multica/core/platform";
import { useWorkspacePaths } from "@multica/core/paths";
import { projectReportOptions } from "@multica/core/projects";
import { useIssueStatuses } from "@multica/core/issue-statuses";
import type { ProjectReport, ProjectReportItem } from "@multica/core/types";
import { AppLink } from "../../navigation";
import { useT, useTimeAgo } from "../../i18n";
import { CHAT_COLUMN, CHAT_GUTTER } from "./chat-column";

// DENE-1667: the chat's progress bar. It shows only when the chat's projects
// have news since the person last heard them; the window lists what moved, and
// "听汇报" asks the agent to tell it (the agent marks it heard server-side).

const SWIPE_CLOSE_PX = 80;

// When the person last opened the window, per person + project: rows that
// moved after it are marked 「刚变」 and float to the top.
function openedKey(userId: string, projectId: string) {
  return `multica:chat-report-opened:${userId}:${projectId}`;
}

function lastOpenedAt(userId: string, projectIds: string[]): number {
  let latest = 0;
  for (const id of projectIds) {
    const at = Date.parse(defaultStorage.getItem(openedKey(userId, id)) ?? "");
    if (!Number.isNaN(at)) latest = Math.max(latest, at);
  }
  return latest;
}

interface ReportRow extends ProjectReportItem {
  fresh: boolean;
}

export function ChatReportBar({
  wsId,
  userId,
  sessionId,
  projectIds,
  disabled,
  onHear,
}: {
  wsId: string;
  userId: string;
  sessionId: string | null;
  projectIds: string[];
  disabled: boolean;
  onHear: (prompt: string) => void;
}) {
  const { t } = useT("chat");
  const isMobile = useIsMobile();
  const [open, setOpen] = useState(false);
  const [seenAt, setSeenAt] = useState(() => lastOpenedAt(userId, projectIds));

  const results = useQueries({
    queries: projectIds.map((id) => ({ ...projectReportOptions(wsId, id), enabled: !!wsId })),
  });
  const reports = results
    .map((r) => r.data)
    .filter((r): r is ProjectReport => !!r && r.items.length > 0);

  const rows = useMemo<ReportRow[]>(() => {
    const all = reports.flatMap((r) =>
      r.items.map((item) => ({ ...item, fresh: Date.parse(item.changed_at) > seenAt })),
    );
    // Rows newer than the last look come first; the server's order otherwise.
    return [...all.filter((r) => r.fresh), ...all.filter((r) => !r.fresh)];
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reports.map((r) => r.until).join(), seenAt]);

  if (rows.length === 0) return null;

  const counts = { waiting_you: 0, in_progress: 0, done: 0 };
  for (const row of rows) counts[row.phase] += 1;
  const hasFresh = rows.some((r) => r.fresh);
  const title = reports.map((r) => r.project_title).join("、");
  const hear = () => {
    setOpen(false);
    onHear(t(($) => $.report.hear_prompt, { project: title }));
  };

  const setWindowOpen = (next: boolean) => {
    setOpen(next);
    if (!next) {
      // Closing the window is the "look": record it so the next opening marks
      // only what moved after it.
      const now = new Date().toISOString();
      for (const id of projectIds) defaultStorage.setItem(openedKey(userId, id), now);
      setSeenAt(Date.parse(now));
    }
  };

  const summary = (
    <span className="flex min-w-0 items-center gap-1.5">
      <span
        aria-hidden
        className={cn("size-1.5 shrink-0 rounded-full", hasFresh ? "bg-brand" : "bg-muted-foreground/40")}
      />
      <span className="truncate">
        {t(($) => $.report.bar, { count: rows.length })}
        <span className="text-muted-foreground">
          {counts.waiting_you > 0 && ` · ${t(($) => $.report.waiting_you, { count: counts.waiting_you })}`}
          {counts.in_progress > 0 && ` · ${t(($) => $.report.in_progress, { count: counts.in_progress })}`}
          {counts.done > 0 && ` · ${t(($) => $.report.done, { count: counts.done })}`}
        </span>
      </span>
      <ChevronUp className="size-3.5 shrink-0 text-muted-foreground" />
    </span>
  );
  const triggerClass =
    "flex min-w-0 flex-1 items-center rounded-md py-1 text-left text-caption hover:bg-accent/60 min-h-11 md:min-h-0";

  const panel = (
    <ReportPanel
      rows={rows}
      sessionId={sessionId}
      wsId={wsId}
      lastHeardAt={reports[0]?.last_heard_at ?? null}
      hearDisabled={disabled}
      onHear={hear}
    />
  );

  return (
    <div className={cn(CHAT_GUTTER, "pb-1")} data-slot="chat-report-bar">
      <div className={cn(CHAT_COLUMN, "flex items-center gap-2")}>
        {isMobile ? (
          <>
            <button type="button" className={triggerClass} onClick={() => setWindowOpen(true)}>
              {summary}
            </button>
            <MobileDrawer open={open} onOpenChange={setWindowOpen} title={t(($) => $.report.title)}>
              {panel}
            </MobileDrawer>
          </>
        ) : (
          <Popover open={open} onOpenChange={setWindowOpen}>
            <PopoverTrigger className={triggerClass}>{summary}</PopoverTrigger>
            <PopoverContent side="top" align="start" className="w-96 gap-0 p-0">
              {panel}
            </PopoverContent>
          </Popover>
        )}
        <Button size="sm" variant="ghost" disabled={disabled} onClick={hear} className="shrink-0">
          {t(($) => $.report.hear)}
        </Button>
      </div>
    </div>
  );
}

// The phone drawer: ✕, a tap on the mask, or a swipe down closes it.
function MobileDrawer({
  open,
  onOpenChange,
  title,
  children,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  children: React.ReactNode;
}) {
  const startY = useRef<number | null>(null);
  const [dragY, setDragY] = useState(0);
  const onTouchStart = (e: TouchEvent) => {
    startY.current = e.touches[0]?.clientY ?? null;
  };
  const onTouchMove = (e: TouchEvent) => {
    if (startY.current == null) return;
    setDragY(Math.max(0, (e.touches[0]?.clientY ?? 0) - startY.current));
  };
  const onTouchEnd = () => {
    if (dragY > SWIPE_CLOSE_PX) onOpenChange(false);
    startY.current = null;
    setDragY(0);
  };
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side="bottom"
        className="max-h-[80dvh] gap-0 p-0"
        style={dragY ? { transform: `translateY(${dragY}px)`, transition: "none" } : undefined}
      >
        <div
          className="flex shrink-0 flex-col items-center pt-2 pb-1"
          onTouchStart={onTouchStart}
          onTouchMove={onTouchMove}
          onTouchEnd={onTouchEnd}
          data-slot="chat-report-drag-handle"
        >
          <span aria-hidden className="h-1 w-10 rounded-full bg-muted-foreground/30" />
          <SheetTitle className="mt-2 self-start px-4 text-body font-medium">{title}</SheetTitle>
        </div>
        {children}
      </SheetContent>
    </Sheet>
  );
}

function ReportPanel({
  rows,
  sessionId,
  wsId,
  lastHeardAt,
  hearDisabled,
  onHear,
}: {
  rows: ReportRow[];
  sessionId: string | null;
  wsId: string;
  lastHeardAt: string | null;
  hearDisabled: boolean;
  onHear: () => void;
}) {
  const { t } = useT("chat");
  const timeAgo = useTimeAgo();
  const statuses = useIssueStatuses(wsId);
  const wsPaths = useWorkspacePaths();

  return (
    <div className="flex min-h-0 flex-col">
      <p className="border-b px-4 py-2 text-caption text-muted-foreground">
        {lastHeardAt
          ? t(($) => $.report.since_heard, { time: timeAgo(lastHeardAt) })
          : t(($) => $.report.since_week)}
      </p>
      <ul className="min-h-0 flex-1 overflow-y-auto py-1 md:max-h-80">
        {rows.map((row) => {
          const move = row.opened && !row.from_status
            ? t(($) => $.report.opened, { to: statuses.labelOf(row.status) })
            : `${statuses.labelOf(row.from_status ?? "")} → ${statuses.labelOf(row.status)}`;
          const source = row.source_chat && row.source_chat.id !== sessionId
            ? row.source_chat.accessible && row.source_chat.title
              ? t(($) => $.report.from_chat, { title: row.source_chat.title })
              : t(($) => $.report.from_other_chat)
            : null;
          return (
            <li key={row.issue_id}>
              <AppLink
                href={wsPaths.issueDetail(row.issue_id)}
                className="flex min-h-11 flex-col justify-center gap-0.5 px-4 py-1.5 hover:bg-accent/60"
              >
                <span className="flex min-w-0 items-baseline gap-2 text-body">
                  <span className="shrink-0 text-caption text-muted-foreground">{row.identifier}</span>
                  <span className="truncate">{row.title}</span>
                </span>
                <span className="line-clamp-2 text-caption text-muted-foreground">
                  {row.fresh && <span className="text-foreground">{t(($) => $.report.just_changed)}</span>}
                  {row.fresh && "："}
                  {move} · {timeAgo(row.changed_at)}
                  {row.needs_you && ` · ${t(($) => $.report.needs_you)}`}
                  {source && ` · ${source}`}
                </span>
              </AppLink>
            </li>
          );
        })}
      </ul>
      <div className="flex justify-end border-t px-3 py-2">
        <Button size="sm" disabled={hearDisabled} onClick={onHear}>
          {t(($) => $.report.hear)}
        </Button>
      </div>
    </div>
  );
}
