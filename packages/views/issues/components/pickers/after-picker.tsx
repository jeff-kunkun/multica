"use client";

import { useState } from "react";
import { ListOrdered } from "lucide-react";
import { PropertyPicker, PickerItem } from "./property-picker";
import { useT } from "../../../i18n";

export interface AfterOption {
  id: string;
  identifier: string;
  title: string;
}

/**
 * Picks the sibling sub-issues a new sub-issue starts after (DENE-2671). The
 * server parks it in backlog while one of them is unfinished and starts it
 * when the last one is done. Several can be picked, so a row toggles and the
 * picker stays open.
 */
export function AfterPicker({
  options,
  selectedIds,
  onChange,
  triggerRender,
  tooltip,
  align,
}: {
  /** Open siblings under the same parent. */
  options: readonly AfterOption[];
  selectedIds: readonly string[];
  onChange: (ids: string[]) => void;
  triggerRender?: React.ReactElement;
  tooltip?: React.ReactNode;
  align?: "start" | "center" | "end";
}) {
  const [open, setOpen] = useState(false);
  const [filter, setFilter] = useState("");
  const { t } = useT("issues");
  const selected = options.filter((o) => selectedIds.includes(o.id));
  const query = filter.trim().toLowerCase();
  const shown = query
    ? options.filter((o) => o.identifier.toLowerCase().includes(query) || o.title.toLowerCase().includes(query))
    : options;

  return (
    <PropertyPicker
      open={open}
      onOpenChange={setOpen}
      width="w-72"
      align={align}
      triggerRender={triggerRender}
      tooltip={tooltip}
      searchable={options.length > 6}
      onSearchChange={setFilter}
      header={
        <div className="px-2 py-1.5 text-caption text-muted-foreground">
          {t(($) => $.after.hint)}
        </div>
      }
      trigger={
        <>
          <ListOrdered className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          <span className="truncate">
            {selected.length === 0
              ? t(($) => $.after.none)
              : t(($) => $.after.value, { list: selected.map((o) => o.identifier).join(", ") })}
          </span>
        </>
      }
    >
      <PickerItem
        emptyValue
        selected={selected.length === 0}
        onClick={() => {
          onChange([]);
          setOpen(false);
        }}
      >
        <span className="inline-flex items-center gap-1.5 text-caption text-muted-foreground">
          <ListOrdered className="h-3 w-3 shrink-0" />
          <span className="truncate">{t(($) => $.after.none)}</span>
        </span>
      </PickerItem>
      {options.length === 0 && (
        <div className="px-2 py-3 text-center text-caption text-muted-foreground">
          {t(($) => $.after.empty)}
        </div>
      )}
      {shown.map((o) => {
        const isSelected = selectedIds.includes(o.id);
        return (
          <PickerItem
            key={o.id}
            selected={isSelected}
            tooltip={o.title}
            onClick={() =>
              onChange(isSelected ? selectedIds.filter((id) => id !== o.id) : [...selectedIds, o.id])
            }
          >
            <span className="flex min-w-0 items-center gap-1.5 text-caption">
              <span className="shrink-0 text-muted-foreground">{o.identifier}</span>
              <span className="truncate">{o.title}</span>
            </span>
          </PickerItem>
        );
      })}
    </PropertyPicker>
  );
}
