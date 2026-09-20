"use client";

import { Plus, Trash2 } from "lucide-react";
import {
  AGENT_SWITCHABLE_MODELS_MAX,
  AGENT_SWITCHABLE_MODEL_ID_MAX_LENGTH,
  AGENT_SWITCHABLE_MODEL_NOTE_MAX_LENGTH,
  AGENT_SWITCHABLE_MODEL_ROLES,
} from "@multica/core/agents";
import type {
  AgentSwitchableModel,
  AgentSwitchableModelRole,
} from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { useT } from "../../i18n";

/**
 * Row editor for an agent's display-only model lineup (DENE-200). The list is
 * never consulted for routing — it documents which model the agent normally
 * runs, what it degrades to, and what it may borrow for batch work — so the
 * model field is free text rather than a runtime-discovered picker: a fallback
 * entry routinely names a model this agent's own runtime cannot serve.
 *
 * Turning the lineup off entirely is the parent's job (DENE-610): an empty
 * list is the "single model" state.
 */
export function SwitchableModelsEditor({
  value,
  onChange,
  disabled = false,
}: {
  value: AgentSwitchableModel[];
  onChange: (value: AgentSwitchableModel[]) => void;
  disabled?: boolean;
}) {
  const { t } = useT("agents");

  const roleLabel = (role: AgentSwitchableModelRole) => {
    switch (role) {
      case "default":
        return t(($) => $.detail.switchable_role_default);
      case "fallback":
        return t(($) => $.detail.switchable_role_fallback);
      case "batch":
        return t(($) => $.detail.switchable_role_batch);
      default:
        return role;
    }
  };

  const roleItems = AGENT_SWITCHABLE_MODEL_ROLES.map((role) => ({
    value: role,
    label: roleLabel(role),
  }));

  const update = <K extends keyof AgentSwitchableModel>(
    index: number,
    field: K,
    nextValue: AgentSwitchableModel[K],
  ) => {
    onChange(
      value.map((row, rowIndex) =>
        rowIndex === index ? { ...row, [field]: nextValue } : row,
      ),
    );
  };

  const hasBlankModel = value.some((row) => row.model.trim().length === 0);

  return (
    <div className="space-y-3">
      {value.map((row, index) => (
        <div
          key={index}
          data-testid={`switchable-model-row-${index}`}
          className="rounded-lg border bg-muted/20 p-3"
        >
          <div className="flex items-center gap-2">
            <Input
              value={row.model}
              maxLength={AGENT_SWITCHABLE_MODEL_ID_MAX_LENGTH}
              disabled={disabled}
              spellCheck={false}
              autoComplete="off"
              translate="no"
              className="font-mono"
              aria-label={t(($) => $.switchable_models.model_label, {
                number: index + 1,
              })}
              placeholder={t(($) => $.switchable_models.model_placeholder)}
              onChange={(event) => update(index, "model", event.target.value)}
            />
            <Select
              items={roleItems}
              value={row.role}
              disabled={disabled}
              onValueChange={(next) =>
                update(index, "role", (next ?? "fallback") as AgentSwitchableModelRole)
              }
            >
              <SelectTrigger
                className="w-32 shrink-0"
                aria-label={t(($) => $.switchable_models.role_label, {
                  number: index + 1,
                })}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {roleItems.map((item) => (
                  <SelectItem key={item.value} value={item.value}>
                    {item.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              disabled={disabled}
              aria-label={t(($) => $.switchable_models.remove, {
                number: index + 1,
              })}
              onClick={() =>
                onChange(value.filter((_, rowIndex) => rowIndex !== index))
              }
            >
              <Trash2 className="size-4" aria-hidden="true" />
            </Button>
          </div>
          <Input
            value={row.note}
            maxLength={AGENT_SWITCHABLE_MODEL_NOTE_MAX_LENGTH}
            disabled={disabled}
            autoComplete="off"
            className="mt-2"
            aria-label={t(($) => $.switchable_models.note_label, {
              number: index + 1,
            })}
            placeholder={t(($) => $.switchable_models.note_placeholder)}
            onChange={(event) => update(index, "note", event.target.value)}
          />
        </div>
      ))}

      {value.length < AGENT_SWITCHABLE_MODELS_MAX ? (
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={disabled}
          onClick={() =>
            onChange([...value, { model: "", role: "fallback", note: "" }])
          }
        >
          <Plus className="size-4" aria-hidden="true" />
          {t(($) => $.switchable_models.add)}
        </Button>
      ) : null}

      {hasBlankModel ? (
        <p className="text-caption text-muted-foreground" role="status">
          {t(($) => $.switchable_models.blank_ignored)}
        </p>
      ) : null}
    </div>
  );
}
