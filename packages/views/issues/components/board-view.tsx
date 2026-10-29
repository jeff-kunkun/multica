"use client";

import { useIssueStatuses } from "@multica/core/issue-statuses/hooks";
import { cn } from "@multica/ui/lib/utils";

import { useState, useCallback, useMemo, useEffect, useRef, memo } from "react";
import { useQuery } from "@tanstack/react-query";
import { useRestoredScrollRef } from "../../platform";
import {
  DndContext,
  DragOverlay,
  PointerSensor,
  useSensor,
  useSensors,
  type DragStartEvent,
  type DragEndEvent,
  type DragOverEvent,
} from "@dnd-kit/core";
import { arrayMove } from "@dnd-kit/sortable";
import { toast } from "sonner";
import type {
  Issue,
  IssueAssigneeType,
  IssueStatus,
  Project,
  IssueProperty,
} from "@multica/core/types";
import { useViewStore } from "@multica/core/issues/stores/view-store-context";
import { propertyIdFromViewKey } from "@multica/core/issues/stores/view-store";
import { propertyListOptions, useSetIssueProperty, useUnsetIssueProperty } from "@multica/core/properties";
import { useWorkspaceId } from "@multica/core/hooks";
import type { IssueGrouping } from "@multica/core/issues/stores/view-store";
import { useActorName } from "@multica/core/workspace/hooks";
import { useFeatureEnabled } from "@multica/core/config";
import { OFFICE_PARENT_GROUPS_FLAG } from "@multica/core/feature-flags";
import { BoardColumn, BOARD_CARD_WIDTH, type BoardColumnGroup } from "./board-column";
import { BoardWorkflowContext, type BoardWorkflowContextValue } from "./board-workflow-context";
import {
  BoardParentGroupsContext,
  BoardParentTabs,
  type BoardParentGroupsValue,
} from "./board-parent-groups";
import {
  assignParentGroupColors,
  groupColumnsByParent,
  parentGroupKey,
} from "../utils/parent-groups";
import {
  resolveProjectWorkflow,
  stepHandsOff,
  useIssueWorkflows,
  workflowAllowsStatus,
  workflowStep,
} from "@multica/core/issue-workflows";
import { WorkflowHandoffConfirmDialog } from "../../workflows/handoff-confirm-dialog";
import { stepHandlerActor, useStepHandlerLabel } from "../../workflows/step-handler";
import { projectListOptions } from "@multica/core/projects/queries";
import { BoardCardContent } from "./board-card";
import { InfiniteScrollSentinel } from "./infinite-scroll-sentinel";
import { ListLoadMoreFooter } from "./list-load-more-footer";
import type { ChildProgress } from "./list-row";
import type { IssueCreateDefaults } from "../surface/types";
import { useIssuePinnedIds } from "../surface/pinned-context";
import type {
  IssueStatusPageState,
  IssueStatusPagination,
} from "../surface/use-issue-status-branches";
import type {
  IssueGroupBranches,
  IssueGroupPageState,
} from "../surface/use-issue-group-branches";
import { useDragSettle } from "./use-drag-settle";
import { useIssuePeekActions } from "../surface/peek-context";
import { useBoardDragPan } from "./use-board-drag-pan";
import { useT } from "../../i18n";
import {
  type DragMoveUpdates,
  makeKanbanCollision,
  statusGroupId,
  assigneeGroupId,
  buildColumns,
  computePosition,
  findColumn,
  getMoveAnchors,
  insertIdByPosition,
  issueMatchesGroup,
  getMoveUpdates,
  propertyGroupId,
  projectGroupId,
} from "../utils/drag-utils";
import { useStatusLabel } from "../utils/status-label";

function isStatusGroup(
  group: BoardColumnGroup,
): group is BoardColumnGroup & { status: IssueStatus } {
  return group.status !== undefined;
}

interface ProjectColumnLabels {
  noProject: string;
  /** A project id the projects query cannot resolve — deleted, or not visible
   *  to this member. Shares the Table's wording so one board column and one
   *  table group row never describe the same project differently. */
  unavailableProject: string;
}

interface BuildGroupsContext extends ProjectColumnLabels {
  getActorName: (type: string, id: string) => string;
  groupingProperty: IssueProperty | null;
  projectMap: Map<string, Project> | undefined;
  noAssigneeLabel: string;
  noValueLabel: string;
}

/**
 * One project column. Shared by the client fallback (columns derived from
 * loaded cards) and the server path (columns derived from group descriptors)
 * so the two can never describe the same project differently.
 */
function projectColumn(
  id: string,
  projectId: string | null,
  projectMap: Map<string, Project> | undefined,
  labels: ProjectColumnLabels,
  totalCount?: number,
): BoardColumnGroup {
  const project = projectId ? projectMap?.get(projectId) ?? null : null;
  return {
    id,
    title: projectId
      ? project?.title ?? labels.unavailableProject
      : labels.noProject,
    projectId,
    project,
    totalCount,
    createData: { project_id: projectId },
  };
}

/**
 * Keep the "No project" column present as a drop target — clearing a card's
 * project by dragging has to stay possible even in a workspace where every
 * card currently has one. A board with no columns at all is left alone: that
 * is the surface's empty state, not a board missing one column.
 */
