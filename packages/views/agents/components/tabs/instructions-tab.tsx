"use client";

import { useEffect, useRef, useState } from "react";
import { Layers, Loader2, Lock, RotateCcw, Save } from "lucide-react";
import { paths, useWorkspaceSlug } from "@multica/core/paths";
import type { Agent } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { ReadonlyContent } from "../../../editor/readonly-content";
import { useT } from "../../../i18n";
import { AppLink } from "../../../navigation";
import { SettingsViewTabs } from "../../../settings/components/settings-layout";
import {
  composeEffectiveInstructions,
  hasInheritedPrompt,
  isSpecialization,
  type InheritedPromptState,
} from "../../specialization";
import { BaseRoleField } from "../base-role-field";

type EditorMode = "write" | "preview";

export function InstructionsTab({
  agent,
  onSave,
  onDirtyChange,
  childAgents = [],
  inheritedPromptState = "ready",
  onRetryInheritedPrompt,
  agents,
  canEdit = true,
  onChangeBaseRole,
  onDetachBaseRole,
}: {
  agent: Agent;
  onSave: (updates: { instructions: string }) => Promise<void>;
  onDirtyChange?: (dirty: boolean) => void;
  /**
   * Active specialisations of this agent, when it is a base role. Supplied by
   * the page (which already holds the list) so the tab can say which agents a
   * prompt edit here reaches (DENE-304).
   */
  childAgents?: readonly Agent[];
  /**
   * How the read that carries `inherited_instructions` went. The page owns the
   * query, so it is the only layer that can tell an in-flight or failed read
   * from a base role that really has no prompt — and stating the latter when
   * the read failed is exactly the misreport this pins down (DENE-384).
   * Defaults to `ready` for the standalone mounts.
   */
  inheritedPromptState?: InheritedPromptState;
  /** Re-runs that read; the failure state offers it as the way out. */
  onRetryInheritedPrompt?: () => void;
  /**
   * The workspace agent list, for the base-role picker below (DENE-300
   * follow-up). Omitted by the standalone mounts, which renders no picker —
   * the same shape a backend without the specialisation fields produces.
   */
  agents?: readonly Agent[];
  canEdit?: boolean;
  /** Attaches this agent to a base role by id. */
  onChangeBaseRole?: (parentAgentId: string) => Promise<void>;
  /** Solidify-and-unbind: bakes the inherited prompt in, then detaches. */
  onDetachBaseRole?: () => Promise<void>;
}) {
  const { t } = useT("agents");
  // Optional read: this tab is mounted standalone in tests, and a missing
  // workspace slug only costs the child links their href (they render as plain
  // text below), not the whole tab.
  const slug = useWorkspaceSlug();
  const workspacePaths = slug ? paths.workspace(slug) : null;
  const [value, setValue] = useState(agent.instructions ?? "");
  const [mode, setMode] = useState<EditorMode>("write");
  const [saving, setSaving] = useState(false);
  const [systemOpen, setSystemOpen] = useState(false);
  // Both inheritance blocks start folded: the parent prompt is context, and
  // the effective prompt is a long string that would push the editable field
  // out of view.
  const [inheritedOpen, setInheritedOpen] = useState(false);
  const [effectiveOpen, setEffectiveOpen] = useState(false);
  const persisted = agent.instructions ?? "";
  const persistedRef = useRef({ agentId: agent.id, instructions: persisted });
  const localRef = useRef(value);
  localRef.current = value;
  const isDirty = value !== persisted;

  // A system agent's prompt has two halves: the product half ships with the
  // backend and updates on deploy, so it is shown read-only; the editable
  // field below holds only this workspace's own notes, which no release
  // overwrites. Ordinary agents have no system half and render unchanged.
  const systemInstructions = agent.system_instructions?.trim() ?? "";
  const hasSystemLayer = systemInstructions.length > 0;

  // DENE-304 inheritance. `inherited_instructions` is only served by the agent
  // DETAIL endpoint, so a specialisation opened from a list that has not
  // loaded it yet renders the "nothing inherited" state rather than a blank
  // block passed off as the parent's prompt.
  //
  // DENE-384: "not loaded it yet" and "failed to load it" are NOT that state.
  // The copy below never claims the base role has no prompt while the read is
  // still in flight or has failed — the page says which of the three it is.
  const isSpecializationAgent = isSpecialization(agent);
  const inheritedInstructions = agent.inherited_instructions ?? "";
  const inheritedPromptReady = inheritedPromptState === "ready";
  const hasInherited = inheritedPromptReady && hasInheritedPrompt(agent);
  const parentName = agent.parent_agent_name || "";
  const inheritedPromptStatusText = inheritedPromptReady
    ? t(($) => $.specialization.inherited_prompt_unavailable)
    : inheritedPromptState === "failed"
      ? t(($) => $.specialization.inherited_prompt_failed)
      : t(($) => $.specialization.inherited_prompt_loading);
  const effectivePrompt = composeEffectiveInstructions(
    inheritedInstructions,
    value,
  );

  const fieldLabel = isSpecializationAgent
    ? t(($) => $.specialization.own_prompt_label)
    : hasSystemLayer
      ? t(($) => $.tab_body.instructions.workspace_notes_label)
      : t(($) => $.tab_body.instructions.system_prompt_label);

  // Refetches can deliver the same instructions again or a newer server copy.
  // A server change is adopted only when the local editor was clean, so a
  // refetch never erases an edit; switching agents always loads the selection.
  useEffect(() => {
    const previous = persistedRef.current;
    const switchingAgents = previous.agentId !== agent.id;

    // The parent publishes the value optimistically while a save is in
    // flight. Neither that snapshot nor a later rollback is confirmed server
    // state, so keep the submitted value available for retry on failure.
    if (!switchingAgents && saving) return;

    const wasLocallyDirty = localRef.current !== previous.instructions;
    persistedRef.current = { agentId: agent.id, instructions: persisted };
    if (
      switchingAgents ||
      (!wasLocallyDirty && previous.instructions !== persisted)
    ) {
      setValue(persisted);
    }
  }, [agent.id, persisted, saving]);

  // Report dirty state up so the parent can guard tab switches.
  useEffect(() => {
    onDirtyChange?.(isDirty);
  }, [isDirty, onDirtyChange]);

  const handleSave = async () => {
    setSaving(true);
    try {
      await onSave({ instructions: value });
    } catch {
      // toast handled by parent
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="space-y-5">
      <p className="max-w-2xl text-pretty text-body leading-6 text-muted-foreground">
        {hasSystemLayer
          ? t(($) => $.tab_body.instructions.workspace_notes_intro)
          : isSpecializationAgent
            ? t(($) => $.specialization.specialization_intro, {
                name: parentName,
              })
            : t(($) => $.tab_body.instructions.intro)}
      </p>

      {/* Which base role this agent hangs off — editable here, because
          picking one at creation is no help to the agents that already exist
          (DENE-300 follow-up). */}
      {agents && onChangeBaseRole && onDetachBaseRole && !hasSystemLayer && (
        <BaseRoleField
          agent={agent}
          agents={agents}
          visibleChildren={childAgents.length}
          canEdit={canEdit}
          onAttach={onChangeBaseRole}
          onDetach={onDetachBaseRole}
        />
      )}

      {/* Base-role view: the prompt below is shared with every specialisation,
          so say so before the user edits it (DENE-304). */}
      {childAgents.length > 0 && (
        <div
          data-testid="agent-specialization-children"
          className="rounded-lg border border-warning/30 bg-warning/5 px-3 py-2.5"
        >
          <p className="text-caption leading-snug">
            {t(($) => $.specialization.base_sync_hint, { name: agent.name })}
          </p>
          <ul className="mt-1.5 flex flex-wrap gap-1.5">
            {childAgents.map((child) => (
              <li key={child.id}>
                {workspacePaths ? (
                  <AppLink
                    href={workspacePaths.agentDetail(child.id)}
                    newTabTitle={child.name}
                    className="inline-flex max-w-40 items-center gap-1 rounded-xs border border-border bg-background px-1.5 py-0.5 text-caption text-muted-foreground hover:text-foreground hover:underline"
                  >
                    <Layers aria-hidden="true" className="size-3 shrink-0" />
                    <span className="min-w-0 truncate">{child.name}</span>
                  </AppLink>
                ) : (
                  <span className="inline-flex max-w-40 items-center gap-1 rounded-xs border border-border bg-background px-1.5 py-0.5 text-caption text-muted-foreground">
                    <Layers aria-hidden="true" className="size-3 shrink-0" />
                    <span className="min-w-0 truncate">{child.name}</span>
                  </span>
                )}
              </li>
            ))}
          </ul>
        </div>
      )}

      {/* Specialisation view: the base role's prompt is not editable from here,
          and it is shown as its own block so the field below reads as the
          ADDITIONAL half rather than as the whole prompt. */}
      {isSpecializationAgent && (
        <div
          data-testid="agent-inherited-prompt"
          className="rounded-lg border bg-muted/30"
        >
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2.5">
            <Lock
              aria-hidden="true"
              className="size-3.5 shrink-0 text-muted-foreground"
            />
            <span className="text-body font-medium">
              {t(($) => $.specialization.inherited_prompt_label)}
            </span>
            <p className="min-w-0 flex-1 text-caption leading-snug text-muted-foreground">
              {hasInherited
                ? t(($) => $.specialization.inherited_prompt_hint, {
                    name: parentName,
                  })
                : inheritedPromptStatusText}
            </p>
            {inheritedPromptState === "failed" && onRetryInheritedPrompt && (
              <Button
                size="sm"
                variant="ghost"
                className="shrink-0"
                data-testid="agent-inherited-prompt-retry"
                onClick={onRetryInheritedPrompt}
              >
                <RotateCcw aria-hidden="true" className="size-3.5" />
                {t(($) => $.specialization.inherited_prompt_retry)}
              </Button>
            )}
            {hasInherited && (
              <Button
                size="sm"
                variant="ghost"
                className="shrink-0"
                aria-expanded={inheritedOpen}
                onClick={() => setInheritedOpen((open) => !open)}
              >
                {inheritedOpen
                  ? t(($) => $.specialization.inherited_prompt_hide)
                  : t(($) => $.specialization.inherited_prompt_show)}
              </Button>
            )}
          </div>
          {hasInherited && inheritedOpen && (
            <pre className="max-h-80 overflow-auto border-t px-3 py-2.5 text-caption leading-6 whitespace-pre-wrap text-muted-foreground">
              {inheritedInstructions}
            </pre>
          )}
        </div>
      )}

      {hasSystemLayer && (
        <div className="rounded-lg border bg-muted/30">
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2.5">
            <span className="text-body font-medium">
              {t(($) => $.tab_body.instructions.system_layer_label)}
            </span>
            <p className="min-w-0 flex-1 text-caption leading-snug text-muted-foreground">
              {t(($) => $.tab_body.instructions.system_layer_hint)}
            </p>
            <Button
              size="sm"
              variant="ghost"
              className="shrink-0"
              aria-expanded={systemOpen}
              onClick={() => setSystemOpen((open) => !open)}
            >
              {systemOpen
                ? t(($) => $.tab_body.instructions.system_layer_hide)
                : t(($) => $.tab_body.instructions.system_layer_show)}
            </Button>
          </div>
          {systemOpen && (
            <pre className="max-h-80 overflow-auto border-t px-3 py-2.5 text-caption leading-6 whitespace-pre-wrap text-muted-foreground">
              {systemInstructions}
            </pre>
          )}
        </div>
      )}

      {/* No overflow clipping on the card: the footer is sticky inside the
          page's scroll container, so Save stays reachable on a long prompt. */}
      <div className="rounded-xl border bg-card transition-[border-color,box-shadow] has-[textarea:focus-visible]:border-ring has-[textarea:focus-visible]:ring-3 has-[textarea:focus-visible]:ring-ring/50">
        <div className="flex h-11 items-center gap-3 border-b px-2">
          {hasSystemLayer || isSpecializationAgent ? (
            <span className="pl-1 text-label font-medium">{fieldLabel}</span>
          ) : null}
          <SettingsViewTabs<EditorMode>
            label={t(($) => $.tab_body.instructions.mode_aria)}
            items={[
              { value: "write", label: t(($) => $.tab_body.instructions.mode_write) },
              { value: "preview", label: t(($) => $.tab_body.instructions.mode_preview) },
            ]}
            value={mode}
            onChange={setMode}
          />
          <span className="ml-auto pr-1 text-caption tabular-nums text-muted-foreground">
            {t(($) => $.tab_body.instructions.char_count, {
              count: [...value].length,
            })}
          </span>
        </div>

        {mode === "write" ? (
          <Textarea
            id={`agent-system-prompt-${agent.id}`}
            name="agent-system-prompt"
            autoComplete="off"
            aria-label={fieldLabel}
            value={value}
            onChange={(event) => setValue(event.target.value)}
            placeholder={
              isSpecializationAgent
                ? t(($) => $.specialization.own_prompt_placeholder)
                : hasSystemLayer
                  ? t(($) => $.tab_body.instructions.workspace_notes_placeholder)
                  : t(($) => $.tab_body.instructions.placeholder)
            }
            className="min-h-[28rem] resize-none rounded-none border-0 bg-transparent px-4 py-3 font-mono text-label leading-6 shadow-none focus-visible:ring-0 md:text-label dark:bg-transparent"
          />
        ) : (
          <div className="min-h-[28rem] px-4 py-3">
            {value.trim() ? (
              <ReadonlyContent content={value} />
            ) : (
              <p className="text-body text-muted-foreground">
                {t(($) => $.tab_body.instructions.preview_empty)}
              </p>
            )}
          </div>
        )}

        <div className="sticky bottom-0 flex min-h-12 items-center gap-2 rounded-b-xl border-t bg-card py-2 pr-2 pl-4">
          {isDirty ? (
            <span className="flex min-w-0 items-center gap-2 text-caption text-muted-foreground">
              <span
                aria-hidden="true"
                className="size-1.5 shrink-0 rounded-full bg-warning"
              />
              {t(($) => $.tab_body.common.unsaved_changes)}
            </span>
          ) : null}
          <div className="ml-auto flex items-center gap-1.5">
            {isDirty ? (
              <Button
                size="sm"
                variant="ghost"
                disabled={saving}
                onClick={() => setValue(persisted)}
              >
                {t(($) => $.tab_body.common.discard)}
              </Button>
            ) : null}
            <Button size="sm" onClick={handleSave} disabled={!isDirty || saving}>
              {saving ? (
                <Loader2
                  className="h-3.5 w-3.5 animate-spin motion-reduce:animate-none"
                  aria-hidden="true"
                />
              ) : (
                <Save className="h-3.5 w-3.5" aria-hidden="true" />
              )}
              {t(($) => $.tab_body.common.save)}
            </Button>
          </div>
        </div>
      </div>

      {/* What the daemon is actually handed: parent first, then this agent's
          own text — composed with the same rule the claim path uses. Shown
          live off the editor draft, so an edit in progress is previewed before
          it is saved. */}
      {isSpecializationAgent && (
        <div
          data-testid="agent-effective-prompt"
          className="rounded-lg border bg-muted/30"
        >
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2.5">
            <span className="text-body font-medium">
              {t(($) => $.specialization.effective_prompt_label)}
            </span>
            <p className="min-w-0 flex-1 text-caption leading-snug text-muted-foreground">
              {t(($) => $.specialization.effective_prompt_hint)}
            </p>
            <Button
              size="sm"
              variant="ghost"
              className="shrink-0"
              aria-expanded={effectiveOpen}
              onClick={() => setEffectiveOpen((open) => !open)}
            >
              {effectiveOpen
                ? t(($) => $.specialization.inherited_prompt_hide)
                : t(($) => $.specialization.inherited_prompt_show)}
            </Button>
          </div>
          {effectiveOpen &&
            (!inheritedPromptReady ? (
              // The parent half is unknown, so there is no honest composed
              // string to preview — showing just this agent's own text as
              // "exactly what the runtime receives" would be a lie in the
              // same way the inherited block's copy was (DENE-384).
              <p
                data-testid="agent-effective-prompt-unknown"
                className="border-t px-3 py-2.5 text-caption text-muted-foreground"
              >
                {inheritedPromptStatusText}
              </p>
            ) : effectivePrompt ? (
              <pre
                data-testid="agent-effective-prompt-text"
                className="max-h-80 overflow-auto border-t px-3 py-2.5 text-caption leading-6 whitespace-pre-wrap"
              >
                {effectivePrompt}
              </pre>
            ) : (
              <p className="border-t px-3 py-2.5 text-caption text-muted-foreground">
                {t(($) => $.specialization.effective_prompt_empty)}
              </p>
            ))}
        </div>
      )}
    </div>
  );
}
