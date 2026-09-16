"use client";

import { useEffect, useMemo, useState } from "react";
import {
  DndContext,
  PointerSensor,
  closestCenter,
  useSensor,
  useSensors,
  type DragEndEvent,
} from "@dnd-kit/core";
import {
  SortableContext,
  arrayMove,
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { restrictToVerticalAxis } from "@dnd-kit/modifiers";
import { GripVertical, Layers, Pencil, Trash2 } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Switch } from "@multica/ui/components/ui/switch";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { cn } from "@multica/ui/lib/utils";
import type { IssueView, IssueViewVisibility } from "@multica/core/api/schemas";
import {
  issueViewSharingChoices,
  parseIssueViewVisibility,
} from "@multica/core/issue-views/visibility";
import {
  DeleteViewConfirm,
  type ViewBarItem,
} from "./view-bar-popover";
import { useT } from "../../i18n";

/** One row in the manager: a built-in tab or a saved view. */
/** The vertical-locked row drag has no DragOverlay under the pointer, so
 *  the grabbing cursor is promoted to the document for the drag's duration
 *  (see the `data-dnd-dragging` contract in ui/styles/base.css). */
function setDndCursor(on: boolean) {
  if (on) document.documentElement.dataset.dndDragging = "true";
  else delete document.documentElement.dataset.dndDragging;
}

function SortableRow({
  item,
  hidden,
  anchor,
  onToggleHidden,
  onEdit,
  onDelete,
  onChangeVisibility,
}: {
  item: ViewBarItem;
  hidden: boolean;
  /** The anchor built-in cannot be hidden — its switch is disabled. */
  anchor: boolean;
  onToggleHidden: () => void;
  onEdit?: () => void;
  onDelete?: () => void;
  onChangeVisibility?: (view: IssueView, visibility: IssueViewVisibility) => void;
}) {
  const { t } = useT("issues");
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } =
    useSortable({ id: item.barItemId });
  const visibilityLabels: Record<IssueViewVisibility, string> = {
    private: t(($) => $.save_view.visibility_private),
    workspace: t(($) => $.save_view.visibility_workspace),
    project: t(($) => $.manage_views.visibility_project),
  };
  const view = item.view;
  const sharingChoices =
    view && item.canManage && view.scope_type !== "my"
      ? issueViewSharingChoices(view.scope_type)
      : [];
  const sharingValue = view ? parseIssueViewVisibility(view.visibility) : "private";

  return (
    <div
      ref={setNodeRef}
      style={{ transform: CSS.Translate.toString(transform), transition }}
      className={cn(
        "flex min-w-0 items-center gap-2 rounded-md px-2 py-1.5",
        isDragging && "z-10 bg-accent opacity-80",
      )}
    >
      <button
        type="button"
        {...attributes}
        {...listeners}
        aria-label={t(($) => $.view_bar.drag_handle)}
        className={cn(
          "cursor-grab active:cursor-grabbing text-faint-foreground hover:text-muted-foreground",
          isDragging && "cursor-grabbing",
        )}
      >
        <GripVertical className="size-3.5" />
      </button>
      <span className="min-w-0 flex-1 truncate text-body">
        {item.label}
        {item.kind === "builtin" && (
          <span className="ml-1.5 text-caption text-muted-foreground">
            {t(($) => $.view_bar.builtin_tag)}
          </span>
        )}
        {view?.visibility === "project" && (
          <span className="ml-1.5 text-caption text-muted-foreground">
            {t(($) => $.view_bar.project_shared_tag)}
          </span>
        )}
      </span>
      {sharingChoices.length > 0 && view && onChangeVisibility && (
        <Select
          items={sharingChoices.map((value) => ({
            value,
            label: visibilityLabels[value],
          }))}
          value={sharingValue}
          onValueChange={(v) => {
            if (v === "private" || v === "workspace" || v === "project") {
              onChangeVisibility(view, v);
            }
          }}
        >
          <SelectTrigger
            size="sm"
            className="w-36 shrink-0"
            aria-label={t(($) => $.manage_views.visibility_label, { name: item.label })}
          >
            <SelectValue>{visibilityLabels[sharingValue]}</SelectValue>
          </SelectTrigger>
          <SelectContent align="end">
            <SelectGroup>
              {sharingChoices.map((value) => (
                <SelectItem key={value} value={value}>
                  {visibilityLabels[value]}
                </SelectItem>
              ))}
            </SelectGroup>
          </SelectContent>
        </Select>
      )}
      {item.kind === "view" && item.canManage && (
        <span className="flex items-center gap-0.5">
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label={t(($) => $.view_selector.edit)}
            onClick={onEdit}
          >
            <Pencil className="size-3.5" />
          </Button>
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label={t(($) => $.view_bar.delete)}
            onClick={onDelete}
            className="text-muted-foreground hover:text-destructive"
          >
            <Trash2 className="size-3.5" />
          </Button>
        </span>
      )}
      <Switch
        size="sm"
        checked={!hidden}
        disabled={anchor}
        aria-label={t(($) => $.view_bar.visible_toggle)}
        onCheckedChange={onToggleHidden}
      />
    </div>
  );
}

