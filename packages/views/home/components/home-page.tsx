"use client";

import { createContext, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { ChevronDown, ChevronRight, FolderKanban, Sparkles } from "lucide-react";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { useActorName } from "@multica/core/workspace/hooks";
import {
  BOARD_LANES,
  splitSeenDone,
  useBoardProject,
  useDoneSeenStore,
  useInboxBoard,
  type BoardLane,
  type BoardProject,
  type BoardRow,
  type InboxBoard,
} from "@multica/core/home";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { cn } from "@multica/ui/lib/utils";
import { PageHeader } from "../../layout/page-header";
import { AppLink, resolveClickIntent, useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { PickerItem, PropertyPicker, PICKER_TRIGGER_CLASS } from "../../issues/components/pickers/property-picker";
import { ProjectIcon } from "../../projects/components/project-icon";
import { useTimeAgo } from "../../inbox/components/inbox-list-item";
import { InboxLayerTabs } from "../../inbox/components/inbox-layer-tabs";
import { IssuePeekHost } from "../../issues/components/issue-peek";
import {
  PEEK_TARGET_ATTR,
  useIssuePeekActions,
  useIsIssuePeeked,
} from "../../issues/surface/peek-context";


export const LANE_TAG_CLASS: Record<BoardLane, string> = {
  running: "bg-success/10 text-success",
  blocked: "bg-muted text-foreground",
  todo: "bg-primary/10 text-primary",
  fresh: "bg-info/10 text-info",
  done: "bg-muted text-muted-foreground",
};

const LANE_PILL_CLASS: Record<BoardLane, string> = {
  running: "border-success/30 text-success",
  blocked: "text-foreground",
  todo: "border-primary/30 text-primary",
  fresh: "border-info/30 text-info",
  done: "text-muted-foreground",
};

function useBoardCopy() {
  const { t } = useT("inbox");
  const { getActorName } = useActorName();
  const timeAgo = useTimeAgo();

  {
    const tag = (row: BoardRow): string => {
      if (row.lane === "fresh") return t(($) => $.board.tag.fresh);
      if (row.lane === "todo") return t(($) => $.board.tag.todo);
      if (row.lane === "blocked") return t(($) => $.board.tag.blocked);
      return row.lane === "running" ? t(($) => $.board.tag.running) : t(($) => $.board.tag.done);
    };
    const ownerName = (o: { type: string; id: string }): string =>
      o.type === "issue" ? t(($) => $.board.next_issue, { id: o.id }) : getActorName(o.type, o.id);
    const meta = (row: BoardRow): string => {
      switch (row.lane) {
        case "running":
          return row.next ? getActorName(row.next.type, row.next.id) : "";
        case "blocked":
          return row.next ? ownerName(row.next) : "";
        default:
          return "";
      }
    };
    return { t, tag, meta, timeAgo };
  }
}

type BoardCopy = ReturnType<typeof useBoardCopy>;

/**
 * Set when the board sits beside the notification list in the merged inbox
 * (DENE-1004): rows open in the page's own detail pane instead of navigating
 * away, and lane headings filter the list.
 */
export interface BoardLinking {
  /** The issue to mark on the board: the open one, or the one just closed. */
  highlightIssueId?: string;
  activeLane: BoardLane | null;
  onSelectIssue: (issueId: string) => void;
  onToggleLane: (lane: BoardLane) => void;
}

const BoardLinkingContext = createContext<BoardLinking | null>(null);

function BoardRowView({
  row,
  copy,
  nested = false,
}: {
  row: BoardRow;
  copy: BoardCopy;
  nested?: boolean;
}) {
  const wsPaths = useWorkspacePaths();
  const { push } = useNavigation();
  const linking = useContext(BoardLinkingContext);
  const peek = useIssuePeekActions();
  const peeked = useIsIssuePeeked(row.issueId);
  const [open, setOpen] = useState(false);
  const href = wsPaths.issueDetail(row.issueId);
  const activate = () => {
    if (peek) {
      peek.open(row.issueId);
      linking?.onSelectIssue(row.issueId);
      return;
    }
    if (linking) {
      linking.onSelectIssue(row.issueId);
      return;
    }
    push(href);
  };
  const highlighted = peeked || (!!linking && linking.highlightIssueId === row.issueId);
  const rowRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (highlighted) rowRef.current?.scrollIntoView({ block: "nearest" });
  }, [highlighted]);
  const meta = copy.meta(row);
  const expandable = row.children.length > 0;
  const time =
    row.lane === "running"
      ? copy.t(($) => $.board.running_for, { time: copy.timeAgo(row.at) })
      : copy.timeAgo(row.at);

  return (
    <div
      ref={rowRef}
      className={cn(!nested && "border-b last:border-b-0")}
      data-testid={`board-row-${row.lane}`}
      data-highlighted={highlighted ? "" : undefined}
    >
      <div
        role="link"
        tabIndex={0}
        {...{ [PEEK_TARGET_ATTR]: row.issueId }}
        onClick={activate}
        onKeyDown={(e) => {
          if (e.key === "Enter") activate();
        }}
        className={cn(
          "group grid cursor-pointer gap-3 px-4 py-3 outline-none transition-colors hover:bg-accent/40 focus-visible:bg-accent/40",
          // Narrow screens stack the title under the tag/meta line; a fixed
          // tag column there leaves the title one glyph wide.
          "grid-cols-[minmax(0,1fr)_auto] sm:grid-cols-[6.5rem_minmax(0,1fr)_auto]",
          nested && "py-2 pl-4 sm:pl-8",
          highlighted && "bg-accent/60 shadow-[inset_3px_0_0_var(--color-primary)]",
        )}
      >
        <span
          className={cn(
            "order-1 h-fit w-fit rounded-sm sm:order-none px-1.5 py-0.5 text-caption font-medium",
            LANE_TAG_CLASS[row.lane],
          )}
        >
          {copy.tag(row)}
        </span>
        <div className="order-3 col-span-2 min-w-0 space-y-0.5 sm:order-none sm:col-span-1">
          <div className="flex min-w-0 items-baseline gap-2">
            <span className="shrink-0 text-caption text-muted-foreground tabular-nums">
              {row.identifier}
            </span>
            <AppLink
              href={href}
              onClick={(e) => {
                e.stopPropagation();
                // A plain click stays in the merged inbox; modifier clicks
                // still open the issue as its own page or tab.
                if ((linking || peek) && resolveClickIntent(e) === "push") {
                  e.preventDefault();
                  activate();
                }
              }}
              className="min-w-0 break-words text-body font-medium hover:underline sm:truncate"
            >
              {row.title}
            </AppLink>
            {row.unread > 0 && (
              <span
                className="inline-flex shrink-0 items-center gap-1 text-[11px] font-medium text-info"
                data-testid="board-row-unread"
              >
                <span className="size-1.5 rounded-full bg-info" aria-hidden />
                {copy.t(($) => $.board.unread, { count: row.unread })}
              </span>
            )}
          </div>
          {expandable && (
            <button
              type="button"
              onClick={(e) => {
                e.stopPropagation();
                setOpen((v) => !v);
              }}
              className="mt-1 inline-flex items-center gap-1 text-caption text-muted-foreground hover:text-foreground"
            >
              {open ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
              {copy.t(($) => $.board.children, { count: row.children.length })}
            </button>
          )}
        </div>
        <div className="order-2 flex min-w-0 flex-col items-end gap-0.5 text-right text-caption text-muted-foreground sm:order-none">
          {meta && <span className="text-foreground">{meta}</span>}
          <div className="flex items-center gap-2">
            <span>{time}</span>
          </div>
        </div>
      </div>
      {open && (
        <div className={cn("space-y-3 px-4 pb-3 sm:pl-[8.75rem]", nested && "sm:pl-[10.75rem]")}>
          <div className="rounded-md border">
            {row.children.map((child) => (
              <BoardRowView key={child.issueId} row={child} copy={copy} nested />
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

function LaneSection({
  lane,
  rows,
  copy,
  footer,
}: {
  lane: BoardLane;
  rows: BoardRow[];
  copy: BoardCopy;
  footer?: ReactNode;
}) {
  const linking = useContext(BoardLinkingContext);
  const title = copy.t(($) => $.board.lanes[lane].title);
  const hint = copy.t(($) => $.board.lanes[lane].hint);
  const active = linking?.activeLane === lane;
  return (
    <section aria-label={title} data-testid={`board-lane-${lane}`}>
      <div className="mb-2 flex items-baseline gap-2">
        {linking ? (
          <h2 className="text-body font-semibold">
            <button
              type="button"
              aria-pressed={active}
              title={copy.t(($) => $.board.lane_filter_hint)}
              onClick={() => linking.onToggleLane(lane)}
              data-testid={`board-lane-filter-${lane}`}
              className={cn(
                "rounded-sm outline-none transition-colors hover:text-primary focus-visible:ring-1 focus-visible:ring-ring",
                active && "text-primary",
              )}
            >
              {title}
              <span className="ml-1.5 font-normal text-muted-foreground tabular-nums">{rows.length}</span>
            </button>
          </h2>
        ) : (
          <h2 className="text-body font-semibold">{title}</h2>
        )}
        <span className="text-caption text-muted-foreground">{hint}</span>
      </div>
      <div className={cn("overflow-hidden rounded-lg border bg-card", active && "ring-2 ring-primary/60")}>
        {rows.length === 0 && !footer ? (
          <p className="px-4 py-3 text-caption text-muted-foreground">
            {copy.t(($) => $.board.lanes[lane].empty)}
          </p>
        ) : (
          rows.map((row) => <BoardRowView key={row.issueId} row={row} copy={copy} />)
        )}
        {footer}
      </div>
    </section>
  );
}

/**
 * The board's lanes (DENE-882): one row per issue. Rows that were unread on
 * arrival keep a marker for this visit (DENE-901). Shared by the stand-alone
 * board and the merged inbox, which passes `linking`.
 */
export function InboxBoardLanes({
  board,
  isLoading,
  isError,
  linking,
}: {
  board: InboxBoard;
  isLoading: boolean;
  isError: boolean;
  linking?: BoardLinking;
}) {
  const wsId = useWorkspaceId();
  const copy = useBoardCopy();
  const peek = useIssuePeekActions();

  // "Done today" shows once. Read the mark left by the previous visit, then
  // move it to now — on arrival and again on leaving, so rows that finish
  // while the page is open also count as seen next time.
  const [seenBefore] = useState(() => useDoneSeenStore.getState().seenAt[wsId] ?? null);
  const markSeen = useDoneSeenStore((s) => s.markSeen);
  useEffect(() => {
    markSeen(wsId, new Date().toISOString());
    return () => markSeen(wsId, new Date().toISOString());
  }, [markSeen, wsId]);
  const done = useMemo(() => splitSeenDone(board.done, seenBefore), [board.done, seenBefore]);
  const [showSeen, setShowSeen] = useState(false);
  // The issue being pointed at may sit among the folded rows.
  const highlightSeen = !!linking?.highlightIssueId && done.seen.some((r) => r.issueId === linking.highlightIssueId);

  const peekOrder = useMemo(() => {
    const ordered: string[] = [];
    const visit = (rows: BoardRow[]) => rows.forEach((row) => {
      ordered.push(row.issueId);
      visit(row.children);
    });
    visit(board.running);
    visit(board.blocked);
    visit(board.todo);
    visit(board.fresh);
    visit(done.fresh);
    if (showSeen || highlightSeen) visit(done.seen);
    return ordered;
  }, [board, done.fresh, done.seen, highlightSeen, showSeen]);

  useEffect(() => {
    if (!peek) return;
    peek.publishColumns([peekOrder]);
    return () => peek.publishColumns(null);
  }, [peek, peekOrder]);

  const seenFooter =
    done.seen.length > 0 ? (
      <>
        {(showSeen || highlightSeen) && done.seen.map((row) => <BoardRowView key={row.issueId} row={row} copy={copy} />)}
        <button
          type="button"
          onClick={() => setShowSeen((v) => !v)}
          className="flex w-full items-center gap-1 border-t px-4 py-2 text-left text-caption text-muted-foreground hover:bg-accent/40 hover:text-foreground"
        >
          {showSeen ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
          {showSeen
            ? copy.t(($) => $.board.hide_seen)
            : copy.t(($) => $.board.seen, { count: done.seen.length })}
        </button>
      </>
    ) : undefined;

  return (
    <BoardLinkingContext.Provider value={linking ?? null}>
      <div className="space-y-3">
        <div className="flex flex-wrap gap-2">
          {BOARD_LANES.map((lane) => {
            const pill = (
              <>
                <b className="mr-1 font-semibold tabular-nums">{board[lane].length}</b>
                {copy.t(($) => $.board.lanes[lane].title)}
              </>
            );
            const className = cn("rounded-full border px-2.5 py-0.5 text-caption", LANE_PILL_CLASS[lane]);
            return linking ? (
              <button
                key={lane}
                type="button"
                aria-pressed={linking.activeLane === lane}
                onClick={() => linking.onToggleLane(lane)}
                className={cn(className, "transition-colors hover:bg-accent/60", linking.activeLane === lane && "bg-accent")}
              >
                {pill}
              </button>
            ) : (
              <span key={lane} className={className}>
                {pill}
              </span>
            );
          })}
        </div>
        {isError && <p className="text-caption text-destructive">{copy.t(($) => $.board.load_error)}</p>}
      </div>
      {isLoading ? (
        <div className="space-y-3">
          <Skeleton className="h-24 w-full" />
          <Skeleton className="h-24 w-full" />
        </div>
      ) : (
        <>
          <LaneSection lane="running" rows={board.running} copy={copy} />
          {board.blocked.length > 0 && <LaneSection lane="blocked" rows={board.blocked} copy={copy} />}
          <LaneSection lane="todo" rows={board.todo} copy={copy} />
          {board.fresh.length > 0 && <LaneSection lane="fresh" rows={board.fresh} copy={copy} />}
          <LaneSection lane="done" rows={done.fresh} copy={copy} footer={seenFooter} />
        </>
      )}
    </BoardLinkingContext.Provider>
  );
}

/**
 * The inbox board on its own page — the compact-width inbox (DENE-882). Wide
 * screens show it beside the notification list instead (DENE-1004).
 */
function HomePageContent() {
  const wsId = useWorkspaceId();
  const copy = useBoardCopy();
  const project = useBoardProject(wsId);
  const { board, isLoading, isError } = useInboxBoard(wsId, { projectId: project.projectId });

  return (
    <div className="flex h-full min-h-0 flex-col">
      <PageHeader>
        <h1 className="flex-1 text-body font-semibold">{copy.t(($) => $.board.title)}</h1>
        <BoardProjectControls project={project} />
      </PageHeader>
      <InboxLayerTabs active="board" />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto w-full max-w-4xl space-y-6 px-6 py-6">
          <InboxBoardLanes board={board} isLoading={isLoading} isError={isError} />
        </div>
      </div>
    </div>
  );
}

export function HomePage() {
  return (
    <IssuePeekHost>
      <HomePageContent />
    </IssuePeekHost>
  );
}

/**
 * The board's project dropdown and the "walk me through it" button beside it
 * (DENE-1019). The button follows the dropdown: with a project picked it sends
 * the chat there, and the prompt names the project so the agent reads the board
 * with `--project` and tells the same tickets the page shows.
 */
export function BoardProjectControls({ project }: { project: BoardProject }) {
  const { t } = useT("inbox");
  const { t: tChat } = useT("chat");
  const { projects, project: current } = project;
  const [open, setOpen] = useState(false);
  const pick = (id: string | null) => {
    project.setProjectId(id);
    setOpen(false);
  };
  const prompt = current
    ? tChat(($) => $.conversation_starters.inbox.prompt_project, { name: current.title, id: current.id })
    : tChat(($) => $.conversation_starters.inbox.prompt);
  return (
    <>
      <div className="inline-flex min-w-0" data-testid="board-project-filter">
        <PropertyPicker
          open={open}
          onOpenChange={setOpen}
          width="w-56"
          align="end"
          searchable={projects.length > 8}
          triggerRender={
            <button
              type="button"
              aria-label={t(($) => $.board.project_filter_aria)}
              className={cn(PICKER_TRIGGER_CLASS, "h-7 max-w-48 rounded-md border px-2 text-caption")}
            />
          }
          trigger={
            <>
              {current ? (
                <ProjectIcon project={current} size="sm" />
              ) : (
                <FolderKanban className="size-3.5 shrink-0 text-muted-foreground" />
              )}
              <span className="truncate">{current?.title ?? t(($) => $.board.project_all)}</span>
              <ChevronDown className="size-3 shrink-0 text-muted-foreground" />
            </>
          }
        >
          <PickerItem emptyValue selected={!current} onClick={() => pick(null)}>
            <FolderKanban className="size-3.5 text-muted-foreground" />
            <span className="text-muted-foreground">{t(($) => $.board.project_all)}</span>
          </PickerItem>
          {projects.map((p) => (
            <PickerItem key={p.id} selected={p.id === current?.id} onClick={() => pick(p.id)}>
              <ProjectIcon project={p} size="sm" />
              <span className="truncate">{p.title}</span>
            </PickerItem>
          ))}
        </PropertyPicker>
      </div>
      <BoardAskAiButton
        prompt={prompt}
        label={current ? t(($) => $.board.ask_ai_project) : t(($) => $.board.ask_ai)}
        projectIds={current ? [current.id] : []}
      />
    </>
  );
}

/** DENE-975: the chat agent reads this same board (`multica inbox board`) and tells it back. */
export function BoardAskAiButton({ prompt, label, projectIds = [] }: { prompt: string; label: string; projectIds?: readonly string[] }) {
  const wsPaths = useWorkspacePaths();
  return (
    <Button
      variant="ghost"
      size="sm"
      className="min-h-11 min-w-11 justify-center text-muted-foreground sm:min-h-0 sm:min-w-0"
      aria-label={label}
      nativeButton={false}
      render={<AppLink href={wsPaths.chatWithPrompt(prompt, ...projectIds)} data-testid="board-ask-ai" />}
    >
      <Sparkles className="size-4" />
      <span className="hidden sm:inline">{label}</span>
    </Button>
  );
}