function withNoProjectColumn(
  columns: BoardColumnGroup[],
  projectMap: Map<string, Project> | undefined,
  labels: ProjectColumnLabels,
): BoardColumnGroup[] {
  if (columns.length === 0) return columns;
  if (columns.some((column) => column.projectId === null)) return columns;
  // No-project sorts first server-side, so it is always in the first page of
  // descriptors when it exists — an absent one cannot arrive with a later page.
  return [
    projectColumn(projectGroupId(null), null, projectMap, labels, 0),
    ...columns,
  ];
}

function buildGroups(
  issues: Issue[],
  visibleStatuses: IssueStatus[],
  grouping: IssueGrouping,
  {
    getActorName,
    groupingProperty,
    projectMap,
    noAssigneeLabel,
    noValueLabel,
    ...projectLabels
  }: BuildGroupsContext,
): BoardColumnGroup[] {
  if (grouping === "status") {
    return visibleStatuses.map((status) => ({
      id: statusGroupId(status),
      title: status,
      status,
      createData: { status: status },
    }));
  }

  // Select-property board: one column per option (definition order) plus a
  // trailing "No value" column. Empty columns stay visible — they are drop
  // targets for assigning the value.
  if (groupingProperty) {
    const columns: BoardColumnGroup[] = (groupingProperty.config.options ?? []).map(
      (option) => ({
        id: propertyGroupId(groupingProperty.id, option.id),
        title: option.name,
        propertyId: groupingProperty.id,
        propertyOptionId: option.id,
        propertyOptionColor: option.color,
      }),
    );
    columns.push({
      id: propertyGroupId(groupingProperty.id, null),
      title: noValueLabel,
      propertyId: groupingProperty.id,
      propertyOptionId: null,
    });
    return columns;
  }

  // Project board: one column per project the loaded cards reference, plus the
  // "No project" column. Ordering mirrors the server's group order (no-project
  // first, then project title) so the client fallback and the paged server
  // columns cannot disagree.
  if (grouping === "project") {
    const columns = new Map<string, BoardColumnGroup>();
    for (const issue of issues) {
      const projectId = issue.project_id ?? null;
      const id = projectGroupId(projectId);
      if (columns.has(id)) continue;
      columns.set(id, projectColumn(id, projectId, projectMap, projectLabels));
    }
    const ordered = Array.from(columns.values()).toSorted((a, b) => {
      if (a.projectId === null) return b.projectId === null ? 0 : -1;
      if (b.projectId === null) return 1;
      return a.title.localeCompare(b.title);
    });
    return withNoProjectColumn(ordered, projectMap, projectLabels);
  }

  const groups = new Map<string, BoardColumnGroup>();
  for (const issue of issues) {
    const id = assigneeGroupId(issue.assignee_type, issue.assignee_id);
    if (groups.has(id)) continue;

    if (issue.assignee_type && issue.assignee_id) {
      groups.set(id, {
        id,
        title: getActorName(issue.assignee_type, issue.assignee_id),
        assigneeType: issue.assignee_type,
        assigneeId: issue.assignee_id,
        createData: {
          assignee_type: issue.assignee_type,
          assignee_id: issue.assignee_id,
        },
      });
      continue;
    }

    groups.set(id, {
      id,
      title: noAssigneeLabel,
      assigneeType: null,
      assigneeId: null,
      createData: {
        assignee_type: null,
        assignee_id: null,
      },
    });
  }

  const order: Record<string, number> = {
    member: 0,
    agent: 1,
    squad: 2,
    none: 3,
  };

  return Array.from(groups.values()).toSorted((a, b) => {
    const aOrder = order[a.assigneeType ?? "none"] ?? 99;
    const bOrder = order[b.assigneeType ?? "none"] ?? 99;
    if (aOrder !== bOrder) return aOrder - bOrder;
    return a.title.localeCompare(b.title);
  });
}

const EMPTY_PROGRESS_MAP = new Map<string, ChildProgress>();
const EMPTY_IDS: string[] = [];

