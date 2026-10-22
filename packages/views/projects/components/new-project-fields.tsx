"use client";

import { useState } from "react";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { useT } from "../../i18n";
import type { NewProjectDirectoryDraft } from "../new-project-directory";
import { patchDirectoryDraft } from "../new-project-directory";

/**
 * The few fields a project needs at the moment it is created: a name, an
 * icon, and — on this machine — the folder tasks will run in.
 */
export function NewProjectFields({
  name,
  onNameChange,
  icon,
  onIconChange,
  directory,
  onDirectoryChange,
  directoryAvailable,
  disabled = false,
}: {
  name: string;
  onNameChange: (name: string) => void;
  icon: string;
  onIconChange: (icon: string) => void;
  directory: NewProjectDirectoryDraft;
  onDirectoryChange: (next: NewProjectDirectoryDraft) => void;
  directoryAvailable: boolean;
  disabled?: boolean;
}) {
  const { t } = useT("projects");
  const [showIcon, setShowIcon] = useState(false);
  const patch = (next: Partial<NewProjectDirectoryDraft>) =>
    onDirectoryChange(patchDirectoryDraft(directory, next));

  return (
    <div className="flex flex-col gap-2 px-1 py-1">
      <div className="flex items-center gap-1.5">
        <button
          type="button"
          className="flex size-7 shrink-0 items-center justify-center rounded-md border text-body"
          onClick={() => setShowIcon((open) => !open)}
          disabled={disabled}
          aria-label={t(($) => $.new_project.icon)}
        >
          {icon || "📁"}
        </button>
        <Input
          value={name}
          disabled={disabled}
          aria-label={t(($) => $.new_project.name)}
          onChange={(event) => onNameChange(event.target.value)}
        />
      </div>
      {showIcon ? (
        <Input
          value={icon}
          disabled={disabled}
          placeholder={t(($) => $.new_project.icon_placeholder)}
          aria-label={t(($) => $.new_project.icon)}
          onChange={(event) => onIconChange(event.target.value)}
        />
      ) : null}

      <label className="flex items-center gap-2 text-caption">
        <input
          type="checkbox"
          checked={directory.enabled}
          disabled={disabled || !directoryAvailable}
          onChange={(event) => patch({ enabled: event.target.checked })}
        />
        {t(($) => $.new_project.directory_enable)}
      </label>
      {!directoryAvailable ? (
        <p className="text-caption text-muted-foreground">
          {t(($) => $.new_project.directory_unavailable)}
        </p>
      ) : null}
      {directory.enabled ? (
        <div className="flex flex-col gap-1.5">
          <label className="flex flex-col gap-1 text-caption text-muted-foreground">
            {t(($) => $.new_project.directory_root)}
            <Input
              value={directory.root}
              disabled={disabled}
              onChange={(event) => patch({ root: event.target.value })}
            />
          </label>
          <label className="flex items-center gap-2 text-caption">
            <input
              type="checkbox"
              checked={directory.container}
              disabled={disabled}
              onChange={(event) => patch({ container: event.target.checked })}
            />
            {t(($) => $.new_project.directory_container)}
          </label>
          {directory.container ? null : (
            <>
              <label className="flex flex-col gap-1 text-caption text-muted-foreground">
                {t(($) => $.new_project.directory_name)}
                <Input
                  value={directory.dirName}
                  disabled={disabled}
                  onChange={(event) => patch({ dirName: event.target.value })}
                />
              </label>
              <label className="flex items-center gap-2 text-caption">
                <input
                  type="checkbox"
                  checked={directory.gitInit}
                  disabled={disabled}
                  onChange={(event) => patch({ gitInit: event.target.checked })}
                />
                {t(($) => $.new_project.directory_git)}
              </label>
            </>
          )}
        </div>
      ) : null}
    </div>
  );
}

export function NewProjectActions({
  onBack,
  onSubmit,
  pending,
  submitLabel,
}: {
  onBack: () => void;
  onSubmit: () => void;
  pending: boolean;
  submitLabel: string;
}) {
  const { t } = useT("projects");
  return (
    <div className="flex justify-end gap-1.5 px-1 pb-1">
      <Button type="button" variant="ghost" size="sm" onClick={onBack} disabled={pending}>
        {t(($) => $.new_project.back)}
      </Button>
      <Button type="button" size="sm" onClick={onSubmit} disabled={pending}>
        {submitLabel}
      </Button>
    </div>
  );
}
