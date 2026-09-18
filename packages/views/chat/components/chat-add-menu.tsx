"use client";

import { useRef, useState } from "react";
import { ArrowLeftRight, FolderKanban, Image as ImageIcon, Plus, X } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { toggleProjectId } from "@multica/core/chat/project-context";
import type { Project } from "@multica/core/types";
import { ProjectIcon } from "../../projects/components/project-icon";
import { useT } from "../../i18n";

interface ChatAddMenuProps {
  /** Called with each selected file — the caller routes it through the
   *  editor's upload extension, same path as paste / drag-drop. */
  onSelectFile?: (file: File) => void;
  projects?: Project[];
  /** The attached projects, in selection order. */
  projectIds?: string[];
  /** The project the main UI is currently on. It heads the submenu's first
   *  screen, which is otherwise limited to what is already attached — the
   *  full workspace list is one click away (DENE-603 §3). */
  currentProjectId?: string | null;
  /** Called with the COMPLETE next set — this menu toggles one entry at a
   *  time, but the set is what the session stores, so the caller never has to
   *  reconstruct it from an add/remove event. */
  onProjectsChange?: (projectIds: string[]) => void;
  /** Soft warning: the active agent's daemon is too old to receive the
   *  project description. Selection stays enabled; the submenu only appends
   *  an explanatory hint so the user knows before choosing. */
  projectContextUnsupported?: boolean;
  disabled?: boolean;
}

/**
 * The "+" affordance at the bottom-left of the chat composer. Replaces the
 * standalone paperclip button: file upload now lives here as a submenu entry,
 * leaving room for future add-actions (agents, skills, tools) under one entry
 * point without crowding the input bar.
 */
export function ChatAddMenu({
  onSelectFile,
  projects = [],
  projectIds = [],
  currentProjectId,
  onProjectsChange,
  projectContextUnsupported,
  disabled,
}: ChatAddMenuProps) {
  const { t } = useT("chat");
  const inputRef = useRef<HTMLInputElement>(null);
  // Cross-project picking is the rare case: the submenu opens focused on the
  // project at hand and only expands to the whole workspace on request. Reset
  // on close so the next open starts focused again.
  const [showAllProjects, setShowAllProjects] = useState(false);

  // The focused screen. Falls back to the full list when it would otherwise be
  // empty — an empty first screen teaches the user nothing and costs a click.
  const focusedProjects = projects.filter(
    (project) => project.id === currentProjectId || projectIds.includes(project.id),
  );
  const visibleProjects =
    showAllProjects || focusedProjects.length === 0 ? projects : focusedProjects;
  const canExpand = !showAllProjects && visibleProjects.length < projects.length;

  const handleChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(e.target.files ?? []);
    if (files.length === 0) return;
    e.target.value = "";
    for (const file of files) onSelectFile?.(file);
  };

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger
          render={
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              disabled={disabled}
              aria-label={t(($) => $.input.add_tooltip)}
              title={t(($) => $.input.add_tooltip)}
              className="rounded-full text-muted-foreground"
            >
              <Plus />
            </Button>
          }
        />
        <DropdownMenuContent align="start" side="top" sideOffset={6}>
          {onSelectFile && (
            <DropdownMenuItem onClick={() => inputRef.current?.click()}>
              <ImageIcon />
              {t(($) => $.input.upload_file)}
            </DropdownMenuItem>
          )}
          {onProjectsChange && (
            <DropdownMenuSub onOpenChange={(open) => !open && setShowAllProjects(false)}>
              <DropdownMenuSubTrigger>
                <FolderKanban />
                <span className="flex-1">{t(($) => $.input.project_context)}</span>
                {projectIds.length > 0 && (
                  <span className="text-caption text-muted-foreground tabular-nums">
                    {projectIds.length}
                  </span>
                )}
              </DropdownMenuSubTrigger>
              <DropdownMenuSubContent className="max-h-72 min-w-52 overflow-y-auto">
                {visibleProjects.map((project) => (
                  // Checkbox items keep the menu open on click (Base UI), which
                  // is the point: attaching two or three projects is one trip.
                  <DropdownMenuCheckboxItem
                    key={project.id}
                    checked={projectIds.includes(project.id)}
                    onCheckedChange={() =>
                      onProjectsChange(toggleProjectId(projectIds, project.id))
                    }
                  >
                    <ProjectIcon project={project} size="md" />
                    <span className="min-w-0 flex-1 truncate">{project.title}</span>
                  </DropdownMenuCheckboxItem>
                ))}
                {canExpand && (
                  // Not a checkbox item: this opens the rest of the workspace
                  // rather than attaching anything, so it must not close the
                  // menu or look like a selection.
                  <DropdownMenuItem
                    closeOnClick={false}
                    onClick={() => setShowAllProjects(true)}
                  >
                    <ArrowLeftRight />
                    {t(($) => $.input.switch_project)}
                  </DropdownMenuItem>
                )}
                {projects.length === 0 && (
                  <div className="px-2 py-1.5 text-caption text-muted-foreground">
                    {t(($) => $.input.no_projects)}
                  </div>
                )}
                {projectIds.length > 0 && <DropdownMenuSeparator />}
                {projectIds.length > 0 && (
                  <DropdownMenuItem onClick={() => onProjectsChange([])}>
                    <X />
                    {t(($) => $.input.remove_all_project_context)}
                  </DropdownMenuItem>
                )}
                {projectContextUnsupported && (
                  <>
                    <DropdownMenuSeparator />
                    <div className="max-w-56 px-2 py-1.5 text-caption text-muted-foreground">
                      {t(($) => $.input.project_context_unsupported)}
                    </div>
                  </>
                )}
              </DropdownMenuSubContent>
            </DropdownMenuSub>
          )}
        </DropdownMenuContent>
      </DropdownMenu>
      {onSelectFile && (
        <input
          ref={inputRef}
          type="file"
          multiple
          className="hidden"
          onChange={handleChange}
        />
      )}
    </>
  );
}
