"use client";

import { createContext, useContext, useEffect, useRef } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArrowUpRight, ChevronRight } from "lucide-react";
import { useWorkspaceId } from "@multica/core/hooks";
import { issueDetailOptions } from "@multica/core/issues/queries";
import { useWorkspacePaths } from "@multica/core/paths";
import { cn } from "@multica/ui/lib/utils";
import { AppLink } from "../../navigation";
import { useT } from "../../i18n";
import { useParentIssueRef, type ParentIssueRef } from "../surface/parent-issue-context";
import { parentGroupTint, shortParentTitle } from "../utils/parent-groups";

/**
 * What a board column needs to draw parent groups (DENE-2760). Null when
 * `office_parent_groups` is off, and the column renders exactly as before.
 */
export interface BoardParentGroupsValue {
  colorOf: (parentId: string) => string | undefined;
  isCollapsed: (columnId: string, parentId: string) => boolean;
  toggle: (columnId: string, parentId: string) => void;
}

export const BoardParentGroupsContext = createContext<BoardParentGroupsValue | null>(null);

export function useBoardParentGroups(): BoardParentGroupsValue | null {
  return useContext(BoardParentGroupsContext);
}

/**
 * The parent from the surface's loaded issues, else one point read. A parent
 * outside the loaded set (a hidden column, a later page) still has to name
 * its group; every header and tab of one parent shares the one query.
 */
function useParentGroupRef(parentId: string): ParentIssueRef | null {
  const wsId = useWorkspaceId();
  const loaded = useParentIssueRef(parentId);
  const { data } = useQuery({
    ...issueDetailOptions(wsId, parentId),
    enabled: !loaded,
    staleTime: 60_000,
  });
  if (loaded) return loaded;
  return data
    ? { id: data.id, number: data.number, identifier: data.identifier, title: data.title }
    : null;
}

/** 「● 2734 防卡死 · 3 个」: the parent's color, number, short title, count. */
function ParentGroupChip({
  parent,
  color,
  count,
}: {
  parent: ParentIssueRef | null;
  color: string | undefined;
  count: number;
}) {
  const { t } = useT("issues");
  return (
    <span
      className="inline-flex min-w-0 max-w-full items-center gap-1.5 rounded-full bg-muted/60 px-2 py-0.5 text-caption font-medium text-foreground"
      style={color ? { backgroundColor: parentGroupTint(color) } : undefined}
    >
      <span
        aria-hidden
        className="size-2 shrink-0 rounded-full bg-muted-foreground/40"
        style={color ? { backgroundColor: color } : undefined}
      />
      {parent ? (
        <>
          <span className="shrink-0 tabular-nums">{parent.number}</span>
          <span className="min-w-0 truncate">{shortParentTitle(parent.title)}</span>
        </>
      ) : (
        <span className="min-w-0 truncate">{t(($) => $.board.parent_group.unknown_parent)}</span>
      )}
      <span className="shrink-0 tabular-nums text-muted-foreground">
        {t(($) => $.board.parent_group.count, { count })}
      </span>
    </span>
  );
}