function BoardViewImpl({
  issues,
  visibleStatuses,
  onMoveIssue,
  childProgressMap = EMPTY_PROGRESS_MAP,
  projectMap,
  projectId,
  onCreateIssue,
  statusPagination,
  groupBranches,
}: {
  issues: Issue[];
  visibleStatuses: IssueStatus[];
  onMoveIssue: (issueId: string, updates: DragMoveUpdates, onSettled?: () => void) => void;
  childProgressMap?: Map<string, ChildProgress>;
  projectMap?: Map<string, Project>;
  /** When set, the per-column "+" pre-fills the project on the create form. */
  projectId?: string;
  onCreateIssue?: (defaults: IssueCreateDefaults) => void;
  statusPagination?: IssueStatusPagination;
  groupBranches?: IssueGroupBranches;
}) {
  const { t } = useT("issues");
  const pinnedIssueIds = useIssuePinnedIds();
  const storeGrouping = useViewStore((s) => s.grouping);
  const sortBy = useViewStore((s) => s.sortBy);
  const boardWsId = useWorkspaceId();
  const catalog = useIssueStatuses(boardWsId);
  const labelOf = useStatusLabel(boardWsId);
  const { data: workspaceProperties = [] } = useQuery(propertyListOptions(boardWsId));
  const groupingPropertyId = propertyIdFromViewKey(storeGrouping);
  const groupingProperty = groupingPropertyId
    ? workspaceProperties.find((p) => p.id === groupingPropertyId && p.type === "select") ?? null
    : null;
  // A persisted `property:<id>` grouping whose definition is gone (archived,
  // deleted, other workspace) falls back to status columns.
  const grouping: IssueGrouping =
    groupingPropertyId && !groupingProperty ? "status" : storeGrouping;
  const groupingOptionIds = useMemo(
    () =>
      groupingProperty
        ? new Set((groupingProperty.config.options ?? []).map((option) => option.id))
        : undefined,
    [groupingProperty],
  );
  const setIssuePropertyMutation = useSetIssueProperty();
  const unsetIssuePropertyMutation = useUnsetIssueProperty();
  const applyPropertyGroupValue = useCallback(
    (group: BoardColumnGroup, issueId: string) => {
      if (group.propertyId === undefined) return;
      // Surface failures like status/assignee drags do (use-issue-surface-
      // actions): the mutation rolls the card back, but without a toast the
      // snap-back reads as a UI glitch instead of a rejected write.
      const onError = (err: unknown) => {
        toast.error(
          err instanceof Error && err.message
            ? err.message
            : t(($) => $.page.move_failed),
        );
      };
      if (group.propertyOptionId === null) {
        unsetIssuePropertyMutation.mutate(
          { issueId, propertyId: group.propertyId },
          { onError },
        );
      } else if (group.propertyOptionId !== undefined) {
        setIssuePropertyMutation.mutate(
          {
            issueId,
            propertyId: group.propertyId,
            value: group.propertyOptionId,
          },
          { onError },
        );
      }
    },
    [setIssuePropertyMutation, t, unsetIssuePropertyMutation],
  );
  const sortFieldKey = sortBy === "created_at" ? "created" : sortBy;
  const sortPropertyId = propertyIdFromViewKey(sortBy);
  const sortLabel = sortBy !== "position"
    ? t(($) => $.board.ordered_by, {
        field: sortPropertyId
          ? workspaceProperties.find((p) => p.id === sortPropertyId)?.name ?? ""
          : t(($) => $.display[`sort_${sortFieldKey}` as keyof typeof $.display]),
      })
    : null;
  const { getActorName } = useActorName();
  const groupedIssues = useMemo(
    () => (groupBranches?.enabled ? groupBranches.issues : issues),
    [groupBranches, issues],
  );
  const hydratedAssigneeGroups = useMemo<BoardColumnGroup[] | undefined>(() => {
    if (grouping === "assignee" && groupBranches?.enabled) {
      return groupBranches.descriptors.flatMap((descriptor): BoardColumnGroup[] => {
        if (descriptor.value.kind !== "assignee") return [];
        const actorRef = descriptor.value.actor;
        const actor: { type: IssueAssigneeType; id: string } | null =
          actorRef &&
          (actorRef.type === "member" ||
            actorRef.type === "agent" ||
            actorRef.type === "squad")
            ? { type: actorRef.type, id: actorRef.id }
            : null;
        return [{
          id: descriptor.key,
          title: actor
            ? getActorName(actor.type, actor.id)
            : t(($) => $.filters.no_assignee),
          assigneeType: actor?.type ?? null,
          assigneeId: actor?.id ?? null,
          totalCount: descriptor.count,
          createData: {
            assignee_type: actor?.type ?? null,
            assignee_id: actor?.id ?? null,
          },
        }];
      });
    }
    return undefined;
  }, [getActorName, groupBranches, grouping, t]);
  const projectColumnLabels = useMemo<ProjectColumnLabels>(
    () => ({
      noProject: t(($) => $.swimlane.no_project),
      unavailableProject: t(($) => $.table.value_unavailable),
    }),
    [t],
  );
  const hydratedProjectGroups = useMemo<BoardColumnGroup[] | undefined>(() => {
    if (grouping !== "project" || !groupBranches?.enabled) return undefined;
    const columns = groupBranches.descriptors.flatMap(
      (descriptor): BoardColumnGroup[] =>
        descriptor.value.kind === "project"
          ? [
              projectColumn(
                // The descriptor key, not our own: it is what `groupPagination`
                // is keyed by. `projectGroupId` reproduces it exactly, which is
                // what lets cards bucket into these columns at all.
                descriptor.key,
                descriptor.value.project_id ?? null,
                projectMap,
                projectColumnLabels,
                descriptor.count,
              ),
            ]
          : [],
    );
    return withNoProjectColumn(columns, projectMap, projectColumnLabels);
  }, [groupBranches, grouping, projectColumnLabels, projectMap]);
  const groupPagination = useMemo(() => {
    if (!groupBranches?.enabled) return undefined;
    const grouped = new Map<string, IssueGroupPageState[]>();
    for (const descriptor of groupBranches.descriptors) {
      const page = groupBranches.pagination[descriptor.key];
      if (!page) continue;
      let id = descriptor.key;
      if (descriptor.value.kind === "property") {
        const value = descriptor.value;
        id =
          value.value_state === "value" && typeof value.value === "string"
            ? propertyGroupId(value.property_id, value.value)
            : propertyGroupId(value.property_id, null);
      }
      const pages = grouped.get(id) ?? [];
      pages.push(page);
      grouped.set(id, pages);
    }
    return Object.fromEntries(
      Array.from(grouped, ([id, pages]) => [
        id,
        {
          total: pages.reduce((sum, page) => sum + page.total, 0),
          loaded: pages.reduce((sum, page) => sum + page.loaded, 0),
          hasMore: pages.some((page) => page.hasMore),
          isLoading: pages.some((page) => page.isLoading),
          isFetching: pages.some((page) => page.isFetching),
          isError: pages.some((page) => page.isError),
          loadMore: () => {
            for (const page of pages) {
              if (page.hasMore) page.loadMore();
            }
          },
          retry: () => {
            for (const page of pages) {
              if (page.isError) page.retry();
            }
          },
        },
      ]),
    ) as Record<string, IssueGroupPageState>;
  }, [groupBranches]);
  const groups = useMemo(
    () => {
      const built =
        hydratedAssigneeGroups ??
        hydratedProjectGroups ??
        buildGroups(issues, visibleStatuses, grouping, {
          getActorName,
          groupingProperty,
          projectMap,
          noAssigneeLabel: t(($) => $.filters.no_assignee),
          noValueLabel: t(($) => $.board.no_value),
          ...projectColumnLabels,
        });
      return built.map((group) => ({
        ...group,
        totalCount: groupPagination?.[group.id]?.total ?? group.totalCount,
      }));
    },
    [hydratedAssigneeGroups, hydratedProjectGroups, issues, visibleStatuses, grouping, getActorName, groupingProperty, projectMap, projectColumnLabels, groupPagination, t],
  );
  const groupIds = useMemo(
    () => new Set(groups.map((group) => group.id)),
    [groups],
  );
  const groupMap = useMemo(
    () => new Map(groups.map((group) => [group.id, group])),
    [groups],
  );
  const collisionDetection = useMemo(
    () => makeKanbanCollision(groupIds),
    [groupIds],
  );

  // --- Drag state ---
  const [activeIssue, setActiveIssue] = useState<Issue | null>(null);
  const [pendingHandoff, setPendingHandoff] = useState<{
    issue: Issue;
    status: IssueStatus;
    updates: ReturnType<typeof getMoveUpdates>;
    anchors: ReturnType<typeof getMoveAnchors>;
  } | null>(null);

  // Project workflows (MUL-7420): a card can only move to a status its
  // project's workflow lists, and a project board names each step's handler.
  const { workflows } = useIssueWorkflows(boardWsId);
  const { data: workspaceProjects } = useQuery(projectListOptions(boardWsId));
  const workflowOfIssue = useCallback(
    (issue: Issue | null | undefined) => {
      if (!issue?.project_id) return null;
      return resolveProjectWorkflow(
        workflows,
        workspaceProjects?.find((p) => p.id === issue.project_id),
      );
    },
    [workflows, workspaceProjects],
  );
  const scopeProject = projectId ? workspaceProjects?.find((p) => p.id === projectId) ?? null : null;
  const activeWorkflow = workflowOfIssue(activeIssue);
  const activeProject = activeIssue?.project_id
    ? workspaceProjects?.find((p) => p.id === activeIssue.project_id) ?? null
    : null;
  const activeHandlerLabel = useStepHandlerLabel(activeProject);
  const boardWorkflow = useMemo<BoardWorkflowContextValue>(
    () => ({
      scopeWorkflow: resolveProjectWorkflow(workflows, scopeProject),
      scopeProject,
      dropBlockedReason: (status) =>
        activeWorkflow && activeIssue?.status !== status && !workflowAllowsStatus(activeWorkflow, status)
          ? t(($) => $.workflows.not_in_workflow, { name: activeWorkflow.name })
          : null,
      dropHint: (status) => {
        if (!activeWorkflow || activeIssue?.status === status) return null;
        const step = workflowStep(activeWorkflow, status);
        const name = activeHandlerLabel(step);
        if (!step || !name) return null;
        const actor = stepHandlerActor(step, activeProject);
        return { name, actor, run: actor ? actor.type !== "member" : step.handler.type === "creator" };
      },
    }),
    [activeHandlerLabel, activeIssue?.status, activeProject, activeWorkflow, scopeProject, t, workflows],
  );
  // Parent groups (DENE-2760) reorder the drag mirror itself, not just the
  // drawing: a drop computes its position from the neighbours in this order,
  // so it has to be the order the user sees.
  const parentGroupsOn = useFeatureEnabled(OFFICE_PARENT_GROUPS_FLAG);
  const layoutColumns = useCallback(() => {
    const built = buildColumns(groupedIssues, groups, grouping, groupingOptionIds);
    if (!parentGroupsOn) return built;
    const parentOf = new Map(groupedIssues.map((issue) => [issue.id, issue.parent_issue_id]));
    return groupColumnsByParent(built, (id) => parentOf.get(id));
  }, [groupedIssues, groups, grouping, groupingOptionIds, parentGroupsOn]);

  // Shared drag/settle primitive: owns the local column mirror, the
  // dragging/settling locks, the post-move animation-frame throttle, and the
  // settle callback. Shared with list-view (and swimlane) so the surfaces
  // can't drift apart. Local columns follow TQ between drags via the resync
  // effect below; during a drag/settle they are frozen by the locks.
  const {
    columns,
    setColumns,
    columnsRef,
    isDraggingRef,
    isSettlingRef,
    recentlyMovedRef,
    settleVersion,
    beginSettle,
  } = useDragSettle(layoutColumns);

  useEffect(() => {
    if (!isDraggingRef.current && !isSettlingRef.current) {
      setColumns(layoutColumns());
    }
  }, [layoutColumns, settleVersion, setColumns, isDraggingRef, isSettlingRef]);

  // --- Issue map ---
  // Frozen during drag so BoardColumn/DraggableBoardCard props stay
  // referentially stable even if a TQ refetch lands mid-drag.
  const issueMap = useMemo(() => {
    const map = new Map<string, Issue>();
    for (const issue of groupedIssues) map.set(issue.id, issue);
    return map;
  }, [groupedIssues]);

  const issueMapRef = useRef(issueMap);
  if (!isDraggingRef.current && !isSettlingRef.current) {
    issueMapRef.current = issueMap;
  }

  // --- Parent groups (DENE-2760) ---
  // Parents are read off the unfiltered mirror, so a tab never repaints the
  // other parents and every column agrees on each parent's color.
  const collapsedParentGroups = useViewStore((s) => s.boardCollapsedParentGroups);
  const toggleParentGroupCollapsed = useViewStore((s) => s.toggleBoardParentGroupCollapsed);
  const storedParentFilter = useViewStore((s) => s.boardParentFilter);
  const setParentFilter = useViewStore((s) => s.setBoardParentFilter);
  const boardParents = useMemo(() => {
    if (!parentGroupsOn) return [];
    const counts = new Map<string, number>();
    for (const group of groups) {
      for (const id of columns[group.id] ?? EMPTY_IDS) {
        const parentId = issueMap.get(id)?.parent_issue_id;
        if (parentId) counts.set(parentId, (counts.get(parentId) ?? 0) + 1);
      }
    }
    // Newest parent first: UUIDv7 ids sort by creation time.
    return Array.from(counts, ([id, count]) => ({ id, count })).toSorted((a, b) =>
      b.id.localeCompare(a.id),
    );
  }, [columns, groups, issueMap, parentGroupsOn]);
  const parentColors = useMemo(
    () => assignParentGroupColors(boardParents.map((parent) => parent.id)),
    [boardParents],
  );
  // Tabs count and filter only the cards the board holds, so they wait until
  // every column has its last page: a parent on an unloaded page would be
  // missing, and its counts would be short. Grouping and colors still apply
  // to the loaded cards. A column still fetching also waits, but only until the
  // board has settled once: after that a background refresh keeps the row and
  // the picked tab in place instead of blinking them off (DENE-2762).
  const boardLoadState = useMemo(() => {
    if (groupBranches?.hasMoreGroups) return { complete: false, fetching: false };
    let complete = true;
    let fetching = false;
    for (const group of groups) {
      const page = isStatusGroup(group)
        ? statusPagination?.[group.status]
        : groupPagination?.[group.id];
      if (!page) continue;
      if (page.hasMore || page.isLoading || page.isError) complete = false;
      if (page.isFetching) fetching = true;
    }
    return { complete, fetching };
  }, [groupBranches?.hasMoreGroups, groupPagination, groups, statusPagination]);
  const boardSettled = boardLoadState.complete && !boardLoadState.fetching;
  const [boardSettledOnce, setBoardSettledOnce] = useState(false);
  if (boardSettled && !boardSettledOnce) setBoardSettledOnce(true);
  const boardFullyLoaded = boardSettled || (boardLoadState.complete && boardSettledOnce);
  // A tab whose parent has left the board reads as 「全部」.
  const parentFilter =
    boardFullyLoaded &&
    storedParentFilter &&
    boardParents.some((parent) => parent.id === storedParentFilter)
      ? storedParentFilter
      : null;
  const parentGroupsValue = useMemo<BoardParentGroupsValue | null>(() => {
    if (!parentGroupsOn) return null;
    const collapsed = new Set(collapsedParentGroups);
    return {
      colorOf: (parentId) => parentColors.get(parentId),
      isCollapsed: (columnId, parentId) => collapsed.has(parentGroupKey(columnId, parentId)),
      toggle: (columnId, parentId) => toggleParentGroupCollapsed(parentGroupKey(columnId, parentId)),
    };
  }, [collapsedParentGroups, parentColors, parentGroupsOn, toggleParentGroupCollapsed]);
  // What each column draws: the mirror, narrowed to the selected parent.
  const displayColumns = useMemo(() => {
    if (!parentFilter) return columns;
    return Object.fromEntries(
      Object.entries(columns).map(([columnId, ids]) => [
        columnId,
        ids.filter((id) => issueMap.get(id)?.parent_issue_id === parentFilter),
      ]),
    );
  }, [columns, issueMap, parentFilter]);

  const sensors = useSensors(
    useSensor(PointerSensor, {
      activationConstraint: { distance: 5 },
    })
  );

  // Side peek steps through a column with J / K, in the order shown here.
  const peek = useIssuePeekActions();
  useEffect(() => {
    peek?.publishColumns(
      groups.map((group) => {
        const ids = displayColumns[group.id] ?? EMPTY_IDS;
        if (!parentGroupsValue) return ids;
        // A folded group's cards are not on screen, so J / K skips them.
        return ids.filter((id) => {
          const parentId = issueMap.get(id)?.parent_issue_id;
          return !parentId || !parentGroupsValue.isCollapsed(group.id, parentId);
        });
      }),
    );
  }, [peek, groups, displayColumns, issueMap, parentGroupsValue]);
  useEffect(() => () => peek?.publishColumns(null), [peek]);

  // #6700: drag empty board background with the left button to pan horizontally
  // (Trello/Linear). Card drags start on `[data-board-card]` and are ignored.
  const pan = useBoardDragPan<HTMLDivElement>();
  // The row of columns scrolls sideways; on a phone one column fills the
  // screen, so coming back from an issue must land on the same column.
  const restoreBoardScrollRef = useRestoredScrollRef("board");
  const panRef = pan.ref;
  const attachBoardScroller = useCallback(
    (el: HTMLDivElement | null) => {
      panRef.current = el;
      restoreBoardScrollRef(el);
    },
    [panRef, restoreBoardScrollRef],
  );

  const handleDragStart = useCallback(
    (event: DragStartEvent) => {
      isDraggingRef.current = true;
      const issue = issueMapRef.current.get(event.active.id as string) ?? null;
      setActiveIssue(issue);
    },
    [isDraggingRef],
  );

  const handleDragOver = useCallback(
    (event: DragOverEvent) => {
      const { active, over } = event;
      if (!over || recentlyMovedRef.current) return;

      const activeId = active.id as string;
      const overId = over.id as string;

      setColumns((prev) => {
        const activeCol = findColumn(prev, activeId, groupIds);
        const overCol = findColumn(prev, overId, groupIds);
        if (!activeCol || !overCol || activeCol === overCol) return prev;
        const targetStatus = groups.find((group) => group.id === overCol)?.status;
        if (targetStatus && catalog.entryOf(targetStatus)?.archived_at) return prev;
        if (targetStatus && !workflowAllowsStatus(workflowOfIssue(issueMapRef.current.get(activeId)), targetStatus)) {
          return prev;
        }

        if (sortBy !== "position") return prev;

        recentlyMovedRef.current = true;
        const oldIds = prev[activeCol]!.filter((id) => id !== activeId);
        const newIds = [...prev[overCol]!];
        const overIndex = newIds.indexOf(overId);
        const insertIndex = overIndex >= 0 ? overIndex : newIds.length;
        newIds.splice(insertIndex, 0, activeId);
        return { ...prev, [activeCol]: oldIds, [overCol]: newIds };
      });
    },
    [groupIds, groups, catalog, sortBy, recentlyMovedRef, setColumns, workflowOfIssue],
  );

  const handleDragEnd = useCallback(
    (event: DragEndEvent) => {
      const { active, over } = event;
      isDraggingRef.current = false;
      setActiveIssue(null);

      const resetColumns = () => setColumns(layoutColumns());

      if (!over) {
        resetColumns();
        return;
      }

      const activeId = active.id as string;
      const overId = over.id as string;

      const cols = columnsRef.current;
      const activeCol = findColumn(cols, activeId, groupIds);
      const overCol = findColumn(cols, overId, groupIds);
      if (!activeCol || !overCol) {
        resetColumns();
        return;
      }

      // Same-column reorder (manual sort only)
      let finalColumns = cols;
      if (activeCol === overCol && sortBy === "position") {
        const ids = cols[activeCol]!;
        const oldIndex = ids.indexOf(activeId);
        const newIndex = ids.indexOf(overId);
        if (oldIndex !== -1 && newIndex !== -1 && oldIndex !== newIndex) {
          const reordered = arrayMove(ids, oldIndex, newIndex);
          finalColumns = { ...cols, [activeCol]: reordered };
          setColumns(finalColumns);
        }
      }

      const finalCol = sortBy === "position"
        ? findColumn(finalColumns, activeId, groupIds)
        : overCol;
      if (!finalCol) {
        resetColumns();
        return;
      }
      const finalGroup = groupMap.get(finalCol);
      if (!finalGroup) {
        resetColumns();
        return;
      }

      const map = issueMapRef.current;
      if (finalGroup.status && map.get(activeId)?.status !== finalGroup.status && catalog.entryOf(finalGroup.status)?.archived_at) {
        resetColumns();
        return;
      }
      const movingWorkflow = workflowOfIssue(map.get(activeId));
      if (
        finalGroup.status &&
        movingWorkflow &&
        map.get(activeId)?.status !== finalGroup.status &&
        !workflowAllowsStatus(movingWorkflow, finalGroup.status)
      ) {
        resetColumns();
        toast.info(
          t(($) => $.workflows.drop_blocked, {
            status: labelOf(finalGroup.status),
            name: movingWorkflow.name,
          }),
        );
        return;
      }

      // Entering a step that hands the issue off is confirmed first: who it
      // goes to, the previous agent's run, and the brief. The card returns to
      // its column until then. (MUL-7420)
      const handoffTarget =
        finalGroup.status &&
        movingWorkflow &&
        map.get(activeId)?.status !== finalGroup.status &&
        stepHandsOff(workflowStep(movingWorkflow, finalGroup.status))
          ? finalGroup.status
          : null;

      if (sortBy !== "position") {
        // Cross-column: only update group (status/assignee), keep original position.
        const currentIssue = map.get(activeId);
        if (!currentIssue || issueMatchesGroup(currentIssue, finalGroup)) {
          resetColumns();
          if (activeId !== overId) {
            toast.info(t(($) => $.board.manual_reorder_hint), {
              id: "issue-manual-reorder-hint",
            });
          }
          return;
        }
        if (handoffTarget) {
          resetColumns();
          setPendingHandoff({
            issue: currentIssue,
            status: handoffTarget,
            updates: getMoveUpdates(finalGroup, currentIssue.position, currentIssue),
            anchors: getMoveAnchors(
              insertIdByPosition((cols[overCol] ?? []).filter((id) => id !== activeId), activeId, currentIssue.position, map),
              activeId,
            ),
          });
          return;
        }
        // Optimistically move the card into the target column *now*. Without
        // this, the sortBy != "position" path never touches local columns on
        // drop, so onDragOver having been a no-op leaves the card in its origin
        // column for the whole request — it only jumps across when the mutation
        // settles. That is the "snaps back to origin, then moves" glitch.
        // Placement mirrors the cache (insertByPosition) so the settle rebuild
        // from TanStack Query is a visual no-op.
        const targetIds = insertIdByPosition(
          (cols[overCol] ?? []).filter((id) => id !== activeId),
          activeId,
          currentIssue.position,
          map,
        );
        setColumns((prev) => {
          const fromIds = (prev[activeCol] ?? []).filter((cid) => cid !== activeId);
          return { ...prev, [activeCol]: fromIds, [overCol]: targetIds };
        });
        onMoveIssue(
          activeId,
          {
            ...getMoveUpdates(finalGroup, currentIssue.position, currentIssue),
            ...getMoveAnchors(targetIds, activeId, pinnedIssueIds),
          },
          beginSettle(),
        );
        applyPropertyGroupValue(finalGroup, activeId);
        return;
      }

      const finalIds = finalColumns[finalCol]!;
      const newPosition = computePosition(finalIds, activeId, map, pinnedIssueIds);
      const currentIssue = map.get(activeId);

      if (
        currentIssue &&
        issueMatchesGroup(currentIssue, finalGroup) &&
        currentIssue.position === newPosition
      ) {
        return;
      }

      if (handoffTarget && currentIssue) {
        resetColumns();
        setPendingHandoff({
          issue: currentIssue,
          status: handoffTarget,
          updates: getMoveUpdates(finalGroup, newPosition, currentIssue),
          anchors: getMoveAnchors(finalIds, activeId),
        });
        return;
      }

      // beginSettle() holds the lock and returns the onSettled callback that
      // releases it and resyncs local columns from the cache: a no-op on
      // success (onSuccess already patched the moved card in place), the revert
      // on error (onError restored the snapshot). Without it a failed move would
      // strand the card at the drop target, since onSettled no longer refetches.
      onMoveIssue(
        activeId,
        {
          ...getMoveUpdates(finalGroup, newPosition, currentIssue),
          ...getMoveAnchors(finalIds, activeId, pinnedIssueIds),
        },
        beginSettle(),
      );
      applyPropertyGroupValue(finalGroup, activeId);
    },
    [applyPropertyGroupValue, beginSettle, catalog, columnsRef, groupIds, groupMap, isDraggingRef, labelOf, layoutColumns, onMoveIssue, pinnedIssueIds, setColumns, sortBy, t, workflowOfIssue],
  );

  // An aborted drag (pointercancel, window resize, tab hide, Escape) fires
  // onDragCancel instead of onDragEnd. Releasing the drag lock here keeps the
  // column mirror resyncing with the cache afterwards — see the same handler in
  // list-view for the touch path that makes this routine (MUL-6240).
  const handleDragCancel = useCallback(() => {
    isDraggingRef.current = false;
    setActiveIssue(null);
    setColumns(layoutColumns());
  }, [layoutColumns, setColumns, isDraggingRef]);

  // A narrowed column counts what it shows, not the whole status.
  const filteredCount = (groupId: string) =>
    parentFilter ? (displayColumns[groupId] ?? EMPTY_IDS).length : undefined;

  return (
    <BoardWorkflowContext.Provider value={boardWorkflow}>
    <BoardParentGroupsContext.Provider value={parentGroupsValue}>
    {parentGroupsValue && boardFullyLoaded && (
      <BoardParentTabs
        parents={boardParents}
        selected={parentFilter}
        colorOf={parentGroupsValue.colorOf}
        onSelect={setParentFilter}
      />
    )}
    <DndContext
      sensors={sensors}
      collisionDetection={collisionDetection}
      onDragStart={handleDragStart}
      onDragOver={handleDragOver}
      onDragEnd={handleDragEnd}
      onDragCancel={handleDragCancel}
    >
      <div
        ref={attachBoardScroller}
        data-tab-scroll-root="board"
        onPointerDown={pan.onPointerDown}
        onPointerMove={pan.onPointerMove}
        onPointerUp={pan.onPointerUp}
        onPointerCancel={pan.onPointerCancel}
        onLostPointerCapture={pan.onLostPointerCapture}
        data-board-scroller=""
        className={cn(
          "flex flex-1 min-h-0 gap-4 overflow-x-auto p-2",
          // While a side peek is open, a trailing spacer as wide as the panel
          // lets every column scroll clear of it (IssuePeekHost).
          "group-data-[peek-open]/peek:after:w-(--issue-peek-width) group-data-[peek-open]/peek:after:shrink-0 group-data-[peek-open]/peek:after:content-['']",
        )}
      >
        {groups.length === 0 ? (
          groupBranches?.isError ? (
            <button
              type="button"
              className="flex min-w-full flex-1 items-center justify-center text-body text-destructive hover:underline"
              onClick={groupBranches.retryGroups}
            >
              {t(($) => $.table.load_more_failed_retry)}
            </button>
          ) : (
            <div className="flex min-w-full flex-1 items-center justify-center text-body text-muted-foreground">
              {t(($) => $.board.empty_grouping)}
            </div>
          )
        ) : (
          groups.map((group) =>
            isStatusGroup(group) ? (
              <ServerPaginatedBoardColumn
                key={group.id}
                group={group}
                issueIds={displayColumns[group.id] ?? EMPTY_IDS}
                issueMap={issueMapRef.current}
                childProgressMap={childProgressMap}
                projectMap={projectMap}
                page={statusPagination?.[group.status]}
                projectId={projectId}
                onCreateIssue={onCreateIssue}
                sortLabel={sortLabel}
                countOverride={filteredCount(group.id)}
              />
            ) : (
              groupPagination?.[group.id] ? (
                <ServerPaginatedBoardColumn
                  key={group.id}
                  group={group}
                  issueIds={displayColumns[group.id] ?? EMPTY_IDS}
                  issueMap={issueMapRef.current}
                  childProgressMap={childProgressMap}
                  projectMap={projectMap}
                  page={groupPagination[group.id]!}
                  projectId={projectId}
                  onCreateIssue={onCreateIssue}
                  sortLabel={sortLabel}
                  countOverride={filteredCount(group.id)}
                />
              ) : (
                <BoardColumn
                  key={group.id}
                  group={group}
                  issueIds={displayColumns[group.id] ?? EMPTY_IDS}
                  issueMap={issueMapRef.current}
                  childProgressMap={childProgressMap}
                  projectMap={projectMap}
                  projectId={projectId}
                  onCreateIssue={onCreateIssue}
                  totalCount={filteredCount(group.id) ?? group.totalCount}
                  sortLabel={sortLabel}
                />
              )
            ),
          )
        )}
        {groupBranches?.hasMoreGroups && (
          <div className="flex w-8 shrink-0 items-center justify-center">
            <InfiniteScrollSentinel
              onVisible={groupBranches.loadMoreGroups}
              loading={groupBranches.isLoadingMoreGroups}
            />
          </div>
        )}
      </div>

      <DragOverlay dropAnimation={null}>
        {activeIssue ? (
          <div style={{ width: BOARD_CARD_WIDTH }} className="rotate-1 cursor-grabbing opacity-90 shadow-lg shadow-black/10">
            <BoardCardContent
              issue={activeIssue}
              childProgress={childProgressMap.get(activeIssue.id)}
              project={
                activeIssue.project_id
                  ? projectMap?.get(activeIssue.project_id)
                  : undefined
              }
              groupColor={
                activeIssue.parent_issue_id
                  ? parentGroupsValue?.colorOf(activeIssue.parent_issue_id)
                  : undefined
              }
            />
          </div>
        ) : null}
      </DragOverlay>
    </DndContext>
    </BoardParentGroupsContext.Provider>
    <WorkflowHandoffConfirmDialog
      issue={pendingHandoff?.issue ?? null}
      toStatus={pendingHandoff?.status ?? ""}
      onCancel={() => setPendingHandoff(null)}
      onConfirm={({ stopPreviousRuns }) => {
        const pending = pendingHandoff;
        setPendingHandoff(null);
        if (!pending) return;
        onMoveIssue(
          pending.issue.id,
          {
            ...pending.updates,
            ...pending.anchors,
            ...(stopPreviousRuns ? { stop_previous_assignee_runs: true } : {}),
          },
          beginSettle(),
        );
      }}
    />
    </BoardWorkflowContext.Provider>
  );
}