/**
 * Central manager for one surface's view bar: order (drag), visibility
 * (switch), edit/delete for manageable saved views. Order and visibility
 * write the per-user preference document optimistically; edit/delete
 * delegate to the host (they need the save dialog / delete mutation wired
 * to the surface's state).
 */
export function ManageViewsDialog({
  open,
  onOpenChange,
  items,
  hiddenSet,
  anchorId,
  onReorder,
  onToggleHidden,
  onEditView,
  onDeleteView,
  onChangeVisibility,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Full ordered list (built-ins + views), pre-composed by the host. */
  items: ViewBarItem[];
  hiddenSet: Set<string>;
  anchorId: string;
  onReorder: (orderedIds: string[]) => void;
  onToggleHidden: (barItemId: string, hidden: boolean) => void;
  onEditView: (view: IssueView) => void;
  onDeleteView: (view: IssueView) => Promise<void>;
  onChangeVisibility: (view: IssueView, visibility: IssueViewVisibility) => void;
}) {
  const { t } = useT("issues");
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
  );
  // Local order during a drag session so rows follow the pointer without a
  // server round-trip per pixel; committed (one PUT) on drop.
  const [localOrder, setLocalOrder] = useState<string[] | null>(null);
  // If the dialog unmounts mid-drag (close on Escape, view deleted under
  // us), dnd-kit fires no cancel — clear the document cursor ourselves.
  useEffect(() => () => setDndCursor(false), []);
  useEffect(() => {
    if (!open) setLocalOrder(null);
  }, [open]);

  const orderedItems = useMemo(() => {
    if (!localOrder) return items;
    const byId = new Map(items.map((item) => [item.barItemId, item]));
    const next: ViewBarItem[] = [];
    for (const id of localOrder) {
      const item = byId.get(id);
      if (item) {
        next.push(item);
        byId.delete(id);
      }
    }
    return [...next, ...byId.values()];
  }, [items, localOrder]);

  const [deleting, setDeleting] = useState<IssueView | null>(null);

  const handleDragEnd = (event: DragEndEvent) => {
    const { active, over } = event;
    if (!over || active.id === over.id) return;
    const ids = orderedItems.map((item) => item.barItemId);
    const from = ids.indexOf(String(active.id));
    const to = ids.indexOf(String(over.id));
    if (from < 0 || to < 0) return;
    const next = arrayMove(ids, from, to);
    setLocalOrder(next);
    onReorder(next);
  };

  return (
    <>
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-1.5">
              <Layers className="size-4" />
              {t(($) => $.view_bar.manage_title)}
            </DialogTitle>
            <DialogDescription>{t(($) => $.view_bar.manage_hint)}</DialogDescription>
          </DialogHeader>
          <div className="-mx-2 min-w-0 overflow-hidden">
            <DndContext
              sensors={sensors}
              collisionDetection={closestCenter}
              // Rows only ever swap vertically — same constraint the table
              // header puts on column reordering, rotated 90°. Horizontal
              // auto-scroll is likewise an axis this gesture cannot act on.
              modifiers={[restrictToVerticalAxis]}
              autoScroll={{ threshold: { x: 0, y: 0.15 } }}
              onDragStart={() => setDndCursor(true)}
              onDragCancel={() => setDndCursor(false)}
              onDragEnd={(event) => {
                setDndCursor(false);
                handleDragEnd(event);
              }}
            >
              <SortableContext
                items={orderedItems.map((item) => item.barItemId)}
                strategy={verticalListSortingStrategy}
              >
                {orderedItems.map((item) => (
                  <SortableRow
                    key={item.barItemId}
                    item={item}
                    hidden={hiddenSet.has(item.barItemId)}
                    anchor={item.barItemId === anchorId}
                    onToggleHidden={() =>
                      onToggleHidden(item.barItemId, !hiddenSet.has(item.barItemId))
                    }
                    onEdit={item.view ? () => onEditView(item.view!) : undefined}
                    onDelete={item.view ? () => setDeleting(item.view!) : undefined}
                    onChangeVisibility={onChangeVisibility}
                  />
                ))}
              </SortableContext>
            </DndContext>
          </div>
        </DialogContent>
      </Dialog>

      <DeleteViewConfirm
        view={deleting}
        onOpenChange={(open) => {
          if (!open) setDeleting(null);
        }}
        onConfirm={onDeleteView}
      />
    </>
  );
}

