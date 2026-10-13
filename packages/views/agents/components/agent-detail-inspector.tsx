"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import type {
  Agent,
  AgentRuntime,
  MemberWithUser,
} from "@multica/core/types";
import {
  AGENT_DESCRIPTION_MAX_LENGTH,
  isAgentWorkEnabled,
} from "@multica/core/agents";
import {
  isRuntimeUsableForUser,
  runtimeModelsOptions,
} from "@multica/core/runtimes";
import { Input } from "@multica/ui/components/ui/input";
import { Switch } from "@multica/ui/components/ui/switch";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { AvatarUploadControl } from "../../common/avatar-upload-control";
import {
  SettingsCard,
  SettingsRow,
  SettingsSaveState,
  SettingsSection,
} from "../../settings/components/settings-layout";
import { useAutoSave } from "../../settings/components/use-auto-save";
import { useT } from "../../i18n";
import { CharCounter } from "./char-counter";
import { ModelPicker } from "./inspector/model-picker";
import {
  buildModelChangeUpdate,
  type ModelCatalog,
} from "./inspector/model-change-cleanup";
import { RuntimePicker } from "./inspector/runtime-picker";
import {
  canEditRuntimeProfile,
  runtimeInheritanceState,
} from "../specialization";

interface InspectorProps {
  agent: Agent;
  runtime: AgentRuntime | null;
  runtimes: AgentRuntime[];
  members: MemberWithUser[];
  currentUserId: string | null;
  canEdit: boolean;
  onUpdate: (id: string, data: Record<string, unknown>) => Promise<void>;
}

interface ProfileDraft {
  name: string;
  description: string;
}

function profileDraftsEqual(left: ProfileDraft, right: ProfileDraft) {
  return left.name === right.name && left.description === right.description;
}

/**
 * Full-width General settings form. Every editable value is presented as an
 * explicit field; compact inspector chips are used only through their
 * settings-field variants, where the whole control is a visible click target.
 *
 * DENE-2009: only what a person must decide is shown. Series, following the
 * base role, fallback runtime, thinking, speed, concurrency, auto-retry and
 * the routing seat run on system defaults (or the saved value) and stay
 * changeable through `multica agent update`.
 */