const ServerPaginatedBoardColumn = memo(function ServerPaginatedBoardColumn({
  group,
  issueIds,
  issueMap,
  childProgressMap,
  projectMap,
  page,
  projectId,
  onCreateIssue,
  sortLabel,
  countOverride,
}: {
  group: BoardColumnGroup;
  issueIds: string[];
  issueMap: Map<string, Issue>;
  childProgressMap?: Map<string, ChildProgress>;
  projectMap?: Map<string, Project>;
  page?: IssueStatusPageState | IssueGroupPageState;
  projectId?: string;
  onCreateIssue?: (defaults: IssueCreateDefaults) => void;
  sortLabel?: string | null;
  /** The header count while a parent tab narrows the column. */
  countOverride?: number;
}) {
  const footer = page ? (
    <ListLoadMoreFooter
      hasMore={page.hasMore}
      isLoading={page.isLoading || page.isFetching}
      total={page.total}
      onLoadMore={page.loadMore}
      isError={page.isError}
      onRetry={page.retry}
    />
  ) : undefined;
  return (
    <BoardColumn
      group={group}
      issueIds={issueIds}
      issueMap={issueMap}
      childProgressMap={childProgressMap}
      projectMap={projectMap}
      totalCount={countOverride ?? page?.total ?? group.totalCount}
      projectId={projectId}
      onCreateIssue={onCreateIssue}
      sortLabel={sortLabel}
      footer={footer}
    />
  );
});


/**
 * Memoized: the surface controller re-renders on loading-flag flips (e.g. a
 * query enabling when the view changes) — without memo every such flip
 * re-rendered this entire view tree (hundreds of ms). All props are
 * referentially stable useMemo/useCallback outputs from the controller.
 */
export const BoardView = memo(BoardViewImpl);