/** Heads one parent's sub-issues in one column; the press folds them. */
export function BoardParentGroupHeader({
  columnId,
  parentId,
  count,
  collapsed,
}: {
  columnId: string;
  parentId: string;
  count: number;
  collapsed: boolean;
}) {
  const { t } = useT("issues");
  const groups = useBoardParentGroups();
  const parent = useParentGroupRef(parentId);
  const paths = useWorkspacePaths();
  if (!groups) return null;
  return (
    <div data-testid="board-parent-group" className="flex min-w-0 items-center gap-0.5">
      <button
        type="button"
        aria-expanded={!collapsed}
        title={parent ? `${parent.identifier} · ${parent.title}` : undefined}
        onClick={() => groups.toggle(columnId, parentId)}
        className="flex min-w-0 flex-1 items-center gap-1 rounded-md py-0.5 pr-1 text-left hover:bg-muted/60 max-md:min-h-11"
      >
        <ChevronRight
          aria-hidden
          className={cn(
            "size-3.5 shrink-0 text-muted-foreground transition-transform",
            !collapsed && "rotate-90",
          )}
        />
        <ParentGroupChip parent={parent} color={groups.colorOf(parentId)} count={count} />
      </button>
      {parent && (
        <AppLink
          href={paths.issueDetail(parent.id)}
          newTabTitle={parent.identifier}
          aria-label={t(($) => $.parent_issue_badge.open, {
            identifier: parent.identifier,
            title: parent.title,
          })}
          className="inline-flex size-6 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-muted/60 hover:text-foreground max-md:size-11"
        >
          <ArrowUpRight className="size-3.5" />
        </AppLink>
      )}
    </div>
  );
}

function ParentTab({
  parentId,
  count,
  color,
  selected,
  onSelect,
}: {
  parentId: string;
  count: number;
  color: string | undefined;
  selected: boolean;
  onSelect: (parentId: string | null) => void;
}) {
  const parent = useParentGroupRef(parentId);
  const ref = useRef<HTMLButtonElement>(null);
  // On a narrow screen the row scrolls sideways; keep the selected tab on it.
  useEffect(() => {
    if (selected) ref.current?.scrollIntoView?.({ block: "nearest", inline: "center" });
  }, [selected]);
  return (
    <button
      ref={ref}
      type="button"
      aria-pressed={selected}
      title={parent ? `${parent.identifier} · ${parent.title}` : undefined}
      onClick={() => onSelect(selected ? null : parentId)}
      className={cn(
        "flex max-w-60 shrink-0 items-center rounded-full outline-offset-1 max-md:min-h-11",
        !selected && "opacity-80 hover:opacity-100",
      )}
    >
      {/* The selection ring hugs the chip, not the taller touch target. */}
      <span
        className={cn("flex min-w-0 rounded-full outline-offset-1", selected && "outline-2")}
        style={selected && color ? { outlineColor: color, outlineStyle: "solid" } : undefined}
      >
        <ParentGroupChip parent={parent} color={color} count={count} />
      </span>
    </button>
  );
}

/**
 * The tab row over the board: 「全部」 plus one tab per parent with sub-issues
 * on the board. A tab narrows every column to that parent's group; pressing it
 * again, or 「全部」, returns the board. Nothing renders without parents.
 */
export function BoardParentTabs({
  parents,
  selected,
  colorOf,
  onSelect,
}: {
  parents: ReadonlyArray<{ id: string; count: number }>;
  selected: string | null;
  colorOf: (parentId: string) => string | undefined;
  onSelect: (parentId: string | null) => void;
}) {
  const { t } = useT("issues");
  if (parents.length === 0) return null;
  return (
    <div
      role="group"
      aria-label={t(($) => $.board.parent_group.tabs_label)}
      className="flex shrink-0 items-center gap-1.5 overflow-x-auto px-2 pt-2 pb-1 [scrollbar-width:none]"
    >
      <button
        type="button"
        aria-pressed={selected === null}
        onClick={() => onSelect(null)}
        className="flex shrink-0 items-center rounded-full outline-offset-1 max-md:min-h-11"
      >
        <span
          className={cn(
            "rounded-full px-2.5 py-0.5 text-caption font-medium",
            selected === null
              ? "bg-foreground text-background"
              : "bg-muted/60 text-muted-foreground hover:text-foreground",
          )}
        >
          {t(($) => $.board.parent_group.all)}
        </span>
      </button>
      {parents.map((parent) => (
        <ParentTab
          key={parent.id}
          parentId={parent.id}
          count={parent.count}
          color={colorOf(parent.id)}
          selected={selected === parent.id}
          onSelect={onSelect}
        />
      ))}
    </div>
  );
}