export function AgentDetailInspector({
  agent,
  runtime,
  runtimes,
  members,
  currentUserId,
  canEdit,
  onUpdate,
}: InspectorProps) {
  const { t } = useT("agents");
  const { t: ts } = useT("settings");
  const update = useCallback(
    (data: Record<string, unknown>) => onUpdate(agent.id, data),
    [agent.id, onUpdate],
  );

  const [name, setName] = useState(agent.name);
  const [description, setDescription] = useState(agent.description ?? "");

  useEffect(() => {
    setName(agent.name);
    setDescription(agent.description ?? "");
    // Reset only when moving to another agent. Cache updates from this form
    // must not erase a newer local draft while an autosave is in flight.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [agent.id]);

  const profileDraft = useMemo(
    () => ({ name: name.trim(), description }),
    [description, name],
  );
  const savedProfile = useMemo(
    () => ({
      name: agent.name,
      description: agent.description ?? "",
    }),
    [agent.description, agent.name],
  );
  const saveProfile = useCallback(
    async (next: ProfileDraft) => {
      await update({ name: next.name, description: next.description });
    },
    [update],
  );
  const profileAutoSave = useAutoSave({
    value: profileDraft,
    savedValue: savedProfile,
    onSave: saveProfile,
    enabled:
      canEdit &&
      profileDraft.name.length > 0 &&
      profileDraft.description.length <= AGENT_DESCRIPTION_MAX_LENGTH,
    isEqual: profileDraftsEqual,
  });

  const isOnline = runtime?.status === "online";
  const canReadRuntime =
    runtime != null && isRuntimeUsableForUser(runtime, currentUserId);
  const canDiscoverRuntimeModels = isOnline && canReadRuntime;
  const nameInvalid = name.trim().length === 0;

  // Runtime inheritance (DENE-505): while a specialisation follows its base
  // role the runtime and model stay visible but read-only.
  const runtimeInherited = runtimeInheritanceState(agent) === "inherited";
  const canEditRuntime = canEditRuntimeProfile(agent, canEdit);

  // Same query the Thinking / Speed fields already use, so switching model
  // costs no extra request. `null` = not authoritative (offline runtime, still
  // loading, or discovery failed) and must not trigger any clearing.
  const modelsQuery = useQuery(
    runtimeModelsOptions(
      canDiscoverRuntimeModels ? agent.runtime_id : null,
      agent.id,
    ),
  );
  const modelCatalog = useMemo<ModelCatalog>(
    () =>
      modelsQuery.isSuccess
        ? modelsQuery.data.supported
          ? modelsQuery.data.models
          : []
        : null,
    [modelsQuery.data, modelsQuery.isSuccess],
  );
  const handleModelChange = useCallback(
    (model: string) =>
      update(
        buildModelChangeUpdate({
          provider: runtime?.provider ?? "",
          model,
          thinkingLevel: agent.thinking_level ?? "",
          serviceTier: agent.service_tier ?? "",
          catalog: modelCatalog,
        }),
      ),
    [agent.service_tier, agent.thinking_level, modelCatalog, runtime?.provider, update],
  );

  return (
    <div className="space-y-8">
      <SettingsSection
        title={t(($) => $.inspector.section_profile)}
        action={
          <SettingsSaveState
            status={profileAutoSave.status}
            savingLabel={ts(($) => $.auto_save.saving)}
            savedLabel={ts(($) => $.auto_save.saved)}
            errorLabel={ts(($) => $.auto_save.failed)}
          />
        }
      >
        <SettingsCard>
          <SettingsRow
            label={t(($) => $.inspector.avatar_label)}
            size="none"
          >
            <div className="flex justify-start sm:justify-end">
              <AvatarUploadControl
                variant="agent"
                value={agent.avatar_url ?? null}
                name={agent.name}
                size={56}
                disabled={!canEdit}
                onUploaded={(url) => update({ avatar_url: url })}
                onEmojiSelected={(value) => update({ avatar_url: value })}
              />
            </div>
          </SettingsRow>

          <SettingsRow
            label={t(($) => $.inspector.name_label)}
            size="text"
          >
            <div>
              <Input
                type="text"
                name="agent-name"
                autoComplete="off"
                aria-label={t(($) => $.inspector.name_label)}
                value={name}
                onChange={(event) => setName(event.target.value)}
                onBlur={profileAutoSave.flush}
                disabled={!canEdit}
                aria-invalid={nameInvalid || undefined}
              />
              {nameInvalid ? (
                <p className="mt-1 text-caption text-destructive">
                  {t(($) => $.inspector.rename_required)}
                </p>
              ) : null}
            </div>
          </SettingsRow>

          <SettingsRow
            label={t(($) => $.inspector.description_label)}
            size="text"
            align="start"
          >
            <div>
              <Textarea
                name="agent-description"
                autoComplete="off"
                aria-label={t(($) => $.inspector.description_label)}
                value={description}
                onChange={(event) => setDescription(event.target.value)}
                onBlur={profileAutoSave.flush}
                disabled={!canEdit}
                rows={5}
                maxLength={AGENT_DESCRIPTION_MAX_LENGTH}
                className="resize-y"
                placeholder={t(($) => $.inspector.description_placeholder)}
              />
              <CharCounter
                length={[...description].length}
                max={AGENT_DESCRIPTION_MAX_LENGTH}
              />
            </div>
          </SettingsRow>
        </SettingsCard>
      </SettingsSection>

      <SettingsSection
        title={t(($) => $.inspector.section_execution)}
      >
        <SettingsCard>
          <SettingsRow
            label={t(($) => $.inspector.prop_runtime)}
            description={
              runtimeInherited ? t(($) => $.inspector.prop_runtime_inherit_hint_on) : undefined
            }
            size="select-wide"
          >
            <RuntimePicker
              variant="field"
              showLabel={false}
              value={agent.runtime_id}
              runtimes={runtimes}
              members={members}
              currentUserId={currentUserId}
              canEdit={canEditRuntime}
              // Model, thinking level, and service tier are runtime/model
              // native. Clear them together so the new runtime resolves its
              // own defaults instead of inheriting incompatible tokens.
              onChange={(id) =>
                update({
                  runtime_id: id,
                  model: "",
                  thinking_level: "",
                  service_tier: "",
                })
              }
            />
          </SettingsRow>
          <SettingsRow
            label={t(($) => $.inspector.prop_model)}
            size="select-wide"
          >
            <ModelPicker
              variant="field"
              showLabel={false}
              runtimeId={agent.runtime_id}
              runtimeOnline={canDiscoverRuntimeModels}
              agentId={agent.id}
              value={agent.model ?? ""}
              canEdit={canEditRuntime}
              onChange={handleModelChange}
            />
          </SettingsRow>
          <SettingsRow
            label={t(($) => $.inspector.prop_work_enabled)}
            description={
              isAgentWorkEnabled(agent) || !agent.work_pause
                ? t(($) => $.inspector.prop_work_enabled_hint)
                : agent.work_pause.reason === "balance_exhausted"
                  ? t(($) => $.inspector.prop_work_pause_balance)
                  : agent.work_pause.reason === "auth_failure"
                    ? t(($) => $.inspector.prop_work_pause_auth)
                    : t(($) => $.inspector.prop_work_pause_quota)
            }
          >
            <Switch
              checked={isAgentWorkEnabled(agent)}
              disabled={!canEdit}
              onCheckedChange={(checked) => {
                void update({ work_enabled: checked });
              }}
              aria-label={t(($) => $.inspector.prop_work_enabled)}
            />
          </SettingsRow>
        </SettingsCard>
      </SettingsSection>

    </div>
  );
}
