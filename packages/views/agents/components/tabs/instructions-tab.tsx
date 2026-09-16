"use client";

import { useEffect, useRef, useState } from "react";
import { Layers, Loader2, Lock, Save } from "lucide-react";
import { useConfigStore } from "@multica/core/config";
import { AGENT_FOCUS_CONVERSATION_STARTERS } from "@multica/core/paths";
import { paths, useWorkspaceSlug } from "@multica/core/paths";
import type { Agent, AgentConversationStarter } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../../i18n";
import { AppLink, useOptionalNavigation } from "../../../navigation";
import {
  composeEffectiveInstructions,
  hasInheritedPrompt,
  isSpecialization,
} from "../../specialization";

import { ConversationStartersEditor } from "../conversation-starters-editor";

/** How long the deep-linked conversation-starters editor stays ringed. */
const FOCUS_FLASH_MS = 1600;

export function InstructionsTab({
  agent,
  onSave,
  onDirtyChange,
  childAgents = [],
}: {
  agent: Agent;
  onSave: (updates: {
    instructions: string;
    conversation_starters?: AgentConversationStarter[];
  }) => Promise<void>;
  onDirtyChange?: (dirty: boolean) => void;
  /**
   * Active specialisations of this agent, when it is a base role. Supplied by
   * the page (which already holds the list) so the tab can say which agents a
   * prompt edit here reaches (DENE-304).
   */
  childAgents?: readonly Agent[];
}) {
  const { t } = useT("agents");
  // Optional read: this tab is a leaf that tests mount in isolation, and its
  // only navigation-dependent behaviour (the deep-link focus below) degrades
  // to "no highlight" when there is no adapter.
  const navigation = useOptionalNavigation();
  // Optional read for the same reason as `navigation`: this tab is mounted
  // standalone in tests, and a missing workspace slug only costs the child
  // links their href (they render as plain text below), not the whole tab.
  const slug = useWorkspaceSlug();
  const workspacePaths = slug ? paths.workspace(slug) : null;
  const conversationStartersSupported = useConfigStore(
    (state) => state.agentConversationStartersSupported,
  );
  const [value, setValue] = useState(agent.instructions ?? "");
  const [conversationStarters, setConversationStarters] = useState<AgentConversationStarter[]>(
    agent.conversation_starters ?? [],
  );
  const [saving, setSaving] = useState(false);
  const [conversationStartersFocused, setConversationStartersFocused] = useState(false);
  const conversationStartersRef = useRef<HTMLDivElement | null>(null);
  const focusHandledForAgentRef = useRef<string | null>(null);
  const focusFlashTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [systemOpen, setSystemOpen] = useState(false);
  // Both inheritance blocks start folded: the parent prompt is context, and
  // the effective prompt is a long string that would push the editable field
  // out of view.
  const [inheritedOpen, setInheritedOpen] = useState(false);
  const [effectiveOpen, setEffectiveOpen] = useState(false);
  const persistedConversationStartersKey = JSON.stringify(
    agent.conversation_starters ?? [],
  );
  const persistedRef = useRef({
    agentId: agent.id,
    instructions: agent.instructions ?? "",
    conversationStartersKey: persistedConversationStartersKey,
  });
  const localRef = useRef({
    instructions: value,
    conversationStartersKey: JSON.stringify(conversationStarters),
  });
  localRef.current = {
    instructions: value,
    conversationStartersKey: JSON.stringify(conversationStarters),
  };
  const isDirty =
    value !== (agent.instructions ?? "") ||
    (conversationStartersSupported &&
      JSON.stringify(conversationStarters) !== persistedConversationStartersKey);
  const conversationStartersValid =
    !conversationStartersSupported ||
    conversationStarters.every(
      (item) => item.label.trim() && item.prompt.trim(),
    );

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
  const isSpecializationAgent = isSpecialization(agent);
  const inheritedInstructions = agent.inherited_instructions ?? "";
  const hasInherited = hasInheritedPrompt(agent);
  const parentName = agent.parent_agent_name || "";
  const effectivePrompt = composeEffectiveInstructions(
    inheritedInstructions,
    value,
  );

  // Refetches replace nested arrays even when their contents are unchanged.
  // Compare against the last persisted semantic snapshot so those refetches
  // do not erase local edits. A real server change is adopted only when the
  // local form was clean; switching agents always loads the selected agent.
  useEffect(() => {
    const previous = persistedRef.current;
    const switchingAgents = previous.agentId !== agent.id;

    // The parent publishes these fields optimistically while a save is in
    // flight. Neither that snapshot nor a later rollback is confirmed server
    // state, so keep the submitted values available for retry on failure.
    if (!switchingAgents && saving) return;

    const local = localRef.current;
    const wasLocallyDirty =
      local.instructions !== previous.instructions ||
      local.conversationStartersKey !== previous.conversationStartersKey;
    const persistedContentsChanged =
      previous.instructions !== (agent.instructions ?? "") ||
      previous.conversationStartersKey !== persistedConversationStartersKey;

    persistedRef.current = {
      agentId: agent.id,
      instructions: agent.instructions ?? "",
      conversationStartersKey: persistedConversationStartersKey,
    };
    if (switchingAgents || (!wasLocallyDirty && persistedContentsChanged)) {
      setValue(agent.instructions ?? "");
      setConversationStarters(
        JSON.parse(persistedConversationStartersKey) as AgentConversationStarter[],
      );
    }
  }, [agent.id, agent.instructions, persistedConversationStartersKey, saving]);

  // Arriving from "customize" in a chat's empty state: bring the starter
  // prompts into view and flash them, so the deep link lands ON the setting
  // rather than on a long page that happens to contain it. The param is
  // consumed on arrival, so a refresh never replays an animation nobody asked
  // for — but every fresh click must land, including a second one from a chat
  // window still open beside this page, and one aimed at a different agent.
  useEffect(() => {
    if (!navigation || !conversationStartersSupported) return;

    if (navigation.searchParams.get("focus") !== AGENT_FOCUS_CONVERSATION_STARTERS) {
      // The param is gone: either this is an ordinary visit, or the `replace`
      // below has landed. Re-arm so the next click focuses again.
      focusHandledForAgentRef.current = null;
      return;
    }
    // Guard against re-running before the stripped URL arrives — the adapter
    // object is not referentially stable, so this effect re-runs on renders
    // the `replace` below itself causes. Keyed by agent so a deep link aimed
    // at a DIFFERENT agent is never swallowed by this latch.
    if (focusHandledForAgentRef.current === agent.id) return;
    focusHandledForAgentRef.current = agent.id;

    // `block: "nearest"` deliberately: native scrollIntoView scrolls every
    // scrollable ancestor, which on desktop drags the shell itself (#3929).
    conversationStartersRef.current?.scrollIntoView?.({ block: "nearest" });
    setConversationStartersFocused(true);

    const params = new URLSearchParams(navigation.searchParams);
    params.delete("focus");
    const query = params.toString();
    navigation.replace(
      query ? `${navigation.pathname}?${query}` : navigation.pathname,
    );

    // The timer is held in a ref, NOT returned as this effect's cleanup: the
    // adapter object is unstable, so a cleanup would be invoked on the very
    // next render and cancel the flash before it ever ended, leaving the ring
    // on permanently. Unmount clears it below. A repeat focus restarts the
    // window rather than inheriting the previous one's remaining time.
    if (focusFlashTimerRef.current) clearTimeout(focusFlashTimerRef.current);
    focusFlashTimerRef.current = setTimeout(
      () => setConversationStartersFocused(false),
      FOCUS_FLASH_MS,
    );
  }, [agent.id, navigation, conversationStartersSupported]);

  useEffect(
    () => () => {
      if (focusFlashTimerRef.current) clearTimeout(focusFlashTimerRef.current);
    },
    [],
  );

  // Report dirty state up so the parent can guard tab switches.
  useEffect(() => {
    onDirtyChange?.(isDirty);
  }, [isDirty, onDirtyChange]);

  const handleSave = async () => {
    setSaving(true);
    try {
      await onSave({
        instructions: value,
        ...(conversationStartersSupported
          ? { conversation_starters: conversationStarters }
          : {}),
      });
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
                : t(($) => $.specialization.inherited_prompt_unavailable)}
            </p>
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

      <div className="space-y-2">
        <label
          htmlFor={`agent-system-prompt-${agent.id}`}
          className="text-body font-medium"
        >
          {isSpecializationAgent
            ? t(($) => $.specialization.own_prompt_label)
            : hasSystemLayer
              ? t(($) => $.tab_body.instructions.workspace_notes_label)
              : t(($) => $.tab_body.instructions.system_prompt_label)}
        </label>
        <Textarea
          id={`agent-system-prompt-${agent.id}`}
          name="agent-system-prompt"
          autoComplete="off"
          value={value}
          onChange={(event) => setValue(event.target.value)}
          placeholder={
            isSpecializationAgent
              ? t(($) => $.specialization.own_prompt_placeholder)
              : hasSystemLayer
                ? t(($) => $.tab_body.instructions.workspace_notes_placeholder)
                : t(($) => $.tab_body.instructions.placeholder)
          }
          rows={18}
          className="min-h-96 resize-y leading-6"
        />
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
            (effectivePrompt ? (
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

      {conversationStartersSupported ? (
        <div
          ref={conversationStartersRef}
          className={cn(
            "scroll-mt-4 rounded-lg transition-shadow duration-500",
            conversationStartersFocused && "ring-2 ring-brand/50 ring-offset-2 ring-offset-background",
          )}
        >
          <ConversationStartersEditor
            value={conversationStarters}
            onChange={setConversationStarters}
          />
        </div>
      ) : null}

      <div className="flex items-center justify-end gap-3">
        {isDirty && (
          <span className="text-caption text-muted-foreground">
            {t(($) => $.tab_body.common.unsaved_changes)}
          </span>
        )}
        <Button
          size="sm"
          onClick={handleSave}
          disabled={!isDirty || !conversationStartersValid || saving}
        >
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
  );
}
