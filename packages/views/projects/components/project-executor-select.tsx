"use client";

import { useState } from "react";
import { Bot, Check } from "lucide-react";
import type { Agent } from "@multica/core/types";
import { Popover, PopoverTrigger, PopoverContent } from "@multica/ui/components/ui/popover";
import { Sheet, SheetContent, SheetTitle } from "@multica/ui/components/ui/sheet";
import { useIsMobile } from "@multica/ui/hooks/use-mobile";
import { cn } from "@multica/ui/lib/utils";
import { PICKER_TRIGGER_CLASS } from "../../issues/components/pickers/property-picker";
import { useGuestReadOnly } from "../../layout/guest-readonly";
import { useT } from "../../i18n";

/**
 * The project's executor allowlist: automatic dispatch for tickets and chats
 * in this project only picks these agents; empty means any agent. Only
 * workspace admins edit it — others open the same list read-only with the
 * reason as its header, so a phone shows why too.
 */
export function ProjectExecutorSelect({
  agents,
  selected,
  canEdit,
  onChange,
}: {
  agents: Agent[];
  selected: string[];
  canEdit: boolean;
  onChange: (next: string[]) => void;
}) {
  const { t } = useT("projects");
  const isMobile = useIsMobile();
  const { isGuest } = useGuestReadOnly();
  const [open, setOpen] = useState(false);
  const editable = canEdit && !isGuest;

  // Listed agents stay visible even when archived, so the list never hides
  // an entry it still holds.
  const options = agents.filter((a) => !a.archived_at || selected.includes(a.id));
  const names = selected
    .map((id) => agents.find((a) => a.id === id)?.name)
    .filter((n): n is string => Boolean(n));
  const label = names.length > 0 ? names.join("、") : t(($) => $.detail.executors_any);
  const header = editable ? t(($) => $.detail.executors_header) : t(($) => $.detail.executors_admin_only);

  const toggle = (id: string) =>
    onChange(selected.includes(id) ? selected.filter((s) => s !== id) : [...selected, id]);

  const list = (
    <div role="listbox" aria-multiselectable className="p-1 max-h-72 overflow-y-auto">
      {options.map((a) => {
        const isSelected = selected.includes(a.id);
        return (
          <button
            key={a.id}
            type="button"
            role="option"
            aria-selected={isSelected}
            disabled={!editable}
            onClick={() => toggle(a.id)}
            className="flex w-full min-h-11 md:min-h-8 items-center gap-2 rounded-sm px-2 text-left text-body hover:bg-accent disabled:cursor-not-allowed disabled:hover:bg-transparent"
          >
            <span className={cn("flex-1 truncate", !editable && !isSelected && "text-muted-foreground")}>{a.name}</span>
            {isSelected && <Check className="size-3.5 shrink-0" />}
          </button>
        );
      })}
    </div>
  );
  const head = <div className="border-b px-3 py-2 text-caption text-muted-foreground">{header}</div>;
  const triggerContent = (
    <>
      <Bot className="size-3.5 shrink-0 text-muted-foreground" />
      <span className={cn("truncate", names.length === 0 && "text-muted-foreground")}>{label}</span>
    </>
  );

  if (isMobile) {
    return (
      <>
        <button type="button" className={PICKER_TRIGGER_CLASS} onClick={() => setOpen(true)}>
          {triggerContent}
        </button>
        <Sheet open={open} onOpenChange={setOpen}>
          <SheetContent side="bottom" showCloseButton={false} className="gap-0 px-2 pt-2">
            <SheetTitle className="sr-only">{t(($) => $.detail.prop_executors)}</SheetTitle>
            {head}
            {list}
          </SheetContent>
        </Sheet>
      </>
    );
  }

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger className={PICKER_TRIGGER_CLASS}>{triggerContent}</PopoverTrigger>
      <PopoverContent align="start" className="w-60 gap-0 p-0">
        {head}
        {list}
      </PopoverContent>
    </Popover>
  );
}
