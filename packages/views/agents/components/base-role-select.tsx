"use client";

import { useMemo } from "react";
import type { Agent } from "@multica/core/types";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { useT } from "../../i18n";
import { baseRoleOptions } from "../specialization";

/** Sentinel for "no base role": the agent being created is a base role itself. */
export const NO_BASE_ROLE = "";

/**
 * Internal item value for the "independent" choice. The public contract stays
 * "an empty agent id means no base role", but the select needs a non-empty
 * option value: an empty string is indistinguishable from "nothing selected"
 * in the primitive's own value handling.
 */
const INDEPENDENT_ITEM_VALUE = "__independent__";

/**
 * The base-role picker for the create flows (DENE-304).
 *
 * The option list is base roles only — the server refuses a parent that is
 * itself a specialisation ("特化角色不能再派生"), and it refuses an archived one,
 * so offering either would only produce a 400. Empty means "independent base
 * role", which is what almost every create is; picking one turns the new agent
 * into a specialisation of it.
 *
 * The hint under the field spells out what selecting a base role actually
 * inherits — the prompt, plus the skills it already holds — because "inherit"
 * is otherwise only discoverable afterwards, on the detail page.
 */
export function BaseRoleSelect({
  agents,
  value,
  onChange,
  disabled = false,
  id = "agent-base-role",
  className,
}: {
  agents: readonly Agent[];
  value: string;
  onChange: (agentId: string) => void;
  disabled?: boolean;
  id?: string;
  className?: string;
}) {
  const { t } = useT("agents");
  const options = useMemo(() => baseRoleOptions(agents), [agents]);
  const selected = options.find((agent) => agent.id === value) ?? null;
  const items = useMemo(
    () => [
      {
        value: INDEPENDENT_ITEM_VALUE,
        label: t(($) => $.specialization.base_role_none),
      },
      ...options.map((agent) => ({ value: agent.id, label: agent.name })),
    ],
    [options, t],
  );
  const skillCount = selected?.skills?.length ?? 0;

  return (
    <div className={className}>
      <Select
        items={items}
        value={selected ? selected.id : INDEPENDENT_ITEM_VALUE}
        // A value that is not (or no longer) a selectable base role — an
        // archived parent from a stale deep link, say — reads as "independent"
        // rather than as a blank trigger, and never reaches the create call.
        onValueChange={(next: string | null) =>
          onChange(
            !next || next === INDEPENDENT_ITEM_VALUE ? NO_BASE_ROLE : next,
          )
        }
        disabled={disabled}
      >
        <SelectTrigger
          id={id}
          size="sm"
          className="w-full"
          aria-label={t(($) => $.specialization.base_role_label)}
        >
          <SelectValue>
            {selected?.name ?? t(($) => $.specialization.base_role_none)}
          </SelectValue>
        </SelectTrigger>
        <SelectContent>
          {items.map((item) => (
            <SelectItem key={item.value} value={item.value}>
              {item.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <p
        data-testid="agent-base-role-hint"
        className="mt-1.5 text-caption leading-snug text-muted-foreground"
      >
        {selected
          ? [
              t(($) => $.specialization.base_role_inherit_prompt),
              skillCount > 0
                ? t(($) => $.specialization.base_role_inherit_skills, {
                    count: skillCount,
                  })
                : null,
            ]
              .filter(Boolean)
              .join(" ")
          : t(($) => $.specialization.base_role_hint_none)}
      </p>
    </div>
  );
}
