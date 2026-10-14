"use client";

import { useEffect, useRef, type WheelEvent } from "react";
import { Pin } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import type { ChatProjectChip } from "@multica/core/chat/project-bar";
import type { Project } from "@multica/core/types";
import { useT } from "../../i18n";
import { ProjectIcon } from "../../projects/components/project-icon";

const CHIP_CLASS =
  "inline-flex h-7 shrink-0 items-center rounded-full border text-caption whitespace-nowrap transition-colors";
const chipTone = (active: boolean) =>
  active
    ? "border-foreground bg-foreground text-background"
    : "border-border bg-background text-foreground hover:bg-accent";

/**
 * One row above the chat list: All, then the person's pinned projects, then
 * projects with chats, most recent first. Picking a chip narrows the list;
 * the row scrolls sideways instead of wrapping or folding into a menu.
 *
 * The pin toggle sits at the chip's end. It always shows on a pinned chip;
 * otherwise it appears on hover and on the selected chip, so touch screens
 * reach it by tapping the chip first.
 *
 * A mouse wheel scrolls the row sideways, and the selected chip is kept in
 * view, so a narrow list column still reaches every project.
 */
export function ChatProjectBar({
  chips,
  projects,
  selectedId,
  onSelect,
  onTogglePin,
}: {
  chips: ChatProjectChip[];
  projects: Project[];
  selectedId: string | null;
  onSelect: (projectId: string | null) => void;
  onTogglePin: (projectId: string) => void;
}) {
  const { t } = useT("chat");
  const rowRef = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    if (!selectedId) return;
    const chip = rowRef.current?.querySelector(`[data-project-chip="${CSS.escape(selectedId)}"]`);
    chip?.scrollIntoView?.({ block: "nearest", inline: "nearest" });
  }, [selectedId]);
  const scrollSideways = (event: WheelEvent<HTMLDivElement>) => {
    if (Math.abs(event.deltaX) >= Math.abs(event.deltaY)) return;
    event.currentTarget.scrollLeft += event.deltaY;
  };
  if (chips.length === 0) return null;
  const projectById = new Map(projects.map((project) => [project.id, project]));

  return (
    <div
      ref={rowRef}
      role="toolbar"
      aria-label={t(($) => $.project_bar.label)}
      data-slot="chat-project-bar"
      onWheel={scrollSideways}
      className="flex shrink-0 items-center gap-1.5 overflow-x-auto border-b px-2 py-2 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
    >
      <button
        type="button"
        aria-pressed={selectedId === null}
        className={cn(CHIP_CLASS, "px-3", chipTone(selectedId === null))}
        onClick={() => onSelect(null)}
      >
        {t(($) => $.project_bar.all)}
      </button>
      {chips.map((chip) => {
        const project = projectById.get(chip.id);
        const name = project?.title ?? "";
        const active = selectedId === chip.id;
        return (
          <div
            key={chip.id}
            data-project-chip={chip.id}
            className={cn(CHIP_CLASS, "group", chipTone(active))}
          >
            <button
              type="button"
              aria-pressed={active}
              className="inline-flex h-full items-center gap-1.5 pl-2.5 pr-1"
              onClick={() => onSelect(active ? null : chip.id)}
            >
              <ProjectIcon project={project} size="sm" />
              {chip.hasUnread && (
                <span
                  role="img"
                  aria-label={t(($) => $.project_bar.unread)}
                  className={cn("size-1.5 shrink-0 rounded-full", active ? "bg-background" : "bg-brand")}
                />
              )}
              <span>{name}</span>
              <span className={cn("tabular-nums", active ? "text-background/70" : "text-muted-foreground")}>
                {chip.chatCount}
              </span>
            </button>
            <button
              type="button"
              aria-pressed={chip.pinned}
              aria-label={
                chip.pinned
                  ? t(($) => $.project_bar.unpin, { name })
                  : t(($) => $.project_bar.pin, { name })
              }
              className={cn(
                "inline-flex h-full items-center pl-0.5 pr-2.5",
                active ? "text-background/80 hover:text-background" : "text-muted-foreground hover:text-foreground",
                !chip.pinned &&
                  !active &&
                  "w-0 overflow-hidden px-0 opacity-0 group-hover:w-auto group-hover:pl-0.5 group-hover:pr-2.5 group-hover:opacity-100 focus-visible:w-auto focus-visible:pl-0.5 focus-visible:pr-2.5 focus-visible:opacity-100",
              )}
              onClick={() => onTogglePin(chip.id)}
            >
              <Pin className={cn("size-3", chip.pinned && "fill-current")} aria-hidden />
            </button>
          </div>
        );
      })}
    </div>
  );
}
